//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func inspectProcess(pid int) (instanceIdentity, error) {
	if pid <= 0 {
		return instanceIdentity{}, fmt.Errorf("invalid pid")
	}
	out, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "lstart=").Output()
	if err != nil {
		return instanceIdentity{}, err
	}
	started := strings.TrimSpace(string(out))
	if started == "" {
		return instanceIdentity{}, os.ErrProcessDone
	}
	var exe string
	if runtime.GOOS == "linux" {
		exe, err = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			return instanceIdentity{}, err
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return instanceIdentity{}, err
		}
		fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
		if len(fields) < 20 {
			return instanceIdentity{}, fmt.Errorf("invalid process stat")
		}
		started = fields[19]
	} else {
		out, err = exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "comm=").Output()
		if err != nil {
			return instanceIdentity{}, err
		}
		exe = strings.TrimSpace(string(out))
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}
	return instanceIdentity{pid, exe, started}, nil
}
func sameExecutable(a, b string) bool      { return a == b }
func terminateProcess(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
func platformProcessRunning(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer p.Release()
	err = p.Signal(syscall.Signal(0))
	if err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	// An exited child can remain a zombie until its parent reaps it.
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			fields := strings.Fields(string(data)[strings.LastIndexByte(string(data), ')')+1:])
			if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
				return false
			}
		}
	} else if out, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "stat=").Output(); err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
		return false
	}
	return true
}
