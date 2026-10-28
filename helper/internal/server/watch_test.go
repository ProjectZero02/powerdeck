package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/webhook"
)

func (e *env) peerKey(name string) string {
	e.t.Helper()
	code, out := e.do(req{method: "POST", path: "/v2/keys", from: phone, body: map[string]any{"name": name, "kind": "peer"}})
	if code != 200 {
		e.t.Fatalf("peer key: %d %v", code, out)
	}
	return out["key"].(string)
}

// Only a machine's own helper says what it's about to do, and only about itself.
func TestNotes(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	h := e.api.s.Machines
	h.AsleepAfter = time.Millisecond
	k.Sleep()
	sleep := map[string]any{"event": "sleep"}
	learned := func() string {
		h.Wait()
		kv, _ := e.st.KVMFor("workstation")
		return kv.Wiring.LEDAsleep
	}

	for _, r := range []req{
		{from: phone},
		{from: atlas, key: e.agentKey([]string{"status", "power"}, "workstation")},
		{from: hubAddr, key: e.peerKey("hub")}, // a peer, but not workstation: nothing happens
	} {
		r.method, r.path, r.body = "POST", "/v2/note", sleep
		code, _ := e.do(r)
		if r.from == hubAddr && code != http.StatusNoContent || r.from != hubAddr && code != http.StatusForbidden {
			t.Errorf("note from %s: %d", r.from, code)
		}
	}
	if got := learned(); got != "" {
		t.Fatalf("a note from another machine counted: %q", got)
	}

	pk := e.peerKey("workstation")
	if code, _ := e.do(req{method: "POST", path: "/v2/note", from: wsAddr, key: pk, body: map[string]any{"event": "nap"}}); code != http.StatusBadRequest {
		t.Errorf("unknown event: %d", code)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/note", from: wsAddr, key: pk, body: sleep}); code != http.StatusNoContent {
		t.Errorf("note: %d", code)
	}
	if got := learned(); got != "off" {
		t.Errorf("light while asleep: %q", got)
	}
}

// sseClient reads one event stream.
type sseClient struct {
	t    *testing.T
	resp *http.Response
	r    *bufio.Reader
}

type sse struct{ id, event, data, comment string }

func (e *env) stream(srv *httptest.Server, from, key, lastID string) (*sseClient, int) {
	e.t.Helper()
	r, _ := http.NewRequest("GET", srv.URL+"/v2/machines/events", nil)
	r.Header.Set("X-Test-From", from)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if lastID != "" {
		r.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, resp.StatusCode
	}
	c := &sseClient{t: e.t, resp: resp, r: bufio.NewReader(resp.Body)}
	e.t.Cleanup(func() { resp.Body.Close() })
	return c, 200
}

// next reads the next event or comment, failing after 2 s.
func (c *sseClient) next() sse {
	c.t.Helper()
	got := make(chan sse, 1)
	go func() {
		var m sse
		for {
			line, err := c.r.ReadString('\n')
			if err != nil {
				got <- sse{event: "EOF " + err.Error()}
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				got <- m
				return
			case strings.HasPrefix(line, ":"):
				m.comment = strings.TrimSpace(line[1:])
			default:
				f, v, _ := strings.Cut(line, ": ")
				switch f {
				case "id":
					m.id = v
				case "event":
					m.event = v
				case "data":
					m.data = v
				}
			}
		}
	}()
	select {
	case m := <-got:
		return m
	case <-time.After(2 * time.Second):
		c.t.Fatal("no event in 2 s")
		return sse{}
	}
}

// nextEvent skips heartbeats.
func (c *sseClient) nextEvent() sse {
	c.t.Helper()
	for {
		if m := c.next(); m.comment == "" {
			return m
		}
	}
}

