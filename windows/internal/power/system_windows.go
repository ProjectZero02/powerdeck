//go:build windows

package power

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Windows drives shutdown.exe, which shows the standard "you're about to be signed out" warning
// to whoever is logged in and can be aborted during the grace period.
type Windows struct{}

func shutdownExe() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "shutdown.exe")
}

func run(args ...string) error {
	cmd := exec.Command(shutdownExe(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		switch exit.ExitCode() {
		case 1190: // ERROR_SHUTDOWN_IS_SCHEDULED
			return ErrPending
		case 1116: // ERROR_NO_SHUTDOWN_IN_PROGRESS
			return ErrNothingPending
		}
		return fmt.Errorf("shutdown.exe exit %d: %s", exit.ExitCode(), out)
	}
	return err
}

func (Windows) Schedule(a Action, delay time.Duration, message string) error {
	mode := "/s"
	if a == Restart {
		mode = "/r"
	}
	if len(message) > 500 {
		message = message[:500]
	}
	// /d p:0:0 = planned, "other". A timeout above zero implies /f (apps are closed).
	return run(mode, "/t", strconv.Itoa(int(delay.Seconds())), "/d", "p:0:0", "/c", message)
}

func (Windows) Abort() error { return run("/a") }

var getTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

func (Windows) BootTime() (time.Time, error) {
	ms, _, _ := getTickCount64.Call() // milliseconds since boot; amd64/arm64 return it whole in r1
	return time.Now().Add(-time.Duration(ms) * time.Millisecond), nil
}
