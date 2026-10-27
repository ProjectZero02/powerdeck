package fleet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
)

type ts struct{}

func (ts) Status(context.Context) (*tailscale.Status, error) {
	return &tailscale.Status{Peers: []tailscale.Node{{DNSName: "pc.example.ts.net", OS: "windows", Online: true, IPv4: netip.MustParseAddr("100.101.1.11")}}}, nil
}

type prober struct{ pin string }

func (p *prober) Helper(context.Context, string) (peer.Hello, string, error) {
	return peer.Hello{App: "PowerDeck", OS: "windows"}, p.pin, nil
}
func (p *prober) Page(context.Context, string) (string, error) { return "", errors.New("no") }

func TestScanRemembersAndRefusesChangedPin(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	pr := &prober{pin: "pin-1"}
	f := New(Deps{TS: ts{}, Store: st, Prober: pr, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Network: func() (netinfo.Network, bool) { return netinfo.Network{LAN: []string{"192.168.1.0/24"}}, true },
		Wake:    func(mac string, _ []string) ([]string, error) { return []string{mac}, nil },
		Call: func(_ context.Context, p store.Peer, _, _ string, _, out any, _ string) error {
			if p.Pin != "pin-1" {
				t.Errorf("called with pin %q", p.Pin)
			}
			if o, ok := out.(*struct {
				Network netinfo.Network `json:"network"`
			}); ok {
				o.Network = netinfo.Network{MAC: "c8:7f:54:00:00:01", LAN: []string{"192.168.1.0/24"}}
			}
			return nil
		}})
	if err := f.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := f.Peer("pc")
	if err != nil || p.MAC != "c8:7f:54:00:00:01" || p.Pin != "pin-1" {
		t.Fatalf("%+v %v", p, err)
	}
	if sent, err := f.Wake("pc"); err != nil || sent[0] != "c8:7f:54:00:00:01" {
		t.Errorf("wake: %v %v", sent, err)
	}
	pr.pin = "pin-evil"
	_ = f.Scan(context.Background())
	if p, _ := f.Peer("pc"); p.Pin != "pin-1" {
		t.Errorf("changed certificate accepted: %+v", p)
	}
}