func TestMachineEvents(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	bus := machines.NewBus(time.Now())
	e.api.s.Machines.Events = bus
	old := heartbeat
	heartbeat = 100 * time.Millisecond
	defer func() { heartbeat = old }()

	// A real server with short timeouts: the stream must outlive them.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = r.Header.Get("X-Test-From")
		e.api.ServeHTTP(w, r)
	}))
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = 150*time.Millisecond, 150*time.Millisecond
	srv.Start()
	defer func() { srv.CloseClientConnections(); srv.Close() }() // streams don't end by themselves

	c, _ := e.stream(srv, phone, "", "")
	snap := c.nextEvent()
	if snap.event != "snapshot" || !strings.Contains(snap.data, `"name":"workstation"`) {
		t.Fatalf("snapshot %+v", snap)
	}
	first := bus.Publish(machines.Event{Event: "hung", Machine: "workstation", State: "hung", From: "unknown"})
	if m := c.nextEvent(); m.event != "machine" || m.id != first.ID || !strings.Contains(m.data, `"state":"hung"`) {
		t.Fatalf("event %+v", m)
	}
	time.Sleep(300 * time.Millisecond) // past the timeouts
	if m := c.next(); m.comment != "still here" {
		t.Errorf("heartbeat %+v", m)
	}
	second := bus.Publish(machines.Event{Event: "recovered", Machine: "workstation", State: "on", From: "hung"})
	if m := c.nextEvent(); m.id != second.ID {
		t.Fatalf("after the timeouts: %+v", m)
	}

	// Reconnecting with the last ID seen replays what came after it, without a snapshot.
	c2, _ := e.stream(srv, phone, "", first.ID)
	if m := c2.nextEvent(); m.event != "machine" || m.id != second.ID {
		t.Errorf("replay %+v", m)
	}
	// An ID no longer held: a fresh snapshot.
	c3, _ := e.stream(srv, phone, "", "12")
	if m := c3.nextEvent(); m.event != "snapshot" || m.id != second.ID {
		t.Errorf("unknown ID: %+v", m)
	}

	// A key for atlas sees atlas only.
	key := e.agentKey([]string{"status"}, "atlas")
	c4, _ := e.stream(srv, atlas, key, "")
	if m := c4.nextEvent(); m.event != "snapshot" || strings.Contains(m.data, "workstation") {
		t.Errorf("atlas's snapshot: %+v", m)
	}
	bus.Publish(machines.Event{Event: "hung", Machine: "workstation", State: "hung"})
	third := bus.Publish(machines.Event{Event: "on", Machine: "atlas", State: "on"})
	if m := c4.nextEvent(); m.id != third.ID {
		t.Errorf("atlas's key got %+v", m)
	}

	// No more than maxStreams at once.
	e.api.s.streams.Add(maxStreams)
	if _, code := e.stream(srv, phone, "", ""); code != http.StatusServiceUnavailable {
		t.Errorf("over the limit: %d", code)
	}
	e.api.s.streams.Add(-maxStreams)
}

