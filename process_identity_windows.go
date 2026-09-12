//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"strings"
)

func inspectProcess(pid int) (instanceIdentity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return instanceIdentity{}, err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return instanceIdentity{}, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return instanceIdentity{}, err
	}
	if code != 259 {
		return instanceIdentity{}, os.ErrProcessDone
	}
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return instanceIdentity{}, err
	}
	return instanceIdentity{pid, windows.UTF16ToString(buf[:size]), fmt.Sprintf("%d:%d", created.HighDateTime, created.LowDateTime)}, nil
}
func sameExecutable(a, b string) bool      { return strings.EqualFold(a, b) }
func terminateProcess(p *os.Process) error { return p.Kill() }
func platformProcessRunning(pid int) bool  { _, err := inspectProcess(pid); return err == nil }
