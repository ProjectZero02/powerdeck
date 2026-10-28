package tailscale

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

const statusJSON = `{
  "BackendState": "Running",
  "Self": {"ID": "nSELF", "HostName": "Gaming-PC", "DNSName": "gaming-pc.example-tailnet.ts.net.", "OS": "windows", "UserID": 1,
           "Online": true, "TailscaleIPs": ["100.101.1.10", "fd7a:115c:a1e0::1"], "KeyExpiry": "2026-12-24T10:00:00Z"},
  "Peer": {
    "k1": {"ID": "nKVM", "HostName": "kvm-a34d", "DNSName": "server-nanokvm.example-tailnet.ts.net.", "OS": "linux", "UserID": 1, "Online": true, "TailscaleIPs": ["100.101.1.20"]},
    "k2": {"ID": "nVM", "HostName": "private", "DNSName": "private.example-tailnet.ts.net.", "OS": "linux", "UserID": 9, "Tags": ["tag:server"], "Online": true, "TailscaleIPs": ["100.101.1.50"]},
    "k3": {"ID": "nSH", "HostName": "friend", "DNSName": "friend.example-tailnet.ts.net.", "OS": "android", "UserID": 7, "Online": false, "ShareeNode": true, "TailscaleIPs": ["100.101.1.60"]},
    "k4": {"ID": "nX", "HostName": "noip", "OS": "linux", "Online": true, "TailscaleIPs": []}
  }
}`

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	if st.BackendState != "Running" || st.Self.Name() != "gaming-pc" || st.Self.UserID != 1 || st.Self.KeyExpiry.IsZero() {
		t.Fatalf("self: %+v", st.Self)
	}
	if len(st.Peers) != 3 {
		t.Fatalf("peers: %+v", st.Peers)
	}
	if p := st.Peers[2]; p.Name() != "server-nanokvm" || p.IPv4 != netip.MustParseAddr("100.101.1.20") || !p.KeyExpiry.IsZero() {
		t.Errorf("sorted peers: %+v", st.Peers)
	}
	if !st.Peers[0].Shared || len(st.Peers[1].Tags) != 1 {
		t.Errorf("shared/tags: %+v", st.Peers)
	}
}

const whoisJSON = `{"Node": {"StableID": "nPHONE", "Name": "pixel-9-pro.example-tailnet.ts.net.", "ComputedName": "pixel-9-pro",
  "Hostinfo": {"Hostname": "Pixel 9 Pro"}, "User": 1},
  "UserProfile": {"ID": 1, "LoginName": "owner@example.com"}, "CapMap": null}`

func TestParseWhoIs(t *testing.T) {
	id, err := ParseWhoIs([]byte(whoisJSON))
	if err != nil {
		t.Fatal(err)
	}
	if id.NodeID != "nPHONE" || id.Hostname != "Pixel 9 Pro" || id.UserID != 1 || id.Tagged || id.Shared {
		t.Fatalf("%+v", id)
	}
	tagged, _ := ParseWhoIs([]byte(`{"Node": {"StableID": "n2", "Name": "atlas.x.ts.net.", "User": 99, "Tags": ["tag:ai"]}, "UserProfile": {"ID": 99}}`))
	if !tagged.Tagged || tagged.NodeName != "atlas" {
		t.Errorf("tagged: %+v", tagged)
	}
}

func TestWhoIsIsCached(t *testing.T) {
	calls := 0
	c := NewWithRunner(func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if args[0] == "whois" && args[2] == "100.101.1.40" {
			return []byte(whoisJSON), nil
		}
		return nil, errors.New("no such peer")
	})
	now := time.Now()
	c.now = func() time.Time { return now }
	ip := netip.MustParseAddr("100.101.1.40")
	for i := 0; i < 3; i++ {
		if id, err := c.WhoIs(context.Background(), ip); err != nil || id.NodeID != "nPHONE" {
			t.Fatal(id, err)
		}
	}
	if calls != 1 {
		t.Errorf("%d CLI calls", calls)
	}
	now = now.Add(2 * time.Minute)
	_, _ = c.WhoIs(context.Background(), ip)
	if calls != 2 {
		t.Errorf("cache didn't expire: %d calls", calls)
	}
}
