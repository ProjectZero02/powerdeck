// Package machines is the hub's power layer for machines wired to a NanoKVM: which KVM drives which
// machine, live state from the KVM's power light and the machine's helper, the power actions with
// the right tool for each state, and what was learned about the wiring.
//
// The rule throughout: the power light is read before and after every press, and nothing is
// pressed when it can't be read or isn't known to be wired. A press on a running computer can
// switch it off, so a guess is never good enough.
package machines

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/secret"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// KVM is what the hub needs from a NanoKVM (a *nanokvm.Client).
type KVM interface {
	PowerLED(ctx context.Context) (bool, error)
	Press(ctx context.Context, button string, d time.Duration) error
	Wake(ctx context.Context, mac string) error
}

// Fleet is what the hub needs from the helper's fleet.
type Fleet interface {
	Peer(name string) (store.Peer, error)
	Wake(name string) ([]string, error)
	Forward(ctx context.Context, name, method, path string, body, out any, who string) error
	Answers(ctx context.Context, name string) bool
}

// Hub drives the KVMs this helper holds.
type Hub struct {
	Store *store.Store
	Box   secret.Box
	Fleet Fleet
	Self  string // this helper's name: it can't be its own KVM's machine
	Log   *slog.Logger
	Now   func() time.Time
	// Dial makes a client for a stored KVM (default: a NanoKVM client that remembers a new
	// certificate pin and HTTPS address in the store).
	Dial func(k store.KVM, password string) KVM
	// AnyAddress lets tests and development use loopback KVMs (always allowed) and LAN addresses.
	AnyAddress bool

	// Timings (zero: defaults). Tests shorten them.
	Poll        time.Duration // between LED readings while waiting for a change (1 s)
	Settle      time.Duration // how long the LED may take to change after a press (15 s)
	OffWait     time.Duration // between a forced off and the power press that follows (5 s)
	ResetVerify time.Duration // how long a reset machine may take to answer again (5 min)
	VerifyEvery time.Duration // between checks while verifying a reset (10 s)
	LEDGap      time.Duration // between the three light readings of one look (0.5 s)
	AskGap      time.Duration // between the two tries at a silent helper in one look (1.5 s)
	AsleepAfter time.Duration // after a sleep note, when to read the light asleep (30 s)

	// Watching (Watch). Ping is the hub's ping of a machine's address (replies, no_reply,
	// unavailable; nil: unavailable); Online is whether Tailscale lists it online (nil: unknown).
	Ping      func(ctx context.Context, addr string) string
	Online    func(ctx context.Context, addr string) *bool
	HungAfter func() time.Duration // the owner's setting (0: the default)
	AutoReset func() time.Duration // the owner's setting: confirm the hub's own reset after this (0: never)
	Events    *Bus                 // state changes and recoveries; nil: not published

	mu      sync.Mutex
	busy    map[string]bool
	clients map[string]cached
	tracks  map[string]*Track
	wg      sync.WaitGroup

	rmu        sync.Mutex // recoveries (recovery.go); never held while taking mu
	recs       []Recovery
	recsLoaded bool
	asked      map[string]bool // machines the hub proposed a reset for during this hang
}

type cached struct {
	sig string
	kvm KVM
}

// Error is a refusal or failure with an API status and code.
type Error struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any // more fields for the reply
}

func (e *Error) Error() string { return e.Message }

func fail(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

func dur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Hub) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// ---- KVM records ----

// KVMView is a KVM as the API shows it: never the password.
type KVMView struct {
	Name     string       `json:"name"`
	Address  string       `json:"address"`
	Model    string       `json:"model,omitempty"`
	User     string       `json:"user"`
	Machine  string       `json:"machine"`
	MAC      string       `json:"mac,omitempty"`
	Firmware string       `json:"firmware,omitempty"`
	Wiring   store.Wiring `json:"wiring"`
	AddedAt  time.Time    `json:"addedAt"`
}

// View hides the password.
func View(k store.KVM) KVMView {
	return KVMView{k.Name, k.Address, k.Model, k.User, k.Machine, k.MAC, k.Firmware, k.Wiring, k.AddedAt}
}

func (h *Hub) client(k store.KVM) (KVM, error) {
	sig := k.Address + "\x00" + k.User + "\x00" + k.Password + "\x00" + k.Model
	h.mu.Lock()
	if c, ok := h.clients[k.Name]; ok && c.sig == sig {
		h.mu.Unlock()
		return c.kvm, nil
	}
	h.mu.Unlock()
	pw, err := h.Box.Open(k.Password)
	if err != nil {
		return nil, fail(http.StatusConflict, "kvm_credentials", "%s's stored password can't be opened (%v). Link the KVM again.", k.Name, err)
	}
	c := h.dial(k, pw)
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[string]cached{}
	}
	h.clients[k.Name] = cached{sig, c}
	h.mu.Unlock()
	return c, nil
}

func (h *Hub) dial(k store.KVM, pw string) KVM {
	if h.Dial != nil {
		return h.Dial(k, pw)
	}
	name := k.Name
	c := &nanokvm.Client{Address: k.Address, User: k.User, Password: pw, Plain: k.Model == "pro", Pin: k.Pin}
	c.OnPin = func(pin string) {
		// Pinned on first sight; from then on it starts on HTTPS with this certificate.
		_ = h.Store.UpdateKVM(name, func(s *store.KVM) {
			if s.Pin == "" {
				s.Pin = pin
			}
		})
	}
	return &remember{c, h, name}
}

