//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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

func runService() error {
	return winsvc.Run(setup.ServiceName, func(ctx context.Context) error {
		log, closer, err := applog.Open(setup.DataDir(), nil)
		if err != nil {
			return err
		}
		defer closer.Close()
		err = app.Run(ctx, app.Options{DataDir: setup.DataDir(), System: newSystem(log), Reports: reports, Log: log})
		if err != nil {
			log.Error("PowerDeck exited", "err", err)
		}
		return err
	})
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

// openWindow shows the PC window: an Edge "app mode" window (no tabs or address bar) on the local
// UI, falling back to the default browser.
func openWindow() error {
	if !setup.ServiceRunning() {
		messageBox("PowerDeck's background service isn't running.\n\nRestart the PC, or reinstall PowerDeck to repair it.", windows.MB_ICONWARNING)
		return nil
	}
	url := "http://" + app.UIAddr + "/"
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		edge := filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`)
		if _, err := os.Stat(edge); err == nil {
			return exec.Command(edge, "--app="+url, "--window-size=1060,900").Start()
		}
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
