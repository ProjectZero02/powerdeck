// Package server is the phone-facing HTTPS API (protocol v1, see docs/protocol.md).
//
// Every request must come from a tailnet or loopback address. Pairing needs the one-time code from
// the QR code; everything else needs the device token issued at pairing. Repeated failures from an
// address are throttled.
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/projectzero02/powerdeck/windows/internal/netinfo"
	"github.com/projectzero02/powerdeck/windows/internal/pairing"
	"github.com/projectzero02/powerdeck/windows/internal/power"
	"github.com/projectzero02/powerdeck/windows/internal/store"
	"github.com/projectzero02/powerdeck/windows/internal/version"
)

// Deps are the server's collaborators.
type Deps struct {
	Store       *store.Store
	Pairing     *pairing.Manager
	Power       *power.Controller
	Name        string
	MAC         func() string
	Discovery   func() netinfo.Discovered
	AllowRemote func(netip.Addr) bool
	Log         *slog.Logger
	Now         func() time.Time
}

type server struct {
	Deps
	limiter *limiter
}

// New returns the API handler.
func New(d Deps) http.Handler {
	if d.AllowRemote == nil {
		d.AllowRemote = netinfo.AllowedRemote
	}
	s := &server{Deps: d, limiter: newLimiter(d.Now)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", s.info)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("GET /v1/status", s.auth(s.status))
	mux.HandleFunc("POST /v1/power/{action}", s.auth(s.power))
	mux.HandleFunc("DELETE /v1/device", s.auth(s.unpair))
	return s.guard(mux)
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, apiError{Error: errCode, Message: msg})
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// guard enforces the source-address policy, throttling and a small body limit.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteAddr(r)
		if !ip.IsValid() || !s.AllowRemote(ip) {
			fail(w, http.StatusForbidden, "forbidden", "PowerDeck only answers devices on your tailnet.")
			return
		}
		if s.limiter.blocked(ip) {
			w.Header().Set("Retry-After", "600")
			fail(w, http.StatusTooManyRequests, "throttled", "Too many failed attempts. Try again in 10 minutes.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		next.ServeHTTP(w, r)
	})
}

func (s *server) auth(h func(http.ResponseWriter, *http.Request, store.Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		dev, found := store.Device{}, false
		if ok {
			dev, found = s.Store.Authenticate(token, s.Now())
		}
		if !found {
			s.limiter.fail(remoteAddr(r))
			fail(w, http.StatusUnauthorized, "unauthorized", "This phone isn't paired with this PC any more. Pair it again.")
			return
		}
		h(w, r, dev)
	}
}

func (s *server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app": "PowerDeck", "name": s.Name, "version": version.Version, "apiVersion": version.APIVersion,
	})
}

type pairRequest struct {
	Code        string `json:"code"`
	DeviceName  string `json:"deviceName"`
	DeviceModel string `json:"deviceModel"`
}

func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func (s *server) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return
	}
	ip := remoteAddr(r)
	if !s.Pairing.Consume(req.Code) {
		s.limiter.fail(ip)
		s.Log.Warn("pairing rejected", "from", ip)
		fail(w, http.StatusForbidden, "bad_code", "That pairing code is wrong or expired. Open PowerDeck on the PC for a fresh one.")
		return
	}
	name := clean(req.DeviceName, 64)
	if name == "" {
		name = "Phone"
	}
	dev, token, err := s.Store.AddDevice(name, clean(req.DeviceModel, 64), s.Now())
	if errors.Is(err, store.ErrTooManyDevices) {
		fail(w, http.StatusConflict, "too_many_devices", "Too many phones are paired. Remove one on the PC first.")
		return
	}
	if err != nil {
		s.Log.Error("pairing: save failed", "err", err)
		fail(w, http.StatusInternalServerError, "internal", "Couldn't save the pairing.")
		return
	}
	s.Log.Info("phone paired", "device", dev.Name, "model", dev.Model, "from", ip)
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId": dev.ID,
		"token":    token,
		"pc":       s.statusBody(),
	})
}

func (s *server) statusBody() map[string]any {
	up, boot := s.Power.Uptime()
	body := map[string]any{
		"name":          s.Name,
		"version":       version.Version,
		"apiVersion":    version.APIVersion,
		"uptimeSeconds": int64(up.Seconds()),
		"pending":       s.Power.Pending(),
		"mac":           s.MAC(),
	}
	if !boot.IsZero() {
		body["bootTime"] = boot.UTC()
	}
	d := s.Discovery()
	if d.NanoKVM != nil {
		body["nanokvm"] = d.NanoKVM
	}
	if d.Proxmox != nil {
		body["proxmox"] = d.Proxmox
	}
	return body
}

func (s *server) status(w http.ResponseWriter, _ *http.Request, _ store.Device) {
	writeJSON(w, http.StatusOK, s.statusBody())
}

func (s *server) power(w http.ResponseWriter, r *http.Request, dev store.Device) {
	action := r.PathValue("action")
	if action == "cancel" {
		err := s.Power.Cancel()
		switch {
		case err == nil:
			s.Log.Info("power: cancelled", "device", dev.Name)
			writeJSON(w, http.StatusOK, map[string]any{"pending": nil})
		case errors.Is(err, power.ErrNothingPending):
			fail(w, http.StatusConflict, "nothing_pending", "Nothing was scheduled.")
		default:
			s.Log.Error("power: cancel failed", "err", err)
			fail(w, http.StatusInternalServerError, "internal", "Windows refused to cancel: "+err.Error())
		}
		return
	}
	var a power.Action
	switch action {
	case "shutdown":
		a = power.Shutdown
	case "restart":
		a = power.Restart
	default:
		fail(w, http.StatusNotFound, "not_found", "Unknown action.")
		return
	}
	req := struct {
		DelaySeconds *int `json:"delaySeconds"`
	}{}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
			return
		}
	}
	delay := 30
	if req.DelaySeconds != nil {
		delay = min(max(*req.DelaySeconds, 0), 600)
	}
	p, err := s.Power.Do(a, time.Duration(delay)*time.Second, dev.Name)
	switch {
	case err == nil:
		s.Log.Info("power: scheduled", "action", a, "delay", delay, "device", dev.Name)
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": p})
	case errors.Is(err, power.ErrPending):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "pending", "message": "A shutdown or restart is already scheduled.", "pending": s.Power.Pending()})
	default:
		s.Log.Error("power: schedule failed", "action", a, "err", err)
		fail(w, http.StatusInternalServerError, "internal", "Windows refused: "+err.Error())
	}
}

func (s *server) unpair(w http.ResponseWriter, _ *http.Request, dev store.Device) {
	if err := s.Store.RemoveDevice(dev.ID); err != nil {
		fail(w, http.StatusInternalServerError, "internal", "Couldn't remove the pairing.")
		return
	}
	s.Log.Info("phone unpaired itself", "device", dev.Name)
	w.WriteHeader(http.StatusNoContent)
}

// limiter blocks an address for 10 minutes after 10 failures within 10 minutes.
type limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	hits map[netip.Addr][]time.Time
}

const (
	limitWindow   = 10 * time.Minute
	limitFailures = 10
)

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, hits: map[netip.Addr][]time.Time{}}
}

func (l *limiter) recent(ip netip.Addr) []time.Time {
	cut := l.now().Add(-limitWindow)
	h := l.hits[ip]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, ip)
	} else {
		l.hits[ip] = h
	}
	return h
}

func (l *limiter) fail(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[ip] = append(l.recent(ip), l.now())
}

func (l *limiter) blocked(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) >= limitFailures
}
