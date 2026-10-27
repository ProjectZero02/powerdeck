// Package server is the helper's HTTPS API (protocol v2, see docs/protocol.md).
//
// Every request must come from a tailnet or loopback address. The caller is identified by its
// Tailscale identity (owner) or a key; each route needs a scope. Repeated failures from an address
// are throttled, and automation keys get extra limits.
package server

import (
	"bytes"
	"context"
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

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/guests"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/pairing"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
	"github.com/projectzero02/powerdeck/helper/internal/update"
	"github.com/projectzero02/powerdeck/helper/internal/version"
)

// Deps are the server's collaborators.
type Deps struct {
	Auth     *auth.Authenticator
	Store    *store.Store
	Pairing  *pairing.Manager
	Power    *power.Controller
	Fleet    *fleet.Fleet
	Name     string
	OS       string
	SelfNode func(ctx context.Context) string // this device's tailnet node ID
	Network  func() (netinfo.Network, bool)
	Checks   func(ctx context.Context) []check.Result
	Fix      func(ctx context.Context, id string) (check.Result, error)
	Guests   func(ctx context.Context) *guests.Info
	Wake     func(mac string, lans []string) ([]string, error)
	Update   Updater // nil: updates aren't managed here (tests, development builds)
	// Uninstall removes PowerDeck from this device in the background (nil: not supported here).
	Uninstall func() error
	// PairLink returns this device's add link (QR payload, with the code) and the current pairing code.
	PairLink func() (link, code string)
	// AllowRemote is the source-address policy (default: tailnet and loopback).
	AllowRemote func(netip.Addr) bool
	Log         *slog.Logger
	Now         func() time.Time
}

// Updater is the helper's self-update (update.Manager).
type Updater interface {
	Status() update.Status
	Check(ctx context.Context) (update.Status, error)
	Install(ctx context.Context) error
}

type server struct {
	Deps
	limiter *limiter
	quota   *quota

	checksMu sync.Mutex
	checks   []check.Result // cached: on Windows they run PowerShell, and the app polls every few seconds
	checksAt time.Time
}

// cachedChecks runs the setup checks at most once a minute (a fix clears the cache).
func (s *server) cachedChecks(ctx context.Context) []check.Result {
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	if s.checks == nil || s.Now().Sub(s.checksAt) > time.Minute {
		s.checks, s.checksAt = s.Checks(ctx), s.Now()
	}
	return s.checks
}

// API is the helper API. It's an http.Handler; schedules run through RunSchedule.
type API struct {
	http.Handler
	s *server
}

// New returns the API.
func New(d Deps) *API {
	if d.AllowRemote == nil {
		d.AllowRemote = netinfo.AllowedRemote
	}
	s := &server{Deps: d, limiter: newLimiter(d.Now), quota: newQuota(d.Now)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/hello", s.hello)
	mux.HandleFunc("POST /v2/pair", s.pair)
	mux.HandleFunc("GET /v2/update", s.with(store.ScopeAdmin, s.ownersOnly(s.updateStatus)))
	mux.HandleFunc("POST /v2/update", s.with(store.ScopeAdmin, s.ownersOnly(s.updateInstall)))
	mux.HandleFunc("POST /v2/settings", s.with(store.ScopeAdmin, s.ownersOnly(s.putSettings)))
	mux.HandleFunc("POST /v2/uninstall", s.with(store.ScopeAdmin, s.ownersOnly(s.uninstall)))
	mux.HandleFunc("GET /v2/pairing", s.with(store.ScopeAdmin, s.ownersOnly(s.pairing))) // codes mint keys
	mux.HandleFunc("GET /v2/owners", s.with(store.ScopeAdmin, s.ownersOnly(s.listOwners)))
	mux.HandleFunc("POST /v2/owners", s.with(store.ScopeAdmin, s.ownersOnly(s.addOwner)))
	mux.HandleFunc("POST /v2/owners/claim", s.with(store.ScopeAdmin, s.ownersOnly(s.claimOwner)))
	mux.HandleFunc("DELETE /v2/owners/{node}", s.with(store.ScopeAdmin, s.ownersOnly(s.removeOwner)))
	mux.HandleFunc("PUT /v2/peers/{name}/key", s.with(store.ScopeAdmin, s.ownersOnly(s.setPeerKey)))
	mux.HandleFunc("GET /v2/status", s.with(store.ScopeStatus, s.status))
	mux.HandleFunc("GET /v2/peers", s.with(store.ScopeStatus, s.peers))
	mux.HandleFunc("POST /v2/power/{action}", s.with(store.ScopePower, s.power))
	mux.HandleFunc("POST /v2/wake", s.with(store.ScopeWake, s.wake))
	mux.HandleFunc("POST /v2/checks/{id}/fix", s.with(store.ScopeAdmin, s.ownersOnly(s.fix))) // changes system settings
	mux.HandleFunc("GET /v2/events", s.with(store.ScopeStatus, s.events))
	// Keys are the owner's: a key from a pairing code mustn't mint wider keys or revoke others.
	mux.HandleFunc("GET /v2/keys", s.with(store.ScopeAdmin, s.ownersOnly(s.listKeys)))
	mux.HandleFunc("POST /v2/keys", s.with(store.ScopeAdmin, s.ownersOnly(s.addKey)))
	mux.HandleFunc("DELETE /v2/keys/{id}", s.with(store.ScopeAdmin, s.ownersOnly(s.removeKey)))
	mux.HandleFunc("DELETE /v2/key", s.with("", s.revokeOwn))
	mux.HandleFunc("GET /v2/schedules", s.with(store.ScopeStatus, s.listSchedules))
	mux.HandleFunc("POST /v2/schedules", s.with(store.ScopeAdmin, s.addSchedule))
	mux.HandleFunc("DELETE /v2/schedules/{id}", s.with(store.ScopeAdmin, s.removeSchedule))
	mux.HandleFunc("POST /v2/devices/{name}/{action}", s.with(store.ScopeStatus, s.relay))
	mux.HandleFunc("POST /mcp", s.with(store.ScopeStatus, s.ownersAndAgents(s.mcp)))
	mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed) // no server-initiated stream
	})
	return &API{Handler: s.guard(mux), s: s}
}

