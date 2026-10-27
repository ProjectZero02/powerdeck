// PowerDeck for Windows: a background service that lets paired phones shut down or restart this PC
// over Tailscale, plus a small app window for pairing.
//
//	PowerDeck.exe [open]     open the app window (default)
//	PowerDeck.exe service    run under the Service Control Manager
//	PowerDeck.exe install    set up the service, firewall, Wake-on-LAN etc. (admin; run by the installer)
//	PowerDeck.exe uninstall  remove the service and firewall rule (admin; run by the uninstaller)
//	PowerDeck.exe run        run in the foreground (development)
//	PowerDeck.exe version
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/projectzero02/powerdeck/windows/internal/app"
	"github.com/projectzero02/powerdeck/windows/internal/applog"
	"github.com/projectzero02/powerdeck/windows/internal/setup"
	"github.com/projectzero02/powerdeck/windows/internal/version"
)

func main() {
	cmd := "open"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "open":
		err = openWindow()
	case "service":
		err = runService()
	case "install":
		err = install()
	case "uninstall":
		err = setup.Uninstall()
	case "run":
		err = runForeground(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	default:
		fmt.Fprintln(os.Stderr, "usage: PowerDeck.exe [open|service|install|uninstall|run|version]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "PowerDeck:", err)
		os.Exit(1)
	}
}

// runForeground runs PowerDeck in the console, logging to stderr too. On non-Windows systems power
// actions are only logged.
func runForeground(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", setup.DataDir(), "data directory")
	apiAddr := fs.String("api", "", "phone API listen address (default :47321)")
	uiAddr := fs.String("ui", "", "app window listen address (default 127.0.0.1:47322)")
	_ = fs.Parse(args)
	log, closer, err := applog.Open(*data, os.Stderr)
	if err != nil {
		return err
	}
	defer closer.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return app.Run(ctx, app.Options{DataDir: *data, APIAddr: *apiAddr, UIAddr: *uiAddr, System: newSystem(log), Log: log, Checks: setup.Checks})
}
