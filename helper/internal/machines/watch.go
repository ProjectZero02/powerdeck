package machines

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/peer"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// Watch looks at every machine with a KVM here every interval (30 s): it tells hung from booting,
// asleep or updating, publishes state changes on Events, and learns the wiring from the light.
func (h *Hub) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		h.WatchOnce(ctx)
		select {
		case <-ctx.Done():
			h.wg.Wait()
			return
		case <-t.C:
		}
	}
}

// WatchOnce takes one look at every machine with a KVM here.
func (h *Hub) WatchOnce(ctx context.Context) {
	var wg sync.WaitGroup
	for _, k := range h.Store.KVMs() {
		wg.Add(1)
		go func(k store.KVM) {
			defer wg.Done()
			h.watch(ctx, k)
		}(k)
	}
	wg.Wait()
}

// lookTime bounds one look: the helper twice, three light readings, a ping and a connection.
const lookTime = 8 * time.Second

func (h *Hub) watch(ctx context.Context, k store.KVM) {
	ctx, cancel := context.WithTimeout(ctx, lookTime)
	defer cancel()
	p, err := h.Fleet.Peer(k.Machine)
	hasHelper := err == nil
	e := Evidence{At: h.now().UTC()}
	var (
		word    *Word
		answers bool
		led     string
		ledErr  error
		wg      sync.WaitGroup
	)
	if hasHelper {
		wg.Add(4)
		go func() { defer wg.Done(); answers, word = h.ask(ctx, k.Machine) }()
		go func() { defer wg.Done(); e.Port = h.port(ctx, p.Address, p.Port) }()
		go func() { defer wg.Done(); e.Ping = h.ping(ctx, p.Address) }()
		go func() { defer wg.Done(); e.Tailnet = h.online(ctx, p.Address) }()
	}
	led, ledErr = h.sampleLED(ctx, k)
	wg.Wait()
	if hasHelper {
		e.Helper = map[bool]string{true: Answers, false: Silent}[answers]
	}
	e.LED = led
	if ledErr != nil {
		e.LEDError = ledErr.Error()
	}
	if led == On || led == Off {
		h.observe(k.Name, answers, led == On)
	}
	if fresh, ok := h.Store.KVM(k.Name); ok {
		k = fresh
	}
	e.LEDTrusted = k.Wiring.LED == store.Wired

	h.mu.Lock()
	tr := h.track(k.Machine)
	if word != nil {
		tr.Word = *word
	}
	if tr.Word.Sleep == nil && hasHelper {
		tr.Word.Sleep = p.Sleep // what it said before the hub last started
	}
	from, changed := tr.Step(e, h.rules(k))
	now := *tr
	h.mu.Unlock()
	if changed && from != "" {
		h.publish(k.Machine, from, now)
	}
}

// track is the detector's memory for a machine (h.mu held).
func (h *Hub) track(machine string) *Track {
	if h.tracks == nil {
		h.tracks = map[string]*Track{}
	}
	key := strings.ToLower(machine)
	tr := h.tracks[key]
	if tr == nil {
		tr = &Track{}
		h.tracks[key] = tr
	}
	return tr
}

// Tracked is the detector's view of a machine, if it has looked at it.
func (h *Hub) Tracked(machine string) (Track, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tr, ok := h.tracks[strings.ToLower(machine)]
	if !ok || tr.State == "" {
		return Track{}, false
	}
	return *tr, true
}

func (h *Hub) rules(k store.KVM) Rules {
	r := DefaultRules
	if h.HungAfter != nil {
		if d := h.HungAfter(); d > 0 {
			r.HungAfter = d
		}
	}
	r.LEDAsleep = k.Wiring.LEDAsleep
	return r
}

func (h *Hub) publish(machine, from string, tr Track) {
	ev := Event{At: tr.Since, Event: tr.State, Machine: machine, State: tr.State, From: from, Reason: tr.Reason,
		Asleep: tr.Asleep, Evidence: tr.Evidence}
	if from == StateHung && tr.State == StateOn {
		ev.Event = "recovered"
	}
	if h.Events != nil {
		ev = h.Events.Publish(ev)
	}
	switch ev.Event {
	case StateHung:
		h.log().Warn("machine is hung", "machine", machine, "evidence", fmt.Sprintf("%+v", tr.Evidence))
		h.Store.AddEvent(store.Event{At: ev.At, Who: h.Self, Action: machine + " looks frozen: nothing answers, power light on", Result: "detected"})
	case "recovered":
		h.log().Info("machine answers again", "machine", machine)
		h.Store.AddEvent(store.Event{At: ev.At, Who: h.Self, Action: machine + " answers again", Result: "recovered"})
	default:
		h.log().Info("machine state", "machine", machine, "state", tr.State, "from", from, "reason", tr.Reason)
	}
}

