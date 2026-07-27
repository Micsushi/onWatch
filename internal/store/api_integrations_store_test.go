package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
)

func insertAPIIntegrationUsageEventForTest(t *testing.T, s *Store, line, sourcePath string) {
	t.Helper()
	event, err := apiintegrations.ParseUsageEventLine([]byte(line), sourcePath)
	if err != nil {
		t.Fatalf("ParseUsageEventLine: %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
		t.Fatalf("InsertAPIIntegrationUsageEvent: %v", err)
	}
}

func TestStore_InsertAPIIntegrationUsageEvent_Dedup(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	event, err := apiintegrations.ParseUsageEventLine([]byte(`{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5}`), "/tmp/api-integrations/notes.jsonl")
	if err != nil {
		t.Fatalf("ParseUsageEventLine: %v", err)
	}

	if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
		t.Fatalf("InsertAPIIntegrationUsageEvent: %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(event); !errors.Is(err, ErrDuplicateAPIIntegrationUsageEvent) {
		t.Fatalf("expected ErrDuplicateAPIIntegrationUsageEvent, got %v", err)
	}
}

func TestStore_CompactAPIIntegrationMetadataJSON(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationUsageEventForTest(t, s,
		`{"ts":"2026-04-03T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.6","prompt_tokens":10,"completion_tokens":5}`,
		"/tmp/api-integrations/codex.jsonl",
	)
	const legacyMetadata = `{"session_id":"session-1","reasoning_effort":"high","input_tokens":10,"custom":"keep-me"}`
	if _, err := s.db.Exec(`UPDATE api_integration_usage_events SET metadata_json = ?`, legacyMetadata); err != nil {
		t.Fatalf("seed legacy metadata: %v", err)
	}

	updated, err := s.CompactAPIIntegrationMetadataJSON()
	if err != nil {
		t.Fatalf("CompactAPIIntegrationMetadataJSON: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated=%d want 1", updated)
	}

	var metadata string
	if err := s.db.QueryRow(`SELECT metadata_json FROM api_integration_usage_events`).Scan(&metadata); err != nil {
		t.Fatalf("read compacted metadata: %v", err)
	}
	if strings.Contains(metadata, "session_id") || strings.Contains(metadata, "reasoning_effort") || strings.Contains(metadata, "input_tokens") {
		t.Fatalf("duplicated metadata survived compaction: %q", metadata)
	}
	if !strings.Contains(metadata, `"custom":"keep-me"`) {
		t.Fatalf("custom metadata was removed: %q", metadata)
	}

	updated, err = s.CompactAPIIntegrationMetadataJSON()
	if err != nil {
		t.Fatalf("second CompactAPIIntegrationMetadataJSON: %v", err)
	}
	if updated != 0 {
		t.Fatalf("second compaction updated=%d want 0", updated)
	}
}

func TestStore_InsertAPIIntegrationUsageEvent_DuplicateUpdatesMetadata(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	sourcePath := "/tmp/api-integrations/codex.jsonl"
	plain, err := apiintegrations.ParseUsageEventLine([]byte(`{"ts":"2026-04-03T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":10,"completion_tokens":5}`), sourcePath)
	if err != nil {
		t.Fatalf("ParseUsageEventLine(plain): %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(plain); err != nil {
		t.Fatalf("InsertAPIIntegrationUsageEvent(plain): %v", err)
	}

	rich, err := apiintegrations.ParseUsageEventLine([]byte(`{"ts":"2026-04-03T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":10,"completion_tokens":5,"metadata":{"reasoning_effort":"xhigh","fast_mode":true}}`), sourcePath)
	if err != nil {
		t.Fatalf("ParseUsageEventLine(rich): %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(rich); !errors.Is(err, ErrDuplicateAPIIntegrationUsageEvent) {
		t.Fatalf("expected ErrDuplicateAPIIntegrationUsageEvent, got %v", err)
	}

	events, err := s.QueryAPIIntegrationUsageRange(time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageRange: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len=%d want 1", len(events))
	}
	if !strings.Contains(events[0].MetadataJSON, `"reasoning_effort":"xhigh"`) || !strings.Contains(events[0].MetadataJSON, `"fast_mode":true`) {
		t.Fatalf("metadata was not updated: %q", events[0].MetadataJSON)
	}
}

func TestStore_APIIntegrationQueriesUseNormalizedMetadataColumns(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	line := `{"ts":"2026-07-20T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.6","prompt_tokens":100,"completion_tokens":20,"cost_usd":1.25,"metadata":{"session_id":"session-1","reasoning_effort":"high","mode":"default","fast_mode":true,"input_tokens":40,"cached_input_tokens":60,"cache_creation_input_tokens":5,"output_tokens":15,"reasoning_output_tokens":5}}`
	insertAPIIntegrationUsageEventForTest(t, s, line, "/tmp/codex-normalized.jsonl")

	var (
		sessionID, effort, mode, speedMode                        string
		inputTokens, cachedTokens, cacheCreate, output, reasoning int
	)
	err = s.db.QueryRow(`
		SELECT session_id, reasoning_effort, mode, speed_mode,
		       input_tokens, cached_input_tokens, cache_creation_input_tokens,
		       output_tokens, reasoning_output_tokens
		FROM api_integration_usage_events
	`).Scan(
		&sessionID,
		&effort,
		&mode,
		&speedMode,
		&inputTokens,
		&cachedTokens,
		&cacheCreate,
		&output,
		&reasoning,
	)
	if err != nil {
		t.Fatalf("query normalized API integration columns: %v", err)
	}
	if sessionID != "session-1" || effort != "high" || mode != "default" || speedMode != "fast" {
		t.Fatalf("normalized dimensions=%q/%q/%q/%q", sessionID, effort, mode, speedMode)
	}
	if inputTokens != 40 || cachedTokens != 60 || cacheCreate != 5 || output != 15 || reasoning != 5 {
		t.Fatalf("normalized tokens=%d/%d/%d/%d/%d", inputTokens, cachedTokens, cacheCreate, output, reasoning)
	}
	var storedMetadata string
	if err := s.db.QueryRow(`SELECT metadata_json FROM api_integration_usage_events`).Scan(&storedMetadata); err != nil {
		t.Fatalf("query compact metadata: %v", err)
	}
	if strings.Contains(storedMetadata, `"session_id"`) || strings.Contains(storedMetadata, `"input_tokens"`) {
		t.Fatalf("normalized metadata is still duplicated in JSON: %s", storedMetadata)
	}
	events, err := s.QueryAPIIntegrationUsageRange(
		time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageRange: %v", err)
	}
	if len(events) != 1 || !strings.Contains(events[0].MetadataJSON, `"session_id":"session-1"`) ||
		!strings.Contains(events[0].MetadataJSON, `"input_tokens":40`) {
		t.Fatalf("rehydrated metadata=%+v", events)
	}

	if _, err := s.db.Exec(`UPDATE api_integration_usage_events SET metadata_json = ''`); err != nil {
		t.Fatalf("clear metadata_json: %v", err)
	}
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	sessions, err := s.QueryAPIIntegrationUsageSessions(start, end, "Codex CLI", 100)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "session-1" || sessions[0].InputTokens != 40 || sessions[0].ReasoningTokens != 5 {
		t.Fatalf("sessions from normalized columns=%+v", sessions)
	}
	efforts, err := s.QueryAPIIntegrationUsageEffortTotals(start, end, "Codex CLI")
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageEffortTotals: %v", err)
	}
	if len(efforts) != 1 || efforts[0].ReasoningEffort != "high" || efforts[0].SpeedMode != "fast" {
		t.Fatalf("efforts from normalized columns=%+v", efforts)
	}
}

func TestStore_QueryAPIIntegrationUsageSummary(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	lines := []string{
		`{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5,"cost_usd":0.1,"metadata":{"input_tokens":6,"cached_input_tokens":4,"cache_creation_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}`,
		`{"ts":"2026-04-03T12:01:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":2,"completion_tokens":3,"cost_usd":0.2,"metadata":{"input_tokens":2,"cached_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`,
		`{"ts":"2026-04-03T12:02:00Z","integration":"notes","provider":"mistral","model":"mistral-small-latest","prompt_tokens":4,"completion_tokens":1}`,
	}
	for i, line := range lines {
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "/tmp/api-integrations/test.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine(%d): %v", i, err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent(%d): %v", i, err)
		}
	}

	summary, err := s.QueryAPIIntegrationUsageSummary()
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageSummary: %v", err)
	}
	if len(summary) != 2 {
		t.Fatalf("len(summary)=%d want 2", len(summary))
	}
	if summary[0].Provider != "anthropic" || summary[0].RequestCount != 2 || summary[0].TotalTokens != 20 {
		t.Fatalf("anthropic summary=%+v", summary[0])
	}
	if summary[0].InputTokens != 8 || summary[0].CachedTokens != 4 || summary[0].OutputTokens != 7 || summary[0].ReasoningTokens != 1 {
		t.Fatalf("anthropic split tokens=%+v", summary[0])
	}
	if summary[0].TotalCostUSD != 0.30000000000000004 && summary[0].TotalCostUSD != 0.3 {
		t.Fatalf("anthropic cost=%v", summary[0].TotalCostUSD)
	}
}