// remember stores the HTTPS address a KVM moved to, after a call that worked.
type remember struct {
	*nanokvm.Client
	h    *Hub
	name string
}

func (r *remember) PowerLED(ctx context.Context) (bool, error) {
	on, err := r.Client.PowerLED(ctx)
	if err == nil && strings.HasPrefix(r.URL(), "https://") {
		_ = r.h.Store.UpdateKVM(r.name, func(s *store.KVM) { s.Address = r.URL() })
	}
	return on, err
}

// LinkRequest adds a KVM.
type LinkRequest struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Model    string `json:"model"`
	User     string `json:"user"`
	Password string `json:"password"`
	Machine  string `json:"machine"`
	MAC      string `json:"mac"`
	// Helper: the machine runs a PowerDeck helper. This helper must know it, or a reset couldn't
	// first check that the machine has stopped answering.
	Helper bool `json:"helper"`
}

// allowedAddress: a Tailscale name or address (WireGuard protects the password, which the KVM only
// obscures with a public key), or loopback for development.
func (h *Hub) allowedAddress(addr string) (host string, ok bool) {
	a := addr
	if !strings.Contains(a, "://") {
		a = "http://" + a
	}
	u, err := url.Parse(a)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host = strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		return host, netinfo.IsTailnet(ip) || ip.IsLoopback() || h.AnyAddress
	}
	return host, strings.HasSuffix(host, ".ts.net") || h.AnyAddress
}

// Link checks a KVM's login and power light, then stores it (password sealed).
func (h *Hub) Link(ctx context.Context, r LinkRequest) (KVMView, *bool, error) {
	host, ok := h.allowedAddress(r.Address)
	if !ok {
		return KVMView{}, nil, fail(http.StatusBadRequest, "not_tailscale",
			"Reach the KVM by its Tailscale name (name.your-tailnet.ts.net) or Tailscale address, so its password only travels inside the tailnet.")
	}
	r.Address = strings.TrimRight(r.Address, "/")
	if r.User == "" {
		r.User = "admin"
	}
	if r.Model != "" && r.Model != "pro" {
		return KVMView{}, nil, fail(http.StatusBadRequest, "bad_request", "Unknown model %q.", r.Model)
	}
	if r.Name == "" {
		r.Name, _, _ = strings.Cut(host, ".")
	}
	r.Name = strings.TrimSpace(r.Name)
	r.Machine = strings.TrimSpace(r.Machine)
	if r.Name == "" || r.Machine == "" || len(r.Name) > 63 || len(r.Machine) > 63 {
		return KVMView{}, nil, fail(http.StatusBadRequest, "bad_request", "A KVM needs a name and the machine it's wired to.")
	}
	if fleet.IsSelf(r.Machine, h.Self) {
		return KVMView{}, nil, fail(http.StatusUnprocessableEntity, "self",
			"%s can't drive the KVM wired to itself (it couldn't check the result of a reset). Keep this KVM in the app, or link it on another always-on PowerDeck device.", h.Self)
	}
	if p, err := h.Fleet.Peer(r.Machine); err == nil {
		r.Machine = p.Name
	} else if r.Helper {
		return KVMView{}, nil, fail(http.StatusUnprocessableEntity, "unknown_helper",
			"%s hasn't seen %s's PowerDeck helper yet, so it couldn't tell a frozen %s from a running one. Try again in a few minutes, with %s on.", h.Self, r.Machine, r.Machine, r.Machine)
	}
	if r.MAC != "" {
		mac, err := normalMAC(r.MAC)
		if err != nil {
			return KVMView{}, nil, fail(http.StatusBadRequest, "bad_request", "%v", err)
		}
		r.MAC = mac
	}
	if old, ok := h.Store.KVMFor(r.Machine); ok && !strings.EqualFold(old.Name, r.Name) {
		return KVMView{}, nil, fail(http.StatusConflict, "machine_has_kvm", "%s is already wired to %s.", r.Machine, old.Name)
	}
	k := store.KVM{Name: r.Name, Address: r.Address, Model: r.Model, User: r.User, Machine: r.Machine, MAC: r.MAC,
		Wiring: store.Wiring{}.Fresh(), AddedAt: h.now().UTC()}
	c := &nanokvm.Client{Address: k.Address, User: k.User, Password: r.Password, Plain: k.Model == "pro"}
	var kc KVM = c
	if h.Dial != nil {
		kc = h.Dial(k, r.Password)
	}
	on, err := kc.PowerLED(ctx)
	if err != nil {
		return KVMView{}, nil, kvmError(k.Name, err)
	}
	if on {
		k.Wiring.LED = store.Wired // an unwired or reversed LED never reads on
	}
	if h.Dial == nil {
		k.Pin = c.Pin
		k.Address = c.URL()
		if info, err := c.Info(ctx); err == nil {
			k.Firmware = strings.TrimSpace(info.Application + " " + info.Hardware)
		}
	}
	if old, ok := h.Store.KVM(k.Name); ok && old.Machine == k.Machine && old.Address == k.Address {
		k.Wiring, k.AddedAt = old.Wiring, old.AddedAt // linked again (new password): keep what was learned
		if on {
			k.Wiring.LED = store.Wired
		}
	}
	if k.Password, err = h.Box.Seal(r.Password); err != nil {
		return KVMView{}, nil, fail(http.StatusInternalServerError, "seal", "Couldn't store the password safely: %v", err)
	}
	if err := h.Store.PutKVM(k); err != nil {
		if errors.Is(err, store.ErrTooManyKVMs) {
			return KVMView{}, nil, fail(http.StatusConflict, "too_many", "This device already drives 20 KVMs.")
		}
		return KVMView{}, nil, fail(http.StatusInternalServerError, "store", "Couldn't save the KVM: %v", err)
	}
	h.forget(k.Name)
	return View(k), &on, nil
}

