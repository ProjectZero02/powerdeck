//go:build linux

package netinfo

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const sysNet = "/sys/class/net"

// defaultRouteInterface reads /proc/net/route for the IPv4 default route with the lowest metric.
func defaultRouteInterface() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	best, bestMetric := "", -1
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 8 || fs[1] != "00000000" || fs[7] != "00000000" {
			continue
		}
		var metric int
		for _, c := range fs[6] {
			metric = metric*10 + int(c-'0')
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = fs[0], metric
		}
	}
	return best
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// physical reports a real network device (not virtual, not Wi-Fi when wired is asked).
func physical(name string) bool { return exists(filepath.Join(sysNet, name, "device")) }

func wireless(name string) bool {
	return exists(filepath.Join(sysNet, name, "wireless")) || exists(filepath.Join(sysNet, name, "phy80211"))
}

// lowerPort finds the physical card under a bridge or bond (Proxmox's vmbr0, bond0), preferring
// one that's up.
func lowerPort(name string) string {
	if physical(name) {
		return name
	}
	for _, sub := range []string{"brif", "bonding/slaves"} {
		dir := filepath.Join(sysNet, name, sub)
		var ports []string
		if sub == "brif" {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				ports = append(ports, e.Name())
			}
		} else if b, err := os.ReadFile(dir); err == nil {
			ports = strings.Fields(string(b))
		}
		var fallback string
		for _, p := range ports {
			if q := lowerPort(p); q != "" {
				if st, _ := os.ReadFile(filepath.Join(sysNet, q, "operstate")); strings.TrimSpace(string(st)) == "up" {
					return q
				}
				if fallback == "" {
					fallback = q
				}
			}
		}
		if fallback != "" {
			return fallback
		}
	}
	return ""
}

// Primary returns the card that carries the default route (through bridges/bonds).
func Primary() (Network, bool) {
	route := defaultRouteInterface()
	if route == "" {
		return Network{}, false
	}
	n := Network{Interface: route}
	if ifc, err := net.InterfaceByName(route); err == nil {
		addrs, _ := ifc.Addrs()
		n.LAN = subnets(addrs)
	}
	port := lowerPort(route)
	if port == "" {
		port = route
	}
	n.Interface = port
	if ifc, err := net.InterfaceByName(port); err == nil && len(ifc.HardwareAddr) == 6 {
		n.MAC = ifc.HardwareAddr.String()
	}
	n.Wired = physical(port) && !wireless(port)
	n.Battery = onBattery()
	return n, n.MAC != ""
}

func onBattery() bool {
	ents, _ := os.ReadDir("/sys/class/power_supply")
	for _, e := range ents {
		t, _ := os.ReadFile(filepath.Join("/sys/class/power_supply", e.Name(), "type"))
		if strings.TrimSpace(string(t)) != "Mains" {
			continue
		}
		on, _ := os.ReadFile(filepath.Join("/sys/class/power_supply", e.Name(), "online"))
		return strings.TrimSpace(string(on)) == "0"
	}
	return false
}
