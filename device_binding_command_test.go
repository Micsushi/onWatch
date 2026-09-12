package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestDeviceBindingCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.db")
	db, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	device, _, err := db.CreateDevice("synthetic", "windows")
	if err != nil {
		t.Fatal(err)
	}
	err = db.SetDeviceDesiredConfig(device.ID, ingest.DesiredConfig{Assignments: []ingest.ProviderAssignment{{Provider: "antigravity", ExternalID: "backup", PollInterval: "60s"}}})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"device", "bind-antigravity", "--db", path, "--device-id", device.ID, "--account", "backup", "--expected-revision", "1", "--email", "owner@example.invalid"}
	previousArgs := os.Args
	os.Args = append([]string{"onwatch"}, args...)
	t.Cleanup(func() { os.Args = previousArgs })
	if err := run(); err != nil {
		t.Fatal("binding dispatch selected another command", err)
	}
	for _, bad := range [][]string{append(append([]string{}, args...), "--unknown"), append(append([]string{}, args...), "--clear"), {"device", "bind-antigravity", "--apply"}} {
		if err := runDeviceCommand(bad); err == nil {
			t.Fatal("invalid flags accepted")
		}
	}
	if err := runDeviceCommand(args); err != nil {
		t.Fatal(err)
	}
	read, err := db.GetDevice(device.ID)
	if err != nil || read.DesiredConfigRevision != 1 {
		t.Fatal("preview changed revision", err)
	}
	if err := runDeviceCommand(append(args, "--apply")); err != nil {
		t.Fatal(err)
	}
	read, err = db.GetDevice(device.ID)
	if err != nil || read.DesiredConfigRevision != 2 || read.DesiredConfig.Assignments[0].AntigravityAccountEmail != "owner@example.invalid" {
		t.Fatal("CLI binding readback failed", err)
	}
	if err := runDeviceCommand(append(args, "--apply")); err == nil {
		t.Fatal("stale CLI write succeeded")
	}
}
