package api

import (
	"testing"
)

// The detection probe enumerates every process and filters on command-line
// text. The probe's own command line contains that text, so it matches itself.
// A self-match makes detection report "port not found" forever instead of
// "process not found", which hides the fact that Antigravity is not running.
func TestSelectAntigravityProcess_IgnoresDetectionProbe(t *testing.T) {
	probe := "powershell -NoProfile -Command Get-CimInstance Win32_Process | " +
		"Where-Object { $_.CommandLine -and ($_.CommandLine -like '*antigravity*' " +
		"-or $_.Name -like '*language_server*')} | Select-Object ProcessId, Name, CommandLine"

	candidates := []AntigravityProcessInfo{
		{PID: 62972, CommandLine: probe},
	}

	if got := selectAntigravityProcess(candidates, 0); got != nil {
		t.Fatalf("expected no candidate, got PID %d", got.PID)
	}
}

func TestSelectAntigravityProcess_IgnoresWMICProbe(t *testing.T) {
	probe := `wmic process where "name like '%antigravity%' or commandline like '%antigravity%'" get processid,commandline`

	candidates := []AntigravityProcessInfo{
		{PID: 4242, CommandLine: probe},
	}

	if got := selectAntigravityProcess(candidates, 0); got != nil {
		t.Fatalf("expected no candidate, got PID %d", got.PID)
	}
}

func TestSelectAntigravityProcess_IgnoresExcludedPID(t *testing.T) {
	candidates := []AntigravityProcessInfo{
		{PID: 777, CommandLine: "antigravity language_server --csrf_token=abc", CSRFToken: "abc"},
	}

	if got := selectAntigravityProcess(candidates, 777); got != nil {
		t.Fatalf("expected excluded PID to be dropped, got PID %d", got.PID)
	}
}

// A process whose command line only mentions Antigravity is an IDE helper, not
// the language server. Selecting one sends port discovery to a PID that never
// listens.
func TestSelectAntigravityProcess_RequiresServerSignal(t *testing.T) {
	candidates := []AntigravityProcessInfo{
		{PID: 100, CommandLine: `C:\Program Files\Antigravity\antigravity.exe --type=renderer`},
	}

	if got := selectAntigravityProcess(candidates, 0); got != nil {
		t.Fatalf("expected renderer to be rejected, got PID %d", got.PID)
	}
}

func TestSelectAntigravityProcess_PicksLanguageServer(t *testing.T) {
	candidates := []AntigravityProcessInfo{
		{PID: 100, CommandLine: `C:\Program Files\Antigravity\antigravity.exe --type=renderer`},
		{
			PID:                 200,
			CommandLine:         `C:\Users\x\.antigravity\language_server_windows_x64.exe --csrf_token=abc --extension_server_port=42100`,
			CSRFToken:           "abc",
			ExtensionServerPort: 42100,
		},
	}

	got := selectAntigravityProcess(candidates, 0)
	if got == nil {
		t.Fatal("expected the language server to be selected")
	}
	if got.PID != 200 {
		t.Fatalf("expected PID 200, got %d", got.PID)
	}
}

func TestSelectAntigravityProcess_RequiresAntigravityInCommandLine(t *testing.T) {
	candidates := []AntigravityProcessInfo{
		{PID: 300, CommandLine: `C:\other\language_server.exe --csrf_token=abc`},
	}

	if got := selectAntigravityProcess(candidates, 0); got != nil {
		t.Fatalf("expected unrelated language server to be rejected, got PID %d", got.PID)
	}
}

// The WMIC fallback only runs when the CIM query yields nothing usable, so the
// CIM path must report "not found" rather than returning a bogus candidate.
func TestParseWMICOutput_RejectsSelfMatch(t *testing.T) {
	output := "Node,CommandLine,ProcessId\n" +
		`host,wmic process where "commandline like '%antigravity%'" get processid,4242` + "\n"

	if info := (&AntigravityClient{}).parseWMICOutput(output); info != nil {
		t.Fatalf("expected no candidate, got PID %d", info.PID)
	}
}
