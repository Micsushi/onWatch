//go:build windows

package store

// syncDir is a no-op on Windows, which has no way to flush a directory: Sync
// calls FlushFileBuffers, and that fails with "Access is denied" on a directory
// handle. Windows commits directory metadata as part of the rename itself, so
// there is nothing left to force. Treating the missing call as a failure used
// to delete every backup the moment after it was verified.
func syncDir(string) error { return nil }
