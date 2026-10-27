// Package power schedules shutdowns and restarts, tracks the one that is pending, and refuses to
// act while someone is using the machine unless told to.
package power

import (
	"errors"
	"sync"
	"time"
)

// Action is a power action on this machine.
type Action string

const (
	Shutdown Action = "shutdown"
	Restart  Action = "restart"
)

var (
	ErrPending        = errors.New("a shutdown or restart is already pending")
	ErrNothingPending = errors.New("nothing is pending")
	ErrInUse          = errors.New("someone is using this device")
	ErrUpdating       = errors.New("the helper is installing an update")
)

// ActiveWithin is how recent input must be for a user to count as active.
const ActiveWithin = 5 * time.Minute

// Presence is who's using the machine right now, as far as the helper can tell.
type Presence struct {
	Active      bool   `json:"active"`
	User        string `json:"user,omitempty"`
	IdleSeconds int    `json:"idleSeconds"`
	Fullscreen  bool   `json:"fullscreen,omitempty"`
	Locked      bool   `json:"locked,omitempty"`
	Sessions    int    `json:"sessions,omitempty"` // Linux: logged-in sessions
}

// System is the OS side.
type System interface {
	// Schedule starts a shutdown or restart after delay, telling logged-in users message.
	Schedule(a Action, delay time.Duration, message string, installUpdates bool) error
	// Abort cancels a scheduled shutdown or restart.
	Abort() error
	BootTime() (time.Time, error)
	// Presence returns nil when the helper can't tell.
	Presence() *Presence
	// UpdatesPending reports OS updates that a shutdown could install (Windows).
	UpdatesPending() bool
}

// Pending is the scheduled action.
type Pending struct {
	Action Action    `json:"action"`
	At     time.Time `json:"at"`
	By     string    `json:"by"`
}

// Options for Do.
type Options struct {
	Delay          time.Duration
	By             string // who asked, shown to logged-in users and in the log
	Force          bool   // act even while someone is using the machine
	InstallUpdates bool
}

// Controller is safe for concurrent use.
type Controller struct {
	mu       sync.Mutex
	sys      System
	now      func() time.Time
	pending  *Pending
	updating time.Time // set while an update restarts the helper; expires on its own
}

// updateHold is how long BeginUpdate holds off new shutdowns and restarts if the update never
// finishes (the new helper starts with a fresh controller anyway).
const updateHold = 10 * time.Minute

// BeginUpdate claims the device for an update restart: it says why not if a countdown is pending,
// and otherwise makes Do refuse until EndUpdate or the hold runs out, so no countdown is accepted
// and then lost to the restart.
func (c *Controller) BeginUpdate() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingLocked() != nil {
		return "a shutdown or restart is counting down"
	}
	c.updating = c.now()
	return ""
}

// EndUpdate releases BeginUpdate's claim (the installer didn't start).
func (c *Controller) EndUpdate() {
	c.mu.Lock()
	c.updating = time.Time{}
	c.mu.Unlock()
}

func NewController(sys System, now func() time.Time) *Controller {
	return &Controller{sys: sys, now: now}
}

// InUse returns the presence when someone is actively using the machine.
func (c *Controller) InUse() (*Presence, bool) {
	p := c.sys.Presence()
	if p != nil && p.Active {
		return p, true
	}
	return p, false
}

// Do schedules a shutdown or restart.
func (c *Controller) Do(a Action, o Options) (Pending, error) {
	if !o.Force {
		if _, busy := c.InUse(); busy {
			return Pending{}, ErrInUse
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.pendingLocked(); p != nil {
		return *p, ErrPending
	}
	if !c.updating.IsZero() && c.now().Sub(c.updating) < updateHold {
		return Pending{}, ErrUpdating
	}
	verb := "shut down"
	if a == Restart {
		verb = "restart"
	}
	msg := "PowerDeck: " + o.By + " asked this device to " + verb + ". Cancel it in the PowerDeck app."
	if err := c.sys.Schedule(a, o.Delay, msg, o.InstallUpdates); err != nil {
		return Pending{}, err
	}
	c.pending = &Pending{Action: a, At: c.now().Add(o.Delay).UTC(), By: o.By}
	return *c.pending, nil
}

// Cancel aborts the pending action.
func (c *Controller) Cancel() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.sys.Abort()
	if err == nil || errors.Is(err, ErrNothingPending) {
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

// Presence is the OS's current presence report.
func (c *Controller) Presence() *Presence { return c.sys.Presence() }

// UpdatesPending reports OS updates a shutdown could install.
func (c *Controller) UpdatesPending() bool { return c.sys.UpdatesPending() }
