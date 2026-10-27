package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/discovery"
	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/guests"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/pairing"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
)

// Addresses on the fake tailnet.
const (
	phone    = "100.101.1.40:51234" // owner's phone (same Tailscale user)
	friend   = "100.101.1.60:51234" // another account's phone
	lyra     = "100.101.1.50:51234" // tagged agent VM
	lyraTwin = "100.101.1.51:51234" // another tagged node
	selfAddr = "100.101.1.10:51234" // this device itself
)

type fakeTS struct{}

func (fakeTS) Status(context.Context) (*tailscale.Status, error) {
	return &tailscale.Status{BackendState: "Running", Self: tailscale.Node{ID: "nSELF", HostName: "Gaming-PC", UserID: 1}}, nil
}

func (fakeTS) WhoIs(_ context.Context, ip netip.Addr) (tailscale.Identity, error) {
	switch ip.String() {
	case "100.101.1.40":
		return tailscale.Identity{NodeID: "nPHONE", NodeName: "oppo", Hostname: "OPPO Find X9 Ultra", UserID: 1}, nil
	case "100.101.1.60":
		return tailscale.Identity{NodeID: "nFRIEND", NodeName: "friend", Hostname: "Friend's phone", UserID: 7}, nil
	case "100.101.1.50":
		return tailscale.Identity{NodeID: "nLYRA", NodeName: "lyra", UserID: 99, Tagged: true}, nil
	case "100.101.1.51":
		return tailscale.Identity{NodeID: "nTWIN", NodeName: "twin", UserID: 99, Tagged: true}, nil
	case "100.101.1.10":
		return tailscale.Identity{NodeID: "nSELF", NodeName: "gaming-pc", Hostname: "Gaming-PC", UserID: 1}, nil
	}
	return tailscale.Identity{}, errors.New("unknown peer")
}

type fakeSys struct {
	mu        sync.Mutex
	scheduled []string
	pending   bool
	presence  *power.Presence
}

