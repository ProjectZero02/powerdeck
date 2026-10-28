// Package fakekvm is a NanoKVM stand-in for tests and app development: the firmware's API routes
// and replies (2.4, 2.5 and Pro login styles), and a simulated computer behind its ATX board.
//
// The computer: a short power press starts it when off and shuts it down cleanly when on (a hung
// one ignores it); holding power for 4 s or more cuts it off; reset reboots it, and the power light
// stays on through a reset as on real boards. Delays and press lengths are multiplied by
// Options.TimeScale, so tests can hold "12 s" in 12 ms.
package fakekvm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/nanokvm"
	"github.com/projectzero02/powerdeck/helper/internal/tlscert"
)

// Login styles.
const (
	Style25  = "2.5" // session only in the HttpOnly cookie
	Style24  = "2.4" // token in the reply body as well
	StylePro = "pro" // plain password
)

// Options shape the fake.
type Options struct {
	User, Password string // default admin/admin
	Style          string // default Style25
	LockAfter      int    // failed logins before a lockout (default 5)
	LockFor        time.Duration
	HTTPS          bool          // plain HTTP redirects to a self-signed HTTPS port
	Forbidden      bool          // the account lacks the power permission: gpio and wol answer 403
	NoLED          bool          // power LED not wired (or reversed): always reads off
	LEDError       bool          // reading the LED fails, as the firmware reports it (-2)
	NoPowerSwitch  bool          // power button not wired: presses do nothing
	NoResetSwitch  bool          // reset button not wired
	On             bool          // the computer starts on
	BootDelay      time.Duration // default 30 s (scaled)
	ShutdownDelay  time.Duration // default 20 s (scaled)
	TimeScale      float64       // default 1
	// SleepLED is what the power light does while the computer sleeps: "off" (default),
	// "blinking" (on and off on alternate readings) or "on" (Modern Standby on many boards).
	SleepLED string
}

// Press is one button press the KVM received.
type Press struct {
	Button   string
	Duration time.Duration // as requested (unscaled)
}

// Fake is a running fake NanoKVM.
type Fake struct {
	URL     string // plain HTTP base URL
	TLSURL  string // HTTPS base URL (HTTPS mode)
	TLSPin  string
	opt     Options
	plain   *httptest.Server
	tls     *httptest.Server
	mu      sync.Mutex
	on      bool
	hung    bool
	asleep  bool
	blink   bool // the next reading while asleep and blinking
	up      bool // the OS is up (the helper would answer)
	gen     int  // cancels pending boot/shutdown timers
	reboots int
	presses []Press
	wakes   []string
	logins  int
	fails   int
	locked  time.Time
	tokens  map[string]bool
	down    bool
}

// Start runs a fake on 127.0.0.1.
func Start(opt Options) *Fake {
	if opt.User == "" {
		opt.User = "admin"
	}
	if opt.Password == "" {
		opt.Password = "admin"
	}
	if opt.Style == "" {
		opt.Style = Style25
	}
	if opt.LockAfter == 0 {
		opt.LockAfter = 5
	}
	if opt.LockFor == 0 {
		opt.LockFor = 10 * time.Minute
	}
	if opt.BootDelay == 0 {
		opt.BootDelay = 30 * time.Second
	}
	if opt.ShutdownDelay == 0 {
		opt.ShutdownDelay = 20 * time.Second
	}
	if opt.TimeScale == 0 {
		opt.TimeScale = 1
	}
	f := &Fake{opt: opt, on: opt.On, up: opt.On, tokens: map[string]bool{}}
	api := http.NewServeMux()
	api.HandleFunc("POST /api/auth/login", f.login)
	api.HandleFunc("GET /api/vm/gpio", f.auth(f.getGPIO))
	api.HandleFunc("POST /api/vm/gpio", f.auth(f.setGPIO))
	api.HandleFunc("POST /api/network/wol", f.auth(f.wol))
	api.HandleFunc("GET /api/vm/info", f.auth(f.info))
	api.HandleFunc("GET /api/vm/hardware", f.auth(f.hardware))
	api.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>NanoKVM</title>"))
	})
	h := f.reachable(api)
	if opt.HTTPS {
		f.tls = httptest.NewUnstartedServer(h)
		f.tls.StartTLS()
		f.TLSURL = f.tls.URL
		f.TLSPin = tlscert.Pin(f.tls.Certificate().Raw)
		port := f.tls.URL[strings.LastIndex(f.tls.URL, ":")+1:]
		f.plain = httptest.NewServer(f.reachable(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, _ := net.SplitHostPort(r.Host)
			http.Redirect(w, r, "https://"+net.JoinHostPort(host, port)+r.URL.RequestURI(), http.StatusTemporaryRedirect)
		})))
	} else {
		f.plain = httptest.NewServer(h)
	}
	f.URL = f.plain.URL
	return f
}

