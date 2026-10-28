//go:build !windows

package sleepinfo

import "github.com/projectzero02/powerdeck/helper/internal/store"

// Read is nil here: on Linux the desktop decides when to sleep, out of a service's sight. A hub
// treats an unknown machine as one that doesn't sleep on its own; a sleep note still counts.
func Read() *store.Sleep { return nil }
