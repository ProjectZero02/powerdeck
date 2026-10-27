//go:build windows

package main

import (
	"context"
	"log/slog"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procCreateEnvironmentBlock = windows.NewLazySystemDLL("userenv.dll").NewProc("CreateEnvironmentBlock")

// companionLoop keeps `PowerDeck.exe presence` running in the active console session, so the
// service knows when someone is using the PC. The service (SYSTEM, session 0) starts it with the
// signed-in user's token, restarts it when the user changes, and stops it when the service stops
// (so updates can replace the executable).
func companionLoop(ctx context.Context, log *slog.Logger) {
	var (
		child   windows.Handle
		session uint32 = 0xFFFFFFFF
	)
	stop := func() {
		if child != 0 {
			_ = windows.TerminateProcess(child, 0)
			windows.CloseHandle(child)
			child = 0
		}
	}
	defer stop()
	exe, _ := os.Executable()
	for {
		sid := windows.WTSGetActiveConsoleSessionId()
		alive := false
		if child != 0 {
			var code uint32
			alive = windows.GetExitCodeProcess(child, &code) == nil && code == 259 // STILL_ACTIVE
		}
		if !alive || sid != session {
			stop()
			if sid != 0xFFFFFFFF {
				if h, err := startInSession(sid, exe); err == nil {
					child, session = h, sid
				} else {
					session = 0xFFFFFFFF // nobody signed in yet; try again later
					log.Debug("presence companion not started", "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

func startInSession(sid uint32, exe string) (windows.Handle, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(sid, &tok); err != nil {
		return 0, err // no user signed in to this session
	}
	defer tok.Close()
	var env *uint16
	if r, _, err := procCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&env)), uintptr(tok), 0); r == 0 {
		return 0, err
	}
	defer windows.DestroyEnvironmentBlock(env)
	cmd, _ := windows.UTF16PtrFromString(`"` + exe + `" presence`)
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop}
	var pi windows.ProcessInformation
	err := windows.CreateProcessAsUser(tok, nil, cmd, nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW|windows.DETACHED_PROCESS, env, nil, &si, &pi)
	if err != nil {
		return 0, err
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}
