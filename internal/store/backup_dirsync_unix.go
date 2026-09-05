//go:build !windows

package store

import "os"

// syncDir makes a completed rename durable. A file's own fsync does not
// guarantee its directory entry survives a crash, so the directory is synced
// too once the backup and its metadata are in place.
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
