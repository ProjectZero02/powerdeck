package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/secret"
)

// hubFleet is the test fleet, with workstation's helper answering while the fake KVM's computer is up.
type hubFleet struct {
	*fleet.Fleet
	kvm *fakekvm.Fake
}

func (f hubFleet) Answers(_ context.Context, name string) bool {
	return strings.EqualFold(name, "workstation") && f.kvm.Up()
}

const kvmPassword = "kvm-Pass-4711"

// withHub makes this helper the hub for a fake NanoKVM wired to workstation, linked by the phone.
func (e *env) withHub(o fakekvm.Options) *fakekvm.Fake {
	e.t.Helper()
	o.TimeScale = 0.001
	o.Password = kvmPassword
	k := fakekvm.Start(o)
	e.t.Cleanup(k.Close)
	h := &machines.Hub{Store: e.st, Box: &secret.KeyFile{Path: filepath.Join(e.t.TempDir(), "secret.key")},
		Fleet: hubFleet{e.api.s.Fleet, k}, Self: "Gaming-PC", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Poll: 5 * time.Millisecond, Settle: 2 * time.Second, OffWait: 5 * time.Millisecond,
		ResetVerify: 2 * time.Second, VerifyEvery: 10 * time.Millisecond}
	e.t.Cleanup(h.Wait)
	e.api.s.Machines = h
	return k
}

func (e *env) linkKVM(k *fakekvm.Fake) {
	e.t.Helper()
	code, out := e.do(req{method: "POST", path: "/v2/kvms", from: phone, body: map[string]any{
		"name": "kvm-2", "address": k.URL, "password": kvmPassword, "machine": "workstation"}})
	if code != http.StatusCreated {
		e.t.Fatalf("link: %d %v", code, out)
	}
}

func TestKVMRoutes(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do(req{method: "GET", path: "/v2/kvms", from: phone}); code != http.StatusNotFound {
		t.Errorf("no hub: %d", code)
	}
	k := e.withHub(fakekvm.Options{On: true})

	// Only the owner's devices link KVMs: not agents, not a pairing-code key.
	body := map[string]any{"name": "kvm-2", "address": k.URL, "password": kvmPassword, "machine": "workstation"}
	agent := e.agentKey([]string{"wake", "power"})
	if code, _ := e.do(req{method: "POST", path: "/v2/kvms", from: atlas, key: agent, body: body}); code != http.StatusForbidden {
		t.Errorf("agent linked a KVM: %d", code)
	}
	paired := pairedKey(t, e)
	if code, _ := e.do(req{method: "POST", path: "/v2/kvms", from: friend, key: paired, body: body}); code != http.StatusForbidden {
		t.Errorf("paired key linked a KVM: %d", code)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/kvms", from: phone, body: map[string]any{"name": "kvm-2", "address": k.URL, "password": "wrong", "machine": "workstation"}}); code != http.StatusUnprocessableEntity || out["error"] != "kvm_password" {
		t.Errorf("wrong password: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/kvms", from: phone, body: map[string]any{"name": "kvm-2", "address": k.URL, "password": kvmPassword, "machine": "Gaming-PC"}}); code/100 != 4 {
		t.Errorf("KVM on the hub itself: %d %v", code, out)
	}
	e.linkKVM(k)

	// The password never comes back, and isn't stored in the clear.
	for _, r := range []req{{method: "GET", path: "/v2/kvms", from: phone}, {method: "GET", path: "/v2/machines", from: phone}} {
		hr := e.raw(r)
		if strings.Contains(strings.ToLower(hr), "password") || strings.Contains(hr, kvmPassword) {
			t.Errorf("%s shows the password: %s", r.path, hr)
		}
	}
	if b, err := os.ReadFile(e.st.Path()); err != nil || strings.Contains(string(b), kvmPassword) || !strings.Contains(string(b), `"password": "k1:`) {
		t.Errorf("stored password not sealed (%v)", err)
	}
	_, out := e.do(req{method: "GET", path: "/v2/kvms", from: phone})
	if kv := out["kvms"].([]any); len(kv) != 1 || kv[0].(map[string]any)["machine"] != "workstation" {
		t.Errorf("kvms: %v", out)
	}

	if code, out := e.do(req{method: "PATCH", path: "/v2/kvms/kvm-2", from: phone, body: map[string]any{"recheck": true}}); code != 200 {
		t.Errorf("recheck: %d %v", code, out)
	}
	if code, _ := e.do(req{method: "DELETE", path: "/v2/kvms/kvm-2", from: atlas, key: agent}); code != http.StatusForbidden {
		t.Errorf("agent removed a KVM: %d", code)
	}
	if code, _ := e.do(req{method: "DELETE", path: "/v2/kvms/kvm-2", from: phone}); code != http.StatusNoContent {
		t.Errorf("remove: %d", code)
	}
	if len(e.st.KVMs()) != 0 {
		t.Error("KVM still stored")
	}
}

func TestMachinesList(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	_, out := e.do(req{method: "GET", path: "/v2/machines", from: phone})
	var ws map[string]any
	for _, m := range out["machines"].([]any) {
		if m.(map[string]any)["name"] == "workstation" {
			ws = m.(map[string]any)
		}
	}
	if ws == nil || ws["state"] != "on" || ws["kvm"] == nil {
		t.Fatalf("workstation: %v", out)
	}
	// A key sees only its own devices.
	key := e.agentKey([]string{"status"}, "atlas")
	_, out = e.do(req{method: "GET", path: "/v2/machines", from: atlas, key: key})
	for _, m := range out["machines"].([]any) {
		if m.(map[string]any)["name"] != "atlas" {
			t.Errorf("key for atlas sees %v", m.(map[string]any)["name"])
		}
	}
}

