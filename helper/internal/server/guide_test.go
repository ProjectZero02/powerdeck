package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
)

// The examples in docs/integrating-an-assistant.md are run here against a test hub, in order, so
// the guide shows what a hub really answers.
//
// An example is a comment <!-- example --> (an agent key; <!-- example owner --> for the owner's
// phone), then a request block (```mcp: a tool and its arguments; ```http: a method and path, then
// a JSON body), then a ```json block with the answer (for http, after the status). The answer
// lists only the fields that matter: every field shown must be there with that value, and an array
// shown must appear in order within the real one. "…" is any value, and "text…" any value starting
// with text. IDs (id, latest) stand for whatever the hub gave out: the guide's ID is replaced by the
// real one in later requests. Names compare ignoring case, and pve is the test hub.

const guidePath = "../../../docs/integrating-an-assistant.md"

var guideAliases = map[string]string{"pve": "Gaming-PC"}

// silentFleet: workstation's helper doesn't answer while the fake KVM's computer is hung or off.
type silentFleet struct{ hubFleet }

func (f silentFleet) Forward(ctx context.Context, name, method, path string, body, out any, who string) error {
	if strings.EqualFold(name, "workstation") && !f.kvm.Up() {
		return fleet.ErrOffline
	}
	return f.hubFleet.Forward(ctx, name, method, path, body, out, who)
}

type example struct {
	line          int
	owner         bool
	kind, request string
	status        int
	answer        any
}

var fence = regexp.MustCompile("(?s)^```(\\w*)\\n(.*?)\\n```")

func guideExamples(t *testing.T) []example {
	b, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	var out []example
	for _, m := range regexp.MustCompile(`<!-- example( owner)? -->\n`).FindAllStringSubmatchIndex(doc, -1) {
		ex := example{line: strings.Count(doc[:m[0]], "\n") + 1, owner: m[2] >= 0}
		rest := doc[m[1]:]
		req := fence.FindStringSubmatch(rest)
		if req == nil || (req[1] != "mcp" && req[1] != "http") {
			t.Fatalf("line %d: an example needs an mcp or http block next", ex.line)
		}
		ex.kind, ex.request = req[1], req[2]
		rest = strings.TrimLeft(rest[len(req[0]):], "\n")
		ans := fence.FindStringSubmatch(rest)
		if ans == nil || ans[1] != "json" {
			t.Fatalf("line %d: the request needs a json block with the answer right after it", ex.line)
		}
		body := ans[2]
		if ex.kind == "http" {
			code, after, _ := strings.Cut(body, " ")
			if ex.status, err = strconv.Atoi(code); err != nil {
				t.Fatalf("line %d: an http answer starts with its status", ex.line)
			}
			body = after
		}
		if err := json.Unmarshal([]byte(body), &ex.answer); err != nil {
			t.Fatalf("line %d: the answer isn't JSON: %v", ex.line, err)
		}
		out = append(out, ex)
	}
	return out
}

// matcher compares a guide answer with a real one, learning what the guide's IDs stand for.
type matcher struct{ ids map[string]string }

func (m *matcher) match(path string, want, got any) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object, got %v", path, got)
		}
		for k, wv := range w {
			if err := m.match(path+"."+k, wv, g[k]); err != nil {
				return err
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Errorf("%s: want an array, got %v", path, got)
		}
		i := 0
		for n, wv := range w {
			for ; ; i++ {
				if i == len(g) {
					return fmt.Errorf("%s[%d]: %v isn't there (in order) in %v", path, n, wv, g)
				}
				try := &matcher{ids: map[string]string{}}
				for k, v := range m.ids {
					try.ids[k] = v
				}
				if try.match(path, wv, g[i]) == nil {
					m.ids, i = try.ids, i+1
					break
				}
			}
		}
	case string:
		g, ok := got.(string)
		switch {
		case !ok:
			return fmt.Errorf("%s: want %q, got %v", path, w, got)
		case w == "…":
		case strings.HasSuffix(w, "…"):
			if p := strings.TrimSuffix(w, "…"); len(g) < len(p) || !strings.EqualFold(g[:len(p)], p) {
				return fmt.Errorf("%s: want %q, got %q", path, w, g)
			}
		case strings.HasSuffix(path, ".id") || strings.HasSuffix(path, ".latest"):
			if real, seen := m.ids[w]; seen && real != g {
				return fmt.Errorf("%s: %s stood for %s, got %s", path, w, real, g)
			}
			m.ids[w] = g
		default:
			if a, ok := guideAliases[w]; ok {
				w = a
			}
			if !strings.EqualFold(w, g) {
				return fmt.Errorf("%s: want %q, got %q", path, w, g)
			}
		}
	default: // numbers, booleans
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return fmt.Errorf("%s: want %v, got %v", path, want, got)
		}
	}
	return nil
}

