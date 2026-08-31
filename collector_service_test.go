package main

import (
	"strings"
	"testing"
)

func TestCollectorWindowsTaskContract(t *testing.T) {
	if got := powerShellSingleQuote(`C:\User's Files\onwatch.exe`); got != `C:\User''s Files\onwatch.exe` {
		t.Fatalf("PowerShell quoting = %q", got)
	}
	args := quoteWindowsArgs([]string{`C:\Program Files\onwatch.exe`, "collector", "run"})
	if !strings.Contains(args, `"C:\Program Files\onwatch.exe"`) {
		t.Fatalf("Windows arguments do not preserve spaces: %s", args)
	}
	if !strings.Contains(collectorHiddenLauncher, "shell.Run(command, 0, True)") {
		t.Fatal("collector launcher is not hidden")
	}
}