func (f *fakeSys) Schedule(a power.Action, d time.Duration, msg string, upd bool) error {
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

var testStart = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func (f *fakeSys) BootTime() (time.Time, error) { return testStart.Add(-90 * time.Minute), nil }
func (f *fakeSys) Presence() *power.Presence    { f.mu.Lock(); defer f.mu.Unlock(); return f.presence }
func (f *fakeSys) UpdatesPending() bool         { return false }

type forwarded struct{ peer, method, path, who string }

type env struct {
	t       *testing.T
	api     *API
	pm      *pairing.Manager
	sys     *fakeSys
	st      *store.Store
	clock   time.Time
	mu      sync.Mutex
	calls   []forwarded
	wakes   []string
	peerErr error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, sys: &fakeSys{}, st: st, clock: testStart}
	now := func() time.Time { return e.clock }
	e.pm = pairing.NewManager(now)
	lan := func() (netinfo.Network, bool) {
		return netinfo.Network{Interface: "eth0", MAC: "d8:5e:d3:12:34:56", LAN: []string{"192.168.1.0/24"}, Wired: true}, true
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wake := func(mac string, lans []string) ([]string, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.wakes = append(e.wakes, mac)
		return []string{"192.168.1.255"}, nil
	}
	fl := fleet.New(fleet.Deps{TS: fakeTS{}, Store: st, Network: lan, Log: log, Now: now, Wake: wake,
		Call: func(_ context.Context, p store.Peer, method, path string, body, out any, who string) error {
			e.mu.Lock()
			e.calls = append(e.calls, forwarded{p.Name, method, path, who})
			err := e.peerErr
			e.mu.Unlock()
			if err != nil {
				return err
			}
			if out != nil {
				b, _ := json.Marshal(map[string]any{"name": p.Name, "pending": map[string]any{"action": "shutdown"}})
				return json.Unmarshal(b, out)
			}
			return nil
		}})
	_ = st.PutPeer(store.Peer{Name: "za-worldo", Address: "100.101.1.11", Port: 47321, Pin: "pin", OS: "windows", MAC: "c8:7f:54:00:00:01", LAN: []string{"192.168.1.0/24"}})
	_ = st.PutPeer(store.Peer{Name: "lyra", Address: "100.101.1.50", Port: 47321, Pin: "pin2", MAC: "aa:00:00:00:00:02", LAN: []string{"192.168.1.0/24"}})
	_ = st.PutPeer(store.Peer{Name: "cabin", Address: "100.101.1.70", Port: 47321, Pin: "pin3", MAC: "aa:00:00:00:00:03", LAN: []string{"10.9.0.0/24"}})
	e.api = New(Deps{
		Auth:  &auth.Authenticator{TS: fakeTS{}, Store: st, Now: now, LocalToken: "pdl_local"},
		Store: st, Pairing: e.pm, Power: power.NewController(e.sys, now), Fleet: fl,
		Name: "Gaming-PC", OS: "windows",
		SelfNode: func(context.Context) string { return "nSELF" },
		Network:  lan,
		Checks: func(context.Context) []check.Result {
			return []check.Result{{ID: "tailscale", Level: check.OK, Title: "Connected"}}
		},
		Fix: func(context.Context, string) (check.Result, error) {
			return check.Result{ID: "x", Level: check.OK}, nil
		},
		Guests:   func(context.Context) *guests.Info { return nil },
		Wake:     wake,
		PairLink: func() (string, string) { return "powerdeck://add?v=2", e.pm.Current().Value },
		Log:      log, Now: now,
	})
	return e
}

type req struct {
	method, path, from, key string
	body                    any
	header                  map[string]string
}

func (e *env) do(r req) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if r.body != nil {
		b, _ := json.Marshal(r.body)
		rd = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(r.method, r.path, rd)
	hr.RemoteAddr = r.from
	if r.key != "" {
		hr.Header.Set("Authorization", "Bearer "+r.key)
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.api.ServeHTTP(w, hr)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *env) agentKey(scopes []string, devices ...string) string {
	e.t.Helper()
	code, out := e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": "Lyra", "scopes": scopes, "devices": devices}})
	if code != 200 {
		e.t.Fatalf("create key: %d %v", code, out)
	}
	return out["key"].(string)
}

func TestSourcePolicyAndOrigin(t *testing.T) {
	e := newEnv(t)
	for _, from := range []string{"192.168.1.31:5000", "8.8.8.8:443", "[2001:db8::1]:80"} {
		if code, _ := e.do(req{method: "GET", path: "/v2/hello", from: from}); code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", from, code)
		}
	}
	if code, out := e.do(req{method: "GET", path: "/v2/hello", from: friend}); code != 200 || out["app"] != "PowerDeck" {
		t.Errorf("hello: %d %v", code, out)
	}
	code, _ := e.do(req{method: "GET", path: "/v2/status", from: phone, header: map[string]string{"Origin": "https://evil.example"}})
	if code != http.StatusForbidden {
		t.Errorf("browser request: %d", code)
	}
}

func TestOwnerIsTrustedByTailscaleIdentity(t *testing.T) {
	e := newEnv(t)
	code, out := e.do(req{method: "GET", path: "/v2/status", from: phone})
	if code != 200 {
		t.Fatalf("owner status: %d %v", code, out)
	}
	you := out["you"].(map[string]any)
	if you["kind"] != auth.Owner || you["name"] != "OPPO Find X9 Ultra" || out["checks"] == nil || out["network"] == nil {
		t.Errorf("status: %v", out)
	}
	if out["uptimeSeconds"].(float64) != 5400 {
		t.Errorf("uptime %v", out["uptimeSeconds"])
	}
	// Another account, a tagged node and an unknown address are nobody.
	for _, from := range []string{friend, lyra, "100.101.9.9:1"} {
		if code, out := e.do(req{method: "GET", path: "/v2/status", from: from}); code != http.StatusUnauthorized || out["error"] != "not_owner" {
			t.Errorf("%s: %d %v", from, code, out)
		}
	}
	// Loopback needs the local token.
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: "127.0.0.1:1"}); code != http.StatusUnauthorized {
		t.Errorf("loopback without token: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: "127.0.0.1:1", key: "pdl_local"}); code != 200 {
		t.Errorf("loopback with token: %d", code)
	}
}

