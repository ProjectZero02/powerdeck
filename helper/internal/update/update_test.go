package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/version"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true}, {"1.10.0", "1.9.9", true}, {"2.0.0", "1.99.99", true},
		{"1.0.0", "1.0.0", false}, {"1.0.0", "1.0.1", false}, {"v1.2.0", "1.1.9", true}, {"1.2.0-rc1", "1.1.0", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestVerifyRejectsWrongKeyAndTampering(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("installer bytes")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(other, data))
	if err := Verify(data, []byte(sig)); err == nil {
		t.Fatal("a signature from another key must be rejected")
	}
	if err := Verify(data, []byte("not base64!")); err == nil {
		t.Fatal("garbage signature must be rejected")
	}
}

// With a release build and a fake GitHub, Install must refuse an installer whose signature fails.
func TestInstallRefusesUnsignedInstaller(t *testing.T) {
	old := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = old }()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.1.0", "html_url": "https://example/r",
				"assets": []map[string]string{
					{"name": InstallerAsset, "browser_download_url": srv.URL + "/exe"},
					{"name": SignatureAsset, "browser_download_url": srv.URL + "/sig"},
				},
			})
		case r.URL.Path == "/exe":
			_, _ = w.Write([]byte("evil installer"))
		case r.URL.Path == "/sig":
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, []byte("evil installer")))))
		}
	}))
	defer srv.Close()

	m := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.api = srv.URL
	ran := false
	m.run = func(string) error { ran = true; return nil }
	st, err := m.Check(context.Background())
	if err != nil || !st.Available || st.Latest != "1.1.0" {
		t.Fatalf("check: %+v %v", st, err)
	}
	if err := m.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("install should fail on signature, got %v", err)
	}
	if ran {
		t.Fatal("installer must not run")
	}
	if m.Status().Installing {
		t.Fatal("installing flag must reset after failure")
	}
}

func TestDevBuildsNeverOfferUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9.9.9"})
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.api = srv.URL
	st, _ := m.Check(context.Background())
	if st.Available {
		t.Fatal("dev builds must not auto-update")
	}
}

func TestStuckInstallExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.now = func() time.Time { return now }
	m.status.Installing, m.since = true, now // installer launched, helper never restarted
	if !m.Status().Installing {
		t.Fatal("expired too early")
	}
	now = now.Add(installTimeout + time.Second)
	if st := m.Status(); st.Installing || st.Error == "" {
		t.Fatalf("stuck install not expired: %+v", st)
	}
}

func TestAutoInstallAt(t *testing.T) {
	pub := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seen := pub.Add(30 * time.Hour)
	cases := []struct {
		name                   string
		published, seen, pause time.Time
		want                   time.Time
	}{
		{"three days after publishing", pub, seen, time.Time{}, pub.Add(72 * time.Hour)},
		{"publish date unknown: from first seen", time.Time{}, seen, time.Time{}, seen.Add(72 * time.Hour)},
		{"publish date in the future: from first seen", seen.Add(time.Hour), seen, time.Time{}, seen.Add(72 * time.Hour)},
		{"a pause past that wins", pub, seen, pub.Add(100 * time.Hour), pub.Add(100 * time.Hour)},
		{"a pause that ended doesn't matter", pub, seen, pub.Add(time.Hour), pub.Add(72 * time.Hour)},
	}
	for _, c := range cases {
		if got := AutoInstallAt(c.published, c.seen, c.pause); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckReadsPublishDate(t *testing.T) {
	old := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.1.0", "published_at": "2026-10-01T12:00:00Z"})
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.api = srv.URL
	clock := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	st, err := m.Check(context.Background())
	if err != nil || !st.Available || !st.Published.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) || !st.FirstSeen.Equal(clock) {
		t.Fatalf("check: %v %+v", err, st)
	}
	clock = clock.Add(time.Hour)
	if st, _ = m.Check(context.Background()); !st.FirstSeen.Equal(clock.Add(-time.Hour)) {
		t.Errorf("first seen moved on a second check: %v", st.FirstSeen)
	}
}
