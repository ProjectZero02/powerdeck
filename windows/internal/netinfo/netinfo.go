// Package netinfo answers "where am I on the tailnet" and finds the NanoKVM and Proxmox host there,
// so the pairing code can carry everything the phone needs without anyone typing addresses.
package netinfo

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// IsTailnet reports whether a is a Tailscale address.
func IsTailnet(a netip.Addr) bool {
	a = a.Unmap()
	return tailnetV4.Contains(a) || tailnetV6.Contains(a)
}

// AllowedRemote is the network-level policy for the phone API: tailnet peers and loopback only.
func AllowedRemote(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || IsTailnet(a)
}

// TailnetIPv4 returns this machine's Tailscale IPv4 address.
func TailnetIPv4() (netip.Addr, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Addr{}, false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() && tailnetV4.Contains(p.Addr()) {
			return p.Addr(), true
		}
	}
	return netip.Addr{}, false
}

// Peer is a node from `tailscale status --json`.
type Peer struct {
	HostName string
	DNSName  string
	OS       string
	Online   bool
	IPv4     netip.Addr
}

// Status is the subset of `tailscale status --json` PowerDeck uses.
type Status struct {
	SelfHostName string
	SelfDNSName  string
	Peers        []Peer
}

type rawNode struct {
	HostName     string
	DNSName      string
	OS           string
	Online       bool
	TailscaleIPs []string
}

// TailscaleStatus runs the Tailscale CLI.
func TailscaleStatus(ctx context.Context) (*Status, error) {
	cmd := exec.CommandContext(ctx, tailscaleCLI(), "status", "--json")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseStatus(out)
}

func parseStatus(b []byte) (*Status, error) {
	var raw struct {
		Self rawNode
		Peer map[string]rawNode
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	st := &Status{SelfHostName: raw.Self.HostName, SelfDNSName: strings.TrimSuffix(raw.Self.DNSName, ".")}
	for _, n := range raw.Peer {
		p := Peer{HostName: n.HostName, DNSName: strings.TrimSuffix(n.DNSName, "."), OS: n.OS, Online: n.Online}
		for _, s := range n.TailscaleIPs {
			if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
				p.IPv4 = a
			}
		}
		if p.IPv4.IsValid() {
			st.Peers = append(st.Peers, p)
		}
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].HostName < st.Peers[j].HostName })
	return st, nil
}

// Found is a discovered device.
type Found struct {
	Address string `json:"address"` // host or host:port as the phone should use it
	Name    string `json:"name"`    // MagicDNS name, for display
}

// Discovered is the result of a tailnet scan.
type Discovered struct {
	SelfDNSName string    `json:"selfDnsName,omitempty"`
	NanoKVM     *Found    `json:"nanokvm,omitempty"`
	Proxmox     *Found    `json:"proxmox,omitempty"`
	CheckedAt   time.Time `json:"checkedAt"`
	Error       string    `json:"error,omitempty"`
}

// Prober fetches a page; swapped out in tests.
type Prober func(ctx context.Context, url string) (string, error)

// HTTPProber is the real prober: short timeouts, any certificate (Proxmox uses a self-signed one;
// nothing secret is sent and the result only decides which address to suggest).
func HTTPProber() Prober {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // identification probe only
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context, url string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return string(b), err
	}
}

// Discover probes online Linux peers for a NanoKVM web UI (port 80) and a Proxmox UI (port 8006).
func Discover(ctx context.Context, st *Status, probe Prober) Discovered {
	d := Discovered{SelfDNSName: st.SelfDNSName, CheckedAt: time.Now().UTC()}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range st.Peers {
		if !p.Online || !strings.EqualFold(p.OS, "linux") {
			continue
		}
		p := p
		wg.Add(2)
		go func() {
			defer wg.Done()
			if body, err := probe(ctx, "http://"+p.IPv4.String()+"/"); err == nil && strings.Contains(strings.ToLower(body), "<title>nanokvm</title>") {
				mu.Lock()
				if d.NanoKVM == nil || p.HostName < d.NanoKVM.Name {
					d.NanoKVM = &Found{Address: p.IPv4.String(), Name: displayName(p)}
				}
				mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			if body, err := probe(ctx, "https://"+p.IPv4.String()+":8006/"); err == nil && strings.Contains(body, "Proxmox Virtual Environment") {
				mu.Lock()
				if d.Proxmox == nil || p.HostName < d.Proxmox.Name {
					d.Proxmox = &Found{Address: p.IPv4.String() + ":8006", Name: displayName(p)}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return d
}

func displayName(p Peer) string {
	if p.DNSName != "" {
		return strings.SplitN(p.DNSName, ".", 2)[0]
	}
	return p.HostName
}
