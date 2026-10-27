// Package peer is a client for other PowerDeck helpers: identification during discovery
// (trust-on-first-use of the certificate) and pinned requests afterwards.
package peer

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
	"strconv"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/tlscert"
)

// Port is the helper API port.
const Port = 47321

// Hello is a helper's /v2/hello answer.
type Hello struct {
	App        string `json:"app"`
	APIVersion int    `json:"apiVersion"`
	Version    string `json:"version"`
	Name       string `json:"name"`
	OS         string `json:"os"`
}

// ErrPinMismatch means a helper presented a different certificate than the one remembered.
var ErrPinMismatch = errors.New("the device's certificate changed")

func client(pin string, timeout time.Duration, seen *string) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS13,
				InsecureSkipVerify: true, //nolint:gosec // verified below by pin (or recorded on first use)
				VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
					if len(raw) == 0 {
						return errors.New("no certificate")
					}
					got := tlscert.Pin(raw[0])
					if seen != nil {
						*seen = got
					}
					if pin != "" && got != pin {
						return ErrPinMismatch
					}
					return nil
				},
			},
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		},
	}
}

func url(addr string, port int, path string) string {
	return "https://" + net.JoinHostPort(addr, strconv.Itoa(port)) + path
}

// Identify fetches /v2/hello without a pin and returns the certificate pin it presented.
func Identify(ctx context.Context, addr string, port int) (Hello, string, error) {
	var pin string
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url(addr, port, "/v2/hello"), nil)
	resp, err := client("", 4*time.Second, &pin).Do(req)
	if err != nil {
		return Hello{}, "", err
	}
	defer resp.Body.Close()
	var h Hello
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&h); err != nil || h.App != "PowerDeck" {
		return Hello{}, "", errors.New("not a PowerDeck helper")
	}
	return h, pin, nil
}

// Error is a non-2xx answer from a helper.
type Error struct {
	Status  int
	Code    string `json:"error"`
	Message string `json:"message"`
	Body    json.RawMessage
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// ForHeader names the person or agent a relayed request is for. Helpers only show it (activity
// log, shutdown message), and only when the caller is an owner.
const ForHeader = "X-PowerDeck-For"

// Do sends a pinned request and decodes a JSON answer into out (may be nil). onBehalfOf, when set,
// is sent as ForHeader.
func Do(ctx context.Context, addr string, port int, pin, method, path string, body, out any, onBehalfOf string) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url(addr, port, path), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if onBehalfOf != "" {
		req.Header.Set(ForHeader, onBehalfOf)
	}
	resp, err := client(pin, 15*time.Second, nil).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode, Body: raw}
		_ = json.Unmarshal(raw, e)
		return e
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