func TestPairingCodeForAnotherAccount(t *testing.T) {
	e := newEnv(t)
	code, out := e.do(req{method: "POST", path: "/v2/pair", from: friend, body: map[string]string{"code": e.pm.Current().Value, "deviceName": "Friend's phone"}})
	if code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	key := out["key"].(string)
	if code, _ := e.do(req{method: "POST", path: "/v2/pair", from: friend, body: map[string]string{"code": "AAAA-AAAA"}}); code != http.StatusForbidden {
		t.Errorf("bad code: %d", code)
	}
	if code, out := e.do(req{method: "GET", path: "/v2/status", from: friend, key: key}); code != 200 || out["you"].(map[string]any)["kind"] != auth.Holder {
		t.Errorf("status with key: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "DELETE", path: "/v2/key", from: friend, key: key}); code != http.StatusNoContent {
		t.Errorf("revoke own: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: friend, key: key}); code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", code)
	}
}

func TestShutdownRespectsPresence(t *testing.T) {
	e := newEnv(t)
	e.sys.presence = &power.Presence{Active: true, User: "Alex", IdleSeconds: 30, Fullscreen: true}
	code, out := e.do(req{method: "POST", path: "/v2/power/shutdown", from: phone})
	if code != http.StatusConflict || out["error"] != "in_use" || out["presence"].(map[string]any)["user"] != "Alex" {
		t.Fatalf("in use: %d %v", code, out)
	}
	code, out = e.do(req{method: "POST", path: "/v2/power/shutdown", from: phone, body: map[string]any{"force": true}})
	if code != http.StatusAccepted || out["pending"].(map[string]any)["by"] != "OPPO Find X9 Ultra" {
		t.Fatalf("forced: %d %v", code, out)
	}
	if !strings.HasPrefix(e.sys.scheduled[0], "shutdown/1m0s/PowerDeck: OPPO Find X9 Ultra asked") {
		t.Errorf("scheduled %v", e.sys.scheduled)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/restart", from: phone, body: map[string]any{"force": true}}); code != http.StatusConflict {
		t.Errorf("second action: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/cancel", from: phone}); code != 200 {
		t.Errorf("cancel: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/cancel", from: phone}); code != http.StatusConflict {
		t.Errorf("cancel nothing: %d", code)
	}
	ev := e.st.Events()
	if len(ev) < 3 || ev[0].Action != "cancel" {
		t.Errorf("events %+v", ev)
	}
}

func TestAutomationKeyRules(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": "x", "scopes": []string{"force"}}}); code != http.StatusBadRequest {
		t.Errorf("force scope allowed: %d", code)
	}
	key := e.agentKey([]string{"wake", "power"}, "Gaming-PC", "za-worldo", "lyra")

	// First use binds the key to Lyra's node; it's then useless from anywhere else.
	if code, out := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/status", from: lyra, key: key}); code != 200 || out["name"] != "Gaming-PC" {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/status", from: lyraTwin, key: key}); code != http.StatusUnauthorized {
		t.Errorf("key from another node: %d", code)
	}
	// No force, and refused while someone is using the PC.
	e.sys.presence = &power.Presence{Active: true, User: "Alex"}
	if code, out := e.do(req{method: "POST", path: "/v2/power/shutdown", from: lyra, key: key, body: map[string]any{"force": true}}); code != http.StatusForbidden {
		t.Errorf("agent force: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/shutdown", from: lyra, key: key}); code != http.StatusConflict || out["error"] != "in_use" {
		t.Errorf("agent while in use: %d %v", code, out)
	}
	// Devices outside the key's list.
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/cabin/wake", from: lyra, key: key}); code != http.StatusForbidden {
		t.Errorf("device outside key: %d", code)
	}
	// Never the device the agent runs on.
	if code, out := e.do(req{method: "POST", path: "/v2/devices/lyra/shutdown", from: lyra, key: key}); code != http.StatusForbidden || out["error"] != "self" {
		t.Errorf("agent's own device: %d %v", code, out)
	}
	// Relay: shutdown of another helper goes to that helper, named for the agent, with a minute's warning.
	code, out := e.do(req{method: "POST", path: "/v2/devices/za-worldo/shutdown", from: lyra, key: key})
	if code != 200 || len(e.calls) != 1 || e.calls[0] != (forwarded{"za-worldo", "POST", "/v2/power/shutdown", "Lyra"}) {
		t.Fatalf("relay: %d %v %+v", code, out, e.calls)
	}
	// One shutdown per device per 10 minutes.
	if code, out := e.do(req{method: "POST", path: "/v2/devices/za-worldo/restart", from: lyra, key: key}); code != http.StatusTooManyRequests {
		t.Errorf("cooldown: %d %v", code, out)
	}
	e.clock = e.clock.Add(11 * time.Minute)
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/za-worldo/restart", from: lyra, key: key}); code != 200 {
		t.Errorf("after cooldown: %d", code)
	}
	// Admin routes are off limits.
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: lyra, key: key}); code != http.StatusForbidden {
		t.Errorf("agent listing keys: %d", code)
	}
}