// Close stops the fake.
func (f *Fake) Close() {
	f.plain.Close()
	if f.tls != nil {
		f.tls.Close()
	}
	f.mu.Lock()
	f.gen++
	f.mu.Unlock()
}

func (f *Fake) scale(d time.Duration) time.Duration {
	return time.Duration(float64(d) * f.opt.TimeScale)
}

// ---- the simulated computer ----

// SetOn switches the computer on (up) or off at once, as if by hand.
func (f *Fake) SetOn(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gen++
	f.on, f.up, f.hung, f.asleep = on, on, false, false
}

// Sleep puts the computer to sleep: nothing answers, the light does Options.SleepLED, and a short
// power press wakes it.
func (f *Fake) Sleep() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.on && !f.hung {
		f.gen++
		f.asleep, f.up = true, false
	}
}

// Hang freezes the OS: the power light stays on, nothing answers, short presses are ignored.
func (f *Fake) Hang() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.on {
		f.hung, f.up = true, false
	}
}

// Up reports whether the OS is running (its helper would answer).
func (f *Fake) Up() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.up }

// On reports whether the computer has power.
func (f *Fake) On() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.on }

// Reboots counts resets that rebooted the computer.
func (f *Fake) Reboots() int { f.mu.Lock(); defer f.mu.Unlock(); return f.reboots }

// Presses lists the button presses received.
func (f *Fake) Presses() []Press {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Press(nil), f.presses...)
}

// Wakes lists the MACs Wake-on-LAN was sent to.
func (f *Fake) Wakes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.wakes...)
}

// Logins counts successful logins.
func (f *Fake) Logins() int { f.mu.Lock(); defer f.mu.Unlock(); return f.logins }

// SetUnreachable drops every connection (KVM unplugged, or off the tailnet).
func (f *Fake) SetUnreachable(down bool) { f.mu.Lock(); f.down = down; f.mu.Unlock() }

// ExpireSessions logs everyone out (the KVM restarted, or the password changed).
func (f *Fake) ExpireSessions() { f.mu.Lock(); f.tokens = map[string]bool{}; f.mu.Unlock() }

// boot schedules the OS coming up.
func (f *Fake) bootLocked() {
	f.gen++
	g := f.gen
	f.up = false
	time.AfterFunc(f.scale(f.opt.BootDelay), func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.gen == g && f.on {
			f.up = true
		}
	})
}

func (f *Fake) pressed(button string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presses = append(f.presses, Press{button, d})
	switch {
	case button == nanokvm.Reset:
		if f.opt.NoResetSwitch || !f.on {
			return
		}
		f.hung, f.asleep = false, false
		f.reboots++
		f.bootLocked()
	case f.opt.NoPowerSwitch:
	case d >= 4*time.Second: // ATX: holding power cuts it
		f.gen++
		f.on, f.up, f.hung, f.asleep = false, false, false, false
	case !f.on:
		f.on = true
		f.bootLocked()
	case f.asleep:
		f.asleep, f.up = false, true
	case f.hung:
		// A frozen OS doesn't handle the power-button event.
	default:
		f.gen++
		g := f.gen
		time.AfterFunc(f.scale(f.opt.ShutdownDelay), func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.gen == g {
				f.on, f.up = false, false
			}
		})
	}
}

