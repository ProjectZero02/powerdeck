// Package localui serves the PC window on 127.0.0.1: the QR code that introduces this PC to the
// phone, the pairing code for other Tailscale accounts, keys (phones and agents), setup checks,
// activity and updates. It also takes presence reports from the user-session companion, which
// shows the notification-area icon.
//
// Hardening for a loopback web app: on Windows only programs of the person at the screen (or an
// administrator) may connect, the Host header must be the loopback address (defeats DNS rebinding), every API call and QR image needs a per-run key that is only embedded in the page itself
// (other websites can't read the page cross-origin), and responses forbid framing and caching.
package localui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/pairing"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/update"
	"github.com/projectzero02/powerdeck/helper/internal/version"
)

//go:embed web
var webFS embed.FS

// MCPPort serves the MCP endpoint over plain HTTP on the tailnet (Tailscale encrypts it), because
// most agent tools can't use a self-signed certificate.
const MCPPort = 47323

// AppDownloadURL always points at the newest Android release.
const AppDownloadURL = "https://github.com/" + version.Repo + "/releases/latest/download/PowerDeck.apk"

// Deps are the local UI's collaborators.
type Deps struct {
	Addr        string // listen address, e.g. 127.0.0.1:47322
	APIPort     int
	Name        string
	Fingerprint string
	Store       *store.Store
	Pairing     *pairing.Manager
	Update      *update.Manager
	Network     func() (netinfo.Network, bool)
	TailnetIP   func() (netip.Addr, bool)
	Checks      func(ctx context.Context) []check.Result
	Fix         func(ctx context.Context, id string) (check.Result, error)
	Reports     *power.Reports // presence from the user-session companion (Windows)
	// AllowLocal, if set, decides whether the program behind a connection (its "ip:port") may use
	// the window: on Windows, only the person at the screen or an administrator.
	AllowLocal func(remote string) bool
	Log        *slog.Logger
	OS         string // default runtime.GOOS (the demo pretends to be Windows)
}

type ui struct {
	Deps
	key  string
	page *template.Template
}

// New returns the handler.
func New(d Deps) (http.Handler, error) {
	k := make([]byte, 24)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	page, err := template.ParseFS(webFS, "web/index.html")
	if err != nil {
		return nil, err
	}
	if d.OS == "" {
		d.OS = runtime.GOOS
	}
	u := &ui{Deps: d, key: base64.RawURLEncoding.EncodeToString(k), page: page}
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", u.index)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /api/state", u.keyed(u.state))
	mux.HandleFunc("GET /qr/{which}", u.keyed(u.qr))
	mux.HandleFunc("POST /api/pairing/renew", u.keyed(u.renew))
	mux.HandleFunc("POST /api/keys", u.keyed(u.addKey))
	mux.HandleFunc("POST /api/keys/{id}/remove", u.keyed(u.removeKey))
	mux.HandleFunc("POST /api/owners/{node}/remove", u.keyed(u.removeOwner))
	mux.HandleFunc("POST /api/checks/{id}/fix", u.keyed(u.fix))
	mux.HandleFunc("POST /api/settings", u.keyed(u.settings))
	mux.HandleFunc("POST /api/update/check", u.keyed(u.updateCheck))
	mux.HandleFunc("POST /api/update/install", u.keyed(u.updateInstall))
	mux.HandleFunc("POST /presence", u.presence)
	mux.HandleFunc("POST /tray", u.tray)
	return u.guard(mux), nil
}

func (u *ui) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(u.Addr)
	allowed := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if u.AllowLocal != nil && !u.AllowLocal(r.RemoteAddr) {
			http.Error(w, "PowerDeck's window is only for the person signed in at this PC's screen. Over Remote Desktop, open PowerDeck as administrator.", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		next.ServeHTTP(w, r)
	})
}

func (u *ui) keyed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-PowerDeck-Key")
		if got == "" {
			got = r.URL.Query().Get("k") // <img> tags can't send headers
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(u.key)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		}
		h(w, r)
	}
}

func (u *ui) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = u.page.Execute(w, map[string]string{"Key": u.key, "Version": version.Version})
}

// AddLink is the payload of the QR code that introduces this device (see docs/protocol.md).
func AddLink(name string, ip netip.Addr, port int, pin, code string) string {
	q := url.Values{}
	q.Set("v", "2")
	q.Set("n", name)
	q.Set("h", ip.String())
	q.Set("p", strconv.Itoa(port))
	q.Set("f", pin)
	if code != "" {
		q.Set("c", code)
	}
	return "powerdeck://add?" + q.Encode()
}

// addLink is the QR payload. It always carries the current pairing code: scanning it is how a phone
// becomes an owner device.
func (u *ui) addLink() (string, error) {
	ip, ok := u.TailnetIP()
	if !ok {
		return "", fmt.Errorf("this PC has no Tailscale address. Is Tailscale running and signed in?")
	}
	return AddLink(u.Name, ip, u.APIPort, u.Fingerprint, u.Pairing.Current().Value), nil
}

