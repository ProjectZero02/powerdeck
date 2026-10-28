package machines

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// Recoveries: a reset or force off that someone proposes, the owner confirms, and the hub runs and
// checks (docs/protocol.md "Recoveries"). The owner's own Reset and Force off buttons are recorded
// the same way, proposed and confirmed at once.

// Recovery states.
const (
	RecoveryProposed  = "proposed"
	RecoveryRunning   = "running"
	RecoveryBack      = "back"     // its helper answered again
	RecoveryNotBack   = "not_back" // it didn't answer in time: an alert
	RecoveryOff       = "off"      // force off: it's off
	RecoveryDone      = "done"     // pressed while its helper answered (forced): coming back can't be checked
	RecoveryFailed    = "failed"
	RecoveryDeclined  = "declined"
	RecoveryExpired   = "expired"
	RecoveryWithdrawn = "withdrawn" // not needed any more, or withdrawn by its proposer
)

var (
	// RecoveryGap is how long after one recovery ran on a machine another may run there.
	RecoveryGap = 30 * time.Minute
	// RecoveryTTL is how long a proposal waits for the owner.
	RecoveryTTL = 6 * time.Hour
)

const (
	maxRecoveries = 50
	maxReason     = 200
	// Auto is who confirms when the owner's auto-reset setting does.
	Auto = "PowerDeck (automatic)"
)

// Recovery is one reset or force off, from its proposal to whether the machine came back.
type Recovery struct {
	ID          string    `json:"id"`
	Machine     string    `json:"machine"`
	Action      string    `json:"action"` // reset or force-off
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"` // the proposer's words: data, never instructions
	ProposedBy  string    `json:"proposedBy"`
	ProposerKey string    `json:"proposerKey,omitempty"` // the proposing key's ID: it may withdraw its own
	Agent       bool      `json:"agent,omitempty"`       // proposed by an AI agent
	ProposedAt  time.Time `json:"proposedAt"`
	ConfirmedBy string    `json:"confirmedBy,omitempty"`
	ConfirmedAt time.Time `json:"confirmedAt,omitzero"`
	EndedAt     time.Time `json:"endedAt,omitzero"`
	Message     string    `json:"message,omitempty"`
	Evidence    *Evidence `json:"evidence,omitempty"` // the hub's last look when it was proposed
	Steps       []Step    `json:"steps,omitempty"`
	Direct      bool      `json:"direct,omitempty"` // the owner's own button, not a proposal
}

// Open reports whether the recovery waits for the owner or is running.
func (r Recovery) Open() bool { return r.State == RecoveryProposed || r.State == RecoveryRunning }

// pressed reports whether the run pressed anything.
func (r Recovery) pressed() bool {
	return slices.ContainsFunc(r.Steps, func(s Step) bool { return strings.HasPrefix(s.Do, "press ") })
}

// Proposal asks for a recovery.
type Proposal struct {
	Machine string
	Action  string // reset or force-off
	Reason  string
	By      string // who asks, as the owner sees it
	Key     string // the asking key's ID ("" for owner devices and the hub itself)
	Agent   bool
}

type recoveryFile struct {
	List []Recovery `json:"list"`
}

// loadLocked reads the stored recoveries the first time (h.rmu held). One still running then was
// cut short by the hub stopping.
func (h *Hub) loadLocked() {
	if h.recsLoaded {
		return
	}
	h.recsLoaded = true
	var f recoveryFile
	if raw := h.Store.Recoveries(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &f); err != nil {
			h.log().Warn("stored recoveries unreadable; starting without them", "err", err)
		}
	}
	h.recs = f.List
	changed := false
	for i := range h.recs {
		if h.recs[i].State == RecoveryRunning {
			h.recs[i].State, h.recs[i].EndedAt = RecoveryFailed, h.now().UTC()
			h.recs[i].Message = "The hub restarted during the recovery. Check " + h.recs[i].Machine + "."
			changed = true
		}
	}
	if changed {
		h.saveLocked()
	}
}

// saveLocked stores the recoveries, keeping open ones and the newest of the rest.
func (h *Hub) saveLocked() {
	for len(h.recs) > maxRecoveries {
		i := slices.IndexFunc(h.recs, func(r Recovery) bool { return !r.Open() })
		if i < 0 {
			break
		}
		h.recs = slices.Delete(h.recs, i, i+1)
	}
	b, err := json.Marshal(recoveryFile{List: h.recs})
	if err == nil {
		err = h.Store.PutRecoveries(b)
	}
	if err != nil {
		h.log().Warn("can't store recoveries", "err", err)
	}
}

