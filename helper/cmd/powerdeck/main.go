// The PowerDeck helper: a background service that lets your phone (and agents you allow) turn this
// device off and on over Tailscale. See docs/protocol.md.
//
//	install [--quiet]          set up the service and system settings (admin/root; run by the installers)
//	uninstall [--purge]        remove the service (--purge also deletes keys and settings)
//	status                     show setup checks
//	qr                         show the QR code and pairing code that add this device to the app
//	key add NAME [--scopes status,wake,power] [--devices a,b] [--allow-self]
//	key list | key rm ID       manage automation keys (AI agents, scripts)
//	run [--dry-run]            run in the foreground (development; --dry-run only logs power actions)
//	version
//
// Windows only: open (the PC window, default), service (Service Control Manager), presence (the
// signed-in user's companion).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/projectzero02/powerdeck/helper/internal/app"
	"github.com/projectzero02/powerdeck/helper/internal/applog"
	"github.com/projectzero02/powerdeck/helper/internal/cli"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/setup"
	"github.com/projectzero02/powerdeck/helper/internal/version"
)

func main() {
	cmd := defaultCommand
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	args := []string{}
	if len(os.Args) > 2 {
		args = os.Args[2:]
	}
	var err error
	switch cmd {
	case "install":
		err = install(args)
	case "uninstall":
		fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
		purge := fs.Bool("purge", false, "also delete keys and settings")
		_ = fs.Parse(args)
		err = setup.Uninstall(*purge)
	case "status":
		err = status()
	case "qr":
		err = showQR()
	case "key":
		err = key(args)
	case "run":
		err = runForeground(args)
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	default:
		if !platformCommand(cmd, args, &err) {
			fmt.Fprintln(os.Stderr, "usage: powerdeck-helper install|uninstall|status|qr|key|run|version")
			os.Exit(2)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "PowerDeck:", err)
		os.Exit(1)
	}
}

func client() cli.Client { return cli.Client{DataDir: setup.DataDir(), Port: app.APIPort} }

// runForeground runs the helper in the console, logging to stderr too.
func runForeground(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", setup.DataDir(), "data directory")
	dry := fs.Bool("dry-run", false, "log power actions instead of performing them")
	service := fs.Bool("service", false, "run as the system service (Linux systemd)")
	apiAddr := fs.String("api", "", "API listen address (default :47321)")
	mcpAddr := fs.String("mcp", "", "agent (MCP) listen address (default :47323)")
	uiAddr := fs.String("ui", "", "PC window address (default 127.0.0.1:47322, - to disable)")
	_ = fs.Parse(args)
	log, closer, err := applog.Open(*data, os.Stderr)
	if err != nil {
		return err
	}
	defer closer.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var sys power.System
	if *dry {
		sys = &power.DryRun{Log: log}
	} else {
		sys = newSystem(nil)
	}
	ui := *uiAddr
	if *service && ui == "" {
		ui = "-" // headless
	}
	return app.Run(ctx, app.Options{DataDir: *data, APIAddr: *apiAddr, MCPAddr: *mcpAddr, UIAddr: ui, System: sys, Log: log, Installed: *service && !*dry})
}

func status() error {
	var st struct {
		Name    string
		Version string
		Checks  []cli.Check
	}
	if err := client().Do("GET", "/v2/status", nil, &st); err != nil {
		return err
	}
	fmt.Printf("PowerDeck %s on %s\n\n", st.Version, st.Name)
	cli.PrintChecks(os.Stdout, st.Checks)
	return nil
}

func showQR() error {
	var p struct{ Link, Code string }
	if err := client().Do("GET", "/v2/pairing", nil, &p); err != nil {
		return err
	}
	q, err := cli.QR(p.Link)
	if err != nil {
		return err
	}
	fmt.Print("\nIn the PowerDeck app tap Add devices and scan:\n\n")
	fmt.Print(q)
	fmt.Printf("\nPhone on a different Tailscale account? Enter the code %s (works once, 10 minutes).\n", p.Code)
	return nil
}

func key(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		var r struct {
			Keys []struct {
				ID, Name, Kind  string
				Scopes, Devices []string
				Bound           bool
			}
		}
		if err := client().Do("GET", "/v2/keys", nil, &r); err != nil {
			return err
		}
		if len(r.Keys) == 0 {
			fmt.Println("No keys. Your own devices are trusted through your Tailscale account.")
		}
		for _, k := range r.Keys {
			fmt.Printf("%s  %-20s %-10s %s  %s\n", k.ID, k.Name, k.Kind, strings.Join(k.Scopes, ","), strings.Join(k.Devices, ","))
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("key add", flag.ExitOnError)
		scopes := fs.String("scopes", "status,wake", "status, wake, power (comma-separated)")
		devices := fs.String("devices", "", "devices this key may control (comma-separated; default all)")
		allowSelf := fs.Bool("allow-self", false, "let the agent shut down the device it runs on")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("usage: key add NAME [--scopes status,wake,power] [--devices a,b]")
		}
		_ = fs.Parse(args[2:])
		body := map[string]any{"name": args[1], "scopes": split(*scopes), "devices": split(*devices), "allowSelf": *allowSelf}
		var r struct{ ID, Key string }
		if err := client().Do("POST", "/v2/keys", body, &r); err != nil {
			return err
		}
		host, _ := os.Hostname()
		fmt.Printf("Key for %s (shown once):\n\n  %s\n\n", args[1], r.Key)
		fmt.Printf("MCP: http://<%s's Tailscale address>:%d/mcp with header Authorization: Bearer <key>\n", host, app.MCPPort)
		fmt.Println("It only works from the first Tailscale device that uses it. Remove it with: key rm " + r.ID)
		return nil
	case "rm", "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: key rm ID")
		}
		return client().Do("DELETE", "/v2/keys/"+args[1], nil, nil)
	default:
		return fmt.Errorf("usage: key add|list|rm")
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
