//go:build linux

package wol

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ethtool ioctl (linux/ethtool.h): struct ethtool_wolinfo { u32 cmd, supported, wolopts; u8 sopass[6]; }
const (
	ethtoolGWOL = 0x5
	ethtoolSWOL = 0x6
	wakeMagic   = 1 << 5 // WAKE_MAGIC
)

type wolinfo struct {
	Cmd, Supported, Wolopts uint32
	Sopass                  [6]byte
}

type ifreq struct {
	Name [unix.IFNAMSIZ]byte
	Data uintptr
	_    [16]byte
}

func ethtool(iface string, w *wolinfo) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var r ifreq
	copy(r.Name[:], iface)
	r.Data = uintptr(unsafe.Pointer(w))
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&r)))
	if e != 0 {
		return e
	}
	return nil
}

// ErrUnsupported means the card can't wake on a magic packet.
var ErrUnsupported = errors.New("this network card doesn't support Wake-on-LAN by magic packet")

// Armed reports whether magic-packet wake is on for iface.
func Armed(iface string) (bool, error) {
	w := wolinfo{Cmd: ethtoolGWOL}
	if err := ethtool(iface, &w); err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) {
			return false, ErrUnsupported // e.g. virtual cards
		}
		return false, err
	}
	if w.Supported&wakeMagic == 0 {
		return false, ErrUnsupported
	}
	return w.Wolopts&wakeMagic != 0, nil
}

// Arm turns on magic-packet wake for iface (needs root). Linux forgets this on reboot on many
// drivers, so the helper re-arms at every start.
func Arm(iface string) error {
	on, err := Armed(iface)
	if err != nil || on {
		return err
	}
	w := wolinfo{Cmd: ethtoolSWOL, Wolopts: wakeMagic}
	return ethtool(iface, &w)
}