func TestStore_QueryAPIIntegrationUsageTotals_RangeAndIntegration(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":100,"completion_tokens":20,"cost_usd":1.2,"metadata":{"input_tokens":40,"cached_input_tokens":60,"cache_creation_input_tokens":5,"output_tokens":15,"reasoning_output_tokens":5}}`, "/tmp/codex-a.jsonl")
	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-03T12:10:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":50,"completion_tokens":10,"cost_usd":0.6,"metadata":{"input_tokens":20,"cached_input_tokens":30,"output_tokens":8,"reasoning_output_tokens":2}}`, "/tmp/codex-b.jsonl")
	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-02T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":999,"completion_tokens":1,"cost_usd":9.99}`, "/tmp/codex-old.jsonl")
	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-03T12:15:00Z","integration":"Claude Code","provider":"anthropic","model":"claude-sonnet-4-6","prompt_tokens":1000,"completion_tokens":100,"cost_usd":2.0}`, "/tmp/claude.jsonl")

	rows, err := s.QueryAPIIntegrationUsageTotals(
		time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		"Codex CLI",
	)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageTotals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len=%d want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.IntegrationName != "Codex CLI" || row.RequestCount != 2 || row.TotalTokens != 180 {
		t.Fatalf("row totals=%+v", row)
	}
	if row.InputTokens != 60 || row.CachedTokens != 90 || row.CacheCreateTokens != 5 || row.OutputTokens != 23 || row.ReasoningTokens != 7 {
		t.Fatalf("row split tokens=%+v", row)
	}
	if row.TotalCostUSD != 1.7999999999999998 && row.TotalCostUSD != 1.8 {
		t.Fatalf("row cost=%v", row.TotalCostUSD)
	}
}