// Patch changes a KVM: the machine it's wired to, its MAC, its password, or forgets the wiring.
type Patch struct {
	Machine  *string `json:"machine"`
	MAC      *string `json:"mac"`
	User     *string `json:"user"`
	Password *string `json:"password"`
	Recheck  bool    `json:"recheck"`
}

// Update applies a patch. A new user or password is tried first.
func (h *Hub) Update(ctx context.Context, name string, p Patch) (KVMView, error) {
	k, ok := h.Store.KVM(name)
	if !ok {
		return KVMView{}, fail(http.StatusNotFound, "unknown_kvm", "No KVM called %s.", name)
	}
	if p.User != nil || p.Password != nil {
		pw := ""
		if p.Password != nil {
			pw = *p.Password
		} else {
			var err error
			if pw, err = h.Box.Open(k.Password); err != nil {
				return KVMView{}, fail(http.StatusConflict, "kvm_credentials", "Give the password again: the stored one can't be opened.")
			}
		}
		if p.User != nil {
			k.User = *p.User
		}
		if _, err := h.dial(k, pw).PowerLED(ctx); err != nil {
			return KVMView{}, kvmError(k.Name, err)
		}
		sealed, err := h.Box.Seal(pw)
		if err != nil {
			return KVMView{}, fail(http.StatusInternalServerError, "seal", "Couldn't store the password safely: %v", err)
		}
		k.Password = sealed
	}
	if p.Machine != nil {
		m := strings.TrimSpace(*p.Machine)
		if m == "" || fleet.IsSelf(m, h.Self) {
			return KVMView{}, fail(http.StatusBadRequest, "bad_request", "Pick another machine.")
		}
		if pr, err := h.Fleet.Peer(m); err == nil {
			m = pr.Name
		}
		if other, ok := h.Store.KVMFor(m); ok && !strings.EqualFold(other.Name, k.Name) {
			return KVMView{}, fail(http.StatusConflict, "machine_has_kvm", "%s is already wired to %s.", m, other.Name)
		}
		if !strings.EqualFold(m, k.Machine) {
			k.Wiring = store.Wiring{}.Fresh() // another machine: nothing learned applies
		}
		k.Machine = m
	}
	if p.MAC != nil {
		mac := ""
		if *p.MAC != "" {
			var err error
			if mac, err = normalMAC(*p.MAC); err != nil {
				return KVMView{}, fail(http.StatusBadRequest, "bad_request", "%v", err)
			}
		}
		k.MAC = mac
	}
	if p.Recheck {
		k.Wiring = store.Wiring{}.Fresh()
	}
	if err := h.Store.UpdateKVM(k.Name, func(s *store.KVM) { *s = k }); err != nil {
		return KVMView{}, fail(http.StatusInternalServerError, "store", "Couldn't save the KVM: %v", err)
	}
	h.forget(k.Name)
	return View(k), nil
}

// Remove forgets a KVM.
func (h *Hub) Remove(name string) error {
	if err := h.Store.RemoveKVM(name); err != nil {
		return fail(http.StatusNotFound, "unknown_kvm", "No KVM called %s.", name)
	}
	h.forget(name)
	return nil
}

func (h *Hub) forget(name string) {
	h.mu.Lock()
	delete(h.clients, name)
	h.mu.Unlock()
}

func normalMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("%q isn't a MAC address", s)
	}
	return hw.String(), nil
}

// kvmError explains a KVM failure.
func kvmError(name string, err error) *Error {
	var ae *nanokvm.APIError
	switch {
	case errors.Is(err, nanokvm.ErrBadPassword):
		return fail(http.StatusUnprocessableEntity, "kvm_password", "%s rejected the user name or password.", name)
	case errors.Is(err, nanokvm.ErrLocked):
		return fail(http.StatusLocked, "kvm_locked", "%s locked logins after failed attempts. Try again in a few minutes.", name)
	case errors.Is(err, nanokvm.ErrForbidden):
		return fail(http.StatusForbidden, "kvm_forbidden", "%s's account isn't allowed to use the power controls. Give it the power permission.", name)
	case errors.Is(err, nanokvm.ErrCertChanged):
		return fail(http.StatusBadGateway, "kvm_cert_changed", "%s's certificate changed. If it was reset, link it again.", name)
	case errors.Is(err, nanokvm.ErrUnreachable), errors.Is(err, context.DeadlineExceeded):
		return fail(http.StatusBadGateway, "kvm_unreachable", "Can't reach %s. Is it on the tailnet?", name)
	case errors.As(err, &ae):
		return fail(http.StatusBadGateway, "kvm_failed", "%s: %s", name, ae.Msg)
	default:
		return fail(http.StatusBadGateway, "kvm_failed", "%s: %v", name, err)
	}
}

