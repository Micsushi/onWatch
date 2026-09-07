//go:build !windows

package store

import "os"

// syncDir makes a completed rename durable on POSIX systems.
func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}
