//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSameExecutableWindowsAliases(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetShortPathName(name, &buf[0], uint32(len(buf)))
	if err != nil || n >= uint32(len(buf)) {
		t.Fatalf("short executable path: %d, %v", n, err)
	}
	if !sameExecutable(exe, windows.UTF16ToString(buf[:n])) {
		t.Fatal("short executable alias was rejected")
	}
	if !sameExecutable(exe, `\\?\`+exe) {
		t.Fatal("extended executable alias was rejected")
	}
	other := filepath.Join(t.TempDir(), "different.exe")
	if err := os.WriteFile(other, []byte("different file"), 0600); err != nil {
		t.Fatal(err)
	}
	if sameExecutable(exe, other) {
		t.Fatal("different executable was accepted")
	}
}
