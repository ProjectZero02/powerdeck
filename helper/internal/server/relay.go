package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/discovery"
	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

type discoveryPeer = discovery.Peer

var actionScope = map[string]string{
	"status": store.ScopeStatus, "wake": store.ScopeWake,
	"shutdown": store.ScopePower, "restart": store.ScopePower, "cancel": store.ScopePower,
	// Through a KVM on this helper. Owner devices only; anyone else proposes a recovery.
	"reset": store.ScopeForce, "force-off": store.ScopeForce,
}

func (s *server) relay(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	res, e := s.act(r.Context(), c, r.PathValue("name"), r.PathValue("action"))
	if e != nil {
		failErr(w, e)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// act runs one device action for a caller: on this device, or relayed to another helper. It's
// shared by the REST relay, MCP and schedules, so the same rules apply everywhere.
func (s *server) act(ctx context.Context, c auth.Caller, device, action string) (map[string]any, *apiError) {
	return s.actWith(ctx, c, device, action, machines.Options{})
}

// actWith is act with options for actions through a KVM (force, stayOff).
func (s *server) actWith(ctx context.Context, c auth.Caller, device, action string, o machines.Options) (map[string]any, *apiError) {
	scope, ok := actionScope[action]
	if !ok {
		return nil, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "Unknown action " + action + "."}
	}
	recovery := action == "reset" || action == "force-off"
	if recovery && (c.Kind != auth.Owner || c.ForAgent()) { // an agent relayed by a hub is still an agent
		return nil, &apiError{Status: http.StatusForbidden, Code: "owner_only",
			Message: "Only the owner's devices can reset a machine or force it off. Propose it instead (POST /v2/machines/" + device + "/recoveries): the owner confirms."}
	}
	if !c.Has(scope) {
		return nil, &apiError{Status: http.StatusForbidden, Code: "scope", Message: "This key isn't allowed to " + action + "."}
	}
	if device == "" || fleet.IsSelf(device, s.Name) {
		device = s.Name
	}
	// A peer may be asked for by its Tailscale name or its own name: use one name from here on,
	// so key limits, the quota and the "not your own machine" rule can't be dodged with the other.
	var names []string
	if device != s.Name {
		if p, err := s.Fleet.Peer(device); err == nil {
			device, names = p.Name, peerNames(p)
		}
	} else {
		names = []string{s.Name}
	}
	if !mayUseAny(c, append(names, device)) {
		return nil, &apiError{Status: http.StatusForbidden, Code: "device", Message: "This key can't control " + device + "."}
	}
	// A key from a pairing code is for this one device; only owners and agents reach the rest.
	if device != s.Name && c.Kind != auth.Owner && !c.IsAutomation() {
		return nil, &apiError{Status: http.StatusForbidden, Code: "device", Message: "This key can only control " + s.Name + "."}
	}
	if c.ForAgent() && action == "wake" {
		if e := s.agentsAllowed(ctx, device, false); e != nil {
			return nil, e
		}
	}
	if c.ForAgent() {
		ctx = peer.ForAgent(ctx) // the device checks its own switch too
	}
	if device == s.Name {
		switch action {
		case "status":
			return s.statusBody(ctx, c), nil
		case "wake":
			return map[string]any{"online": true, "message": device + " is already on."}, nil
		case "reset", "force-off":
			return nil, &apiError{Status: http.StatusUnprocessableEntity, Code: "self", Message: device + " can't " + action + " itself."}
		default:
			return s.localPower(ctx, c, action, powerRequest{})
		}
	}
	kvm := s.Machines.HasKVM(device)
	if recovery && !kvm {
		return nil, &apiError{Status: http.StatusUnprocessableEntity, Code: "no_kvm", Message: "No KVM on " + s.Name + " is wired to " + device + "."}
	}
	o.Who, o.Agent = c.Name, c.ForAgent()
	switch action {
	case "wake":
		if c.IsAutomation() {
			if e := s.quota.take(c.Key.ID+"/"+strings.ToLower(device), kindWake); e != nil {
				return nil, e
			}
		}
		if kvm {
			return s.hubAct(ctx, c, device, action, o)
		}
		sent, err := s.Fleet.Wake(device)
		if err != nil {
			return nil, fleetError(device, err)
		}
		s.logEvent(c.Name, "wake "+device, "sent")
		return map[string]any{"sentOn": sent, "message": "Wake signal sent to " + device + ". It usually takes 30–90 seconds."}, nil
	case "status":
		var st map[string]any
		if err := s.Fleet.Forward(ctx, device, "GET", "/v2/status", nil, &st, c.Name); err != nil {
			if errors.Is(err, fleet.ErrOffline) {
				p, _ := s.Fleet.Peer(device)
				return map[string]any{"name": device, "online": false, "lastSeen": p.LastSeen}, nil
			}
			return nil, fleetError(device, err)
		}
		st["online"] = true
		return st, nil
	default:
		if c.IsAutomation() && !c.Key.AllowSelf && c.Node != "" && slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, c.Node) }) {
			return nil, &apiError{Status: http.StatusForbidden, Code: "self",
				Message: "An agent can't shut down or restart the device it runs on."}
		}
		if c.ForAgent() && action != "cancel" {
			if e := s.agentsAllowed(ctx, device, true); e != nil {
				return nil, e
			}
		}
		if c.IsAutomation() && action != "cancel" {
			if e := s.quota.take(c.Key.ID+"/"+strings.ToLower(device), kindPower); e != nil {
				return nil, e
			}
		}
		if kvm && action != "cancel" {
			return s.hubAct(ctx, c, device, action, o)
		}
		var res map[string]any
		err := s.Fleet.Forward(ctx, device, "POST", "/v2/power/"+action, map[string]any{"delaySeconds": 60}, &res, c.Name)
		if err != nil {
			return nil, fleetError(device, err)
		}
		s.logEvent(c.Name, action+" "+device, "scheduled")
		return res, nil
	}
}

