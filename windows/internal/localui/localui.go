// Package localui serves the PC-side app window on 127.0.0.1: pairing QR codes, paired phones,
// setup checks and updates.
//
// Hardening for a loopback web app: the Host header must be the loopback address (defeats DNS
// rebinding), every API call and QR image needs a per-run key that is only embedded in the page itself
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
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/projectzero02/powerdeck/windows/internal/netinfo"
	"github.com/projectzero02/powerdeck/windows/internal/pairing"
	"github.com/projectzero02/powerdeck/windows/internal/store"
	"github.com/projectzero02/powerdeck/windows/internal/update"
	"github.com/projectzero02/powerdeck/windows/internal/version"
)

//go:embed web
var webFS embed.FS

// AppDownloadURL always points at the newest Android release.
const AppDownloadURL = "https://github.com/" + version.Repo + "/releases/latest/download/PowerDeck.apk"

// Check is one line of the setup checklist.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Deps are the local UI's collaborators.
type Deps struct {
	Addr        string // listen address, e.g. 127.0.0.1:47322
	APIPort     int
	Name        string
	Fingerprint string
	Store       *store.Store
	Pairing     *pairing.Manager
	Update      *update.Manager
	MAC         func() string
	TailnetIP   func() (netip.Addr, bool)
	Discovery   func() netinfo.Discovered
	Rediscover  func()
	Checks      func() []Check
	Log         *slog.Logger
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
	u := &ui{Deps: d, key: base64.RawURLEncoding.EncodeToString(k), page: page}
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", u.index)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /api/state", u.keyed(u.state))
	mux.HandleFunc("GET /qr/{which}", u.keyed(u.qr))
	mux.HandleFunc("POST /api/pairing/renew", u.keyed(u.renew))
	mux.HandleFunc("POST /api/devices/{id}/remove", u.keyed(u.remove))
	mux.HandleFunc("POST /api/settings", u.keyed(u.settings))
	mux.HandleFunc("POST /api/update/check", u.keyed(u.updateCheck))
	mux.HandleFunc("POST /api/update/install", u.keyed(u.updateInstall))
	mux.HandleFunc("POST /api/rediscover", u.keyed(u.rediscover))
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

// PairLink is the payload of the pairing QR code (see docs/protocol.md).
func (u *ui) pairLink(code pairing.Code) (string, error) {
	ip, ok := u.TailnetIP()
	if !ok {
		return "", fmt.Errorf("this PC has no Tailscale address. Is Tailscale running?")
	}
	q := url.Values{}
	q.Set("v", "1")
	q.Set("n", u.Name)
	q.Set("h", ip.String())
	q.Set("p", strconv.Itoa(u.APIPort))
	q.Set("f", u.Fingerprint)
	q.Set("c", code.Value)
	if mac := u.MAC(); mac != "" {
		q.Set("m", mac)
	}
	d := u.Discovery()
	if d.NanoKVM != nil {
		q.Set("k", d.NanoKVM.Address)
	}
	if d.Proxmox != nil {
		q.Set("x", d.Proxmox.Address)
	}
	return "powerdeck://pair?" + q.Encode(), nil
}

type deviceView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Model    string    `json:"model,omitempty"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
}

func (u *ui) state(w http.ResponseWriter, _ *http.Request) {
	code := u.Pairing.Current()
	link, linkErr := u.pairLink(code)
	devs := []deviceView{}
	for _, d := range u.Store.Devices() {
		devs = append(devs, deviceView{ID: d.ID, Name: d.Name, Model: d.Model, PairedAt: d.PairedAt, LastSeen: d.LastSeen})
	}
	ip, _ := u.TailnetIP()
	body := map[string]any{
		"name":        u.Name,
		"version":     version.Version,
		"tailnetIP":   ip.String(),
		"mac":         u.MAC(),
		"pairLink":    link,
		"pairExpires": code.Expires,
		"appUrl":      AppDownloadURL,
		"devices":     devs,
		"discovery":   u.Discovery(),
		"checks":      u.Checks(),
		"update":      u.Update.Status(),
		"settings":    u.Store.Settings(),
	}
	if linkErr != nil {
		body["pairError"] = linkErr.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (u *ui) qr(w http.ResponseWriter, r *http.Request) {
	var text string
	switch r.PathValue("which") {
	case "app.svg":
		text = AppDownloadURL
	case "pair.svg":
		link, err := u.pairLink(u.Pairing.Current())
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

func (u *ui) remove(w http.ResponseWriter, r *http.Request) {
	if err := u.Store.RemoveDevice(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	u.Log.Info("phone removed on the PC", "id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (u *ui) settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoUpdate *bool `json:"autoUpdate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.AutoUpdate != nil {
		if err := u.Store.SetAutoUpdate(*req.AutoUpdate); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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

func (u *ui) rediscover(w http.ResponseWriter, _ *http.Request) {
	u.Rediscover()
	w.WriteHeader(http.StatusNoContent)
}
