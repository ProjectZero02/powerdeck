// Package update checks GitHub Releases for a newer PowerDeck and installs it.
//
// Downloads (the Windows installer, or the Linux helper binary) are only run after their Ed25519
// signature (<asset>.sig, made by the release workflow) verifies against PublicKey, so a compromised
// download location can't push code to the device.
package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/version"
)

// PublicKey verifies release installers. The private half lives only in the release workflow's secrets.
const PublicKey = "CJxP8WfqitXsZh9tobWk/cyQbfMMVkk2+iLCHWYkucs="

// Release is the latest published release.
type Release struct {
	Version   string            `json:"version"`
	URL       string            `json:"url"`
	Published time.Time         `json:"published"`
	Assets    map[string]string `json:"-"`
}

// Staging is how long a release must have been public before it installs on its own, so a bad (or
// forged) release can be pulled before most helpers take it. Installing from the app or the PC
// window doesn't wait.
const Staging = 72 * time.Hour

// PauseFor is how long "pause updates" holds automatic installs.
const PauseFor = 7 * 24 * time.Hour

// AutoInstallAt is when an available release may install on its own: once it has been public for
// Staging, and not while updates are paused. Zero published (unknown) means it waits the full time
// from when it was first seen.
func AutoInstallAt(published, firstSeen, pausedUntil time.Time) time.Time {
	if published.IsZero() || published.After(firstSeen) {
		published = firstSeen
	}
	at := published.Add(Staging)
	if pausedUntil.After(at) {
		at = pausedUntil
	}
	return at
}

// Status is shown in the PC app.
type Status struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	Available  bool      `json:"available"`
	CheckedAt  time.Time `json:"checkedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
	Installing bool      `json:"installing"`
	Published  time.Time `json:"published,omitzero"` // when the latest release was published
	FirstSeen  time.Time `json:"-"`                  // when this helper first saw it
}

// Newer reports whether version a is newer than b ("1.10.0" > "1.9.3"). Non-numeric parts compare as 0.
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.SplitN(v, "-", 2)[0], "v")
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// Verify checks an installer against its detached signature (base64 text).
func Verify(installer, sig []byte) error {
	pub, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad embedded public key")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("signature is not base64: %w", err)
	}
	if !ed25519.Verify(pub, installer, s) {
		return errors.New("signature does not match")
	}
	return nil
}

// Manager keeps update state and performs checks and installs.
type Manager struct {
	mu      sync.Mutex
	status  Status
	latest  *Release
	client  *http.Client
	api     string // GitHub API base, overridable for tests
	dataDir string
	log     *slog.Logger
	run     func(path string) error // starts the installer; OS-specific
	// Busy, when set, says why the helper mustn't restart right now (e.g. a shutdown countdown it
	// would lose), or claims the restart; checked just before installing, since the download can
	// take minutes. Idle releases the claim when the installer doesn't start.
	Busy  func() string
	Idle  func()
	now   func() time.Time
	since time.Time // when Installing was set
}

// installTimeout: an installer that started but never restarted the helper has failed.
const installTimeout = 10 * time.Minute

func NewManager(dataDir string, log *slog.Logger) *Manager {
	return &Manager{
		status:  Status{Current: version.Version},
		client:  &http.Client{Timeout: 60 * time.Second},
		api:     "https://api.github.com",
		dataDir: dataDir,
		log:     log,
		run:     runInstaller,
		now:     time.Now,
	}
}

// Status returns the current update state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	return m.status
}

// Check asks GitHub for the latest release.
func (m *Manager) Check(ctx context.Context) (Status, error) {
	rel, err := m.fetchLatest(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	m.status.CheckedAt = time.Now().UTC()
	if err != nil {
		m.status.Error = err.Error()
		return m.status, err
	}
	if m.status.Latest != rel.Version || m.status.FirstSeen.IsZero() {
		m.status.FirstSeen = m.now().UTC()
	}
	m.latest = rel
	m.status.Error = ""
	m.status.Latest = rel.Version
	m.status.Published = rel.Published
	m.status.Available = version.IsRelease() && Newer(rel.Version, version.Version)
	return m.status, nil
}

func (m *Manager) fetchLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.api+"/repos/"+version.Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "PowerDeck/"+version.Version)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var body struct {
		TagName   string    `json:"tag_name"`
		HTMLURL   string    `json:"html_url"`
		Published time.Time `json:"published_at"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	rel := &Release{Version: strings.TrimPrefix(body.TagName, "v"), URL: body.HTMLURL, Published: body.Published.UTC(), Assets: map[string]string{}}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

func (m *Manager) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PowerDeck/"+version.Version)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Install downloads, verifies and installs the latest release: on Windows the installer stops this
// service, replaces the files and starts the new version; on Linux the binary is swapped and the
// service restarted.
func (m *Manager) expireLocked() {
	if m.status.Installing && m.now().Sub(m.since) > installTimeout {
		m.status.Installing = false
		m.status.Error = "the update didn't finish; the helper is still on " + m.status.Current
	}
}

func (m *Manager) Install(ctx context.Context) error {
	m.mu.Lock()
	m.expireLocked()
	rel, avail, busy := m.latest, m.status.Available, m.status.Installing
	if busy {
		m.mu.Unlock()
		return errors.New("an update is already being installed")
	}
	m.status.Installing, m.status.Error, m.since = true, "", m.now()
	m.mu.Unlock()
	err := m.install(ctx, rel, avail)
	if err != nil {
		m.mu.Lock()
		m.status.Installing = false
		m.status.Error = err.Error()
		m.mu.Unlock()
	}
	return err
}

func (m *Manager) install(ctx context.Context, rel *Release, avail bool) error {
	if rel == nil || !avail {
		return errors.New("no update available")
	}
	exeURL, sigURL := rel.Assets[InstallerAsset], rel.Assets[SignatureAsset]
	if exeURL == "" || sigURL == "" {
		return errors.New("release is missing the installer or its signature")
	}
	exe, err := m.download(ctx, exeURL, 200<<20)
	if err != nil {
		return err
	}
	sig, err := m.download(ctx, sigURL, 4<<10)
	if err != nil {
		return err
	}
	if err := Verify(exe, sig); err != nil {
		return fmt.Errorf("refusing update %s: %w", rel.Version, err)
	}
	dir := filepath.Join(m.dataDir, "updates")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, rel.Version+"-"+InstallerAsset)
	if err := os.WriteFile(path, exe, 0o600); err != nil {
		return err
	}
	if m.Busy != nil {
		if why := m.Busy(); why != "" {
			return errors.New("not installing now: " + why)
		}
	}
	m.log.Info("update verified, starting installer", "version", rel.Version)
	if err := m.run(path); err != nil {
		if m.Idle != nil {
			m.Idle()
		}
		return err
	}
	return nil
}
