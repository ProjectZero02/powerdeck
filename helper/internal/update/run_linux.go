//go:build linux

package update

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Linux releases ship the helper binary per architecture, gzip-compressed (about 4 MB). The
// signature covers the .gz as downloaded.
var (
	InstallerAsset = "powerdeck-helper-linux-" + runtime.GOARCH + ".gz"
	SignatureAsset = InstallerAsset + ".sig"
)

// runInstaller swaps the running binary for the verified download (rename is atomic, the running
// process keeps the old inode) and asks systemd to restart the service.
func runInstaller(path string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(zr, 100<<20))
	if err != nil {
		return err
	}
	tmp := self + ".new"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, self); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "--no-block", "restart", "powerdeck").CombinedOutput(); err != nil {
		return fmt.Errorf("restart: %v: %s", err, out)
	}
	return nil
}
