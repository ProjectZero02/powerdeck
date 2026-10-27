//go:build windows

package power

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows is the real implementation. Reports (optional) carries presence sent by the companion
// that runs in the signed-in user's session (`PowerDeck.exe presence`), which can see full-screen
// apps and recent input; the service itself runs in session 0 and can't.
type Windows struct {
	Reports *Reports
}

var (
	advapi32             = windows.NewLazySystemDLL("advapi32.dll")
	procInitiateShutdown = advapi32.NewProc("InitiateShutdownW")
	procAbortShutdown    = advapi32.NewProc("AbortSystemShutdownW")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64   = kernel32.NewProc("GetTickCount64")
	wtsapi32             = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySession  = wtsapi32.NewProc("WTSQuerySessionInformationW")
)

const (
	shutdownForceOthers    = 0x1
	shutdownRestart        = 0x4
	shutdownPowerOff       = 0x8
	shutdownInstallUpdates = 0x40
	// SHUTDOWN_HYBRID (0x200) is deliberately never set: without it Windows does a full shutdown
	// even when Fast Startup is on, so the network card stays armed for Wake-on-LAN.

	errShutdownIsScheduled  = windows.Errno(1190)
	errNoShutdownInProgress = windows.Errno(1116)
	reasonPlanned           = 0x80000000 // SHTDN_REASON_FLAG_PLANNED, major/minor "other"
)

var privOnce sync.Once

func enableShutdownPrivilege() error {
	var err error
	privOnce.Do(func() {
		var tok windows.Token
		if err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
			return
		}
		defer tok.Close()
		var luid windows.LUID
		if err = windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeShutdownPrivilege"), &luid); err != nil {
			return
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		err = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
	})
	return err
}

func (Windows) Schedule(a Action, delay time.Duration, message string, installUpdates bool) error {
	if err := enableShutdownPrivilege(); err != nil {
		return fmt.Errorf("shutdown privilege: %w", err)
	}
	flags := uint32(shutdownPowerOff | shutdownForceOthers)
	if a == Restart {
		flags = shutdownRestart | shutdownForceOthers
	}
	if installUpdates {
		flags |= shutdownInstallUpdates
	}
	if len(message) > 500 {
		message = message[:500]
	}
	msg, _ := windows.UTF16PtrFromString(message)
	r, _, _ := procInitiateShutdown.Call(0, uintptr(unsafe.Pointer(msg)), uintptr(delay.Seconds()), uintptr(flags), reasonPlanned)
	switch e := windows.Errno(r); e {
	case 0:
		return nil
	case errShutdownIsScheduled:
		return ErrPending
	default:
		return fmt.Errorf("InitiateShutdown: %w", e)
	}
}

func (Windows) Abort() error {
	if err := enableShutdownPrivilege(); err != nil {
		return err
	}
	r, _, e := procAbortShutdown.Call(0)
	if r != 0 {
		return nil
	}
	if errors.Is(e, errNoShutdownInProgress) {
		return ErrNothingPending
	}
	return fmt.Errorf("AbortSystemShutdown: %w", e)
}

func (Windows) BootTime() (time.Time, error) {
	ms, _, _ := procGetTickCount64.Call() // milliseconds since boot; amd64/arm64 return it whole in r1
	return time.Now().Add(-time.Duration(ms) * time.Millisecond), nil
}

// Presence prefers a fresh report from the user-session companion, and otherwise asks Terminal
// Services about the console session. Someone active over Remote Desktop counts too.
func (w Windows) Presence() *Presence {
	var p *Presence
	if w.Reports != nil {
		p = w.Reports.Fresh()
	}
	if p == nil {
		p = consolePresence()
	}
	if p != nil && p.Active {
		return p
	}
	if r := remotePresence(); r != nil {
		return r
	}
	return p
}

// remotePresence finds an active session other than the console's (Remote Desktop).
func remotePresence() *Presence {
	var list *windows.WTS_SESSION_INFO
	var n uint32
	if windows.WTSEnumerateSessions(0, 0, 1, &list, &n) != nil {
		return nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(list)))
	console := windows.WTSGetActiveConsoleSessionId()
	for _, si := range unsafe.Slice(list, n) {
		if si.SessionID == 0 || si.SessionID == console || si.State != wtsActive {
			continue
		}
		if p := sessionPresence(si.SessionID); p != nil && p.Active {
			return p
		}
	}
	return nil
}

const (
	wtsActive        = 0
	wtsSessionInfo   = 24
	wtsSessionInfoEx = 25
	wtsStateLock     = 0
)

func querySession(id uint32, class int) ([]byte, bool) {
	var buf *byte
	var n uint32
	r, _, _ := procWTSQuerySession.Call(0, uintptr(id), uintptr(class), uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || buf == nil {
		return nil, false
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return append([]byte(nil), unsafe.Slice(buf, n)...), true
}

func consolePresence() *Presence {
	id := windows.WTSGetActiveConsoleSessionId()
	if id == 0xFFFFFFFF {
		return &Presence{}
	}
	return sessionPresence(id)
}

func sessionPresence(id uint32) *Presence {
	info, ok := querySession(id, wtsSessionInfo)
	if !ok || len(info) < 216 {
		return nil
	}
	state := *(*uint32)(unsafe.Pointer(&info[0]))
	user := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&info[130])), 21))
	if state != wtsActive || user == "" {
		return &Presence{} // nobody signed in to this session
	}
	p := &Presence{User: user, Active: true}
	if ex, ok := querySession(id, wtsSessionInfoEx); ok && len(ex) >= 20 {
		if *(*int32)(unsafe.Pointer(&ex[16])) == wtsStateLock {
			p.Locked, p.Active = true, false
		}
	}
	last := *(*int64)(unsafe.Pointer(&info[192]))
	cur := *(*int64)(unsafe.Pointer(&info[208]))
	if last > 0 && cur >= last {
		p.IdleSeconds = int((cur - last) / 1e7)
		if !p.Locked {
			p.Active = time.Duration(p.IdleSeconds)*time.Second < ActiveWithin
		}
	}
	return p
}

// UpdatesPending reports updates waiting for a restart or for "Update and shut down".
func (Windows) UpdatesPending() bool {
	for _, path := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`,
	} {
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE); err == nil {
			k.Close()
			return true
		}
	}
	return false
}