// RunSchedule performs a schedule's action now, under the same rules as any other caller.
func (a *API) RunSchedule(ctx context.Context, sc store.Schedule) {
	c := auth.Caller{Kind: auth.Owner, Name: "Schedule", Scopes: []string{store.ScopeStatus, store.ScopeWake, store.ScopePower}}
	var e *apiError
	switch sc.Action {
	case "wake":
		_, e = a.s.act(ctx, c, sc.Target, "wake")
	default:
		req := powerRequest{}
		if !sc.SkipIfInUse {
			req.Force = true
			c.Scopes = append(c.Scopes, store.ScopeForce)
		}
		_, e = a.s.localPower(ctx, c, sc.Action, req)
	}
	if e != nil {
		a.s.logEvent("Schedule", sc.Action+" "+sc.Target+" at "+sc.Time, "skipped: "+e.Message)
	}
}

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"error"`
	Message string `json:"message"`
	Extra   any    `json:"-"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, apiError{Code: errCode, Message: msg})
}

func failErr(w http.ResponseWriter, e *apiError) {
	if e.Extra == nil {
		writeJSON(w, e.Status, e)
		return
	}
	body := map[string]any{"error": e.Code, "message": e.Message}
	if m, ok := e.Extra.(map[string]any); ok {
		for k, v := range m {
			body[k] = v
		}
	}
	writeJSON(w, e.Status, body)
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
		// Browsers send Origin; PowerDeck's clients don't. Refusing it stops web pages from driving
		// the API (defence in depth: the self-signed certificate already blocks them).
		if r.Header.Get("Origin") != "" {
			fail(w, http.StatusForbidden, "forbidden", "Browsers can't use this API.")
			return
		}
		if s.limiter.blocked(ip) {
			w.Header().Set("Retry-After", "600")
			fail(w, http.StatusTooManyRequests, "throttled", "Too many failed attempts. Try again in 10 minutes.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		next.ServeHTTP(w, r)
	})
}

type callerKey struct{}

// with authenticates the caller and requires scope ("" = any authenticated caller).
func (s *server) with(scope string, h func(http.ResponseWriter, *http.Request, auth.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := remoteAddr(r)
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, err := s.Auth.Resolve(r.Context(), ip, strings.TrimSpace(bearer))
		if errors.Is(err, auth.ErrUnavailable) {
			fail(w, http.StatusServiceUnavailable, "tailscale", "Tailscale on this device isn't answering. Try again in a moment.")
			return
		}
		if err != nil {
			s.limiter.fail(ip)
			fail(w, http.StatusUnauthorized, "unauthorized", "This key isn't valid here any more. Add the device again.")
			return
		}
		if c.Kind == auth.Nobody {
			fail(w, http.StatusUnauthorized, "not_owner",
				"This phone isn't one of "+s.Name+"'s devices yet. Scan its code, or enter the pairing code it shows.")
			return
		}
		// A helper relaying for someone (a hub acting for an agent or a schedule) names them.
		if who := clean(r.Header.Get(peer.ForHeader), 64); who != "" && c.Relays() {
			c.Name = who + " via " + c.Name
		}
		if r.Header.Get(peer.AgentHeader) != "" && c.Relays() {
			c.Agent = true
		}
		if scope != "" && !c.Has(scope) {
			fail(w, http.StatusForbidden, "scope", "This key isn't allowed to do that.")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)), c)
	}
}

