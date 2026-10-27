// Package cli is the helper's command-line side: it talks to the running service over loopback
// with the local token (readable only by Administrators/root) and prints things for a terminal.
package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"
)

// Client calls the local helper.
type Client struct {
	DataDir string
	Port    int
}

func (c Client) token() (string, error) {
	b, err := os.ReadFile(filepath.Join(c.DataDir, "local.token"))
	if errors.Is(err, os.ErrPermission) {
		return "", errors.New("permission denied: run this as Administrator / with sudo")
	}
	if err != nil {
		return "", errors.New("the PowerDeck service isn't running (no local token)")
	}
	return strings.TrimSpace(string(b)), nil
}

// Do calls the local API.
func (c Client) Do(method, path string, body, out any) error {
	tok, err := c.token()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, "https://127.0.0.1:"+strconv.Itoa(c.Port)+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // loopback to our own service
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("the PowerDeck service isn't answering: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct{ Message string }
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return errors.New(e.Message)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// QR renders text as a terminal QR code using half blocks (two rows per line), dark on light.
func QR(text string) (string, error) {
	c, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", err
	}
	const quiet = 2
	n := c.Size + 2*quiet
	black := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < c.Size && y < c.Size && c.Black(x, y)
	}
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.WriteString("\x1b[30;47m") // black on white, so it scans on dark terminals too
		for x := 0; x < n; x++ {
			top, bot := black(x, y), y+1 < n && black(x, y+1)
			switch {
			case top && bot:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bot:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}

// Check is a setup check as the API returns it.
type Check struct {
	ID, Level, Title, Detail string
	Fixable                  bool
}

// PrintChecks prints a checklist.
func PrintChecks(w io.Writer, cs []Check) {
	for _, c := range cs {
		mark := map[string]string{"ok": "\x1b[32m✓\x1b[0m", "warn": "\x1b[33m!\x1b[0m", "error": "\x1b[31m✕\x1b[0m", "info": "\x1b[90mi\x1b[0m"}[c.Level]
		fmt.Fprintf(w, "  %s %s\n", mark, c.Title)
		if c.Detail != "" && c.Level != "ok" {
			fmt.Fprintf(w, "      \x1b[90m%s\x1b[0m\n", c.Detail)
		}
	}
}
