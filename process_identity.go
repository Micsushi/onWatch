package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type instanceIdentity struct {
	PID        int
	Executable string
	Started    string
}

func writeInstanceIdentity(path string, pid int) error {
	id, err := inspectProcess(pid)
	if err != nil {
		return err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".identity", data, 0600)
}
func ownedProcess(proc *os.Process) error {
	current, err := inspectProcess(proc.Pid)
	if err != nil {
		return err
	}
	own, err := os.Executable()
	if err != nil {
		return err
	}
	own, _ = filepath.EvalSymlinks(own)
	for _, path := range []string{pidFile, menubarPIDPath(false), menubarPIDPath(true)} {
		data, err := os.ReadFile(path + ".identity")
		if err != nil {
			continue
		}
		var saved instanceIdentity
		if json.Unmarshal(data, &saved) == nil && saved.PID == current.PID && saved.Started == current.Started && sameExecutable(saved.Executable, own) && (sameExecutable(strings.TrimSuffix(current.Executable, " (deleted)"), saved.Executable) || strings.HasPrefix(current.Executable, saved.Executable+".old-")) {
			return nil
		}
	}
	return fmt.Errorf("PID %d has no matching instance identity; use its service manager or stop it manually", proc.Pid)
}
func stopOwnedProcess(proc *os.Process) error {
	if err := ownedProcess(proc); err != nil {
		return err
	}
	if err := terminateProcess(proc); err != nil {
		return err
	}
	if !waitForProcessesExit([]int{proc.Pid}, 5*time.Second) {
		return fmt.Errorf("PID %d did not exit; PID file retained", proc.Pid)
	}
	return nil
}
func removeStoppedPID(path string) {
	if pid := readRuntimePID(path); pid == os.Getpid() || !processRunning(pid) {
		_ = os.Remove(path)
		_ = os.Remove(path + ".identity")
	}
}
