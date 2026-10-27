//go:build windows

package netinfo

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func tailscaleCLI() string {
	p := filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "tailscale.exe"
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// Adapter is a network adapter that can be woken with a magic packet.
type Adapter struct {
	Name        string // friendly name, e.g. "Ethernet"
	Description string
	MAC         string // aa:bb:cc:dd:ee:ff
}

// PrimaryWiredAdapter returns the up, physical Ethernet adapter that carries the default route.
func PrimaryWiredAdapter() (Adapter, bool) {
	var size uint32 = 15000
	var buf []byte
	for i := 0; i < 3; i++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_GATEWAYS,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return Adapter{}, false
		}
	}
	var best *windows.IpAdapterAddresses
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.IfType != windows.IF_TYPE_ETHERNET_CSMACD || a.OperStatus != windows.IfOperStatusUp ||
			a.PhysicalAddressLength != 6 || a.FirstGatewayAddress == nil {
			continue
		}
		desc := strings.ToLower(windows.UTF16PtrToString(a.Description))
		if strings.Contains(desc, "virtual") || strings.Contains(desc, "hyper-v") || strings.Contains(desc, "vmware") ||
			strings.Contains(desc, "virtualbox") || strings.Contains(desc, "tap-") || strings.Contains(desc, "wintun") {
			continue
		}
		if best == nil || a.Ipv4Metric < best.Ipv4Metric {
			best = a
		}
	}
	if best == nil {
		return Adapter{}, false
	}
	return Adapter{
		Name:        windows.UTF16PtrToString(best.FriendlyName),
		Description: windows.UTF16PtrToString(best.Description),
		MAC:         net.HardwareAddr(best.PhysicalAddress[:6]).String(),
	}, true
}