func TestStore_QueryAPIIntegrationUsageEffortSummary(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":10,"completion_tokens":5,"cost_usd":0.1,"metadata":{"reasoning_effort":"high","mode":"default","fast_mode":true,"input_tokens":6,"cached_input_tokens":4,"output_tokens":4,"reasoning_output_tokens":1}}`, "/tmp/codex.jsonl")
	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-03T12:01:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":7,"completion_tokens":3,"cost_usd":0.05,"metadata":{"reasoning_effort":"high","mode":"default","fast_mode":true,"input_tokens":5,"cached_input_tokens":2,"output_tokens":3,"reasoning_output_tokens":0}}`, "/tmp/codex.jsonl")
	insertAPIIntegrationUsageEventForTest(t, s, `{"ts":"2026-04-03T12:02:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":4,"completion_tokens":2,"metadata":{"reasoning_effort":"medium","mode":"default","fast_mode":false}}`, "/tmp/codex.jsonl")

	rows, err := s.QueryAPIIntegrationUsageEffortSummary()
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageEffortSummary: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len=%d want 2: %+v", len(rows), rows)
	}
	if rows[0].ReasoningEffort != "high" || rows[0].SpeedMode != "fast" || rows[0].RequestCount != 2 || rows[0].TotalTokens != 25 {
		t.Fatalf("high row=%+v", rows[0])
	}
	if rows[0].InputTokens != 11 || rows[0].CachedTokens != 6 || rows[0].OutputTokens != 7 || rows[0].ReasoningTokens != 1 {
		t.Fatalf("high row split tokens=%+v", rows[0])
	}
	if rows[1].ReasoningEffort != "medium" || rows[1].SpeedMode != "standard" || rows[1].RequestCount != 1 || rows[1].TotalTokens != 6 {
		t.Fatalf("medium row=%+v", rows[1])
	}
}

