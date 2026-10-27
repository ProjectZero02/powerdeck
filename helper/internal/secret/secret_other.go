//go:build !windows

package secret

import (
	"os"
	"path/filepath"
)

// Default is the platform's box: a key file in /etc/powerdeck (POWERDECK_KEYS overrides the folder;
// with POWERDECK_DATA set for development it's that folder plus "-keys").
func Default() Box {
	dir := "/etc/powerdeck"
	if d := os.Getenv("POWERDECK_KEYS"); d != "" {
		dir = d
	} else if d := os.Getenv("POWERDECK_DATA"); d != "" {
		dir = filepath.Clean(d) + "-keys"
	}
	return &KeyFile{Path: filepath.Join(dir, "secret.key")}
}

// Dir is the folder Default keeps its key in (uninstall removes it with the data).
func Dir() string {
	return filepath.Dir(Default().(*KeyFile).Path)
}
