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
