package localui

import (
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

	"github.com/projectzero02/powerdeck/windows/internal/netinfo"
	"github.com/projectzero02/powerdeck/windows/internal/pairing"
	"github.com/projectzero02/powerdeck/windows/internal/store"
	"github.com/projectzero02/powerdeck/windows/internal/update"
)

func newUI(t *testing.T) (http.Handler, string) {
	t.Helper()
	st, _ := store.Open(t.TempDir())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := New(Deps{
		Addr: "127.0.0.1:47322", APIPort: 47321, Name: "Gaming-PC", Fingerprint: "FP_abc",
		Store: st, Pairing: pairing.NewManager(time.Now), Update: update.NewManager(t.TempDir(), log),
		MAC:       func() string { return "d8:5e:d3:12:34:56" },
		TailnetIP: func() (netip.Addr, bool) { return netip.MustParseAddr("100.101.1.10"), true },
		Discovery: func() netinfo.Discovered {
			return netinfo.Discovered{NanoKVM: &netinfo.Found{Address: "100.101.1.20"}, Proxmox: &netinfo.Found{Address: "100.101.1.30:8006"}}
		},
		Rediscover: func() {}, Checks: func() []Check { return nil }, Log: log,
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

func TestAPINeedsKey(t *testing.T) {
	h, key := newUI(t)
	for _, p := range []string{"/api/state", "/qr/pair.svg", "/qr/app.svg"} {
		if w := get(h, "127.0.0.1:47322", p, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s without key: %d", p, w.Code)
		}
	}
	if w := get(h, "127.0.0.1:47322", "/qr/pair.svg?k="+url.QueryEscape(key), nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Errorf("qr with key: %d", w.Code)
	}
	w := get(h, "127.0.0.1:47322", "/", nil)
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("security headers missing")
	}
}

func TestPairLinkCarriesEverything(t *testing.T) {
	h, key := newUI(t)
	w := get(h, "127.0.0.1:47322", "/api/state", map[string]string{"X-PowerDeck-Key": key})
	var st struct{ PairLink string }
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.PairLink == "" {
		t.Fatalf("no pair link: %v %s", err, w.Body.String())
	}
	link := st.PairLink
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "powerdeck" || u.Host != "pair" {
		t.Fatalf("link: %s", link)
	}
	q := u.Query()
	want := map[string]string{"v": "1", "n": "Gaming-PC", "h": "100.101.1.10", "p": "47321", "f": "FP_abc",
		"m": "d8:5e:d3:12:34:56", "k": "100.101.1.20", "x": "100.101.1.30:8006"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if len(q.Get("c")) != 26 {
		t.Errorf("pairing code should be 26 base32 chars, got %q", q.Get("c"))
	}
}

func TestQRSVG(t *testing.T) {
	svg, err := QRSVG("powerdeck://pair?v=1")
	if err != nil || !strings.Contains(svg, `<path fill="#000" d="M`) {
		t.Fatalf("svg: %v %s", err, svg[:80])
	}
}