func TestStore_QueryAPIIntegrationUsageSummary_BoundedAndOrdered(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	totalGroups := apiIntegrationUsageSummaryLimit + 10
	for i := 0; i < totalGroups; i++ {
		line := fmt.Sprintf(`{"ts":"%s","integration":"integration-%03d","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":1,"completion_tokens":1}`,
			base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339),
			i,
		)
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "/tmp/api-integrations/bounded.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine(%d): %v", i, err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent(%d): %v", i, err)
		}
	}

	summary, err := s.QueryAPIIntegrationUsageSummary()
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageSummary: %v", err)
	}
	if len(summary) != apiIntegrationUsageSummaryLimit {
		t.Fatalf("len(summary)=%d want %d", len(summary), apiIntegrationUsageSummaryLimit)
	}
	if summary[0].IntegrationName != "integration-000" {
		t.Fatalf("first summary row=%+v", summary[0])
	}
	last := summary[len(summary)-1]
	if last.IntegrationName != fmt.Sprintf("integration-%03d", apiIntegrationUsageSummaryLimit-1) {
		t.Fatalf("last summary row=%+v", last)
	}
}

func TestStore_QueryAPIIntegrationUsageRange_AndIngestState(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	event, err := apiintegrations.ParseUsageEventLine([]byte(`{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":7,"completion_tokens":2}`), "/tmp/api-integrations/notes.jsonl")
	if err != nil {
		t.Fatalf("ParseUsageEventLine: %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
		t.Fatalf("InsertAPIIntegrationUsageEvent: %v", err)
	}

	start := time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 3, 13, 0, 0, 0, time.UTC)
	events, err := s.QueryAPIIntegrationUsageRange(start, end)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageRange: %v", err)
	}
	if len(events) != 1 || events[0].TotalTokens != 9 {
		t.Fatalf("events=%+v", events)
	}

	state := &apiintegrations.IngestState{
		SourcePath:  "/tmp/api-integrations/notes.jsonl",
		Offset:      42,
		FileSize:    100,
		FileModTime: time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC),
		PartialLine: `{"ts":"2026`,
	}
	if err := s.UpsertAPIIntegrationIngestState(state); err != nil {
		t.Fatalf("UpsertAPIIntegrationIngestState: %v", err)
	}
	got, err := s.GetAPIIntegrationIngestState(state.SourcePath)
	if err != nil {
		t.Fatalf("GetAPIIntegrationIngestState: %v", err)
	}
	if got == nil || got.Offset != 42 || got.PartialLine != state.PartialLine {
		t.Fatalf("state=%+v", got)
	}
	if got.PartialLineBytes != len(state.PartialLine) || got.PartialLineOversized {
		t.Fatalf("unexpected partial line metadata: %+v", got)
	}
}

