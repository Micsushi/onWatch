//go:build !windows

package collector

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockCollectorFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
