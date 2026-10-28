package machines

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// hung is a hub watching gaming-pc, frozen long enough to be called hung (and so proposed).
func hung(t *testing.T, o fakekvm.Options) *watchEnv {
	t.Helper()
	o.On = true
	w := newWatchEnv(t, o)
	w.link("gaming-pc")
	w.rounds(1)
	w.kvm.Hang()
	if tr := w.rounds(7); tr.State != StateHung {
		t.Fatalf("not hung: %+v", tr)
	}
	return w
}

func (w *watchEnv) open() Recovery {
	w.t.Helper()
	open := w.hub.Recoveries(true)
	if len(open) != 1 {
		w.t.Fatalf("open recoveries: %+v", open)
	}
	return open[0]
}

func (w *watchEnv) logged(action, result string) bool {
	for _, e := range w.st.Events() {
		if strings.HasPrefix(e.Action, action) && e.Result == result {
			return true
		}
	}
	return false
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestHubProposesAResetAndTheOwnerConfirms(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	r := w.open()
	if r.Action != "reset" || r.State != RecoveryProposed || r.ProposedBy != "atlas" || r.Evidence == nil || r.Evidence.LED != On {
		t.Fatalf("%+v", r)
	}
	if !w.logged("propose reset gaming-pc: Nothing answers", RecoveryProposed) {
		t.Errorf("log %+v", w.st.Events())
	}
	if m, _ := w.hub.Get(context.Background(), "gaming-pc"); m.Recovery == nil || m.Recovery.ID != r.ID {
		t.Errorf("machine view %+v", m.Recovery)
	}
	if presses(w.kvm) != "" {
		t.Fatal("pressed before the owner confirmed")
	}

	got, err := w.hub.Confirm(r.ID, "Pixel 9 Pro")
	if err != nil || got.State != RecoveryRunning || got.ConfirmedBy != "Pixel 9 Pro" {
		t.Fatalf("%+v %v", got, err)
	}
	w.hub.Wait()
	got, _ = w.hub.Recovery(r.ID)
	if got.State != RecoveryBack || presses(w.kvm) != "reset 800ms" || len(got.Steps) == 0 || got.EndedAt.IsZero() {
		t.Fatalf("%+v %s", got, presses(w.kvm))
	}
	if !w.logged("confirm reset gaming-pc", "confirmed") || !w.logged("gaming-pc answers again after the reset", RecoveryBack) {
		t.Errorf("log %+v", w.st.Events())
	}
	if s := w.recoveryStates(); s != "proposed,running,running,back" {
		t.Errorf("events %s", s)
	}
	// Answering again: no new proposal.
	if w.rounds(1); len(w.hub.Recoveries(true)) != 0 {
		t.Errorf("open %+v", w.hub.Recoveries(true))
	}
}

func TestProposeRefusals(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	w.link("gaming-pc")
	p := Proposal{Machine: "gaming-pc", Action: "reset", By: "Atlas agent", Key: "k1", Agent: true}
	if _, _, err := w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "wake"}); code(err) != "bad_action" {
		t.Errorf("wake: %v", err)
	}
	if _, _, err := w.hub.Propose(Proposal{Machine: "nas", Action: "reset"}); code(err) != "no_kvm" {
		t.Errorf("no KVM: %v", err)
	}
	w.rounds(1)
	if _, _, err := w.hub.Propose(p); code(err) != "helper_answers" {
		t.Errorf("answering: %v", err)
	}
	w.kvm.SetOn(false)
	w.rounds(1)
	if _, _, err := w.hub.Propose(p); code(err) != "already_off" {
		t.Errorf("off: %v", err)
	}

	// Silent but not yet hung: an agent may know better; the owner decides.
	w.kvm.SetOn(true)
	time.Sleep(60 * time.Millisecond)
	w.rounds(1)
	w.kvm.Hang()
	w.rounds(2)
	p.Reason = "Can't reach it.\x1b[31m " + strings.Repeat("x", 300)
	r, created, err := w.hub.Propose(p)
	if err != nil || !created || r.ProposerKey != "k1" || !r.Agent || len([]rune(r.Reason)) != maxReason || strings.ContainsRune(r.Reason, '\x1b') {
		t.Fatalf("%+v %v %v", r, created, err)
	}
	if again, created, _ := w.hub.Propose(Proposal{Machine: "GAMING-PC", Action: "force-off", By: "Pixel 9 Pro"}); created || again.ID != r.ID {
		t.Errorf("a second proposal: %+v %v", again, created)
	}
	// The hub's own proposal at the hang is the same one.
	if w.rounds(6); len(w.hub.Recoveries(false)) != 1 {
		t.Errorf("%+v", w.hub.Recoveries(false))
	}
}

