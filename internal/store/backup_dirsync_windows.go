//go:build windows

package store

// Windows does not support FlushFileBuffers on a directory handle.
// The backup file itself is flushed before the rename.
func syncDir(string) error { return nil }
