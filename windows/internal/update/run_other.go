//go:build !windows

package update

import "errors"

func runInstaller(string) error { return errors.New("updates install only on Windows") }