func TestProposeWithoutHelper(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	if _, _, err := w.hub.Link(context.Background(), LinkRequest{Name: "kvm", Address: w.kvm.URL, Password: "admin", Machine: "nas"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.hub.Propose(Proposal{Machine: "nas", Action: "reset"}); code(err) != "no_helper" {
		t.Errorf("%v", err)
	}
}

func TestDeclineAndWithdraw(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	r := w.open()
	if got, _ := w.hub.Decline(r.ID, "Pixel 9 Pro", false); got.State != RecoveryDeclined {
		t.Fatalf("%+v", got)
	}
	if got, _ := w.hub.Confirm(r.ID, "Pixel 9 Pro"); got.State != RecoveryDeclined || presses(w.kvm) != "" {
		t.Errorf("confirmed after declining: %+v", got)
	}
	if !w.logged("decline reset gaming-pc", RecoveryDeclined) {
		t.Errorf("log %+v", w.st.Events())
	}
	// Not asked again during this hang.
	if w.rounds(10); len(w.hub.Recoveries(true)) != 0 {
		t.Errorf("asked again: %+v", w.hub.Recoveries(true))
	}

	// An agent withdraws its own proposal.
	r, _, err := w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "force-off", By: "Atlas agent", Key: "k1", Agent: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := w.hub.Decline(r.ID, "Atlas agent", true); got.State != RecoveryWithdrawn {
		t.Errorf("%+v", got)
	}

	// Moot once it answers again.
	r, _, _ = w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "reset", By: "Atlas agent"})
	w.kvm.SetOn(false)
	w.kvm.SetOn(true)
	time.Sleep(60 * time.Millisecond)
	w.rounds(1)
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryWithdrawn || got.Message != "gaming-pc answers again." {
		t.Errorf("%+v", got)
	}

	// A new hang is asked about again.
	w.kvm.Hang()
	if w.rounds(7); len(w.hub.Recoveries(true)) != 1 {
		t.Errorf("not asked at the next hang: %+v", w.hub.Recoveries(true))
	}
}

func TestProposalExpires(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	r := w.open()
	w.mu.Lock()
	w.clock = w.clock.Add(RecoveryTTL)
	w.mu.Unlock()
	w.rounds(1)
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryExpired {
		t.Errorf("%+v", got)
	}
}

func TestConfirmAfterItCameBackPressesNothing(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	r := w.open()
	w.kvm.SetOn(false)
	w.kvm.SetOn(true)
	time.Sleep(60 * time.Millisecond) // back, but the hub hasn't looked yet
	w.hub.Confirm(r.ID, "Pixel 9 Pro")
	w.hub.Wait()
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryWithdrawn || presses(w.kvm) != "" {
		t.Errorf("%+v %s", got, presses(w.kvm))
	}
	// Nothing was pressed, so another may run.
	w.kvm.Hang()
	if _, _, err := w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "reset"}); err != nil {
		t.Errorf("%v", err)
	}
}

func TestConfirmTwiceRunsOnce(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	r := w.open()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); w.hub.Confirm(r.ID, "Pixel 9 Pro") }()
	}
	wg.Wait()
	w.hub.Wait()
	confirms := 0
	for _, e := range w.st.Events() {
		if e.Action == "confirm reset gaming-pc" {
			confirms++
		}
	}
	if got, _ := w.hub.Recovery(r.ID); presses(w.kvm) != "reset 800ms" || confirms != 1 || got.State != RecoveryBack {
		t.Errorf("presses %s, %d confirms, %+v", presses(w.kvm), confirms, got)
	}
}

func TestNotBackThenOffAndOn(t *testing.T) {
	w := hung(t, fakekvm.Options{NoResetSwitch: true})
	w.hub.ResetVerify = 100 * time.Millisecond
	r := w.open()
	w.hub.Confirm(r.ID, "Pixel 9 Pro")
	w.hub.Wait()
	got, _ := w.hub.Recovery(r.ID)
	if got.State != RecoveryNotBack || !w.logged("gaming-pc didn't answer within", RecoveryNotBack) {
		t.Fatalf("%+v %+v", got, w.st.Events())
	}
	if _, _, err := w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "reset"}); code(err) != "too_soon" {
		t.Errorf("within 30 min: %v", err)
	} else {
		var e *Error
		errors.As(err, &e)
		if at, _ := e.Extra["retryAt"].(time.Time); !at.Equal(got.ConfirmedAt.Add(RecoveryGap)) {
			t.Errorf("retryAt %v", e.Extra)
		}
	}
	// Still frozen after the boot grace and the 30 minutes: asked again, and this time it's off
	// and on, since the reset switch did nothing.
	w.rounds(61)
	r = w.open()
	w.hub.Confirm(r.ID, "Pixel 9 Pro")
	w.hub.Wait()
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryBack || presses(w.kvm) != "reset 800ms, power 12s, power 800ms" {
		t.Errorf("%+v %s", got, presses(w.kvm))
	}
}

