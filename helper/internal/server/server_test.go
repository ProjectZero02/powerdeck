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
	"os"
	"path/filepath"
	"slices"
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
	"github.com/projectzero02/powerdeck/helper/internal/update"
)

// Addresses on the fake tailnet.
const (
	phone     = "100.101.1.40:51234" // owner's phone (same Tailscale user)
	friend    = "100.101.1.60:51234" // another account's phone
	atlas     = "100.101.1.50:51234" // tagged agent VM
	atlasTwin = "100.101.1.51:51234" // another tagged node
	selfAddr  = "100.101.1.10:51234" // this device itself
	laptop    = "100.101.1.41:51234" // same Tailscale user, not paired as an owner device
	hubAddr   = "100.101.1.12:51234" // another helper of the owner, untagged (e.g. a server)
)

type fakeTS struct{}

func (fakeTS) Status(context.Context) (*tailscale.Status, error) {
	return &tailscale.Status{BackendState: "Running", Self: tailscale.Node{ID: "nSELF", HostName: "Gaming-PC", UserID: 1},
		Peers: []tailscale.Node{
			{ID: "nATLAS", HostName: "atlas", DNSName: "atlas.example.ts.net", UserID: 99, Tags: []string{"tag:agent"}, IPv4: netip.MustParseAddr("100.101.1.50")},
			{ID: "nGUEST", HostName: "guest", DNSName: "guest.example.ts.net", UserID: 7, Shared: true, IPv4: netip.MustParseAddr("100.101.1.70")},
			{ID: "nLAPTOP", HostName: "Laptop", DNSName: "laptop.example.ts.net", UserID: 1, IPv4: netip.MustParseAddr("100.101.1.41")},
		}}, nil
}

func (fakeTS) WhoIs(_ context.Context, ip netip.Addr) (tailscale.Identity, error) {
	switch ip.String() {
	case "100.101.1.40":
		return tailscale.Identity{NodeID: "nPHONE", NodeName: "oppo", Hostname: "Pixel 9 Pro", UserID: 1}, nil
	case "100.101.1.60":
		return tailscale.Identity{NodeID: "nFRIEND", NodeName: "friend", Hostname: "Friend's phone", UserID: 7}, nil
	case "100.101.1.50":
		return tailscale.Identity{NodeID: "nATLAS", NodeName: "atlas", UserID: 99, Tagged: true}, nil
	case "100.101.1.51":
		return tailscale.Identity{NodeID: "nTWIN", NodeName: "twin", UserID: 99, Tagged: true}, nil
	case "100.101.1.41":
		return tailscale.Identity{NodeID: "nLAPTOP", NodeName: "laptop", Hostname: "Laptop", UserID: 1}, nil
	case "100.101.1.12":
		return tailscale.Identity{NodeID: "nHUB", NodeName: "hub", Hostname: "hub", UserID: 1}, nil
	case "100.101.1.10":
		return tailscale.Identity{NodeID: "nSELF", NodeName: "gaming-pc", Hostname: "Gaming-PC", UserID: 1}, nil
	}
	return tailscale.Identity{}, errors.New("unknown peer")
}

type fakeUpdater struct {
	mu        sync.Mutex
	st        update.Status
	installed chan struct{}
}

func (f *fakeUpdater) Status() update.Status { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *fakeUpdater) Check(context.Context) (update.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Latest, f.st.Available = "2.1.0", true
	f.st.Published, f.st.FirstSeen = testStart.Add(-24*time.Hour), testStart
	return f.st, nil
}
func (f *fakeUpdater) Install(context.Context) error { close(f.installed); return nil }

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
	upd     *fakeUpdater
	removed int // Uninstall calls
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetAutoUpdate(true) // a desktop's default
	_ = st.AddOwner(store.Owner{NodeID: "nPHONE", Name: "Pixel 9 Pro", AddedAt: testStart, AddedBy: "pairing code"})
	return newEnvWith(t, st)
}

