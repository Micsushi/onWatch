package api

import (
	"fmt"
	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
	"os"
	"syscall"
	"unsafe"
)

func configureAgyLaunch(cmd *pty.Cmd) {
	// Fake SSH selects a different auth context and bypasses the saved local
	// session. Keep local authentication, but forbid browser/helper children.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "AGY_CLI_DISABLE_AUTO_UPDATE=true")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
}

func finishAgyLaunch(cmd *pty.Cmd) (func(), error) { return containAgyProcess(cmd.Process.Pid) }

// The CLI quota listener lives in the parent process. A one-process job
// deliberately disallows optional helpers as well as automatic login browsers.
// Assign while suspended, so no application code can launch a child first.
func containAgyProcess(pid int) (cleanup func(), result error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	defer func() {
		if result != nil {
			windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	limits.BasicLimitInformation.ActiveProcessLimit = 1
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return nil, fmt.Errorf("assign job: %w", err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot thread: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return nil, fmt.Errorf("open thread: %w", err)
		}
		_, err = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		if err != nil {
			return nil, fmt.Errorf("resume thread: %w", err)
		}
		return func() { windows.CloseHandle(job) }, nil
	}
	return nil, fmt.Errorf("suspended process thread unavailable")
}
