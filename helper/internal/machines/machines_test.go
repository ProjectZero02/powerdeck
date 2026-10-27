package machines

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/secret"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// fakeFleet: the machine's helper answers while the fake KVM's computer is up.
type fakeFleet struct {
	mu       sync.Mutex
	kvm      *fakekvm.Fake
	peers    map[string]store.Peer
	wakes    []string
	forwards []string
	fwdErr   error
	wakeErr  error
	silent   bool // the helper never answers (e.g. not installed yet)
}

func (f *fakeFleet) Peer(name string) (store.Peer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.peers {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	return store.Peer{}, fleet.ErrUnknownDevice
}

func (f *fakeFleet) Wake(name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wakeErr != nil {
		return nil, f.wakeErr
	}
	f.wakes = append(f.wakes, name)
	return []string{"192.168.1.0/24"}, nil
}

func (f *fakeFleet) Forward(_ context.Context, name, method, path string, body, out any, who string) error {
	f.mu.Lock()
	b, _ := json.Marshal(body)
	f.forwards = append(f.forwards, method+" "+path+" "+string(b)+" "+who)
	err := f.fwdErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if !f.kvm.Up() {
		return fleet.ErrOffline
	}
	return json.Unmarshal([]byte(`{"pending":{"action":"shutdown"}}`), out)
}

func (f *fakeFleet) Answers(context.Context, string) bool {
	f.mu.Lock()
	silent := f.silent
	f.mu.Unlock()
	return !silent && f.kvm.Up()
}

type env struct {
	t     *testing.T
	hub   *Hub
	st    *store.Store
	dir   string
	kvm   *fakekvm.Fake
	fleet *fakeFleet
}

func newEnv(t *testing.T, o fakekvm.Options) *env {
	t.Helper()
	o.TimeScale = 0.001 // boot 30 ms, 12 s hold 12 ms
	k := fakekvm.Start(o)
	t.Cleanup(k.Close)
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.PutPeer(store.Peer{Name: "gaming-pc", Address: "100.101.1.10", Port: 47321, Pin: "p", MAC: "02:00:5e:00:00:01", LAN: []string{"192.168.1.0/24"}})
	fl := &fakeFleet{kvm: k, peers: map[string]store.Peer{}}
	for _, p := range st.Peers() {
		fl.peers[p.Name] = p
	}
	h := &Hub{Store: st, Box: &secret.KeyFile{Path: filepath.Join(t.TempDir(), "secret.key")}, Fleet: fl, Self: "atlas",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Poll: 5 * time.Millisecond, Settle: 2 * time.Second,
		OffWait: 5 * time.Millisecond, ResetVerify: 2 * time.Second, VerifyEvery: 10 * time.Millisecond}
	t.Cleanup(h.Wait)
	return &env{t, h, st, dir, k, fl}
}

func (e *env) link(machine string) KVMView {
	e.t.Helper()
	v, _, err := e.hub.Link(context.Background(), LinkRequest{Name: "kvm", Address: e.kvm.URL, Password: "admin", Machine: machine})
	if err != nil {
		e.t.Fatalf("link: %v", err)
	}
	return v
}

func (e *env) wiring() store.Wiring {
	k, _ := e.st.KVM("kvm")
	return k.Wiring
}

func (e *env) act(action string, o Options) Result {
	return e.hub.Act(context.Background(), "gaming-pc", action, o)
}

func presses(f *fakekvm.Fake) string {
	var out []string
	for _, p := range f.Presses() {
		out = append(out, p.Button+" "+p.Duration.String())
	}
	return strings.Join(out, ", ")
}

func ledSteps(r Result) int {
	n := 0
	for _, s := range r.Steps {
		if strings.HasPrefix(s.Do, "read power light") {
			n++
		}
	}
	return n
}

func TestLink(t *testing.T) {
	e := newEnv(t, fakekvm.Options{Password: "s3cret", On: true})
	ctx := context.Background()
	cases := []struct {
		r    LinkRequest
		code string
	}{
		{LinkRequest{Address: "192.168.1.20", Password: "s3cret", Machine: "gaming-pc"}, "not_tailscale"}, // LAN: password nearly in the clear
		{LinkRequest{Address: e.kvm.URL, Password: "s3cret", Machine: "Atlas"}, "self"},
		{LinkRequest{Address: e.kvm.URL, Password: "wrong", Machine: "gaming-pc"}, "kvm_password"},
		{LinkRequest{Address: "kvm.invalid.ts.net", Password: "s3cret", Machine: "gaming-pc"}, "kvm_unreachable"},
		{LinkRequest{Address: e.kvm.URL, Password: "s3cret", Machine: "nas", MAC: "nope"}, "bad_request"},
	}
	for _, c := range cases {
		_, _, err := e.hub.Link(ctx, c.r)
		var he *Error
		if !errors.As(err, &he) || he.Code != c.code {
			t.Errorf("%+v: got %v, want %s", c.r, err, c.code)
		}
	}
	v, led, err := e.hub.Link(ctx, LinkRequest{Address: e.kvm.URL, Password: "s3cret", Machine: "GAMING-PC"})
	if err != nil || led == nil || !*led {
		t.Fatalf("link: %v %v", led, err)
	}
	if v.Name != "127" || v.Machine != "gaming-pc" || v.Wiring.LED != store.Wired || v.Wiring.Power != store.Unknown {
		t.Errorf("view %+v", v) // named after the host's first label; an LED seen on is wired
	}
	if !strings.Contains(v.Firmware, "2.5.2") {
		t.Errorf("firmware %q", v.Firmware)
	}
	// The password is sealed in the state file.
	raw, _ := os.ReadFile(filepath.Join(e.dir, "state.json"))
	if len(raw) == 0 {
		matches, _ := filepath.Glob(filepath.Join(e.dir, "*.json"))
		for _, m := range matches {
			b, _ := os.ReadFile(m)
			raw = append(raw, b...)
		}
	}
	if !strings.Contains(string(raw), `"kvms"`) || strings.Contains(string(raw), "s3cret") {
		t.Errorf("state file: kvms stored=%v, password in clear=%v", strings.Contains(string(raw), `"kvms"`), strings.Contains(string(raw), "s3cret"))
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "password") {
		t.Errorf("view shows a password field: %s", b)
	}
	// A second KVM for the same machine is refused.
	if _, _, err := e.hub.Link(ctx, LinkRequest{Name: "other", Address: e.kvm.URL, Password: "s3cret", Machine: "gaming-pc"}); err == nil {
		t.Error("two KVMs for one machine")
	}
	// Works again after the hub restarts (a new client opens the sealed password).
	e.hub.forget("127")
	if m, ok := e.hub.Get(ctx, "gaming-pc"); !ok || m.LED == nil || !*m.LED || m.State != "on" {
		t.Errorf("after restart: %+v", m)
	}
}

func TestWake(t *testing.T) {
	ctx := context.Background()
	t.Run("already on", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{On: true})
		e.link("gaming-pc")
		if r := e.act("wake", Options{}); !r.OK || r.Code != "already_on" || presses(e.kvm) != "" {
			t.Errorf("%+v %s", r, presses(e.kvm))
		}
	})
	t.Run("press when the trusted light reads off", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{On: true})
		e.link("gaming-pc") // seen on: wired
		e.kvm.SetOn(false)
		r := e.act("wake", Options{})
		if !r.OK || presses(e.kvm) != "power 800ms" || !e.kvm.On() || ledSteps(r) < 2 {
			t.Fatalf("%+v %s", r, presses(e.kvm))
		}
		if e.wiring().Power != store.Wired {
			t.Errorf("power switch not learned: %+v", e.wiring())
		}
		if len(e.fleet.wakes) != 0 {
			t.Error("sent Wake-on-LAN as well")
		}
	})
	t.Run("no press until the light was seen on", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{})
		e.link("gaming-pc") // off at link time: LED unknown
		r := e.act("wake", Options{})
		if !r.OK || presses(e.kvm) != "" || len(e.fleet.wakes) != 1 {
			t.Errorf("%+v %s wakes=%v", r, presses(e.kvm), e.fleet.wakes)
		}
	})
	t.Run("no press when the light can't be read", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{On: true})
		e.link("gaming-pc")
		e.kvm.SetOn(false)
		e.kvm.SetUnreachable(true)
		r := e.act("wake", Options{})
		if !r.OK || presses(e.kvm) != "" || len(e.fleet.wakes) != 1 {
			t.Errorf("%+v %s", r, presses(e.kvm))
		}
	})
	t.Run("power switch not wired: Wake-on-LAN instead", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{On: true, NoPowerSwitch: true})
		e.link("gaming-pc")
		e.kvm.SetOn(false)
		e.hub.Settle = 50 * time.Millisecond
		r := e.act("wake", Options{})
		if !r.OK || len(e.fleet.wakes) != 1 || e.wiring().Power != store.NotWired {
			t.Fatalf("%+v %+v", r, e.wiring())
		}
		// Next time it doesn't press at all.
		e.act("wake", Options{})
		if presses(e.kvm) != "power 800ms" {
			t.Errorf("pressed again: %s", presses(e.kvm))
		}
	})
	t.Run("from the KVM when no helper is on that LAN", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{})
		e.link("gaming-pc")
		e.fleet.wakeErr = fleet.ErrNoWaker
		r := e.act("wake", Options{})
		if !r.OK || len(e.kvm.Wakes()) != 1 || e.kvm.Wakes()[0] != "02:00:5e:00:00:01" {
			t.Errorf("%+v %v", r, e.kvm.Wakes())
		}
	})
	t.Run("machine without a helper", func(t *testing.T) {
		e := newEnv(t, fakekvm.Options{})
		if _, _, err := e.hub.Link(ctx, LinkRequest{Name: "kvm", Address: e.kvm.URL, Password: "admin", Machine: "nas"}); err != nil {
			t.Fatal(err)
		}
		r := e.hub.Act(ctx, "nas", "wake", Options{})
		if r.OK || r.Code != "cant_wake" {
			t.Errorf("no MAC: %+v", r)
		}
		mac := "02-00-5E-00-00-09"
		if _, err := e.hub.Update(ctx, "kvm", Patch{MAC: &mac}); err != nil {
			t.Fatal(err)
		}
		if r := e.hub.Act(ctx, "nas", "wake", Options{}); !r.OK || e.kvm.Wakes()[0] != "02:00:5e:00:00:09" {
			t.Errorf("with MAC: %+v %v", r, e.kvm.Wakes())
		}
	})
}

