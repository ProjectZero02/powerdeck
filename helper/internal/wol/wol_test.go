package wol

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
)

func TestPacket(t *testing.T) {
	mac, _ := net.ParseMAC("d8:5e:d3:12:34:56")
	p := Packet(mac)
	if len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{0xFF}, 6)) || !bytes.Equal(p[96:], mac) {
		t.Fatalf("bad packet % x", p)
	}
}

func TestTargets(t *testing.T) {
	got := Targets([]string{"192.168.1.0/24", "10.0.0.0/16", "bogus"})
	want := []string{"255.255.255.255:9", "192.168.1.255:9", "10.0.255.255:9"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("target %d: %s, want %s", i, got[i], want[i])
		}
	}
	if b := Broadcast(netip.MustParsePrefix("172.16.5.9/20")); b.String() != "172.16.15.255" {
		t.Errorf("broadcast %s", b)
	}
}
