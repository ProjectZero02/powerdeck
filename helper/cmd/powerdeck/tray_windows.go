//go:build windows

package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/projectzero02/powerdeck/helper/internal/app"
)

// The notification-area icon ("Show icon in hidden icons", off by default) lives in the presence
// companion, which already runs in the signed-in user's session. Click: open PowerDeck. Right-click:
// Open PowerDeck, Hide this icon.

var (
	procRegisterWindowMsg = user32.NewProc("RegisterWindowMessageW")
	procPostMessageW      = user32.NewProc("PostMessageW")
	procCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	procAppendMenuW       = user32.NewProc("AppendMenuW")
	procTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	procDestroyMenu       = user32.NewProc("DestroyMenu")
	procGetCursorPos      = user32.NewProc("GetCursorPos")
	procShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	procGetSystemMetricsT = user32.NewProc("GetSystemMetrics")
)

const (
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1 // from the shell: something happened on the icon
	wmTraySet      = wmApp + 2 // from the presence loop: wParam 1 = show, 0 = hide
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	wmNull         = 0x0000
	nimAdd         = 0
	nimDelete      = 2
	nifMessage     = 0x1
	nifIcon        = 0x2
	nifTip         = 0x4
	mfString       = 0x0
	mfSeparator    = 0x800
	tpmReturnCmd   = 0x100
	tpmRightButton = 0x2
	smCxSmIcon     = 49
	cmdOpen        = 1
	cmdHide        = 2
)

type notifyIconData struct {
	Size            uint32
	Wnd             windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     windows.Handle
}

type trayIcon struct {
	hwnd          uintptr
	data          notifyIconData
	shown         bool
	taskbarRetart uint32
}

var tray *trayIcon

// startTray creates the (hidden) window that owns the icon, on its own thread. It returns a
// function that shows or hides the icon.
func startTray() func(show bool) {
	ready := make(chan uintptr)
	go func() {
		runtime.LockOSThread()
		inst, _, _ := procGetModuleHandleW.Call(0)
		cls, _ := windows.UTF16PtrFromString("PowerDeckTray")
		wc := wndClassEx{Instance: windows.Handle(inst), ClassName: cls, WndProc: windows.NewCallback(trayProc)}
		wc.Size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		// A real (never shown) window, not a message-only one: only those hear TaskbarCreated.
		hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
		if hwnd == 0 {
			close(ready)
			return
		}
		tc, _ := windows.UTF16PtrFromString("TaskbarCreated")
		restart, _, _ := procRegisterWindowMsg.Call(uintptr(unsafe.Pointer(tc)))
		appName, _ := windows.UTF16PtrFromString("APP")
		size, _, _ := procGetSystemMetricsT.Call(smCxSmIcon)
		icon, _, _ := procLoadImageW.Call(inst, uintptr(unsafe.Pointer(appName)), imageIcon, size, size, lrShared)
		t := &trayIcon{hwnd: hwnd, taskbarRetart: uint32(restart)}
		t.data = notifyIconData{Wnd: windows.HWND(hwnd), ID: 1, Flags: nifMessage | nifIcon | nifTip,
			CallbackMessage: wmTrayCallback, Icon: windows.Handle(icon)}
		t.data.Size = uint32(unsafe.Sizeof(t.data))
		copy(t.data.Tip[:], windows.StringToUTF16("PowerDeck"))
		tray = t
		ready <- hwnd
		var m msg
		for {
			if r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0); int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	hwnd, ok := <-ready
	if !ok {
		return func(bool) {}
	}
	return func(show bool) {
		v := uintptr(0)
		if show {
			v = 1
		}
		procPostMessageW.Call(hwnd, wmTraySet, v, 0)
	}
}

func (t *trayIcon) set(show bool) {
	if show == t.shown {
		return
	}
	if show {
		r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.data)))
		t.shown = r != 0
	} else {
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.data)))
		t.shown = false
	}
}

func trayProc(hwnd, m, wp, lp uintptr) uintptr {
	t := tray
	if t == nil || t.hwnd != hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
		return r
	}
	switch {
	case m == wmTraySet:
		t.set(wp == 1)
		return 0
	case m == uintptr(t.taskbarRetart) && t.shown:
		t.shown = false // Explorer restarted and forgot the icon
		t.set(true)
		return 0
	case m == wmTrayCallback:
		switch lp & 0xFFFF {
		case wmLButtonUp:
			openFromTray()
		case wmRButtonUp:
			t.menu()
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func (t *trayIcon) menu() {
	h, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(h)
	add := func(flags, id uintptr, text string) {
		p, _ := windows.UTF16PtrFromString(text)
		procAppendMenuW.Call(h, flags, id, uintptr(unsafe.Pointer(p)))
	}
	add(mfString, cmdOpen, "Open PowerDeck")
	add(mfSeparator, 0, "")
	add(mfString, cmdHide, "Hide this icon")
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWnd.Call(t.hwnd) // so the menu closes when you click elsewhere
	cmd, _, _ := procTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	switch cmd {
	case cmdOpen:
		openFromTray()
	case cmdHide:
		t.set(false)
		go func() {
			hc := &http.Client{Timeout: 5 * time.Second}
			if resp, err := hc.Post("http://"+app.UIAddr+"/tray", "application/json", bytes.NewReader([]byte(`{"show":false}`))); err == nil {
				resp.Body.Close()
			}
		}()
	}
}

func openFromTray() {
	if focusExisting() {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = exec.Command(exe, "open").Start()
	}
}
