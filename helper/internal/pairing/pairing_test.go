package pairing

import (
	"regexp"
	"testing"
	"time"
)

func TestCodeFormatAndOneTimeUse(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	m := NewManager(func() time.Time { return now })
	c := m.Current()
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`).MatchString(c.Value) {
		t.Fatalf("code %q", c.Value)
	}
	typed := c.Value[:4] + " " + c.Value[5:] // spaces and lower case are fine
	if !m.Consume(lower(typed)) {
		t.Fatal("typed code refused")
	}
	if m.Consume(c.Value) {
		t.Fatal("code reused")
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestGuessesAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	m := NewManager(func() time.Time { return now })
	c := m.Current()
	for i := 0; i < MaxFailures; i++ {
		m.Consume("0000-0000")
	}
	if m.Consume(c.Value) {
		t.Fatal("code survived too many guesses")
	}
	c = m.Current()
	now = now.Add(TTL + time.Second)
	if m.Consume(c.Value) {
		t.Fatal("expired code accepted")
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("ab1o-il2z"); got != "AB10-112Z" {
		t.Errorf("got %q", got)
	}
}
