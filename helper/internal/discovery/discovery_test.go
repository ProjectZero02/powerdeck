package discovery

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"

	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
)

type fake struct {
	mu     sync.Mutex
	probed []string
}

func (f *fake) Helper(_ context.Context, addr string) (peer.Hello, string, error) {
	f.mu.Lock()
	f.probed = append(f.probed, addr)
	f.mu.Unlock()
	if addr == "100.101.1.11" || addr == "100.101.1.30" {
		return peer.Hello{App: "PowerDeck", Version: "2.0.0", OS: "linux"}, "pin-" + addr, nil
	}
	return peer.Hello{}, "", errors.New("refused")
}

func (f *fake) Page(_ context.Context, url string) (string, error) {
	switch url {
	case "http://100.101.1.20/":
		return `<!doctype html><html><head><title>NanoKVM</title>`, nil
	case "https://100.101.1.21/":
		return `<title>PiKVM</title>`, nil
	}
	return "", errors.New("refused")
}

func node(name, os, ip string, online bool) tailscale.Node {
	return tailscale.Node{DNSName: name + ".example.ts.net", OS: os, Online: online, IPv4: netip.MustParseAddr(ip)}
}

func TestScan(t *testing.T) {
	st := &tailscale.Status{Peers: []tailscale.Node{
		node("workstation", "windows", "100.101.1.11", true),
		node("server-nanokvm", "linux", "100.101.1.20", true),
		node("pikvm", "linux", "100.101.1.21", true),
		node("pve", "linux", "100.101.1.30", true),
		node("phone", "android", "100.101.1.40", true),
		node("off", "linux", "100.1.1.1", false),
	}}
	f := &fake{}
	got := Scan(context.Background(), st, f)
	want := []struct{ kind, name, pin string }{
		{KindPiKVM, "pikvm", ""}, {KindHelper, "pve", "pin-100.101.1.30"},
		{KindNanoKVM, "server-nanokvm", ""}, {KindHelper, "workstation", "pin-100.101.1.11"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Name != w.name || got[i].Pin != w.pin {
			t.Errorf("%d: %+v, want %+v", i, got[i], w)
		}
	}
	for _, a := range f.probed {
		if a == "100.1.1.1" {
			t.Error("offline peers must not be probed")
		}
	}
}
