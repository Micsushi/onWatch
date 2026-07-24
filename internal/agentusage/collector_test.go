package agentusage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectorWritesNormalizedJSONLAndSkipsAlreadySeenEvents(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "claude.jsonl")
	writeFixture(t, source, []string{
		`{"timestamp":"2026-05-25T12:34:56Z","sessionId":"s1","requestId":"req_1","message":{"id":"m1","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":10}}}`,
	})
	outDir := filepath.Join(dir, "out")

	collector := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceClaude, Path: source},
	}, nil)

	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() second error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1: %s", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"integration":"Claude Code"`) {
		t.Fatalf("missing Claude integration line: %s", lines[0])
	}
}

func TestCollectorDedupesClaudeStreamingUsageForSameMessage(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "claude.jsonl")
	writeFixture(t, source, []string{
		`{"timestamp":"2026-05-25T12:34:56.001Z","sessionId":"s1","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":20,"output_tokens":10}}}`,
		`{"timestamp":"2026-05-25T12:34:56.500Z","sessionId":"s1","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":20,"output_tokens":10}}}`,
	})
	outDir := filepath.Join(dir, "out")

	collector := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceClaude, Path: source},
	}, nil)

	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1: %s", len(lines), string(data))
	}
}

func TestCollectorOnlyRescansChangedFilesAfterInitialPass(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"total_tokens":110}}}}`,
	})
	outDir := filepath.Join(dir, "out")
	collector := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceCodex, Path: source},
	}, nil)

	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() unchanged error = %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n" + `{"type":"event_msg","timestamp":"2026-05-25T12:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"cached_input_tokens":30,"output_tokens":20,"total_tokens":220}}}}` + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() changed error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2: %s", len(lines), string(data))
	}
	if !strings.Contains(lines[1], `"ts":"2026-05-25T12:00:02Z"`) {
		t.Fatalf("second line did not use event timestamp: %s", lines[1])
	}
}

func TestCollectorCodexReadsOnlyAppendedBytesAndPreservesContext(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"high","collaboration_mode":{"mode":"default","settings":{"reasoning_effort":"high"}},"fast_mode":true}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)

	first, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first events len = %d, want 1", len(first))
	}

	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"event_msg","timestamp":"2026-05-25T12:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"output_tokens":20,"total_tokens":220}}}}` + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	appended, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(append) error = %v", err)
	}
	if len(appended) != 1 {
		t.Fatalf("appended events len = %d, want 1: %+v", len(appended), appended)
	}
	event := appended[0]
	if event.Model != "gpt-5.5" || event.ReasoningEffort != "high" || event.Mode != "default" || event.FastMode == nil || !*event.FastMode {
		t.Fatalf("appended event lost turn context: %+v", event)
	}
	if event.TotalTokens != 220 {
		t.Fatalf("appended total tokens = %d, want 220", event.TotalTokens)
	}
}

func TestCollectorCodexRetainsIncompleteTrailingLine(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	contextLine := `{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`
	eventLine := `{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`
	split := len(eventLine) / 2
	if err := os.WriteFile(source, []byte(contextLine+"\n"+eventLine[:split]), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(partial) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("partial events len = %d, want 0", len(events))
	}

	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(eventLine[split:] + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	events, err = collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(completed) error = %v", err)
	}
	if len(events) != 1 || events[0].Model != "gpt-5.5" || events[0].TotalTokens != 110 {
		t.Fatalf("completed events = %+v, want one gpt-5.5 event with 110 tokens", events)
	}
}

func TestCollectorCodexResetsAfterTruncation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":200,"output_tokens":100,"reasoning_output_tokens":50,"total_tokens":1350}}}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}

	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T13:00:00Z","payload":{"model":"gpt-5.2-codex","effort":"low"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T13:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}}`,
	})

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(truncated) error = %v", err)
	}
	if len(events) != 1 || events[0].Model != "gpt-5.2-codex" || events[0].TotalTokens != 6 {
		t.Fatalf("truncated events = %+v, want replacement event", events)
	}
}

func TestCollectorCodexResetsAfterFileReplacement(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}

	replacement := filepath.Join(dir, "replacement.jsonl")
	writeFixture(t, replacement, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T13:00:00Z","payload":{"model":"gpt-5.2-codex","effort":"low","padding":"` + strings.Repeat("x", 300) + `"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T13:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}}`,
	})
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatal(err)
	}

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(replacement) error = %v", err)
	}
	if len(events) != 1 || events[0].Model != "gpt-5.2-codex" || events[0].TotalTokens != 6 {
		t.Fatalf("replacement events = %+v, want replacement event", events)
	}
}

