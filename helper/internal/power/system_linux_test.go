//go:build linux

package power

import (
	"testing"
	"time"
)

func TestParseSessions(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	tty := map[string]time.Time{"pts/0": now.Add(-30 * time.Second), "pts/1": now.Add(-2 * time.Hour)}
	in := func(name string) (time.Time, bool) { t, ok := tty[name]; return t, ok }
	p := parseSessions([]byte(`[{"session":"3","user":"admin","class":"user","tty":"pts/1"},
		{"session":"5","user":"root","class":"user","tty":"pts/0"},
		{"session":"c1","user":"gdm","class":"greeter","tty":"tty1"}]`), in, now)
	if p == nil || !p.Active || p.User != "root" || p.Sessions != 2 || p.IdleSeconds != 30 {
		t.Fatalf("%+v", p)
	}
	p = parseSessions([]byte(`[{"session":"3","user":"admin","class":"user","tty":"pts/1"}]`), in, now)
	if p.Active || p.IdleSeconds != 7200 {
		t.Errorf("idle ssh session counted as active: %+v", p)
	}
	if p := parseSessions([]byte(`[]`), in, now); p == nil || p.Active || p.Sessions != 0 {
		t.Errorf("nobody: %+v", p)
	}
	if parseSessions([]byte(`not json`), in, now) != nil {
		t.Error("garbage should mean unknown")
	}
}
