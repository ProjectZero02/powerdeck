//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/projectzero02/powerdeck/helper/internal/app"
	"github.com/projectzero02/powerdeck/helper/internal/setup"
)

// The PC window is PowerDeck's own window hosting WebView2 (the Edge engine built into Windows),
// showing the local UI. Its title bar takes the page's colours, so the two read as one surface.

const windowClass = "PowerDeckWindow"

// Page background (see web/app.css --bg), as COLORREF (0x00BBGGRR) and RGB.
var (
	lightBG = [3]byte{0xF6, 0xF7, 0xFB}
	darkBG  = [3]byte{0x11, 0x12, 0x1F}
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW  = user32.NewProc("RegisterClassExW")
	procCreateWindowExW   = user32.NewProc("CreateWindowExW")
	procDefWindowProcW    = user32.NewProc("DefWindowProcW")
	procShowWindow        = user32.NewProc("ShowWindow")
	procUpdateWindow      = user32.NewProc("UpdateWindow")
	procGetMessageW       = user32.NewProc("GetMessageW")
	procTranslateMessage  = user32.NewProc("TranslateMessage")
	procDispatchMessageW  = user32.NewProc("DispatchMessageW")
	procPostQuitMessage   = user32.NewProc("PostQuitMessage")
	procDestroyWindow     = user32.NewProc("DestroyWindow")
	procFindWindowW       = user32.NewProc("FindWindowW")
	procSetForegroundWnd  = user32.NewProc("SetForegroundWindow")
	procIsIconic          = user32.NewProc("IsIconic")
	procLoadImageW        = user32.NewProc("LoadImageW")
	procGetDpiForSystem   = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	gdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	procCreateSolidBrush  = gdi32.NewProc("CreateSolidBrush")
	dwmapi                = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr  = dwmapi.NewProc("DwmSetWindowAttribute")
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW     = shell32.NewProc("ShellExecuteW")
	procQueryNotifyState  = shell32.NewProc("SHQueryUserNotificationState")
	procGetLastInputInfo  = user32.NewProc("GetLastInputInfo")
	procGetTickCount      = kernel32.NewProc("GetTickCount")
	procAttachConsole     = kernel32.NewProc("AttachConsole")
	procCreateEnvironment = windows.NewLazySystemDLL("userenv.dll").NewProc("CreateEnvironmentBlock")
)

const (
	wmDestroy        = 0x0002
	wmMove           = 0x0003
	wmSize           = 0x0005
	wmClose          = 0x0010
	wmSettingChange  = 0x001A
	wmGetMinMaxInfo  = 0x0024
	wmDpiChanged     = 0x02E0
	wsOverlappedWnd  = 0x00CF0000
	swHide           = 0
	swShow           = 5
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swRestore        = 9
	smCxScreen       = 0
	smCyScreen       = 1
	imageIcon        = 1
	lrDefaultSize    = 0x40
	lrShared         = 0x8000
	dwmwaDarkMode    = 20
	dwmwaBorderColor = 34
	dwmwaCaptionClr  = 35
	dwmwaTextColor   = 36
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type msg struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type minMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize struct{ X, Y int32 }
}

// openWindow shows the PC window, or brings it forward if it's already open.
func openWindow() error {
	if !setup.ServiceRunning() {
		messageBox("PowerDeck's background service isn't running.\n\nRestart the PC, or reinstall PowerDeck to repair it.", windows.MB_ICONWARNING)
		return nil
	}
	if focusExisting() {
		return nil
	}
	url := "http://" + app.UIAddr + "/"
	if err := runWindow(url); err != nil {
		// No WebView2 runtime (very old Windows 10): the default browser will do.
		return openURL(url)
	}
	return nil
}

func focusExisting() bool {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if h == 0 {
		return false
	}
	if r, _, _ := procIsIconic.Call(h); r != 0 {
		procShowWindow.Call(h, swRestore)
	}
	procSetForegroundWnd.Call(h)
	return true
}

type window struct {
	hwnd uintptr
	web  *edge.Chromium // set once WebView2 is up
	dpi  uint32
	// closing: closed while WebView2 was still starting. The window is only hidden then, as
	// WebView2 needs it until it's up.
	closing bool
}

var win *window

