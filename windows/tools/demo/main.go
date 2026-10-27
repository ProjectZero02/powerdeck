// demo serves the PC app window with sample data on 127.0.0.1:47322, for screenshots and UI work
// without Windows or Tailscale:
//
//	go run ./tools/demo
package main

import (
	"log"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/projectzero02/powerdeck/windows/internal/localui"
	"github.com/projectzero02/powerdeck/windows/internal/netinfo"
	"github.com/projectzero02/powerdeck/windows/internal/pairing"
	"github.com/projectzero02/powerdeck/windows/internal/store"
	"github.com/projectzero02/powerdeck/windows/internal/update"
	"github.com/projectzero02/powerdeck/windows/internal/version"
)

func main() {
	version.Version = "1.0.0"
	dir, err := os.MkdirTemp("", "powerdeck-demo")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	_, _, _ = st.AddDevice("Pixel 9 Pro", "Pixel 9 Pro", time.Now().Add(-26*time.Hour))
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h, err := localui.New(localui.Deps{
		Addr: "127.0.0.1:47322", APIPort: 47321, Name: "Gaming-PC",
		Fingerprint: "s_oOS5kgII0iP7Eakz2-K4i1ozLtGvTC0oHbf_NV1Qk",
		Store:       st, Pairing: pairing.NewManager(time.Now), Update: update.NewManager(dir, logger),
		MAC:       func() string { return "d8:5e:d3:12:34:56" },
		TailnetIP: func() (netip.Addr, bool) { return netip.MustParseAddr("100.101.1.10"), true },
		Discovery: func() netinfo.Discovered {
			return netinfo.Discovered{
				NanoKVM: &netinfo.Found{Address: "100.101.1.20", Name: "server-nanokvm"},
				Proxmox: &netinfo.Found{Address: "100.101.1.30:8006", Name: "pve"},
			}
		},
		Rediscover: func() {},
		Checks: func() []localui.Check {
			return []localui.Check{
				{Name: "Connected to Tailscale", OK: true, Detail: "100.101.1.10"},
				{Name: "Firewall allows the phone (tailnet only)", OK: true},
				{Name: "Wake-on-LAN on Ethernet", OK: true, Detail: "d8:5e:d3:12:34:56 · also needs ErP off and Power On By PCI-E on in the BIOS"},
				{Name: "Fast Startup off (full shutdowns)", OK: true},
				{Name: "Tailscale stays on at the sign-in screen", OK: false, Detail: "In the Tailscale tray menu, turn on Preferences → Run unattended."},
			}
		},
		Log: logger,
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("demo on http://127.0.0.1:47322/")
	log.Fatal(http.ListenAndServe("127.0.0.1:47322", h))
}
