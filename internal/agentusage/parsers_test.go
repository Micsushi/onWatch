package agentusage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPricing(t *testing.T) *PricingMap {
	t.Helper()
	pricing, err := NewPricingMapFromJSON([]byte(`{
		"claude-sonnet-4-5": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
			"cache_creation_1h_input_token_cost": 0.000006
		},
		"gpt-5.2-codex": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.00001,
			"cache_read_input_token_cost": 0.0000001
		},
		"gpt-5.5": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.00003,
			"cache_read_input_token_cost": 0.0000005
		},
		"google/gemini-2.5-pro": {
			"input_cost_per_token": 0.00000125,
			"output_cost_per_token": 0.00001,
			"cache_read_input_token_cost": 0.000000125
		},
		"gemini-2.5-flash": {
			"input_cost_per_token": 0.0000003,
			"output_cost_per_token": 0.0000025
		}
	}`))
	if err != nil {
		t.Fatalf("NewPricingMapFromJSON() error = %v", err)
	}
	return pricing
}

func TestParseClaudeUsageLine(t *testing.T) {
	line := []byte(`{"timestamp":"2026-05-25T12:34:56Z","sessionId":"s1","requestId":"req_1","message":{"id":"m1","model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":80}}}`)

	event, err := ParseClaudeUsageLine(line, `C:\Users\sushi\.claude\projects\p\s1.jsonl`, testPricing(t))
	if err != nil {
		t.Fatalf("ParseClaudeUsageLine() error = %v", err)
	}

	if event.Source != "claude" || event.Provider != "anthropic" {
		t.Fatalf("source/provider = %s/%s", event.Source, event.Provider)
	}
	if event.Model != "claude-sonnet-4-5" || event.SessionID != "s1" || event.RequestID != "req_1" {
		t.Fatalf("bad identity fields: %+v", event)
	}
	if event.InputTokens != 1000 || event.CacheCreationTokens != 20 || event.CachedInputTokens != 300 || event.OutputTokens != 80 || event.TotalTokens != 1400 {
		t.Fatalf("bad token fields: %+v", event)
	}
	if event.CostUSD <= 0 {
		t.Fatalf("expected cost > 0, got %v", event.CostUSD)
	}
}

func TestParseClaudeUsageLinePricesOneHourCacheCreation(t *testing.T) {
	line := []byte(`{"timestamp":"2026-05-25T12:34:56Z","sessionId":"s1","requestId":"req_1","message":{"id":"m1","model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":80,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":15}}}}`)

	event, err := ParseClaudeUsageLine(line, `C:\Users\sushi\.claude\projects\p\s1.jsonl`, testPricing(t))
	if err != nil {
		t.Fatalf("ParseClaudeUsageLine() error = %v", err)
	}

	if event.CacheCreationTokens != 20 || event.CacheCreation1hTokens != 15 {
		t.Fatalf("bad cache creation fields: %+v", event)
	}
	const want = 0.00439875
	if event.CostUSD != want {
		t.Fatalf("cost = %.8f, want %.8f", event.CostUSD, want)
	}
}

func TestParseCodexUsageFileSessionAndHeadlessLines(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rollout-test.jsonl")
	writeFixture(t, file, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"high","collaboration_mode":{"mode":"default","settings":{"reasoning_effort":"high"}},"fast_mode":true}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135}}}}`,
		`{"type":"turn.completed","timestamp":"2026-05-25T12:00:02Z","model":"gpt-5.2-codex","usage":{"input_tokens":50,"cached_input_tokens":5,"output_tokens":10,"total_tokens":60}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %+v", len(events), events)
	}
	if events[0].Source != "codex" || events[0].Provider != "openai" {
		t.Fatalf("source/provider = %s/%s", events[0].Source, events[0].Provider)
	}
	if events[0].InputTokens != 80 || events[0].CachedInputTokens != 20 || events[0].OutputTokens != 30 || events[0].ReasoningTokens != 5 {
		t.Fatalf("bad first event tokens: %+v", events[0])
	}
	if events[0].ReasoningEffort != "high" || events[0].Mode != "default" || events[0].FastMode == nil || !*events[0].FastMode {
		t.Fatalf("bad first event effort metadata: %+v", events[0])
	}
	if events[0].CostUSD != 0.003275 {
		t.Fatalf("first event cost = %.8f, want %.8f", events[0].CostUSD, 0.003275)
	}
	if events[1].InputTokens != 45 || events[1].CachedInputTokens != 5 || events[1].TotalTokens != 60 {
		t.Fatalf("bad second event tokens: %+v", events[1])
	}
}

