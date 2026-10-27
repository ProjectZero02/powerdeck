//go:build !windows

package netinfo

import (
	"net"
	"os/exec"
)

func tailscaleCLI() string { return "tailscale" }

func hideWindow(*exec.Cmd) {}

// Adapter is a network adapter that can be woken with a magic packet.
type Adapter struct {
	Name        string
	Description string
	MAC         string
}

// PrimaryWiredAdapter is a development stand-in: the first up, non-loopback interface with an IPv4
// private address.
func PrimaryWiredAdapter() (Adapter, bool) {
	ifs, err := net.Interfaces()
	if err != nil {
		return Adapter{}, false
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || len(i.HardwareAddr) != 6 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				return Adapter{Name: i.Name, Description: i.Name, MAC: i.HardwareAddr.String()}, true
			}
		}
	}
	return Adapter{}, false
}
