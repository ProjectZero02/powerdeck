// Package store persists PowerDeck's state (paired phones and settings) as JSON in the data directory.
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
	"sort"
	"sync"
	"time"
)

// Device is a paired phone. Only a hash of its token is stored.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Model     string    `json:"model,omitempty"`
	TokenHash string    `json:"tokenHash"`
	PairedAt  time.Time `json:"pairedAt"`
	LastSeen  time.Time `json:"lastSeen,omitempty"`
}

// Settings are user-adjustable options.
type Settings struct {
	AutoUpdate bool `json:"autoUpdate"`
}

type state struct {
	Devices  []Device `json:"devices"`
	Settings Settings `json:"settings"`
}

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	st   state
}

const maxDevices = 20

var (
	ErrTooManyDevices = errors.New("too many paired devices")
	ErrNotFound       = errors.New("device not found")
)

// Open loads (or initialises) the state file in dir.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "state.json"), st: state{Settings: Settings{AutoUpdate: true}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, err
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

// HashToken returns the stored form of a device token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// AddDevice registers a phone and returns it with its one-time plaintext token.
func (s *Store) AddDevice(name, model string, now time.Time) (Device, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Devices) >= maxDevices {
		return Device{}, "", ErrTooManyDevices
	}
	token := randomString(32)
	d := Device{ID: randomString(9), Name: name, Model: model, TokenHash: HashToken(token), PairedAt: now.UTC()}
	s.st.Devices = append(s.st.Devices, d)
	return d, token, s.saveLocked()
}

// Authenticate finds the device that owns token and records it as seen.
func (s *Store) Authenticate(token string, now time.Time) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	h := []byte(HashToken(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Devices {
		d := &s.st.Devices[i]
		if subtle.ConstantTimeCompare(h, []byte(d.TokenHash)) == 1 {
			// Persist last-seen at most once a minute to avoid constant disk writes.
			if now.Sub(d.LastSeen) > time.Minute {
				d.LastSeen = now.UTC()
				_ = s.saveLocked()
			}
			return *d, true
		}
	}
	return Device{}, false
}

// RemoveDevice unpairs a phone.
func (s *Store) RemoveDevice(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.st.Devices {
		if d.ID == id {
			s.st.Devices = append(s.st.Devices[:i], s.st.Devices[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// Devices returns paired phones, most recently paired first.
func (s *Store) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Device(nil), s.st.Devices...)
	sort.Slice(out, func(i, j int) bool { return out[i].PairedAt.After(out[j].PairedAt) })
	return out
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
