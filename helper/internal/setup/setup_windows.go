//go:build windows

package setup

// Windows: the installer runs `PowerDeck.exe install` elevated. It is idempotent, so updates simply
// run it again.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/projectzero02/powerdeck/helper/internal/check"
	"github.com/projectzero02/powerdeck/helper/internal/netinfo"
)

const (
	ServiceName  = "PowerDeck"
	FirewallRule = "PowerDeck (tailnet only)"
	oldRule      = "PowerDeck (phone, tailnet only)" // 1.x
	powerKey     = `SYSTEM\CurrentControlSet\Control\Session Manager\Power`
	runKey       = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	ports        = "47321,47323"
)

// DataDir is %ProgramData%\PowerDeck (POWERDECK_DATA overrides it for development).
func DataDir() string {
	if d, ok := devDataDir(); ok {
		return d
	}
	return filepath.Join(os.Getenv("ProgramData"), "PowerDeck")
}

func hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// Install sets everything up and starts the service. Only service registration failures are fatal;
// the rest is reported so the checks can show what still needs attention.
func Install(exePath string) ([]Step, error) {
	var steps []Step
	add := func(name string, err error, detail string) {
		s := Step{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			s.Detail = err.Error()
		}
		steps = append(steps, s)
	}

	if err := secureDataDir(); err != nil {
		return steps, fmt.Errorf("data folder: %w", err)
	}
	add("Private data folder", nil, DataDir())

	if err := installService(exePath); err != nil {
		return steps, fmt.Errorf("service: %w", err)
	}
	add("Background service", nil, "starts with Windows, restarts itself if it stops")

	add("Firewall allows PowerDeck (tailnet only)", firewall(exePath), "TCP "+ports+" from 100.64.0.0/10")

	if n, ok := netinfo.Primary(); ok && n.Wired {
		detail, err := enableWake(n.MAC)
		add("Wake-on-LAN on "+n.Interface, err, detail)
	} else {
		add("Wake-on-LAN", errors.New("no wired network adapter with internet access was found"), "")
	}

	add("Fast Startup off (full shutdowns)", disableFastStartup(), "")
	add("Tailscale stays on at the sign-in screen", tailscaleUnattended(), "")

	if err := startService(); err != nil {
		return steps, fmt.Errorf("start service: %w", err)
	}
	b, _ := json.MarshalIndent(steps, "", "  ")
	_ = os.WriteFile(filepath.Join(DataDir(), "setup.json"), b, 0o600)
	return steps, nil
}

// Uninstall stops and removes the service, firewall rule and sign-in entry. The installer deletes
// the files (and, with purge, the data folder).
func Uninstall(purge bool) error {
	var errs []error
	if err := removeService(); err != nil {
		errs = append(errs, err)
	}
	for _, r := range []string{FirewallRule, oldRule} {
		_ = hidden("netsh", "advfirewall", "firewall", "delete", "rule", "name="+r).Run()
	}
	// Older builds started the presence companion from the Run key; the service starts it now.
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue("PowerDeck")
		k.Close()
	}
	if purge {
		errs = append(errs, os.RemoveAll(DataDir()))
	}
	return errors.Join(errs...)
}

// secureDataDir creates the data folder readable by SYSTEM and Administrators only: it holds the
// TLS private key and the paired-device list.
func secureDataDir() error {
	dir := DataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func installService(exePath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	cfg := mgr.Config{
		DisplayName:  "PowerDeck",
		Description:  "Lets your phone turn this PC off and on over Tailscale (PowerDeck).",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	s, err := m.OpenService(ServiceName)
	if err == nil {
		// Upgrade: point the existing service at this binary.
		cur, err := s.Config()
		if err == nil {
			cur.BinaryPathName = `"` + exePath + `" service`
			cur.DisplayName, cur.Description, cur.StartType = cfg.DisplayName, cfg.Description, cfg.StartType
			err = s.UpdateConfig(cur)
		}
		if err != nil {
			s.Close()
			return err
		}
	} else {
		s, err = m.CreateService(ServiceName, exePath, cfg, "service")
		if err != nil {
			return err
		}
	}
	defer s.Close()
	return s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 2 * time.Minute},
	}, uint32((24 * time.Hour).Seconds()))
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Running {
		return nil
	}
	return s.Start()
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return nil // already gone
	}
	defer s.Close()
	if st, err := s.Control(svc.Stop); err == nil {
		for deadline := time.Now().Add(15 * time.Second); st.State != svc.Stopped && time.Now().Before(deadline); {
			time.Sleep(300 * time.Millisecond)
			if st, err = s.Query(); err != nil {
				break
			}
		}
	}
	return s.Delete()
}