func runWindow(url string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		return errors.New("WebView2 isn't installed")
	}

	inst, _, _ := procGetModuleHandleW.Call(0)
	dark := darkTheme()
	appName, _ := windows.UTF16PtrFromString("APP") // the icon resource (winres)
	icon, _, _ := procLoadImageW.Call(inst, uintptr(unsafe.Pointer(appName)), imageIcon, 0, 0, lrDefaultSize|lrShared)
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorref(bg(dark))))
	cls, _ := windows.UTF16PtrFromString(windowClass)
	wc := wndClassEx{
		Instance: windows.Handle(inst), Icon: windows.Handle(icon), IconSm: windows.Handle(icon),
		Background: windows.Handle(brush), ClassName: cls, WndProc: windows.NewCallback(wndProc),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}

	w := &window{dpi: 96}
	if d, _, _ := procGetDpiForSystem.Call(); d != 0 {
		w.dpi = uint32(d)
	}
	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	width, height := w.scale(980), w.scale(780)
	if height > int32(sh)-w.scale(48) {
		height = int32(sh) - w.scale(48)
	}
	title, _ := windows.UTF16PtrFromString("PowerDeck")
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), wsOverlappedWnd,
		uintptr((int32(sw)-width)/2), uintptr((int32(sh)-height)/2), uintptr(width), uintptr(height), 0, 0, inst, 0)
	if hwnd == 0 {
		return err
	}
	w.hwnd = hwnd
	win = w
	theme(hwnd, dark)
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)

	web := edge.NewChromium()
	web.DataPath = filepath.Join(os.Getenv("LOCALAPPDATA"), "PowerDeck", "WebView2")
	web.MessageCallback = w.message
	ok := web.Embed(hwnd) // runs the message loop until WebView2 is up
	if w.closing {
		procDestroyWindow.Call(hwnd)
		return nil // closed by the user: no browser either
	}
	if !ok {
		win = nil
		procDestroyWindow.Call(hwnd)
		return errors.New("WebView2 isn't available")
	}
	w.web = web
	if c2 := w.web.GetController().GetICoreWebView2Controller2(); c2 != nil {
		b := bg(dark)
		_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: b[0], G: b[1], B: b[2]})
	}
	if s, err := w.web.GetSettings(); err == nil {
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
		_ = s.PutAreBrowserAcceleratorKeysEnabled(false)
	}
	w.web.Resize()
	w.web.Navigate(url)

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (w *window) scale(px int32) int32 { return px * int32(w.dpi) / 96 }

// message handles window.chrome.webview.postMessage from the page: {"open": url} opens a link in
// the default browser (only GitHub links are sent).
func (w *window) message(raw string) {
	var m struct {
		Open string `json:"open"`
	}
	if json.Unmarshal([]byte(raw), &m) == nil && strings.HasPrefix(m.Open, "https://github.com/") {
		_ = openURL(m.Open)
	}
}

func wndProc(hwnd, m, wp, lp uintptr) uintptr {
	w := win
	if w != nil && w.hwnd == hwnd {
		switch m {
		case wmSize:
			if w.web != nil {
				w.web.Resize()
			}
		case wmMove:
			if w.web != nil {
				_ = w.web.NotifyParentWindowPositionChanged()
			}
		case wmDpiChanged:
			// Moved to a screen with another scale: take the size Windows suggests for it.
			w.dpi = uint32(wp & 0xFFFF)
			r := *(**windows.Rect)(unsafe.Pointer(&lp))
			procSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
			return 0
		case wmGetMinMaxInfo:
			mmi := *(**minMaxInfo)(unsafe.Pointer(&lp)) // lParam is a pointer (keeps go vet quiet)
			mmi.MinTrackSize.X, mmi.MinTrackSize.Y = w.scale(560), w.scale(560)
			return 0
		case wmSettingChange:
			// Light/dark mode changed: the page follows by itself, the title bar needs telling.
			if lp != 0 && windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&lp))) == "ImmersiveColorSet" {
				theme(hwnd, darkTheme())
			}
		case wmClose:
			if w.web == nil {
				w.closing = true
				procShowWindow.Call(hwnd, swHide)
				return 0
			}
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func bg(dark bool) [3]byte {
	if dark {
		return darkBG
	}
	return lightBG
}

func colorref(c [3]byte) uint32 { return uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16 }

// theme colours the title bar like the page (Windows 11; Windows 10 takes only dark mode).
func theme(hwnd uintptr, dark bool) {
	set := func(attr uint32, v uint32) {
		procDwmSetWindowAttr.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&v)), 4)
	}
	set(dwmwaDarkMode, map[bool]uint32{true: 1, false: 0}[dark])
	c := colorref(bg(dark))
	set(dwmwaCaptionClr, c)
	set(dwmwaBorderColor, c)
	if dark {
		set(dwmwaTextColor, colorref([3]byte{0xEC, 0xEE, 0xFA}))
	} else {
		set(dwmwaTextColor, colorref([3]byte{0x15, 0x17, 0x2A}))
	}
}

func darkTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// openURL opens a link in the default browser.
func openURL(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	u, _ := windows.UTF16PtrFromString(url)
	if r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(u)), 0, 0, swShow); r > 32 {
		return nil
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
