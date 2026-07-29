package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func usageLine(minute int, promptTokens int) string {
	return fmt.Sprintf(
		`{"ts":"2026-04-03T12:%02d:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":%d,"completion_tokens":2}`+"\n",
		minute, promptTokens)
}

func queueEventCount(t *testing.T, st *store.Store) int {
	t.Helper()
	events, err := st.QueryAPIIntegrationUsageRange(
		time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 3, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageRange: %v", err)
	}
	return len(events)
}

func activeIngestAlerts(t *testing.T, st *store.Store) []store.SystemAlert {
	t.Helper()
	alerts, err := st.GetActiveSystemAlerts()
	if err != nil {
		t.Fatalf("GetActiveSystemAlerts: %v", err)
	}
	matched := make([]store.SystemAlert, 0, len(alerts))
	for _, alert := range alerts {
		if alert.AlertType == "ingest_error" {
			matched = append(matched, alert)
		}
	}
	return matched
}

// The queue file is deleted once drained and recreated by the collector under
// the same name. The persisted cursor must not outlive the file it describes,
// or the next scan seeks into the middle of a line in the new file.
func TestAPIIntegrationsIngestAgent_RecreatedQueueFileIsReadFromStart(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "agent-usage-2026-04-03.jsonl")
	ag := NewAPIIntegrationsIngestAgent(st, dir, 0, slog.Default())

	// First generation: two events, then drained and removed.
	first := usageLine(0, 10) + usageLine(1, 11)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ag.scanFile(path); err != nil {
		t.Fatalf("scanFile(first): %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected the drained queue file to be removed, stat err = %v", statErr)
	}
	if got := queueEventCount(t, st); got != 2 {
		t.Fatalf("expected 2 events from the first generation, got %d", got)
	}

	// Second generation: recreated at least as large as the old cursor, so a
	// size comparison alone cannot detect that this is a different file.
	second := usageLine(2, 12) + usageLine(3, 13) + usageLine(4, 14)
	if len(second) < len(first) {
		t.Fatalf("test setup: second generation must not be smaller than the first")
	}
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatalf("WriteFile(second): %v", err)
	}
	if err := ag.scanFile(path); err != nil {
		t.Fatalf("scanFile(second): %v", err)
	}

	if alerts := activeIngestAlerts(t, st); len(alerts) != 0 {
		t.Fatalf("recreated file produced parse alerts: %+v", alerts)
	}
	if got := queueEventCount(t, st); got != 5 {
		t.Fatalf("expected all 5 events across both generations, got %d", got)
	}
}

func TestAPIIntegrationsIngestAgent_RemovingConsumedFileClearsCursor(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "agent-usage-2026-04-03.jsonl")
	if err := os.WriteFile(path, []byte(usageLine(0, 10)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ag := NewAPIIntegrationsIngestAgent(st, dir, 0, slog.Default())
	if err := ag.scanFile(path); err != nil {
		t.Fatalf("scanFile: %v", err)
	}

	state, err := st.GetAPIIntegrationIngestState(path)
	if err != nil {
		t.Fatalf("GetAPIIntegrationIngestState: %v", err)
	}
	if state != nil {
		t.Fatalf("cursor outlived the removed queue file: %+v", state)
	}
}

// Defence in depth: if a cursor ever survives its file (a crash between the
// remove and the cursor delete), resuming must not start mid-line.
func TestAPIIntegrationsIngestAgent_ResumeOffsetOffLineBoundaryRestarts(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	content := usageLine(0, 10) + usageLine(1, 11)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// A cursor pointing into the middle of the first line.
	if err := st.UpsertAPIIntegrationIngestState(&apiintegrations.IngestState{
		SourcePath: path,
		Offset:     20,
		FileSize:   int64(len(content)),
	}); err != nil {
		t.Fatalf("UpsertAPIIntegrationIngestState: %v", err)
	}

	ag := NewAPIIntegrationsIngestAgent(st, dir, 0, slog.Default())
	if err := ag.scanFile(path); err != nil {
		t.Fatalf("scanFile: %v", err)
	}

	if alerts := activeIngestAlerts(t, st); len(alerts) != 0 {
		t.Fatalf("off-boundary resume produced parse alerts: %+v", alerts)
	}
	if got := queueEventCount(t, st); got != 2 {
		t.Fatalf("expected both events after restart, got %d", got)
	}
}