func TestParseCodexUsageFileSkipsRepeatedTokenSnapshots(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rollout-repeat.jsonl")
	repeated := `{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135}}}}`
	writeFixture(t, file, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium","collaboration_mode":{"mode":"default","settings":{"reasoning_effort":"medium"}}}}`,
		repeated,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135}}}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:03Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"cached_input_tokens":100,"output_tokens":40,"reasoning_output_tokens":7,"total_tokens":290},"last_token_usage":{"input_tokens":150,"cached_input_tokens":80,"output_tokens":10,"reasoning_output_tokens":2,"total_tokens":155}}}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %+v", len(events), events)
	}
	if events[0].TotalTokens != 135 || events[1].TotalTokens != 155 {
		t.Fatalf("bad deduped events: %+v", events)
	}
}

func TestParseCodexUsageFileSkipsUsageBeforeModelContext(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rollout-no-context.jsonl")
	writeFixture(t, file, []string{
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"total_tokens":110}}}}`,
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:01Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"cached_input_tokens":30,"output_tokens":20,"total_tokens":220}}}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	if events[0].Model != "gpt-5.5" || events[0].TotalTokens != 220 {
		t.Fatalf("bad event after model context: %+v", events[0])
	}
}

func TestParseCodexUsageFileTracksModelAndEffortChangesPerTurn(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rollout-switch.jsonl")
	writeFixture(t, file, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"low","collaboration_mode":{"mode":"default","settings":{"reasoning_effort":"low"}}}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"total_tokens":110}}}}`,
		`{"type":"turn_context","timestamp":"2026-05-25T12:01:00Z","payload":{"model":"gpt-5.2-codex","effort":"high","collaboration_mode":{"mode":"default","settings":{"reasoning_effort":"high"}}}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:01:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":50,"cached_input_tokens":5,"output_tokens":10,"total_tokens":60}}}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %+v", len(events), events)
	}
	if events[0].Model != "gpt-5.5" || events[0].ReasoningEffort != "low" {
		t.Fatalf("first event context = %s/%s, want gpt-5.5/low", events[0].Model, events[0].ReasoningEffort)
	}
	if events[1].Model != "gpt-5.2-codex" || events[1].ReasoningEffort != "high" {
		t.Fatalf("second event context = %s/%s, want gpt-5.2-codex/high", events[1].Model, events[1].ReasoningEffort)
	}
}

func TestParseCodexUsageFileAnnotatesFastServiceTierFromGlobalState(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	sessionsDir := filepath.Join(codexDir, "sessions", "2026", "05", "25")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, ".codex-global-state.json"), []byte(`{"electron-persisted-atom-state":{"default-service-tier":"fast","has-user-changed-service-tier":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sessionsDir, "rollout-fast.jsonl")
	writeFixture(t, file, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135}}}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	if events[0].SpeedMode != "fast" || events[0].SpeedMultiplier != 2.5 || events[0].SpeedSource != "codex_global_state" {
		t.Fatalf("bad speed metadata: %+v", events[0])
	}
	if events[0].CostUSD != 0.003275 {
		t.Fatalf("cost = %.8f, want %.8f", events[0].CostUSD, 0.003275)
	}
}

func TestParseCodexUsageFileAnnotatesPriorityServiceTierFromConfig(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	sessionsDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("service_tier = 'priority'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sessionsDir, "rollout-priority.jsonl")
	writeFixture(t, file, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135}}}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	if events[0].SpeedMode != "fast" || events[0].SpeedMultiplier != 2.5 || events[0].SpeedSource != "codex_config" {
		t.Fatalf("bad speed metadata: %+v", events[0])
	}
	if events[0].CostUSD != 0.003275 {
		t.Fatalf("cost = %.8f, want %.8f", events[0].CostUSD, 0.003275)
	}
}

