package machines

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// watchEnv is a hub watching gaming-pc: a fake KVM, a fake helper, a fake ping that answers while
// the OS is up (or always, with pingUp), and the helper's port on a real loopback listener that
// accepts and never answers (what a hung Windows does).
type watchEnv struct {
	*env
	mu     sync.Mutex
	clock  time.Time
	pingUp bool
	events []Event
}

func newWatchEnv(t *testing.T, o fakekvm.Options) *watchEnv {
	t.Helper()
	w := &watchEnv{env: newEnv(t, o), clock: t0}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { // accept, say nothing
		var held []net.Conn
		for {
			c, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					c.Close()
				}
				return
			}
			held = append(held, c)
		}
	}()
	w.setPort(ln.Addr().(*net.TCPAddr).Port)
	w.hub.Now = func() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.clock }
	w.hub.LEDGap, w.hub.AskGap, w.hub.AsleepAfter = time.Millisecond, time.Millisecond, time.Millisecond
	w.hub.Ping = func(context.Context, string) string {
		w.mu.Lock()
		up := w.pingUp
		w.mu.Unlock()
		if up || w.kvm.Up() {
			return Replies
		}
		return NoReply
	}
	w.hub.Events = NewBus(t0)
	ch, stop := w.hub.Events.Subscribe(100)
	t.Cleanup(stop)
	go func() {
		for e := range ch {
			w.mu.Lock()
			w.events = append(w.events, e)
			w.mu.Unlock()
		}
	}()
	return w
}

func (w *watchEnv) setPort(port int) {
	p, _ := w.st.Peer("gaming-pc")
	p.Address, p.Port = "127.0.0.1", port
	_ = w.st.PutPeer(p)
	w.fleet.peers["gaming-pc"] = p
}

// rounds takes n looks, 30 s apart, and returns the state after the last.
func (w *watchEnv) rounds(n int) Track {
	for i := 0; i < n; i++ {
		w.hub.WatchOnce(context.Background())
		w.mu.Lock()
		w.clock = w.clock.Add(30 * time.Second)
		w.mu.Unlock()
	}
	tr, _ := w.hub.Tracked("gaming-pc")
	return tr
}

func (w *watchEnv) eventNames() string {
	time.Sleep(20 * time.Millisecond) // let the subscriber catch up
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, e := range w.events {
		if e.Event != EventRecovery {
			out = append(out, e.Event)
		}
	}
	return strings.Join(out, ",")
}

// recoveryStates lists the recovery events' states, in order.
func (w *watchEnv) recoveryStates() string {
	time.Sleep(20 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, e := range w.events {
		if e.Event == EventRecovery {
			out = append(out, e.Recovery.State)
		}
	}
	return strings.Join(out, ",")
}

func TestWatchFindsAHangAndTheRecovery(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	w.link("gaming-pc")
	if tr := w.rounds(1); tr.State != StateOn {
		t.Fatalf("on: %+v", tr)
	}
	if w := w.wiring(); w.LED != store.Wired {
		t.Fatalf("the light was seen on while the helper answered: %+v", w)
	}
	w.kvm.Hang()
	tr := w.rounds(6)
	if tr.State != StateUnknown || tr.Reason != ReasonChecking {
		t.Fatalf("2.5 min silent: %+v", tr)
	}
	tr = w.rounds(1)
	if tr.State != StateHung {
		t.Fatalf("3 min silent: %+v", tr)
	}
	e := tr.Evidence
	if e.Helper != Silent || e.Port != Accepts || e.Ping != NoReply || e.LED != On || !e.LEDTrusted {
		t.Errorf("evidence %+v", e)
	}
	m, _ := w.hub.Get(context.Background(), "gaming-pc")
	if m.State != StateHung || m.Since == nil || !m.Since.Equal(t0.Add(3*time.Minute+30*time.Second)) || m.Evidence == nil {
		t.Errorf("machine %+v", m)
	}
	if got := w.eventNames(); got != "unknown,hung" {
		t.Errorf("events %s", got)
	}
	found := false
	for _, ev := range w.st.Events() {
		found = found || ev.Action == "gaming-pc looks frozen: nothing answers, power light on" && ev.Result == "detected"
	}
	if !found {
		t.Errorf("not in the activity log: %+v", w.st.Events())
	}

	// Reset it: booting, then answering again.
	if r := w.act("reset", Options{}); !r.OK {
		t.Fatalf("reset %+v", r)
	}
	time.Sleep(60 * time.Millisecond) // the fake boots in 30 ms
	if tr := w.rounds(1); tr.State != StateOn {
		t.Fatalf("after reset: %+v", tr)
	}
	if got := w.eventNames(); got != "unknown,hung,recovered" {
		t.Errorf("events %s", got)
	}
	// The hub proposed a reset when it saw the hang; the owner's own button made it moot.
	if got := w.recoveryStates(); got != "proposed,withdrawn,running,back" {
		t.Errorf("recoveries %s", got)
	}
}

func TestWatchBootGraceAfterAWake(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true, BootDelay: time.Hour})
	w.link("gaming-pc")
	w.rounds(1)
	w.kvm.SetOn(false)
	if tr := w.rounds(1); tr.State != StateOff {
		t.Fatalf("off: %+v", tr)
	}
	if r := w.act("wake", Options{}); !r.OK {
		t.Fatalf("wake %+v", r)
	}
	// The fake takes an hour to boot: silent, light on. Booting for 10 minutes, then counted.
	if tr := w.rounds(20); tr.State != StateUnknown || tr.Reason != ReasonBooting {
		t.Fatalf("after 10 min: %+v", tr)
	}
	if tr := w.rounds(8); tr.State != StateHung {
		t.Fatalf("after 14 min: %+v", tr)
	}
}

