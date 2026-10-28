//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/projectzero02/powerdeck/helper/internal/app"
	"github.com/projectzero02/powerdeck/helper/internal/applog"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/setup"
	"github.com/projectzero02/powerdeck/helper/internal/winsvc"
)

const defaultCommand = "open"

// reports carries presence from the user-session companion to the service's power checks.
var reports = &power.Reports{}

func newSystem(*slog.Logger) power.System { return power.Windows{Reports: reports} }

func platformCommand(cmd string, _ []string, err *error) bool {
	switch cmd {
	case "open":
		*err = openWindow()
	case "service":
		*err = runService()
	case "presence":
		*err = runPresence()
	default:
		return false
	}
	return true
}

// systemStopping: on Windows the service hears of shutdowns itself (see runService).
var systemStopping func() bool

func runService() error {
	notes := make(chan app.Notice)
	// notify hands what Windows is about to do to the running helper, which tells the hubs; it
	// waits for that, briefly, as Windows goes on without it.
	notify := func(event string) {
		n := app.Notice{Event: event, Done: make(chan struct{})}
		select {
		case notes <- n:
		case <-time.After(200 * time.Millisecond):
			return // not running
		}
		select {
		case <-n.Done:
		case <-time.After(2 * time.Second):
		}
	}
	return winsvc.Run(setup.ServiceName, func(ctx context.Context) error {
		log, closer, err := applog.Open(setup.DataDir(), nil)
		if err != nil {
			return err
		}
		defer closer.Close()
		go companionLoop(ctx, log)
		err = app.Run(ctx, app.Options{DataDir: setup.DataDir(), System: newSystem(log), Reports: reports, Log: log, Installed: true,
			Notes: notes, AllowLocal: func(remote string) bool { return winsvc.LocalCaller(remote, app.UIPort) }})
		if err != nil {
			log.Error("PowerDeck exited", "err", err)
		}
		return err
	}, notify)
}

func install([]string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	steps, err := setup.Install(exe)
	// The installer reads this log if something fails.
	if f, ferr := os.Create(filepath.Join(os.TempDir(), "PowerDeck-install.log")); ferr == nil {
		for _, s := range steps {
			fmt.Fprintf(f, "%v\t%s\t%s\n", s.OK, s.Name, s.Detail)
		}
		if err != nil {
			fmt.Fprintf(f, "FATAL\t%v\n", err)
		}
		f.Close()
	}
	return err
}

func messageBox(text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("PowerDeck")
	windows.MessageBox(0, t, c, flags)
}

// The exe is a GUI-subsystem program (no console window flashes when the service or companion
// starts), so attach to the terminal it was started from, if any, for CLI output.
func init() {
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS
	r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess))
	if r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
}
