// Package store keeps PowerDeck's state in one JSON file in the data directory: issued keys (only
// their SHA-256), settings, the event log and what this helper knows about other PowerDeck devices.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Key scopes.
const (
	ScopeStatus = "status"
	ScopeWake   = "wake"
	ScopePower  = "power"
	ScopeForce  = "force"
	ScopeAdmin  = "admin"
)

// AllScopes are what an owner (or a pairing-code key) gets.
var AllScopes = []string{ScopeStatus, ScopeWake, ScopePower, ScopeForce, ScopeAdmin}

// Key kinds.
const (
	KindPhone      = "phone"      // issued through a pairing code
	KindAutomation = "automation" // created by an owner for an agent or script
	KindPeer       = "peer"       // held by another PowerDeck helper of the same owner, to reach this one
)

// PeerScopes are what a peer key may do: see a device, wake it, shut it down or restart it.
var PeerScopes = []string{ScopeStatus, ScopeWake, ScopePower}

// PhoneScopes are what a pairing code grants a phone on another Tailscale account: everything on
// this one device except managing it (keys, settings, owners, updates, uninstall).
var PhoneScopes = []string{ScopeStatus, ScopeWake, ScopePower, ScopeForce}

// Owner is a device that controls this helper fully: a phone or computer recorded by pairing.
type Owner struct {
	NodeID  string    `json:"nodeId"` // Tailscale stable node ID
	Name    string    `json:"name"`   // its name when it was added
	AddedAt time.Time `json:"addedAt"`
	AddedBy string    `json:"addedBy"` // "pairing code", "this device" or "upgrade"
}

// Owner modes.
const (
	// OwnersLegacy: a helper from before owner devices. Any untagged node of this device's
	// Tailscale user is an owner, until the first owner device is recorded.
	OwnersLegacy = ""
	// OwnersExplicit: only recorded owner devices (and this machine's own tools) are owners.
	OwnersExplicit = "explicit"
)

// DefaultAutoUpdate is the automatic-update setting of a new install: on for desktops, off for
// Linux, where PowerDeck mostly runs on servers that should be updated by hand.
var DefaultAutoUpdate = runtime.GOOS != "linux"

// Key is an issued API key.
type Key struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Hash      string    `json:"hash"`
	Scopes    []string  `json:"scopes"`
	Devices   []string  `json:"devices,omitempty"`   // relay targets this key may use; empty = all
	AllowSelf bool      `json:"allowSelf,omitempty"` // may power off the device the caller runs on
	BoundNode string    `json:"boundNode,omitempty"` // tailnet node that first used it (automation keys)
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed,omitempty"`
}

// Has reports whether the key has a scope.
func (k Key) Has(scope string) bool { return slices.Contains(k.Scopes, scope) }

// MayUse reports whether the key may act on the named device.
func (k Key) MayUse(device string) bool {
	return len(k.Devices) == 0 || slices.ContainsFunc(k.Devices, func(d string) bool { return strings.EqualFold(d, device) })
}

// Event is one entry of the activity log.
type Event struct {
	At     time.Time `json:"at"`
	Who    string    `json:"who"`
	Action string    `json:"action"`
	Result string    `json:"result"`
}

// Peer is another PowerDeck helper this one has seen, remembered so it can still be woken (and
// trusted by pin) while it's off.
type Peer struct {
	Name     string    `json:"name"`
	Device   string    `json:"device,omitempty"` // its own name, which the app and schedules may use`
	DNSName  string    `json:"dnsName,omitempty"`
	Address  string    `json:"address"`
	Port     int       `json:"port"`
	Pin      string    `json:"pin"`
	OS       string    `json:"os,omitempty"`
	MAC      string    `json:"mac,omitempty"`
	LAN      []string  `json:"lan,omitempty"`
	NoAgents bool      `json:"noAgents,omitempty"` // it turns agents away (last we heard)
	LastSeen time.Time `json:"lastSeen"`
	// Key is the peer key this helper presents to it (set up by the owner's phone).
	Key string `json:"key,omitempty"`
}