// acted gives a machine the hub just started, restarted, reset or forced off time to boot.
func (h *Hub) acted(machine string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.track(machine).Acted = h.now().UTC()
}

// ask gets the machine's helper's status (twice before calling it silent). answers is true when
// the helper replied at all; word is set when it replied with its status.
func (h *Hub) ask(ctx context.Context, machine string) (answers bool, word *Word) {
	for i := 0; i < 2; i++ {
		var st struct {
			Pending *struct {
				Action string    `json:"action"`
				At     time.Time `json:"at"`
			} `json:"pending"`
			UpdatesPending bool         `json:"updatesPending"`
			Sleep          *store.Sleep `json:"sleep"`
		}
		err := h.Fleet.Forward(ctx, machine, "GET", "/v2/status", nil, &st, "")
		var pe *peer.Error
		switch {
		case err == nil:
			w := &Word{At: h.now().UTC(), UpdatesPending: st.UpdatesPending, Sleep: st.Sleep}
			if st.Pending != nil {
				w.PendingAction, w.PendingAt = st.Pending.Action, st.Pending.At
			}
			return true, w
		case errors.As(err, &pe):
			return true, nil // it replied (without a key for it: not linked yet)
		case !errors.Is(err, fleet.ErrOffline):
			return false, nil
		}
		if i == 0 {
			select {
			case <-ctx.Done():
				return false, nil
			case <-time.After(dur(h.AskGap, 1500*time.Millisecond)):
			}
		}
	}
	return false, nil
}

// port connects to the helper's port: refused means the machine's OS is up and answered.
func (h *Hub) port(ctx context.Context, addr string, port int) string {
	if port == 0 {
		port = peer.Port
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err == nil {
		c.Close()
		return Accepts
	}
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "refused") {
		return Refuses
	}
	return NoReply
}

func (h *Hub) ping(ctx context.Context, addr string) string {
	if h.Ping == nil {
		return Unavailable
	}
	return h.Ping(ctx, addr)
}

func (h *Hub) online(ctx context.Context, addr string) *bool {
	if h.Online == nil {
		return nil
	}
	return h.Online(ctx, addr)
}

// sampleLED reads the light three times, half a second apart: on and off within one look is
// blinking, which many boards do while asleep.
func (h *Hub) sampleLED(ctx context.Context, k store.KVM) (string, error) {
	var on, off int
	var last error
	for i := 0; i < 3; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(dur(h.LEDGap, 500*time.Millisecond)):
			}
		}
		led, err := h.readLED(ctx, k)
		switch {
		case err != nil:
			last = err
		case *led:
			on++
		default:
			off++
		}
	}
	switch {
	case on > 0 && off > 0:
		return Blinking, nil
	case on > 0:
		return On, nil
	case off > 0:
		return Off, nil
	}
	return Unreadable, last
}

// Note records what a machine's helper said is about to happen. After a sleep note, the hub
// learns what the machine's light does while it sleeps.
func (h *Hub) Note(machine string, n Note) {
	n.At = h.now().UTC()
	h.mu.Lock()
	h.track(machine).Note = &n
	h.mu.Unlock()
	h.log().Info("machine says", "machine", machine, "event", n.Event, "updates", n.Updates)
	k, ok := h.Store.KVMFor(machine)
	if n.Event != "sleep" || !ok {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		time.Sleep(dur(h.AsleepAfter, 30*time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), lookTime)
		defer cancel()
		if h.Fleet.Answers(ctx, machine) {
			return // it didn't sleep after all
		}
		led, _ := h.sampleLED(ctx, k)
		if led == Unreadable {
			return
		}
		_ = h.Store.UpdateKVM(k.Name, func(k *store.KVM) {
			if k.Wiring.LEDAsleep != led {
				h.log().Info("learned the power light while asleep", "kvm", k.Name, "led", led)
				k.Wiring.LEDAsleep = led
			}
		})
	}()
}