func TestStatusOnlyKeyCantActAndWakeQuota(t *testing.T) {
	e := newEnv(t)
	key := e.agentKey(nil) // default: status, wake
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/za-worldo/shutdown", from: lyra, key: key}); code != http.StatusForbidden {
		t.Errorf("shutdown without power scope: %d", code)
	}
	for i := 0; i < 6; i++ {
		if code, out := e.do(req{method: "POST", path: "/v2/devices/za-worldo/wake", from: lyra, key: key}); code != 200 {
			t.Fatalf("wake %d: %d %v", i, code, out)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/za-worldo/wake", from: lyra, key: key}); code != http.StatusTooManyRequests {
		t.Errorf("7th wake in an hour: %d", code)
	}
	if len(e.wakes) != 6 || e.wakes[0] != "c8:7f:54:00:00:01" {
		t.Errorf("wakes %v", e.wakes)
	}
}

func TestRelayErrors(t *testing.T) {
	e := newEnv(t)
	if code, out := e.do(req{method: "POST", path: "/v2/devices/cabin/wake", from: phone}); code != http.StatusUnprocessableEntity || out["error"] != "cant_wake" {
		t.Errorf("other LAN: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/nope/status", from: phone}); code != http.StatusNotFound {
		t.Errorf("unknown: %d", code)
	}
	e.peerErr = fleet.ErrOffline
	if code, out := e.do(req{method: "POST", path: "/v2/devices/za-worldo/status", from: phone}); code != 200 || out["online"] != false {
		t.Errorf("offline status: %d %v", code, out)
	}
	e.peerErr = &peer.Error{Status: 409, Code: "in_use", Message: "Someone is using this device."}
	if code, out := e.do(req{method: "POST", path: "/v2/devices/za-worldo/shutdown", from: phone}); code != http.StatusConflict || out["error"] != "in_use" {
		t.Errorf("relayed in_use: %d %v", code, out)
	}
}

func TestOnBehalfOfOnlyFromOwners(t *testing.T) {
	e := newEnv(t)
	h := map[string]string{peer.ForHeader: "Lyra"}
	_, out := e.do(req{method: "GET", path: "/v2/status", from: phone, header: h})
	if out["you"].(map[string]any)["name"] != "Lyra via OPPO Find X9 Ultra" {
		t.Errorf("owner relay name: %v", out["you"])
	}
	key := e.agentKey(nil)
	_, out = e.do(req{method: "GET", path: "/v2/status", from: lyra, key: key, header: map[string]string{peer.ForHeader: "Owner"}})
	if out["you"].(map[string]any)["name"] != "Lyra" {
		t.Errorf("key holder spoofed name: %v", out["you"])
	}
}

func mcp(e *env, from, key string, id int, method string, params any) map[string]any {
	e.t.Helper()
	code, out := e.do(req{method: "POST", path: "/mcp", from: from, key: key,
		body: map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}})
	if code != 200 {
		e.t.Fatalf("mcp %s: %d %v", method, code, out)
	}
	return out
}