// Schedule is a recurring power action run by this helper.
type Schedule struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`           // shutdown, restart or wake
	Target      string   `json:"target,omitempty"` // wake: a known peer's name
	Time        string   `json:"time"`             // HH:MM, local time
	Days        []string `json:"days"`             // mon … sun
	SkipIfInUse bool     `json:"skipIfInUse"`
	Enabled     bool     `json:"enabled"`
}

// Settings are user preferences.
type Settings struct {
	AutoUpdate bool `json:"autoUpdate"`
	// NoAgents turns AI agents away from this device (the zero value allows them).
	NoAgents bool `json:"noAgents,omitempty"`
	// Tray shows PowerDeck's icon in the Windows notification area.
	Tray bool `json:"tray,omitempty"`
	// SetupDone records that the PC window's first-run setup was finished or skipped.
	SetupDone bool `json:"setupDone,omitempty"`
	// UpdatesPausedUntil holds automatic updates back until then.
	UpdatesPausedUntil time.Time `json:"updatesPausedUntil,omitzero"`
}

type state struct {
	Keys      []Key           `json:"keys"`
	Settings  Settings        `json:"settings"`
	Events    []Event         `json:"events"`
	Peers     map[string]Peer `json:"peers"`
	Schedules []Schedule      `json:"schedules"`
	Owners    []Owner         `json:"owners,omitempty"`
	OwnerMode string          `json:"ownerMode,omitempty"`
}

// fresh is a new install's state.
func fresh() state {
	return state{Settings: Settings{AutoUpdate: DefaultAutoUpdate}, OwnerMode: OwnersExplicit}
}

const (
	maxKeys   = 50
	maxEvents = 200
	KeyPrefix = "pdk_"
)

var (
	ErrTooManyKeys = errors.New("too many keys")
	ErrNotFound    = errors.New("not found")
)

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	st   state
}

// Open loads (or creates) state.json in dir.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "state.json")}
	err := s.load(s.path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		// Never set up, or deliberately reset: start fresh, and don't let an old backup come back later.
		_ = os.Remove(s.path + ".bak")
		s.st = fresh()
	case isCorrupt(err):
		// Cut short (e.g. by a power loss): keep it aside and fall back to the last good copy, or start
		// fresh, rather than leave the device unreachable.
		_ = os.Rename(s.path, s.path+".broken")
		if s.load(s.path+".bak") != nil {
			s.st = fresh() // nothing to fall back on; nobody is an owner until paired again
		}
	default:
		return nil, err // can't read it (permissions, disk): don't guess
	}
	if s.st.Peers == nil {
		s.st.Peers = map[string]Peer{}
	}
	if err != nil {
		return s, s.saveLocked()
	}
	return s, nil
}

func isCorrupt(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syn) || errors.As(err, &typ) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (s *Store) load(path string) error {
	s.st = state{Settings: Settings{AutoUpdate: true}} // files from before the setting had it on
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.st)
}

// saveLocked writes the state durably: a synced temp file renamed over the old one, which is kept
// as state.json.bak in case the new one is lost to a power cut.
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(s.path); err == nil && json.Valid(cur) {
		_ = os.WriteFile(s.path+".bak", cur, 0o600)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(s.path)); err == nil { // make the rename durable (Linux)
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// HashKey is the stored form of a key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewKey describes a key to issue.
type NewKey struct {
	Name      string
	Kind      string
	Scopes    []string
	Devices   []string
	AllowSelf bool
}

// AddKey issues a key and returns it with its secret, which is never stored.
func (s *Store) AddKey(nk NewKey, now time.Time) (Key, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Keys) >= maxKeys {
		return Key{}, "", ErrTooManyKeys
	}
	secret := KeyPrefix + random(32)
	k := Key{ID: random(6), Name: nk.Name, Kind: nk.Kind, Hash: HashKey(secret), Scopes: nk.Scopes,
		Devices: nk.Devices, AllowSelf: nk.AllowSelf, CreatedAt: now.UTC()}
	s.st.Keys = append(s.st.Keys, k)
	return k, secret, s.saveLocked()
}

// Authenticate finds the key for a secret and records its use.
func (s *Store) Authenticate(secret string, now time.Time) (Key, bool) {
	if !strings.HasPrefix(secret, KeyPrefix) {
		return Key{}, false
	}
	h := []byte(HashKey(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Keys {
		k := &s.st.Keys[i]
		if subtle.ConstantTimeCompare(h, []byte(k.Hash)) == 1 {
			if now.Sub(k.LastUsed) > time.Minute {
				k.LastUsed = now.UTC()
				_ = s.saveLocked()
			}
			return *k, true
		}
	}
	return Key{}, false
}

// BindKey records the tailnet node an automation key is used from, the first time.
func (s *Store) BindKey(id, node string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Keys {
		if s.st.Keys[i].ID == id {
			if s.st.Keys[i].BoundNode == "" {
				s.st.Keys[i].BoundNode = node
				return node, s.saveLocked()
			}
			return s.st.Keys[i].BoundNode, nil
		}
	}
	return "", ErrNotFound
}

// RemoveKey revokes a key.
func (s *Store) RemoveKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.st.Keys {
		if k.ID == id {
			s.st.Keys = slices.Delete(s.st.Keys, i, i+1)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// Keys lists keys, newest first.
func (s *Store) Keys() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.st.Keys)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// AddEvent appends to the activity log, keeping the newest maxEvents.
func (s *Store) AddEvent(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.At = e.At.UTC()
	s.st.Events = append(s.st.Events, e)
	if n := len(s.st.Events); n > maxEvents {
		s.st.Events = slices.Clone(s.st.Events[n-maxEvents:])
	}
	_ = s.saveLocked()
}

// Events returns the log, newest first.
func (s *Store) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Event{}, s.st.Events...) // never nil: the PC window expects a list
	slices.Reverse(out)
	return out
}

// PutPeer remembers another helper (keyed by name), keeping its last known MAC and LAN when the
// new record lacks them.
func (s *Store) PutPeer(p Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.st.Peers[p.Name]; ok {
		if p.MAC == "" {
			p.MAC = old.MAC
		}
		if p.Key == "" && old.Pin == p.Pin {
			p.Key = old.Key // a reinstalled peer (new certificate) has new keys too
		}
		if p.LAN == nil {
			p.LAN = old.LAN
		}
	}
	p.LastSeen = p.LastSeen.UTC()
	s.st.Peers[p.Name] = p
	return s.saveLocked()
}

// Peer returns a remembered helper by name (case-insensitive).
func (s *Store) Peer(name string) (Peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for n, p := range s.st.Peers {
		if strings.EqualFold(n, name) || p.Device != "" && strings.EqualFold(p.Device, name) {
			return p, true
		}
	}
	return Peer{}, false
}

// Peers lists remembered helpers by name.
func (s *Store) Peers() []Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Peer, 0, len(s.st.Peers))
	for _, p := range s.st.Peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

const maxSchedules = 20

// ErrTooManySchedules is returned when the schedule list is full.
var ErrTooManySchedules = errors.New("too many schedules")

// AddSchedule stores a schedule and returns it with its new ID.
func (s *Store) AddSchedule(sc Schedule) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Schedules) >= maxSchedules {
		return Schedule{}, ErrTooManySchedules
	}
	sc.ID = random(4)
	s.st.Schedules = append(s.st.Schedules, sc)
	return sc, s.saveLocked()
}

// RemoveSchedule deletes a schedule.
func (s *Store) RemoveSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sc := range s.st.Schedules {
		if sc.ID == id {
			s.st.Schedules = slices.Delete(s.st.Schedules, i, i+1)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// Schedules lists schedules in creation order.
func (s *Store) Schedules() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.st.Schedules)
}

// Settings returns the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Settings
}

// SetAutoUpdate turns automatic updates on or off.
func (s *Store) SetAutoUpdate(on bool) error {
	return s.UpdateSettings(func(st *Settings) { st.AutoUpdate = on })
}

// UpdateSettings changes settings with f and saves them.
func (s *Store) UpdateSettings(f func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.st.Settings)
	return s.saveLocked()
}

// SetPeerKey stores the key this helper presents to a remembered peer ("" removes it).
func (s *Store) SetPeerKey(name, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for n, p := range s.st.Peers {
		if strings.EqualFold(n, name) || p.Device != "" && strings.EqualFold(p.Device, name) {
			p.Key = key
			s.st.Peers[n] = p
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

const maxOwners = 20

// ErrTooManyOwners is returned when the owner list is full.
var ErrTooManyOwners = errors.New("too many owner devices")

// OwnerMode reports OwnersLegacy or OwnersExplicit.
func (s *Store) OwnerMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.OwnerMode
}

// Owners lists owner devices in the order they were added.
func (s *Store) Owners() []Owner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.st.Owners)
}

// IsOwnerNode reports whether a Tailscale node is a recorded owner device.
func (s *Store) IsOwnerNode(nodeID string) bool {
	if nodeID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.st.Owners, func(o Owner) bool { return o.NodeID == nodeID })
}

// AddOwner records an owner device (again: its name is updated) and ends the legacy mode.
func (s *Store) AddOwner(o Owner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o.AddedAt = o.AddedAt.UTC()
	s.st.OwnerMode = OwnersExplicit
	if i := slices.IndexFunc(s.st.Owners, func(x Owner) bool { return x.NodeID == o.NodeID }); i >= 0 {
		s.st.Owners[i].Name = o.Name
		return s.saveLocked()
	}
	if len(s.st.Owners) >= maxOwners {
		return ErrTooManyOwners
	}
	s.st.Owners = append(s.st.Owners, o)
	return s.saveLocked()
}

// RemoveOwner forgets an owner device.
func (s *Store) RemoveOwner(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, o := range s.st.Owners {
		if o.NodeID == nodeID {
			s.st.Owners = slices.Delete(s.st.Owners, i, i+1)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}
