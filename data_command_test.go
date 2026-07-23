package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestRunDataCommandExportsImportsAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source, err := store.New(sourcePath)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	renewsAt := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	if _, err := source.InsertSnapshot(&api.Snapshot{
		CapturedAt: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 10, RenewsAt: renewsAt},
		Search:     api.QuotaInfo{Limit: 100, Requests: 2, RenewsAt: renewsAt},
		ToolCall:   api.QuotaInfo{Limit: 50, Requests: 1, RenewsAt: renewsAt},
	}); err != nil {
		source.Close()
		t.Fatalf("insert source snapshot: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	archivePath := filepath.Join(dir, "source.onwatch.zip")
	if err := runDataCommand([]string{"data", "export", "--db", sourcePath, "--out", archivePath}); err != nil {
		t.Fatalf("data export: %v", err)
	}
	if info, err := os.Stat(archivePath); err != nil || info.Size() == 0 {
		t.Fatalf("export archive missing or empty: info=%v err=%v", info, err)
	}
	if err := runDataCommand([]string{"data", "export", "--db", sourcePath, "--out", archivePath}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite error = %v, want already exists", err)
	}

	destinationPath := filepath.Join(dir, "destination.db")
	if err := runDataCommand([]string{"data", "import", "--db", destinationPath, archivePath}); err != nil {
		t.Fatalf("data import: %v", err)
	}
	if err := runDataCommand([]string{"data", "import", "--db", destinationPath, archivePath}); err != nil {
		t.Fatalf("data reimport: %v", err)
	}
	destination, err := store.New(destinationPath)
	if err != nil {
		t.Fatalf("open destination: %v", err)
	}
	defer destination.Close()
	latest, err := destination.QueryLatest()
	if err != nil {
		t.Fatalf("query destination: %v", err)
	}
	if latest == nil || latest.Sub.Requests != 10 {
		t.Fatalf("latest destination snapshot = %#v", latest)
	}
	rangeRows, err := destination.QueryRange(time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC), renewsAt, 0)
	if err != nil {
		t.Fatalf("query destination range: %v", err)
	}
	if len(rangeRows) != 1 {
		t.Fatalf("destination row count after reimport = %d, want 1", len(rangeRows))
	}
}

func TestRunDataCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"data"},
		{"data", "unknown"},
		{"data", "import"},
		{"data", "export", "--unknown"},
	} {
		if err := runDataCommand(args); err == nil {
			t.Fatalf("runDataCommand(%q) unexpectedly succeeded", args)
		}
	}
	if err := runDataCommand([]string{"data", "--help"}); err != nil {
		t.Fatalf("data --help: %v", err)
	}
}
