// demo serves the PC app window with sample data on 127.0.0.1:47322, for screenshots and UI work
// without Windows or Tailscale:
//
//	go run ./tools/demo
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/localui"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/pairing"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/update"
	"github.com/projectzero02/powerdeck/helper/internal/version"
)

func main() {
	version.Version = "2.0.0"
	dir, err := os.MkdirTemp("", "powerdeck-demo")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	now := time.Now()
	_, _, _ = st.AddKey(store.NewKey{Name: "Atlas", Kind: store.KindAutomation, Scopes: []string{"status", "wake", "power"}, Devices: []string{"Gaming-PC"}}, now.Add(-26*time.Hour))
	_ = st.PutPeer(store.Peer{Name: "pve", Address: "100.101.1.30", Port: 47321, Pin: "x"})
	st.AddEvent(store.Event{At: now.Add(-3 * time.Hour), Who: "Pixel 9 Pro", Action: "restart", Result: "scheduled"})
	st.AddEvent(store.Event{At: now.Add(-40 * time.Minute), Who: "Atlas via pve", Action: "wake Gaming-PC", Result: "sent"})
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h, err := localui.New(localui.Deps{
		Addr: "127.0.0.1:47322", APIPort: 47321, Name: "Gaming-PC",
		Fingerprint: "s_oOS5kgII0iP7Eakz2-K4i1ozLtGvTC0oHbf_NV1Qk",
		Store:       st, Pairing: pairing.NewManager(time.Now), Update: update.NewManager(dir, logger),
		Network: func() (netinfo.Network, bool) {
			return netinfo.Network{Interface: "Ethernet", MAC: "d8:5e:d3:12:34:56", LAN: []string{"192.168.1.0/24"}, Wired: true}, true
		},
		TailnetIP: func() (netip.Addr, bool) { return netip.MustParseAddr("100.101.1.10"), true },
		Checks: func(context.Context) []check.Result {
			return []check.Result{
				{ID: "tailscale", Level: check.OK, Title: "Connected to Tailscale", Detail: "gaming-pc · 100.101.1.10"},
				{ID: "key_expiry", Level: check.Warn, Title: "Tailscale key expires in 58 days",
					Detail: "After that this device drops off your tailnet until someone signs in on it. In the Tailscale admin console → Machines → gaming-pc → ⋯ → Disable key expiry."},
				{ID: "unattended", Level: check.Error, Title: "Tailscale stops when you sign out", Detail: "Turn on Run unattended (Tailscale tray menu → Preferences), or press Fix.", Fixable: true},
				{ID: "firewall", Level: check.OK, Title: "Firewall allows PowerDeck (tailnet only)"},
				{ID: "wired", Level: check.OK, Title: "Wired network", Detail: "Ethernet · d8:5e:d3:12:34:56"},
				{ID: "wol_nic", Level: check.OK, Title: "Wake-on-LAN on Ethernet", Detail: "d8:5e:d3:12:34:56"},
				{ID: "fast_startup", Level: check.OK, Title: "Fast Startup off"},
				{ID: "bios", Level: check.Info, Title: "BIOS: Wake-on-LAN on, ErP/EuP off", Detail: "These are BIOS settings PowerDeck can't see. Turn it off, then tap Turn on in the app once to confirm."},
				{ID: "updates", Level: check.OK, Title: "Automatic updates on"},
			}
		},
		Fix:     func(context.Context, string) (check.Result, error) { return check.Result{Level: check.OK}, nil },
		Reports: &power.Reports{},
		Log:     logger,
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("demo on http://127.0.0.1:47322/")
	log.Fatal(http.ListenAndServe("127.0.0.1:47322", h))
}
