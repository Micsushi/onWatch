package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

func TestCompactProviderSnapshotHistoryKeepsOneDailyPointBeforeCutoff(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for day := range 2 {
		for sample := range 3 {
			capturedAt := base.Add(time.Duration(day)*24*time.Hour + time.Duration(sample)*time.Hour)
			resetAt := capturedAt.Add(5 * time.Hour)
			codex := newTestCodexSnapshot(capturedAt, &resetAt)
			if _, err := s.InsertCodexSnapshot(codex); err != nil {
				t.Fatalf("InsertCodexSnapshot: %v", err)
			}
			gemini := &api.GeminiSnapshot{
				CapturedAt: capturedAt,
				Quotas: []api.GeminiQuota{{
					ModelID:      "gemini-test",
					UsagePercent: float64(sample),
				}},
			}
			if _, err := s.InsertGeminiSnapshot(gemini); err != nil {
				t.Fatalf("InsertGeminiSnapshot: %v", err)
			}
			anthropic := &api.AnthropicSnapshot{
				CapturedAt: capturedAt,
				Quotas: []api.AnthropicQuota{{
					Name:        "five_hour",
					Utilization: float64(sample),
				}},
			}
			if _, err := s.InsertAnthropicSnapshot(anthropic); err != nil {
				t.Fatalf("InsertAnthropicSnapshot: %v", err)
			}
		}
	}

	recent := base.Add(40 * 24 * time.Hour)
	resetAt := recent.Add(5 * time.Hour)
	if _, err := s.InsertCodexSnapshot(newTestCodexSnapshot(recent, &resetAt)); err != nil {
		t.Fatalf("InsertCodexSnapshot recent: %v", err)
	}
	if _, err := s.InsertGeminiSnapshot(&api.GeminiSnapshot{
		CapturedAt: recent,
		Quotas:     []api.GeminiQuota{{ModelID: "gemini-test"}},
	}); err != nil {
		t.Fatalf("InsertGeminiSnapshot recent: %v", err)
	}
	if _, err := s.InsertAnthropicSnapshot(&api.AnthropicSnapshot{
		CapturedAt: recent,
		Quotas:     []api.AnthropicQuota{{Name: "five_hour"}},
	}); err != nil {
		t.Fatalf("InsertAnthropicSnapshot recent: %v", err)
	}

	result, err := s.CompactProviderSnapshotHistory(base.Add(30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("CompactProviderSnapshotHistory: %v", err)
	}
	if result.DeletedSnapshots != 12 {
		t.Fatalf("DeletedSnapshots=%d want 12", result.DeletedSnapshots)
	}

	for _, table := range []string{"codex_snapshots", "gemini_snapshots", "anthropic_snapshots"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 3 {
			t.Fatalf("%s count=%d want 3", table, count)
		}
	}
	for _, table := range []string{"codex_quota_values", "gemini_quota_values", "anthropic_quota_values"} {
		var orphanCount int
		parent := map[string]string{
			"codex_quota_values":     "codex_snapshots",
			"gemini_quota_values":    "gemini_snapshots",
			"anthropic_quota_values": "anthropic_snapshots",
		}[table]
		if err := s.db.QueryRow(`
			SELECT COUNT(*) FROM ` + table + ` child
			WHERE NOT EXISTS (SELECT 1 FROM ` + parent + ` parent WHERE parent.id = child.snapshot_id)
		`).Scan(&orphanCount); err != nil {
			t.Fatalf("count %s orphans: %v", table, err)
		}
		if orphanCount != 0 {
			t.Fatalf("%s orphan count=%d", table, orphanCount)
		}
	}
}