// ownersOnly refuses every key holder.
func (s *server) ownersOnly(h func(http.ResponseWriter, *http.Request, auth.Caller)) func(http.ResponseWriter, *http.Request, auth.Caller) {
	return func(w http.ResponseWriter, r *http.Request, c auth.Caller) {
		if c.Kind != auth.Owner {
			fail(w, http.StatusForbidden, "scope", "Only your own devices can do that.")
			return
		}
		h(w, r, c)
	}
}

// ownersAndAgents refuses keys from pairing codes: they're for one device, not the whole fleet.
func (s *server) ownersAndAgents(h func(http.ResponseWriter, *http.Request, auth.Caller)) func(http.ResponseWriter, *http.Request, auth.Caller) {
	return func(w http.ResponseWriter, r *http.Request, c auth.Caller) {
		if c.Kind != auth.Owner && !c.IsAutomation() {
			fail(w, http.StatusForbidden, "scope", "Only your own devices and agent keys can use this.")
			return
		}
		h(w, r, c)
	}
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

func decode(r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	return json.NewDecoder(r.Body).Decode(v) == nil
}

func (s *server) logEvent(who, action, result string) {
	s.Store.AddEvent(store.Event{At: s.Now(), Who: who, Action: action, Result: result})
	s.Log.Info("event", "who", who, "action", action, "result", result)
}

func (s *server) hello(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app": "PowerDeck", "apiVersion": version.APIVersion, "version": version.Version, "name": s.Name, "os": s.OS,
	})
}

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
}

func (s *server) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if !decode(r, &req) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return
	}
	ip := remoteAddr(r)
	if s.limiter.blocked(ip) {
		fail(w, http.StatusTooManyRequests, "throttled", "Too many failed attempts. Try again in 10 minutes.")
		return
	}
	if !s.Pairing.Consume(req.Code) {
		s.limiter.fail(ip)
		s.Log.Warn("pairing rejected", "from", ip)
		fail(w, http.StatusForbidden, "bad_code", "That pairing code is wrong or expired. Show a fresh one on the device.")
		return
	}
	name := clean(req.DeviceName, 64)
	if name == "" {
		name = "Phone"
	}
	// A phone on this device's own Tailscale account becomes an owner device: the code proves
	// someone at this device let it in, and Tailscale proves which node it is from then on.
	if id, err := s.Auth.TS.WhoIs(r.Context(), ip); err == nil && s.Auth.SameAccount(r.Context(), id) {
		if err := s.Store.AddOwner(store.Owner{NodeID: id.NodeID, Name: name, AddedAt: s.Now(), AddedBy: "pairing code"}); err != nil {
			fail(w, http.StatusConflict, "too_many_owners", "Too many owner devices. Remove one first.")
			return
		}
		s.logEvent(name, "added as an owner device", "ok")
		c := auth.Caller{Kind: auth.Owner, Name: name, NodeID: id.NodeID, Node: id.NodeName, Scopes: store.AllScopes}
		writeJSON(w, http.StatusOK, map[string]any{"owner": true, "status": s.statusBody(r.Context(), c)})
		return
	}
	k, secret, err := s.Store.AddKey(store.NewKey{Name: name, Kind: store.KindPhone, Scopes: store.PhoneScopes}, s.Now())
	if errors.Is(err, store.ErrTooManyKeys) {
		fail(w, http.StatusConflict, "too_many_keys", "Too many keys. Remove one on the device first.")
		return
	}
	if err != nil {
		s.Log.Error("saving key failed", "err", err)
		fail(w, http.StatusInternalServerError, "internal", "Couldn't save the key: "+err.Error())
		return
	}
	s.logEvent(name, "paired", "ok")
	c := auth.Caller{Kind: auth.Holder, Name: name, Scopes: k.Scopes, Key: &k}
	writeJSON(w, http.StatusOK, map[string]any{"key": secret, "status": s.statusBody(r.Context(), c)})
}

