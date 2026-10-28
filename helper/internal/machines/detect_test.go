package machines

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

var t0 = time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)

// Looks, as short codes: h = helper answers, x = everything silent with the light on (a hang's
// look), o = light off, b = blinking, u = light unreadable, p = ping replies, r = port refuses,
// n = no helper at all, t = light not trusted.
func look(c byte, at time.Time) Evidence {
	e := Evidence{At: at, Helper: Silent, Port: Accepts, Ping: NoReply, LED: On, LEDTrusted: true}
	switch c {
	case 'h':
		e.Helper, e.Port, e.Ping = Answers, Accepts, Replies
	case 'o':
		e.LED, e.Port = Off, NoReply
	case 'b':
		e.LED, e.Port = Blinking, NoReply
	case 'u':
		e.LED = Unreadable
	case 'p':
		e.Ping = Replies
	case 'r':
		e.Port = Refuses
	case 'n':
		e.Helper = ""
	case 't':
		e.LEDTrusted = false
	}
	return e
}

// feed feeds looks 30 s apart and returns each resulting state (with its reason when unknown).
func feed(tr *Track, r Rules, looks string, start time.Time) []string {
	var out []string
	for i := 0; i < len(looks); i++ {
		tr.Step(look(looks[i], start.Add(time.Duration(i)*30*time.Second)), r)
		s := tr.State
		if tr.Reason != "" {
			s += "/" + tr.Reason
		}
		if tr.Asleep {
			s += "/asleep"
		}
		out = append(out, s)
	}
	return out
}

func last(s []string) string { return s[len(s)-1] }

func TestDetector(t *testing.T) {
	r := DefaultRules
	for _, c := range []struct {
		name  string
		setup func(*Track)
		looks string
		want  string
	}{
		{"answers", nil, "h", "on"},
		{"silent 2.5 min is not yet hung", nil, "hxxxxx", "unknown/checking"},
		{"silent 3 min is hung", nil, "hxxxxxxx", "hung"},
		{"off", nil, "ho", "off"},
		{"blinking is asleep", nil, "hb", "off/asleep"},
		{"ping replies: the helper stopped", nil, "hxxxxxxpp", "unknown/helper_stopped"},
		{"port refuses: the helper stopped", nil, "hxxxxxxxr", "unknown/helper_stopped"},
		{"a contradiction starts the count again", nil, "hxxxxxpxxxxxx", "unknown/checking"},
		{"... and it ends 3 min after", nil, "hxxxxxpxxxxxxx", "hung"},
		{"light off, then on again: booting", nil, "hxxxxoxxxxxx", "unknown/booting"},
		{"unreadable light neither counts nor resets", nil, "hxxuuxxxx", "hung"},
		{"unreadable alone isn't hung", nil, "huuuuuuuuu", "unknown/led_unreadable"},
		{"hung stays hung while unreadable", nil, "hxxxxxxxuu", "hung"},
		{"no helper is never hung", nil, "nnnnnnnnnnnn", "unknown/no_helper"},
		{"untrusted light is never hung", nil, "tttttttttttt", "unknown/led_not_trusted"},
		{"answers again", nil, "hxxxxxxxh", "on"},
		{"light came on: booting for 10 min", nil, "o" + strings.Repeat("x", 20), "unknown/booting"},
		{"... then counted from 10 min", nil, "o" + strings.Repeat("x", 26), "unknown/checking"},
		{"... hung 3 min after that", nil, "o" + strings.Repeat("x", 27), "hung"},
		{"hub acted 1 min ago", func(tr *Track) { tr.Acted = t0 }, "xxxxxxxxxx", "unknown/booting"},
		{"restart note", func(tr *Track) { tr.Note = &Note{At: t0, Event: "restart"} }, "xxxxxxxxxx", "unknown/restarting"},
		{"shutdown note, light still on after 10 min", func(tr *Track) { tr.Note = &Note{At: t0, Event: "shutdown"} },
			strings.Repeat("x", 27), "hung"},
		{"restart with updates", func(tr *Track) { tr.Note = &Note{At: t0, Event: "restart", Updates: true} },
			strings.Repeat("x", 120), "unknown/updating"},
		{"updates pending, countdown seen", func(tr *Track) {
			tr.Word = Word{At: t0, PendingAction: "restart", PendingAt: t0.Add(time.Minute), UpdatesPending: true}
		}, strings.Repeat("x", 120), "unknown/updating"},
		{"updates pending but no restart: a hang", func(tr *Track) { tr.Word = Word{At: t0, UpdatesPending: true} },
			"xxxxxxx", "hung"},
		{"countdown due later", func(tr *Track) {
			tr.Word = Word{At: t0, PendingAction: "shutdown", PendingAt: t0.Add(5 * time.Minute)}
		}, "xxxxxxxxxxxxxxxxxxxx", "unknown/shutting_down"},
		{"sleep note", func(tr *Track) { tr.Note = &Note{At: t0, Event: "sleep"} }, "xxxxxxxxxx", "unknown/may_be_asleep"},
		{"sleep note, then off", func(tr *Track) { tr.Note = &Note{At: t0, Event: "sleep"} }, "xo", "off/asleep"},
		{"sleeps on its own, light in sleep unknown", func(tr *Track) {
			tr.Word.Sleep = &store.Sleep{ModernStandby: true, AfterMinutes: 30}
		}, "xxxxxxxxxx", "unknown/may_be_asleep"},
		{"never sleeps on its own", func(tr *Track) { tr.Word.Sleep = &store.Sleep{S3: true} }, "xxxxxxx", "hung"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := &Track{}
			if c.setup != nil {
				c.setup(tr)
			}
			got := feed(tr, r, c.looks, t0)
			if last(got) != c.want {
				t.Errorf("%s → %v, want %s at the end", c.looks, got, c.want)
			}
		})
	}
}

