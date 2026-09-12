package api

import (
	"context"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestLocalContextChildContainment(t *testing.T) {
	if os.Getenv("ONWATCH_TEST_CONTAINMENT_CHILD") == "1" {
		if exec.Command("cmd.exe", "/c", "exit", "0").Run() == nil {
			os.Exit(9)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalContextChildContainment$")
	cmd.Env = append(os.Environ(), "ONWATCH_TEST_CONTAINMENT_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	if err := cmd.Start(); err != nil {
		t.Fatal("start containment self-check")
	}
	defer cmd.Process.Kill()
	cleanup, err := containAgyProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Wait(); err != nil {
		t.Fatal("child process denial self-check failed")
	}
}
