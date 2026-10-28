//go:build windows

package winsvc

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	powrprof         = windows.NewLazySystemDLL("powrprof.dll")
	procRegister     = powrprof.NewProc("PowerRegisterSuspendResumeNotification")
	procUnregister   = powrprof.NewProc("PowerUnregisterSuspendResumeNotification")
	suspendCallback  uintptr
	suspendOnce      sync.Once
	suspendMu        sync.Mutex
	suspendNotify    func(event string)
	suspendSubscribe deviceNotifySubscribeParameters // Windows holds on to it: not on the Go heap
)

// DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS
type deviceNotifySubscribeParameters struct {
	callback uintptr
	context  uintptr
}

const (
	deviceNotifyCallback  = 2    // DEVICE_NOTIFY_CALLBACK
	pbtAPMSuspend         = 0x4  // PBT_APMSUSPEND: about to sleep or hibernate
	pbtAPMResumeAutomatic = 0x12 // PBT_APMRESUMEAUTOMATIC: woke, for any reason
)

// powerEvent names what a suspend/resume notification means to the hub ("" for the rest).
func powerEvent(typ uintptr) string {
	switch typ {
	case pbtAPMSuspend:
		return "sleep"
	case pbtAPMResumeAutomatic:
		return "resume"
	}
	return ""
}

// watchSuspend calls notify("sleep") just before the PC sleeps and notify("resume") when it wakes.
// Unlike a service's power events (answered as soon as they're queued), Windows waits for this
// callback before it goes on to sleep, so the hub hears of it while the network is still up.
func watchSuspend(notify func(event string)) (stop func(), err error) {
	suspendOnce.Do(func() {
		suspendCallback = windows.NewCallback(func(_, typ, _ uintptr) uintptr {
			suspendMu.Lock()
			f := suspendNotify
			suspendMu.Unlock()
			if e := powerEvent(typ); e != "" && f != nil {
				f(e)
			}
			return 0
		})
	})
	suspendMu.Lock()
	suspendNotify = notify
	suspendMu.Unlock()
	suspendSubscribe = deviceNotifySubscribeParameters{callback: suspendCallback}
	var h uintptr
	r, _, _ := procRegister.Call(deviceNotifyCallback, uintptr(unsafe.Pointer(&suspendSubscribe)), uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return nil, windows.Errno(r)
	}
	return func() {
		procUnregister.Call(h)
		suspendMu.Lock()
		suspendNotify = nil
		suspendMu.Unlock()
	}, nil
}