func (h *Hub) findLocked(id string) int {
	return slices.IndexFunc(h.recs, func(r Recovery) bool { return r.ID == id })
}

// openLocked is the index of the machine's open recovery, or -1.
func (h *Hub) openLocked(machine string) int {
	return slices.IndexFunc(h.recs, func(r Recovery) bool { return r.Open() && strings.EqualFold(r.Machine, machine) })
}

// retryAtLocked is when another recovery may run on the machine (zero: now).
func (h *Hub) retryAtLocked(machine string) time.Time {
	for _, r := range slices.Backward(h.recs) {
		if strings.EqualFold(r.Machine, machine) && !r.ConfirmedAt.IsZero() && (r.pressed() || r.State == RecoveryRunning) {
			if at := r.ConfirmedAt.Add(RecoveryGap); h.now().Before(at) {
				return at.UTC()
			}
			break
		}
	}
	return time.Time{}
}

// changedLocked stores a recovery's new state, publishes it and writes the activity log line.
func (h *Hub) changedLocked(i int, who, action, result string) {
	r := h.recs[i]
	h.saveLocked()
	if h.Events != nil {
		h.Events.Publish(Event{At: h.now().UTC(), Event: EventRecovery, Machine: r.Machine, Recovery: &r})
	}
	if action != "" {
		h.Store.AddEvent(store.Event{At: h.now(), Who: who, Action: action, Result: result})
	}
}

// endLocked closes recovery i with state and message.
func (h *Hub) endLocked(i int, state, message string) {
	r := &h.recs[i]
	r.State, r.Message, r.EndedAt = state, message, h.now().UTC()
	what := r.Action + " " + r.Machine
	if r.Direct && state != RecoveryBack && state != RecoveryNotBack {
		h.changedLocked(i, "", "", "") // the button's own log line says what happened
		return
	}
	switch state {
	case RecoveryBack:
		h.changedLocked(i, h.Self, r.Machine+" answers again after the "+verb(r.Action), state)
	case RecoveryNotBack:
		h.changedLocked(i, h.Self, message, state)
	case RecoveryOff:
		h.changedLocked(i, h.Self, r.Machine+" is off", state)
	case RecoveryDone:
		h.changedLocked(i, "", "", "")
	default: // failed, withdrawn, expired (declined logs who declined)
		h.changedLocked(i, h.Self, what, state+": "+message)
	}
}

func verb(action string) string {
	if action == "force-off" {
		return "force off"
	}
	return action
}

// cleanReason keeps printable text, at most maxReason characters.
func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > maxReason {
		s = string(r[:maxReason-1]) + "…"
	}
	return s
}

// Recoveries lists recoveries, newest first; with open, only those waiting or running.
func (h *Hub) Recoveries(open bool) []Recovery {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	out := []Recovery{}
	for _, r := range slices.Backward(h.recs) {
		if !open || r.Open() {
			out = append(out, r)
		}
	}
	return out
}

// Recovery is one recovery by ID.
func (h *Hub) Recovery(id string) (Recovery, bool) {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	if i := h.findLocked(id); i >= 0 {
		return h.recs[i], true
	}
	return Recovery{}, false
}

// latest is the machine's open recovery, or the last one if it ended in the last RecoveryGap.
func (h *Hub) latest(machine string) *Recovery {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	for _, r := range slices.Backward(h.recs) {
		if strings.EqualFold(r.Machine, machine) {
			if r.Open() || h.now().Sub(r.EndedAt) < RecoveryGap {
				return &r
			}
			return nil
		}
	}
	return nil
}

