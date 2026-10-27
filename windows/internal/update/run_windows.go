//go:build windows

package update

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// runInstaller starts the installer detached, so it survives the installer stopping this service.
func runInstaller(path string) error {
	cmd := exec.Command(path, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
