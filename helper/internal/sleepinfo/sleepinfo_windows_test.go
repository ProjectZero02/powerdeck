//go:build windows

package sleepinfo

import (
	"testing"
	"unsafe"
)

func TestCapabilitiesLayout(t *testing.T) {
	var c powerCapabilities
	if n := unsafe.Sizeof(c); n != 76 {
		t.Fatalf("SYSTEM_POWER_CAPABILITIES is 76 bytes, this is %d", n)
	}
	if off := unsafe.Offsetof(c.AoAc); off != 20 {
		t.Errorf("AoAc at %d, want 20", off)
	}
	if off := unsafe.Offsetof(c.BatteryScale); off != 32 {
		t.Errorf("BatteryScale at %d, want 32", off)
	}
}

// What this Windows says about sleep (logged: a CI runner is a VM, which usually can't sleep).
func TestRead(t *testing.T) {
	c, ok := capabilities()
	if !ok {
		t.Fatal("GetPwrCapabilities failed")
	}
	secs, ok := sleepAfter()
	t.Logf("S1-S5 %d%d%d%d%d, AoAc %d, hibernate file %d; sleep after %d s on AC (read: %v)",
		c.SystemS1, c.SystemS2, c.SystemS3, c.SystemS4, c.SystemS5, c.AoAc, c.HiberFilePresent, secs, ok)
	s := Read()
	if s == nil {
		t.Fatal("no sleep facts")
	}
	t.Logf("reported: %+v", *s)
	if !s.S3 && !s.ModernStandby && s.AfterMinutes != 0 {
		t.Errorf("can't sleep, yet sleeps after %d minutes", s.AfterMinutes)
	}
}