func TestParseCodexUsageFileDoesNotApplyGlobalSpeedToArchivedSession(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	archivedDir := filepath.Join(codexDir, "archived_sessions")
	if err := os.MkdirAll(archivedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, ".codex-global-state.json"), []byte(`{"electron-persisted-atom-state":{"default-service-tier":"fast"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(archivedDir, "rollout-archived.jsonl")
	writeFixture(t, file, []string{
		`{"type":"turn_context","timestamp":"2026-05-25T12:00:00Z","payload":{"model":"gpt-5.5","effort":"medium"}}`,
		`{"type":"event_msg","timestamp":"2026-05-25T12:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":135}}}}`,
	})

	events, err := ParseCodexUsageFile(file, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCodexUsageFile() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	if events[0].SpeedMode != "" || events[0].SpeedMultiplier != 0 || events[0].SpeedSource != "" {
		t.Fatalf("archived event should not inherit global speed: %+v", events[0])
	}
	if events[0].CostUSD != 0.00131 {
		t.Fatalf("cost = %.8f, want %.8f", events[0].CostUSD, 0.00131)
	}
}

func TestParseGeminiUsageFileSupportsJsonAndJsonlStats(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "session.json")
	if err := os.WriteFile(jsonFile, []byte(`{"sessionId":"g1","timestamp":"2026-05-25T13:00:00Z","model":"gemini-2.5-pro","stats":{"tokens":{"input":1000,"cached":100,"output":50,"thoughts":10,"total":1160}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonlFile := filepath.Join(dir, "transcript.jsonl")
	writeFixture(t, jsonlFile, []string{
		`{"session_id":"ag1","timestamp":"2026-05-25T13:01:00Z","model":"gemini-2.5-flash"}`,
		`{"type":"gemini","timestamp":"2026-05-25T13:01:01Z","tokens":{"input":200,"output":20,"total":220}}`,
	})

	jsonEvents, err := ParseGeminiUsageFile(jsonFile, "gemini", "gemini", testPricing(t))
	if err != nil {
		t.Fatalf("ParseGeminiUsageFile(json) error = %v", err)
	}
	jsonlEvents, err := ParseGeminiUsageFile(jsonlFile, "antigravity", "gemini", testPricing(t))
	if err != nil {
		t.Fatalf("ParseGeminiUsageFile(jsonl) error = %v", err)
	}

	if len(jsonEvents) != 1 || jsonEvents[0].InputTokens != 900 || jsonEvents[0].CachedInputTokens != 100 || jsonEvents[0].ReasoningTokens != 10 {
		t.Fatalf("bad json events: %+v", jsonEvents)
	}
	if len(jsonlEvents) != 1 || jsonlEvents[0].Source != "antigravity" || jsonlEvents[0].Model != "gemini-2.5-flash" {
		t.Fatalf("bad jsonl events: %+v", jsonlEvents)
	}
}

func TestParseAntigravitySettingsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-a.settings.json")
	if err := os.WriteFile(path, []byte(`{"providerLock":"google","providerLockTimestamp":"2026-05-25T15:00:00Z","model":"custom:Gemini-2.5-Pro-[Google]","tokenUsage":{"inputTokens":1000,"cacheReadTokens":100,"outputTokens":80,"thinkingTokens":20}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	event, err := ParseAntigravitySettingsFile(path, testPricing(t))
	if err != nil {
		t.Fatalf("ParseAntigravitySettingsFile() error = %v", err)
	}
	if event == nil {
		t.Fatal("expected event")
	}
	if event.Source != "antigravity" || event.Provider != "gemini" || event.SessionID != "session-a" {
		t.Fatalf("bad identity fields: %+v", event)
	}
	if event.Model != "gemini-2-5-pro" || event.InputTokens != 900 || event.CachedInputTokens != 100 || event.OutputTokens != 80 || event.ReasoningTokens != 20 {
		t.Fatalf("bad event: %+v", event)
	}
}

func TestParseCursorUsageCSV(t *testing.T) {
	csv := "Date,Kind,Model,User Email,Input Tokens,Cached Input Tokens,Output Tokens,Usage Based Price\n" +
		"2026-05-25T14:00:00Z,Usage-based,gpt-5.2-codex,sushi@example.com,1000,100,80,0.0042\n"

	events, err := ParseCursorUsageCSV([]byte(csv), "cursor-export.csv")
	if err != nil {
		t.Fatalf("ParseCursorUsageCSV() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	event := events[0]
	if event.Source != "cursor" || event.Provider != "cursor" || event.Account != "sushi@example.com" {
		t.Fatalf("bad identity fields: %+v", event)
	}
	if event.InputTokens != 900 || event.CachedInputTokens != 100 || event.OutputTokens != 80 || event.TotalTokens != 1080 {
		t.Fatalf("bad token fields: %+v", event)
	}
	if event.CostUSD != 0.0042 {
		t.Fatalf("cost = %v, want 0.0042", event.CostUSD)
	}
}

func TestCursorEventFromBubbleUsesTokenCountAndComposerModel(t *testing.T) {
	bubble := []byte(`{
		"type":2,
		"createdAt":"2026-05-25T14:00:00Z",
		"bubbleId":"bubble-a",
		"requestId":"req-a",
		"unifiedMode":2,
		"tokenCount":{"inputTokens":1200,"cachedInputTokens":200,"outputTokens":80}
	}`)
	models := map[string]string{"composer-a": "claude-4-6-sonnet-medium"}

	event, ok := cursorEventFromBubble("bubbleId:composer-a:bubble-a", bubble, models, "state.vscdb", testPricing(t))
	if !ok {
		t.Fatal("expected event")
	}
	if event.Source != "cursor" || event.Provider != "cursor" || event.SessionID != "composer-a" {
		t.Fatalf("bad identity fields: %+v", event)
	}
	if event.Model != "claude-4-6-sonnet-medium" || event.Mode != "agent" || event.RequestID != "req-a" {
		t.Fatalf("bad model/mode fields: %+v", event)
	}
	if event.InputTokens != 1200 || event.CachedInputTokens != 200 || event.OutputTokens != 80 || event.TotalTokens != 1480 {
		t.Fatalf("bad token fields: %+v", event)
	}
}

func TestCursorEventFromBubbleSkipsZeroTokenCounts(t *testing.T) {
	bubble := []byte(`{"createdAt":"2026-05-25T14:00:00Z","tokenCount":{"inputTokens":0,"outputTokens":0}}`)

	_, ok := cursorEventFromBubble("bubbleId:composer-a:bubble-a", bubble, nil, "state.vscdb", testPricing(t))
	if ok {
		t.Fatal("expected zero-token Cursor bubble to be skipped")
	}
}

func TestParseCursorStateDBSkipsNullValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES ('composerData:composer-a', NULL), ('bubbleId:composer-a:bubble-a', NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := ParseCursorStateDB(path, testPricing(t))
	if err != nil {
		t.Fatalf("ParseCursorStateDB() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events len = %d, want 0: %+v", len(events), events)
	}
}

func TestEventToAPIIntegrationLineRoundTripsThroughExistingParser(t *testing.T) {
	event := UsageEvent{
		Timestamp:         time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC),
		Source:            "codex",
		Provider:          "openai",
		SessionID:         "s1",
		Model:             "gpt-5.2-codex",
		InputTokens:       100,
		CachedInputTokens: 20,
		OutputTokens:      30,
		TotalTokens:       150,
		CostUSD:           0.123,
		SourcePath:        "sessions/s1.jsonl",
	}

	line, err := event.ToAPIIntegrationLine()
	if err != nil {
		t.Fatalf("ToAPIIntegrationLine() error = %v", err)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		t.Fatalf("line should end in newline: %q", string(line))
	}
}

func writeFixture(t *testing.T, path string, lines []string) {
	t.Helper()
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
