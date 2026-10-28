// Package webhook posts the hub's machine events to the owner's webhooks, signed so the receiver
// can tell they came from this hub, with retries.
//
// Each delivery is the event as JSON, with these headers:
//
//	X-PowerDeck-Event: hung
//	X-PowerDeck-Delivery: <the event's ID; the same on every retry>
//	X-PowerDeck-Timestamp: <Unix seconds of this attempt>
//	X-PowerDeck-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//
// A receiver checks the signature, and that the timestamp is recent (a few minutes) so an old
// delivery can't be replayed.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/secret"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/version"
)

// DefaultEvents are what a webhook gets unless it asks for others.
var DefaultEvents = []string{machines.StateHung, "recovered", machines.EventRecovery}

// Retries are the waits before each new try after a failed delivery.
var Retries = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute}

// Sign is the signature header's value for a delivery.
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

var tailnet4 = netip.MustParsePrefix("100.64.0.0/10")
var tailnet6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

// CheckURL accepts https anywhere, and plain http only to a Tailscale or loopback address or a
// MagicDNS name (WireGuard encrypts those already).
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return errors.New("that isn't a web address")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if a, err := netip.ParseAddr(host); err == nil {
			if a.IsLoopback() || tailnet4.Contains(a) || tailnet6.Contains(a) {
				return nil
			}
		} else if host == "localhost" || strings.HasSuffix(host, ".ts.net") {
			return nil
		}
		return errors.New("plain http is only allowed to a Tailscale or loopback address; use https")
	}
	return errors.New("the address must start with https://")
}

// Sender delivers events to every webhook that wants them.
type Sender struct {
	Store   *store.Store
	Box     secret.Box
	Bus     *machines.Bus
	Log     *slog.Logger
	Now     func() time.Time
	Client  *http.Client    // default: 10 s timeout, redirects not followed
	Retries []time.Duration // default: Retries

	wg sync.WaitGroup
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return defaultClient
}

var defaultClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Run delivers events until ctx ends, then waits for deliveries in progress (their retries stop).
func (s *Sender) Run(ctx context.Context) {
	defer s.wg.Wait()
	for {
		ch, stop := s.Bus.Subscribe(64)
		for open := true; open; {
			select {
			case <-ctx.Done():
				stop()
				return
			case e, ok := <-ch:
				if !ok {
					open = false // fell behind: events were missed
					s.log().Warn("webhook deliveries fell behind; some events weren't sent")
					break
				}
				s.dispatch(ctx, e)
			}
		}
	}
}

func (s *Sender) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Sender) dispatch(ctx context.Context, e machines.Event) {
	for _, h := range s.Store.Webhooks() {
		if !slices.Contains(h.Events, e.Event) {
			continue
		}
		s.wg.Add(1)
		go func(h store.Webhook) {
			defer s.wg.Done()
			s.deliver(ctx, h, e)
		}(h)
	}
}

// deliver tries once, then after each retry wait, until a 2xx.
func (s *Sender) deliver(ctx context.Context, h store.Webhook, e machines.Event) {
	waits := s.Retries
	if waits == nil {
		waits = Retries
	}
	for i := 0; ; i++ {
		err := s.Send(ctx, h, e)
		result := "ok"
		if err != nil {
			result = err.Error()
		}
		s.Store.WebhookDelivered(h.ID, s.now(), result)
		if err == nil {
			return
		}
		if i == len(waits) {
			s.log().Warn("webhook delivery failed; giving up", "webhook", h.ID, "event", e.ID, "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(waits[i]):
		}
	}
}

// Send makes one delivery of e to h.
func (s *Sender) Send(ctx context.Context, h store.Webhook, e machines.Event) error {
	sec, err := s.Box.Open(h.Secret)
	if err != nil {
		return fmt.Errorf("the webhook's secret can't be opened: %w", err)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(s.now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PowerDeck/"+version.Version)
	req.Header.Set("X-PowerDeck-Event", e.Event)
	req.Header.Set("X-PowerDeck-Delivery", e.ID)
	req.Header.Set("X-PowerDeck-Timestamp", ts)
	req.Header.Set("X-PowerDeck-Signature", Sign(sec, ts, body))
	resp, err := s.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is in the list already
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Wait waits for deliveries in progress (tests).
func (s *Sender) Wait() { s.wg.Wait() }
