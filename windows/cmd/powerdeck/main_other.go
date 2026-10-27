//go:build !windows

package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/projectzero02/powerdeck/windows/internal/app"
	"github.com/projectzero02/powerdeck/windows/internal/power"
)

func newSystem(log *slog.Logger) power.System { return &power.DryRun{Log: log} }

func runService() error { return errors.New("service mode is Windows-only") }
func install() error    { return errors.New("install is Windows-only") }

func openWindow() error {
	fmt.Println("Open http://" + app.UIAddr + "/ (start it with: powerdeck run)")
	return nil
}
