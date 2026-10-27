package power

import (
	"sync"
	"time"
)

// Reports holds the latest presence report from a user-session companion. Reports older than 90
// seconds are ignored (the companion sends one every 20 s while the user is signed in).
type Reports struct {
	mu   sync.Mutex
	last *Presence
	at   time.Time
	Now  func() time.Time
}

func (r *Reports) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Put stores a report.
func (r *Reports) Put(p Presence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.Active = !p.Locked && (p.Fullscreen || time.Duration(p.IdleSeconds)*time.Second < ActiveWithin)
	r.last, r.at = &p, r.now()
}

// Fresh returns the latest report if it's recent.
func (r *Reports) Fresh() *Presence {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil || r.now().Sub(r.at) > 90*time.Second {
		return nil
	}
	cp := *r.last
	return &cp
}
