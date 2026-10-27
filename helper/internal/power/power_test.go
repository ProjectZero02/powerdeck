package power

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type sys struct {
	DryRun
	p *Presence
}

func (s *sys) Presence() *Presence { return s.p }

func TestInUseBlocksUnlessForced(t *testing.T) {
	s := &sys{p: &Presence{Active: true, User: "Alex"}}
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()
	c := NewController(s, func() time.Time { return now })
	if _, err := c.Do(Shutdown, Options{Delay: time.Minute, By: "phone"}); !errors.Is(err, ErrInUse) {
		t.Fatalf("got %v", err)
	}
	p, err := c.Do(Shutdown, Options{Delay: time.Minute, By: "phone", Force: true})
	if err != nil || p.By != "phone" || !p.At.Equal(now.Add(time.Minute).UTC()) {
		t.Fatalf("forced: %+v %v", p, err)
	}
}

func TestReportsExpire(t *testing.T) {
	now := time.Now()
	r := &Reports{Now: func() time.Time { return now }}
	r.Put(Presence{IdleSeconds: 10})
	if p := r.Fresh(); p == nil || !p.Active {
		t.Fatalf("%+v", p)
	}
	r.Put(Presence{IdleSeconds: 10, Locked: true})
	if p := r.Fresh(); p.Active {
		t.Error("locked counts as active")
	}
	now = now.Add(2 * time.Minute)
	if r.Fresh() != nil {
		t.Error("stale report used")
	}
}

func TestNoCountdownDuringUpdate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewController(&DryRun{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, func() time.Time { return now })
	if why := c.BeginUpdate(); why != "" {
		t.Fatal(why)
	}
	if _, err := c.Do(Shutdown, Options{Delay: time.Minute, By: "x"}); !errors.Is(err, ErrUpdating) {
		t.Fatalf("got %v, want ErrUpdating", err)
	}
	now = now.Add(updateHold + time.Second) // the update never came back
	if _, err := c.Do(Shutdown, Options{Delay: time.Minute, By: "x"}); err != nil {
		t.Fatal(err)
	}
	if why := c.BeginUpdate(); why == "" {
		t.Error("update claimed the device during a countdown")
	}
}
