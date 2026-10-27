//go:build linux

package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/wol"
)

const (
	ServiceName = "powerdeck"
	BinPath     = "/usr/local/bin/powerdeck-helper"
	unitPath    = "/etc/systemd/system/powerdeck.service"
)

// DataDir is /var/lib/powerdeck (POWERDECK_DATA overrides it for development).
func DataDir() string {
	if d, ok := devDataDir(); ok {
		return d
	}
	return "/var/lib/powerdeck"
}

const unit = `[Unit]
Description=PowerDeck helper (remote power control over Tailscale)
Documentation=https://github.com/ProjectZero02/powerdeck
Wants=network-online.target
After=network-online.target tailscaled.service

[Service]
ExecStart=` + BinPath + ` run --service
Restart=always
RestartSec=5
# Root is needed to power off, arm Wake-on-LAN and read Proxmox guests.
User=root
NoNewPrivileges=yes
ProtectHome=read-only
PrivateTmp=yes
StateDirectory=powerdeck
StateDirectoryMode=0700

[Install]
WantedBy=multi-user.target
`

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst) // atomic, so a running copy keeps its old inode
}

// Install copies the binary to BinPath, installs the systemd unit and (re)starts it. It's
// idempotent: the installer runs it on every update.
func Install(exePath string) ([]Step, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("run this as root (sudo)")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, errors.New("this system doesn't use systemd; see the README for running the helper another way")
	}
	var steps []Step
	if abs, _ := filepath.Abs(exePath); abs != BinPath {
		if err := copyFile(exePath, BinPath); err != nil {
			return steps, fmt.Errorf("install binary: %w", err)
		}
	}
	steps = append(steps, Step{Name: "Helper installed", OK: true, Detail: BinPath})
	if err := os.MkdirAll(DataDir(), 0o700); err != nil {
		return steps, err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return steps, fmt.Errorf("systemd unit: %w", err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return steps, err
	}
	if err := systemctl("enable", ServiceName); err != nil {
		return steps, err
	}
	if err := systemctl("restart", ServiceName); err != nil {
		return steps, err
	}
	steps = append(steps, Step{Name: "Background service", OK: true, Detail: "starts at boot, restarts itself if it stops"})
	if err := systemctl("enable", "tailscaled"); err == nil {
		steps = append(steps, Step{Name: "Tailscale starts at boot", OK: true})
	} else {
		steps = append(steps, Step{Name: "Tailscale starts at boot", Detail: err.Error()})
	}
	return steps, nil
}

// Uninstall stops and removes the service and binary. With purge it also deletes keys and settings.
func Uninstall(purge bool) error {
	if os.Geteuid() != 0 {
		return errors.New("run this as root (sudo)")
	}
	_ = systemctl("disable", "--now", ServiceName)
	var errs []error
	for _, p := range []string{unitPath, BinPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	_ = systemctl("daemon-reload")
	if purge {
		errs = append(errs, os.RemoveAll(DataDir()))
	}
	return errors.Join(errs...)
}

// ServiceRunning reports whether the systemd service is active.
func ServiceRunning() bool {
	return exec.Command("systemctl", "is-active", "--quiet", ServiceName).Run() == nil
}

// Checks are the Linux setup checks.
func Checks(e Env) []check.Check {
	return []check.Check{
		tailscaleCheck(e),
		keyExpiryCheck(e),
		{ID: "tailscale_boot",
			Run: func(context.Context) check.Result {
				if exec.Command("systemctl", "is-enabled", "--quiet", "tailscaled").Run() == nil {
					return check.Result{Level: check.OK, Title: "Tailscale starts at boot"}
				}
				return check.Result{Level: check.Error, Title: "Tailscale doesn't start at boot",
					Detail: "Without it, this device is unreachable after a restart."}
			},
			Fix: func(context.Context) error { return systemctl("enable", "--now", "tailscaled") },
		},
		wiredCheck(e),
		{ID: "wol_nic",
			Run: func(context.Context) check.Result {
				n, ok := e.Network()
				if !ok || !n.Wired {
					return check.Result{Level: check.Info, Title: "Wake-on-LAN", Detail: "Needs a wired network card."}
				}
				on, err := wol.Armed(n.Interface)
				switch {
				case errors.Is(err, wol.ErrUnsupported):
					return check.Result{Level: check.Warn, Title: "Network card can't wake by magic packet", Detail: n.Interface}
				case err != nil:
					return check.Result{Level: check.Warn, Title: "Wake-on-LAN state unknown", Detail: err.Error()}
				case !on:
					return check.Result{Level: check.Warn, Title: "Wake-on-LAN is off on " + n.Interface}
				}
				return check.Result{Level: check.OK, Title: "Wake-on-LAN on " + n.Interface}
			},
			Fix: func(context.Context) error {
				n, ok := e.Network()
				if !ok {
					return errors.New("no wired network card found")
				}
				return wol.Arm(n.Interface)
			},
		},
		batteryCheck(e),
		biosCheck(),
		{ID: "ac_restore", Run: func(context.Context) check.Result {
			return check.Result{Level: check.Info, Title: "BIOS: restore power after an outage",
				Detail: "Set \"Restore on AC power loss\" to On so it comes back after a power cut."}
		}},
		updatesCheck(e),
	}
}

// PrepareNetwork arms Wake-on-LAN on the primary card. Many drivers forget it at reboot, so the
// helper calls this at every start.
func PrepareNetwork(e Env) error {
	n, ok := e.Network()
	if !ok || !n.Wired {
		return nil
	}
	return wol.Arm(n.Interface)
}
