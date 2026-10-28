// The PowerDeck helper: a background service that lets your phone (and agents you allow) turn this
// device off and on over Tailscale. See docs/protocol.md.
//
//	install [--quiet]          set up the service and system settings (admin/root; run by the installers)
//	uninstall [--purge]        remove the service (--purge also deletes keys and settings)
//	status                     show setup checks
//	qr                         show the QR code and pairing code that add this device to the app
//	key add NAME [--scopes status,wake,power] [--devices a,b] [--allow-self]
//	key list | key rm ID       manage automation keys (AI agents, scripts)
//	owner list | owner add NAME | owner rm NODE
//	                           the owner devices (phones, computers) that manage this device
//	webhook add URL [--events hung,recovered,recovery] | webhook list | webhook test ID | webhook rm ID
//	                           where a hub posts machine events (an assistant's endpoint)
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
	"time"

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
	case "owner":
		err = owner(args)
	case "webhook":
		err = webhook(args)
	case "run":
		err = runForeground(args)
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	default:
		if !platformCommand(cmd, args, &err) {
			fmt.Fprintln(os.Stderr, "usage: powerdeck-helper install|uninstall|status|qr|key|owner|webhook|run|version")
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
	return app.Run(ctx, app.Options{DataDir: *data, APIAddr: *apiAddr, MCPAddr: *mcpAddr, UIAddr: ui, System: sys, Log: log, Installed: *service && !*dry,
		Stopping: systemStopping})
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
	fmt.Print("\nIn the PowerDeck app tap Add devices and scan (works once, for 10 minutes):\n\n")
	fmt.Print(q)
	fmt.Printf("\nOr enter the code %s in the app.\n", p.Code)
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
			fmt.Println("No keys. Owner devices need none (see: owner list).")
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

func owner(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		var r struct {
			Legacy bool
			Owners []struct {
				Node, Name, AddedBy string
				AddedAt             time.Time
			}
		}
		if err := client().Do("GET", "/v2/owners", nil, &r); err != nil {
			return err
		}
		if r.Legacy {
			fmt.Println("No owner devices yet: until one is recorded, every personal device on this")
			fmt.Println("device's Tailscale account counts as an owner (how PowerDeck 2.0 worked).")
			fmt.Println("Open the updated app, or run: owner add <your phone's Tailscale name>")
			return nil
		}
		if len(r.Owners) == 0 {
			fmt.Println("No owner devices. Scan the code from `qr` in the app, or run: owner add NAME")
		}
		for _, o := range r.Owners {
			fmt.Printf("%-16s %-28s added %s by %s\n", o.Node, o.Name, o.AddedAt.Local().Format("2006-01-02"), o.AddedBy)
		}
		return nil
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: owner add NAME (the device's Tailscale name)")
		}
		var r struct{ Node, Name string }
		if err := client().Do("POST", "/v2/owners", map[string]any{"name": args[1]}, &r); err != nil {
			return err
		}
		fmt.Printf("%s (%s) is now an owner device.\n", r.Name, r.Node)
		return nil
	case "rm", "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: owner rm NODE (from owner list)")
		}
		return client().Do("DELETE", "/v2/owners/"+args[1], nil, nil)
	default:
		return fmt.Errorf("usage: owner list|add|rm")
	}
}

func webhook(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		var r struct {
			Webhooks []struct {
				ID, URL, LastResult string
				Events              []string
				LastAt              time.Time
			}
		}
		if err := client().Do("GET", "/v2/webhooks", nil, &r); err != nil {
			return err
		}
		if len(r.Webhooks) == 0 {
			fmt.Println("No webhooks. Add one with: webhook add URL")
		}
		for _, h := range r.Webhooks {
			last := "not sent yet"
			if !h.LastAt.IsZero() {
				last = h.LastAt.Local().Format("2006-01-02 15:04") + " " + h.LastResult
			}
			fmt.Printf("%s  %s  %s  (%s)\n", h.ID, h.URL, strings.Join(h.Events, ","), last)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("webhook add", flag.ExitOnError)
		events := fs.String("events", "", "on, off, hung, unknown, recovered, recovery (comma-separated; default hung,recovered,recovery)")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("usage: webhook add URL [--events hung,recovered,recovery]")
		}
		_ = fs.Parse(args[2:])
		var r struct {
			Webhook struct {
				ID, Secret string
				Events     []string
			}
		}
		if err := client().Do("POST", "/v2/webhooks", map[string]any{"url": args[1], "events": split(*events)}, &r); err != nil {
			return err
		}
		fmt.Printf("Webhook %s for %s.\n\nSecret (shown once; the receiver checks X-PowerDeck-Signature with it):\n\n  %s\n\n",
			r.Webhook.ID, strings.Join(r.Webhook.Events, ", "), r.Webhook.Secret)
		fmt.Println("Send a test event with: webhook test " + r.Webhook.ID)
		return nil
	case "test":
		if len(args) < 2 {
			return fmt.Errorf("usage: webhook test ID")
		}
		var r struct {
			OK     bool
			Result string
		}
		if err := client().Do("POST", "/v2/webhooks/"+args[1]+"/test", nil, &r); err != nil {
			return err
		}
		if !r.OK {
			return fmt.Errorf("the test event wasn't delivered: %s", r.Result)
		}
		fmt.Println("Delivered a test event (machine \"test\", hung).")
		return nil
	case "rm", "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: webhook rm ID")
		}
		return client().Do("DELETE", "/v2/webhooks/"+args[1], nil, nil)
	default:
		return fmt.Errorf("usage: webhook add|list|test|rm")
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