func TestMCP(t *testing.T) {
	e := newEnv(t)
	key := e.agentKey([]string{"wake", "power"}, "za-worldo")
	init := mcp(e, lyra, key, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize: %v", init)
	}
	if code, _ := e.do(req{method: "POST", path: "/mcp", from: lyra, key: key, body: map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}}); code != http.StatusAccepted {
		t.Errorf("notification: %d", code)
	}
	tools := mcp(e, lyra, key, 2, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("tools: %v", tools)
	}
	for _, tl := range tools {
		m := tl.(map[string]any)
		if m["name"] == "power_off" && m["annotations"].(map[string]any)["destructiveHint"] != true {
			t.Error("power_off not marked destructive")
		}
	}
	list := mcp(e, lyra, key, 3, "tools/call", map[string]any{"name": "list_devices", "arguments": map[string]any{}})
	devs := list["result"].(map[string]any)["structuredContent"].(map[string]any)["devices"].([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["name"] != "za-worldo" {
		t.Errorf("devices limited to the key: %v", devs)
	}
	on := mcp(e, lyra, key, 4, "tools/call", map[string]any{"name": "power_on", "arguments": map[string]any{"device": "za-worldo"}})
	if on["result"].(map[string]any)["isError"] != false || len(e.wakes) != 1 {
		t.Errorf("power_on: %v", on)
	}
	denied := mcp(e, lyra, key, 5, "tools/call", map[string]any{"name": "power_off", "arguments": map[string]any{"device": "Gaming-PC"}})
	if denied["result"].(map[string]any)["isError"] != true {
		t.Errorf("power_off outside key: %v", denied)
	}
	if bad := mcp(e, lyra, key, 6, "nope", nil); bad["error"] == nil {
		t.Errorf("unknown method: %v", bad)
	}
	// Without a key the agent's tagged node is nobody.
	if code, _ := e.do(req{method: "POST", path: "/mcp", from: lyra, body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}}); code != http.StatusUnauthorized {
		t.Errorf("mcp without key: %d", code)
	}
}

func TestThrottle(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < limitFailures; i++ {
		e.do(req{method: "GET", path: "/v2/status", from: friend, key: "pdk_wrong"})
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/hello", from: friend}); code != http.StatusTooManyRequests {
		t.Errorf("after failures: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/hello", from: phone}); code != 200 {
		t.Errorf("other address blocked: %d", code)
	}
	e.clock = e.clock.Add(limitWindow + time.Second)
	if code, _ := e.do(req{method: "GET", path: "/v2/hello", from: friend}); code != 200 {
		t.Errorf("after window: %d", code)
	}
}

func TestSchedules(t *testing.T) {
	e := newEnv(t)
	code, out := e.do(req{method: "POST", path: "/v2/schedules", from: phone, body: map[string]any{"action": "shutdown", "time": "01:00", "skipIfInUse": true, "enabled": true}})
	if code != 200 {
		t.Fatalf("add: %d %v", code, out)
	}
	sc := out["schedule"].(map[string]any)
	if len(sc["days"].([]any)) != 7 || sc["target"] != "Gaming-PC" {
		t.Errorf("normalised: %v", sc)
	}
	for _, bad := range []map[string]any{
		{"action": "sleep", "time": "01:00"}, {"action": "shutdown", "time": "25:00"},
		{"action": "wake", "time": "07:00"}, {"action": "wake", "target": "unknown", "time": "07:00"},
	} {
		if code, _ := e.do(req{method: "POST", path: "/v2/schedules", from: phone, body: bad}); code != http.StatusBadRequest {
			t.Errorf("%v: %d", bad, code)
		}
	}
	// A schedule skips while someone's using the device, and says so in the log.
	e.sys.presence = &power.Presence{Active: true}
	e.api.RunSchedule(context.Background(), store.Schedule{Action: "shutdown", Time: "01:00", SkipIfInUse: true})
	if len(e.sys.scheduled) != 0 || !strings.HasPrefix(e.st.Events()[0].Result, "skipped") {
		t.Errorf("skip: %v %+v", e.sys.scheduled, e.st.Events()[0])
	}
	e.api.RunSchedule(context.Background(), store.Schedule{Action: "wake", Target: "za-worldo", Time: "07:00"})
	if len(e.wakes) != 1 {
		t.Errorf("wake schedule: %v", e.wakes)
	}
}

func TestPeersEndpoint(t *testing.T) {
	e := newEnv(t)
	code, out := e.do(req{method: "GET", path: "/v2/peers", from: phone})
	if code != 200 || out["peers"] == nil {
		t.Errorf("peers: %d %v", code, out)
	}
	_ = discovery.KindHelper
}
