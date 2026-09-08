package agentusage

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)

func TestParseAntigravityHistoryDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.db")
	wantTime := time.Unix(1788739200, 123456789).UTC()
	writeAntigravityHistoryDB(t, path, historyMetadataFixture(wantTime, 40, 10, 30))

	events, err := ParseAntigravityHistoryDB(path, testPricing(t))
	if err != nil {
		t.Fatalf("ParseAntigravityHistoryDB() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	event := events[0]
	if !event.Timestamp.Equal(wantTime) || event.SessionID != "conversation" || event.RequestID != "gen_metadata:7" {
		t.Fatalf("bad identity fields: %+v", event)
	}
	if event.Provider != "gemini" || event.Model != "gemini-3-8-flash-tiered" {
		t.Fatalf("bad provider/model: %+v", event)
	}
	if event.InputTokens != 70 || event.CachedInputTokens != 30 || event.CacheCreationTokens != 5 || event.OutputTokens != 30 || event.ReasoningTokens != 10 || event.TotalTokens != 145 {
		t.Fatalf("bad token counts: %+v", event)
	}
	if event.CostUSD != 0 {
		t.Fatalf("unknown model cost = %v, want 0", event.CostUSD)
	}
	line, err := event.ToAPIIntegrationLine()
	if err != nil {
		t.Fatalf("ToAPIIntegrationLine() error = %v", err)
	}
	if strings.Contains(string(line), "synthetic-secret") {
		t.Fatal("history payload leaked into normalized output")
	}
	if !strings.Contains(string(line), `"prompt_tokens":105`) || !strings.Contains(string(line), `"completion_tokens":30`) || !strings.Contains(string(line), `"total_tokens":145`) {
		t.Fatalf("normalized counts missing: %s", line)
	}
}

func TestAntigravityHistoryCountsDerivesResponseWhenAbsent(t *testing.T) {
	data := historyVarint(2, 100)
	data = historyAppendVarint(data, 3, 40)
	data = historyAppendVarint(data, 5, 30)
	data = historyAppendVarint(data, 9, 10)

	counts, outputTotal, ok := antigravityHistoryCounts(data)
	if !ok {
		t.Fatal("expected supported output relation")
	}
	if outputTotal != 40 || counts.OutputTokens != 30 || counts.ReasoningTokens != 10 || counts.TotalTokens != 140 {
		t.Fatalf("counts = %+v, output total = %d", counts, outputTotal)
	}
}

func TestAntigravityHistoryRejectsIncompleteBoundedImports(t *testing.T) {
	for _, scenario := range []string{"oversized blob", "too many rows"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "conversation.db")
			writeAntigravityHistoryDB(t, path, historyMetadataFixture(time.Unix(1788739200, 0), 40, 10, 30))
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if scenario == "oversized blob" {
				// Do not trust the stored size field; bound actual SQLite blob length
				// before Scan allocates the payload in Go.
				_, err = db.Exec(`INSERT INTO gen_metadata VALUES(8, zeroblob(?), 1)`, antigravityHistoryMaxBlobBytes+1)
			} else {
				_, err = db.Exec(`WITH RECURSIVE indices(x) AS (SELECT 100 UNION ALL SELECT x+1 FROM indices WHERE x < ?) INSERT INTO gen_metadata SELECT x, data, size FROM indices CROSS JOIN gen_metadata WHERE idx=7`, antigravityHistoryMaxRows+99)
			}
			if err != nil {
				t.Fatal(err)
			}
			events, err := ParseAntigravityHistoryDB(path, testPricing(t))
			if err == nil || len(events) != 0 {
				t.Fatalf("incomplete import returned %d events, err=%v", len(events), err)
			}
		})
	}
}

func TestAntigravityHistoryCountsSkipsUnsupportedOutputRelation(t *testing.T) {
	data := historyVarint(3, 40)
	data = historyAppendVarint(data, 9, 10)
	data = historyAppendVarint(data, 10, 25)
	if _, _, ok := antigravityHistoryCounts(data); ok {
		t.Fatal("expected unsupported response/thinking relation to be skipped")
	}
}

func TestExpandAntigravityHistoryPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "first.db"),
		filepath.Join(dir, "nested", "second.db"),
		filepath.Join(dir, "ignored.sqlite"),
	} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := expandSourcePaths(dir, SourceAntigravityHistory)
	if err != nil {
		t.Fatalf("expandSourcePaths() error = %v", err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], ".db") || !strings.HasSuffix(paths[1], ".db") {
		t.Fatalf("paths = %v, want two db files", paths)
	}
}

func TestAntigravityHistoryCollectorReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "conversation.db")
	writeAntigravityHistoryDB(t, dbPath, historyMetadataFixture(time.Unix(1788739200, 0).UTC(), 40, 10, 30))
	outDir := filepath.Join(dir, "out")
	source := Source{Kind: SourceAntigravityHistory, Path: dir, Source: "antigravity", Provider: "gemini", InitialBackfill: true}

	first := NewCollector(outDir, testPricing(t), []Source{source}, nil)
	if err := first.CollectOnce(); err != nil {
		t.Fatalf("first CollectOnce() error = %v", err)
	}
	second := NewCollector(outDir, testPricing(t), []Source{source}, nil)
	if err := second.CollectOnce(); err != nil {
		t.Fatalf("replay CollectOnce() error = %v", err)
	}

	files, err := filepath.Glob(filepath.Join(outDir, "agent-usage-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("output files = %v, err = %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 1 {
		t.Fatalf("replay wrote %d lines, want 1: %s", lines, data)
	}
}

func TestDefaultSourcesIncludesAntigravityHistory(t *testing.T) {
	home := t.TempDir()
	conversationDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	if err := os.MkdirAll(conversationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DROID_SESSIONS_DIR", "")
	t.Setenv("GEMINI_DATA_DIR", "")
	sources := DefaultSources(home)
	for _, source := range sources {
		if source.Kind == SourceAntigravityHistory && filepath.Clean(source.Path) == filepath.Clean(conversationDir) {
			return
		}
	}
	t.Fatalf("sources = %+v, want Antigravity history path", sources)
}

func writeAntigravityHistoryDB(t *testing.T, path string, data []byte) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB NOT NULL, size INTEGER NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)`, 7, data, len(data)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func historyMetadataFixture(at time.Time, output, thinking, response uint64) []byte {
	timestamp := historyVarint(1, uint64(at.Unix()))
	timestamp = historyAppendVarint(timestamp, 2, uint64(at.Nanosecond()))
	start := historyBytes(4, timestamp)
	usage := historyVarint(1, 24)
	usage = historyAppendVarint(usage, 2, 100)
	usage = historyAppendVarint(usage, 3, output)
	usage = historyAppendVarint(usage, 4, 5)
	usage = historyAppendVarint(usage, 5, 30)
	usage = historyAppendVarint(usage, 6, 24)
	usage = historyAppendVarint(usage, 7, 123)
	usage = historyAppendVarint(usage, 9, thinking)
	usage = historyAppendVarint(usage, 10, response)
	usage = historyAppendBytes(usage, 8, []byte("synthetic-secret"))
	usage = historyAppendBytes(usage, 11, []byte("synthetic-id"))
	chat := historyBytes(4, usage)
	chat = historyAppendBytes(chat, 9, start)
	chat = historyAppendBytes(chat, 19, []byte("stale-model"))
	chat = historyAppendBytes(chat, 21, []byte("Display Name"))
	chat = historyAppendBytes(chat, 22, []byte("gemini-3.8-flash-tiered"))
	return historyAppendBytes(historyBytes(8, []byte("ignored-top-level")), 1, chat)
}

func historyVarint(number protowire.Number, value uint64) []byte {
	return historyAppendVarint(nil, number, value)
}

func historyAppendVarint(data []byte, number protowire.Number, value uint64) []byte {
	data = protowire.AppendTag(data, number, protowire.VarintType)
	return protowire.AppendVarint(data, value)
}

func historyBytes(number protowire.Number, value []byte) []byte {
	return historyAppendBytes(nil, number, value)
}

func historyAppendBytes(data []byte, number protowire.Number, value []byte) []byte {
	data = protowire.AppendTag(data, number, protowire.BytesType)
	return protowire.AppendBytes(data, value)
}
