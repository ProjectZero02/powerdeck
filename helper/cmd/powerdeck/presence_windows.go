//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/projectzero02/powerdeck/helper/internal/app"
	"github.com/projectzero02/powerdeck/helper/internal/power"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procQueryNotifyState = shell32.NewProc("SHQueryUserNotificationState")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

// QUERY_USER_NOTIFICATION_STATE values.
const (
	qunsNotPresent = 1 // screen saver, locked, or inactive fast-user-switching session
	qunsBusy       = 2 // full-screen app
	qunsD3DFull    = 3 // full-screen Direct3D (games)
	qunsPresenting = 4 // presentation mode
)

func sample() power.Presence {
	p := power.Presence{User: os.Getenv("USERNAME")}
	var lii struct{ Size, Time uint32 }
	lii.Size = uint32(unsafe.Sizeof(lii))
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii))); r != 0 {
		now, _, _ := procGetTickCount.Call()
		p.IdleSeconds = int((uint32(now) - lii.Time) / 1000)
	}
	var state int32
	if r, _, _ := procQueryNotifyState.Call(uintptr(unsafe.Pointer(&state))); r == 0 { // S_OK
		switch state {
		case qunsNotPresent:
			p.Locked = true
		case qunsBusy, qunsD3DFull, qunsPresenting:
			p.Fullscreen = true
		}
	}
	return p
}

// runPresence runs in the signed-in user's session (started at sign-in) and tells the service every
// 20 seconds whether someone is using the PC. Gamepad input doesn't count as input to Windows, so
// full-screen apps (games, videos) count as "in use" on their own.
func runPresence() error {
	hc := &http.Client{Timeout: 5 * time.Second}
	for {
		b, _ := json.Marshal(sample())
		if resp, err := hc.Post("http://"+app.UIAddr+"/presence", "application/json", bytes.NewReader(b)); err == nil {
			resp.Body.Close()
		}
		time.Sleep(20 * time.Second)
	}
}
