package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditProcessHelper(t *testing.T) {
	if os.Getenv("ONWATCH_AUDIT_PROCESS_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}
func TestAuditProcessOwnershipAndNativeStop(t *testing.T) {
	old := pidFile
	pidFile = filepath.Join(t.TempDir(), "test.pid")
	defer func() { pidFile = old }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuditProcessHelper$")
	cmd.Env = append(os.Environ(), "ONWATCH_AUDIT_PROCESS_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeInstanceIdentity(pidFile, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(pidFile + ".identity")
	if err != nil {
		t.Fatal(err)
	}
	var wrong instanceIdentity
	if err := json.Unmarshal(original, &wrong); err != nil {
		t.Fatal(err)
	}
	wrong.Started = "different creation time"
	data, _ := json.Marshal(wrong)
	if err := os.WriteFile(pidFile+".identity", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := stopOwnedProcess(cmd.Process); err == nil {
		t.Fatal("stale identity allowed stop")
	}
	if !processRunning(cmd.Process.Pid) {
		t.Fatal("unverified process terminated")
	}
	if err := os.WriteFile(pidFile+".identity", original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := stopOwnedProcess(cmd.Process); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("process not reaped")
	}
	removeStoppedPID(pidFile)
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("exited instance PID not removed")
	}
}
