//go:build linux

package power

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Linux schedules with its own timer (so delays are exact and cancellable) and then runs
// `systemctl poweroff` / `reboot`. On Proxmox that stops guests cleanly first (pve-guests).
type Linux struct {
	mu    sync.Mutex
	timer *time.Timer
}

func wall(msg string) {
	cmd := exec.Command("wall")
	cmd.Stdin = strings.NewReader(msg + "\n")
	_ = cmd.Run()
}

func (l *Linux) Schedule(a Action, delay time.Duration, message string, _ bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		return ErrPending
	}
	verb := "poweroff"
	if a == Restart {
		verb = "reboot"
	}
	wall(fmt.Sprintf("%s (in %d s)", message, int(delay.Seconds())))
	l.timer = time.AfterFunc(delay, func() {
		if err := exec.Command("systemctl", verb).Run(); err != nil {
			_ = exec.Command("shutdown", map[string]string{"poweroff": "-P", "reboot": "-r"}[verb], "now").Run()
		}
	})
	return nil
}

func (l *Linux) Abort() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer == nil || !l.timer.Stop() {
		l.timer = nil
		return ErrNothingPending
	}
	l.timer = nil
	wall("PowerDeck: the scheduled shutdown/restart was cancelled.")
	return nil
}

func (l *Linux) BootTime() (time.Time, error) {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(-time.Duration(si.Uptime) * time.Second), nil
}

// Presence counts logged-in user sessions (logind). A session is active when its terminal saw
// input in the last few minutes, or logind doesn't consider it idle.
func (l *Linux) Presence() *Presence {
	out, err := exec.Command("loginctl", "list-sessions", "--json=short").Output()
	if err != nil {
		return nil
	}
	return parseSessions(out, func(tty string) (time.Time, bool) {
		var st syscall.Stat_t
		if syscall.Stat("/dev/"+tty, &st) != nil {
			return time.Time{}, false
		}
		return time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)), true
	}, time.Now())
}

func parseSessions(b []byte, ttyInput func(string) (time.Time, bool), now time.Time) *Presence {
	var sessions []struct {
		User  string `json:"user"`
		Class string `json:"class"`
		TTY   string `json:"tty"`
		Idle  bool   `json:"idle"`
		Since *int64 `json:"since"` // µs since the epoch, when idle
	}
	if json.Unmarshal(bytes.TrimSpace(b), &sessions) != nil {
		return nil
	}
	p := &Presence{IdleSeconds: -1}
	for _, s := range sessions {
		if s.Class != "user" {
			continue
		}
		p.Sessions++
		idle := 0
		if s.TTY != "" && s.TTY != "-" {
			if at, ok := ttyInput(s.TTY); ok {
				idle = max(0, int(now.Sub(at).Seconds()))
			}
		} else if s.Idle && s.Since != nil {
			idle = max(0, int(now.Sub(time.UnixMicro(*s.Since)).Seconds()))
		}
		if p.IdleSeconds < 0 || idle < p.IdleSeconds {
			p.IdleSeconds, p.User = idle, s.User
		}
	}
	if p.Sessions == 0 {
		return &Presence{}
	}
	p.Active = time.Duration(p.IdleSeconds)*time.Second < ActiveWithin
	return p
}

func (l *Linux) UpdatesPending() bool {
	_, err := os.Stat("/var/run/reboot-required")
	return err == nil
}
