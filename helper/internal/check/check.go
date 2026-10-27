// Package check describes setup checks: things that must be true for remote power control to work
// reliably, each with an optional automatic fix.
package check

import "context"

// Level of a check result.
type Level string

const (
	OK    Level = "ok"
	Warn  Level = "warn"
	Error Level = "error"
	Info  Level = "info"
)

// Result is one check as shown to the user.
type Result struct {
	ID      string `json:"id"`
	Level   Level  `json:"level"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Fixable bool   `json:"fixable,omitempty"`
}

// Check computes a result and can optionally fix it.
type Check struct {
	ID  string
	Run func(ctx context.Context) Result
	Fix func(ctx context.Context) error // nil when there's no automatic fix
}

// RunAll evaluates checks in order, filling in IDs and fixability.
func RunAll(ctx context.Context, cs []Check) []Result {
	out := make([]Result, 0, len(cs))
	for _, c := range cs {
		r := c.Run(ctx)
		r.ID = c.ID
		r.Fixable = c.Fix != nil && r.Level != OK && r.Level != Info
		out = append(out, r)
	}
	return out
}

// Find returns the check with this ID.
func Find(cs []Check, id string) (Check, bool) {
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}