func TestCollectorCodexResetsAfterSameFileRewriteAndRegrow(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}

	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T13:00:00Z","payload":{"model":"gpt-5.2-codex","effort":"low","padding":"` + strings.Repeat("x", 500) + `"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T13:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}}`,
	})

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(rewritten) error = %v", err)
	}
	if len(events) != 1 || events[0].Model != "gpt-5.2-codex" || events[0].TotalTokens != 6 {
		t.Fatalf("rewritten events = %+v, want replacement event", events)
	}
}

func TestCollectorCodexRetriesCompleteParserErrorWithoutFileChange(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}
	before := collector.codexStates[source].offset

	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{not-json}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := collector.collectSource(collector.sources[0]); err == nil {
		t.Fatal("collectSource(malformed) error = nil, want parser error")
	}
	if got := collector.codexStates[source].offset; got != before {
		t.Fatalf("parser offset after error = %d, want unchanged %d", got, before)
	}
	if _, err := collector.collectSource(collector.sources[0]); err == nil {
		t.Fatal("collectSource(retry) error = nil, want the unchanged malformed line retried")
	}
}

func TestCollectorRetriesWholeSourceAfterLaterFileFails(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(sourceDir, "first.jsonl")
	secondPath := filepath.Join(sourceDir, "second.jsonl")
	firstContext := `{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`
	secondContext := `{"type":"turn_context","timestamp":"2026-05-25T12:01:00Z","payload":{"model":"gpt-5.2-codex","effort":"low"}}`
	writeFixture(t, firstPath, []string{firstContext})
	writeFixture(t, secondPath, []string{secondContext})
	now := time.Now()
	if err := os.Chtimes(firstPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(secondPath, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	collector := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceCodex, Path: sourceDir, InitialBackfill: true},
	}, nil)
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(baseline) error = %v", err)
	}

	firstEvent := `{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`
	firstFile, err := os.OpenFile(firstPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstFile.WriteString(firstEvent + "\n"); err != nil {
		firstFile.Close()
		t.Fatal(err)
	}
	if err := firstFile.Close(); err != nil {
		t.Fatal(err)
	}
	secondFile, err := os.OpenFile(secondPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondFile.WriteString("{not-json}\n"); err != nil {
		secondFile.Close()
		t.Fatal(err)
	}
	if err := secondFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(malformed) error = %v", err)
	}

	secondEvent := `{"type":"event_msg","timestamp":"2026-05-25T12:01:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"output_tokens":20,"total_tokens":220}}}}`
	writeFixture(t, secondPath, []string{secondContext, secondEvent})
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(retry) error = %v", err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(unchanged) error = %v", err)
	}

	outPath := filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want both pending events exactly once: %s", len(lines), data)
	}
	if !strings.Contains(string(data), `"ts":"2026-05-25T12:00:01Z"`) || !strings.Contains(string(data), `"ts":"2026-05-25T12:01:01Z"`) {
		t.Fatalf("output missing pending event: %s", data)
	}
}

func TestCollectorReusesDiscoveredPathsWithinCacheWindow(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(sourceDir, "first.jsonl")
	writeFixture(t, firstPath, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: sourceDir, InitialBackfill: true},
	}, nil)
	if events, err := collector.collectSource(collector.sources[0]); err != nil || len(events) != 1 {
		t.Fatalf("collectSource(first) events/error = %d/%v, want 1/nil", len(events), err)
	}

	secondPath := filepath.Join(sourceDir, "second.jsonl")
	writeFixture(t, secondPath, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:01:00Z","payload":{"model":"gpt-5.2-codex","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:01:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"output_tokens":20,"total_tokens":220}}}}`,
	})

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(cached) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("cached events len = %d, want 0 before discovery refresh: %+v", len(events), events)
	}
}

func TestCollectorRefreshesDiscoveredPathsAfterCacheExpiry(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(sourceDir, "first.jsonl")
	writeFixture(t, firstPath, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: sourceDir, InitialBackfill: true},
	}, nil)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	collector.now = func() time.Time { return now }
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}

	secondPath := filepath.Join(sourceDir, "second.jsonl")
	writeFixture(t, secondPath, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:01:00Z","payload":{"model":"gpt-5.2-codex","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:01:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"output_tokens":20,"total_tokens":220}}}}`,
	})
	now = now.Add(sourcePathCacheTTL)

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(refreshed) error = %v", err)
	}
	if len(events) != 1 || events[0].SourcePath != secondPath {
		t.Fatalf("refreshed events = %+v, want event from %s", events, secondPath)
	}
}

func TestCollectorSkipsDeletedCachedSourcePath(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "session.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: sourceDir, InitialBackfill: true},
	}, nil)
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}

	events, err := collector.collectSource(collector.sources[0])
	if err != nil {
		t.Fatalf("collectSource(deleted cached path) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("deleted cached path events = %+v, want none", events)
	}
}

