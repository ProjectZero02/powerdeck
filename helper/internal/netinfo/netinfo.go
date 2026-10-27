// Package netinfo answers network questions: is an address on the tailnet, what's this device's
// tailnet address, and which wired card (MAC, LAN subnets) wakes it.
package netinfo

import (
	"net"
	"net/netip"
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

// AllowedRemote is the helper API's source-address policy: tailnet and loopback only.
func AllowedRemote(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || IsTailnet(a)
}

// TailnetIPv4 returns this device's Tailscale IPv4 address.
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

// Network is the card that carries this device's LAN traffic, which Wake-on-LAN must target.
type Network struct {
	Interface string   `json:"interface"`         // OS name of the physical card (e.g. "Ethernet", "enp3s0")
	MAC       string   `json:"mac,omitempty"`     // aa:bb:cc:dd:ee:ff
	LAN       []string `json:"lan,omitempty"`     // IPv4 subnets, e.g. 192.168.1.0/24
	Wired     bool     `json:"wired"`             // Ethernet (Wi-Fi can't wake from shutdown)
	Battery   bool     `json:"battery,omitempty"` // running on battery (laptops)
}

// Overlaps reports whether two subnet lists share a subnet.
func Overlaps(a, b []string) bool {
	for _, x := range a {
		px, err := netip.ParsePrefix(x)
		if err != nil {
			continue
		}
		for _, y := range b {
			if py, err := netip.ParsePrefix(y); err == nil && px.Overlaps(py) {
				return true
			}
		}
	}
	return false
}

// subnets returns the IPv4 subnets of an interface's addresses (excluding the tailnet).
func subnets(addrs []net.Addr) []string {
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil {
			continue
		}
		p, err := netip.ParsePrefix(ipn.String())
		if err != nil || tailnetV4.Contains(p.Addr()) || p.Addr().IsLinkLocalUnicast() {
			continue
		}
		out = append(out, p.Masked().String())
	}
	return out
}
