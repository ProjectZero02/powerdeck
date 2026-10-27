//go:build windows

package netinfo

import (
	"net"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ifTypeWiFi = 71 // IF_TYPE_IEEE80211

func virtualAdapter(desc string) bool {
	d := strings.ToLower(desc)
	for _, s := range []string{"virtual", "hyper-v", "vmware", "virtualbox", "tap-", "wintun", "tailscale", "wireguard", "vpn", "docker", "loopback", "bluetooth"} {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

// Primary returns the physical adapter that carries the default route, preferring Ethernet over
// Wi-Fi and the lowest route metric.
func Primary() (Network, bool) {
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
			return Network{}, false
		}
	}
	var best *windows.IpAdapterAddresses
	better := func(a *windows.IpAdapterAddresses) bool {
		if best == nil {
			return true
		}
		aw, bw := a.IfType == windows.IF_TYPE_ETHERNET_CSMACD, best.IfType == windows.IF_TYPE_ETHERNET_CSMACD
		if aw != bw {
			return aw
		}
		return a.Ipv4Metric < best.Ipv4Metric
	}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if (a.IfType != windows.IF_TYPE_ETHERNET_CSMACD && a.IfType != ifTypeWiFi) || a.OperStatus != windows.IfOperStatusUp ||
			a.PhysicalAddressLength != 6 || a.FirstGatewayAddress == nil || virtualAdapter(windows.UTF16PtrToString(a.Description)) {
			continue
		}
		if better(a) {
			best = a
		}
	}
	if best == nil {
		return Network{}, false
	}
	n := Network{
		Interface: windows.UTF16PtrToString(best.FriendlyName),
		MAC:       net.HardwareAddr(best.PhysicalAddress[:6]).String(),
		Wired:     best.IfType == windows.IF_TYPE_ETHERNET_CSMACD,
		Battery:   onBattery(),
	}
	var addrs []net.Addr
	for u := best.FirstUnicastAddress; u != nil; u = u.Next {
		ip := u.Address.IP()
		if ip.To4() == nil {
			continue
		}
		addrs = append(addrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(int(u.OnLinkPrefixLength), 32)})
	}
	n.LAN = subnets(addrs)
	return n, true
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

func onBattery() bool {
	var st struct {
		ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag byte
		BatteryLifeTime, BatteryFullLifeTime                            uint32
	}
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st)))
	return r != 0 && st.ACLineStatus == 0 && st.BatteryFlag != 128 // 128 = no battery
}
