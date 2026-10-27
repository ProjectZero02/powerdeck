// Package app wires the helper together and runs it until the context ends. The Windows service,
// the Linux systemd service and the foreground/dev mode all use it.
package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/discovery"
	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/guests"
	"github.com/projectzero02/powerdeck/helper/internal/localui"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
	"github.com/projectzero02/powerdeck/helper/internal/pairing"
	"github.com/projectzero02/powerdeck/helper/internal/power"
	"github.com/projectzero02/powerdeck/helper/internal/schedule"
	"github.com/projectzero02/powerdeck/helper/internal/server"
	"github.com/projectzero02/powerdeck/helper/internal/setup"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
	"github.com/projectzero02/powerdeck/helper/internal/tlscert"
	"github.com/projectzero02/powerdeck/helper/internal/update"
	"github.com/projectzero02/powerdeck/helper/internal/version"
	"github.com/projectzero02/powerdeck/helper/internal/wol"
)

const (
	APIPort = 47321
	UIPort  = 47322
	MCPPort = localui.MCPPort
)

// UIAddr is where the PC window is served.
var UIAddr = "127.0.0.1:" + strconv.Itoa(UIPort)

// LocalTokenFile holds the token this machine's own CLI uses to act as owner over loopback.
const LocalTokenFile = "local.token"

// Options configure a run.
type Options struct {
	DataDir string
	APIAddr string // default ":47321"
	MCPAddr string // default ":47323"
	UIAddr  string // default UIAddr; "-" disables the PC window (Linux)
	System  power.System
	Reports *power.Reports
	Log     *slog.Logger
	// AllowLocal limits who may use the PC window (see localui.Deps).
	AllowLocal func(remote string) bool
}

// cachedNetwork re-reads the network card at most once a minute.
func cachedNetwork() func() (netinfo.Network, bool) {
	var (
		mu sync.Mutex
		n  netinfo.Network
		ok bool
		at time.Time
	)
	return func() (netinfo.Network, bool) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) > time.Minute {
			n, ok = netinfo.Primary()
			at = time.Now()
		}
		return n, ok
	}
}

func writeLocalToken(dir string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := "pdl_" + base64.RawURLEncoding.EncodeToString(b)
	return tok, os.WriteFile(filepath.Join(dir, LocalTokenFile), []byte(tok), 0o600)
}

// Run serves the helper until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if o.APIAddr == "" {
		o.APIAddr = ":" + strconv.Itoa(APIPort)
	}
	if o.MCPAddr == "" {
		o.MCPAddr = ":" + strconv.Itoa(MCPPort)
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
	localToken, err := writeLocalToken(o.DataDir)
	if err != nil {
		return fmt.Errorf("local token: %w", err)
	}
	now := time.Now
	ts := tailscale.New()
	pm := pairing.NewManager(now)
	pc := power.NewController(o.System, now)
	upd := update.NewManager(o.DataDir, log)
	upd.Busy = func() string {
		if pc.Pending() != nil {
			return "a shutdown or restart is counting down"
		}
		return ""
	}
	network := cachedNetwork()
	env := setup.Env{TS: ts, Store: st, Network: network, Now: now}
	checkList := func() []check.Check { return setup.Checks(env) }
	runChecks := func(ctx context.Context) []check.Result { return check.RunAll(ctx, checkList()) }
	fix := setup.Fixer(checkList)
	fl := fleet.New(fleet.Deps{TS: ts, Store: st, Prober: discovery.Network{}, Network: network, Log: log, Now: now})

	if err := setup.PrepareNetwork(env); err != nil {
		log.Warn("couldn't arm Wake-on-LAN", "err", err)
	}

	api := server.New(server.Deps{
		Auth:  &auth.Authenticator{TS: ts, Store: st, Now: now, LocalToken: localToken},
		Store: st, Pairing: pm, Power: pc, Fleet: fl, Name: name, OS: runtime.GOOS,
		SelfNode: func(ctx context.Context) string {
			if s, err := ts.Status(ctx); err == nil {
				return s.Self.ID
			}
			return ""
		},
		Network: network, Checks: runChecks, Fix: fix, Guests: guests.Cached(15*time.Second, guests.Running), Wake: wol.Send,
		Update: upd,
		PairLink: func() (string, string) {
			ip, _ := netinfo.TailnetIPv4()
			code := pm.Current().Value
			return localui.AddLink(name, ip, APIPort, id.Fingerprint, ""), code
		},
		Log: log, Now: now,
	})
	srv := func(addr string, h http.Handler) *http.Server {
		return &http.Server{
			Addr: addr, Handler: h,
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second,
			WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second,
			ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
		}
	}
	apiSrv := srv(o.APIAddr, api)
	apiSrv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{id.Cert}, MinVersion: tls.VersionTLS13}
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", api)
	mcpSrv := srv(o.MCPAddr, mcpMux)

	type listener struct {
		ln    net.Listener
		serve func(net.Listener) error
		srv   *http.Server
	}
	var ls []listener
	listen := func(addr, what string, s *http.Server, tlsOn bool) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("%s port: %w", what, err)
		}
		serve := s.Serve
		if tlsOn {
			serve = func(l net.Listener) error { return s.ServeTLS(l, "", "") }
		}
		ls = append(ls, listener{ln, serve, s})
		return nil
	}
	closeAll := func() {
		for _, l := range ls {
			l.ln.Close()
		}
	}
	if err := listen(o.APIAddr, "API", apiSrv, true); err != nil {
		return err
	}
	if err := listen(o.MCPAddr, "agent (MCP)", mcpSrv, false); err != nil {
		closeAll()
		return err
	}
	if o.UIAddr != "-" {
		uiHandler, err := localui.New(localui.Deps{
			Addr: o.UIAddr, APIPort: APIPort, Name: name, Fingerprint: id.Fingerprint,
			Store: st, Pairing: pm, Update: upd, Network: network, TailnetIP: netinfo.TailnetIPv4,
			Checks: runChecks, Fix: fix, Reports: o.Reports, Log: log, AllowLocal: o.AllowLocal,
		})
		if err != nil {
			closeAll()
			return err
		}
		if err := listen(o.UIAddr, "PC window", srv(o.UIAddr, uiHandler), false); err != nil {
			closeAll()
			return err
		}
	}
	log.Info("PowerDeck helper started", "version", version.Version, "name", name, "os", runtime.GOOS, "pin", id.Fingerprint)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, len(ls))
	for _, l := range ls {
		go func(l listener) { errc <- l.serve(l.ln) }(l)
	}
	go fl.Loop(ctx, 5*time.Minute, nil)
	go schedule.Loop(ctx, st.Schedules, api.RunSchedule, now)
	go updateLoop(ctx, upd, st, pc, log)
	go func() { // drivers on some Linux systems drop Wake-on-LAN after link changes
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
				_ = setup.PrepareNetwork(env)
			}
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	for _, l := range ls {
		_ = l.srv.Shutdown(shutCtx)
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	log.Info("PowerDeck helper stopped")
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