func TestStore_GetAPIIntegrationIngestState_BoundsOversizedPartialLine(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	state := &apiintegrations.IngestState{
		SourcePath:  "/tmp/api-integrations/oversized.jsonl",
		Offset:      7,
		FileSize:    9,
		FileModTime: time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC),
		PartialLine: strings.Repeat("x", apiintegrations.MaxIngestPartialLineBytes+1),
	}
	if err := s.UpsertAPIIntegrationIngestState(state); err != nil {
		t.Fatalf("UpsertAPIIntegrationIngestState: %v", err)
	}

	got, err := s.GetAPIIntegrationIngestState(state.SourcePath)
	if err != nil {
		t.Fatalf("GetAPIIntegrationIngestState: %v", err)
	}
	if got == nil {
		t.Fatal("expected ingest state")
	}
	if got.PartialLine != "" {
		t.Fatalf("expected bounded partial line to be empty, got len=%d", len(got.PartialLine))
	}
	if !got.PartialLineOversized {
		t.Fatalf("expected oversized flag, got %+v", got)
	}
	if got.PartialLineBytes != len(state.PartialLine) {
		t.Fatalf("partial line bytes=%d want %d", got.PartialLineBytes, len(state.PartialLine))
	}
}

func TestStore_DeleteAPIIntegrationUsageEventsOlderThan(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	lines := []string{
		`{"ts":"2026-01-01T12:00:00Z","integration":"notes","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":1,"completion_tokens":1}`,
		`{"ts":"2026-03-15T12:00:00Z","integration":"notes","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":2,"completion_tokens":2}`,
	}
	for i, line := range lines {
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "/tmp/api-integrations/retention.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine(%d): %v", i, err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent(%d): %v", i, err)
		}
	}

	deleted, err := s.DeleteAPIIntegrationUsageEventsOlderThan(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("DeleteAPIIntegrationUsageEventsOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want 1", deleted)
	}

	events, err := s.QueryAPIIntegrationUsageRange(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageRange: %v", err)
	}
	if len(events) != 1 || events[0].Timestamp.Format(time.RFC3339) != "2026-03-15T12:00:00Z" {
		t.Fatalf("events=%+v", events)
	}
}

