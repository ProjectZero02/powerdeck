package winsvc

import "testing"

func TestPowerEvent(t *testing.T) {
	for typ, want := range map[uintptr]string{4: "sleep", 0x12: "resume", 7: "", 0xA: "", 0x8013: ""} {
		if got := powerEvent(typ); got != want {
			t.Errorf("%#x: %q, want %q", typ, got, want)
		}
	}
}

// Registering for suspend and resume works for an ordinary process too (CI isn't a service).
func TestWatchSuspendRegisters(t *testing.T) {
	stop, err := watchSuspend(func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
