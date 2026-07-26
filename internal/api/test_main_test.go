package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain runs before all tests in the api package. It enables test mode
// to prevent tests from reading or writing real credentials in the macOS
// Keychain or Linux keyring. Without this, tests that call
// WriteAnthropicCredentials or DetectAnthropicToken can overwrite the user's
// real Claude Code OAuth tokens, causing Claude Code to be logged out.
func TestMain(m *testing.M) {
	testHome, err := os.MkdirTemp("", "onwatch-api-test-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated test home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", testHome); err != nil {
		fmt.Fprintf(os.Stderr, "set HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("USERPROFILE", testHome); err != nil {
		fmt.Fprintf(os.Stderr, "set USERPROFILE: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("CODEX_HOME", filepath.Join(testHome, ".codex")); err != nil {
		fmt.Fprintf(os.Stderr, "set CODEX_HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("ONWATCH_TEST_HOME", testHome); err != nil {
		fmt.Fprintf(os.Stderr, "set ONWATCH_TEST_HOME: %v\n", err)
		os.Exit(1)
	}

	SetTestMode(true)
	code := m.Run()
	_ = os.RemoveAll(testHome)
	os.Exit(code)
}

func TestMainUsesIsolatedHome(t *testing.T) {
	isolatedHome := os.Getenv("ONWATCH_TEST_HOME")
	if isolatedHome == "" {
		t.Fatal("ONWATCH_TEST_HOME is not set; tests may write credentials into the user's real home")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir() error = %v", err)
	}
	if filepath.Clean(home) != filepath.Clean(isolatedHome) {
		t.Fatalf("os.UserHomeDir() = %q, want isolated test home %q", home, isolatedHome)
	}
}