// ---- machines ----

// Machine is one machine the hub knows: a helper, a machine behind a KVM, or both.
type Machine struct {
	Name          string   `json:"name"`
	Helper        bool     `json:"helper"`
	OS            string   `json:"os,omitempty"`
	MAC           string   `json:"mac,omitempty"`
	LAN           []string `json:"lan,omitempty"`
	KVM           *KVMView `json:"kvm,omitempty"`
	HelperAnswers *bool    `json:"helperAnswers,omitempty"`
	LED           *bool    `json:"led,omitempty"` // the power light now; absent without a KVM or reading
	LEDError      string   `json:"ledError,omitempty"`
	// State: on (helper answers), off (wired LED reads off), hung (see Track.Step), unknown (not
	// enough evidence, Reason says why).
	State   string   `json:"state"`
	Reason  string   `json:"reason,omitempty"`
	Asleep  bool     `json:"asleep,omitempty"`
	Actions []string `json:"actions"` // what the hub can do for it now
	// Since is when the hub's watch first saw the current state; Evidence is its last look.
	Since    *time.Time `json:"since,omitempty"`
	Evidence *Evidence  `json:"evidence,omitempty"`
	// Recovery is its open recovery, or the last one if it ended in the last 30 minutes.
	Recovery *Recovery `json:"recovery,omitempty"`
}

// HasKVM reports whether a machine has a KVM here.
func (h *Hub) HasKVM(machine string) bool {
	if h == nil {
		return false
	}
	_, ok := h.Store.KVMFor(machine)
	return ok
}

// List shows every machine with live state. Peers without a KVM are listed by their helper alone.
func (h *Hub) List(ctx context.Context) []Machine { return h.list(ctx, false) }

// KVMMachines is List for the machines with a KVM here only (no probing of the rest).
func (h *Hub) KVMMachines(ctx context.Context) []Machine { return h.list(ctx, true) }

func (h *Hub) list(ctx context.Context, kvmOnly bool) []Machine {
	kvms := h.Store.KVMs()
	byMachine := map[string]store.KVM{}
	for _, k := range kvms {
		byMachine[strings.ToLower(k.Machine)] = k
	}
	var out []Machine
	for _, p := range h.Store.Peers() {
		m := Machine{Name: p.Name, Helper: true, OS: p.OS, MAC: p.MAC, LAN: p.LAN}
		if k, ok := byMachine[strings.ToLower(p.Name)]; ok {
			v := View(k)
			m.KVM = &v
			delete(byMachine, strings.ToLower(p.Name))
		} else if kvmOnly {
			continue
		}
		out = append(out, m)
	}
	for _, k := range kvms {
		if _, ok := byMachine[strings.ToLower(k.Machine)]; !ok {
			continue
		}
		v := View(k)
		out = append(out, Machine{Name: k.Machine, MAC: k.MAC, KVM: &v})
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(m *Machine) {
			defer wg.Done()
			h.look(ctx, m)
		}(&out[i])
	}
	wg.Wait()
	for i := range out {
		if out[i].KVM != nil {
			out[i].Recovery = h.latest(out[i].Name)
		}
	}
	return out
}

// Get is one machine with live state.
func (h *Hub) Get(ctx context.Context, name string) (Machine, bool) {
	for _, m := range h.List(ctx) {
		if strings.EqualFold(m.Name, name) || m.KVM != nil && strings.EqualFold(m.KVM.Name, name) {
			return m, true
		}
	}
	return Machine{}, false
}

// look reads the helper and the KVM, learns from the pair, and sets the state and actions.
func (h *Hub) look(ctx context.Context, m *Machine) {
	var answers bool
	var wg sync.WaitGroup
	if m.Helper {
		wg.Add(1)
		go func() { defer wg.Done(); answers = h.Fleet.Answers(ctx, m.Name) }()
	}
	var led *bool
	var ledErr error
	if m.KVM != nil {
		if k, ok := h.Store.KVM(m.KVM.Name); ok {
			led, ledErr = h.readLED(ctx, k)
		}
	}
	wg.Wait()
	if m.Helper {
		m.HelperAnswers = &answers
	}
	m.LED = led
	if ledErr != nil {
		m.LEDError = ledErr.Error()
	}
	if m.KVM != nil && led != nil {
		h.observe(m.KVM.Name, m.Helper && answers, *led)
		if k, ok := h.Store.KVM(m.KVM.Name); ok {
			v := View(k)
			m.KVM = &v
		}
	}
	trusted := m.KVM != nil && m.KVM.Wiring.LED == store.Wired
	switch {
	case m.Helper && answers:
		m.State = StateOn
	case trusted && led != nil && !*led:
		m.State = StateOff
	default:
		m.State, m.Reason = StateUnknown, ReasonNoHelper
		if m.Helper {
			m.Reason = ReasonLEDNotTrusted
			if trusted {
				m.Reason = ReasonChecking
			}
		}
	}
	// The watch has the history: hung, or why not. What was just seen decides on and off.
	if tr, ok := h.Tracked(m.Name); ok && m.KVM != nil {
		if m.State == StateUnknown {
			m.State, m.Reason = tr.State, tr.Reason
			if m.State == StateOn || m.State == StateOff { // it changed since the last look
				m.State, m.Reason = StateUnknown, ReasonChecking
			}
		}
		if m.State == tr.State {
			since := tr.Since
			m.Since, m.Asleep = &since, tr.Asleep
		}
		ev := tr.Evidence
		m.Evidence = &ev
	}
	m.Actions = []string{"wake"}
	if m.Helper {
		m.Actions = append(m.Actions, "shutdown", "restart")
	}
	if trusted && led != nil && *led {
		m.Actions = append(m.Actions, "reset", "force-off")
	}
}