func TestAutoReset(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	w.hub.AutoReset = func() time.Duration { return 10 * time.Minute }
	r := w.open()
	w.rounds(19) // 9.5 minutes after the proposal
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryProposed {
		t.Fatalf("early: %+v", got)
	}
	w.rounds(2)
	w.hub.Wait()
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryBack || got.ConfirmedBy != Auto || presses(w.kvm) != "reset 800ms" {
		t.Errorf("%+v %s", got, presses(w.kvm))
	}
}

func TestAutoResetOnlyResetsHungMachines(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	w.hub.AutoReset = func() time.Duration { return 5 * time.Minute }
	w.link("gaming-pc")
	w.rounds(1)
	w.kvm.Hang()
	w.rounds(2)
	r, _, err := w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "force-off", By: "Atlas agent"})
	if err != nil {
		t.Fatal(err)
	}
	if tr := w.rounds(30); tr.State != StateHung {
		t.Fatalf("%+v", tr)
	}
	if got, _ := w.hub.Recovery(r.ID); got.State != RecoveryProposed || presses(w.kvm) != "" {
		t.Errorf("force off: %+v %s", got, presses(w.kvm))
	}

	// Installing updates at a restart: not hung, so an agent's reset waits for the owner.
	w2 := newWatchEnv(t, fakekvm.Options{On: true})
	w2.hub.AutoReset = func() time.Duration { return 5 * time.Minute }
	w2.fleet.status = `{"updatesPending":true,"pending":{"action":"restart","at":"` + t0.Add(time.Minute).Format(time.RFC3339) + `"}}`
	w2.link("gaming-pc")
	w2.rounds(1)
	w2.kvm.Hang()
	w2.rounds(2)
	r, _, _ = w2.hub.Propose(Proposal{Machine: "gaming-pc", Action: "reset", By: "Atlas agent"})
	if tr := w2.rounds(30); tr.Reason != ReasonUpdating {
		t.Fatalf("%+v", tr)
	}
	if got, _ := w2.hub.Recovery(r.ID); got.State != RecoveryProposed || presses(w2.kvm) != "" {
		t.Errorf("updating: %+v %s", got, presses(w2.kvm))
	}
}

func TestOwnerButtonsAreRecoveries(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	proposal := w.open()
	if r := w.act("force-off", Options{Who: "Pixel 9 Pro", StayOff: true}); !r.OK {
		t.Fatalf("%+v", r)
	}
	w.hub.Wait()
	if got, _ := w.hub.Recovery(proposal.ID); got.State != RecoveryWithdrawn || got.Message != "Pixel 9 Pro used force off directly." {
		t.Errorf("proposal %+v", got)
	}
	all := w.hub.Recoveries(false)
	if len(all) != 2 || !all[0].Direct || all[0].State != RecoveryOff || all[0].ConfirmedBy != "Pixel 9 Pro" {
		t.Fatalf("%+v", all)
	}
	// It counts towards the limit, but doesn't stop the owner's buttons.
	w.kvm.SetOn(true)
	time.Sleep(60 * time.Millisecond)
	w.kvm.Hang()
	if _, _, err := w.hub.Propose(Proposal{Machine: "gaming-pc", Action: "reset"}); code(err) != "too_soon" {
		t.Errorf("%v", err)
	}
	if r := w.act("reset", Options{Who: "Pixel 9 Pro"}); !r.OK {
		t.Errorf("%+v", r)
	}
	// Refused before any press: not kept.
	w.hub.Wait()
	before := len(w.hub.Recoveries(false))
	if r := w.act("reset", Options{Who: "Pixel 9 Pro"}); r.Code != "helper_answers" || len(w.hub.Recoveries(false)) != before {
		t.Errorf("%+v %d", r, len(w.hub.Recoveries(false)))
	}
}

func TestRecoveryCutShortByARestart(t *testing.T) {
	w := hung(t, fakekvm.Options{})
	r := w.open()
	// Running when the hub stopped: as stored.
	w.hub.rmu.Lock()
	w.hub.recs[0].State = RecoveryRunning
	w.hub.saveLocked()
	w.hub.rmu.Unlock()

	st, err := store.Open(filepath.Dir(w.st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	h2 := &Hub{Store: st, Fleet: w.fleet, Self: "atlas", Now: w.hub.Now}
	if got, _ := h2.Recovery(r.ID); got.State != RecoveryFailed || !strings.Contains(got.Message, "restarted") {
		t.Errorf("%+v", got)
	}
}
