//go:build !windows

package tailscale

import (
	"os"
	"os/exec"
)

func cliPath() string {
	for _, p := range []string{"/usr/bin/tailscale", "/usr/local/bin/tailscale", "/usr/sbin/tailscale"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "tailscale"
}

func hideWindow(*exec.Cmd) {}