type keyView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Scopes    []string  `json:"scopes"`
	Devices   []string  `json:"devices,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
}

func (u *ui) state(w http.ResponseWriter, r *http.Request) {
	code := u.Pairing.Current()
	_, linkErr := u.addLink()
	keys := []keyView{}
	for _, k := range u.Store.Keys() {
		keys = append(keys, keyView{ID: k.ID, Name: k.Name, Kind: k.Kind, Scopes: k.Scopes, Devices: k.Devices, CreatedAt: k.CreatedAt, LastUsed: k.LastUsed})
	}
	ip, _ := u.TailnetIP()
	n, _ := u.Network()
	peers := []string{}
	for _, p := range u.Store.Peers() {
		peers = append(peers, p.Name)
	}
	checks := u.Checks(r.Context())
	if checks == nil {
		checks = []check.Result{}
	}
	body := map[string]any{
		"name":         u.Name,
		"os":           u.OS,
		"version":      version.Version,
		"tailnetIP":    ip.String(),
		"network":      n,
		"pairCode":     code.Value,
		"pairExpires":  code.Expires,
		"appUrl":       AppDownloadURL,
		"mcpUrl":       "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(MCPPort)) + "/mcp",
		"keys":         keys,
		"owners":       u.Store.Owners(),
		"ownersLegacy": u.Store.OwnerMode() == store.OwnersLegacy,
		"peers":        peers,
		"events":       u.Store.Events(),
		"checks":       checks,
		"update":       u.Update.Status(),
		"settings":     u.Store.Settings(),
	}
	if linkErr != nil {
		body["linkError"] = linkErr.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (u *ui) qr(w http.ResponseWriter, r *http.Request) {
	var text string
	switch r.PathValue("which") {
	case "app.svg":
		text = AppDownloadURL
	case "add.svg", "pair.svg":
		link, err := u.addLink()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		text = link
	default:
		http.NotFound(w, r)
		return
	}
	svg, err := QRSVG(text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(svg))
}

// QRSVG renders text as a QR code (error correction M) with a 4-module quiet zone.
func QRSVG(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	n := c.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; {
			if !c.Black(x, y) {
				x++
				continue
			}
			start := x
			for x < c.Size && c.Black(x, y) {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start+quiet, y+quiet, x-start, x-start)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

func (u *ui) renew(w http.ResponseWriter, _ *http.Request) {
	u.Pairing.Renew()
	w.WriteHeader(http.StatusNoContent)
}

func (u *ui) addKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		Scopes  []string `json:"scopes"`
		Devices []string `json:"devices"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "A key needs a name.", http.StatusBadRequest)
		return
	}
	scopes, err := auth.ParseScopes(strings.Join(req.Scopes, ","))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		name = name[:64]
	}
	k, secret, err := u.Store.AddKey(store.NewKey{Name: name, Kind: store.KindAutomation, Scopes: scopes, Devices: req.Devices}, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	u.Store.AddEvent(store.Event{At: time.Now(), Who: "this PC", Action: "created key " + k.Name, Result: strings.Join(scopes, ",")})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": k.ID, "key": secret})
}

func (u *ui) removeOwner(w http.ResponseWriter, r *http.Request) {
	if err := u.Store.RemoveOwner(r.PathValue("node")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	u.Store.AddEvent(store.Event{At: time.Now(), Who: "this PC", Action: "removed an owner device", Result: "ok"})
	w.WriteHeader(http.StatusNoContent)
}

func (u *ui) removeKey(w http.ResponseWriter, r *http.Request) {
	if err := u.Store.RemoveKey(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	u.Store.AddEvent(store.Event{At: time.Now(), Who: "this PC", Action: "removed key", Result: "ok"})
	w.WriteHeader(http.StatusNoContent)
}

func (u *ui) fix(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := u.Fix(ctx, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// presence takes a report from the companion running in the signed-in user's session. It's
// unauthenticated but loopback-only, JSON-only and refuses browsers (Origin), so a web page can't
// fake it; a local program could, but a local program can already shut the PC down itself.
func (u *ui) presence(w http.ResponseWriter, r *http.Request) {
	if u.Reports == nil || r.Header.Get("Origin") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var p power.Presence
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u.Reports.Put(p)
	// The companion also shows the notification-area icon when it's turned on.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"tray": u.Store.Settings().Tray})
}

func (u *ui) settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoUpdate *bool `json:"autoUpdate"`
		Agents     *bool `json:"agents"`
		Tray       *bool `json:"tray"`
		SetupDone  *bool `json:"setupDone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	changedAgents := req.Agents != nil && *req.Agents == u.Store.Settings().NoAgents
	err := u.Store.UpdateSettings(func(s *store.Settings) {
		if req.AutoUpdate != nil {
			s.AutoUpdate = *req.AutoUpdate
		}
		if req.Agents != nil {
			s.NoAgents = !*req.Agents
		}
		if req.Tray != nil {
			s.Tray = *req.Tray
		}
		if req.SetupDone != nil {
			s.SetupDone = *req.SetupDone
		}
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if changedAgents {
		u.Store.AddEvent(store.Event{At: time.Now(), Who: "this PC", Action: map[bool]string{true: "allowed AI agents", false: "turned AI agents away"}[*req.Agents], Result: "ok"})
	}
	w.WriteHeader(http.StatusNoContent)
}

// tray is the notification-area icon's "Hide this icon" (from the user-session companion), with
// the same guards as presence.
func (u *ui) tray(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Show bool `json:"show"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := u.Store.UpdateSettings(func(s *store.Settings) { s.Tray = req.Show }); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (u *ui) updateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st, _ := u.Update.Check(ctx)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (u *ui) updateInstall(w http.ResponseWriter, _ *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := u.Update.Install(ctx); err != nil {
			u.Log.Error("update install failed", "err", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}