func TestStore_CompactAPIIntegrationUsageEventsHourly(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	sourcePath := "/tmp/api-integrations/compact.jsonl"
	oldLineA := `{"ts":"2026-01-15T12:05:00Z","integration":"Codex CLI","provider":"openai","account":"work","model":"gpt-5.6-sol","prompt_tokens":100,"completion_tokens":20,"cost_usd":0.25,"metadata":{"input_tokens":80,"cached_input_tokens":20,"output_tokens":15,"reasoning_output_tokens":5,"reasoning_effort":"high","mode":"agent","fast_mode":true}}`
	oldLineB := `{"ts":"2026-01-15T12:45:00Z","integration":"Codex CLI","provider":"openai","account":"work","model":"gpt-5.6-sol","prompt_tokens":50,"completion_tokens":10,"cost_usd":0.15,"metadata":{"input_tokens":40,"cached_input_tokens":10,"output_tokens":8,"reasoning_output_tokens":2,"reasoning_effort":"high","mode":"agent","fast_mode":true}}`
	recentLine := `{"ts":"2026-03-20T12:00:00Z","integration":"Codex CLI","provider":"openai","account":"work","model":"gpt-5.6-sol","prompt_tokens":7,"completion_tokens":3,"cost_usd":0.05}`
	for _, line := range []string{oldLineA, oldLineB, recentLine} {
		insertAPIIntegrationUsageEventForTest(t, s, line, sourcePath)
	}
	if _, err := s.db.Exec(`
		INSERT INTO data_transfer_records (table_name, local_record_id, origin_id, origin_record_id)
		SELECT 'api_integration_usage_events', CAST(id AS TEXT), 'imported-test', fingerprint
		FROM api_integration_usage_events
	`); err != nil {
		t.Fatalf("insert transfer provenance: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO data_transfer_records (table_name, local_record_id, origin_id, origin_record_id)
		VALUES ('api_integration_usage_events', 'unrelated-orphan', 'other-import', 'other-record')
	`); err != nil {
		t.Fatalf("insert unrelated transfer provenance: %v", err)
	}

	result, err := s.CompactAPIIntegrationUsageEvents(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CompactAPIIntegrationUsageEvents: %v", err)
	}
	if result.CompactedEvents != 2 || result.HourlyRows != 1 {
		t.Fatalf("result=%+v", result)
	}

	var requests, prompt, completion, total, input, cached, output, reasoning int64
	var cost float64
	err = s.db.QueryRow(`
		SELECT request_count, prompt_tokens, completion_tokens, total_tokens,
		       input_tokens, cached_input_tokens, output_tokens, reasoning_output_tokens, total_cost_usd
		FROM api_integration_usage_hourly
		WHERE hour_start = '2026-01-15T12:00:00Z'
	`).Scan(&requests, &prompt, &completion, &total, &input, &cached, &output, &reasoning, &cost)
	if err != nil {
		t.Fatalf("query archive row: %v", err)
	}
	if requests != 2 || prompt != 150 || completion != 30 || total != 180 ||
		input != 120 || cached != 30 || output != 23 || reasoning != 7 || cost != 0.4 {
		t.Fatalf("archive totals requests=%d prompt=%d completion=%d total=%d input=%d cached=%d output=%d reasoning=%d cost=%v",
			requests, prompt, completion, total, input, cached, output, reasoning, cost)
	}

	var rawCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_integration_usage_events`).Scan(&rawCount); err != nil {
		t.Fatalf("count raw events: %v", err)
	}
	if rawCount != 1 {
		t.Fatalf("rawCount=%d want 1", rawCount)
	}
	var provenanceCount int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM data_transfer_records
		WHERE table_name = 'api_integration_usage_events'
	`).Scan(&provenanceCount); err != nil {
		t.Fatalf("count API integration provenance: %v", err)
	}
	if provenanceCount != 2 {
		t.Fatalf("provenanceCount=%d want 2 (recent event plus unrelated orphan)", provenanceCount)
	}

	replayed, err := apiintegrations.ParseUsageEventLine([]byte(oldLineA), sourcePath)
	if err != nil {
		t.Fatalf("ParseUsageEventLine(replayed): %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(replayed); !errors.Is(err, ErrDuplicateAPIIntegrationUsageEvent) {
		t.Fatalf("replayed compacted event error=%v want duplicate", err)
	}

	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC)
	buckets, err := s.QueryAPIIntegrationUsageBuckets(start, end, time.Hour)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageBuckets: %v", err)
	}
	if len(buckets) != 2 || buckets[0].RequestCount != 2 || buckets[1].RequestCount != 1 {
		t.Fatalf("mixed buckets=%+v", buckets)
	}

	totals, err := s.QueryAPIIntegrationUsageTotals(start, end, "Codex CLI")
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageTotals: %v", err)
	}
	if len(totals) != 1 || totals[0].RequestCount != 3 || totals[0].TotalTokens != 190 || totals[0].TotalCostUSD != 0.45 {
		t.Fatalf("mixed totals=%+v", totals)
	}

	efforts, err := s.QueryAPIIntegrationUsageEffortTotals(start, end, "Codex CLI")
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageEffortTotals: %v", err)
	}
	var requestTotal int
	for _, effort := range efforts {
		requestTotal += effort.RequestCount
	}
	if requestTotal != 3 {
		t.Fatalf("mixed effort totals=%+v", efforts)
	}
}

func TestStore_QueryAPIIntegrationUsageBuckets(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	lines := []string{
		`{"ts":"2026-04-03T12:01:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5,"cost_usd":0.1}`,
		`{"ts":"2026-04-03T12:04:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":2,"completion_tokens":3,"cost_usd":0.2}`,
		`{"ts":"2026-04-03T12:16:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":4,"completion_tokens":1}`,
		`{"ts":"2026-04-03T12:08:00Z","integration":"daily-report","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":6,"completion_tokens":2}`,
	}
	for i, line := range lines {
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "/tmp/api-integrations/test.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine(%d): %v", i, err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent(%d): %v", i, err)
		}
	}

	start := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 3, 13, 0, 0, 0, time.UTC)
	rows, err := s.QueryAPIIntegrationUsageBuckets(start, end, 15*time.Minute)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageBuckets: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len(rows)=%d want 3", len(rows))
	}

	if rows[0].IntegrationName != "daily-report" || rows[0].BucketStart.Format(time.RFC3339) != "2026-04-03T12:00:00Z" || rows[0].TotalTokens != 8 {
		t.Fatalf("unexpected first bucket: %+v", rows[0])
	}
	if rows[1].IntegrationName != "notes" || rows[1].BucketStart.Format(time.RFC3339) != "2026-04-03T12:00:00Z" || rows[1].RequestCount != 2 || rows[1].TotalTokens != 20 {
		t.Fatalf("unexpected second bucket: %+v", rows[1])
	}
	if rows[1].TotalCostUSD < 0.299 || rows[1].TotalCostUSD > 0.301 {
		t.Fatalf("unexpected second bucket cost: %+v", rows[1])
	}
	if rows[2].IntegrationName != "notes" || rows[2].BucketStart.Format(time.RFC3339) != "2026-04-03T12:15:00Z" || rows[2].TotalTokens != 5 {
		t.Fatalf("unexpected third bucket: %+v", rows[2])
	}
}