func TestDetectorLightInSleep(t *testing.T) {
	sleeps := Word{Sleep: &store.Sleep{S3: true, AfterMinutes: 30}}
	for led, want := range map[string]string{"": "unknown/may_be_asleep", On: "unknown/may_be_asleep", Off: "hung", Blinking: "hung"} {
		r := DefaultRules
		r.LEDAsleep = led
		tr := &Track{Word: sleeps}
		if got := last(feed(tr, r, "xxxxxxx", t0)); got != want {
			t.Errorf("light in sleep %q: %s, want %s", led, got, want)
		}
	}
}

func TestDetectorHungAfter(t *testing.T) {
	r := DefaultRules
	r.HungAfter = 10 * time.Minute
	got := feed(&Track{}, r, strings.Repeat("x", 21), t0)
	if got[19] != "unknown/checking" || got[20] != "hung" {
		t.Errorf("%v", got)
	}
}

func TestDetectorGapStartsAgain(t *testing.T) {
	tr := &Track{}
	feed(tr, DefaultRules, "xxxx", t0) // 1.5 min silent
	// The hub was down for an hour: one look after it isn't 3 minutes of evidence.
	tr.Step(look('x', t0.Add(time.Hour)), DefaultRules)
	if tr.State != StateUnknown || tr.Reason != ReasonChecking {
		t.Errorf("after a gap: %s/%s", tr.State, tr.Reason)
	}
}

func TestDetectorSinceAndChanges(t *testing.T) {
	tr := &Track{}
	var changes []string
	for i, c := range "hhxxxxxxxxh" {
		at := t0.Add(time.Duration(i) * 30 * time.Second)
		if from, ok := tr.Step(look(byte(c), at), DefaultRules); ok {
			changes = append(changes, fmt.Sprintf("%s→%s@%d", from, tr.State, i))
		}
	}
	if got := strings.Join(changes, " "); got != "→on@0 on→unknown@2 unknown→hung@8 hung→on@10" {
		t.Errorf("changes %s", got)
	}
	if !tr.Since.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("since %v", tr.Since)
	}
}
