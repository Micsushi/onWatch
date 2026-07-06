package main

import (
	"os"
	"testing"
)

// TestMain forces the web server under test to bind loopback instead of the
// 0.0.0.0 default. Non-loopback listeners make the macOS application firewall
// prompt "accept incoming network connections?" for every test binary.
// Subprocess daemon tests inherit this via os.Environ().
func TestMain(m *testing.M) {
	if os.Getenv("ONWATCH_HOST") == "" {
		os.Setenv("ONWATCH_HOST", "127.0.0.1")
	}
	os.Exit(m.Run())
}
