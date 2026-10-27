// Package power schedules shutdowns and restarts and tracks the one that is pending.
package power

import (
	"errors"
	"sync"
	"time"
)

// Action is a pending power action.
type Action string

const (
	Shutdown Action = "shutdown"
	Restart  Action = "restart"
)

var (
	ErrPending        = errors.New("a shutdown or restart is already pending")
	ErrNothingPending = errors.New("nothing is pending")
)

// System is the OS-specific part: schedule with a grace period (showing the OS warning) or abort.
type System interface {
	Schedule(a Action, delay time.Duration, message string) error
	Abort() error
	BootTime() (time.Time, error)
}

// Pending describes a scheduled action.
type Pending struct {
	Action Action    `json:"action"`
	At     time.Time `json:"at"`
}

// Controller serialises power actions.
type Controller struct {
	mu      sync.Mutex
	sys     System
	now     func() time.Time
	pending *Pending
}

func NewController(sys System, now func() time.Time) *Controller {
	return &Controller{sys: sys, now: now}
}

// Do schedules a shutdown or restart after delay.
func (c *Controller) Do(a Action, delay time.Duration, requestedBy string) (Pending, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.pendingLocked(); p != nil {
		return *p, ErrPending
	}
	verb := "shut down"
	if a == Restart {
		verb = "restart"
	}
	msg := "PowerDeck: " + requestedBy + " asked this PC to " + verb + ". Open PowerDeck on the phone to cancel."
	if err := c.sys.Schedule(a, delay, msg); err != nil {
		return Pending{}, err
	}
	c.pending = &Pending{Action: a, At: c.now().Add(delay).UTC()}
	return *c.pending, nil
}

// Cancel aborts the pending action, including one scheduled outside PowerDeck.
func (c *Controller) Cancel() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.sys.Abort()
	if err == nil || err == ErrNothingPending {
		c.pending = nil
	}
	return err
}

// Pending returns the scheduled action, if any.
func (c *Controller) Pending() *Pending {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.pendingLocked(); p != nil {
		cp := *p
		return &cp
	}
	return nil
}

func (c *Controller) pendingLocked() *Pending {
	// Allow a few seconds past the deadline: the OS is going down by then.
	if c.pending != nil && c.now().After(c.pending.At.Add(15*time.Second)) {
		c.pending = nil
	}
	return c.pending
}

// Uptime returns time since boot (zero if unknown).
func (c *Controller) Uptime() (time.Duration, time.Time) {
	bt, err := c.sys.BootTime()
	if err != nil {
		return 0, time.Time{}
	}
	return c.now().Sub(bt), bt
}