func TestStore_QueryAPIIntegrationUsageBuckets_HourlyRange(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	lines := []string{
		`{"ts":"2026-04-03T12:10:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":3,"completion_tokens":2}`,
		`{"ts":"2026-04-03T12:50:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":4,"completion_tokens":1}`,
		`{"ts":"2026-04-03T13:05:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":5,"completion_tokens":5,"cost_usd":0.5}`,
		`{"ts":"2026-04-03T13:25:00Z","integration":"report","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":7,"completion_tokens":3}`,
	}
	for i, line := range lines {
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "/tmp/api-integrations/hourly.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine(%d): %v", i, err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent(%d): %v", i, err)
		}
	}

	start := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	rows, err := s.QueryAPIIntegrationUsageBuckets(start, end, time.Hour)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageBuckets: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len(rows)=%d want 3", len(rows))
	}
	if rows[0].IntegrationName != "notes" || rows[0].BucketStart.Format(time.RFC3339) != "2026-04-03T12:00:00Z" || rows[0].TotalTokens != 10 {
		t.Fatalf("unexpected first hourly bucket: %+v", rows[0])
	}
	if rows[1].IntegrationName != "notes" || rows[1].BucketStart.Format(time.RFC3339) != "2026-04-03T13:00:00Z" || rows[1].TotalCostUSD != 0.5 {
		t.Fatalf("unexpected second hourly bucket: %+v", rows[1])
	}
	if rows[2].IntegrationName != "report" || rows[2].BucketStart.Format(time.RFC3339) != "2026-04-03T13:00:00Z" || rows[2].TotalTokens != 10 {
		t.Fatalf("unexpected third hourly bucket: %+v", rows[2])
	}
}

func TestStore_QueryAPIIntegrationUsageBuckets_Bounded(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	// Insert apiIntegrationUsageBucketsLimit + 10 events, each in its own 1-minute bucket
	// across different integrations so GROUP BY produces many rows.
	base := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	total := apiIntegrationUsageBucketsLimit + 10
	for i := 0; i < total; i++ {
		line := fmt.Sprintf(`{"ts":"%s","integration":"integ-%04d","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":1,"completion_tokens":1}`,
			base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), i)
		event, err := apiintegrations.ParseUsageEventLine([]byte(line), "/tmp/bounded.jsonl")
		if err != nil {
			t.Fatalf("ParseUsageEventLine(%d): %v", i, err)
		}
		if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
			t.Fatalf("InsertAPIIntegrationUsageEvent(%d): %v", i, err)
		}
	}

	start := base
	end := base.Add(time.Duration(total+1) * time.Minute)
	rows, err := s.QueryAPIIntegrationUsageBuckets(start, end, time.Minute)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageBuckets: %v", err)
	}
	if len(rows) != apiIntegrationUsageBucketsLimit {
		t.Fatalf("len(rows)=%d want %d", len(rows), apiIntegrationUsageBucketsLimit)
	}
}

