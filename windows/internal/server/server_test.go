package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/windows/internal/netinfo"
	"github.com/projectzero02/powerdeck/windows/internal/pairing"
	"github.com/projectzero02/powerdeck/windows/internal/power"
	"github.com/projectzero02/powerdeck/windows/internal/store"
)

type fakeSys struct {
	mu        sync.Mutex
	scheduled []string
	pending   bool
}

func (f *fakeSys) Schedule(a power.Action, d time.Duration, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending {
		return power.ErrPending
	}
	f.pending = true
	f.scheduled = append(f.scheduled, string(a)+"/"+d.String()+"/"+msg)
	return nil
}

func (f *fakeSys) Abort() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.pending {
		return power.ErrNothingPending
	}
	f.pending = false
	return nil
}

// testStart is the env's fixed clock; the fake PC booted 90 minutes before it.
var testStart = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func (f *fakeSys) BootTime() (time.Time, error) { return testStart.Add(-90 * time.Minute), nil }

type env struct {
	t     *testing.T
	h     http.Handler
	pm    *pairing.Manager
	sys   *fakeSys
	st    *store.Store
	from  string
	clock time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, sys: &fakeSys{}, st: st, from: "100.101.1.40:51234", clock: testStart}
	now := func() time.Time { return e.clock }
	e.pm = pairing.NewManager(now)
	e.h = New(Deps{
		Store: st, Pairing: e.pm, Power: power.NewController(e.sys, now), Name: "Gaming-PC",
		MAC: func() string { return "d8:5e:d3:12:34:56" },
		Discovery: func() netinfo.Discovered {
			return netinfo.Discovered{NanoKVM: &netinfo.Found{Address: "100.101.1.20", Name: "server-nanokvm"}}
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: now,
	})
	return e
}

func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = e.from
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *env) pair() string {
	e.t.Helper()
	code, out := e.do("POST", "/v1/pair", "", map[string]string{"code": e.pm.Current().Value, "deviceName": "Pixel 9 Pro", "deviceModel": "Pixel 9 Pro"})
	if code != 200 {
		e.t.Fatalf("pair: %d %v", code, out)
	}
	return out["token"].(string)
}

func TestNonTailnetSourcesAreRefused(t *testing.T) {
	e := newEnv(t)
	for _, from := range []string{"192.168.1.31:5000", "8.8.8.8:443", "[2001:db8::1]:80"} {
		e.from = from
		if code, _ := e.do("GET", "/v1/info", "", nil); code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", from, code)
		}
	}
	for _, from := range []string{"100.101.1.10:1", "[fd7a:115c:a1e0::1]:1", "127.0.0.1:1", "[::ffff:100.101.1.40]:1"} {
		e.from = from
		if code, _ := e.do("GET", "/v1/info", "", nil); code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", from, code)
		}
	}
}

func TestPairingIsOneTimeAndReturnsContext(t *testing.T) {
	e := newEnv(t)
	c := e.pm.Current().Value
	code, out := e.do("POST", "/v1/pair", "", map[string]string{"code": c, "deviceName": "Phone\x00‮ evil"})
	if code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	pc := out["pc"].(map[string]any)
	if pc["mac"] != "d8:5e:d3:12:34:56" || pc["name"] != "Gaming-PC" || pc["nanokvm"].(map[string]any)["address"] != "100.101.1.20" {
		t.Errorf("pc context: %v", pc)
	}
	if d := e.st.Devices(); len(d) != 1 || d[0].Name != "Phone evil" {
		t.Errorf("device name not sanitised: %+v", d)
	}
	if code, _ := e.do("POST", "/v1/pair", "", map[string]string{"code": c, "deviceName": "again"}); code != http.StatusForbidden {
		t.Errorf("code reuse: got %d, want 403", code)
	}
}

func TestPairingCodeExpires(t *testing.T) {
	e := newEnv(t)
	c := e.pm.Current().Value
	e.clock = e.clock.Add(pairing.TTL + time.Second)
	if code, _ := e.do("POST", "/v1/pair", "", map[string]string{"code": c}); code != http.StatusForbidden {
		t.Errorf("expired code: got %d", code)
	}
}

