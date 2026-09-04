package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Pragmas applied with db.Exec land on whichever pooled connection is free, so
// a second connection opened later never got busy_timeout and failed writes
// instantly with SQLITE_BUSY instead of waiting. Every connection must carry it.
func TestNew_BusyTimeoutAppliesToEveryPooledConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pragma.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Pin one connection open so the second query must use a different one.
	first, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatalf("first conn: %v", err)
	}
	defer first.Close()

	second, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatalf("second conn: %v", err)
	}
	defer second.Close()

	var timeout int
	if err := first.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout on first conn: %v", err)
	}
	if timeout < 5000 {
		t.Fatalf("first conn busy_timeout = %d, want >= 5000", timeout)
	}

	if err := second.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout on second conn: %v", err)
	}
	if timeout < 5000 {
		t.Fatalf("second conn busy_timeout = %d, want >= 5000 - this is the SQLITE_BUSY bug", timeout)
	}

	var fk int
	if err := second.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys on second conn: %v", err)
	}
	if fk != 1 {
		t.Fatalf("second conn foreign_keys = %d, want 1", fk)
	}
}

// Nothing ever checkpointed the WAL, so it grew without bound (34MB in
// production) and starved further checkpoints.
func TestCheckpointWAL_TruncatesTheLog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer s.Close()

	for i := 0; i < 2000; i++ {
		if err := s.SetSetting("k", "value-that-is-long-enough-to-grow-the-wal-a-little"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	walPath := dbPath + "-wal"
	before, err := os.Stat(walPath)
	if err != nil {
		t.Skipf("no WAL file produced: %v", err)
	}

	if err := s.CheckpointWAL(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	after, err := os.Stat(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return // truncated away entirely, which is the goal
		}
		t.Fatalf("stat after checkpoint: %v", err)
	}
	if after.Size() >= before.Size() && before.Size() > 0 {
		t.Fatalf("WAL did not shrink: %d -> %d bytes", before.Size(), after.Size())
	}
}
