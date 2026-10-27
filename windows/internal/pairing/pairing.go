// Package pairing manages the one-time code shown in the pairing QR code.
//
// One code is active at a time. It expires after TTL, is consumed by the first successful pairing,
// and is discarded after MaxFailures wrong guesses, so it can't be brute-forced.
package pairing

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"strings"
	"sync"
	"time"
)

const (
	TTL         = 10 * time.Minute
	MaxFailures = 5
)

// Code is the active pairing code.
type Code struct {
	Value   string
	Expires time.Time
}

// Manager is safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	cur      *Code
	failures int
	now      func() time.Time
}

func NewManager(now func() time.Time) *Manager { return &Manager{now: now} }

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

func newValue() string {
	b := make([]byte, 16) // 128 bits
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(enc.EncodeToString(b))
}

// Current returns the active code, creating a fresh one when none is valid.
func (m *Manager) Current() Code {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || !m.now().Before(m.cur.Expires) {
		m.renewLocked()
	}
	return *m.cur
}

// Renew discards the active code and creates a new one.
func (m *Manager) Renew() Code {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renewLocked()
	return *m.cur
}

func (m *Manager) renewLocked() {
	m.cur = &Code{Value: newValue(), Expires: m.now().Add(TTL)}
	m.failures = 0
}

// Consume checks a code presented by a phone. A match consumes it; too many misses discard it.
func (m *Manager) Consume(value string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || !m.now().Before(m.cur.Expires) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(value)), []byte(m.cur.Value)) == 1 {
		m.cur = nil
		return true
	}
	m.failures++
	if m.failures >= MaxFailures {
		m.cur = nil
	}
	return false
}