// ---- HTTP ----

type reply struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func send(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply{code, msg, data})
}

func (f *Fake) reachable(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		down := f.down
		f.mu.Unlock()
		if down {
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
					return
				}
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (f *Fake) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("nano-kvm-token")
		f.mu.Lock()
		ok := err == nil && f.tokens[c.Value]
		f.mu.Unlock()
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`"unauthorized"`))
			return
		}
		h(w, r)
	}
}

func (f *Fake) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		send(w, -1, "invalid parameters", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Now().Before(f.locked) {
		send(w, -5, "Account locked due to too many failed attempts, please try again later", nil)
		return
	}
	pw := req.Password
	if f.opt.Style != StylePro {
		var err error
		if pw, err = nanokvm.DecryptPassword(req.Password); err != nil {
			pw = "\x00" // the firmware treats an undecryptable password as wrong
		}
	}
	if req.Username != f.opt.User || pw != f.opt.Password {
		f.fails++
		if f.fails >= f.opt.LockAfter {
			f.fails = 0
			f.locked = time.Now().Add(f.scale(f.opt.LockFor))
			send(w, -5, "Account locked due to too many failed attempts, please try again later", nil)
			return
		}
		send(w, -2, "invalid username or password", nil)
		return
	}
	f.fails = 0
	f.logins++
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	f.tokens[tok] = true
	if f.opt.Style == Style24 {
		send(w, 0, "success", map[string]string{"token": tok})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "nano-kvm-token", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	send(w, 0, "success", nil)
}

func (f *Fake) getGPIO(w http.ResponseWriter, _ *http.Request) {
	if f.opt.Forbidden {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.opt.LEDError {
		send(w, -2, "failed to read power led: read /sys/class/gpio/gpio504/value: no such file", nil)
		return
	}
	f.mu.Lock()
	pwr := f.on && !f.opt.NoLED
	if f.asleep && pwr {
		switch f.opt.SleepLED {
		case "on":
		case "blinking":
			f.blink = !f.blink
			pwr = f.blink
		default:
			pwr = false
		}
	}
	f.mu.Unlock()
	send(w, 0, "success", map[string]bool{"pwr": pwr, "hdd": false})
}

func (f *Fake) setGPIO(w http.ResponseWriter, r *http.Request) {
	if f.opt.Forbidden {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var req struct {
		Type     string
		Duration uint
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		send(w, -1, "invalid arguments", nil)
		return
	}
	if req.Type != nanokvm.Power && req.Type != nanokvm.Reset {
		send(w, -2, "invalid power event: "+req.Type, nil)
		return
	}
	d := time.Duration(req.Duration) * time.Millisecond
	if d == 0 {
		d = 800 * time.Millisecond
	}
	time.Sleep(f.scale(d)) // the firmware answers when the press ends
	f.pressed(req.Type, d)
	send(w, 0, "success", nil)
}

func (f *Fake) wol(w http.ResponseWriter, r *http.Request) {
	if f.opt.Forbidden {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var req struct{ Mac string }
	if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Mac) < 12 {
		send(w, -2, "invalid MAC address", nil)
		return
	}
	f.mu.Lock()
	f.wakes = append(f.wakes, strings.ToLower(req.Mac))
	f.mu.Unlock()
	send(w, 0, "success", nil)
}

func (f *Fake) info(w http.ResponseWriter, _ *http.Request) {
	app := "2.5.2"
	if f.opt.Style == Style24 {
		app = "2.4.3"
	}
	send(w, 0, "success", map[string]any{"ips": []any{}, "mdns": "nanokvm", "image": "v1.4.1", "application": app, "deviceKey": ""})
}

func (f *Fake) hardware(w http.ResponseWriter, _ *http.Request) {
	send(w, 0, "success", map[string]string{"version": "Beta"})
}
