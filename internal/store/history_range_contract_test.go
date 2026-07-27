package store

import (
	"fmt"
	"os"
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
