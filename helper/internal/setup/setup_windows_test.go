//go:build windows

package setup

import (
	"strings"
	"testing"
)

// Started with -EncodedCommand, PowerShell writes progress ("Preparing modules for first use"),
// warnings and errors to stderr as CLIXML. A check read that as the adapter's Wake-on-LAN state.
func TestPowerShellReturnsOnlyOutput(t *testing.T) {
	// Show that this PowerShell really does write CLIXML here, so the test below means something.
	raw, _ := hidden(powershellExe(), "-NoProfile", "-NonInteractive", "-EncodedCommand",
		encodeCommand(`Write-Progress -Activity 'Preparing modules for first use.'; Write-Warning 'careful'; 'Enabled'`)).CombinedOutput()
	t.Logf("PowerShell's own combined output: %q", raw)

	out, err := powershell(`Write-Progress -Activity 'Preparing modules for first use.'; Write-Warning 'careful'; Write-Output ('Enabled|' + $args[0])`, "Owner's NIC")
	if err != nil || out != "Enabled|Owner's NIC" {
		t.Fatalf("got %q, %v; want only the output", out, err)
	}
}

func TestPowerShellErrorsAreReadable(t *testing.T) {
	for _, script := range []string{
		`throw 'No adapter with MAC 02-00-5E-00-00-01'`,
		`Write-Error 'No adapter with MAC 02-00-5E-00-00-01'`, // not terminating unless stopped
		`Write-Progress -Activity 'Preparing modules for first use.'; Get-NetAdapterPowerManagement -Name 'No adapter with MAC 02-00-5E-00-00-01'`,
	} {
		out, err := powershell(script)
		if err == nil {
			t.Errorf("%s: no error (output %q)", script, out)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, "CLIXML") || strings.Contains(msg, "<Obj") || !strings.Contains(msg, "No adapter with MAC") {
			t.Errorf("%s: error %q, want PowerShell's message", script, msg)
		}
	}
}
