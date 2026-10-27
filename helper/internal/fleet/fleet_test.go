package fleet

import (
	"context"
	"encoding/json"
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

type ts struct{ dns *string }

func (t ts) Status(context.Context) (*tailscale.Status, error) {
	return &tailscale.Status{Peers: []tailscale.Node{{DNSName: *t.dns, OS: "windows", Online: true, IPv4: netip.MustParseAddr("100.101.1.11")}}}, nil
}

type prober struct{ pin string }

func (p *prober) Helper(context.Context, string) (peer.Hello, string, error) {
	return peer.Hello{App: "PowerDeck", OS: "windows", Name: "GAMING-PC"}, p.pin, nil
}
func (p *prober) Page(context.Context, string) (string, error) { return "", errors.New("no") }

func TestScanRemembersAndHandlesChangedPin(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	pr := &prober{pin: "pin-1"}
	dns := "pc.example.ts.net"
	f := New(Deps{TS: ts{&dns}, Store: st, Prober: pr, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Network: func() (netinfo.Network, bool) { return netinfo.Network{LAN: []string{"192.168.1.0/24"}}, true },
		Wake:    func(mac string, _ []string) ([]string, error) { return []string{mac}, nil },
		Call: func(_ context.Context, p store.Peer, _, _ string, _, out any, _ string) error {
			if p.Pin != pr.pin {
				t.Errorf("called with pin %q", p.Pin)
			}
			b, _ := json.Marshal(map[string]any{"network": netinfo.Network{MAC: "02:00:5e:00:00:01", LAN: []string{"192.168.1.0/24"}},
				"agents": map[string]any{"allowed": false}})
			return json.Unmarshal(b, out)
		}})
	if err := f.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := f.Peer("pc")
	if err != nil || p.MAC != "02:00:5e:00:00:01" || p.Pin != "pin-1" || !p.NoAgents {
		t.Fatalf("%+v %v", p, err)
	}
	if sent, err := f.Wake("pc"); err != nil || sent[0] != "02:00:5e:00:00:01" {
		t.Errorf("wake: %v %v", sent, err)
	}
	// Known by its own name too.
	if p, err := f.Peer("gaming-pc"); err != nil || p.Name != "pc" {
		t.Errorf("by device name: %+v %v", p, err)
	}
	// Reinstalled: a new certificate from the same tailnet node is accepted.
	pr.pin = "pin-2"
	_ = f.Scan(context.Background())
	if p, _ := f.Peer("pc"); p.Pin != "pin-2" {
		t.Errorf("reinstall not accepted: %+v", p)
	}
	// A different node taking the name is ignored.
	dns, pr.pin = "pc.other.ts.net", "pin-evil"
	_ = f.Scan(context.Background())
	if p, _ := f.Peer("pc"); p.Pin != "pin-2" || p.DNSName != "pc.example.ts.net" {
		t.Errorf("impostor accepted: %+v", p)
	}
}

// A helper with owner devices answers 401 without a key, so the scan must ask with the stored one.
func TestScanSendsPeerKey(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	pr := &prober{pin: "pin-1"}
	dns := "pc.example.ts.net"
	if err := st.PutPeer(store.Peer{Name: "pc", DNSName: dns, Pin: "pin-1", Key: "pdk_peer"}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	f := New(Deps{TS: ts{&dns}, Store: st, Prober: pr, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Network: func() (netinfo.Network, bool) { return netinfo.Network{}, true },
		Call: func(_ context.Context, p store.Peer, _, _ string, _, out any, _ string) error {
			keys = append(keys, p.Key)
			if p.Key != "pdk_peer" {
				return &peer.Error{Status: 401, Code: "unauthorized"}
			}
			b, _ := json.Marshal(map[string]any{"network": netinfo.Network{MAC: "02:00:5e:00:00:01"}})
			return json.Unmarshal(b, out)
		}})
	_ = f.Scan(context.Background())
	if p, _ := f.Peer("pc"); p.MAC != "02:00:5e:00:00:01" || p.Key != "pdk_peer" {
		t.Fatalf("status not learned with the key: %+v (sent %q)", p, keys)
	}
	// After a reinstall the old key belongs to the old install; it isn't sent.
	pr.pin, keys = "pin-2", nil
	_ = f.Scan(context.Background())
	if len(keys) != 1 || keys[0] != "" {
		t.Errorf("key sent to a reinstalled helper: %q", keys)
	}
}

// A key the target no longer accepts is forgotten, so it isn't sent again (and the app relinks).
func TestRefusedKeyIsForgotten(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	dns := "pc.example.ts.net"
	if err := st.PutPeer(store.Peer{Name: "pc", DNSName: dns, Pin: "pin-1", Key: "pdk_old"}); err != nil {
		t.Fatal(err)
	}
	f := New(Deps{TS: ts{&dns}, Store: st, Prober: &prober{pin: "pin-1"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Call: func(context.Context, store.Peer, string, string, any, any, string) error {
			return &peer.Error{Status: 401, Code: "unauthorized"}
		}})
	err := f.Forward(context.Background(), "pc", "POST", "/v2/power/sleep", nil, nil, "")
	var pe *peer.Error
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("err %v", err)
	}
	if p, _ := st.Peer("pc"); p.Key != "" {
		t.Errorf("refused key kept: %+v", p)
	}
	// Other failures keep it.
	_ = st.SetPeerKey("pc", "pdk_new")
	f.Call = func(context.Context, store.Peer, string, string, any, any, string) error {
		return &peer.Error{Status: 409, Code: "in_use"}
	}
	_ = f.Forward(context.Background(), "pc", "POST", "/v2/power/sleep", nil, nil, "")
	if p, _ := st.Peer("pc"); p.Key != "pdk_new" {
		t.Errorf("key dropped on a 409: %+v", p)
	}
}
