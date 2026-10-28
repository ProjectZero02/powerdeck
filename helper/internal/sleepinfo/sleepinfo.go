// Package sleepinfo tells a hub whether this machine sleeps on its own, and what kind of sleep it
// has, so a sleeping machine isn't taken for a hung one.
package sleepinfo

import (
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// Cached returns Read, read again at most every 5 minutes (the owner may change the power plan).
func Cached() func() *store.Sleep {
	var (
		mu   sync.Mutex
		last *store.Sleep
		at   time.Time
	)
	return func() *store.Sleep {
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > 5*time.Minute {
			last, at = Read(), time.Now()
		}
		return last
	}
}
