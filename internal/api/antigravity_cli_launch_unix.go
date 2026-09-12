//go:build !windows

package api

import (
	pty "github.com/aymanbagabas/go-pty"
	"os"
)

func configureAgyLaunch(cmd *pty.Cmd) {
	// Preserve the existing remote-style flow on Unix.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "SSH_CONNECTION=onwatch-collector")
}

func finishAgyLaunch(*pty.Cmd) (func(), error) { return nil, nil }
