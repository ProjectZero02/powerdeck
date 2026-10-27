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
	"os"
	"path/filepath"
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
)

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
	DNSName  string    `json:"dnsName,omitempty"`
	Address  string    `json:"address"`
	Port     int       `json:"port"`
	Pin      string    `json:"pin"`
	OS       string    `json:"os,omitempty"`
	MAC      string    `json:"mac,omitempty"`
	LAN      []string  `json:"lan,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
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
}

type state struct {
	Keys      []Key           `json:"keys"`
	Settings  Settings        `json:"settings"`
	Events    []Event         `json:"events"`
	Peers     map[string]Peer `json:"peers"`
	Schedules []Schedule      `json:"schedules"`
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
	s := &Store{path: filepath.Join(dir, "state.json"), st: state{Settings: Settings{AutoUpdate: true}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.st.Peers = map[string]Peer{}
		return s, s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, err
	}
	if s.st.Peers == nil {
		s.st.Peers = map[string]Peer{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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
func (s *Store) BindKey(id, node string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Keys {
		if s.st.Keys[i].ID == id {
			if s.st.Keys[i].BoundNode == "" {
				s.st.Keys[i].BoundNode = node
				return s.saveLocked()
			}
			return nil
		}
	}
	return ErrNotFound
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
	out := slices.Clone(s.st.Events)
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
		if strings.EqualFold(n, name) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Settings.AutoUpdate = on
	return s.saveLocked()
}
