package listen

import (
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func dial(port int, ip string) error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), time.Second)
	if err == nil {
		_ = c.Close()
	}
	return err
}

func TestFollowsAddresses(t *testing.T) {
	var mu sync.Mutex
	set := []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	addrs := func() []netip.Addr { mu.Lock(); defer mu.Unlock(); return append([]netip.Addr(nil), set...) }
	port := freePort(t)
	m, err := New(port, addrs, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	go func() {
		for {
			c, err := m.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if err := dial(port, "127.0.0.1"); err != nil {
		t.Fatalf("loopback: %v", err)
	}
	// 127.0.0.2 stands in for a Tailscale address: not bound, so nothing answers there.
	if dial(port, "127.0.0.2") == nil {
		t.Fatal("answered on an address it wasn't given")
	}
	mu.Lock()
	set = append(set, netip.MustParseAddr("127.0.0.2"))
	mu.Unlock()
	waitFor(t, func() bool { return dial(port, "127.0.0.2") == nil }, "the new address")
	mu.Lock()
	set = set[:1]
	mu.Unlock()
	waitFor(t, func() bool { return dial(port, "127.0.0.2") != nil }, "the address to be dropped")
	if got := m.Bound(); len(got) != 1 || got[0].String() != "127.0.0.1" {
		t.Errorf("bound: %v", got)
	}
}

func TestCloseUnblocksAccept(t *testing.T) {
	m, err := New(freePort(t), func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error)
	go func() { _, err := m.Accept(); errc <- err }()
	_ = m.Close()
	select {
	case err := <-errc:
		if err != net.ErrClosed {
			t.Errorf("accept after close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept still blocked")
	}
}

func TestPortTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := New(port, func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("bound a taken port")
	}
}

func TestDefaultAddrsAreNeverWildcard(t *testing.T) {
	for _, a := range Addrs() {
		if a.IsUnspecified() || !(a.IsLoopback() || a.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(a) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(a)) {
			t.Errorf("default address %s", a)
		}
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A listener that fails on its own (not closed by us) is bound again on the next check.
func TestRebindsFailedListener(t *testing.T) {
	a := netip.MustParseAddr("127.0.0.1")
	port := freePort(t)
	m, err := New(port, func() []netip.Addr { return []netip.Addr{a} }, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	go func() {
		for {
			c, err := m.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	m.mu.Lock()
	old := m.bound[a]
	m.mu.Unlock()
	_ = old.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		ln := m.bound[a]
		m.mu.Unlock()
		if ln != nil && ln != old && dial(port, "127.0.0.1") == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("not bound again after the listener failed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
