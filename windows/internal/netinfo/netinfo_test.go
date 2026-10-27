package netinfo

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
)

const statusJSON = `{
  "Self": {"HostName": "Gaming-PC", "DNSName": "gaming-pc.example-tailnet.ts.net.", "OS": "windows", "Online": true, "TailscaleIPs": ["100.101.1.10", "fd7a:115c:a1e0::1"]},
  "Peer": {
    "a": {"HostName": "kvm-1a2b", "DNSName": "server-nanokvm.example-tailnet.ts.net.", "OS": "linux", "Online": true, "TailscaleIPs": ["100.101.1.20", "fd7a:115c:a1e0::2"]},
    "b": {"HostName": "pve", "DNSName": "pve.example-tailnet.ts.net.", "OS": "linux", "Online": true, "TailscaleIPs": ["100.101.1.30"]},
    "c": {"HostName": "agent", "DNSName": "agent.example-tailnet.ts.net.", "OS": "linux", "Online": true, "TailscaleIPs": ["100.101.1.50"]},
    "d": {"HostName": "Pixel", "DNSName": "pixel.example-tailnet.ts.net.", "OS": "android", "Online": true, "TailscaleIPs": ["100.101.1.40"]},
    "e": {"HostName": "old-kvm", "DNSName": "old-kvm.example-tailnet.ts.net.", "OS": "linux", "Online": false, "TailscaleIPs": ["100.1.1.1"]}
  }
}`

func TestDiscover(t *testing.T) {
	st, err := parseStatus([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	if st.SelfDNSName != "gaming-pc.example-tailnet.ts.net" || len(st.Peers) != 5 {
		t.Fatalf("parse: %+v", st)
	}
	var mu sync.Mutex // Discover probes concurrently
	var probed []string
	probe := func(_ context.Context, url string) (string, error) {
		mu.Lock()
		probed = append(probed, url)
		mu.Unlock()
		switch url {
		case "http://100.101.1.20/":
			return `<!doctype html><html><head><title>NanoKVM</title>`, nil
		case "https://100.101.1.30:8006/":
			return `<title>pve - Proxmox Virtual Environment</title>`, nil
		case "http://100.1.1.1/":
			t.Error("offline peers must not be probed")
		}
		return "", errors.New("refused")
	}
	d := Discover(context.Background(), st, probe)
	if d.NanoKVM == nil || d.NanoKVM.Address != "100.101.1.20" || d.NanoKVM.Name != "server-nanokvm" {
		t.Errorf("nanokvm: %+v", d.NanoKVM)
	}
	if d.Proxmox == nil || d.Proxmox.Address != "100.101.1.30:8006" || d.Proxmox.Name != "pve" {
		t.Errorf("proxmox: %+v", d.Proxmox)
	}
	for _, u := range probed {
		if u == "http://100.101.1.40/" {
			t.Error("non-Linux peers must not be probed")
		}
	}
}

func TestAllowedRemote(t *testing.T) {
	for s, want := range map[string]bool{
		"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false, "100.63.255.255": false,
		"fd7a:115c:a1e0::5": true, "fd7a:115c:a1e1::5": false, "127.0.0.1": true, "::1": true,
		"192.168.1.50": false, "::ffff:100.101.1.10": true,
	} {
		if got := AllowedRemote(netip.MustParseAddr(s)); got != want {
			t.Errorf("AllowedRemote(%s) = %v", s, got)
		}
	}
}
