//go:build !windows

package api

import "os/exec"

func configureClaudeCredentialRefresh(cmd *exec.Cmd) {}