func (h *Hub) readLED(ctx context.Context, k store.KVM) (*bool, error) {
	c, err := h.client(k)
	if err != nil {
		return nil, err
	}
	on, err := c.PowerLED(ctx)
	if err != nil {
		return nil, kvmError(k.Name, err)
	}
	return &on, nil
}

// observe learns the LED wiring: a reading of "on" proves it's wired (an unwired or reversed LED
// never lights); "off" while the helper answers, three times at least 30 s apart, means it isn't.
func (h *Hub) observe(name string, helperUp, led bool) {
	now := h.now().UTC()
	_ = h.Store.UpdateKVM(name, func(k *store.KVM) {
		w := &k.Wiring
		switch {
		case led:
			if w.LED == store.Wired && w.LEDOffWhileUp == 0 {
				return // nothing new
			}
			if w.LED != store.Wired {
				h.log().Info("KVM power light is wired", "kvm", k.Name)
			}
			w.LED, w.LEDOffWhileUp = store.Wired, 0
		case helperUp:
			if !w.CheckedAt.IsZero() && now.Sub(w.CheckedAt) < 30*time.Second && w.LEDOffWhileUp > 0 {
				return
			}
			w.LEDOffWhileUp++
			if w.LEDOffWhileUp >= 3 && w.LED != store.NotWired {
				w.LED = store.NotWired
				h.log().Warn("KVM power light reads off while the machine answers: not wired, or wired backwards", "kvm", k.Name)
			}
		default:
			return
		}
		w.CheckedAt = now
	})
}

// ---- actions ----

// Step is one thing an action did, for the reply (and the audit log in phase 4).
type Step struct {
	At    time.Time `json:"at"`
	Do    string    `json:"do"`
	LED   *bool     `json:"led,omitempty"`
	Error string    `json:"error,omitempty"`
}

// Result is an action's outcome.
type Result struct {
	Machine string         `json:"machine"`
	Action  string         `json:"action"`
	OK      bool           `json:"ok"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message"`
	Steps   []Step         `json:"steps"`
	Helper  map[string]any `json:"helper,omitempty"` // the machine's helper's reply (shutdown, restart)
	Status  int            `json:"-"`                // HTTP status when not OK

	verifying bool // Options.Back will be called
}

// Options change how an action runs.
type Options struct {
	Who     string // shown on the machine and in events
	Force   bool   // reset or force off even though the helper answers
	StayOff bool   // force off without powering on again
	Press   bool   // shutdown: press the power button (the OS shuts down cleanly) if the helper doesn't answer
	Agent   bool   // on behalf of an AI agent (relayed to the machine's helper)
	// Back, when set, is called once the hub knows whether the machine answered again after a reset
	// or an off-and-on (within ResetVerify). Not called when that can't be checked: no helper, or it
	// answered all along.
	Back func(back bool)
}

type run struct {
	h   *Hub
	ctx context.Context
	res *Result
	k   store.KVM
	kvm KVM
}

func (r *run) step(do string, led *bool, err error) {
	s := Step{At: r.h.now().UTC(), Do: do, LED: led}
	if err != nil {
		s.Error = err.Error()
	}
	r.res.Steps = append(r.res.Steps, s)
}

func (r *run) done(ok bool, code, format string, a ...any) Result {
	r.res.OK, r.res.Code, r.res.Message = ok, code, fmt.Sprintf(format, a...)
	if !ok && r.res.Status == 0 {
		r.res.Status = http.StatusConflict
	}
	return *r.res
}

func (r *run) refuse(status int, code, format string, a ...any) Result {
	r.res.Status = status
	return r.done(false, code, format, a...)
}

// led reads the power light and records it.
func (r *run) led(what string) *bool {
	on, err := r.kvm.PowerLED(r.ctx)
	if err != nil {
		r.step("read power light "+what, nil, err)
		return nil
	}
	r.step("read power light "+what, &on, nil)
	return &on
}

func (r *run) press(button string, d time.Duration) error {
	err := r.kvm.Press(r.ctx, button, d)
	r.step(fmt.Sprintf("press %s %s", button, d), nil, err)
	return err
}

