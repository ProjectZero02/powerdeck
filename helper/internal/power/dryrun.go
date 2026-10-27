package power

import (
	"log/slog"
	"sync"
	"time"
)

// DryRun logs power actions instead of performing them (development, demos).
type DryRun struct {
	Log     *slog.Logger
	mu      sync.Mutex
	pending bool
}

func (d *DryRun) Schedule(a Action, delay time.Duration, message string, installUpdates bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending {
		return ErrPending
	}
	d.pending = true
	d.Log.Info("dry-run: would schedule", "action", a, "delay", delay, "message", message, "installUpdates", installUpdates)
	return nil
}

func (d *DryRun) Abort() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pending {
		return ErrNothingPending
	}
	d.pending = false
	d.Log.Info("dry-run: would abort pending action")
	return nil
}

func (d *DryRun) BootTime() (time.Time, error) { return time.Now().Add(-3 * time.Hour), nil }
func (d *DryRun) Presence() *Presence          { return &Presence{} }
func (d *DryRun) UpdatesPending() bool         { return false }
