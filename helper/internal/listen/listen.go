// Package listen binds the helper's ports to this device's Tailscale addresses and loopback only,
// never to every interface: the LAN, Wi-Fi and other VPNs can't even open a connection. Tailscale
// may start after the helper or change address, so the set is checked again every few seconds.
package listen

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
)

// Addrs is the default address set: loopback plus the Tailscale addresses this device has now.
func Addrs() []netip.Addr {
	return append([]netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}, netinfo.TailnetAddrs()...)
}

// Multi is a net.Listener over a changing set of addresses on one port.
type Multi struct {
	port  int
	addrs func() []netip.Addr
	log   *slog.Logger

	conns chan net.Conn
	done  chan struct{}
	once  sync.Once

	mu    sync.Mutex
	bound map[netip.Addr]net.Listener
	warn  map[netip.Addr]bool
}

// New binds port on the addresses addrs returns and re-checks them every interval. It fails only
// when not even one address (loopback) can be bound, e.g. the port is taken.
func New(port int, addrs func() []netip.Addr, interval time.Duration, log *slog.Logger) (*Multi, error) {
	m := &Multi{port: port, addrs: addrs, log: log, conns: make(chan net.Conn), done: make(chan struct{}),
		bound: map[netip.Addr]net.Listener{}, warn: map[netip.Addr]bool{}}
	if err := m.sync(); err != nil {
		return nil, err
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-m.done:
				return
			case <-t.C:
				_ = m.sync()
			}
		}
	}()
	return m, nil
}

// sync binds new addresses and closes ones that went away.
func (m *Multi) sync() error {
	want := m.addrs()
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.done:
		return net.ErrClosed
	default:
	}
	var firstErr error
	for _, a := range want {
		if _, ok := m.bound[a]; ok {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(a.String(), strconv.Itoa(m.port)))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if !m.warn[a] { // once per address, not every few seconds
				m.warn[a] = true
				m.log.Warn("can't listen", "addr", a, "port", m.port, "err", err)
			}
			continue
		}
		delete(m.warn, a)
		m.bound[a] = ln
		m.log.Debug("listening", "addr", ln.Addr())
		go m.accept(a, ln)
	}
	for a, ln := range m.bound {
		if !slices.Contains(want, a) {
			_ = ln.Close()
			delete(m.bound, a)
			m.log.Info("stopped listening (address gone)", "addr", a, "port", m.port)
		}
	}
	if len(m.bound) == 0 {
		if firstErr == nil {
			firstErr = errors.New("no address to listen on")
		}
		return firstErr
	}
	return nil
}

// accept forwards connections until ln fails. If it fails on its own (not closed by sync or
// Close), the address is forgotten so the next sync binds it again.
func (m *Multi) accept(a netip.Addr, ln net.Listener) {
	defer func() {
		m.mu.Lock()
		if m.bound[a] == ln {
			_ = ln.Close()
			delete(m.bound, a)
		}
		m.mu.Unlock()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return // closed (address gone, or Close)
		}
		select {
		case m.conns <- c:
		case <-m.done:
			_ = c.Close()
			return
		}
	}
}

// Accept waits for a connection on any bound address.
func (m *Multi) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

// Close stops listening everywhere.
func (m *Multi) Close() error {
	m.once.Do(func() {
		close(m.done)
		m.mu.Lock()
		defer m.mu.Unlock()
		for a, ln := range m.bound {
			_ = ln.Close()
			delete(m.bound, a)
		}
	})
	return nil
}

// Addr is the first bound address (net.Listener needs one).
func (m *Multi) Addr() net.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ln := range m.bound {
		return ln.Addr()
	}
	return &net.TCPAddr{Port: m.port}
}

// Bound lists the addresses being listened on now.
func (m *Multi) Bound() []netip.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]netip.Addr, 0, len(m.bound))
	for a := range m.bound {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}