// ServiceRunning reports whether the PowerDeck service is running (no admin rights needed).
func ServiceRunning() bool {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(h)
	name, _ := windows.UTF16PtrFromString(ServiceName)
	sh, err := windows.OpenService(h, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(sh)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(sh, &st); err != nil {
		return false
	}
	return st.CurrentState == windows.SERVICE_RUNNING
}

func firewall(exePath string) error {
	for _, r := range []string{FirewallRule, oldRule} {
		_ = hidden("netsh", "advfirewall", "firewall", "delete", "rule", "name="+r).Run()
	}
	out, err := hidden("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+FirewallRule, "dir=in", "action=allow", "protocol=TCP", "localport="+ports,
		"remoteip=100.64.0.0/10,fd7a:115c:a1e0::/48", "profile=any", "program="+exePath, "enable=yes",
		"description=PowerDeck helper API. Only Tailscale addresses are allowed.").CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firewallPresent() bool {
	return hidden("netsh", "advfirewall", "firewall", "show", "rule", "name="+FirewallRule).Run() == nil
}

// enableWake turns on magic-packet wake for the adapter with this MAC. Settings are only written
// when they differ, so re-running (updates) doesn't restart the adapter.
const wakeScript = `
$ErrorActionPreference = 'Stop'
$mac = $args[0]
$nic = Get-NetAdapter -Physical | Where-Object { $_.MacAddress -eq $mac } | Select-Object -First 1
if (-not $nic) { throw "No adapter with MAC $mac" }
$pm = Get-NetAdapterPowerManagement -Name $nic.Name
if ($pm.WakeOnMagicPacket -ne 'Enabled' -or $pm.WakeOnPattern -ne 'Disabled') {
  Set-NetAdapterPowerManagement -Name $nic.Name -WakeOnMagicPacket Enabled -WakeOnPattern Disabled
}
Get-NetAdapterAdvancedProperty -Name $nic.Name |
  Where-Object { $_.DisplayName -match 'Magic Packet|Shutdown Wake|Wake.*power off|Wake.*S5' -and $_.ValidDisplayValues -contains 'Enabled' -and $_.DisplayValue -ne 'Enabled' } |
  ForEach-Object { Set-NetAdapterAdvancedProperty -Name $nic.Name -DisplayName $_.DisplayName -DisplayValue 'Enabled' }
powercfg.exe /deviceenablewake "$($nic.InterfaceDescription)" 2>&1 | Out-Null
$pm = Get-NetAdapterPowerManagement -Name $nic.Name
Write-Output ("{0}|{1}" -f $pm.WakeOnMagicPacket, $nic.InterfaceDescription)
`

// powershell runs a script block with single-quoted arguments via -EncodedCommand, which sidesteps
// Windows command-line quoting entirely.
func powershell(script string, args ...string) (string, error) {
	cmd := "& {" + script + "}"
	for _, a := range args {
		cmd += " '" + strings.ReplaceAll(a, "'", "''") + "'"
	}
	u := windows.StringToUTF16(cmd)
	b := make([]byte, 0, len(u)*2)
	for _, c := range u[:len(u)-1] { // drop the trailing NUL
		b = append(b, byte(c), byte(c>>8))
	}
	enc := base64.StdEncoding.EncodeToString(b)
	out, err := hidden(filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`),
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", enc).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func enableWake(mac string) (string, error) {
	dashed := strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
	out, err := powershell(wakeScript, dashed)
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, lastLine(out))
	}
	state, desc, _ := strings.Cut(lastLine(out), "|")
	if !strings.EqualFold(state, "Enabled") {
		return "", fmt.Errorf("the driver reports magic-packet wake as %q", state)
	}
	return desc + " (" + mac + ")", nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func disableFastStartup() error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, powerKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue("HiberbootEnabled", 0)
}

func fastStartupOff() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, powerKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("HiberbootEnabled")
	return err == nil && v == 0
}

func tailscaleExe() string {
	return filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
}

func tailscaleUnattended() error {
	if _, err := os.Stat(tailscaleExe()); err != nil {
		return errors.New("Tailscale isn't installed")
	}
	if unattendedOn() {
		return nil
	}
	if out, err := hidden(tailscaleExe(), "set", "--unattended=true").CombinedOutput(); err != nil {
		return fmt.Errorf("turn on Run unattended in the Tailscale tray menu (%s)", strings.TrimSpace(string(out)))
	}
	return nil
}

func unattendedOn() bool {
	out, err := hidden(tailscaleExe(), "debug", "prefs").Output()
	if err != nil {
		return false
	}
	var p struct{ ForceDaemon bool }
	return json.Unmarshal(out, &p) == nil && p.ForceDaemon
}

// Checks are the Windows setup checks.
func Checks(e Env) []check.Check {
	exe, _ := os.Executable()
	return []check.Check{
		tailscaleCheck(e),
		keyExpiryCheck(e),
		{ID: "unattended",
			Run: func(context.Context) check.Result {
				if unattendedOn() {
					return check.Result{Level: check.OK, Title: "Tailscale stays on at the sign-in screen"}
				}
				return check.Result{Level: check.Error, Title: "Tailscale stops when you sign out",
					Detail: "Turn on Run unattended (Tailscale tray menu → Preferences), or press Fix."}
			},
			Fix: func(context.Context) error { return tailscaleUnattended() },
		},
		{ID: "firewall",
			Run: func(context.Context) check.Result {
				if firewallPresent() {
					return check.Result{Level: check.OK, Title: "Firewall allows PowerDeck (tailnet only)"}
				}
				return check.Result{Level: check.Error, Title: "Firewall blocks PowerDeck"}
			},
			Fix: func(context.Context) error { return firewall(exe) },
		},
		wiredCheck(e),
		{ID: "wol_nic",
			Run: func(context.Context) check.Result {
				n, ok := e.Network()
				if !ok || !n.Wired {
					return check.Result{Level: check.Info, Title: "Wake-on-LAN", Detail: "Needs a wired network card."}
				}
				out, err := powershell(`(Get-NetAdapterPowerManagement -Name $args[0]).WakeOnMagicPacket`, n.Interface)
				if err == nil && strings.EqualFold(lastLine(out), "Enabled") {
					return check.Result{Level: check.OK, Title: "Wake-on-LAN on " + n.Interface, Detail: n.MAC}
				}
				return check.Result{Level: check.Warn, Title: "Wake-on-LAN is off on " + n.Interface}
			},
			Fix: func(context.Context) error {
				n, ok := e.Network()
				if !ok {
					return errors.New("no wired network card found")
				}
				_, err := enableWake(n.MAC)
				return err
			},
		},
		{ID: "fast_startup",
			Run: func(context.Context) check.Result {
				if fastStartupOff() {
					return check.Result{Level: check.OK, Title: "Fast Startup off"}
				}
				return check.Result{Level: check.Warn, Title: "Fast Startup is on",
					Detail: "PowerDeck's own shutdowns are full shutdowns anyway, but shutting down from the Start menu then leaves the PC unable to wake."}
			},
			Fix: func(context.Context) error { return disableFastStartup() },
		},
		batteryCheck(e),
		biosCheck(),
		updatesCheck(e),
	}
}

// PrepareNetwork is a no-op on Windows: the driver keeps its Wake-on-LAN setting.
func PrepareNetwork(Env) error { return nil }
