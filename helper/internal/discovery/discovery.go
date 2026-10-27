// Package discovery finds PowerDeck helpers and KVMs among this device's tailnet peers. The phone
// can't list tailnet devices itself, so it asks a helper it knows (see docs/protocol.md).
package discovery

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
)

// Peer kinds.
const (
	KindHelper  = "helper"
	KindNanoKVM = "nanokvm"
	KindPiKVM   = "pikvm"
)

// Peer is a found device.
type Peer struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	DNSName string `json:"dnsName,omitempty"`
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	OS      string `json:"os,omitempty"`
	Online  bool   `json:"online"`
	Pin     string `json:"pin,omitempty"`
	Version string `json:"version,omitempty"`
}

// Prober identifies what runs on a peer; injectable for tests.
type Prober interface {
	Helper(ctx context.Context, addr string) (peer.Hello, string, error)
	Page(ctx context.Context, url string) (string, error)
}

// Network is the real prober.
type Network struct{}

func (Network) Helper(ctx context.Context, addr string) (peer.Hello, string, error) {
	return peer.Identify(ctx, addr, peer.Port)
}

var pageClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // identification only
		DisableKeepAlives: true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (Network) Page(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := pageClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return string(b), err
}

// Scan probes every online peer (16 at a time) and returns what it found, sorted by name.
func Scan(ctx context.Context, st *tailscale.Status, pr Prober) []Peer {
	var (
		mu  sync.Mutex
		out []Peer
		wg  sync.WaitGroup
	)
	sem := make(chan struct{}, 16)
	add := func(p Peer) {
		mu.Lock()
		out = append(out, p)
		mu.Unlock()
	}
	for _, n := range st.Peers {
		if !n.Online {
			continue
		}
		wg.Add(1)
		go func(n tailscale.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			addr := n.IPv4.String()
			base := Peer{Name: n.Name(), DNSName: n.DNSName, Address: addr, OS: n.OS, Online: true}
			if h, pin, err := pr.Helper(ctx, addr); err == nil {
				p := base
				p.Kind, p.Port, p.Pin, p.Version = KindHelper, peer.Port, pin, h.Version
				if h.OS != "" {
					p.OS = h.OS
				}
				add(p)
			}
			if !strings.EqualFold(n.OS, "linux") {
				return
			}
			if body, err := pr.Page(ctx, "http://"+addr+"/"); err == nil && strings.Contains(strings.ToLower(body), "<title>nanokvm</title>") {
				p := base
				p.Kind = KindNanoKVM
				add(p)
				return
			}
			if body, err := pr.Page(ctx, "https://"+addr+"/"); err == nil && strings.Contains(strings.ToLower(body), "pikvm") {
				p := base
				p.Kind = KindPiKVM
				add(p)
			}
		}(n)
	}
	wg.Wait()
	sortPeers(out)
	return out
}

func sortPeers(ps []Peer) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].Kind < ps[j].Kind
	})
}
