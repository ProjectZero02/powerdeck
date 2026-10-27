package schedule

import (
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

func TestDue(t *testing.T) {
	sc := store.Schedule{Time: "01:00", Days: []string{"mon", "fri"}, Enabled: true}
	mon := time.Date(2026, 10, 26, 1, 0, 30, 0, time.Local) // a Monday
	if !Due(sc, mon) {
		t.Error("Monday 01:00 should be due")
	}
	if Due(sc, mon.Add(time.Minute)) || Due(sc, mon.AddDate(0, 0, 1)) {
		t.Error("wrong minute or day ran")
	}
	sc.Enabled = false
	if Due(sc, mon) {
		t.Error("disabled schedule ran")
	}
}
