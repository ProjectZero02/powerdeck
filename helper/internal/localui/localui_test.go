package localui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/pairing"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/update"
)

func newUI(t *testing.T) (http.Handler, string) {
	t.Helper()
	st, _ := store.Open(t.TempDir())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := New(Deps{
		Addr: "127.0.0.1:47322", APIPort: 47321, Name: "Gaming-PC", Fingerprint: "FP_abc",
		Store: st, Pairing: pairing.NewManager(time.Now), Update: update.NewManager(t.TempDir(), log),
		Network: func() (netinfo.Network, bool) {
			return netinfo.Network{Interface: "Ethernet", MAC: "d8:5e:d3:12:34:56", Wired: true}, true
		},
		TailnetIP: func() (netip.Addr, bool) { return netip.MustParseAddr("100.101.1.10"), true },
		Checks:    func(context.Context) []check.Result { return nil },
		Fix:       func(context.Context, string) (check.Result, error) { return check.Result{}, nil },
		Reports:   &power.Reports{},
		Log:       log,
		// httptest requests come from 192.0.2.1; stand in for "another user on this PC".
		AllowLocal: func(remote string) bool { return !strings.HasPrefix(remote, "10.9.9.9:") },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1:47322/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	m := regexp.MustCompile(`name="pd-key" content="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no key in page: %s", w.Body.String())
	}
	return h, m[1]
}

func get(h http.Handler, host, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "http://"+host+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHostHeaderBlocksDNSRebinding(t *testing.T) {
	h, key := newUI(t)
	if w := get(h, "evil.example:47322", "/api/state", map[string]string{"X-PowerDeck-Key": key}); w.Code != http.StatusForbidden {
		t.Errorf("foreign Host must be refused, got %d", w.Code)
	}
	if w := get(h, "localhost:47322", "/api/state", map[string]string{"X-PowerDeck-Key": key}); w.Code != 200 {
		t.Errorf("localhost must work, got %d", w.Code)
	}
}

func TestOtherLocalUsersAreRefused(t *testing.T) {
	h, _ := newUI(t)
	req := httptest.NewRequest("GET", "http://127.0.0.1:47322/", nil)
	req.RemoteAddr = "10.9.9.9:50000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "pd-key") {
		t.Errorf("another local user got the page: %d", w.Code)
	}
}

func TestAPINeedsKey(t *testing.T) {
	h, key := newUI(t)
	for _, p := range []string{"/api/state", "/qr/add.svg", "/qr/app.svg"} {
		if w := get(h, "127.0.0.1:47322", p, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s without key: %d", p, w.Code)
		}
	}
	if w := get(h, "127.0.0.1:47322", "/qr/add.svg?k="+url.QueryEscape(key), nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Errorf("qr with key: %d", w.Code)
	}
	w := get(h, "127.0.0.1:47322", "/", nil)
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("security headers missing")
	}
}

func TestAddLink(t *testing.T) {
	link := AddLink("Gaming-PC", netip.MustParseAddr("100.101.1.10"), 47321, "FP_abc", "")
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "powerdeck" || u.Host != "add" {
		t.Fatalf("link: %s", link)
	}
	q := u.Query()
	for k, v := range map[string]string{"v": "2", "n": "Gaming-PC", "h": "100.101.1.10", "p": "47321", "f": "FP_abc"} {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if q.Has("c") {
		t.Error("the add link must not carry the pairing code")
	}
}

func TestStateAndAgentKey(t *testing.T) {
	h, key := newUI(t)
	w := get(h, "127.0.0.1:47322", "/api/state", map[string]string{"X-PowerDeck-Key": key})
	var st struct{ PairCode, McpUrl string }
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || len(st.PairCode) != 9 || st.McpUrl != "http://100.101.1.10:47323/mcp" {
		t.Fatalf("state: %v %s", err, w.Body.String())
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:47322/api/keys", strings.NewReader(`{"name":"Lyra","scopes":["wake","power"]}`))
	req.Header.Set("X-PowerDeck-Key", key)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"key":"pdk_`) {
		t.Fatalf("create key: %d %s", rw.Code, rw.Body.String())
	}
}

func TestPresenceReports(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	rep := &power.Reports{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, _ := New(Deps{Addr: "127.0.0.1:47322", Store: st, Pairing: pairing.NewManager(time.Now), Update: update.NewManager(t.TempDir(), log),
		Network: func() (netinfo.Network, bool) { return netinfo.Network{}, false }, TailnetIP: func() (netip.Addr, bool) { return netip.Addr{}, false },
		Checks: func(context.Context) []check.Result { return nil }, Reports: rep, Log: log})
	post := func(ct, origin, body string) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1:47322/presence", strings.NewReader(body))
		r.Header.Set("Content-Type", ct)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := post("text/plain", "", `{"fullscreen":true}`); c != http.StatusForbidden {
		t.Errorf("text/plain: %d", c)
	}
	if c := post("application/json", "https://evil.example", `{"fullscreen":true}`); c != http.StatusForbidden {
		t.Errorf("browser: %d", c)
	}
	if c := post("application/json", "", `{"user":"Alex","idleSeconds":900,"fullscreen":true}`); c != http.StatusNoContent {
		t.Errorf("report: %d", c)
	}
	if p := rep.Fresh(); p == nil || !p.Active || p.User != "Alex" {
		t.Errorf("full-screen counts as active even when idle: %+v", p)
	}
}

func TestQRSVG(t *testing.T) {
	svg, err := QRSVG("powerdeck://add?v=2")
	if err != nil || !strings.Contains(svg, `<path fill="#000" d="M`) {
		t.Fatalf("svg: %v %s", err, svg[:80])
	}
}