func newEnvWith(t *testing.T, st *store.Store) *env {
	t.Helper()
	e := &env{t: t, sys: &fakeSys{}, st: st, clock: testStart, upd: &fakeUpdater{st: update.Status{Current: "2.0.1"}, installed: make(chan struct{})}}
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
	_ = st.PutPeer(store.Peer{Name: "workstation", Address: "100.101.1.11", Port: 47321, Pin: "pin", OS: "windows", MAC: "02:00:5e:00:00:01", LAN: []string{"192.168.1.0/24"}})
	_ = st.PutPeer(store.Peer{Name: "atlas", Address: "100.101.1.50", Port: 47321, Pin: "pin2", MAC: "aa:00:00:00:00:02", LAN: []string{"192.168.1.0/24"}})
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
		Guests: func(context.Context) *guests.Info { return nil },
		Update: e.upd,
		Uninstall: func() error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.removed++
			return nil
		},
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
	code, out := e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": "Atlas", "scopes": scopes, "devices": devices}})
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
	if you["kind"] != auth.Owner || you["name"] != "Pixel 9 Pro" || out["checks"] == nil || out["network"] == nil {
		t.Errorf("status: %v", out)
	}
	if out["uptimeSeconds"].(float64) != 5400 {
		t.Errorf("uptime %v", out["uptimeSeconds"])
	}
	// Another account, a tagged node and an unknown address are nobody.
	for _, from := range []string{friend, atlas, "100.101.9.9:1"} {
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
	// Any program on this device can reach its own tailnet address; that's local too, not the owner.
	for _, path := range []string{"/v2/status", "/v2/keys", "/v2/pairing"} {
		if code, _ := e.do(req{method: "GET", path: path, from: selfAddr}); code != http.StatusUnauthorized {
			t.Errorf("own tailnet address %s: %d", path, code)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/shutdown", from: selfAddr, body: map[string]any{}}); code != http.StatusUnauthorized {
		t.Errorf("own tailnet address shut down: %d", code)
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
	// A paired key controls this device only: no relaying to the fleet, no MCP, no raw wakes.
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/workstation/status", from: friend, key: key}); code != http.StatusForbidden {
		t.Errorf("paired key relayed: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/mcp", from: friend, key: key, body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}}); code != http.StatusForbidden {
		t.Errorf("paired key used MCP: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/wake", from: friend, key: key, body: map[string]any{"mac": "aa:bb:cc:dd:ee:ff"}}); code != http.StatusOK {
		t.Errorf("paired key raw wake (a tagged hub's only way to wake): %d", code)
	}
	// It can't mint a wider key, see or revoke the owner's keys, or schedule waking other devices.
	if code, _ := e.do(req{method: "POST", path: "/v2/keys", from: friend, key: key, body: map[string]any{"name": "x", "scopes": []string{"power"}}}); code != http.StatusForbidden {
		t.Errorf("paired key minted a key: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: friend, key: key}); code != http.StatusForbidden {
		t.Errorf("paired key listed keys: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/pairing", from: friend, key: key}); code != http.StatusForbidden {
		t.Errorf("paired key read a fresh pairing code: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/checks/wol/fix", from: friend, key: key}); code != http.StatusForbidden {
		t.Errorf("paired key changed system settings: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/schedules", from: friend, key: key, body: map[string]any{"action": "wake", "target": "workstation", "time": "07:00"}}); code != http.StatusForbidden {
		t.Errorf("paired key scheduled a wake: %d", code)
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
	if code != http.StatusAccepted || out["pending"].(map[string]any)["by"] != "Pixel 9 Pro" {
		t.Fatalf("forced: %d %v", code, out)
	}
	if !strings.HasPrefix(e.sys.scheduled[0], "shutdown/1m0s/PowerDeck: Pixel 9 Pro asked") {
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
	key := e.agentKey([]string{"wake", "power"}, "Gaming-PC", "workstation", "atlas")

	// First use binds the key to Atlas's node; it's then useless from anywhere else.
	if code, out := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/status", from: atlas, key: key}); code != 200 || out["name"] != "Gaming-PC" {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/status", from: atlasTwin, key: key}); code != http.StatusUnauthorized {
		t.Errorf("key from another node: %d", code)
	}
	// No force, and refused while someone is using the PC.
	e.sys.presence = &power.Presence{Active: true, User: "Alex"}
	if code, out := e.do(req{method: "POST", path: "/v2/power/shutdown", from: atlas, key: key, body: map[string]any{"force": true}}); code != http.StatusForbidden {
		t.Errorf("agent force: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/shutdown", from: atlas, key: key}); code != http.StatusConflict || out["error"] != "in_use" {
		t.Errorf("agent while in use: %d %v", code, out)
	}
	// Devices outside the key's list.
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/cabin/wake", from: atlas, key: key}); code != http.StatusForbidden {
		t.Errorf("device outside key: %d", code)
	}
	// Never the device the agent runs on.
	if code, out := e.do(req{method: "POST", path: "/v2/devices/atlas/shutdown", from: atlas, key: key}); code != http.StatusForbidden || out["error"] != "self" {
		t.Errorf("agent's own device: %d %v", code, out)
	}
	// Relay: shutdown of another helper goes to that helper, named for the agent, with a minute's warning.
	code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/shutdown", from: atlas, key: key})
	// (First it asks workstation whether it lets agents in.)
	if code != 200 || len(e.calls) != 2 || e.calls[0] != (forwarded{"workstation", "GET", "/v2/status", ""}) ||
		e.calls[1] != (forwarded{"workstation", "POST", "/v2/power/shutdown", "Atlas"}) {
		t.Fatalf("relay: %d %v %+v", code, out, e.calls)
	}
	// One shutdown per device per 10 minutes.
	if code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/restart", from: atlas, key: key}); code != http.StatusTooManyRequests {
		t.Errorf("cooldown: %d %v", code, out)
	}
	e.clock = e.clock.Add(11 * time.Minute)
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/workstation/restart", from: atlas, key: key}); code != 200 {
		t.Errorf("after cooldown: %d", code)
	}
	// Admin routes are off limits.
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: atlas, key: key}); code != http.StatusForbidden {
		t.Errorf("agent listing keys: %d", code)
	}
}

