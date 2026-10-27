package auth

import (
	"slices"
	"testing"
)

func TestParseScopes(t *testing.T) {
	got, err := ParseScopes("")
	if err != nil || !slices.Equal(got, []string{"status", "wake"}) {
		t.Errorf("default: %v %v", got, err)
	}
	got, err = ParseScopes("power, WAKE,power")
	if err != nil || !slices.Equal(got, []string{"status", "power", "wake"}) {
		t.Errorf("dedupe/normalise: %v %v", got, err)
	}
	for _, bad := range []string{"force", "admin", "status,root"} {
		if _, err := ParseScopes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
