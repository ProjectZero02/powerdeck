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

	// Another key's stream never says which key proposed it.
	other := e.agentKey([]string{"status"})
	c3, _ := e.stream(srv, atlas, other, "")
	if m := c3.nextEvent(); !strings.Contains(m.data, `"state":"proposed"`) || strings.Contains(m.data, "proposerKey") {
		t.Errorf("another key's snapshot: %+v", m)
	}
	e.do(req{method: "POST", path: "/v2/recoveries/" + e.api.s.Machines.Recoveries(true)[0].ID + "/decline", from: phone})
	if m := c3.nextEvent(); m.event != "recovery" || !strings.Contains(m.data, `"state":"declined"`) || strings.Contains(m.data, "proposerKey") {
		t.Errorf("another key's event: %+v", m)
	}
}

// tool calls a hub tool over MCP and returns its result.
func tool(e *env, key, name string, args map[string]any) (map[string]any, bool) {
	e.t.Helper()
	res := mcp(e, atlas, key, 1, "tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
	return res["structuredContent"].(map[string]any), res["isError"] == true
}

// An agent over MCP: the same proposals and rules as the REST routes, and no way to confirm.
func TestMCPHub(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	k.Hang()
	e.api.s.Machines.Events = machines.NewBus(time.Now())
	agent := e.agentKey([]string{"status", "power"}, "workstation")
	other := e.agentKey([]string{"status", "power"})

	init := mcp(e, atlas, agent, 1, "initialize", nil)["result"].(map[string]any)
	if !strings.Contains(init["instructions"].(string), "propose_reset") {
		t.Errorf("instructions: %v", init["instructions"])
	}
	tools := map[string]bool{}
	for _, tl := range mcp(e, atlas, agent, 2, "tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		tools[tl.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"list_devices", "list_machines", "propose_reset", "propose_force_off", "get_recovery", "withdraw_recovery", "get_events"} {
		if !tools[n] {
			t.Errorf("no %s in %v", n, tools)
		}
	}
	if len(tools) != 11 {
		t.Errorf("tools: %v", tools)
	}
	if out := mcp(e, atlas, agent, 3, "tools/call", map[string]any{"name": "confirm_recovery", "arguments": map[string]any{}}); out["error"] == nil {
		t.Errorf("a confirm tool: %v", out)
	}

	out, isErr := tool(e, agent, "list_machines", nil)
	if ms := out["machines"].([]any); isErr || len(ms) != 1 || ms[0].(map[string]any)["name"] != "workstation" {
		t.Errorf("list_machines: %v", out)
	}
	out, _ = tool(e, agent, "get_events", nil)
	if len(out["events"].([]any)) != 0 || out["latest"] != "" || out["missed"] != nil {
		t.Errorf("no events yet: %v", out)
	}

	if out, isErr := tool(e, agent, "propose_reset", map[string]any{}); !isErr || out["error"] != "bad_request" {
		t.Errorf("no machine: %v", out)
	}
	out, isErr = tool(e, agent, "propose_reset", map[string]any{"machine": "workstation", "reason": "Atlas can't reach it"})
	r := recoveryOf(out)
	if isErr || out["created"] != true || r["state"] != "proposed" || r["agent"] != true || r["proposerKey"] == nil {
		t.Fatalf("propose_reset: %v", out)
	}
	id := r["id"].(string)
	out, _ = tool(e, other, "propose_force_off", map[string]any{"machine": "workstation"})
	if r := recoveryOf(out); out["created"] != false || r["id"] != id || r["proposerKey"] != nil {
		t.Errorf("second proposal: %v", out)
	}

	// Another key sees the recovery everywhere, never which key proposed it.
	out, _ = tool(e, other, "list_machines", nil)
	if rec := out["machines"].([]any)[0].(map[string]any)["recovery"].(map[string]any); rec["id"] != id || rec["proposerKey"] != nil {
		t.Errorf("list_machines recovery: %v", rec)
	}
	_, rest := e.do(req{method: "GET", path: "/v2/machines", from: atlas, key: other})
	for _, m := range rest["machines"].([]any) {
		if m := m.(map[string]any); m["name"] == "workstation" && (m["recovery"] == nil || recoveryOf(m)["proposerKey"] != nil) {
			t.Errorf("GET /v2/machines: %v", m)
		}
	}
	out, _ = tool(e, other, "get_events", nil)
	evs := out["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["event"] != "recovery" || recoveryOf(evs[0].(map[string]any))["proposerKey"] != nil {
		t.Fatalf("get_events: %v", out)
	}
	latest := out["latest"].(string)
	if out, _ = tool(e, other, "get_events", map[string]any{"after": latest}); len(out["events"].([]any)) != 0 || out["latest"] != latest {
		t.Errorf("nothing new: %v", out)
	}
	if out, _ = tool(e, other, "get_events", map[string]any{"after": "12"}); out["missed"] != true || len(out["events"].([]any)) != 1 {
		t.Errorf("too old: %v", out)
	}

	if out, isErr := tool(e, other, "withdraw_recovery", map[string]any{"id": id}); !isErr || out["error"] != "owner_only" {
		t.Errorf("another key withdrew: %v", out)
	}
	if out, isErr := tool(e, agent, "withdraw_recovery", map[string]any{"id": id}); isErr || recoveryOf(out)["state"] != "withdrawn" {
		t.Errorf("withdraw: %v", out)
	}
	if out, isErr := tool(e, agent, "get_recovery", map[string]any{"id": "nope"}); !isErr || out["error"] != "not_found" {
		t.Errorf("unknown recovery: %v", out)
	}

	// The owner confirms; the agent follows it to the end.
	out, _ = tool(e, agent, "propose_reset", map[string]any{"machine": "workstation"})
	id = recoveryOf(out)["id"].(string)
	if presses(k) != "" {
		t.Fatalf("pressed before the owner confirmed: %s", presses(k))
	}
	e.do(req{method: "POST", path: "/v2/recoveries/" + id + "/confirm", from: phone})
	e.api.s.Machines.Wait()
	if out, _ := tool(e, agent, "get_recovery", map[string]any{"id": id}); recoveryOf(out)["state"] != "back" || presses(k) != "reset 800ms" {
		t.Errorf("after: %v %s", out, presses(k))
	}
	out, _ = tool(e, agent, "get_events", map[string]any{"after": latest})
	var states []string
	for _, ev := range out["events"].([]any) {
		if rec := recoveryOf(ev.(map[string]any)); rec != nil && (len(states) == 0 || states[len(states)-1] != rec["state"]) {
			states = append(states, rec["state"].(string)) // running comes again with each step
		}
	}
	if strings.Join(states, " ") != "withdrawn proposed running back" {
		t.Errorf("recovery events: %v", states)
	}

	// Within 30 minutes: refused, with when to try again.
	k.Hang()
	if out, isErr := tool(e, agent, "propose_reset", map[string]any{"machine": "workstation"}); !isErr || out["error"] != "too_soon" || out["retryAt"] == nil {
		t.Errorf("too soon: %v", out)
	}
}

// A helper that isn't a hub lists only its own tools.
func TestMCPNoHub(t *testing.T) {
	e := newEnv(t)
	key := e.agentKey([]string{"status", "power"}, "workstation")
	if init := mcp(e, atlas, key, 1, "initialize", nil)["result"].(map[string]any); strings.Contains(init["instructions"].(string), "propose_reset") {
		t.Errorf("instructions: %v", init["instructions"])
	}
	if out := mcp(e, atlas, key, 2, "tools/call", map[string]any{"name": "list_machines", "arguments": map[string]any{}}); out["error"] == nil {
		t.Errorf("list_machines without a hub: %v", out)
	}
}