func TestAgentCantDodgeSelfRuleWithOtherName(t *testing.T) {
	e := newEnv(t)
	if err := e.st.PutPeer(store.Peer{Name: "atlas", Device: "atlas-vm", DNSName: "atlas.example.ts.net", Address: "100.101.1.50", Port: 47321, Pin: "p"}); err != nil {
		t.Fatal(err)
	}
	key := e.agentKey([]string{"wake", "power"})
	for _, name := range []string{"atlas", "atlas-vm", "ATLAS-VM"} {
		if code, out := e.do(req{method: "POST", path: "/v2/devices/" + name + "/shutdown", from: atlas, key: key}); code != http.StatusForbidden || out["error"] != "self" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// A key limited to a device by one name accepts the other.
	only := e.agentKey([]string{"wake"}, "atlas-vm")
	if code, out := e.do(req{method: "POST", path: "/v2/devices/atlas/status", from: atlasTwin, key: only}); code == http.StatusForbidden {
		t.Errorf("alias refused: %d %v", code, out)
	}
}

func TestKeyDeviceListAppliesToDirectRoutes(t *testing.T) {
	e := newEnv(t)
	key := e.agentKey([]string{"wake", "power"}, "workstation")
	if code, out := e.do(req{method: "POST", path: "/v2/power/shutdown", from: atlas, key: key}); code != http.StatusForbidden || out["error"] != "device" {
		t.Errorf("direct shutdown of an unlisted device: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/wake", from: atlas, key: key, body: map[string]any{"mac": "aa:bb:cc:dd:ee:ff"}}); code != http.StatusForbidden {
		t.Errorf("raw wake with a device list: %d", code)
	}
	if e.sys.scheduled != nil {
		t.Error("shutdown scheduled")
	}
}

func TestStatusOnlyKeyCantActAndWakeQuota(t *testing.T) {
	e := newEnv(t)
	key := e.agentKey(nil) // default: status, wake
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/workstation/shutdown", from: atlas, key: key}); code != http.StatusForbidden {
		t.Errorf("shutdown without power scope: %d", code)
	}
	for i := 0; i < 6; i++ {
		if code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/wake", from: atlas, key: key}); code != 200 {
			t.Fatalf("wake %d: %d %v", i, code, out)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/workstation/wake", from: atlas, key: key}); code != http.StatusTooManyRequests {
		t.Errorf("7th wake in an hour: %d", code)
	}
	if len(e.wakes) != 6 || e.wakes[0] != "02:00:5e:00:00:01" {
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
	if code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/status", from: phone}); code != 200 || out["online"] != false {
		t.Errorf("offline status: %d %v", code, out)
	}
	e.peerErr = &peer.Error{Status: 409, Code: "in_use", Message: "Someone is using this device."}
	if code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/shutdown", from: phone}); code != http.StatusConflict || out["error"] != "in_use" {
		t.Errorf("relayed in_use: %d %v", code, out)
	}
	// The device refusing this helper is a gateway problem, not the phone's key failing.
	e.peerErr = &peer.Error{Status: 401, Code: "unauthorized", Message: "This key isn't valid here any more."}
	if code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/shutdown", from: phone}); code != http.StatusBadGateway || out["error"] != "peer_refused" {
		t.Errorf("relayed 401: %d %v", code, out)
	}
}

