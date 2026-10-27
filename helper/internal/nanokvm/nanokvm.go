// Package nanokvm talks to a Sipeed NanoKVM's web API (firmware 2.x): login, the power LED, the
// power and reset buttons on its ATX board, and Wake-on-LAN from its own network port.
//
// A NanoKVM speaks plain HTTP, or redirects to HTTPS with a self-signed certificate when HTTPS is
// turned on. PowerDeck reaches it by its Tailscale name, so WireGuard protects either way; on HTTPS
// the certificate is pinned the first time and a changed one is refused.
package nanokvm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/tlscert"
)

// Errors, phrased for people.
var (
	ErrUnreachable = errors.New("can't reach the NanoKVM")
	ErrBadPassword = errors.New("the NanoKVM rejected the user name or password")
	ErrLocked      = errors.New("the NanoKVM locked logins after failed attempts; try again in a few minutes")
	ErrForbidden   = errors.New("the NanoKVM account isn't allowed to do that (it needs the power permission)")
	ErrNoLED       = errors.New("the NanoKVM didn't report the power light")
	ErrCertChanged = errors.New("the NanoKVM's certificate changed; link it again if it was reset")
)

// APIError is a NanoKVM reply with a non-zero code.
type APIError struct {
	Path string
	Code int
	Msg  string
}

func (e *APIError) Error() string { return fmt.Sprintf("NanoKVM %s failed (%d): %s", e.Path, e.Code, e.Msg) }

// Buttons on the ATX board.
const (
	Power = "power"
	Reset = "reset"
)

// Client is one NanoKVM. Safe for concurrent use.
type Client struct {
	Address  string // host[:port], or a URL; plain http unless it says https
	User     string
	Password string
	Plain    bool   // NanoKVM Pro takes the password as is
	Pin      string // pinned HTTPS certificate ("" = not seen yet)
	// OnPin is called when an HTTPS certificate is seen for the first time, to be remembered.
	OnPin func(pin string)
	// Timeout for calls other than presses (default 8 s).
	Timeout time.Duration

	mu       sync.Mutex
	base     string // scheme://host[:port]
	cookie   string
	loggedIn bool
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

var errSession = errors.New("session expired")

func (c *Client) baseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.base == "" {
		a := strings.TrimRight(c.Address, "/")
		if !strings.HasPrefix(a, "http://") && !strings.HasPrefix(a, "https://") {
			a = "http://" + a
		}
		c.base = a
	}
	return c.base
}

func (c *Client) httpClient(timeout time.Duration) *http.Client {
	c.mu.Lock()
	pin := c.Pin
	c.mu.Unlock()
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // self-signed; verified by pin below
				VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
					if len(raw) == 0 {
						return errors.New("no certificate")
					}
					got := tlscert.Pin(raw[0])
					if pin != "" && got != pin {
						return ErrCertChanged
					}
					if pin == "" {
						c.mu.Lock()
						first := c.Pin == ""
						c.Pin = got
						c.mu.Unlock()
						if first && c.OnPin != nil {
							c.OnPin(got)
						}
					}
					return nil
				},
			},
		},
	}
}

// do sends one request and decodes the envelope. It follows one redirect to HTTPS on the same host.
func (c *Client) do(ctx context.Context, method, path string, body any, timeout time.Duration) (*envelope, http.Header, error) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, bytes.NewReader(payload))
		if err != nil {
			return nil, nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		c.mu.Lock()
		if c.cookie != "" {
			req.Header.Set("Cookie", "nano-kvm-token="+c.cookie)
		}
		c.mu.Unlock()
		resp, err := c.httpClient(timeout).Do(req)
		if err != nil {
			if errors.Is(err, ErrCertChanged) {
				return nil, nil, ErrCertChanged
			}
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			if attempt > 0 || !c.toHTTPS(resp.Header.Get("Location")) {
				return nil, nil, fmt.Errorf("%w: it redirects to %q", ErrUnreachable, resp.Header.Get("Location"))
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, nil, errSession
		case resp.StatusCode == http.StatusForbidden:
			return nil, nil, ErrForbidden
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, nil, fmt.Errorf("unexpected NanoKVM reply (HTTP %d)", resp.StatusCode)
		}
		return &env, resp.Header, nil
	}
}