func TestWatchHelperStopped(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	w.link("gaming-pc")
	w.rounds(1)
	w.fleet.mu.Lock()
	w.fleet.silent = true // the helper crashed; the machine is fine
	w.fleet.mu.Unlock()
	if tr := w.rounds(10); tr.State != StateUnknown || tr.Reason != ReasonHelperStopped || tr.Evidence.Ping != Replies {
		t.Fatalf("%+v", tr)
	}

	// Without a ping (no Tailscale), the port refusing says the same.
	w.hub.Ping = nil
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	w.setPort(port)
	if tr := w.rounds(10); tr.State != StateUnknown || tr.Reason != ReasonHelperStopped ||
		tr.Evidence.Port != Refuses || tr.Evidence.Ping != Unavailable {
		t.Fatalf("%+v", tr)
	}
}

func TestWatchSleep(t *testing.T) {
	for _, c := range []struct {
		led   string
		state string
	}{{"off", "off/asleep"}, {"blinking", "off/asleep"}, {"on", "unknown/may_be_asleep"}} {
		t.Run(c.led, func(t *testing.T) {
			w := newWatchEnv(t, fakekvm.Options{On: true, SleepLED: c.led})
			w.link("gaming-pc")
			w.rounds(1)
			w.hub.Note("gaming-pc", Note{Event: "sleep"})
			w.kvm.Sleep()
			w.hub.Wait() // the light asleep is learned
			if got := w.wiring().LEDAsleep; got != c.led {
				t.Errorf("learned the light asleep as %q", got)
			}
			tr := w.rounds(20)
			got := tr.State + "/" + tr.Reason
			if tr.Asleep {
				got = tr.State + "/asleep"
			}
			if got != c.state {
				t.Errorf("asleep 10 min: %s, want %s", got, c.state)
			}
		})
	}
}

func TestWatchModernStandby(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true, SleepLED: "on"})
	w.fleet.status = `{"sleep":{"s3":false,"modernStandby":true,"afterMinutes":30}}`
	w.link("gaming-pc")
	w.rounds(1)
	w.kvm.Sleep() // no note: Windows didn't send one
	if tr := w.rounds(20); tr.State != StateUnknown || tr.Reason != ReasonMayBeAsleep {
		t.Fatalf("%+v", tr)
	}
	// It wakes up and goes back to work: answering again.
	if r := w.act("wake", Options{}); !r.OK {
		t.Fatalf("%+v", r)
	}
}

func TestWatchUpdatesAtRestart(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	w.fleet.status = `{"updatesPending":true,"pending":{"action":"restart","at":"` + t0.Add(time.Minute).Format(time.RFC3339) + `"}}`
	w.link("gaming-pc")
	w.rounds(1)
	w.kvm.Hang() // "Working on updates": light on, nothing answers
	if tr := w.rounds(60); tr.State != StateUnknown || tr.Reason != ReasonUpdating {
		t.Fatalf("30 min: %+v", tr)
	}
	if got := w.eventNames(); got != "unknown" {
		t.Errorf("events %s", got)
	}
}

func TestWatchMachineWithoutHelper(t *testing.T) {
	w := newWatchEnv(t, fakekvm.Options{On: true})
	_, _, err := w.hub.Link(context.Background(), LinkRequest{Name: "kvm", Address: w.kvm.URL, Password: "admin",
		Machine: "nas", MAC: "02:00:5e:00:00:09"})
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := w.hub.Tracked("nas"); tr.State != "" {
		t.Fatal(tr)
	}
	w.kvm.Hang()
	w.rounds(20)
	if tr, _ := w.hub.Tracked("nas"); tr.State != StateUnknown || tr.Reason != ReasonNoHelper {
		t.Fatalf("%+v", tr)
	}
}

func TestBusReplay(t *testing.T) {
	b := NewBus(t0)
	var ids []string
	for i := 0; i < maxHeld+10; i++ {
		ids = append(ids, b.Publish(Event{Machine: strconv.Itoa(i)}).ID)
	}
	if got, ok := b.After(ids[len(ids)-3]); !ok || len(got) != 2 || got[1].ID != ids[len(ids)-1] {
		t.Errorf("after the third last: %v %v", ok, got)
	}
	if got, ok := b.After(ids[len(ids)-1]); !ok || len(got) != 0 {
		t.Errorf("after the last: %v %v", ok, got)
	}
	if _, ok := b.After(ids[0]); ok {
		t.Error("replayed from an ID no longer held")
	}
	if _, ok := b.After("12"); ok {
		t.Error("replayed from an ID before this bus")
	}
	if _, ok := b.After("x"); ok {
		t.Error("replayed from garbage")
	}
	if b2 := NewBus(t0.Add(time.Hour)); b2.Publish(Event{}).ID <= ids[len(ids)-1] {
		t.Error("IDs after a restart don't grow")
	}

	// A subscriber that stops reading is cut off.
	ch, stop := b.Subscribe(2)
	defer stop()
	for i := 0; i < 3; i++ {
		b.Publish(Event{})
	}
	n := 0
	for range ch {
		n++
	}
	if n != 2 || b.Subscribers() != 0 {
		t.Errorf("got %d before the cut, %d subscribers", n, b.Subscribers())
	}
}