func TestOnBehalfOfOnlyFromOwners(t *testing.T) {
	e := newEnv(t)
	h := map[string]string{peer.ForHeader: "Atlas"}
	_, out := e.do(req{method: "GET", path: "/v2/status", from: phone, header: h})
	if out["you"].(map[string]any)["name"] != "Atlas via Pixel 9 Pro" {
		t.Errorf("owner relay name: %v", out["you"])
	}
	key := e.agentKey(nil)
	_, out = e.do(req{method: "GET", path: "/v2/status", from: atlas, key: key, header: map[string]string{peer.ForHeader: "Owner"}})
	if out["you"].(map[string]any)["name"] != "Atlas" {
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
	key := e.agentKey([]string{"wake", "power"}, "workstation")
	init := mcp(e, atlas, key, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize: %v", init)
	}
	if code, _ := e.do(req{method: "POST", path: "/mcp", from: atlas, key: key, body: map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}}); code != http.StatusAccepted {
		t.Errorf("notification: %d", code)
	}
	tools := mcp(e, atlas, key, 2, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("tools: %v", tools)
	}
	for _, tl := range tools {
		m := tl.(map[string]any)
		if m["name"] == "power_off" && m["annotations"].(map[string]any)["destructiveHint"] != true {
			t.Error("power_off not marked destructive")
		}
	}
	list := mcp(e, atlas, key, 3, "tools/call", map[string]any{"name": "list_devices", "arguments": map[string]any{}})
	devs := list["result"].(map[string]any)["structuredContent"].(map[string]any)["devices"].([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["name"] != "workstation" {
		t.Errorf("devices limited to the key: %v", devs)
	}
	on := mcp(e, atlas, key, 4, "tools/call", map[string]any{"name": "power_on", "arguments": map[string]any{"device": "workstation"}})
	if on["result"].(map[string]any)["isError"] != false || len(e.wakes) != 1 {
		t.Errorf("power_on: %v", on)
	}
	denied := mcp(e, atlas, key, 5, "tools/call", map[string]any{"name": "power_off", "arguments": map[string]any{"device": "Gaming-PC"}})
	if denied["result"].(map[string]any)["isError"] != true {
		t.Errorf("power_off outside key: %v", denied)
	}
	if bad := mcp(e, atlas, key, 6, "nope", nil); bad["error"] == nil {
		t.Errorf("unknown method: %v", bad)
	}
	// Without a key the agent's tagged node is nobody.
	if code, _ := e.do(req{method: "POST", path: "/mcp", from: atlas, body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}}); code != http.StatusUnauthorized {
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
	e.api.RunSchedule(context.Background(), store.Schedule{Action: "wake", Target: "workstation", Time: "07:00"})
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

func TestUpdatesFromTheApp(t *testing.T) {
	e := newEnv(t)
	code, out := e.do(req{method: "GET", path: "/v2/status", from: phone})
	if u, _ := out["update"].(map[string]any); code != 200 || u == nil || u["auto"] != true || u["available"] != false {
		t.Fatalf("status update block: %d %v", code, out["update"])
	}
	if code, out := e.do(req{method: "GET", path: "/v2/update?check=1", from: phone}); code != 200 || out["latest"] != "2.1.0" || out["available"] != true {
		t.Errorf("check: %d %v", code, out)
	}
	// Owners only: not a paired key, not an agent.
	pk := pairedKey(t, e)
	for _, k := range []string{pk, e.agentKey([]string{"status", "wake", "power"})} {
		from := friend
		if k != pk {
			from = atlas
		}
		if code, _ := e.do(req{method: "POST", path: "/v2/update", from: from, key: k}); code != http.StatusForbidden {
			t.Errorf("non-owner started an update: %d", code)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"autoUpdate": false}}); code != http.StatusNoContent || e.st.Settings().AutoUpdate {
		t.Errorf("turn auto-update off: %d", code)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/update", from: phone}); code != http.StatusAccepted || out["installing"] != true {
		t.Fatalf("install: %d %v", code, out)
	}
	select {
	case <-e.upd.installed:
	case <-time.After(2 * time.Second):
		t.Error("install never started")
	}
}

func pairedKey(t *testing.T, e *env) string {
	t.Helper()
	code, out := e.do(req{method: "POST", path: "/v2/pair", from: friend, body: map[string]string{"code": e.pm.Current().Value, "deviceName": "Friend's phone"}})
	if code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	return out["key"].(string)
}

func TestAgentsTurnedAway(t *testing.T) {
	e := newEnv(t)
	key := e.agentKey([]string{"wake", "power"}, "Gaming-PC", "workstation", "cabin")
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: atlas, key: key, body: map[string]any{"agents": true}}); code != http.StatusForbidden {
		t.Errorf("agent changing the setting: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"agents": false}}); code != http.StatusNoContent {
		t.Fatalf("owner turning agents away: %d", code)
	}
	_, st := e.do(req{method: "GET", path: "/v2/status", from: phone})
	if a := st["agents"].(map[string]any); a["allowed"] != false || len(a["keys"].([]any)) != 1 {
		t.Errorf("status agents: %v", a)
	}
	for _, path := range []string{"/v2/power/shutdown", "/v2/devices/Gaming-PC/restart"} {
		if code, out := e.do(req{method: "POST", path: path, from: atlas, key: key}); code != http.StatusForbidden || out["error"] != "agents_off" {
			t.Errorf("%s while agents are off: %d %v", path, code, out)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/Gaming-PC/status", from: atlas, key: key}); code != 200 {
		t.Errorf("status while agents are off: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/shutdown", from: phone}); code != http.StatusAccepted {
		t.Errorf("owner while agents are off: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/cancel", from: atlas, key: key}); code != 200 {
		t.Errorf("agent cancelling: %d", code)
	}
	// Another helper that turned agents away: remembered for wakes.
	_ = e.st.PutPeer(store.Peer{Name: "cabin", Address: "100.101.1.70", Port: 47321, Pin: "pin3", MAC: "aa:00:00:00:00:03", LAN: []string{"192.168.1.0/24"}, NoAgents: true})
	if code, out := e.do(req{method: "POST", path: "/v2/devices/cabin/wake", from: atlas, key: key}); code != http.StatusForbidden || out["error"] != "agents_off" {
		t.Errorf("wake a device that turned agents away: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/workstation/wake", from: atlas, key: key}); code != 200 {
		t.Errorf("wake a device that allows agents: %d", code)
	}
	anyKey := e.agentKey([]string{"wake"})
	if code, out := e.do(req{method: "POST", path: "/v2/wake", from: atlas, key: anyKey, body: map[string]any{"mac": "AA:00:00:00:00:03"}}); code != http.StatusForbidden {
		t.Errorf("raw wake of a device that turned agents away: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/wake", from: atlas, key: anyKey, body: map[string]any{"mac": "aa-00-00-00-00-03"}}); code != http.StatusForbidden {
		t.Errorf("raw wake spelled with dashes: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/wake", from: atlas, key: anyKey, body: map[string]any{"mac": "not-a-mac"}}); code != http.StatusBadRequest {
		t.Errorf("raw wake of nonsense: %d", code)
	}
	// A hub relaying an agent's request: the agent mark comes along and this helper refuses too.
	agent := map[string]string{peer.ForHeader: "Atlas", peer.AgentHeader: "1"}
	if code, out := e.do(req{method: "POST", path: "/v2/power/shutdown", from: phone, header: agent}); code != http.StatusForbidden || out["error"] != "agents_off" {
		t.Errorf("relayed agent while agents are off: %d %v", code, out)
	}
}

func TestUninstall(t *testing.T) {
	e := newEnv(t)
	_, st := e.do(req{method: "GET", path: "/v2/status", from: phone})
	if !slices.Contains(st["caps"].([]any), any("uninstall")) {
		t.Errorf("caps %v", st["caps"])
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/uninstall", from: phone, body: map[string]any{"confirm": "workstation"}}); code != http.StatusBadRequest {
		t.Errorf("wrong name: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/uninstall", from: friend, body: map[string]any{"confirm": "Gaming-PC"}}); code/100 != 4 {
		t.Errorf("another account: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/uninstall", from: phone, body: map[string]any{"confirm": "gaming-pc"}}); code != http.StatusAccepted {
		t.Fatalf("uninstall: %d", code)
	}
	for i := 0; i < 50; i++ {
		e.mu.Lock()
		n := e.removed
		e.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("Uninstall wasn't called")
}

func TestOwnerDevices(t *testing.T) {
	e := newEnv(t)
	// Same Tailscale account but never paired: nobody.
	if code, out := e.do(req{method: "GET", path: "/v2/status", from: laptop}); code != http.StatusUnauthorized || out["error"] != "not_owner" {
		t.Fatalf("unpaired laptop: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/pair", from: laptop, body: map[string]any{"code": "WRONG-CODE"}}); code != http.StatusForbidden {
		t.Errorf("wrong code: %d", code)
	}
	code, out := e.do(req{method: "POST", path: "/v2/pair", from: laptop, body: map[string]any{"code": e.pm.Current().Value, "deviceName": "Laptop"}})
	if code != 200 || out["owner"] != true || out["key"] != nil {
		t.Fatalf("pair same account: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: laptop}); code != 200 {
		t.Errorf("new owner device can't manage: %d", code)
	}
	_, list := e.do(req{method: "GET", path: "/v2/owners", from: phone})
	if owners := list["owners"].([]any); len(owners) != 2 || list["legacy"] != false {
		t.Errorf("owners: %v", list)
	}
	// Another account: a key for this device, without admin.
	code, out = e.do(req{method: "POST", path: "/v2/pair", from: friend, body: map[string]any{"code": e.pm.Current().Value, "deviceName": "Friend"}})
	if code != 200 || out["key"] == nil {
		t.Fatalf("pair friend: %d %v", code, out)
	}
	fk := out["key"].(string)
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: friend, key: fk}); code != http.StatusForbidden {
		t.Errorf("friend managing keys: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/shutdown", from: friend, key: fk}); code != http.StatusAccepted {
		t.Errorf("friend shutting down: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/owners/claim", from: friend, key: fk}); code != http.StatusForbidden {
		t.Errorf("friend claiming: %d", code)
	}
	// Removing an owner device.
	if code, _ := e.do(req{method: "DELETE", path: "/v2/owners/nLAPTOP", from: phone}); code != http.StatusNoContent {
		t.Errorf("remove owner: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: laptop}); code != http.StatusUnauthorized {
		t.Errorf("removed owner still in: %d", code)
	}
}

func TestLegacyOwnersUntilClaimed(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"keys":[],"settings":{"autoUpdate":true}}`), 0o600)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, st)
	// Upgraded helper: the old rule still holds.
	code, out := e.do(req{method: "GET", path: "/v2/status", from: laptop})
	if code != 200 || out["owners"].(map[string]any)["legacy"] != true {
		t.Fatalf("legacy status: %d %v", code, out["owners"])
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: friend}); code != http.StatusUnauthorized {
		t.Errorf("other account in legacy mode: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: atlas}); code != http.StatusUnauthorized {
		t.Errorf("tagged node in legacy mode: %d", code)
	}
	// The first claim ends it.
	if code, _ := e.do(req{method: "POST", path: "/v2/owners/claim", from: laptop, body: map[string]any{"deviceName": "Laptop"}}); code != http.StatusNoContent {
		t.Fatalf("claim: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: phone}); code != http.StatusUnauthorized {
		t.Errorf("other same-account device after the claim: %d", code)
	}
	if code, out := e.do(req{method: "GET", path: "/v2/status", from: laptop}); code != 200 || out["owners"].(map[string]any)["legacy"] != false {
		t.Errorf("claimed owner: %d %v", code, out["owners"])
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/owners/claim", from: "127.0.0.1:5000", key: "pdl_local"}); code != http.StatusBadRequest {
		t.Errorf("local tools claiming: %d", code)
	}
}

func TestPeerKeys(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: hubAddr}); code != http.StatusUnauthorized {
		t.Fatalf("hub without a key: %d", code)
	}
	code, out := e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": "hub", "kind": "peer"}})
	if code != 200 {
		t.Fatalf("peer key: %d %v", code, out)
	}
	pk := out["key"].(string)
	code, st := e.do(req{method: "GET", path: "/v2/status", from: hubAddr, key: pk})
	if code != 200 || st["agents"].(map[string]any)["allowed"] != true || st["checks"] != nil || st["owners"] != nil {
		t.Fatalf("peer status: %d %v", code, st)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/power/shutdown", from: hubAddr, key: pk, header: map[string]string{peer.ForHeader: "Atlas"}}); code != http.StatusAccepted {
		t.Errorf("peer shutdown: %d", code)
	}
	if ev := e.st.Events(); !strings.HasPrefix(ev[0].Who, "Atlas via ") {
		t.Errorf("relayed name not shown: %v", ev[0])
	}
	for _, r := range []req{
		{method: "GET", path: "/v2/keys"},
		{method: "POST", path: "/v2/settings", body: map[string]any{"agents": true}},
		{method: "POST", path: "/v2/devices/workstation/status"},
		{method: "POST", path: "/mcp"},
		{method: "GET", path: "/v2/owners"},
	} {
		r.from, r.key = hubAddr, pk
		if code, _ := e.do(r); code != http.StatusForbidden {
			t.Errorf("peer %s %s: %d", r.method, r.path, code)
		}
	}
	// Bound to the hub's node.
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: atlas, key: pk}); code != http.StatusUnauthorized {
		t.Errorf("peer key from another node: %d", code)
	}
	// The agent mark from a peer counts.
	_, _ = e.do(req{method: "POST", path: "/v2/power/cancel", from: phone})
	_, _ = e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"agents": false}})
	if code, out := e.do(req{method: "POST", path: "/v2/power/shutdown", from: hubAddr, key: pk, header: map[string]string{peer.AgentHeader: "1"}}); code != http.StatusForbidden || out["error"] != "agents_off" {
		t.Errorf("peer relaying for an agent while agents are off: %d %v", code, out)
	}
	// A new peer key replaces the old one.
	_, out = e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": "hub", "kind": "peer"}})
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: hubAddr, key: pk}); code != http.StatusUnauthorized {
		t.Errorf("replaced peer key still works: %d", code)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/status", from: hubAddr, key: out["key"].(string)}); code != 200 {
		t.Errorf("new peer key: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": "x", "kind": "phone"}}); code != http.StatusBadRequest {
		t.Errorf("other kinds: %d", code)
	}
	// Storing the key this helper uses for a peer.
	if code, _ := e.do(req{method: "PUT", path: "/v2/peers/workstation/key", from: phone, body: map[string]any{"key": "pdk_abc"}}); code != http.StatusNoContent {
		t.Errorf("set peer key: %d", code)
	}
	if p, _ := e.st.Peer("workstation"); p.Key != "pdk_abc" {
		t.Errorf("stored peer key: %+v", p)
	}
	if code, _ := e.do(req{method: "PUT", path: "/v2/peers/nowhere/key", from: phone, body: map[string]any{"key": "pdk_abc"}}); code != http.StatusNotFound {
		t.Errorf("unknown peer: %d", code)
	}
	if code, _ := e.do(req{method: "PUT", path: "/v2/peers/workstation/key", from: phone, body: map[string]any{"key": "nope"}}); code != http.StatusBadRequest {
		t.Errorf("not a key: %d", code)
	}
	_, st = e.do(req{method: "GET", path: "/v2/status", from: phone})
	if pks := st["peerKeys"].([]any); len(pks) != 1 || pks[0] != "workstation" {
		t.Errorf("peerKeys: %v", st["peerKeys"])
	}
}

func TestAddOwnerByName(t *testing.T) {
	e := newEnv(t)
	local := req{method: "POST", path: "/v2/owners", from: "127.0.0.1:5000", key: "pdl_local"}
	for name, want := range map[string]int{"atlas": http.StatusBadRequest, "guest": http.StatusBadRequest, "nobody": http.StatusNotFound, "": http.StatusBadRequest} {
		r := local
		r.body = map[string]any{"name": name}
		if code, _ := e.do(r); code != want {
			t.Errorf("owner add %q: %d, want %d", name, code, want)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/owners", from: friend, body: map[string]any{"name": "laptop"}}); code != http.StatusUnauthorized {
		t.Errorf("a stranger adding an owner: %d", code)
	}
	local.body = map[string]any{"name": "LAPTOP"}
	if code, out := e.do(local); code != 200 || out["node"] != "nLAPTOP" {
		t.Fatalf("owner add laptop: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "GET", path: "/v2/keys", from: laptop}); code != 200 {
		t.Errorf("added owner can't manage: %d", code)
	}
}

func TestUpdateStagingAndPause(t *testing.T) {
	e := newEnv(t)
	_, out := e.do(req{method: "GET", path: "/v2/update?check=1", from: phone})
	if at, _ := time.Parse(time.RFC3339, out["autoAt"].(string)); !at.Equal(testStart.Add(48*time.Hour)) || out["pausedUntil"] != nil {
		t.Fatalf("staged install time: %v", out)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: atlas, key: e.agentKey([]string{"status", "wake", "power"}), body: map[string]any{"pauseUpdates": true}}); code != http.StatusForbidden {
		t.Errorf("agent pausing updates: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"pauseUpdates": true}}); code != http.StatusNoContent {
		t.Fatalf("pause: %d", code)
	}
	week := testStart.Add(7 * 24 * time.Hour)
	if !e.st.Settings().UpdatesPausedUntil.Equal(week) {
		t.Errorf("paused until %v", e.st.Settings().UpdatesPausedUntil)
	}
	_, out = e.do(req{method: "GET", path: "/v2/update", from: phone})
	if at, _ := time.Parse(time.RFC3339, out["autoAt"].(string)); !at.Equal(week) || out["pausedUntil"] == nil {
		t.Errorf("paused: %v", out)
	}
	_, _ = e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"pauseUpdates": false}})
	if !e.st.Settings().UpdatesPausedUntil.IsZero() {
		t.Error("resume didn't clear the pause")
	}
	_, _ = e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"autoUpdate": false}})
	if _, out = e.do(req{method: "GET", path: "/v2/update", from: phone}); out["autoAt"] != nil {
		t.Errorf("auto off but autoAt: %v", out)
	}
}
