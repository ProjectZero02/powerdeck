package winsvc

import (
	"net"
	"net/netip"
	"os"
	"testing"
)

// The test runs as an administrator (CI), in or outside the console session: either way LocalCaller
// must find this process behind a real loopback connection and let it in.
func TestLocalCallerFindsTheProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	server := uint16(ln.Addr().(*net.TCPAddr).Port)
	client := netip.MustParseAddrPort(c.LocalAddr().String())
	pid, ok := owningPID(client, server)
	if !ok || pid != uint32(os.Getpid()) {
		t.Fatalf("owner %d %v, want %d", pid, ok, os.Getpid())
	}
	if !administrator(pid) {
		t.Skip("not running as an administrator")
	}
	if !LocalCaller(client.String(), server) {
		t.Error("an administrator was refused")
	}
	if LocalCaller("127.0.0.1:1", server) {
		t.Error("a connection nobody owns was let in")
	}
}