// waitLED reads the light until it shows want or the settle time runs out. seen is false when the
// last reading failed or the action ran out of time: then nothing can be learned from it.
func (r *run) waitLED(want bool) (ok, seen bool) {
	deadline := r.h.now().Add(dur(r.h.Settle, 15*time.Second))
	for {
		on, err := r.kvm.PowerLED(r.ctx)
		if err == nil && on == want {
			r.step("read power light after", &on, nil)
			return true, true
		}
		if r.h.now().After(deadline) || r.ctx.Err() != nil {
			if err != nil {
				r.step("read power light after", nil, err)
				return false, false
			}
			r.step("read power light after", &on, nil)
			return false, r.ctx.Err() == nil
		}
		select {
		case <-r.ctx.Done():
		case <-time.After(dur(r.h.Poll, time.Second)):
		}
	}
}

func (r *run) learn(f func(w *store.Wiring)) {
	_ = r.h.Store.UpdateKVM(r.k.Name, func(k *store.KVM) { f(&k.Wiring) })
	if k, ok := r.h.Store.KVM(r.k.Name); ok {
		r.k = k
	}
}

// Actions the hub runs.
var Actions = []string{"wake", "shutdown", "restart", "reset", "force-off"}

// actionTime bounds one action. A press sequence runs to its end even when the caller hangs up, so
// a machine held off is also started again.
const actionTime = 2 * time.Minute

// Act runs one action on a machine with a KVM here. One action per machine at a time. The caller
// checks permissions (internal/server's act). A reset or force off is recorded as a recovery that
// its caller confirmed.
func (h *Hub) Act(ctx context.Context, name, action string, o Options) Result {
	if action == "reset" || action == "force-off" {
		return h.direct(ctx, name, action, o)
	}
	return h.act(ctx, name, action, o)
}

func (h *Hub) act(ctx context.Context, name, action string, o Options) Result {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), actionTime)
	defer cancel()
	res := &Result{Machine: name, Action: action, Steps: []Step{}}
	r := &run{h: h, ctx: ctx, res: res}
	k, ok := h.Store.KVMFor(name)
	if !ok {
		return r.refuse(http.StatusNotFound, "no_kvm", "No KVM here is wired to %s.", name)
	}
	r.k = k
	res.Machine = k.Machine
	key := strings.ToLower(k.Machine)
	h.mu.Lock()
	if h.busy == nil {
		h.busy = map[string]bool{}
	}
	if h.busy[key] {
		h.mu.Unlock()
		return r.refuse(http.StatusConflict, "busy", "Already working on %s.", k.Machine)
	}
	h.busy[key] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.busy, key)
		h.mu.Unlock()
	}()
	_, helperErr := h.Fleet.Peer(k.Machine)
	hasHelper := helperErr == nil
	c, err := h.client(k)
	if err != nil {
		var e *Error
		errors.As(err, &e)
		if !hasHelper || action == "reset" || action == "force-off" {
			return r.refuse(e.Status, e.Code, "%s", e.Message)
		}
		c = unusable{e} // the helper can still wake, shut down or restart it
	}
	r.kvm = c
	var out Result
	switch action {
	case "wake":
		out = r.wake(hasHelper)
	case "shutdown", "restart":
		out = r.powerOff(hasHelper, action, o)
	case "reset":
		out = r.reset(hasHelper, o)
	case "force-off":
		out = r.forceOff(hasHelper, o)
	default:
		return r.refuse(http.StatusNotFound, "not_found", "Unknown action %s.", action)
	}
	if out.OK && action != "shutdown" && out.Code == "" {
		h.acted(k.Machine) // it's starting: give it time to boot before calling it hung
	}
	return out
}

func (r *run) wake(hasHelper bool) Result {
	m := r.k.Machine
	if hasHelper && r.h.Fleet.Answers(r.ctx, m) {
		r.step("helper answers", nil, nil)
		return r.done(true, "already_on", "%s is already on.", m)
	}
	led := r.led("before")
	if led != nil && *led {
		r.ledWired()
		return r.done(true, "led_on", "%s's power light is on: it's on or still starting.", m)
	}
	trusted := r.k.Wiring.LED == store.Wired
	switch {
	case led != nil && !*led && trusted && r.k.Wiring.Power != store.NotWired:
		if err := r.press(nanokvm.Power, 800*time.Millisecond); err == nil {
			ok, seen := r.waitLED(true)
			if ok {
				r.learn(func(w *store.Wiring) { w.Power = store.Wired })
				return r.done(true, "", "%s is starting.", m)
			}
			if seen {
				r.learn(func(w *store.Wiring) { w.Power = store.NotWired })
				r.h.log().Warn("power press didn't turn the machine on: power switch not wired?", "kvm", r.k.Name)
			}
		}
	case led != nil && !*led && !trusted:
		r.step("no press: the power light hasn't been seen on yet, so off can't be told from not wired", nil, nil)
	case led == nil:
		r.step("no press: the power light can't be read", nil, nil)
	}
	// Wake-on-LAN: from this helper when it's on the machine's LAN, else from the KVM's own port.
	if hasHelper {
		sent, err := r.h.Fleet.Wake(m)
		if err == nil {
			r.step("Wake-on-LAN from "+r.h.Self, nil, nil)
			return r.done(true, "", "Wake signal sent to %s (%s). It usually takes under a minute.", m, strings.Join(sent, ", "))
		}
		r.step("Wake-on-LAN from "+r.h.Self, nil, err)
	}
	mac := r.k.MAC
	if p, err := r.h.Fleet.Peer(m); err == nil && p.MAC != "" {
		mac = p.MAC
	}
	if mac == "" {
		return r.refuse(http.StatusUnprocessableEntity, "cant_wake", "%s's network card isn't known, and its power light can't be trusted for a press yet.", m)
	}
	err := r.kvm.Wake(r.ctx, mac)
	r.step("Wake-on-LAN from "+r.k.Name, nil, err)
	if err != nil {
		return r.refuse(http.StatusBadGateway, "cant_wake", "Couldn't send the wake signal: %v", kvmError(r.k.Name, err))
	}
	return r.done(true, "", "Wake signal sent by %s. %s usually takes under a minute.", r.k.Name, m)
}