func TestPowerFlow(t *testing.T) {
	e := newEnv(t)
	tok := e.pair()
	if code, out := e.do("GET", "/v1/status", tok, nil); code != 200 || out["pending"] != nil || out["uptimeSeconds"].(float64) < 5000 {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/power/shutdown", tok, nil); code != http.StatusAccepted || out["pending"].(map[string]any)["action"] != "shutdown" {
		t.Fatalf("shutdown: %d %v", code, out)
	}
	if len(e.sys.scheduled) != 1 || e.sys.scheduled[0][:len("shutdown/30s/PowerDeck: Pixel 9 Pro")] != "shutdown/30s/PowerDeck: Pixel 9 Pro" {
		t.Errorf("scheduled: %v", e.sys.scheduled)
	}
	if code, _ := e.do("POST", "/v1/power/restart", tok, nil); code != http.StatusConflict {
		t.Errorf("second action while pending: got %d, want 409", code)
	}
	if _, out := e.do("GET", "/v1/status", tok, nil); out["pending"] == nil {
		t.Errorf("status should show pending")
	}
	if code, _ := e.do("POST", "/v1/power/cancel", tok, nil); code != 200 {
		t.Errorf("cancel: %d", code)
	}
	if code, _ := e.do("POST", "/v1/power/cancel", tok, nil); code != http.StatusConflict {
		t.Errorf("cancel with nothing pending: %d", code)
	}
	if code, _ := e.do("POST", "/v1/power/restart", tok, map[string]int{"delaySeconds": 9999}); code != http.StatusAccepted {
		t.Errorf("restart: %d", code)
	}
	if e.sys.scheduled[1][:len("restart/10m0s")] != "restart/10m0s" {
		t.Errorf("delay should be clamped to 600 s: %v", e.sys.scheduled[1])
	}
	if code, _ := e.do("POST", "/v1/power/format", tok, nil); code != http.StatusNotFound {
		t.Errorf("unknown action: %d", code)
	}
	if code, _ := e.do("GET", "/v1/power/shutdown", tok, nil); code != http.StatusMethodNotAllowed {
		t.Errorf("GET must not trigger power actions: %d", code)
	}
}

func TestAuthAndThrottle(t *testing.T) {
	e := newEnv(t)
	tok := e.pair()
	if code, _ := e.do("GET", "/v1/status", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	e.from = "100.100.1.1:1"
	for i := 0; i < limitFailures; i++ {
		e.do("GET", "/v1/status", "wrong", nil)
	}
	if code, _ := e.do("GET", "/v1/status", tok, nil); code != http.StatusTooManyRequests {
		t.Errorf("after %d failures a valid token from that address should be throttled, got %d", limitFailures, code)
	}
	e.from = "100.101.1.40:1"
	if code, _ := e.do("GET", "/v1/status", tok, nil); code != 200 {
		t.Errorf("other addresses unaffected: %d", code)
	}
	e.from = "100.100.1.1:1"
	e.clock = e.clock.Add(limitWindow + time.Second)
	if code, _ := e.do("GET", "/v1/status", tok, nil); code != 200 {
		t.Errorf("throttle should lift after the window: %d", code)
	}
}

func TestPairingGuessesAreCapped(t *testing.T) {
	e := newEnv(t)
	good := e.pm.Current().Value
	for i := 0; i < pairing.MaxFailures; i++ {
		e.from = netip.AddrPortFrom(netip.MustParseAddr("100.64.0.1").Next(), uint16(1000+i)).String()
		e.do("POST", "/v1/pair", "", map[string]string{"code": "nope"})
	}
	if code, _ := e.do("POST", "/v1/pair", "", map[string]string{"code": good}); code != http.StatusForbidden {
		t.Errorf("code must be discarded after %d wrong guesses, got %d", pairing.MaxFailures, code)
	}
}

func TestUnpairSelf(t *testing.T) {
	e := newEnv(t)
	tok := e.pair()
	if code, _ := e.do("DELETE", "/v1/device", tok, nil); code != http.StatusNoContent {
		t.Fatalf("unpair: %d", code)
	}
	if code, _ := e.do("GET", "/v1/status", tok, nil); code != http.StatusUnauthorized {
		t.Errorf("token must stop working after unpair: %d", code)
	}
}
