// Package setup installs and removes the helper's system integration, and provides the setup
// checks (with automatic fixes where possible) shown in the app and the PC window.
package setup

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
)

// Step is one install step's outcome, printed by the installer.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Env is what the checks look at.
type Env struct {
	TS      *tailscale.Client
	Store   *store.Store
	Network func() (netinfo.Network, bool)
	Now     func() time.Time
}

func devDataDir() (string, bool) {
	d := os.Getenv("POWERDECK_DATA")
	return d, d != ""
}

func tailscaleCheck(e Env) check.Check {
	return check.Check{ID: "tailscale", Run: func(ctx context.Context) check.Result {
		st, err := e.TS.Status(ctx)
		switch {
		case err != nil:
			return check.Result{Level: check.Error, Title: "Tailscale isn't running", Detail: "Install Tailscale and sign in. PowerDeck works only over your tailnet."}
		case st.BackendState != "Running":
			return check.Result{Level: check.Error, Title: "Tailscale isn't connected", Detail: "Tailscale says " + st.BackendState + ". Open Tailscale and sign in."}
		}
		return check.Result{Level: check.OK, Title: "Connected to Tailscale", Detail: st.Self.Name() + " · " + st.Self.IPv4.String()}
	}}
}

func keyExpiryCheck(e Env) check.Check {
	return check.Check{ID: "key_expiry", Run: func(ctx context.Context) check.Result {
		st, err := e.TS.Status(ctx)
		if err != nil {
			return check.Result{Level: check.Info, Title: "Tailscale key expiry", Detail: "Unknown while Tailscale is off."}
		}
		if st.Self.KeyExpiry.IsZero() {
			return check.Result{Level: check.OK, Title: "Tailscale key never expires"}
		}
		days := int(st.Self.KeyExpiry.Sub(e.Now()).Hours() / 24)
		lvl := check.Warn
		if days < 14 {
			lvl = check.Error
		}
		return check.Result{Level: lvl, Title: fmt.Sprintf("Tailscale key expires in %d days", days),
			Detail: "After that this device drops off your tailnet until someone signs in on it. In the Tailscale admin console → Machines → " +
				st.Self.Name() + " → ⋯ → Disable key expiry."}
	}}
}

func wiredCheck(e Env) check.Check {
	return check.Check{ID: "wired", Run: func(context.Context) check.Result {
		n, ok := e.Network()
		switch {
		case !ok:
			return check.Result{Level: check.Warn, Title: "No network card with internet access found"}
		case !n.Wired:
			return check.Result{Level: check.Warn, Title: "Connected over Wi-Fi",
				Detail: "Wi-Fi can't wake a computer from shutdown. Use Ethernet if you want to turn this device on remotely."}
		}
		return check.Result{Level: check.OK, Title: "Wired network", Detail: n.Interface + " · " + n.MAC}
	}}
}

func batteryCheck(e Env) check.Check {
	return check.Check{ID: "battery", Run: func(context.Context) check.Result {
		if n, ok := e.Network(); ok && n.Battery {
			return check.Result{Level: check.Warn, Title: "Running on battery",
				Detail: "Laptops usually can't be woken remotely unless plugged in (and often only with the lid open)."}
		}
		return check.Result{Level: check.OK, Title: "On mains power"}
	}}
}

func biosCheck() check.Check {
	return check.Check{ID: "bios", Run: func(context.Context) check.Result {
		return check.Result{Level: check.Info, Title: "BIOS: Wake-on-LAN on, ErP/EuP off",
			Detail: "These are BIOS settings PowerDeck can't see (ASUS: ErP Ready Disabled, Power On By PCI-E Enabled). Turn it off, then tap Turn on in the app once to confirm."}
	}}
}

func updatesCheck(e Env) check.Check {
	return check.Check{ID: "updates",
		Run: func(context.Context) check.Result {
			if e.Store.Settings().AutoUpdate {
				return check.Result{Level: check.OK, Title: "Automatic updates on"}
			}
			return check.Result{Level: check.Warn, Title: "Automatic updates off", Detail: "Updates fix bugs and security issues."}
		},
		Fix: func(context.Context) error { return e.Store.SetAutoUpdate(true) },
	}
}

// Fixer runs a check's fix and returns the check's new result.
func Fixer(checks func() []check.Check) func(ctx context.Context, id string) (check.Result, error) {
	return func(ctx context.Context, id string) (check.Result, error) {
		c, ok := check.Find(checks(), id)
		if !ok {
			return check.Result{}, fmt.Errorf("unknown check %q", id)
		}
		if c.Fix == nil {
			return check.Result{}, fmt.Errorf("%q can't be fixed automatically", id)
		}
		if err := c.Fix(ctx); err != nil {
			return check.Result{}, err
		}
		return check.RunAll(ctx, []check.Check{c})[0], nil
	}
}
