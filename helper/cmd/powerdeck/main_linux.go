//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/cli"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/setup"
)

const defaultCommand = "help"

func newSystem(*slog.Logger) power.System { return &power.Linux{} }

func platformCommand(string, []string, *error) bool { return false }

// systemStopping reports whether systemd is shutting the system down (rather than stopping or
// restarting just this service), so the helper can tell its hubs first.
func systemStopping() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "systemctl", "is-system-running").Output()
	return strings.TrimSpace(string(out)) == "stopping"
}

// install sets up the systemd service, then shows the checks and the QR code that adds this
// device to the app.
func install(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	steps, err := setup.Install(exe)
	for _, s := range steps {
		mark := "\x1b[32m✓\x1b[0m"
		if !s.OK {
			mark = "\x1b[33m!\x1b[0m"
		}
		fmt.Printf("  %s %s  \x1b[90m%s\x1b[0m\n", mark, s.Name, s.Detail)
	}
	if err != nil {
		return err
	}
	// Wait for the service to answer (it writes a fresh local token at start).
	var st struct{ Checks []cli.Check }
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err = client().Do("GET", "/v2/status", nil, &st); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("the service didn't start: %w (see: journalctl -u powerdeck)", err)
	}
	fmt.Println("\nSetup checks:")
	cli.PrintChecks(os.Stdout, st.Checks)
	for _, a := range args {
		if a == "--quiet" {
			return nil
		}
	}
	return showQR()
}
