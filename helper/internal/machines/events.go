package machines

import (
	"strconv"
	"sync"
	"time"
)

// Event is a machine's state changing, as the hub saw it, or a recovery's.
type Event struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	Event    string    `json:"event"` // the new state, "recovered" (hung, then answering) or "recovery"
	Machine  string    `json:"machine"`
	State    string    `json:"state,omitempty"`
	From     string    `json:"from,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Asleep   bool      `json:"asleep,omitempty"`
	Evidence Evidence  `json:"evidence,omitzero"`
	Recovery *Recovery `json:"recovery,omitempty"` // a recovery event: the recovery as it is now
}

// EventRecovery is a recovery's state changing.
const EventRecovery = "recovery"

// EventNames are what an event can be (webhook filters use them).
var EventNames = []string{StateOn, StateOff, StateHung, StateUnknown, "recovered", EventRecovery}

// maxHeld is how many events the bus keeps for replay.
const maxHeld = 500

// Bus keeps the last events for replay and hands new ones to subscribers. IDs are numbers that
// keep growing across restarts (they start from the clock), so an ID from before a restart is
// simply too old to replay.
type Bus struct {
	mu   sync.Mutex
	next int64
	held []Event
	subs map[chan Event]bool
}

// NewBus starts event IDs from now.
func NewBus(now time.Time) *Bus {
	return &Bus{next: now.UnixMicro(), subs: map[chan Event]bool{}}
}

// Publish gives e an ID, keeps it and sends it to every subscriber. A subscriber that isn't
// keeping up is cut off (its channel closed); it can come back and replay from its last ID.
func (b *Bus) Publish(e Event) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	e.ID = strconv.FormatInt(b.next, 10)
	b.next++
	b.held = append(b.held, e)
	if len(b.held) > maxHeld {
		b.held = append([]Event(nil), b.held[len(b.held)-maxHeld:]...)
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
	return e
}

// After returns the events after id, oldest first. ok is false when id isn't one this bus can
// replay from (unknown, or older than what it still holds).
func (b *Bus) After(id string) (events []Event, ok bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	first := b.next
	if len(b.held) > 0 {
		first, _ = strconv.ParseInt(b.held[0].ID, 10, 64)
	}
	if err != nil || n < first-1 || n >= b.next {
		return nil, false
	}
	for _, e := range b.held {
		if v, _ := strconv.ParseInt(e.ID, 10, 64); v > n {
			events = append(events, e)
		}
	}
	return events, true
}

// Recent returns up to n of the newest events, oldest first.
func (b *Bus) Recent(n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.held) {
		n = len(b.held)
	}
	return append([]Event(nil), b.held[len(b.held)-n:]...)
}

// Subscribe returns a channel of new events and a function to stop. The channel is closed when the
// subscriber falls more than buf events behind.
func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	ch := make(chan Event, buf)
	b.mu.Lock()
	b.subs[ch] = true
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.subs[ch] {
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// Subscribers counts current subscribers.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