func TestWebhookRoutes(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do(req{method: "GET", path: "/v2/webhooks", from: phone}); code != http.StatusNotFound {
		t.Errorf("no hub: %d", code)
	}
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	e.api.s.Webhooks = &webhook.Sender{Store: e.st, Box: e.api.s.Machines.Box, Bus: machines.NewBus(time.Now())}

	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, bodies = append(got, r), append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer rcv.Close()
	body := map[string]any{"url": rcv.URL + "/powerdeck"}

	// Owners only.
	agent := e.agentKey([]string{"status", "wake", "power"})
	for _, r := range []req{
		{from: atlas, key: agent},
		{from: hubAddr, key: e.peerKey("hub")},
		{from: friend, key: pairedKey(t, e)},
	} {
		r.method, r.path, r.body = "POST", "/v2/webhooks", body
		if code, _ := e.do(r); code != http.StatusForbidden {
			t.Errorf("webhook added from %s: %d", r.from, code)
		}
	}
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"url": "http://example.com/hook"}, "bad_url"},
		{map[string]any{"url": "https://example.com/hook", "events": []string{"exploded"}}, "bad_event"},
	} {
		if code, out := e.do(req{method: "POST", path: "/v2/webhooks", from: phone, body: c.body}); code != http.StatusBadRequest || out["error"] != c.code {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}

	code, out := e.do(req{method: "POST", path: "/v2/webhooks", from: phone, body: body})
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, out)
	}
	h := out["webhook"].(map[string]any)
	sec, _ := h["secret"].(string)
	if !strings.HasPrefix(sec, "pdwh_") || h["events"].([]any)[0] != "hung" {
		t.Fatalf("created %v", h)
	}
	id := h["id"].(string)
	if raw := e.raw(req{method: "GET", path: "/v2/webhooks", from: phone}); strings.Contains(raw, sec) || !strings.Contains(raw, id) {
		t.Errorf("list: %s", raw)
	}
	if strings.Contains(e.raw(req{method: "GET", path: "/v2/status", from: phone}), sec) {
		t.Error("secret in the status")
	}
	if !e.hasEvent("add webhook "+rcv.URL+"/powerdeck", "ok") {
		t.Error("not in the activity log")
	}

	// The test delivery is signed with the secret shown once.
	code, out = e.do(req{method: "POST", path: "/v2/webhooks/" + id + "/test", from: phone})
	if code != 200 || out["ok"] != true {
		t.Fatalf("test: %d %v", code, out)
	}
	mu.Lock()
	r, b := got[0], bodies[0]
	mu.Unlock()
	if r.Header.Get("X-PowerDeck-Signature") != webhook.Sign(sec, r.Header.Get("X-PowerDeck-Timestamp"), b) ||
		r.Header.Get("X-PowerDeck-Event") != "hung" {
		t.Errorf("test delivery %v %s", r.Header, b)
	}
	var ev machines.Event
	if json.Unmarshal(b, &ev) != nil || ev.Machine != "test" {
		t.Errorf("test event %s", b)
	}

	if code, _ := e.do(req{method: "DELETE", path: "/v2/webhooks/" + id, from: phone}); code != http.StatusNoContent {
		t.Errorf("remove: %d", code)
	}
	if code, _ := e.do(req{method: "DELETE", path: "/v2/webhooks/" + id, from: phone}); code != http.StatusNotFound {
		t.Errorf("remove again: %d", code)
	}

	for i := 0; i < store.MaxWebhooks; i++ {
		if code, _ := e.do(req{method: "POST", path: "/v2/webhooks", from: phone, body: body}); code != http.StatusCreated {
			t.Fatalf("add %d: %d", i, code)
		}
	}
	if code, out := e.do(req{method: "POST", path: "/v2/webhooks", from: phone, body: body}); code != http.StatusConflict {
		t.Errorf("one too many: %d %v", code, out)
	}
}

func TestHungAfterSetting(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	hub := func(from, key string) any {
		_, st := e.do(req{method: "GET", path: "/v2/status", from: from, key: key})
		return st["hub"]
	}
	if h := hub(phone, ""); h.(map[string]any)["hungAfterMinutes"] != 3.0 {
		t.Errorf("default: %v", h)
	}
	for _, n := range []int{1, 31, -1} {
		if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"hungAfterMinutes": n}}); code != http.StatusBadRequest {
			t.Errorf("%d minutes: %d", n, code)
		}
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"hungAfterMinutes": 5}}); code/100 != 2 {
		t.Errorf("5 minutes: %d", code)
	}
	if h := hub(phone, ""); h.(map[string]any)["hungAfterMinutes"] != 5.0 {
		t.Errorf("after setting: %v", h)
	}
	if e.st.Settings().HungAfterMinutes != 5 {
		t.Errorf("stored %+v", e.st.Settings())
	}
	if h := hub(atlas, e.agentKey([]string{"status"})); h != nil {
		t.Errorf("an agent sees the hub settings: %v", h)
	}
	if code, _ := e.do(req{method: "POST", path: "/v2/settings", from: phone, body: map[string]any{"hungAfterMinutes": 0}}); code/100 != 2 {
		t.Errorf("back to the default: %d", code)
	}
	if h := hub(phone, ""); h.(map[string]any)["hungAfterMinutes"] != 3.0 {
		t.Errorf("default again: %v", h)
	}
}
