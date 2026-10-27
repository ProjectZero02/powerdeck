// Package wol sends Wake-on-LAN magic packets and (on Linux) arms a card to be woken.
package wol

import (
	"errors"
	"net"
	"net/netip"
	"time"
)

// Packet builds a magic packet: 6 × 0xFF, then the MAC 16 times.
func Packet(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		p = append(p, 0xFF)
	}
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p
}

// Broadcast is the directed-broadcast address of an IPv4 subnet.
func Broadcast(prefix netip.Prefix) netip.Addr {
	a := prefix.Masked().Addr().As4()
	host := 32 - prefix.Bits()
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v |= (1 << host) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// Targets are where Send aims: each LAN's broadcast and the limited broadcast, on port 9.
func Targets(lans []string) []netip.AddrPort {
	out := []netip.AddrPort{netip.AddrPortFrom(netip.AddrFrom4([4]byte{255, 255, 255, 255}), 9)}
	for _, l := range lans {
		if p, err := netip.ParsePrefix(l); err == nil && p.Addr().Is4() && p.Bits() < 31 {
			out = append(out, netip.AddrPortFrom(Broadcast(p), 9))
		}
	}
	return out
}

// Send sends the magic packet for mac three times to every target, 100 ms apart. It succeeds if
// at least one packet left the machine.
func Send(mac string, lans []string) ([]string, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, errors.New("not a MAC address: " + mac)
	}
	conn, err := listenBroadcast()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	pkt := Packet(hw)
	var sent []string
	var lastErr error
	targets := Targets(lans)
	for round := 0; round < 3; round++ {
		for _, t := range targets {
			if _, err := conn.WriteToUDPAddrPort(pkt, t); err != nil {
				lastErr = err
			} else if round == 0 {
				sent = append(sent, t.Addr().String())
			}
		}
		if round < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if len(sent) == 0 {
		return nil, lastErr
	}
	return sent, nil
}