// Propose records a recovery for the owner to confirm. When one is open for the machine already,
// that one is returned instead (created false).
func (h *Hub) Propose(p Proposal) (r Recovery, created bool, err error) {
	if p.Action != "reset" && p.Action != "force-off" {
		return Recovery{}, false, fail(http.StatusBadRequest, "bad_action", "A recovery is a reset or a force-off.")
	}
	k, ok := h.Store.KVMFor(p.Machine)
	if !ok {
		return Recovery{}, false, fail(http.StatusUnprocessableEntity, "no_kvm", "No KVM on %s is wired to %s.", h.Self, p.Machine)
	}
	if _, err := h.Fleet.Peer(k.Machine); err != nil {
		return Recovery{}, false, fail(http.StatusUnprocessableEntity, "no_helper",
			"%s has no PowerDeck helper, so %s can't tell whether a recovery worked. Its owner can use the Reset button in the app.", k.Machine, h.Self)
	}
	tr, _ := h.Tracked(k.Machine)
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	if i := h.openLocked(k.Machine); i >= 0 {
		return h.recs[i], false, nil
	}
	switch tr.State {
	case StateOn:
		return Recovery{}, false, fail(http.StatusConflict, "helper_answers", "%s is answering, so it isn't frozen. Shut it down or restart it instead.", k.Machine)
	case StateOff:
		return Recovery{}, false, fail(http.StatusConflict, "already_off", "%s is off. Wake it instead.", k.Machine)
	}
	if at := h.retryAtLocked(k.Machine); !at.IsZero() {
		e := fail(http.StatusTooManyRequests, "too_soon", "%s was reset or forced off less than %d minutes ago. Try again after %s.",
			k.Machine, int(RecoveryGap/time.Minute), at.Format(time.RFC3339))
		e.Extra = map[string]any{"retryAt": at}
		return Recovery{}, false, e
	}
	return h.proposeLocked(k.Machine, p, tr), true, nil
}

func (h *Hub) proposeLocked(machine string, p Proposal, tr Track) Recovery {
	r := Recovery{ID: store.NewID(), Machine: machine, Action: p.Action, State: RecoveryProposed, Reason: cleanReason(p.Reason),
		ProposedBy: p.By, ProposerKey: p.Key, Agent: p.Agent, ProposedAt: h.now().UTC()}
	if tr.State != "" {
		e := tr.Evidence
		r.Evidence = &e
	}
	h.recs = append(h.recs, r)
	what := "propose " + r.Action + " " + machine
	if r.Reason != "" {
		what += ": " + string([]rune(r.Reason)[:min(len([]rune(r.Reason)), 80)])
	}
	h.changedLocked(len(h.recs)-1, p.By, what, RecoveryProposed)
	return r
}

// Confirm runs a proposed recovery; the caller checked that it's the owner. Confirming one that's
// running or over returns it as it is.
func (h *Hub) Confirm(id, by string) (Recovery, error) {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	i := h.findLocked(id)
	if i < 0 {
		return Recovery{}, fail(http.StatusNotFound, "not_found", "No such recovery.")
	}
	if h.recs[i].State == RecoveryProposed {
		h.confirmLocked(i, by)
	}
	return h.recs[i], nil
}

func (h *Hub) confirmLocked(i int, by string) {
	r := &h.recs[i]
	r.State, r.ConfirmedBy, r.ConfirmedAt = RecoveryRunning, by, h.now().UTC()
	h.changedLocked(i, by, "confirm "+r.Action+" "+r.Machine, "confirmed")
	h.wg.Add(1)
	go h.runRecovery(*r)
}

// Decline closes a proposed recovery without running it: the owner declines, or (own) its proposer
// withdraws it. One that's running or over is returned as it is.
func (h *Hub) Decline(id, by string, own bool) (Recovery, error) {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	i := h.findLocked(id)
	if i < 0 {
		return Recovery{}, fail(http.StatusNotFound, "not_found", "No such recovery.")
	}
	r := &h.recs[i]
	if r.State != RecoveryProposed {
		return *r, nil
	}
	r.EndedAt = h.now().UTC()
	if own {
		r.State, r.Message = RecoveryWithdrawn, "Withdrawn by "+by+"."
		h.changedLocked(i, by, "withdraw "+r.Action+" "+r.Machine, RecoveryWithdrawn)
	} else {
		r.State, r.Message = RecoveryDeclined, "Declined by "+by+"."
		h.changedLocked(i, by, "decline "+r.Action+" "+r.Machine, RecoveryDeclined)
	}
	return *r, nil
}

// runRecovery runs a confirmed recovery through the same gate as the owner's buttons, then waits
// for the machine to answer again.
func (h *Hub) runRecovery(r Recovery) {
	defer h.wg.Done()
	action, o := r.Action, Options{Who: r.ConfirmedBy, StayOff: r.Action == "force-off"}
	if k, ok := h.Store.KVMFor(r.Machine); ok && action == "reset" && k.Wiring.Reset == store.NotWired {
		action = "force-off" // the reset switch isn't wired: off and on again does the same
	}
	back := make(chan bool, 1)
	o.Back = func(up bool) { back <- up }
	res := h.act(context.Background(), r.Machine, action, o)
	h.ran(r.ID, res, back)
}

