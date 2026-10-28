//go:build windows

// Package winsvc adapts a context-driven run function to the Windows Service Control Manager.
package winsvc

import (
	"context"

	"golang.org/x/sys/windows/svc"
)

type handler struct {
	run    func(ctx context.Context) error
	notify func(event string)
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	accepts := svc.AcceptStop | svc.AcceptShutdown
	if h.notify != nil {
		// Pre-shutdown comes while the network is still up, and Windows waits for it.
		accepts |= svc.AcceptPreShutdown
		if stop, err := watchSuspend(h.notify); err == nil {
			defer stop()
		}
	}
	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.PreShutdown, svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				if c.Cmd == svc.PreShutdown {
					h.notify("shutdown")
				}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			if err != nil {
				return true, 1 // non-zero exit triggers the service's restart-on-failure actions
			}
			return false, 0
		}
	}
}

// Run blocks, serving the SCM until the service is stopped. notify, if set, is told "sleep" just
// before the PC sleeps, "resume" when it wakes and "shutdown" before it shuts down or restarts; it
// should return within a second or two, as Windows goes on without it.
func Run(name string, run func(ctx context.Context) error, notify func(event string)) error {
	return svc.Run(name, &handler{run: run, notify: notify})
}