func TestAssistantGuide(t *testing.T) {
	e := newEnv(t)
	k := e.withHub(fakekvm.Options{On: true})
	e.linkKVM(k)
	h := e.api.s.Machines
	h.Fleet = silentFleet{h.Fleet.(hubFleet)}

	// What the hub's watch sees: a port that accepts and says nothing, and a ping.
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
	p, _ := e.st.Peer("workstation")
	p.Address, p.Port = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
	_ = e.st.PutPeer(p)
	var mu sync.Mutex
	clock := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	h.LEDGap, h.AskGap = time.Millisecond, time.Millisecond
	h.Ping = func(context.Context, string) string {
		if k.Up() {
			return machines.Replies
		}
		return machines.NoReply
	}
	h.Events = machines.NewBus(clock)
	look := func() { // one of the watch's looks, 30 s apart
		h.WatchOnce(context.Background())
		mu.Lock()
		clock = clock.Add(30 * time.Second)
		mu.Unlock()
	}

	// Workstation freezes: after 3 minutes the hub calls it hung and asks the owner to reset it.
	look()
	k.Hang()
	for i := 0; i < 8; i++ {
		look()
	}
	agent := e.agentKey([]string{"status", "power"}, "workstation")

	examples := guideExamples(t)
	if len(examples) < 6 {
		t.Fatalf("only %d examples in the guide", len(examples))
	}
	m := &matcher{ids: map[string]string{}}
	for _, ex := range examples {
		request := ex.request
		for doc, real := range m.ids {
			request = strings.ReplaceAll(request, doc, real)
		}
		var got any
		switch ex.kind {
		case "mcp":
			name, args, _ := strings.Cut(request, " ")
			var a map[string]any
			if strings.TrimSpace(args) != "" {
				if err := json.Unmarshal([]byte(args), &a); err != nil {
					t.Fatalf("line %d: arguments: %v", ex.line, err)
				}
			}
			res := mcp(e, atlas, agent, ex.line, "tools/call", map[string]any{"name": name, "arguments": a})["result"].(map[string]any)
			got = res["structuredContent"]
			if _, refused := ex.answer.(map[string]any)["error"]; refused != (res["isError"] == true) {
				t.Errorf("line %d: isError %v: %v", ex.line, res["isError"], got)
			}
		case "http":
			first, body, _ := strings.Cut(request, "\n")
			method, path, _ := strings.Cut(first, " ")
			rq := req{method: method, path: path, from: atlas, key: agent}
			if ex.owner {
				rq.from, rq.key = phone, ""
			}
			if strings.TrimSpace(body) != "" {
				var b map[string]any
				if err := json.Unmarshal([]byte(body), &b); err != nil {
					t.Fatalf("line %d: body: %v", ex.line, err)
				}
				rq.body = b
			}
			code, out := e.do(rq)
			if code != ex.status {
				t.Errorf("line %d: %s %s: status %d, want %d: %v", ex.line, method, path, code, ex.status, out)
			}
			if out != nil {
				got = map[string]any(out)
			}
		}
		if err := m.match("", ex.answer, got); err != nil {
			b, _ := json.Marshal(got)
			t.Errorf("line %d: %v\n  answer: %s", ex.line, err, b)
		}
		h.Wait() // a confirmed recovery runs to its end
		look()
	}
}
