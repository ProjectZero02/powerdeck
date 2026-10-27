// Package netinfo answers network questions: is an address on the tailnet, what's this device's
// tailnet address, and which wired card (MAC, LAN subnets) wakes it.
package netinfo

import (
	"net"
	"net/netip"
	"slices"
	"strings"
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

// TailnetAddrs returns this device's Tailscale addresses (IPv4 and IPv6). Only Tailscale's own
// interface counts: an ISP's carrier-grade NAT also hands out 100.64.0.0/10 addresses, on the LAN card.
func TailnetAddrs() []netip.Addr {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var all []Iface
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		x := Iface{Name: ifc.Name}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				x.Addrs = append(x.Addrs, p.Addr().Unmap())
			}
		}
		all = append(all, x)
	}
	return tailnetAddrsOf(all)
}

// TailnetIPv4 returns this device's Tailscale IPv4 address.
func TailnetIPv4() (netip.Addr, bool) {
	for _, a := range TailnetAddrs() {
		if a.Is4() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// Iface is one network interface's name and addresses.
type Iface struct {
	Name  string
	Addrs []netip.Addr
}

// tailnetAddrsOf picks the Tailscale addresses: those on an interface named for Tailscale
// ("tailscale0" on Linux, "Tailscale" on Windows) or one that also carries a Tailscale IPv6
// address (macOS utun, a renamed interface).
func tailnetAddrsOf(ifs []Iface) []netip.Addr {
	var out []netip.Addr
	for _, ifc := range ifs {
		ts := strings.Contains(strings.ToLower(ifc.Name), "tailscale") ||
			slices.ContainsFunc(ifc.Addrs, tailnetV6.Contains)
		if !ts {
			continue
		}
		for _, a := range ifc.Addrs {
			if IsTailnet(a) {
				out = append(out, a)
			}
		}
	}
	return out
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