func (r *run) powerOff(hasHelper bool, action string, o Options) Result {
	m := r.k.Machine
	led := r.led("before")
	if !hasHelper || o.Press {
		switch {
		case action == "restart":
			return r.refuse(http.StatusUnprocessableEntity, "no_helper", "%s can only be restarted through its PowerDeck helper.", m)
		case hasHelper && r.h.Fleet.Answers(r.ctx, m):
			r.step("helper answers: asking it instead of pressing", nil, nil) // so its in-use check applies
		default:
			return r.pressOff(led, o)
		}
	}
	ctx := r.ctx
	if o.Agent {
		ctx = peer.ForAgent(ctx)
	}
	var reply map[string]any
	err := r.h.Fleet.Forward(ctx, m, "POST", "/v2/power/"+action, map[string]any{"delaySeconds": 60}, &reply, o.Who)
	r.step("ask the helper to "+action, nil, err)
	r.led("after")
	if err != nil {
		var pe *peer.Error
		switch {
		case errors.Is(err, fleet.ErrOffline):
			return r.refuse(http.StatusBadGateway, "helper_down", "%s's helper isn't answering. If %s is frozen, reset it.", m, m)
		case errors.As(err, &pe):
			code := pe.Code
			status := pe.Status
			if code == "in_use" || code == "pending" {
				status = http.StatusConflict
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden && code != "agents_off" {
				status, code = http.StatusBadGateway, "peer_refused"
			}
			r.res.Helper = map[string]any{"error": pe.Code, "message": pe.Message}
			if len(pe.Body) > 0 {
				r.res.Helper["body"] = pe.Body
			}
			return r.refuse(status, code, "%s: %s", m, pe.Message)
		default:
			return r.refuse(http.StatusBadGateway, "relay_failed", "%s: %v", m, err)
		}
	}
	r.res.Helper = reply
	verb := "shuts down"
	if action == "restart" {
		verb = "restarts"
	}
	return r.done(true, "", "%s %s in 60 seconds. It can be cancelled until then.", m, verb)
}

// pressOff is a short power press on a machine that's on: the OS shuts down cleanly (Proxmox stops
// its VMs first). Nothing checks whether someone is using it, so only the owner may.
func (r *run) pressOff(led *bool, o Options) Result {
	m := r.k.Machine
	if o.Agent {
		return r.refuse(http.StatusForbidden, "owner_only", "Without its helper nobody can check whether someone is using %s, so only the owner can press its power button.", m)
	}
	switch {
	case led == nil:
		return r.refuse(http.StatusBadGateway, "led_unreadable", "Can't read %s's power light, so PowerDeck won't press its button.", m)
	case !*led && r.k.Wiring.LED == store.Wired:
		return r.done(true, "already_off", "%s is already off.", m)
	case !*led:
		return r.refuse(http.StatusConflict, "led_untrusted", "%s's power light reads off, but it hasn't been seen on yet, so off can't be told from not wired. Turn %s on by hand once.", m, m)
	}
	r.ledWired()
	if r.k.Wiring.Power == store.NotWired {
		return r.refuse(http.StatusUnprocessableEntity, "power_not_wired", "%s's power button doesn't reach %s.", r.k.Name, m)
	}
	if err := r.press(nanokvm.Power, 800*time.Millisecond); err != nil {
		return r.refuse(http.StatusBadGateway, "kvm_failed", "%v", kvmError(r.k.Name, err))
	}
	r.led("after")
	return r.done(true, "", "Pressed %s's power button. It shuts down cleanly, which can take a few minutes.", m)
}

// gate checks what reset and force off need: a trusted light that reads on, and a helper that
// doesn't answer (unless forced). answers: the helper answered (forced).
func (r *run) gate(hasHelper bool, o Options, what string) (answers bool, res *Result) {
	m := r.k.Machine
	if hasHelper && r.h.Fleet.Answers(r.ctx, m) {
		r.step("helper answers", nil, nil)
		if !o.Force {
			res := r.refuse(http.StatusConflict, "helper_answers", "%s is answering, so it isn't frozen. Shut it down or restart it instead: %s loses unsaved work.", m, what)
			return true, &res
		}
		answers = true
	}
	led := r.led("before")
	switch {
	case led == nil:
		res := r.refuse(http.StatusBadGateway, "led_unreadable", "Can't read %s's power light, so nothing is pressed.", m)
		return answers, &res
	case *led:
		r.ledWired()
		return answers, nil
	case r.k.Wiring.LED != store.Wired:
		res := r.refuse(http.StatusUnprocessableEntity, "led_untrusted", "%s's power light reads off and hasn't been seen on yet, so PowerDeck can't tell off from not wired. Check the LED cable on the KVM's ATX board.", m)
		return answers, &res
	default:
		res := r.refuse(http.StatusConflict, "already_off", "%s's power light is off: it's already off. Wake it instead.", m)
		return answers, &res
	}
}

// ledWired records that the light was seen on: an unwired or reversed LED never lights.
func (r *run) ledWired() {
	if r.k.Wiring.LED != store.Wired {
		r.learn(func(w *store.Wiring) { w.LED, w.LEDOffWhileUp = store.Wired, 0 })
	}
}

func (r *run) reset(hasHelper bool, o Options) Result {
	m := r.k.Machine
	answered, res := r.gate(hasHelper, o, "a reset")
	if res != nil {
		return *res
	}
	if r.k.Wiring.Reset == store.NotWired && !o.Force {
		return r.refuse(http.StatusUnprocessableEntity, "reset_not_wired", "The last reset had no effect on %s (reset switch not wired?). Use force off instead.", m)
	}
	if err := r.press(nanokvm.Reset, 800*time.Millisecond); err != nil {
		return r.refuse(http.StatusBadGateway, "kvm_failed", "The reset press failed: %v", kvmError(r.k.Name, err))
	}
	r.led("after")              // stays on through a reset on most boards: the result shows when the helper is back
	if hasHelper && !answered { // a machine that answered all along can't show whether the reset worked
		r.verifyBack(true, o.Back)
		return r.done(true, "", "Pressed reset on %s. It should answer again in a few minutes.", m)
	}
	return r.done(true, "", "Pressed reset on %s.", m)
}

// verifyBack waits in the background for the machine's helper to answer again, then calls back (if
// set). After a reset it also records whether the reset switch works.
func (r *run) verifyBack(reset bool, back func(bool)) {
	h, kvm, machine := r.h, r.k.Name, r.k.Machine
	if !reset && back == nil {
		return // nothing to learn and nobody waiting
	}
	r.res.verifying = back != nil
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		up := false
		every := dur(h.VerifyEvery, 10*time.Second)
		for i := time.Duration(0); !up && i < dur(h.ResetVerify, 5*time.Minute)/every; i++ {
			time.Sleep(every)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			up = h.Fleet.Answers(ctx, machine)
			cancel()
		}
		if up {
			h.log().Info("the machine answers again", "machine", machine)
		} else {
			h.log().Warn("the machine didn't answer in time", "machine", machine)
		}
		if reset {
			_ = h.Store.UpdateKVM(kvm, func(k *store.KVM) {
				if up {
					k.Wiring.Reset = store.Wired
				} else if k.Wiring.Reset != store.Wired {
					k.Wiring.Reset = store.NotWired
				}
			})
		}
		if back != nil {
			back(up)
		}
	}()
}

