package machines

import (
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// What a look at a machine can see.
const (
	Answers     = "answers"     // helper: it replied
	Silent      = "silent"      // helper: no reply; ping: no reply
	Refuses     = "refuses"     // port: the OS turned the connection away, so it's running
	Accepts     = "accepts"     // port: connected, but the helper behind it said nothing
	NoReply     = "no_reply"    // port, ping: nothing came back
	Replies     = "replies"     // ping: the machine's network stack answered
	Unavailable = "unavailable" // ping: the hub can't ping it (no Tailscale here or there)
	On          = "on"          // led
	Off         = "off"         // led
	Blinking    = "blinking"    // led: on and off within one look (asleep, on many boards)
	Unreadable  = "unreadable"  // led: the KVM didn't say
)

// Machine states.
const (
	StateOn      = "on"
	StateOff     = "off"
	StateHung    = "hung"
	StateUnknown = "unknown"
)

// Why a machine that isn't answering isn't called hung (Machine.Reason).
const (
	ReasonNoHelper      = "no_helper"       // nothing on it can say it's fine
	ReasonLEDNotTrusted = "led_not_trusted" // the light's wiring isn't known yet
	ReasonLEDUnreadable = "led_unreadable"
	ReasonHelperStopped = "helper_stopped" // the network answers: the machine runs, its helper doesn't
	ReasonBooting       = "booting"
	ReasonShuttingDown  = "shutting_down"
	ReasonRestarting    = "restarting"
	ReasonUpdating      = "updating" // installing Windows updates during a restart
	ReasonMayBeAsleep   = "may_be_asleep"
	ReasonChecking      = "checking" // silent for less than HungAfter
)

// Evidence is what one look at a machine saw.
type Evidence struct {
	At         time.Time `json:"at"`
	Helper     string    `json:"helper,omitempty"` // answers, silent; absent without a helper
	Port       string    `json:"port,omitempty"`   // the helper's port: refuses, accepts, no_reply
	Ping       string    `json:"ping,omitempty"`   // replies, no_reply, unavailable
	LED        string    `json:"led,omitempty"`    // on, off, blinking, unreadable
	LEDTrusted bool      `json:"ledTrusted"`
	LEDError   string    `json:"ledError,omitempty"`
	// Tailnet is whether Tailscale's control plane lists the machine online. It never decides: a
	// hung PC stays "online" there for minutes.
	Tailnet *bool `json:"tailnetOnline,omitempty"`
}

// Word is what the machine's helper last said about itself.
type Word struct {
	At             time.Time    // when it last answered
	PendingAction  string       // a shutdown or restart counting down
	PendingAt      time.Time    // when it's due
	UpdatesPending bool         // Windows updates install at the next restart or shutdown
	Sleep          *store.Sleep // whether it sleeps on its own
}

// Note is something a machine's helper told the hub just before it happened.
type Note struct {
	At      time.Time `json:"at"`
	Event   string    `json:"event"`             // sleep, shutdown, restart, resume
	Updates bool      `json:"updates,omitempty"` // Windows updates will install
}

// Note events.
var NoteEvents = []string{"sleep", "shutdown", "restart", "resume"}

// Rules are the detector's timings, and what it knows about the machine's light.
type Rules struct {
	HungAfter time.Duration // how long every look must agree (3 min)
	Boot      time.Duration // grace after the machine was started, or a shutdown or restart began (10 min)
	Updating  time.Duration // the same with Windows updates to install (90 min)
	LEDAsleep string        // what the light does while the machine sleeps; "" = not seen yet
}

// DefaultRules are the timings unless the owner changes HungAfter.
var DefaultRules = Rules{HungAfter: 3 * time.Minute, Boot: 10 * time.Minute, Updating: 90 * time.Minute}

// Track is what the detector remembers about one machine between looks.
type Track struct {
	State    string
	Reason   string
	Since    time.Time // when State began
	Asleep   bool      // off because it sleeps
	Evidence Evidence  // the last look
	// Suspect is the first look of the current run in which the machine looked hung.
	Suspect time.Time
	LastLED string    // the last reading that said something (on, off or blinking)
	LEDOnAt time.Time // when the light last came on
	Acted   time.Time // when the hub last started, restarted, reset or forced it off
	Word    Word
	Note    *Note // the newest note since the helper last answered
}

// Step takes one look into account and reports whether the state changed (from what).
//
// A machine is hung when, for HungAfter, every look saw its helper silent, its network silent (no
// ping reply, the helper's port not refusing) and a trusted power light steadily on, and nothing
// explains it: starting, shutting down, restarting, installing updates or maybe asleep.
func (t *Track) Step(e Evidence, r Rules) (from string, changed bool) {
	now := e.At
	if e.LED == On && (t.LastLED == Off || t.LastLED == Blinking) {
		t.LEDOnAt = now
	}
	if e.LED == On || e.LED == Off || e.LED == Blinking {
		t.LastLED = e.LED
	}
	if e.Helper == Answers {
		t.Note = nil // whatever it announced is over
	}
	if !t.Suspect.IsZero() && !t.Evidence.At.IsZero() && now.Sub(t.Evidence.At) > r.HungAfter {
		t.Suspect = time.Time{} // the hub looked away too long (it was off, or restarting) to count on
	}
	state, reason, asleep := t.decide(e, r)
	t.Evidence = e
	from = t.State
	t.Reason, t.Asleep = reason, asleep
	if state == t.State {
		return from, false
	}
	t.State, t.Since = state, now
	return from, true
}

func (t *Track) decide(e Evidence, r Rules) (state, reason string, asleep bool) {
	now := e.At
	switch {
	case e.Helper == Answers:
		t.Suspect = time.Time{}
		return StateOn, "", false
	case e.LEDTrusted && (e.LED == Off || e.LED == Blinking):
		t.Suspect = time.Time{}
		return StateOff, "", e.LED == Blinking || t.Note != nil && t.Note.Event == "sleep"
	case e.Helper == "":
		t.Suspect = time.Time{}
		return StateUnknown, ReasonNoHelper, false
	case !e.LEDTrusted:
		t.Suspect = time.Time{}
		return StateUnknown, ReasonLEDNotTrusted, false
	case e.Ping == Replies || e.Port == Refuses:
		t.Suspect = time.Time{}
		return StateUnknown, ReasonHelperStopped, false
	case e.LED != On: // unreadable: it neither counts nor clears
		if t.State == StateHung {
			return StateHung, "", false
		}
		return StateUnknown, ReasonLEDUnreadable, false
	}
	// Helper and network silent, light steadily on.
	if why := t.excuse(now, r); why != "" {
		t.Suspect = time.Time{}
		return StateUnknown, why, false
	}
	if t.Suspect.IsZero() {
		t.Suspect = now
	}
	if now.Sub(t.Suspect) >= r.HungAfter {
		return StateHung, "", false
	}
	return StateUnknown, ReasonChecking, false
}

// excuse is a harmless reason for a silent machine with its light on, or "".
func (t *Track) excuse(now time.Time, r Rules) string {
	within := func(since time.Time, d time.Duration) bool { return !since.IsZero() && now.Sub(since) < d }
	if within(t.Acted, r.Boot) || within(t.LEDOnAt, r.Boot) {
		return ReasonBooting
	}
	// A shutdown or restart it announced, or that was counting down when it last answered.
	action, at, updates := "", time.Time{}, t.Word.UpdatesPending
	if n := t.Note; n != nil && (n.Event == "shutdown" || n.Event == "restart") {
		action, at, updates = n.Event, n.At, updates || n.Updates
	}
	if t.Word.PendingAction != "" && t.Word.PendingAt.After(at) {
		action, at = t.Word.PendingAction, t.Word.PendingAt
	}
	if action != "" {
		switch {
		case updates && within(at, r.Updating):
			return ReasonUpdating
		case within(at, r.Boot) || at.After(now):
			if action == "restart" {
				return ReasonRestarting
			}
			return ReasonShuttingDown
		}
	}
	if t.Note != nil && t.Note.Event == "sleep" {
		return ReasonMayBeAsleep
	}
	// It sleeps on its own, and its light may stay on while it does.
	if s := t.Word.Sleep; s != nil && s.AfterMinutes > 0 && r.LEDAsleep != Off && r.LEDAsleep != Blinking {
		return ReasonMayBeAsleep
	}
	return ""
}
