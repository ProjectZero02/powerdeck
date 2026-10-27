// Package tailscale reads this device's Tailscale state through the official CLI: the peer list
// (for discovery), who a caller is (for authorization) and a few preferences (for setup checks).
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Node is this device or a peer.
type Node struct {
	ID        string
	HostName  string
	DNSName   string // without the trailing dot
	OS        string
	UserID    int64
	Tags      []string
	Online    bool
	IPv4      netip.Addr
	KeyExpiry time.Time // zero when key expiry is disabled
	Shared    bool      // shared in from another tailnet
}

// Name is the short MagicDNS name, falling back to the host name.
func (n Node) Name() string {
	if n.DNSName != "" {
		return strings.SplitN(n.DNSName, ".", 2)[0]
	}
	return n.HostName
}

// Status is the part of `tailscale status --json` PowerDeck uses.
type Status struct {
	BackendState string
	Self         Node
	Peers        []Node
}

type rawNode struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	UserID       int64
	Tags         []string
	Online       bool
	TailscaleIPs []string
	KeyExpiry    *time.Time
	ShareeNode   bool
}

func (r rawNode) node() Node {
	n := Node{ID: r.ID, HostName: r.HostName, DNSName: strings.TrimSuffix(r.DNSName, "."), OS: r.OS,
		UserID: r.UserID, Tags: r.Tags, Online: r.Online, Shared: r.ShareeNode}
	if r.KeyExpiry != nil {
		n.KeyExpiry = r.KeyExpiry.UTC()
	}
	for _, s := range r.TailscaleIPs {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			n.IPv4 = a
		}
	}
	return n
}

// ParseStatus parses `tailscale status --json`.
func ParseStatus(b []byte) (*Status, error) {
	var raw struct {
		BackendState string
		Self         rawNode
		Peer         map[string]rawNode
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	st := &Status{BackendState: raw.BackendState, Self: raw.Self.node()}
	for _, r := range raw.Peer {
		if n := r.node(); n.IPv4.IsValid() {
			st.Peers = append(st.Peers, n)
		}
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].Name() < st.Peers[j].Name() })
	return st, nil
}

// Identity is who a tailnet caller is, from `tailscale whois`.
type Identity struct {
	NodeID   string
	NodeName string // short MagicDNS name, e.g. "oppo-find-x9-ultra"
	Hostname string // the device's own name, e.g. "Pixel 9 Pro"
	UserID   int64
	Login    string
	Tagged   bool
	Shared   bool // the node belongs to another tailnet and was shared in
}

// ParseWhoIs parses `tailscale whois --json`.
func ParseWhoIs(b []byte) (Identity, error) {
	var raw struct {
		Node struct {
			StableID     string
			Name         string
			ComputedName string
			Hostinfo     struct{ Hostname string }
			User         int64
			Tags         []string
			Sharer       int64
		}
		UserProfile struct {
			ID        int64
			LoginName string
		}
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Identity{}, err
	}
	n := raw.Node
	id := Identity{
		NodeID: n.StableID, NodeName: n.ComputedName, Hostname: n.Hostinfo.Hostname,
		UserID: n.User, Login: raw.UserProfile.LoginName, Tagged: len(n.Tags) > 0, Shared: n.Sharer != 0,
	}
	if id.NodeName == "" {
		id.NodeName = strings.SplitN(strings.TrimSuffix(n.Name, "."), ".", 2)[0]
	}
	if id.Hostname == "" {
		id.Hostname = id.NodeName
	}
	if id.NodeID == "" {
		id.NodeID = id.NodeName
	}
	return id, nil
}

// Client runs the tailscale CLI. The zero value is not usable; use New.
type Client struct {
	run func(ctx context.Context, args ...string) ([]byte, error)

	mu    sync.Mutex
	whois map[netip.Addr]cached
	st    *Status
	stAt  time.Time
	now   func() time.Time
}

type cached struct {
	id  Identity
	err error
	at  time.Time
}

// New returns a client for the installed CLI.
func New() *Client {
	return NewWithRunner(func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, cliPath(), args...)
		hideWindow(cmd)
		return cmd.Output()
	})
}

// NewWithRunner returns a client that runs CLI commands through run (tests).
func NewWithRunner(run func(ctx context.Context, args ...string) ([]byte, error)) *Client {
	return &Client{run: run, whois: map[netip.Addr]cached{}, now: time.Now}
}

// ErrUnavailable means Tailscale isn't installed or isn't running.
var ErrUnavailable = errors.New("Tailscale isn't running on this device")

// Status returns the current status, cached for 15 seconds.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	c.mu.Lock()
	if c.st != nil && c.now().Sub(c.stAt) < 15*time.Second {
		st := c.st
		c.mu.Unlock()
		return st, nil
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := c.run(ctx, "status", "--json")
	if err != nil && len(out) == 0 {
		return nil, ErrUnavailable
	}
	st, err := ParseStatus(out)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.st, c.stAt = st, c.now()
	c.mu.Unlock()
	return st, nil
}

// WhoIs identifies a tailnet address. Results are cached for a minute (failures for 5 seconds), so a
// busy phone doesn't spawn a process per request.
func (c *Client) WhoIs(ctx context.Context, ip netip.Addr) (Identity, error) {
	c.mu.Lock()
	if e, ok := c.whois[ip]; ok && (c.now().Sub(e.at) < 5*time.Second || e.err == nil && c.now().Sub(e.at) < time.Minute) {
		c.mu.Unlock()
		return e.id, e.err
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id Identity
	out, err := c.run(ctx, "whois", "--json", ip.String())
	if err == nil {
		id, err = ParseWhoIs(out)
	}
	c.mu.Lock()
	if len(c.whois) > 256 {
		clear(c.whois)
	}
	c.whois[ip] = cached{id: id, err: err, at: c.now()}
	c.mu.Unlock()
	return id, err
}

// Unattended reports Windows' "Run unattended" preference (ForceDaemon).
func (c *Client) Unattended(ctx context.Context) (bool, error) {
	out, err := c.run(ctx, "debug", "prefs")
	if err != nil {
		return false, err
	}
	var p struct{ ForceDaemon bool }
	if err := json.Unmarshal(out, &p); err != nil {
		return false, err
	}
	return p.ForceDaemon, nil
}

// SetUnattended turns on "Run unattended" (Windows only).
func (c *Client) SetUnattended(ctx context.Context) error {
	_, err := c.run(ctx, "set", "--unattended=true")
	return err
}