// unusable stands in for a KVM whose stored password can't be opened: every call fails with why.
type unusable struct{ err error }

func (u unusable) PowerLED(context.Context) (bool, error)             { return false, u.err }
func (u unusable) Press(context.Context, string, time.Duration) error { return u.err }
func (u unusable) Wake(context.Context, string) error                 { return u.err }

// Wait waits for background checks (tests).
func (h *Hub) Wait() { h.wg.Wait() }

func (r *run) forceOff(hasHelper bool, o Options) Result {
	m := r.k.Machine
	answered, res := r.gate(hasHelper, o, "forcing it off")
	if res != nil {
		return *res
	}
	if err := r.press(nanokvm.Power, 12*time.Second); err != nil {
		return r.refuse(http.StatusBadGateway, "kvm_failed", "The power hold failed: %v", kvmError(r.k.Name, err))
	}
	if ok, seen := r.waitLED(false); !ok {
		if !seen {
			return r.refuse(http.StatusBadGateway, "led_unreadable", "Held %s's power button for 12 s, but its power light can't be read now. Check it before pressing again.", m)
		}
		r.learn(func(w *store.Wiring) { w.Power = store.NotWired })
		return r.refuse(http.StatusBadGateway, "no_effect", "Held %s's power button for 12 s, but its power light stayed on. Is the power switch wired to the KVM?", m)
	}
	r.learn(func(w *store.Wiring) { w.Power = store.Wired })
	if o.StayOff {
		return r.done(true, "", "%s is off.", m)
	}
	select {
	case <-r.ctx.Done():
		return r.refuse(http.StatusGatewayTimeout, "cancelled", "%s is off; turning it on again was cancelled.", m)
	case <-time.After(dur(r.h.OffWait, 5*time.Second)):
	}
	if err := r.press(nanokvm.Power, 800*time.Millisecond); err != nil {
		return r.refuse(http.StatusBadGateway, "kvm_failed", "%s is off, but the power press to start it failed: %v", m, kvmError(r.k.Name, err))
	}
	if ok, _ := r.waitLED(true); !ok {
		return r.refuse(http.StatusBadGateway, "didnt_start", "%s is off, but didn't start again. Wake it, or press its power button.", m)
	}
	if hasHelper && !answered {
		r.verifyBack(false, o.Back)
	}
	return r.done(true, "", "Switched %s off and on again. It's starting.", m)
}