func TestShutdownThroughHelper(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true})
	e.link("gaming-pc")
	r := e.act("shutdown", Options{Who: "Pixel 9 Pro"})
	if !r.OK || ledSteps(r) != 2 || presses(e.kvm) != "" || r.Helper["pending"] == nil {
		t.Fatalf("%+v", r)
	}
	if f := e.fleet.forwards; len(f) != 1 || f[0] != `POST /v2/power/shutdown {"delaySeconds":60} Pixel 9 Pro` {
		t.Errorf("forwards %v", f)
	}
	e.fleet.fwdErr = &peer.Error{Status: 409, Code: "in_use", Message: "Someone is using this device."}
	if r := e.act("restart", Options{}); r.OK || r.Code != "in_use" || r.Status != 409 {
		t.Errorf("in use: %+v", r)
	}
	e.fleet.fwdErr = nil
	e.kvm.Hang()
	if r := e.act("shutdown", Options{}); r.OK || r.Code != "helper_down" || presses(e.kvm) != "" {
		t.Errorf("frozen: %+v", r) // no press: a reset is the owner's decision
	}
}

func TestResetGatesAndVerify(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true})
	e.link("gaming-pc")
	if r := e.act("reset", Options{}); r.Code != "helper_answers" || presses(e.kvm) != "" {
		t.Errorf("answering: %+v", r)
	}
	e.kvm.Hang()
	r := e.act("reset", Options{})
	if !r.OK || presses(e.kvm) != "reset 800ms" || ledSteps(r) != 2 || e.kvm.Reboots() != 1 {
		t.Fatalf("%+v %s", r, presses(e.kvm))
	}
	e.hub.Wait()
	if e.wiring().Reset != store.Wired || !e.kvm.Up() {
		t.Errorf("reset not verified: %+v", e.wiring())
	}
	// Off (trusted light): nothing to reset.
	e.kvm.SetOn(false)
	if r := e.act("reset", Options{}); r.Code != "already_off" {
		t.Errorf("off: %+v", r)
	}
}