func (s *server) listOwners(w http.ResponseWriter, _ *http.Request, c auth.Caller) {
	list := []map[string]any{}
	for _, o := range s.Store.Owners() {
		list = append(list, map[string]any{"node": o.NodeID, "name": o.Name, "addedAt": o.AddedAt, "addedBy": o.AddedBy, "you": o.NodeID == c.NodeID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"legacy": s.Store.OwnerMode() == store.OwnersLegacy, "owners": list})
}

// claimOwner records the calling owner as an owner device. On a helper upgraded from before owner
// devices that ends the old "any device on my Tailscale account" rule; the app does it on its own.
func (s *server) claimOwner(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if c.NodeID == "" || c.NodeID == "local" {
		fail(w, http.StatusBadRequest, "not_a_device", "Only a phone or computer on the tailnet can be an owner device.")
		return
	}
	var req struct {
		DeviceName string `json:"deviceName"`
	}
	_ = decode(r, &req)
	name := clean(req.DeviceName, 64)
	if name == "" {
		name = c.Name
	}
	legacy := s.Store.OwnerMode() == store.OwnersLegacy
	if err := s.Store.AddOwner(store.Owner{NodeID: c.NodeID, Name: name, AddedAt: s.Now(), AddedBy: map[bool]string{true: "upgrade", false: "owner"}[legacy]}); err != nil {
		fail(w, http.StatusConflict, "too_many_owners", "Too many owner devices. Remove one first.")
		return
	}
	if legacy {
		s.logEvent(name, "became the first owner device (other devices on the account now need the code)", "ok")
	}
	w.WriteHeader(http.StatusNoContent)
}

// addOwner records another tailnet device as an owner device by its Tailscale name (`powerdeck-helper
// owner add`, or an owner in the app). Any untagged device that isn't shared in from another tailnet
// can be chosen: whoever runs this already controls the helper.
func (s *server) addOwner(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(r, &req) || strings.TrimSpace(req.Name) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "Give the device's Tailscale name.")
		return
	}
	st, err := s.Auth.TS.Status(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "tailscale", "Tailscale on this device isn't answering. Try again in a moment.")
		return
	}
	want := strings.ToLower(strings.TrimSpace(req.Name))
	var found *tailscale.Node
	for i, n := range st.Peers {
		if strings.ToLower(n.Name()) == want || strings.ToLower(n.HostName) == want || strings.ToLower(n.DNSName) == want {
			found = &st.Peers[i]
			break
		}
	}
	switch {
	case found == nil:
		fail(w, http.StatusNotFound, "unknown_device", "No device called "+clean(req.Name, 64)+" on this tailnet.")
		return
	case len(found.Tags) > 0 || found.Shared:
		fail(w, http.StatusBadRequest, "not_personal", found.Name()+" is a tagged or shared-in device; only personal devices can be owner devices.")
		return
	}
	if err := s.Store.AddOwner(store.Owner{NodeID: found.ID, Name: found.HostName, AddedAt: s.Now(), AddedBy: c.Name}); err != nil {
		fail(w, http.StatusConflict, "too_many_owners", "Too many owner devices. Remove one first.")
		return
	}
	s.logEvent(c.Name, "made "+found.Name()+" an owner device", "ok")
	writeJSON(w, http.StatusOK, map[string]any{"node": found.ID, "name": found.HostName})
}

func (s *server) removeOwner(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if err := s.Store.RemoveOwner(r.PathValue("node")); err != nil {
		fail(w, http.StatusNotFound, "not_found", "No such owner device.")
		return
	}
	s.logEvent(c.Name, "removed an owner device", "ok")
	w.WriteHeader(http.StatusNoContent)
}

// setPeerKey stores the peer key another helper issued for this one (the owner's phone passes it
// on), so this helper can reach it with status, wake and power only.
func (s *server) setPeerKey(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req struct {
		Key string `json:"key"`
	}
	if !decode(r, &req) || (req.Key != "" && !strings.HasPrefix(req.Key, store.KeyPrefix)) {
		fail(w, http.StatusBadRequest, "bad_request", "That isn't a PowerDeck key.")
		return
	}
	name := r.PathValue("name")
	if err := s.Store.SetPeerKey(name, req.Key); err != nil {
		fail(w, http.StatusNotFound, "unknown_device", "This helper hasn't seen "+clean(name, 64)+" on the tailnet yet. Try again in a few minutes.")
		return
	}
	s.logEvent(c.Name, "set the key for "+clean(name, 64), "ok")
	w.WriteHeader(http.StatusNoContent)
}

// pairing shows the add link and current pairing code, for the CLI (`powerdeck-helper qr`) and for
// an owner who wants to let someone on another Tailscale account in.
func (s *server) pairing(w http.ResponseWriter, _ *http.Request, _ auth.Caller) {
	link, code := s.PairLink()
	writeJSON(w, http.StatusOK, map[string]any{"link": link, "code": code, "expires": s.Pairing.Current().Expires})
}

