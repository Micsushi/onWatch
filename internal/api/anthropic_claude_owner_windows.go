package api

import (
	"os/exec"
	"syscall"
)

func configureClaudeCredentialRefresh(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
