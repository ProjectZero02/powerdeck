// Package guests reports running virtual machines and containers on a Proxmox host, so the phone
// can warn before shutting the host down.
package guests

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Info is what's running.
type Info struct {
	Running int      `json:"running"`
	Names   []string `json:"names,omitempty"`
}

// Available reports whether this is a Proxmox host.
func Available() bool {
	_, err := os.Stat("/usr/sbin/qm")
	return err == nil
}

// Running lists running VMs (qm) and containers (pct). Nil when this isn't a Proxmox host.
func Running(ctx context.Context) *Info {
	if !Available() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info := &Info{}
	if out, err := exec.CommandContext(ctx, "/usr/sbin/qm", "list").Output(); err == nil {
		info.add(ParseQM(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "/usr/sbin/pct", "list").Output(); err == nil {
		info.add(ParsePCT(string(out)))
	}
	return info
}

// Cached wraps list so it runs at most once per ttl, and once at a time: qm and pct take about a
// second each, and every phone polls status every few seconds.
func Cached(ttl time.Duration, list func(context.Context) *Info) func(context.Context) *Info {
	var (
		mu   sync.Mutex
		last *Info
		at   time.Time
	)
	return func(ctx context.Context) *Info {
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > ttl {
			last, at = list(context.WithoutCancel(ctx)), time.Now() // a dropped request mustn't cache "none running"
		}
		return last
	}
}

func (i *Info) add(names []string) {
	i.Running += len(names)
	i.Names = append(i.Names, names...)
}

// ParseQM returns names of running VMs from `qm list` (VMID NAME STATUS MEM BOOTDISK PID).
func ParseQM(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 3 && f[2] == "running" {
			names = append(names, f[1])
		}
	}
	return names
}

// ParsePCT returns names of running containers from `pct list` (VMID Status [Lock] Name).
func ParsePCT(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == "running" {
			names = append(names, f[len(f)-1])
		}
	}
	return names
}