func TestResetRefusedBlind(t *testing.T) {
	e := newEnv(t, fakekvm.Options{})
	e.link("gaming-pc") // off: LED unknown
	e.fleet.silent = true
	if r := e.act("reset", Options{}); r.Code != "led_untrusted" || presses(e.kvm) != "" {
		t.Errorf("untrusted: %+v", r)
	}
	e2 := newEnv(t, fakekvm.Options{On: true})
	e2.link("gaming-pc")
	e2.kvm.Hang()
	e2.kvm.SetUnreachable(true)
	if r := e2.act("force-off", Options{}); r.Code != "led_unreadable" || presses(e2.kvm) != "" {
		t.Errorf("unreadable: %+v", r)
	}
}

func TestResetSwitchNotWired(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true, NoResetSwitch: true})
	e.link("gaming-pc")
	e.kvm.Hang()
	e.hub.ResetVerify = 100 * time.Millisecond
	if r := e.act("reset", Options{}); !r.OK {
		t.Fatalf("%+v", r)
	}
	e.hub.Wait()
	if e.wiring().Reset != store.NotWired {
		t.Fatalf("%+v", e.wiring())
	}
	if r := e.act("reset", Options{}); r.Code != "reset_not_wired" {
		t.Errorf("second reset: %+v", r)
	}
	// Recovery moves on to force off and power on.
	r := e.act("force-off", Options{})
	if !r.OK || presses(e.kvm) != "reset 800ms, power 12s, power 800ms" || !e.kvm.On() {
		t.Fatalf("%+v %s", r, presses(e.kvm))
	}
	time.Sleep(60 * time.Millisecond) // boot (scaled)
	if !e.kvm.Up() || e.wiring().Power != store.Wired {
		t.Errorf("not back: up=%v %+v", e.kvm.Up(), e.wiring())
	}
}