// toHTTPS switches to HTTPS when loc points at the same host over https.
func (c *Client) toHTTPS(loc string) bool {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "https" {
		return false
	}
	cur, err := url.Parse(c.baseURL())
	if err != nil || !strings.EqualFold(u.Hostname(), cur.Hostname()) {
		return false
	}
	c.mu.Lock()
	c.base = "https://" + u.Host
	c.mu.Unlock()
	return true
}

// URL is where the KVM answers, after any switch to HTTPS. Store it so the next client starts there.
func (c *Client) URL() string { return c.baseURL() }

// Login starts a session. Calls log in on their own; this is for checking credentials.
func (c *Client) Login(ctx context.Context) error {
	pw := c.Password
	if !c.Plain {
		var err error
		if pw, err = encryptPassword(c.Password); err != nil {
			return err
		}
	}
	env, h, err := c.do(ctx, http.MethodPost, "/api/auth/login", map[string]string{"username": c.User, "password": pw}, c.timeout())
	if errors.Is(err, errSession) {
		return ErrBadPassword
	}
	if err != nil {
		return err
	}
	switch env.Code {
	case 0:
	case -2:
		return ErrBadPassword
	case -4, -5:
		return ErrLocked
	default:
		return &APIError{"/api/auth/login", env.Code, env.Msg}
	}
	// From 2.5 the token is only the HttpOnly cookie; up to 2.4.x it's in the body too.
	var data struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(env.Data, &data)
	token := data.Token
	for _, sc := range (&http.Response{Header: h}).Cookies() {
		if sc.Name == "nano-kvm-token" && sc.Value != "" {
			token = sc.Value
		}
	}
	c.mu.Lock()
	c.cookie, c.loggedIn = token, true // no token: authentication is turned off on the KVM
	c.mu.Unlock()
	return nil
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 8 * time.Second
}

// call logs in if needed, and once more if the session expired.
func (c *Client) call(ctx context.Context, method, path string, body, out any, timeout time.Duration) error {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		in := c.loggedIn
		c.mu.Unlock()
		if !in {
			if err := c.Login(ctx); err != nil {
				return err
			}
		}
		env, _, err := c.do(ctx, method, path, body, timeout)
		if errors.Is(err, errSession) {
			c.mu.Lock()
			c.loggedIn, c.cookie = false, ""
			c.mu.Unlock()
			continue
		}
		if err != nil {
			return err
		}
		if env.Code != 0 {
			return &APIError{path, env.Code, env.Msg}
		}
		if out != nil {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("unexpected NanoKVM reply to %s", path)
			}
		}
		return nil
	}
	return ErrBadPassword // logged in, then refused again at once
}

// PowerLED reads the machine's power light, live.
func (c *Client) PowerLED(ctx context.Context) (bool, error) {
	var data map[string]json.RawMessage
	if err := c.call(ctx, http.MethodGet, "/api/vm/gpio", nil, &data, c.timeout()); err != nil {
		return false, err
	}
	var on bool
	if err := json.Unmarshal(data["pwr"], &on); err != nil {
		return false, ErrNoLED // no reading is not "off"
	}
	return on, nil
}

// Press holds a button for d (800 ms is a normal press). The KVM answers when the press ends.
func (c *Client) Press(ctx context.Context, button string, d time.Duration) error {
	if button != Power && button != Reset {
		return fmt.Errorf("unknown button %q", button)
	}
	return c.call(ctx, http.MethodPost, "/api/vm/gpio", map[string]any{"type": button, "duration": d.Milliseconds()}, nil, d+15*time.Second)
}

// Wake sends a Wake-on-LAN packet from the KVM's own network port.
func (c *Client) Wake(ctx context.Context, mac string) error {
	return c.call(ctx, http.MethodPost, "/api/network/wol", map[string]string{"mac": mac}, nil, c.timeout())
}

// Info is the KVM's firmware and hardware.
type Info struct {
	Application string `json:"application"`
	Image       string `json:"image"`
	Hardware    string `json:"hardware"` // Alpha, Beta, PCIE (Full/Cube and PCIe boards)
}

// Info reads the firmware versions and hardware.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	if err := c.call(ctx, http.MethodGet, "/api/vm/info", nil, &i, c.timeout()); err != nil {
		return i, err
	}
	var hw struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/vm/hardware", nil, &hw, c.timeout()); err == nil {
		i.Hardware = hw.Version
	}
	return i, nil
}