// agentsAllowed refuses an agent when the device turns agents away. With fresh, another helper's
// setting is asked for first (before a shutdown or restart); otherwise, as when it's off, what it
// said last time it was seen counts. Cancelling is always allowed.
func (s *server) agentsAllowed(ctx context.Context, device string, fresh bool) *apiError {
	off := &apiError{Status: http.StatusForbidden, Code: "agents_off",
		Message: device + " doesn't let AI agents turn it on or off. Its owner can allow it in the PowerDeck app."}
	if device == s.Name {
		if s.Store.Settings().NoAgents {
			return off
		}
		return nil
	}
	p, err := s.Fleet.Peer(device)
	if err != nil {
		return nil // unknown devices fail later with a clearer error
	}
	if fresh {
		var st struct {
			Agents *struct {
				Allowed bool `json:"allowed"`
			} `json:"agents"`
		}
		if err := s.Fleet.Forward(ctx, device, "GET", "/v2/status", nil, &st, ""); err == nil {
			if st.Agents != nil && !st.Agents.Allowed {
				return off
			}
			return nil
		}
	}
	if p.NoAgents {
		return off
	}
	return nil
}

// peerNames lists every name a remembered helper goes by.
func peerNames(p store.Peer) []string {
	out := []string{p.Name}
	if p.Device != "" {
		out = append(out, p.Device)
	}
	if label, _, _ := strings.Cut(p.DNSName, "."); label != "" {
		out = append(out, label)
	}
	return out
}

func mayUseAny(c auth.Caller, names []string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return n != "" && c.MayUse(n) })
}

func fleetError(device string, err error) *apiError {
	var pe *peer.Error
	switch {
	case errors.As(err, &pe):
		e := &apiError{Status: pe.Status, Code: pe.Code, Message: device + ": " + pe.Message}
		switch {
		case pe.Code == "in_use" || pe.Code == "pending":
			e.Status = http.StatusConflict
		case pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden:
			// The device refused this helper, not the caller: don't look like the caller's key failed.
			return &apiError{Status: http.StatusBadGateway, Code: "peer_refused",
				Message: device + " doesn't accept this helper. Open the PowerDeck app once to link them again."}
		}
		return e
	case errors.Is(err, fleet.ErrUnknownDevice):
		return &apiError{Status: http.StatusNotFound, Code: "unknown_device", Message: "This helper doesn't know a device called " + device + "."}
	case errors.Is(err, fleet.ErrOffline):
		return &apiError{Status: http.StatusBadGateway, Code: "offline", Message: device + " isn't answering (it may be off)."}
	case errors.Is(err, fleet.ErrNoWaker), errors.Is(err, fleet.ErrNoMAC):
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "cant_wake", Message: device + ": " + err.Error()}
	default:
		return &apiError{Status: http.StatusBadGateway, Code: "relay_failed", Message: device + ": " + err.Error()}
	}
}

// ---- limits ----

// limiter blocks an address for 10 minutes after 10 failures within 10 minutes.
type limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	hits map[string][]time.Time
}

const (
	limitWindow   = 10 * time.Minute
	limitFailures = 10
)

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, hits: map[string][]time.Time{}}
}

func (l *limiter) recent(key string, window time.Duration) []time.Time {
	cut := l.now().Add(-window)
	h := l.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = h
	}
	return h
}

func (l *limiter) fail(ip interface{ String() string }) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ip.String()
	l.hits[k] = append(l.recent(k, limitWindow), l.now())
}

func (l *limiter) blocked(ip interface{ String() string }) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip.String(), limitWindow)) >= limitFailures
}

// quota limits what an automation key can do per device: one shutdown/restart per 10 minutes and
// six wakes per hour.
type quota struct{ l *limiter }

const (
	kindPower = "power"
	kindWake  = "wake"
)

func newQuota(now func() time.Time) *quota { return &quota{l: newLimiter(now)} }

func (q *quota) take(key, kind string) *apiError {
	window, max := 10*time.Minute, 1
	if kind == kindWake {
		window, max = time.Hour, 6
	}
	q.l.mu.Lock()
	defer q.l.mu.Unlock()
	k := kind + "/" + key
	if len(q.l.recent(k, window)) >= max {
		return &apiError{Status: http.StatusTooManyRequests, Code: "cooldown",
			Message: fmt.Sprintf("Agents can %s a device at most %d time(s) per %s.", kind, max, window)}
	}
	q.l.hits[k] = append(q.l.hits[k], q.l.now())
	return nil
}

// ---- schedules ----

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

var allDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ValidateSchedule checks and normalises a schedule (self is this helper's name).
func ValidateSchedule(sc *store.Schedule, self string) error {
	sc.Action = strings.ToLower(sc.Action)
	switch sc.Action {
	case "shutdown", "restart":
		sc.Target = self
	case "wake":
		if sc.Target == "" || fleet.IsSelf(sc.Target, self) {
			return errors.New("a wake schedule needs another device as its target")
		}
	default:
		return errors.New("action must be shutdown, restart or wake")
	}
	if !hhmm.MatchString(sc.Time) {
		return errors.New("time must be HH:MM (24-hour)")
	}
	if len(sc.Days) == 0 {
		sc.Days = allDays
	}
	for i, d := range sc.Days {
		d = strings.ToLower(d)
		if len(d) > 3 {
			d = d[:3]
		}
		if !slices.Contains(allDays, d) {
			return errors.New("unknown day " + sc.Days[i])
		}
		sc.Days[i] = d
	}
	return nil
}