func (s *server) statusBody(ctx context.Context, c auth.Caller) map[string]any {
	up, boot := s.Power.Uptime()
	body := map[string]any{
		"name": s.Name, "os": s.OS, "version": version.Version, "apiVersion": version.APIVersion,
		"uptimeSeconds": int64(up.Seconds()), "pending": s.Power.Pending(),
		"utcOffsetMinutes": utcOffsetMinutes(s.Now()), // schedules run on this device's clock
		"caps":             []string{"shutdown", "restart"},
		"you":              map[string]any{"kind": c.Kind, "name": c.Name, "scopes": c.Scopes},
	}
	if !boot.IsZero() {
		body["bootTime"] = boot.UTC().Truncate(time.Second)
	}
	if p := s.Power.Presence(); p != nil {
		body["presence"] = p
	}
	if s.Power.UpdatesPending() {
		body["updatesPending"] = true
	}
	if n, ok := s.Network(); ok {
		body["network"] = n
		if n.Wired {
			body["caps"] = []string{"shutdown", "restart", "wake"}
		}
	}
	if g := s.Guests(ctx); g != nil {
		body["guests"] = g
	}
	if s.Update != nil && c.Kind == auth.Owner {
		u := s.Update.Status() // what the last check found; no network here
		body["update"] = map[string]any{"available": u.Available, "latest": u.Latest, "installing": u.Installing,
			"error": u.Error, "auto": s.Store.Settings().AutoUpdate}
	}
	if c.Has(store.ScopeAdmin) || c.Kind == auth.Owner {
		body["checks"] = s.cachedChecks(ctx)
	}
	if c.Kind == auth.Owner {
		// Owners see whether agents may use this device, and which agent keys this helper holds.
		keys := []map[string]any{}
		for _, k := range s.Store.Keys() {
			if k.Kind == store.KindAutomation {
				keys = append(keys, map[string]any{"name": k.Name, "devices": k.Devices})
			}
		}
		body["agents"] = map[string]any{"allowed": !s.Store.Settings().NoAgents, "keys": keys}
		if s.Uninstall != nil {
			body["caps"] = append(body["caps"].([]string), "uninstall")
		}
		// For the app: whether to claim this helper, and which peers it can already reach.
		peerKeys := []string{}
		for _, p := range s.Store.Peers() {
			if p.Key != "" {
				peerKeys = append(peerKeys, p.Name)
			}
		}
		body["owners"] = map[string]any{"legacy": s.Store.OwnerMode() == store.OwnersLegacy, "you": s.Store.IsOwnerNode(c.NodeID)}
		body["peerKeys"] = peerKeys
	} else if c.IsPeer() {
		// A hub needs this to honour the switch for agents before it even asks.
		body["agents"] = map[string]any{"allowed": !s.Store.Settings().NoAgents}
	}
	return body
}

func (s *server) updateBody(u update.Status) map[string]any {
	return map[string]any{"current": u.Current, "latest": u.Latest, "available": u.Available, "checkedAt": u.CheckedAt,
		"error": u.Error, "installing": u.Installing, "auto": s.Store.Settings().AutoUpdate}
}

// updateStatus reports this helper's update state; ?check=1 asks GitHub first.
func (s *server) updateStatus(w http.ResponseWriter, r *http.Request, _ auth.Caller) {
	if s.Update == nil {
		fail(w, http.StatusNotImplemented, "no_updates", "This build doesn't update itself.")
		return
	}
	u := s.Update.Status()
	if r.URL.Query().Get("check") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		u, _ = s.Update.Check(ctx)
		cancel()
	}
	writeJSON(w, http.StatusOK, s.updateBody(u))
}

// updateInstall installs the latest release now. The helper restarts, so it answers first.
func (s *server) updateInstall(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.Update == nil {
		fail(w, http.StatusNotImplemented, "no_updates", "This build doesn't update itself.")
		return
	}
	if s.Power.Pending() != nil {
		fail(w, http.StatusConflict, "pending", "A shutdown or restart is counting down. Update after it, or cancel it first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	u, err := s.Update.Check(ctx)
	cancel()
	if err != nil {
		fail(w, http.StatusBadGateway, "update_check", "Couldn't check for updates: "+err.Error())
		return
	}
	if !u.Available {
		writeJSON(w, http.StatusOK, s.updateBody(u))
		return
	}
	if u.Installing {
		writeJSON(w, http.StatusAccepted, s.updateBody(u))
		return
	}
	s.logEvent(c.Name, "started update to "+u.Latest, "ok")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := s.Update.Install(ctx); err != nil {
			s.Log.Error("update install failed", "err", err)
		}
	}()
	u.Installing = true
	writeJSON(w, http.StatusAccepted, s.updateBody(u))
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req struct {
		AutoUpdate *bool `json:"autoUpdate"`
		Agents     *bool `json:"agents"`
	}
	if !decode(r, &req) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed settings.")
		return
	}
	if req.Agents != nil && *req.Agents == s.Store.Settings().NoAgents {
		if err := s.Store.UpdateSettings(func(st *store.Settings) { st.NoAgents = !*req.Agents }); err != nil {
			fail(w, http.StatusInternalServerError, "store", err.Error())
			return
		}
		s.logEvent(c.Name, map[bool]string{true: "allowed AI agents", false: "turned AI agents away"}[*req.Agents], "ok")
	}
	if req.AutoUpdate != nil && *req.AutoUpdate != s.Store.Settings().AutoUpdate {
		if err := s.Store.SetAutoUpdate(*req.AutoUpdate); err != nil {
			fail(w, http.StatusInternalServerError, "store", err.Error())
			return
		}
		s.checksMu.Lock()
		s.checks = nil // the "automatic updates" check changes
		s.checksMu.Unlock()
		s.logEvent(c.Name, map[bool]string{true: "turned automatic updates on", false: "turned automatic updates off"}[*req.AutoUpdate], "ok")
	}
	w.WriteHeader(http.StatusNoContent)
}