func TestForceOff(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true})
	e.link("gaming-pc")
	if r := e.act("force-off", Options{}); r.Code != "helper_answers" {
		t.Errorf("answering: %+v", r)
	}
	if r := e.act("force-off", Options{Force: true, StayOff: true}); !r.OK || presses(e.kvm) != "power 12s" || e.kvm.On() {
		t.Errorf("stay off: %+v %s", r, presses(e.kvm))
	}
	e2 := newEnv(t, fakekvm.Options{On: true, NoPowerSwitch: true})
	e2.link("gaming-pc")
	e2.kvm.Hang()
	e2.hub.Settle = 50 * time.Millisecond
	if r := e2.act("force-off", Options{}); r.Code != "no_effect" || e2.wiring().Power != store.NotWired {
		t.Errorf("not wired: %+v %+v", r, e2.wiring())
	}
}

func TestOneActionAtATime(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true, TimeScale: 0})
	e.link("gaming-pc")
	e.kvm.Hang()
	e.hub.OffWait = 300 * time.Millisecond
	done := make(chan Result)
	go func() { done <- e.act("force-off", Options{}) }()
	time.Sleep(100 * time.Millisecond)
	if r := e.act("reset", Options{}); r.Code != "busy" {
		t.Errorf("second action: %+v", r)
	}
	if r := <-done; !r.OK {
		t.Errorf("first: %+v", r)
	}
}

func TestLEDWiringLearned(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true, NoLED: true})
	clock := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	e.hub.Now = func() time.Time { return clock }
	e.link("gaming-pc")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if w := e.wiring(); w.LED != store.Unknown {
			t.Fatalf("after %d readings: %+v", i, w)
		}
		e.hub.List(ctx)
		e.hub.List(ctx) // readings close together count once
		clock = clock.Add(time.Minute)
	}
	if w := e.wiring(); w.LED != store.NotWired {
		t.Fatalf("off while answering ×3: %+v", w)
	}
	m, _ := e.hub.Get(ctx, "gaming-pc")
	if m.State != "on" || strings.Contains(strings.Join(m.Actions, ","), "reset") {
		t.Errorf("machine %+v", m)
	}
	// While the helper is silent, an off reading says nothing.
	e.fleet.silent = true
	if _, err := e.hub.Update(ctx, "kvm", Patch{Recheck: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e.hub.List(ctx)
		clock = clock.Add(time.Minute)
	}
	if w := e.wiring(); w.LED != store.Unknown {
		t.Errorf("learned from a silent helper: %+v", w)
	}
}

func TestList(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true})
	e.link("gaming-pc")
	_ = e.st.PutPeer(store.Peer{Name: "laptop", Address: "100.101.1.12"})
	e.fleet.peers["laptop"] = store.Peer{Name: "laptop"}
	ms := e.hub.List(context.Background())
	if len(ms) != 2 {
		t.Fatalf("%+v", ms)
	}
	g := ms[0]
	if g.Name != "gaming-pc" || g.KVM == nil || g.State != "on" || *g.LED != true || strings.Join(g.Actions, ",") != "wake,shutdown,restart,reset,force-off" {
		t.Errorf("gaming-pc %+v", g)
	}
	e.kvm.SetOn(false)
	if m, _ := e.hub.Get(context.Background(), "kvm"); m.State != "off" || strings.Join(m.Actions, ",") != "wake,shutdown,restart" {
		t.Errorf("off: %+v", m)
	}
	if ms[1].Name != "laptop" || ms[1].KVM != nil {
		t.Errorf("laptop %+v", ms[1])
	}
}

func TestStaleCredentials(t *testing.T) {
	e := newEnv(t, fakekvm.Options{On: true})
	e.link("gaming-pc")
	// Another installation's key (or a tampered value) can't open it: a clear error, no press.
	e.hub.Box = &secret.KeyFile{Path: filepath.Join(t.TempDir(), "other.key")}
	e.hub.forget("kvm")
	e.kvm.Hang()
	if r := e.act("reset", Options{}); r.Code != "kvm_credentials" || presses(e.kvm) != "" {
		t.Errorf("%+v", r)
	}
	// Linking again with the password fixes it and keeps what was learned.
	before := e.wiring()
	e.link("gaming-pc")
	if e.wiring().LED != before.LED {
		t.Errorf("wiring lost: %+v", e.wiring())
	}
	_ = nanokvm.Power
}
