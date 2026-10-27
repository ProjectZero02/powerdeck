//go:build !windows

package power

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// DryRun logs power actions instead of performing them. Non-Windows builds exist only for tests and
// development, so this is the only implementation there.
type DryRun struct {
	Log     *slog.Logger
	pending bool
}

func (d *DryRun) Schedule(a Action, delay time.Duration, message string) error {
	if d.pending {
		return ErrPending
	}
	d.pending = true
	d.Log.Info("dry-run: would schedule", "action", a, "delay", delay, "message", message)
	return nil
}

func (d *DryRun) Abort() error {
	if !d.pending {
		return ErrNothingPending
	}
	d.pending = false
	d.Log.Info("dry-run: would abort pending shutdown")
	return nil
}

func (d *DryRun) BootTime() (time.Time, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}, err
	}
	secs, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(-time.Duration(secs * float64(time.Second))), nil
}
