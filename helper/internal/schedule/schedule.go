// Package schedule runs stored schedules at their local time.
package schedule

import (
	"context"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// Due reports whether sc should run in the minute containing now (local time).
func Due(sc store.Schedule, now time.Time) bool {
	if !sc.Enabled || now.Format("15:04") != sc.Time {
		return false
	}
	day := [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[now.Weekday()]
	for _, d := range sc.Days {
		if d == day {
			return true
		}
	}
	return false
}

// Loop checks every 20 seconds and runs each due schedule once per minute. Nothing runs in the minute
// the helper started: a restart (an update, a crash) right after a schedule fired, or after someone
// cancelled it, mustn't run it again.
func Loop(ctx context.Context, list func() []store.Schedule, run func(context.Context, store.Schedule), now func() time.Time) {
	done := map[string]string{} // schedule ID -> minute it last ran
	started := now().Format("2006-01-02T15:04")
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		n := now()
		minute := n.Format("2006-01-02T15:04")
		for _, sc := range list() {
			if Due(sc, n) && done[sc.ID] != minute && minute != started {
				done[sc.ID] = minute
				go run(ctx, sc)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
