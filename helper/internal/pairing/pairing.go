// Package pairing manages the one-time code a helper shows for callers it can't trust by their
// Tailscale identity (another Tailscale account, or a helper on a tagged node).
//
// One code is active at a time: 8 Crockford base32 characters (40 bits), shown as XXXX-XXXX. It
// expires after TTL, is consumed by the first successful pairing, and is discarded after
// MaxFailures wrong guesses, so guessing it is hopeless (5 tries against 2^40).
package pairing

import (
	"crypto/rand"
	"crypto/subtle"
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
	Value   string // XXXX-XXXX
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

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford: no I, L, O, U

func newValue() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, c := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[c&31])
	}
	return string(out)
}

// Normalize maps what a person types to the canonical form: case, spaces and dashes don't matter,
// and the look-alikes O/I/L read as 0/1/1.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case ' ', '-':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	v := b.String()
	if len(v) == 8 {
		v = v[:4] + "-" + v[4:]
	}
	return v
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

// Consume checks a presented code. A match consumes it; too many misses discard it.
func (m *Manager) Consume(value string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || !m.now().Before(m.cur.Expires) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(Normalize(value)), []byte(m.cur.Value)) == 1 {
		m.cur = nil
		return true
	}
	m.failures++
	if m.failures >= MaxFailures {
		m.cur = nil
	}
	return false
}
