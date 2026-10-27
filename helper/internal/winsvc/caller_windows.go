//go:build windows

package winsvc

import (
	"encoding/binary"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// LocalCaller reports whether the program behind a loopback connection (remote is the client's
// "ip:port", server the listening port) runs in the active console session, i.e. belongs to the
// person at this PC's screen, or is elevated. Other users signed in to the same PC (fast user
// switching, Remote Desktop) and services are refused.
func LocalCaller(remote string, server uint16) bool {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil || !ap.Addr().Is4() {
		return false
	}
	pid, ok := owningPID(ap, server)
	if !ok {
		return false
	}
	var session uint32
	if windows.ProcessIdToSessionId(pid, &session) == nil && session == windows.WTSGetActiveConsoleSessionId() {
		return true
	}
	return elevated(pid)
}

// owningPID finds the process that owns the client side of a loopback TCP connection.
func owningPID(client netip.AddrPort, server uint16) (uint32, bool) {
	const tcpTableOwnerPIDConnections = 4
	size := uint32(64 << 10)
	for range 3 {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
			windows.AF_INET, tcpTableOwnerPIDConnections, 0)
		if r == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if r != 0 {
			return 0, false
		}
		// MIB_TCPTABLE_OWNER_PID: a DWORD count, then rows of six DWORDs
		// (state, localAddr, localPort, remoteAddr, remotePort, pid); addresses and ports in network order.
		n := binary.LittleEndian.Uint32(buf)
		ip := client.Addr().As4()
		for i := range n {
			row := buf[4+i*24 : 4+(i+1)*24]
			lport := binary.BigEndian.Uint16(row[8:10])
			rport := binary.BigEndian.Uint16(row[16:18])
			if [4]byte(row[4:8]) == ip && lport == client.Port() && rport == server {
				return binary.LittleEndian.Uint32(row[20:24]), true
			}
		}
		return 0, false
	}
	return 0, false
}

func elevated(pid uint32) bool {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(p)
	var t windows.Token
	if windows.OpenProcessToken(p, windows.TOKEN_QUERY, &t) != nil {
		return false
	}
	defer t.Close()
	return t.IsElevated()
}
