package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
)

func presses(k *fakekvm.Fake) string {
	var out []string
	for _, p := range k.Presses() {
		out = append(out, p.Button+" "+p.Duration.String())
	}
	return strings.Join(out, ", ")
}

func recoveryOf(out map[string]any) map[string]any {
	r, _ := out["recovery"].(map[string]any)
	return r
}

// Agents propose; only the owner's own devices confirm.
func TestRecoveryRoutes(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	k.Hang()
	agent := e.agentKey([]string{"status", "power"}, "workstation")
	other := e.agentKey([]string{"status", "power"})
	noPower := e.agentKey([]string{"status", "wake"})
	path := "/v2/machines/workstation/recoveries"

	if code, out := e.do(req{method: "POST", path: "/v2/devices/workstation/reset", from: atlas, key: agent}); code != http.StatusForbidden ||
		out["error"] != "owner_only" || !strings.Contains(out["message"].(string), path) {
		t.Errorf("agent's own reset: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: path, from: atlas, key: noPower}); code != http.StatusForbidden || out["error"] != "scope" {
		t.Errorf("without power: %d %v", code, out)
	}
	if code, out := e.do(req{method: "POST", path: "/v2/machines/Gaming-PC/recoveries", from: phone}); code != http.StatusUnprocessableEntity {
		t.Errorf("the hub itself: %d %v", code, out)
	}

	code, out := e.do(req{method: "POST", path: path, from: atlas, key: agent, body: map[string]any{"reason": "Atlas can't reach it"}})
	r := recoveryOf(out)
	if code != http.StatusCreated || r["state"] != "proposed" || r["action"] != "reset" || r["proposedBy"] != "Atlas" ||
		r["agent"] != true || r["reason"] != "Atlas can't reach it" || r["proposerKey"] == nil {
		t.Fatalf("propose: %d %v", code, out)
	}
	id := r["id"].(string)
	if code, out := e.do(req{method: "POST", path: path, from: atlas, key: other, body: map[string]any{"action": "force-off"}}); code != 200 || recoveryOf(out)["id"] != id {
		t.Errorf("second proposal: %d %v", code, out)
	}
	_, out = e.do(req{method: "GET", path: "/v2/recoveries?open=1", from: atlas, key: other})
	if list := out["recoveries"].([]any); len(list) != 1 || list[0].(map[string]any)["proposerKey"] != nil {
		t.Errorf("another key sees: %v", out)
	}

	// Confirming: not an agent key, not the owner's hub relaying for one, not another key.
	for _, rq := range []req{
		{from: atlas, key: agent},
		{from: phone, header: map[string]string{peer.AgentHeader: "1"}},
		{from: friend, key: pairedKey(t, e)},
	} {
		rq.method, rq.path = "POST", "/v2/recoveries/"+id+"/confirm"
		if code, _ := e.do(rq); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("confirmed from %s: %d", rq.from, code)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/recoveries/" + id + "/decline", from: atlas, key: other}); code != http.StatusForbidden {
		t.Errorf("another key declined: %d", code)
	}
	if presses(k) != "" {
		t.Fatalf("pressed: %s", presses(k))
	}

	// The proposer withdraws its own; then the owner confirms a new one.
	if code, out := e.do(req{method: "POST", path: "/v2/recoveries/" + id + "/decline", from: atlas, key: agent}); code != 200 || recoveryOf(out)["state"] != "withdrawn" {
		t.Errorf("withdraw: %d %v", code, out)
	}
	_, out = e.do(req{method: "POST", path: path, from: atlas, key: agent})
	id = recoveryOf(out)["id"].(string)
	if code, out := e.do(req{method: "POST", path: "/v2/recoveries/" + id + "/confirm", from: phone}); code != http.StatusAccepted || recoveryOf(out)["state"] != "running" {
		t.Fatalf("confirm: %d %v", code, out)
	}
	e.api.s.Machines.Wait()
	if _, out := e.do(req{method: "GET", path: "/v2/recoveries/" + id, from: atlas, key: agent}); recoveryOf(out)["state"] != "back" || presses(k) != "reset 800ms" {
		t.Errorf("after: %v %s", out, presses(k))
	}
	if !e.hasEvent("confirm reset workstation", "confirmed") || !e.hasEvent("workstation answers again after the reset", "back") {
		t.Errorf("log %+v", e.st.Events())
	}

	// Within 30 minutes: not again.
	k.Hang()
	if code, out := e.do(req{method: "POST", path: path, from: atlas, key: agent}); code != http.StatusTooManyRequests || out["error"] != "too_soon" || out["retryAt"] == nil {
		t.Errorf("too soon: %d %v", code, out)
	}

	// The machine's own "Allow AI agents" switch.
	p, _ := e.st.Peer("workstation")
	p.NoAgents = true
	_ = e.st.PutPeer(p)
	if code, out := e.do(req{method: "POST", path: path, from: atlas, key: agent}); code != http.StatusForbidden || out["error"] != "agents_off" {
		t.Errorf("agents off: %d %v", code, out)
	}
}

func TestAutoResetSetting(t *testing.T) {
	e := newEnv(t)
	e.withHub(fakekvm.Options{On: true})
	for n, want := range map[int]int{3: 400, 61: 400, 10: 204, 0: 204} {
		if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"autoResetMinutes": n}}); code != want {
			t.Errorf("%d minutes: %d", n, code)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, header: map[string]string{peer.AgentHeader: "1"},
		body: map[string]any{"autoResetMinutes": 10}}); code != http.StatusForbidden {
		t.Errorf("for an agent: %d", code)
	}
	e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"autoResetMinutes": 15}})
	_, st := e.do(req{method: "GET", path: "/v2/status", from: phone})
	if hub, _ := st["hub"].(map[string]any); hub["autoResetMinutes"] != 15.0 || e.st.Settings().AutoResetMinutes != 15 {
		t.Errorf("status %v", st["hub"])
	}
	if !e.hasEvent("set automatic resets after 15 minutes", "ok") {
		t.Errorf("log %+v", e.st.Events())
	}
}

// Recovery events go out as their own SSE event, and open ones are in the snapshot.
func TestRecoveryEvents(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	k.Hang()
	e.api.s.Machines.Events = machines.NewBus(time.Now())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = r.Header.Get("X-Test-From")
		e.api.ServeHTTP(w, r)
	}))
	defer func() { srv.CloseClientConnections(); srv.Close() }()

	c, _ := e.stream(srv, phone, "", "")
	if m := c.nextEvent(); m.event != "snapshot" || !strings.Contains(m.data, `"recoveries":[]`) {
		t.Fatalf("snapshot %+v", m)
	}
	agent := e.agentKey([]string{"status", "power"}, "workstation")
	e.do(req{method: "POST", path: "/v2/machines/workstation/recoveries", from: atlas, key: agent})
	if m := c.nextEvent(); m.event != "recovery" || !strings.Contains(m.data, `"event":"recovery"`) || !strings.Contains(m.data, `"state":"proposed"`) ||
		strings.Contains(m.data, `"evidence"`) && !strings.Contains(m.data, `"recovery":{`) {
		t.Errorf("event %+v", m)
	}
	c2, _ := e.stream(srv, phone, "", "")
	if m := c2.nextEvent(); !strings.Contains(m.data, `"state":"proposed"`) {
		t.Errorf("snapshot with an open recovery: %+v", m)
	}
}