func TestResetOnlyByTheOwner(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	k.Hang()

	agent := e.agentKey([]string{"wake", "power"})
	for _, r := range []req{
		{method: "POST", path: "/v2/machines/workstation/reset", from: atlas, key: agent},
		{method: "POST", path: "/v2/devices/workstation/reset", from: atlas, key: agent},
		{method: "POST", path: "/v2/machines/workstation/force-off", from: atlas, key: agent},
		// An agent relayed by one of the owner's devices is still an agent.
		{method: "POST", path: "/v2/machines/workstation/reset", from: phone, header: map[string]string{peer.ForHeader: "Atlas", peer.AgentHeader: "1"}},
		{method: "POST", path: "/v2/machines/workstation/reset", from: friend, key: pairedKey(t, e)},
	} {
		if code, out := e.do(r); code != http.StatusForbidden {
			t.Errorf("%s from %s: %d %v", r.path, r.from, code, out)
		}
	}
	if len(k.Presses()) != 0 {
		t.Fatalf("pressed for a refused caller: %v", k.Presses())
	}
	// MCP has no reset tool (Phase 4 adds proposals instead).
	if out := mcp(e, atlas, agent, 1, "tools/call", map[string]any{"name": "reset", "arguments": map[string]any{"device": "workstation"}}); out["error"] == nil && !isToolError(out) {
		t.Errorf("MCP reset: %v", out)
	}

	if code, out := e.do(req{method: "POST", path: "/v2/machines/Gaming-PC/reset", from: phone}); code != http.StatusUnprocessableEntity || out["error"] != "self" {
		t.Errorf("reset self: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/machines/atlas/reset", from: phone}); code != http.StatusUnprocessableEntity || out["error"] != "no_kvm" {
		t.Errorf("no KVM: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/machines/workstation/explode", from: phone}); code != http.StatusNotFound {
		t.Errorf("unknown action: %d %v", code, out)
	}

	code, out := e.do(req{method: "POST", path: "/v2/machines/workstation/reset", from: phone})
	if code != 200 || out["ok"] != true {
		t.Fatalf("owner reset: %d %v", code, out)
	}
	steps, _ := out["steps"].([]any)
	if len(steps) < 3 || !strings.HasPrefix(steps[0].(map[string]any)["do"].(string), "read power light") {
		t.Errorf("steps: %v", steps)
	}
	e.api.s.Machines.Wait()
	if k.Reboots() != 1 || !k.Up() {
		t.Errorf("reboots %d up %v", k.Reboots(), k.Up())
	}
	if !e.hasEvent("reset workstation", "done") {
		t.Errorf("not in the activity log: %+v", e.st.Events())
	}
}

func TestResetRefusedWhileHelperAnswers(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	code, out := e.do(req{method: "POST", path: "/v2/machines/workstation/reset", from: phone})
	if code != http.StatusConflict || out["error"] != "helper_answers" || out["steps"] == nil {
		t.Errorf("reset of a working machine: %d %v", code, out)
	}
	if !e.hasEvent("reset workstation", "refused: helper_answers") {
		t.Errorf("refusal not logged: %+v", e.st.Events())
	}
	code, out = e.do(req{method: "POST", path: "/v2/machines/workstation/reset", from: phone, body: map[string]any{"force": true}})
	if code != 200 || out["ok"] != true {
		t.Errorf("forced reset: %d %v", code, out)
	}
}

func TestWakeAndShutdownThroughTheHub(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k) // learns the light is wired: it's on
	k.SetOn(false)

	// The relay (and MCP's wake) goes through the KVM: a power press, not Wake-on-LAN.
	code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/wake", from: phone})
	if code != 200 || out["ok"] != true || len(k.Presses()) != 1 || len(e.wakes) != 0 {
		t.Fatalf("wake: %d %v presses %v wakes %v", code, out, k.Presses(), e.wakes)
	}
	time.Sleep(50 * time.Millisecond) // boot
	// Shutdown asks the helper, with the light read before and after; the helper's reply is kept.
	code, out = e.do(req{method: "POST", path: "/v2/devices/workstation/shutdown", from: phone})
	if code != 200 || out["pending"] == nil || out["steps"] == nil {
		t.Fatalf("shutdown: %d %v", code, out)
	}
	if n := len(e.calls); n == 0 || e.calls[n-1] != (forwarded{"workstation", "POST", "/v2/power/shutdown", "Pixel 9 Pro"}) {
		t.Errorf("forwarded: %+v", e.calls)
	}
	// Cancel still goes straight to the helper.
	if code, _ := e.do(req{method: "POST", path: "/v2/devices/workstation/cancel", from: phone}); code != 200 {
		t.Errorf("cancel: %d", code)
	}
}

// raw returns the reply body as text.
func (e *env) raw(r req) string {
	e.t.Helper()
	_, out := e.do(r)
	b, _ := json.Marshal(out)
	return string(b)
}

func (e *env) hasEvent(action, result string) bool {
	for _, ev := range e.st.Events() {
		if ev.Action == action && ev.Result == result {
			return true
		}
	}
	return false
}

func isToolError(out map[string]any) bool {
	r, _ := out["result"].(map[string]any)
	return r != nil && r["isError"] == true
}
