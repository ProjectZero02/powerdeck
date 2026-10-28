//go:build windows

package sleepinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

var (
	powrprof              = windows.NewLazySystemDLL("powrprof.dll")
	getPwrCapabilities    = powrprof.NewProc("GetPwrCapabilities")
	powerGetActiveScheme  = powrprof.NewProc("PowerGetActiveScheme")
	powerReadACValueIndex = powrprof.NewProc("PowerReadACValueIndex")

	// SUB_SLEEP and STANDBYIDLE, as powercfg calls them.
	sleepSubgroup  = windows.GUID{Data1: 0x238c9fa8, Data2: 0x0aad, Data3: 0x41ed, Data4: [8]byte{0x83, 0xf4, 0x97, 0xbe, 0x24, 0x2c, 0x8f, 0x20}}
	standbyTimeout = windows.GUID{Data1: 0x29f6c1db, Data2: 0x86da, Data3: 0x48c5, Data4: [8]byte{0x9f, 0xdb, 0xf2, 0xb6, 0x7b, 0x1f, 0x44, 0xda}}
)

// powerCapabilities is SYSTEM_POWER_CAPABILITIES from winnt.h (76 bytes).
type powerCapabilities struct {
	PowerButtonPresent, SleepButtonPresent, LidPresent      byte
	SystemS1, SystemS2, SystemS3, SystemS4, SystemS5        byte
	HiberFilePresent, FullWake, VideoDimPresent, ApmPresent byte
	UpsPresent, ThermalControl, ProcessorThrottle           byte
	ProcessorMinThrottle, ProcessorMaxThrottle              byte
	FastSystemS4, Hiberboot, WakeAlarmPresent, AoAc         byte
	DiskSpinDown, HiberFileType, AoAcConnectivitySupported  byte
	_                                                       [6]byte
	SystemBatteriesPresent, BatteriesAreShortTerm           byte
	BatteryScale                                            [3]struct{ Granularity, Capacity uint32 }
	AcOnLineWake, SoftLidWake, RtcWake                      int32
	MinDeviceWakeState, DefaultLowLatencyWake               int32
}

func capabilities() (powerCapabilities, bool) {
	var c powerCapabilities
	ok, _, _ := getPwrCapabilities.Call(uintptr(unsafe.Pointer(&c)))
	return c, ok&0xff != 0 // BOOLEAN
}

// sleepAfter is the active power plan's "sleep after" on mains power, in seconds (0 = never).
func sleepAfter() (uint32, bool) {
	var scheme *windows.GUID
	if r, _, _ := powerGetActiveScheme.Call(0, uintptr(unsafe.Pointer(&scheme))); r != 0 || scheme == nil {
		return 0, false
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(scheme)))
	var secs uint32
	r, _, _ := powerReadACValueIndex.Call(0, uintptr(unsafe.Pointer(scheme)), uintptr(unsafe.Pointer(&sleepSubgroup)),
		uintptr(unsafe.Pointer(&standbyTimeout)), uintptr(unsafe.Pointer(&secs)))
	return secs, r == 0
}

// Read is the machine's sleep facts, or nil when Windows won't say.
func Read() *store.Sleep {
	c, ok := capabilities()
	if !ok {
		return nil
	}
	s := &store.Sleep{S3: c.SystemS3 != 0, ModernStandby: c.AoAc != 0}
	if !s.S3 && !s.ModernStandby {
		return s // it can't sleep: "never" whatever the plan says
	}
	secs, ok := sleepAfter()
	if !ok {
		return nil
	}
	s.AfterMinutes = int((secs + 59) / 60)
	return s
}