// uninstall removes PowerDeck from this device. The body must name the device, so a request meant
// for another one can't remove this one.
func (s *server) uninstall(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !decode(r, &req) || !strings.EqualFold(req.Confirm, s.Name) {
		fail(w, http.StatusBadRequest, "confirm", "Name this device ("+s.Name+") to uninstall PowerDeck from it.")
		return
	}
	if s.Uninstall == nil {
		fail(w, http.StatusNotImplemented, "no_uninstall", "PowerDeck can't uninstall itself here. Uninstall it on the device.")
		return
	}
	if s.Power.Pending() != nil {
		fail(w, http.StatusConflict, "pending", "A shutdown or restart is counting down. Cancel it first.")
		return
	}
	// Uninstall only starts the work (it waits a moment), so the answer still reaches the phone and
	// a failure to start is reported to it.
	if err := s.Uninstall(); err != nil {
		s.Log.Error("uninstall failed", "err", err)
		s.logEvent(c.Name, "uninstall PowerDeck", "failed")
		fail(w, http.StatusInternalServerError, "uninstall_failed", "PowerDeck couldn't start its uninstaller: "+err.Error())
		return
	}
	s.logEvent(c.Name, "uninstalled PowerDeck", "ok")
	w.WriteHeader(http.StatusAccepted)
}

func utcOffsetMinutes(t time.Time) int {
	_, off := t.Local().Zone()
	return off / 60
}

func (s *server) status(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	writeJSON(w, http.StatusOK, s.statusBody(r.Context(), c))
}

func (s *server) peers(w http.ResponseWriter, r *http.Request, _ auth.Caller) {
	if r.URL.Query().Get("refresh") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		_ = s.Fleet.Scan(ctx)
		cancel()
	}
	found, at := s.Fleet.Found()
	if found == nil {
		found = []discoveryPeer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkedAt": at.UTC(), "peers": found})
}

type powerRequest struct {
	DelaySeconds   *int `json:"delaySeconds"`
	Force          bool `json:"force"`
	InstallUpdates bool `json:"installUpdates"`
}

func (s *server) power(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req powerRequest
	if !decode(r, &req) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return
	}
	res, e := s.localPower(r.Context(), c, r.PathValue("action"), req)
	if e != nil {
		failErr(w, e)
		return
	}
	code := http.StatusAccepted
	if r.PathValue("action") == "cancel" {
		code = http.StatusOK
	}
	writeJSON(w, code, res)
}

// localPower runs shutdown/restart/cancel on this device, with every rule applied.
func (s *server) localPower(ctx context.Context, c auth.Caller, action string, req powerRequest) (map[string]any, *apiError) {
	if !c.MayUse(s.Name) {
		return nil, &apiError{Status: http.StatusForbidden, Code: "device", Message: "This key can't control " + s.Name + "."}
	}
	if c.ForAgent() && action != "cancel" {
		if e := s.agentsAllowed(ctx, s.Name, false); e != nil {
			return nil, e
		}
	}
	if c.IsAutomation() && !c.Key.AllowSelf && s.SelfNode != nil && c.NodeID != "" && c.NodeID == s.SelfNode(ctx) {
		return nil, &apiError{Status: http.StatusForbidden, Code: "self",
			Message: "An agent can't shut down or restart the device it runs on."}
	}
	if action == "cancel" {
		err := s.Power.Cancel()
		switch {
		case err == nil:
			s.logEvent(c.Name, "cancel", "ok")
			return map[string]any{"pending": nil}, nil
		case errors.Is(err, power.ErrNothingPending):
			return nil, &apiError{Status: http.StatusConflict, Code: "nothing_pending", Message: "Nothing was scheduled."}
		default:
			s.Log.Error("cancel failed", "err", err)
			return nil, &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "The OS refused to cancel: " + err.Error()}
		}
	}
	var a power.Action
	switch action {
	case "shutdown":
		a = power.Shutdown
	case "restart":
		a = power.Restart
	default:
		return nil, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "Unknown action."}
	}
	if req.Force && !c.Has(store.ScopeForce) {
		return nil, &apiError{Status: http.StatusForbidden, Code: "scope", Message: "This key can't override someone using the device."}
	}
	if c.IsAutomation() {
		if e := s.quota.take(c.Key.ID+"/"+s.Name, kindPower); e != nil {
			return nil, e
		}
	}
	delay := 60
	if req.DelaySeconds != nil {
		delay = min(max(*req.DelaySeconds, 0), 600)
	}
	if c.IsAutomation() {
		delay = max(delay, 60) // agents always leave the visible minute to cancel
	}
	p, err := s.Power.Do(a, power.Options{Delay: time.Duration(delay) * time.Second, By: c.Name, Force: req.Force, InstallUpdates: req.InstallUpdates})
	switch {
	case err == nil:
		s.logEvent(c.Name, string(a), "scheduled")
		return map[string]any{"pending": p}, nil
	case errors.Is(err, power.ErrInUse):
		s.logEvent(c.Name, string(a), "refused: in use")
		return nil, &apiError{Status: http.StatusConflict, Code: "in_use", Message: "Someone is using this device.",
			Extra: map[string]any{"presence": s.Power.Presence()}}
	case errors.Is(err, power.ErrUpdating):
		return nil, &apiError{Status: http.StatusConflict, Code: "updating", Message: "The helper is installing an update. Try again in a minute."}
	case errors.Is(err, power.ErrPending):
		return nil, &apiError{Status: http.StatusConflict, Code: "pending", Message: "A shutdown or restart is already scheduled.",
			Extra: map[string]any{"pending": s.Power.Pending()}}
	default:
		s.Log.Error("power failed", "action", a, "err", err)
		return nil, &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "The OS refused: " + err.Error()}
	}
}

