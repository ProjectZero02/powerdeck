package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/secret"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// The same signature from Python's hmac and from openssl:
//
//	printf '1700000000.{"id":"1"}' | openssl dgst -sha256 -hmac pdwh_test
func TestSignKnownAnswer(t *testing.T) {
	got := Sign("pdwh_test", "1700000000", []byte(`{"id":"1"}`))
	if got != "sha256=8fc0a23c6a794d26f2a6c83ac48c588dfbbe64deefd997f2bb8cc5b0ac0c0914" {
		t.Errorf("got %s", got)
	}
}

func TestCheckURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://example.com/hook":            true,
		"https://192.168.1.5/hook":            true,
		"http://100.101.1.20:8080/hook":       true,
		"http://127.0.0.1:9000/":              true,
		"http://localhost:9000/":              true,
		"http://assistant.example.ts.net/x":   true,
		"http://[fd7a:115c:a1e0::5]:80/x":     true,
		"http://example.com/hook":             false,
		"http://192.168.1.5/hook":             false,
		"ftp://example.com/":                  false,
		"https://user:pw@example.com/":        false,
		"example.com/hook":                    false,
		"https:///nohost":                     false,
		"http://100.128.0.1/ (outside CGNAT)": false,
	} {
		if err := CheckURL(u); (err == nil) != ok {
			t.Errorf("%s: %v, want ok=%v", u, err, ok)
		}
	}
}

type receiver struct {
	mu    sync.Mutex
	got   []*http.Request
	body  [][]byte
	fails int // answer 500 this many times first
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got, r.body = append(r.got, req), append(r.body, b)
	if r.fails > 0 {
		r.fails--
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *receiver) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

func setup(t *testing.T, events ...string) (*Sender, *receiver, *httptest.Server, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box := &secret.KeyFile{Path: filepath.Join(t.TempDir(), "secret.key")}
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	sec := store.NewWebhookSecret()
	sealed, err := box.Seal(sec)
	if err != nil {
		t.Fatal(err)
	}
	if events == nil {
		events = DefaultEvents
	}
	if err := st.AddWebhook(store.Webhook{ID: "w1", URL: srv.URL, Events: events, Secret: sealed}); err != nil {
		t.Fatal(err)
	}
	s := &Sender{Store: st, Box: box, Bus: machines.NewBus(time.Now()), Retries: []time.Duration{time.Millisecond, time.Millisecond}}
	return s, rc, srv, sec
}

func run(t *testing.T, s *Sender) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for s.Bus.Subscribers() == 0 {
		time.Sleep(time.Millisecond)
	}
	return func() { cancel(); <-done }
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 500 && !f(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !f() {
		t.Fatal("timed out")
	}
}

func TestDeliverySignedAndFiltered(t *testing.T) {
	s, rc, _, sec := setup(t)
	stop := run(t, s)
	s.Bus.Publish(machines.Event{Event: "unknown", Machine: "gaming-pc", State: "unknown"}) // not asked for
	hung := s.Bus.Publish(machines.Event{Event: "hung", Machine: "gaming-pc", State: "hung", From: "unknown",
		Evidence: machines.Evidence{Helper: machines.Silent, LED: machines.On, LEDTrusted: true}})
	waitFor(t, func() bool { return rc.count() == 1 })
	stop()
	req, body := rc.got[0], rc.body[0]
	ts := req.Header.Get("X-PowerDeck-Timestamp")
	if req.Header.Get("X-PowerDeck-Event") != "hung" || req.Header.Get("X-PowerDeck-Delivery") != hung.ID ||
		req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", req.Header)
	}
	if req.Header.Get("X-PowerDeck-Signature") != Sign(sec, ts, body) {
		t.Errorf("signature %s", req.Header.Get("X-PowerDeck-Signature"))
	}
	if Sign("pdwh_other", ts, body) == req.Header.Get("X-PowerDeck-Signature") {
		t.Error("another secret signs the same")
	}
	var got machines.Event
	if err := json.Unmarshal(body, &got); err != nil || got.Machine != "gaming-pc" || got.Evidence.LED != "on" || got.ID != hung.ID {
		t.Errorf("body %s (%v)", body, err)
	}
	if h := s.Store.Webhooks()[0]; h.LastResult != "ok" || h.LastAt.IsZero() {
		t.Errorf("last delivery %+v", h)
	}
}

func TestRetriesThenGivesUp(t *testing.T) {
	s, rc, _, _ := setup(t)
	rc.fails = 2
	stop := run(t, s)
	s.Bus.Publish(machines.Event{Event: "recovered", Machine: "gaming-pc"})
	waitFor(t, func() bool { return rc.count() == 3 })
	s.Wait()
	if h := s.Store.Webhooks()[0]; h.LastResult != "ok" {
		t.Errorf("after two failures: %+v", h)
	}
	ids := map[string]bool{}
	for _, r := range rc.got {
		ids[r.Header.Get("X-PowerDeck-Delivery")] = true
	}
	if len(ids) != 1 {
		t.Errorf("retries carry the same delivery ID: %v", ids)
	}

	rc.mu.Lock()
	rc.fails = 10
	rc.mu.Unlock()
	s.Bus.Publish(machines.Event{Event: "hung", Machine: "gaming-pc"})
	waitFor(t, func() bool { return rc.count() == 6 })
	s.Wait()
	stop()
	if h := s.Store.Webhooks()[0]; h.LastResult != "HTTP 500" {
		t.Errorf("gave up: %+v", h)
	}
	if rc.count() != 6 {
		t.Errorf("tries %d, want 3 (one and two retries)", rc.count())
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	s, rc, srv, _ := setup(t)
	target := httptest.NewServer(rc)
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()
	h := s.Store.Webhooks()[0]
	h.URL = redirect.URL
	err := s.Send(context.Background(), h, machines.Event{Event: "hung"})
	if err == nil || !strings.Contains(err.Error(), "307") || rc.count() != 0 {
		t.Errorf("err %v, deliveries %d", err, rc.count())
	}
	_ = srv
}

func TestSecretTampered(t *testing.T) {
	s, rc, _, _ := setup(t)
	h := s.Store.Webhooks()[0]
	h.Secret = h.Secret[:len(h.Secret)-4] + "AAAA"
	if err := s.Send(context.Background(), h, machines.Event{Event: "hung"}); err == nil || rc.count() != 0 {
		t.Errorf("err %v, deliveries %d", err, rc.count())
	}
}
