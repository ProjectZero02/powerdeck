package netinfo

import (
	"net/netip"
	"slices"
	"testing"
)

func TestTailnetAddrsOnlyFromTailscale(t *testing.T) {
	ip := netip.MustParseAddr
	ifs := []Iface{
		{Name: "eth0", Addrs: []netip.Addr{ip("100.72.3.4"), ip("192.168.1.10")}}, // carrier-grade NAT on the LAN card
		{Name: "tailscale0", Addrs: []netip.Addr{ip("100.101.1.11"), ip("fd7a:115c:a1e0::1")}},
		{Name: "utun4", Addrs: []netip.Addr{ip("100.101.1.12"), ip("fd7a:115c:a1e0::2"), ip("fe80::1")}},
		{Name: "Tailscale", Addrs: []netip.Addr{ip("100.101.1.13")}}, // Windows with IPv6 off
		{Name: "utun5", Addrs: []netip.Addr{ip("100.80.0.1")}},       // another VPN in the same range
	}
	got := tailnetAddrsOf(ifs)
	want := []netip.Addr{ip("100.101.1.11"), ip("fd7a:115c:a1e0::1"), ip("100.101.1.12"), ip("fd7a:115c:a1e0::2"), ip("100.101.1.13")}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
