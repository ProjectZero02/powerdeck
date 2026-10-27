//go:build !windows

// Package setup is Windows-only; these stubs keep the module building elsewhere for tests and dev.
package setup

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/projectzero02/powerdeck/windows/internal/localui"
)

const ServiceName = "PowerDeck"

// Step mirrors the Windows type.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func DataDir() string {
	if d := os.Getenv("POWERDECK_DATA"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "powerdeck-dev")
}

func Install(string, int) ([]Step, error) { return nil, errors.New("install is Windows-only") }
func Uninstall() error                    { return errors.New("uninstall is Windows-only") }
func ServiceRunning() bool                { return false }
func Checks() []localui.Check {
	return []localui.Check{{Name: "Development build", OK: true, Detail: "power actions are logged, not performed"}}
}
