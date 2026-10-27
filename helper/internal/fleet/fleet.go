// Package fleet is what a helper knows about the other PowerDeck devices on the tailnet. It scans
// the tailnet, remembers helpers (certificate pin, MAC, LAN) so they can be woken while off, and
// relays actions to them for agents and schedules.
package fleet

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/discovery"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
	"github.com/projectzero02/powerdeck/helper/internal/wol"
)

// Deps are the fleet's collaborators.
type Deps struct {
	TS interface {
		Status(ctx context.Context) (*tailscale.Status, error)
	}
	Store   *store.Store
	Prober  discovery.Prober
	Network func() (netinfo.Network, bool)
	Wake    func(mac string, lans []string) ([]string, error) // default wol.Send
	Call    func(ctx context.Context, p store.Peer, method, path string, body, out any, onBehalfOf string) error
	Log     *slog.Logger
	Now     func() time.Time
}

// Fleet is safe for concurrent use.
type Fleet struct {
	Deps
	mu      sync.Mutex
	found   []discovery.Peer
	scanned time.Time
	scanMu  sync.Mutex
}

func New(d Deps) *Fleet {
	if d.Wake == nil {
		d.Wake = wol.Send
	}
	if d.Call == nil {
		d.Call = func(ctx context.Context, p store.Peer, method, path string, body, out any, onBehalfOf string) error {
			return peer.Do(ctx, p.Address, p.Port, p.Pin, p.Key, method, path, body, out, onBehalfOf)
		}
	}
	return &Fleet{Deps: d}
}

// Found returns the last scan's results and when it ran.
func (f *Fleet) Found() ([]discovery.Peer, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]discovery.Peer(nil), f.found...), f.scanned
}

// Scan rescans the tailnet and refreshes what's remembered about each helper.
func (f *Fleet) Scan(ctx context.Context) error {
	f.scanMu.Lock() // one scan at a time
	defer f.scanMu.Unlock()
	st, err := f.TS.Status(ctx)
	if err != nil {
		return err
	}
	found := discovery.Scan(ctx, st, f.Prober)
	for _, p := range found {
		if p.Kind != discovery.KindHelper {
			continue
		}
		f.remember(ctx, p)
	}
	f.mu.Lock()
	f.found, f.scanned = found, f.Now()
	f.mu.Unlock()
	return nil
}

func (f *Fleet) remember(ctx context.Context, p discovery.Peer) {
	rec := store.Peer{Name: p.Name, Device: p.Device, DNSName: p.DNSName, Address: p.Address, Port: p.Port, Pin: p.Pin, OS: p.OS, LastSeen: f.Now()}
	if old, ok := f.Store.Peer(p.Name); ok && old.Pin != "" && old.Pin != p.Pin {
		// A new certificate is expected after a reinstall. Accept it only from the same tailnet node
		// (WireGuard vouches for that); a different node taking over the name is ignored.
		if old.DNSName == "" || !strings.EqualFold(old.DNSName, p.DNSName) {
			f.Log.Warn("a different node now uses this name; ignoring it", "peer", p.Name)
			return
		}
		f.Log.Warn("peer certificate changed (reinstalled?); accepting it from the same node", "peer", p.Name)
	}
	var st struct {
		Network netinfo.Network `json:"network"`
		Agents  *struct {
			Allowed bool `json:"allowed"`
		} `json:"agents"`
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := f.Call(cctx, rec, "GET", "/v2/status", nil, &st, ""); err == nil {
		rec.MAC, rec.LAN = st.Network.MAC, st.Network.LAN
		rec.NoAgents = st.Agents != nil && !st.Agents.Allowed
	} else {
		f.Log.Info("peer status unavailable", "peer", p.Name, "err", err)
		if old, ok := f.Store.Peer(p.Name); ok {
			rec.NoAgents = old.NoAgents
		}
	}
	if err := f.Store.PutPeer(rec); err != nil {
		f.Log.Warn("remember peer", "peer", p.Name, "err", err)
	}
}

// Loop rescans every interval until ctx ends. kick triggers an early scan.
func (f *Fleet) Loop(ctx context.Context, interval time.Duration, kick <-chan struct{}) {
	for {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := f.Scan(sctx); err != nil {
			f.Log.Info("tailnet scan failed", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-kick:
		}
	}
}

var (
	ErrUnknownDevice = errors.New("unknown device")
	ErrNoWaker       = errors.New("this helper isn't on the device's network, so it can't wake it")
	ErrNoMAC         = errors.New("the device's network card isn't known yet (it must have been online once while this helper was running)")
	ErrOffline       = errors.New("the device isn't answering")
)

// Peer returns a remembered helper.
func (f *Fleet) Peer(name string) (store.Peer, error) {
	p, ok := f.Store.Peer(name)
	if !ok {
		return store.Peer{}, ErrUnknownDevice
	}
	return p, nil
}

// Wake sends a magic packet for a remembered helper, if it's on this helper's LAN.
func (f *Fleet) Wake(name string) ([]string, error) {
	p, err := f.Peer(name)
	if err != nil {
		return nil, err
	}
	if p.MAC == "" {
		return nil, ErrNoMAC
	}
	own, ok := f.Network()
	if !ok || !netinfo.Overlaps(own.LAN, p.LAN) {
		return nil, ErrNoWaker
	}
	return f.Deps.Wake(p.MAC, own.LAN)
}

// Forward calls a remembered helper's API with this helper's peer key for it, on behalf of who.
func (f *Fleet) Forward(ctx context.Context, name, method, path string, body, out any, who string) error {
	p, err := f.Peer(name)
	if err != nil {
		return err
	}
	err = f.Call(ctx, p, method, path, body, out, who)
	var pe *peer.Error
	if err != nil && !errors.As(err, &pe) {
		return ErrOffline
	}
	return err
}

// Names lists remembered helpers.
func (f *Fleet) Names() []string {
	var out []string
	for _, p := range f.Store.Peers() {
		out = append(out, p.Name)
	}
	return out
}

// IsSelf reports whether name is this device.
func IsSelf(name, self string) bool { return strings.EqualFold(name, self) }