func (s *server) wake(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req struct {
		MAC string `json:"mac"`
	}
	if !decode(r, &req) || req.MAC == "" {
		fail(w, http.StatusBadRequest, "bad_request", "Send {\"mac\": \"aa:bb:cc:dd:ee:ff\"}.")
		return
	}
	mac, err := net.ParseMAC(req.MAC)
	if err != nil || len(mac) != 6 {
		fail(w, http.StatusBadRequest, "bad_request", "That isn't a MAC address.")
		return
	}
	// A raw MAC could be any device, so keys limited to some devices wake by name (the relay).
	// Pairing-code keys may: a tagged hub has no owner, and a magic packet only ever turns things on.
	if c.Key != nil && len(c.Key.Devices) > 0 {
		fail(w, http.StatusForbidden, "device", "This key can't wake other devices directly.")
		return
	}
	if c.ForAgent() {
		// Compare addresses, not text: aa-bb-… and aabb.cc… name the same card.
		for _, p := range s.Store.Peers() {
			if pm, err := net.ParseMAC(p.MAC); err == nil && p.NoAgents && bytes.Equal(pm, mac) {
				failErr(w, s.agentsAllowed(r.Context(), p.Name, false))
				return
			}
		}
	}
	if c.IsAutomation() {
		if e := s.quota.take(c.Key.ID+"/"+mac.String(), kindWake); e != nil {
			failErr(w, e)
			return
		}
	}
	n, _ := s.Network()
	sent, err := s.Wake(mac.String(), n.LAN)
	if err != nil {
		fail(w, http.StatusBadRequest, "wake_failed", err.Error())
		return
	}
	s.logEvent(c.Name, "wake "+mac.String(), "sent")
	writeJSON(w, http.StatusOK, map[string]any{"sentOn": sent})
}

func (s *server) fix(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	res, err := s.Fix(r.Context(), r.PathValue("id"))
	s.checksMu.Lock()
	s.checks = nil
	s.checksMu.Unlock()
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "fix_failed", err.Error())
		return
	}
	s.logEvent(c.Name, "fix "+r.PathValue("id"), string(res.Level))
	writeJSON(w, http.StatusOK, map[string]any{"check": res})
}

func (s *server) events(w http.ResponseWriter, _ *http.Request, _ auth.Caller) {
	writeJSON(w, http.StatusOK, map[string]any{"events": s.Store.Events()})
}

