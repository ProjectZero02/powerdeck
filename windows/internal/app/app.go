// Package app wires PowerDeck together and runs it until the context ends. The Windows service and
// the console/dev mode both use it.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/windows/internal/localui"
	"github.com/projectzero02/powerdeck/windows/internal/netinfo"
	"github.com/projectzero02/powerdeck/windows/internal/pairing"
	"github.com/projectzero02/powerdeck/windows/internal/power"
	"github.com/projectzero02/powerdeck/windows/internal/server"
	"github.com/projectzero02/powerdeck/windows/internal/store"
	"github.com/projectzero02/powerdeck/windows/internal/tlscert"
	"github.com/projectzero02/powerdeck/windows/internal/update"
	"github.com/projectzero02/powerdeck/windows/internal/version"
)

const (
	APIPort = 47321
	UIPort  = 47322
)

// UIAddr is where the PC app window is served.
var UIAddr = "127.0.0.1:" + strconv.Itoa(UIPort)

// Options configure a run.
type Options struct {
	DataDir string
	APIAddr string // default ":47321"
	UIAddr  string // default UIAddr
	System  power.System
	Log     *slog.Logger
	// Checks returns OS-specific setup checks (firewall, Wake-on-LAN, …).
	Checks func() []localui.Check
}

type discovery struct {
	mu     sync.Mutex
	cur    netinfo.Discovered
	kick   chan struct{}
	prober netinfo.Prober
	log    *slog.Logger
}

func (d *discovery) get() netinfo.Discovered {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur
}

func (d *discovery) loop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		st, err := netinfo.TailscaleStatus(cctx)
		var res netinfo.Discovered
		if err != nil {
			res = netinfo.Discovered{CheckedAt: time.Now().UTC(), Error: "Tailscale status unavailable: " + err.Error()}
		} else {
			res = netinfo.Discover(cctx, st, d.prober)
		}
		cancel()
		d.mu.Lock()
		prev := d.cur
		// Keep the last good find if a scan comes back empty (e.g. the server is off right now).
		if res.NanoKVM == nil {
			res.NanoKVM = prev.NanoKVM
		}
		if res.Proxmox == nil {
			res.Proxmox = prev.Proxmox
		}
		d.cur = res
		d.mu.Unlock()
		d.log.Info("tailnet scan", "nanokvm", res.NanoKVM, "proxmox", res.Proxmox, "error", res.Error)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.kick:
		}
	}
}

// Run serves the phone API and the PC app window until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if o.APIAddr == "" {
		o.APIAddr = ":" + strconv.Itoa(APIPort)
	}
	if o.UIAddr == "" {
		o.UIAddr = UIAddr
	}
	log := o.Log
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return err
	}
	name, _ := os.Hostname()
	st, err := store.Open(o.DataDir)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	id, err := tlscert.LoadOrCreate(o.DataDir, "PowerDeck "+name)
	if err != nil {
		return fmt.Errorf("tls identity: %w", err)
	}
	now := time.Now
	pm := pairing.NewManager(now)
	pc := power.NewController(o.System, now)
	upd := update.NewManager(o.DataDir, log)
	disc := &discovery{kick: make(chan struct{}, 1), prober: netinfo.HTTPProber(), log: log}
	mac := func() string {
		if a, ok := netinfo.PrimaryWiredAdapter(); ok {
			return a.MAC
		}
		return ""
	}
	checks := func() []localui.Check {
		var out []localui.Check
		if ip, ok := netinfo.TailnetIPv4(); ok {
			out = append(out, localui.Check{Name: "Connected to Tailscale", OK: true, Detail: ip.String()})
		} else {
			out = append(out, localui.Check{Name: "Connected to Tailscale", Detail: "Start Tailscale and sign in, then reopen this window."})
		}
		if o.Checks != nil {
			out = append(out, o.Checks()...)
		}
		return out
	}

	api := &http.Server{
		Addr: o.APIAddr,
		Handler: server.New(server.Deps{
			Store: st, Pairing: pm, Power: pc, Name: name, MAC: mac, Discovery: disc.get, Log: log, Now: now,
		}),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{id.Cert}, MinVersion: tls.VersionTLS13},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	uiHandler, err := localui.New(localui.Deps{
		Addr: o.UIAddr, APIPort: APIPort, Name: name, Fingerprint: id.Fingerprint,
		Store: st, Pairing: pm, Update: upd, MAC: mac, TailnetIP: netinfo.TailnetIPv4,
		Discovery: disc.get, Checks: checks, Log: log,
		Rediscover: func() {
			select {
			case disc.kick <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		return err
	}
	ui := &http.Server{Addr: o.UIAddr, Handler: uiHandler, ReadHeaderTimeout: 10 * time.Second}

	apiLn, err := net.Listen("tcp", o.APIAddr)
	if err != nil {
		return fmt.Errorf("phone API port: %w", err)
	}
	uiLn, err := net.Listen("tcp", o.UIAddr)
	if err != nil {
		apiLn.Close()
		return fmt.Errorf("app window port: %w", err)
	}
	log.Info("PowerDeck started", "version", version.Version, "name", name, "api", apiLn.Addr().String(),
		"ui", uiLn.Addr().String(), "fingerprint", id.Fingerprint)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- api.ServeTLS(apiLn, "", "") }()
	go func() { errc <- ui.Serve(uiLn) }()
	go disc.loop(ctx)
	go updateLoop(ctx, upd, st, pc, log)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = api.Shutdown(shutCtx)
	_ = ui.Shutdown(shutCtx)
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	log.Info("PowerDeck stopped")
	return runErr
}

// updateLoop checks for updates shortly after start and every 6 hours, and installs them when
// automatic updates are on and nothing is scheduled.
func updateLoop(ctx context.Context, upd *update.Manager, st *store.Store, pc *power.Controller, log *slog.Logger) {
	if !version.IsRelease() {
		return
	}
	wait := 2 * time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 6 * time.Hour
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		s, err := upd.Check(cctx)
		cancel()
		if err != nil {
			log.Warn("update check failed", "err", err)
			continue
		}
		if s.Available && st.Settings().AutoUpdate && pc.Pending() == nil {
			log.Info("installing update", "version", s.Latest)
			ictx, icancel := context.WithTimeout(ctx, 10*time.Minute)
			if err := upd.Install(ictx); err != nil {
				log.Error("automatic update failed", "err", err)
			}
			icancel()
		}
	}
}
