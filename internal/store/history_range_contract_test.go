package store

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
)

func TestHistoryRangeQueriesUseHalfOpenEnd(t *testing.T) {
	t.Parallel()
	files := []string{
		"store.go",
		"zai_store.go",
		"gemini_store.go",
		"openrouter_store.go",
		"cursor_store.go",
		"anthropic_store.go",
		"codex_store.go",
		"copilot_store.go",
		"antigravity_store.go",
		"minimax_store.go",
		"api_integrations_store.go",
	}
	for _, name := range files {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if strings.Contains(string(source), "captured_at BETWEEN ? AND ?") {
			t.Errorf("%s still contains an inclusive history end boundary", name)
		}
	}
}

func TestHistoryQueriesUseChronologicalTimestampCollation(t *testing.T) {
	t.Parallel()
	files := []string{
		"store.go",
		"zai_store.go",
		"gemini_store.go",
		"openrouter_store.go",
		"cursor_store.go",
		"anthropic_store.go",
		"codex_store.go",
		"copilot_store.go",
		"antigravity_store.go",
		"minimax_store.go",
		"api_integrations_store.go",
		"migration.go",
	}
	uncollatedComparison := regexp.MustCompile(`(?:\w+\.)?captured_at\s*(?:<=|>=|<|>)`)
	uncollatedOrdering := regexp.MustCompile(`ORDER BY\s+(?:\w+\.)?captured_at\s+(?:ASC|DESC)`)
	for _, name := range files {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if match := uncollatedComparison.Find(source); match != nil {
			t.Errorf("%s contains an uncollated timestamp comparison: %s", name, match)
		}
		if match := uncollatedOrdering.Find(source); match != nil {
			t.Errorf("%s contains uncollated timestamp ordering: %s", name, match)
		}
	}
}

func TestQueryRangeExcludesSnapshotAtEnd(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	start := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for _, capturedAt := range []time.Time{start, start.Add(30 * time.Minute), end} {
		renewsAt := capturedAt.Add(time.Hour)
		if _, err := s.InsertSnapshot(&api.Snapshot{
			CapturedAt: capturedAt,
			Sub:        api.QuotaInfo{RenewsAt: renewsAt},
			Search:     api.QuotaInfo{RenewsAt: renewsAt},
			ToolCall:   api.QuotaInfo{RenewsAt: renewsAt},
		}); err != nil {
			t.Fatalf("InsertSnapshot(%s): %v", capturedAt, err)
		}
	}

	rows, err := s.QueryRange(start, end)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("QueryRange returned %d rows, want 2 without the end boundary", len(rows))
	}
	limited, err := s.QueryRange(start, end, 10)
	if err != nil {
		t.Fatalf("QueryRange limited: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited QueryRange returned %d rows, want 2 without the end boundary", len(limited))
	}
}

func TestProviderRangesOrderFractionalSecondsChronologically(t *testing.T) {
	t.Parallel()

	capturedAt := time.Date(2026, 7, 1, 12, 0, 0, 123_000_000, time.UTC)
	start := capturedAt.Add(-time.Hour)
	end := capturedAt.Add(100 * time.Microsecond)

	t.Run("synthetic", func(t *testing.T) {
		s, err := New(":memory:")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer s.Close()

		renewsAt := capturedAt.Add(time.Hour)
		if _, err := s.InsertSnapshot(&api.Snapshot{
			CapturedAt: capturedAt,
			Sub:        api.QuotaInfo{RenewsAt: renewsAt},
			Search:     api.QuotaInfo{RenewsAt: renewsAt},
			ToolCall:   api.QuotaInfo{RenewsAt: renewsAt},
		}); err != nil {
			t.Fatalf("InsertSnapshot: %v", err)
		}

		rows, err := s.QueryRange(start, end)
		if err != nil {
			t.Fatalf("QueryRange: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("QueryRange returned %d rows, want 1", len(rows))
		}
	})

	t.Run("zai", func(t *testing.T) {
		s, err := New(":memory:")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer s.Close()

		if _, err := s.InsertZaiSnapshot(&api.ZaiSnapshot{CapturedAt: capturedAt}); err != nil {
			t.Fatalf("InsertZaiSnapshot: %v", err)
		}

		rows, err := s.QueryZaiRange(start, end)
		if err != nil {
			t.Fatalf("QueryZaiRange: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("QueryZaiRange returned %d rows, want 1", len(rows))
		}
		limited, err := s.QueryZaiRange(start, end, 200)
		if err != nil {
			t.Fatalf("limited QueryZaiRange: %v", err)
		}
		if len(limited) != 1 {
			t.Fatalf("limited QueryZaiRange returned %d rows, want 1", len(limited))
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		s, err := New(":memory:")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer s.Close()

		if _, err := s.InsertAnthropicSnapshot(&api.AnthropicSnapshot{
			CapturedAt: capturedAt,
			Quotas: []api.AnthropicQuota{{
				Name:        "five_hour",
				Utilization: 10,
			}},
		}); err != nil {
			t.Fatalf("InsertAnthropicSnapshot: %v", err)
		}

		rows, err := s.QueryAnthropicRange(start, end)
		if err != nil {
			t.Fatalf("QueryAnthropicRange: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("QueryAnthropicRange returned %d rows, want 1", len(rows))
		}
		limited, err := s.QueryAnthropicRange(start, end, 200)
		if err != nil {
			t.Fatalf("limited QueryAnthropicRange: %v", err)
		}
		if len(limited) != 1 {
			t.Fatalf("limited QueryAnthropicRange returned %d rows, want 1", len(limited))
		}
	})

	t.Run("gemini", func(t *testing.T) {
		s, err := New(":memory:")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer s.Close()

		if _, err := s.InsertGeminiSnapshot(&api.GeminiSnapshot{
			CapturedAt: capturedAt,
			Quotas: []api.GeminiQuota{{
				ModelID:           "gemini-2.5-pro",
				RemainingFraction: 0.9,
				UsagePercent:      10,
			}},
		}); err != nil {
			t.Fatalf("InsertGeminiSnapshot: %v", err)
		}

		rows, err := s.QueryGeminiRange(start, end)
		if err != nil {
			t.Fatalf("QueryGeminiRange: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("QueryGeminiRange returned %d rows, want 1", len(rows))
		}
		limited, err := s.QueryGeminiRange(start, end, 200)
		if err != nil {
			t.Fatalf("limited QueryGeminiRange: %v", err)
		}
		if len(limited) != 1 {
			t.Fatalf("limited QueryGeminiRange returned %d rows, want 1", len(limited))
		}
	})
}

func TestAPIIntegrationRangesExcludeEventAtEnd(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	start := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for index, capturedAt := range []time.Time{start, start.Add(30 * time.Minute), end} {
		line := fmt.Sprintf(
			`{"ts":%q,"integration":"Codex CLI","provider":"openai","model":"gpt-test","prompt_tokens":1,"completion_tokens":1,"metadata":{"session_id":"session-1","event_key":%q}}`,
			capturedAt.Format(time.RFC3339Nano),
			fmt.Sprintf("event-%d", index),
		)
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "history-range-test.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine: %v", err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent: %v", err)
		}
	}

	events, err := s.QueryAPIIntegrationUsageRange(start, end)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageRange: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("usage range returned %d events, want 2 without the end boundary", len(events))
	}
	sessions, err := s.QueryAPIIntegrationUsageSessions(start, end, "", 10)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].RequestCount != 2 {
		t.Fatalf("session range included the end boundary: %+v", sessions)
	}
}