type keyView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Scopes    []string  `json:"scopes"`
	Devices   []string  `json:"devices,omitempty"`
	AllowSelf bool      `json:"allowSelf,omitempty"`
	Bound     bool      `json:"bound"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
}

func viewKey(k store.Key) keyView {
	return keyView{ID: k.ID, Name: k.Name, Kind: k.Kind, Scopes: k.Scopes, Devices: k.Devices, AllowSelf: k.AllowSelf,
		Bound: k.BoundNode != "", CreatedAt: k.CreatedAt, LastUsed: k.LastUsed}
}

func (s *server) listKeys(w http.ResponseWriter, _ *http.Request, _ auth.Caller) {
	out := []keyView{}
	for _, k := range s.Store.Keys() {
		out = append(out, viewKey(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *server) addKey(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req struct {
		Name      string   `json:"name"`
		Kind      string   `json:"kind"`
		Scopes    []string `json:"scopes"`
		Devices   []string `json:"devices"`
		AllowSelf bool     `json:"allowSelf"`
	}
	if !decode(r, &req) || clean(req.Name, 64) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "A key needs a name.")
		return
	}
	if req.Kind == store.KindPeer {
		s.addPeerKey(w, clean(req.Name, 64), c)
		return
	}
	if req.Kind != "" && req.Kind != store.KindAutomation {
		fail(w, http.StatusBadRequest, "bad_kind", "Keys are for agents (automation) or other helpers (peer).")
		return
	}
	scopes, err := auth.ParseScopes(strings.Join(req.Scopes, ","))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_scopes", err.Error())
		return
	}
	var devices []string
	for _, d := range req.Devices {
		if d = clean(d, 64); d != "" {
			devices = append(devices, d)
		}
	}
	k, secret, err := s.Store.AddKey(store.NewKey{Name: clean(req.Name, 64), Kind: store.KindAutomation, Scopes: scopes,
		Devices: devices, AllowSelf: req.AllowSelf}, s.Now())
	if errors.Is(err, store.ErrTooManyKeys) {
		fail(w, http.StatusConflict, "too_many_keys", "Too many keys. Remove one first.")
		return
	}
	if err != nil {
		s.Log.Error("saving key failed", "err", err)
		fail(w, http.StatusInternalServerError, "internal", "Couldn't save the key: "+err.Error())
		return
	}
	s.logEvent(c.Name, "created key "+k.Name, strings.Join(scopes, ","))
	writeJSON(w, http.StatusOK, map[string]any{"id": k.ID, "key": secret, "info": viewKey(k)})
}

// addPeerKey issues the key another helper of this owner uses to reach this one (replacing any it
// had): status, wake and power, bound to that helper's node on first use, no relaying.
func (s *server) addPeerKey(w http.ResponseWriter, name string, c auth.Caller) {
	for _, k := range s.Store.Keys() {
		if k.Kind == store.KindPeer && strings.EqualFold(k.Name, name) {
			_ = s.Store.RemoveKey(k.ID)
		}
	}
	k, secret, err := s.Store.AddKey(store.NewKey{Name: name, Kind: store.KindPeer, Scopes: store.PeerScopes}, s.Now())
	if errors.Is(err, store.ErrTooManyKeys) {
		fail(w, http.StatusConflict, "too_many_keys", "Too many keys. Remove one first.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", "Couldn't save the key: "+err.Error())
		return
	}
	s.logEvent(c.Name, "created peer key for "+name, strings.Join(k.Scopes, ","))
	writeJSON(w, http.StatusOK, map[string]any{"id": k.ID, "key": secret, "info": viewKey(k)})
}

func (s *server) removeKey(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if err := s.Store.RemoveKey(r.PathValue("id")); err != nil {
		fail(w, http.StatusNotFound, "not_found", "No such key.")
		return
	}
	s.logEvent(c.Name, "removed key "+r.PathValue("id"), "ok")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) revokeOwn(w http.ResponseWriter, _ *http.Request, c auth.Caller) {
	if c.Key == nil {
		fail(w, http.StatusBadRequest, "no_key", "You're trusted through your Tailscale account; there's no key to remove.")
		return
	}
	_ = s.Store.RemoveKey(c.Key.ID)
	s.logEvent(c.Name, "removed itself", "ok")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listSchedules(w http.ResponseWriter, _ *http.Request, _ auth.Caller) {
	out := s.Store.Schedules()
	if out == nil {
		out = []store.Schedule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (s *server) addSchedule(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	sc := store.Schedule{Enabled: true, SkipIfInUse: true} // what a request leaves out
	if !decode(r, &sc) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed schedule.")
		return
	}
	if err := ValidateSchedule(&sc, s.Name); err != nil {
		fail(w, http.StatusBadRequest, "bad_schedule", err.Error())
		return
	}
	if sc.Action == "wake" {
		// A wake schedule acts on another device, which only owners may reach.
		if c.Kind != auth.Owner {
			fail(w, http.StatusForbidden, "scope", "Only your own devices can schedule waking another device.")
			return
		}
		if _, err := s.Fleet.Peer(sc.Target); err != nil {
			fail(w, http.StatusBadRequest, "bad_schedule", "This helper doesn't know "+sc.Target+" yet.")
			return
		}
	}
	saved, err := s.Store.AddSchedule(sc)
	if err != nil {
		fail(w, http.StatusConflict, "too_many_schedules", err.Error())
		return
	}
	s.logEvent(c.Name, "added schedule "+saved.Action+" "+saved.Time, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"id": saved.ID, "schedule": saved})
}

func (s *server) removeSchedule(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if err := s.Store.RemoveSchedule(r.PathValue("id")); err != nil {
		fail(w, http.StatusNotFound, "not_found", "No such schedule.")
		return
	}
	s.logEvent(c.Name, "removed schedule", "ok")
	w.WriteHeader(http.StatusNoContent)
}