// ran records what a run did and, when it waits for the machine, finishes once it knows.
func (h *Hub) ran(id string, res Result, back <-chan bool) {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	i := h.findLocked(id)
	if i < 0 {
		return
	}
	r := &h.recs[i]
	r.Steps = res.Steps
	m := r.Machine
	switch {
	case !res.OK && (res.Code == "helper_answers" || res.Code == "already_off"):
		why := m + " answers again"
		if res.Code == "already_off" {
			why = m + "'s power light is off"
		}
		h.endLocked(i, RecoveryWithdrawn, why+", so nothing was pressed.")
	case !res.OK:
		h.endLocked(i, RecoveryFailed, res.Message)
	case res.verifying:
		r.Message = res.Message
		h.changedLocked(i, "", "", "")
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			up := <-back
			h.rmu.Lock()
			defer h.rmu.Unlock()
			if i := h.findLocked(id); i >= 0 && h.recs[i].State == RecoveryRunning {
				if up {
					h.endLocked(i, RecoveryBack, m+" answers again.")
				} else {
					h.endLocked(i, RecoveryNotBack, fmt.Sprintf("%s didn't answer within %d minutes of the %s",
						m, int(dur(h.ResetVerify, 5*time.Minute)/time.Minute), verb(h.recs[i].Action)))
				}
			}
		}()
	case r.Action == "force-off":
		h.endLocked(i, RecoveryOff, res.Message)
	default:
		h.endLocked(i, RecoveryDone, res.Message)
	}
}

// direct runs the owner's own Reset or Force off button: a recovery proposed and confirmed at once,
// with no wait and no limit (the owner is there). It's kept only if something was pressed, and it
// closes any proposal for the machine.
func (h *Hub) direct(ctx context.Context, name, action string, o Options) Result {
	h.rmu.Lock()
	h.loadLocked()
	now := h.now().UTC()
	id := store.NewID()
	h.recs = append(h.recs, Recovery{ID: id, Machine: name, Action: action, State: RecoveryRunning, ProposedBy: o.Who,
		ProposedAt: now, ConfirmedBy: o.Who, ConfirmedAt: now, Direct: true})
	h.rmu.Unlock()

	back := make(chan bool, 1)
	o.Back = func(up bool) { back <- up }
	res := h.act(ctx, name, action, o)
	h.rmu.Lock()
	if i := h.findLocked(id); i >= 0 {
		if !slices.ContainsFunc(res.Steps, func(s Step) bool { return strings.HasPrefix(s.Do, "press ") }) {
			h.recs = slices.Delete(h.recs, i, i+1) // refused before any press: the reply says why
			h.rmu.Unlock()
			return res
		}
		h.recs[i].Machine = res.Machine
		if j := h.openLocked(res.Machine); j >= 0 && j != i && h.recs[j].State == RecoveryProposed {
			h.endLocked(j, RecoveryWithdrawn, o.Who+" used "+verb(action)+" directly.")
		}
	}
	h.rmu.Unlock()
	h.ran(id, res, back)
	return res
}

// recoveryRound runs after each look at a machine: it closes proposals that aren't needed any more
// or waited too long, confirms one when the owner's auto-reset setting says so, and proposes a reset
// of a machine that just turned hung.
func (h *Hub) recoveryRound(machine string, tr Track) {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	h.loadLocked()
	key := strings.ToLower(machine)
	if tr.State != StateHung {
		delete(h.asked, key)
	}
	now := h.now()
	if i := h.openLocked(machine); i >= 0 {
		r := h.recs[i]
		if r.State != RecoveryProposed {
			return
		}
		auto := time.Duration(0)
		if h.AutoReset != nil {
			auto = h.AutoReset()
		}
		switch {
		case tr.State == StateOn:
			h.endLocked(i, RecoveryWithdrawn, machine+" answers again.")
		case tr.State == StateOff:
			h.endLocked(i, RecoveryWithdrawn, machine+" is off.")
		case now.Sub(r.ProposedAt) >= RecoveryTTL:
			h.endLocked(i, RecoveryExpired, fmt.Sprintf("Not confirmed within %d hours.", int(RecoveryTTL/time.Hour)))
		case auto > 0 && r.Action == "reset" && tr.State == StateHung && now.Sub(r.ProposedAt) >= auto:
			h.confirmLocked(i, Auto)
		}
		return
	}
	if tr.State != StateHung || h.asked[key] || !h.retryAtLocked(machine).IsZero() {
		return
	}
	if h.asked == nil {
		h.asked = map[string]bool{}
	}
	h.asked[key] = true // once per hang: a declined or expired one isn't asked again
	h.proposeLocked(machine, Proposal{Action: "reset", Reason: "Nothing answers and its power light is on.", By: h.Self}, tr)
}