func TestStore_QueryAPIIntegrationIngestHealth_AndAlertsByProvider(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	stateA := &apiintegrations.IngestState{
		SourcePath:  "/tmp/api-integrations/notes.jsonl",
		Offset:      128,
		FileSize:    256,
		FileModTime: time.Date(2026, 4, 3, 12, 5, 0, 0, time.UTC),
		PartialLine: `{"ts":"2026-04`,
	}
	stateB := &apiintegrations.IngestState{
		SourcePath:  "/tmp/api-integrations/report.jsonl",
		Offset:      64,
		FileSize:    64,
		FileModTime: time.Date(2026, 4, 3, 12, 6, 0, 0, time.UTC),
	}
	if err := s.UpsertAPIIntegrationIngestState(stateA); err != nil {
		t.Fatalf("UpsertAPIIntegrationIngestState(stateA): %v", err)
	}
	if err := s.UpsertAPIIntegrationIngestState(stateB); err != nil {
		t.Fatalf("UpsertAPIIntegrationIngestState(stateB): %v", err)
	}

	event, err := apiintegrations.ParseUsageEventLine([]byte(`{"ts":"2026-04-03T12:07:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5}`), stateA.SourcePath)
	if err != nil {
		t.Fatalf("ParseUsageEventLine: %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
		t.Fatalf("InsertAPIIntegrationUsageEvent: %v", err)
	}

	if _, err := s.CreateSystemAlert("api_integrations", "ingest_warning", "Bad line", "Skipped malformed JSON", "warning", `{"sourcePath":"/tmp/api-integrations/notes.jsonl"}`); err != nil {
		t.Fatalf("CreateSystemAlert(api_integrations): %v", err)
	}
	if _, err := s.CreateSystemAlert("anthropic", "auth_error", "Nope", "ignore me", "error", ""); err != nil {
		t.Fatalf("CreateSystemAlert(anthropic): %v", err)
	}

	healthRows, err := s.QueryAPIIntegrationIngestHealth()
	if err != nil {
		t.Fatalf("QueryAPIIntegrationIngestHealth: %v", err)
	}
	if len(healthRows) != 2 {
		t.Fatalf("len(healthRows)=%d want 2", len(healthRows))
	}
	if healthRows[0].SourcePath != stateA.SourcePath {
		t.Fatalf("unexpected first health row: %+v", healthRows[0])
	}
	if healthRows[0].LastCapturedAt == nil || healthRows[0].LastCapturedAt.Format(time.RFC3339) != "2026-04-03T12:07:00Z" {
		t.Fatalf("unexpected first health lastCapturedAt: %+v", healthRows[0])
	}
	if healthRows[1].SourcePath != stateB.SourcePath || healthRows[1].LastCapturedAt != nil {
		t.Fatalf("unexpected second health row: %+v", healthRows[1])
	}

	alerts, err := s.GetActiveSystemAlertsByProvider("api_integrations", 10)
	if err != nil {
		t.Fatalf("GetActiveSystemAlertsByProvider: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts)=%d want 1", len(alerts))
	}
	if alerts[0].Provider != "api_integrations" || alerts[0].AlertType != "ingest_warning" {
		t.Fatalf("unexpected alert: %+v", alerts[0])
	}
	if alerts[0].CreatedAt.Format(time.RFC3339) == "0001-01-01T00:00:00Z" {
		t.Fatalf("expected parsed alert createdAt, got %+v", alerts[0])
	}
}