func TestCollectorRefreshRemovesMissingSourceState(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "session.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
	})
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: sourceDir, InitialBackfill: true},
	}, nil)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	collector.now = func() time.Time { return now }
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(first) error = %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	now = now.Add(sourcePathCacheTTL)

	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(refresh) error = %v", err)
	}
	if _, ok := collector.fileStates[source]; ok {
		t.Fatal("missing source remained in file state after discovery refresh")
	}
	if _, ok := collector.codexStates[source]; ok {
		t.Fatal("missing source remained in Codex parser state after discovery refresh")
	}
}

func TestCollectorInitialBackfillIncludesOldArchivedSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old-codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-04-04T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-04-04T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"total_tokens":110}}}}`,
	})
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}

	withoutBackfill := NewCollector(filepath.Join(dir, "without"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source},
	}, nil)
	if err := withoutBackfill.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() without backfill error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "without", "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("old non-backfill source wrote output, err=%v", err)
	}

	withBackfill := NewCollector(filepath.Join(dir, "with"), testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if err := withBackfill.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce() with backfill error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "with", "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ts":"2026-04-04T12:00:01Z"`) {
		t.Fatalf("backfill output missing archived event timestamp: %s", string(data))
	}
}

func TestCollectorPersistsSeenKeysWhenQueueFileIsRemoved(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old-codex.jsonl")
	writeFixture(t, source, []string{
		`{"type":"turn_context","timestamp":"2026-04-04T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-04-04T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"total_tokens":110}}}}`,
	})
	outDir := filepath.Join(dir, "out")

	first := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if err := first.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(first) error = %v", err)
	}
	outPath := filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.Remove(outPath); err != nil {
		t.Fatalf("Remove queue file: %v", err)
	}

	second := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceCodex, Path: source, InitialBackfill: true},
	}, nil)
	if err := second.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(second) error = %v", err)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected removed queue file to stay absent after seen-key reload, err=%v", err)
	}
}

func TestCollectorAntigravitySettingsEmitsDeltasAfterBaseline(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "session-a.settings.json")
	if err := os.WriteFile(source, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:00:00Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":100,"outputTokens":20,"totalTokens":120}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	collector := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceAntigravity, Path: source, Source: "antigravity", Provider: "gemini", InitialBackfill: true},
	}, nil)

	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce baseline error = %v", err)
	}
	outPath := filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected baseline not to emit output, err=%v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(source, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:01:00Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":140,"outputTokens":35,"totalTokens":175}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce delta error = %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1: %s", len(lines), string(data))
	}
	line := lines[0]
	if !strings.Contains(line, `"prompt_tokens":40`) || !strings.Contains(line, `"completion_tokens":15`) || !strings.Contains(line, `"total_tokens":55`) {
		t.Fatalf("expected Antigravity delta tokens, got: %s", line)
	}
}

func TestCollectorAntigravityRetriesWholeSourceAfterLaterFileFails(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(sourceDir, "first.settings.json")
	secondPath := filepath.Join(sourceDir, "second.settings.json")
	if err := os.WriteFile(firstPath, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:00:00Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":100,"outputTokens":20,"totalTokens":120}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:01:00Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":200,"outputTokens":30,"totalTokens":230}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(firstPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(secondPath, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	collector := NewCollector(outDir, testPricing(t), []Source{
		{Kind: SourceAntigravity, Path: sourceDir, Source: "antigravity", Provider: "gemini", InitialBackfill: true},
	}, nil)
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(baseline) error = %v", err)
	}

	if err := os.WriteFile(firstPath, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:00:01Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":150,"outputTokens":30,"totalTokens":180}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(`{not-json}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(malformed) error = %v", err)
	}

	if err := os.WriteFile(secondPath, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:01:01Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":250,"outputTokens":40,"totalTokens":290}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(retry) error = %v", err)
	}
	if err := collector.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce(unchanged) error = %v", err)
	}

	outPath := filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want both pending deltas exactly once: %s", len(lines), data)
	}
	if !strings.Contains(string(data), `"ts":"2026-05-25T15:00:01Z"`) || !strings.Contains(string(data), `"ts":"2026-05-25T15:01:01Z"`) {
		t.Fatalf("output missing pending Antigravity delta: %s", data)
	}
}

func TestCollectorAntigravityClearsStateForDeletedCachedPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "session.settings.json")
	if err := os.WriteFile(source, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:00:00Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":100,"outputTokens":20,"totalTokens":120}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewCollector(filepath.Join(dir, "out"), testPricing(t), []Source{
		{Kind: SourceAntigravity, Path: source, Source: "antigravity", Provider: "gemini", InitialBackfill: true},
	}, nil)
	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(baseline) error = %v", err)
	}
	if _, ok := collector.antigravity[source]; !ok {
		t.Fatal("baseline did not create Antigravity state")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}

	if _, err := collector.collectSource(collector.sources[0]); err != nil {
		t.Fatalf("collectSource(deleted) error = %v", err)
	}
	if _, ok := collector.antigravity[source]; ok {
		t.Fatal("deleted cached path retained stale Antigravity state")
	}
}
