package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func insertAPIIntegrationEventForTest(t *testing.T, s *store.Store, line, sourcePath string) {
	t.Helper()
	event, err := apiintegrations.ParseUsageEventLine([]byte(line), sourcePath)
	if err != nil {
		t.Fatalf("ParseUsageEventLine: %v", err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(event); err != nil {
		t.Fatalf("InsertAPIIntegrationUsageEvent: %v", err)
	}
}

func TestAPIIntegrationResponseCacheKeepsStalePayload(t *testing.T) {
	t.Parallel()

	now := time.Now()
	payload := map[string]interface{}{"saved": true}
	var cache apiIntegrationResponseCache
	cache.set("current", 1, now, payload)

	if _, ok := cache.get("current", 2, now); ok {
		t.Fatal("version-mismatched payload reported as fresh")
	}
	stale, ok := cache.getStale("current")
	if !ok {
		t.Fatal("version-mismatched payload was discarded instead of remaining available as stale data")
	}
	if stale["saved"] != true {
		t.Fatalf("stale payload=%v want saved payload", stale)
	}
}

func TestAPIIntegrationsCurrentUsesOneAggregateQuery(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("api_integrations_handlers.go")
	if err != nil {
		t.Fatalf("read API integrations handler: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (h *Handler) buildAPIIntegrationsCurrent()")
	end := strings.Index(text[start:], "\n// APIIntegrationsHistory")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate buildAPIIntegrationsCurrent")
	}
	currentBuilder := text[start : start+end]
	if strings.Contains(currentBuilder, "QueryAPIIntegrationUsageSummary()") {
		t.Fatal("current summary still performs a separate full-table summary query")
	}
	if !strings.Contains(currentBuilder, "QueryAPIIntegrationUsageEffortSummary()") {
		t.Fatal("current summary must retain the detailed aggregate query")
	}
}

func TestHandler_APIIntegrationsCurrent_Empty(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/current", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsCurrent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(response) != 0 {
		t.Fatalf("response=%v want empty object", response)
	}
}

func TestHandler_APIIntegrationsCurrent_GroupedTotals(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"anthropic","account":"personal","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5,"cost_usd":0.1}`, "/tmp/api-integrations/notes.jsonl")
	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:02:00Z","integration":"notes","provider":"anthropic","account":"personal","model":"claude-3-7-haiku","prompt_tokens":4,"completion_tokens":1,"cost_usd":0.05}`, "/tmp/api-integrations/notes.jsonl")
	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:04:00Z","integration":"notes","provider":"mistral","account":"team","model":"mistral-small-latest","prompt_tokens":6,"completion_tokens":2}`, "/tmp/api-integrations/notes.jsonl")
	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:05:00Z","integration":"report","provider":"openai","model":"gpt-4.1-mini","prompt_tokens":3,"completion_tokens":2}`, "/tmp/api-integrations/report.jsonl")

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/current", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsCurrent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}

	var response map[string]struct {
		Integration      string  `json:"integration"`
		RequestCount     int     `json:"requestCount"`
		PromptTokens     int     `json:"promptTokens"`
		CompletionTokens int     `json:"completionTokens"`
		TotalTokens      int     `json:"totalTokens"`
		TotalCostUSD     float64 `json:"totalCostUsd"`
		LastCapturedAt   string  `json:"lastCapturedAt"`
		Providers        []struct {
			Provider         string  `json:"provider"`
			RequestCount     int     `json:"requestCount"`
			PromptTokens     int     `json:"promptTokens"`
			CompletionTokens int     `json:"completionTokens"`
			TotalTokens      int     `json:"totalTokens"`
			TotalCostUSD     float64 `json:"totalCostUsd"`
			LastCapturedAt   string  `json:"lastCapturedAt"`
			Accounts         []struct {
				Account          string `json:"account"`
				RequestCount     int    `json:"requestCount"`
				PromptTokens     int    `json:"promptTokens"`
				CompletionTokens int    `json:"completionTokens"`
				TotalTokens      int    `json:"totalTokens"`
				Models           []struct {
					Model            string `json:"model"`
					RequestCount     int    `json:"requestCount"`
					PromptTokens     int    `json:"promptTokens"`
					CompletionTokens int    `json:"completionTokens"`
					TotalTokens      int    `json:"totalTokens"`
				} `json:"models"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	notes := response["notes"]
	if notes.Integration != "notes" || notes.RequestCount != 3 || notes.TotalTokens != 28 {
		t.Fatalf("notes=%+v", notes)
	}
	if notes.LastCapturedAt != "2026-04-03T12:04:00Z" {
		t.Fatalf("notes lastCapturedAt=%q", notes.LastCapturedAt)
	}
	if len(notes.Providers) != 2 || notes.Providers[0].Provider != "anthropic" || notes.Providers[1].Provider != "mistral" {
		t.Fatalf("notes providers=%+v", notes.Providers)
	}
	if notes.Providers[0].Accounts[0].Account != "personal" || len(notes.Providers[0].Accounts[0].Models) != 2 {
		t.Fatalf("notes anthropic account breakdown=%+v", notes.Providers[0].Accounts)
	}
	if notes.Providers[0].Accounts[0].Models[0].Model != "claude-3-7-haiku" || notes.Providers[0].Accounts[0].Models[1].Model != "claude-3-7-sonnet" {
		t.Fatalf("notes anthropic models=%+v", notes.Providers[0].Accounts[0].Models)
	}

	report := response["report"]
	if report.Integration != "report" || report.RequestCount != 1 || report.TotalTokens != 5 {
		t.Fatalf("report=%+v", report)
	}
	if len(report.Providers) != 1 || report.Providers[0].Accounts[0].Account != "default" {
		t.Fatalf("report providers=%+v", report.Providers)
	}
}

func TestHandler_APIIntegrationsCurrent_ServesStaleWhileRefreshing(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"anthropic","model":"claude","prompt_tokens":10,"completion_tokens":5}`, "/tmp/api-integrations/notes.jsonl")
	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/current", nil)
	first := httptest.NewRecorder()
	h.APIIntegrationsCurrent(first, req)

	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:01:00Z","integration":"notes","provider":"anthropic","model":"claude","prompt_tokens":4,"completion_tokens":2}`, "/tmp/api-integrations/notes.jsonl")
	stale := httptest.NewRecorder()
	h.APIIntegrationsCurrent(stale, req)

	if got := stale.Header().Get("X-OnWatch-Data-State"); got != "stale" {
		t.Fatalf("data state=%q want stale", got)
	}
	var staleResponse map[string]struct {
		RequestCount int `json:"requestCount"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &staleResponse); err != nil {
		t.Fatalf("json.Unmarshal stale response: %v", err)
	}
	if got := staleResponse["notes"].RequestCount; got != 1 {
		t.Fatalf("stale requestCount=%d want 1", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		current := httptest.NewRecorder()
		h.APIIntegrationsCurrent(current, req)
		if current.Header().Get("X-OnWatch-Data-State") == "fresh" {
			var response map[string]struct {
				RequestCount int `json:"requestCount"`
			}
			if err := json.Unmarshal(current.Body.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal fresh response: %v", err)
			}
			if got := response["notes"].RequestCount; got != 2 {
				t.Fatalf("fresh requestCount=%d want 2", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for refreshed API integration summary")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandler_APIIntegrationsCurrent_IncludesEffortBreakdown(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":10,"completion_tokens":5,"metadata":{"reasoning_effort":"high","mode":"default","fast_mode":true}}`, "/tmp/codex.jsonl")
	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:01:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":4,"completion_tokens":2,"metadata":{"reasoning_effort":"medium","mode":"default","fast_mode":false}}`, "/tmp/codex.jsonl")

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/current", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsCurrent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response map[string]struct {
		Providers []struct {
			Accounts []struct {
				Models []struct {
					Model   string `json:"model"`
					Efforts []struct {
						ReasoningEffort string `json:"reasoningEffort"`
						Mode            string `json:"mode"`
						SpeedMode       string `json:"speedMode"`
						RequestCount    int    `json:"requestCount"`
						TotalTokens     int    `json:"totalTokens"`
					} `json:"efforts"`
				} `json:"models"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	model := response["Codex CLI"].Providers[0].Accounts[0].Models[0]
	if model.Model != "gpt-5.5" || len(model.Efforts) != 2 {
		t.Fatalf("model effort breakdown=%+v", model)
	}
	if model.Efforts[0].ReasoningEffort != "high" || model.Efforts[0].SpeedMode != "fast" {
		t.Fatalf("first effort=%+v", model.Efforts[0])
	}
	if model.Efforts[1].ReasoningEffort != "medium" || model.Efforts[1].SpeedMode != "standard" {
		t.Fatalf("second effort=%+v", model.Efforts[1])
	}
}

func TestHandler_APIIntegrationsHistory_RangeAndDownsample(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	for i := 0; i < 520; i++ {
		line := `{"ts":"` + base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339) + `","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":1,"completion_tokens":1}`
		insertAPIIntegrationEventForTest(t, s, line, "/tmp/api-integrations/notes.jsonl")
	}

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/history?range=30d", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsHistory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}

	var response map[string][]struct {
		CapturedAt       string  `json:"capturedAt"`
		RequestCount     int     `json:"requestCount"`
		PromptTokens     int     `json:"promptTokens"`
		CompletionTokens int     `json:"completionTokens"`
		TotalTokens      int     `json:"totalTokens"`
		TotalCostUSD     float64 `json:"totalCostUsd"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	buckets := response["notes"]
	if len(buckets) == 0 {
		t.Fatal("expected history buckets for notes")
	}
	if len(buckets) > maxChartPoints {
		t.Fatalf("len(buckets)=%d exceeds maxChartPoints=%d", len(buckets), maxChartPoints)
	}
	if buckets[0].RequestCount < 1 || buckets[0].TotalTokens < 2 {
		t.Fatalf("unexpected first bucket: %+v", buckets[0])
	}
}

func TestHandler_APIIntegrationsSessions_IncludesUnsampledTotals(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		line := `{"ts":"` + base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339) + `","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":10,"completion_tokens":5,"cost_usd":0.25,"metadata":{"session_id":"chat-a"}}`
		insertAPIIntegrationEventForTest(t, s, line, "/tmp/api-integrations/codex-a.jsonl")
	}
	insertAPIIntegrationEventForTest(t, s, `{"ts":"`+base.Add(10*time.Minute).Format(time.RFC3339)+`","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":20,"completion_tokens":10,"cost_usd":0.5,"metadata":{"session_id":"chat-b"}}`, "/tmp/api-integrations/codex-b.jsonl")
	insertAPIIntegrationEventForTest(t, s, `{"ts":"`+base.Add(10*time.Minute).Format(time.RFC3339)+`","integration":"Claude Code","provider":"anthropic","model":"claude-sonnet-4-6","prompt_tokens":100,"completion_tokens":50,"cost_usd":1.5}`, "/tmp/api-integrations/claude.jsonl")

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	windowStart := base.Add(-time.Hour).Format(time.RFC3339)
	windowEnd := base.Add(time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/sessions?integration=Codex%20CLI&start="+windowStart+"&end="+windowEnd, nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response struct {
		Totals map[string]struct {
			RequestCount int     `json:"requestCount"`
			TotalTokens  int     `json:"totalTokens"`
			TotalCostUSD float64 `json:"totalCostUsd"`
		} `json:"_totals"`
		Models map[string][]struct {
			Model        string  `json:"model"`
			RequestCount int     `json:"requestCount"`
			TotalTokens  int     `json:"totalTokens"`
			TotalCostUSD float64 `json:"totalCostUsd"`
		} `json:"_models"`
		Codex []struct {
			SessionID             string  `json:"sessionId"`
			RequestCount          int     `json:"requestCount"`
			TotalTokens           int     `json:"totalTokens"`
			CumulativeCostUSD     float64 `json:"cumulativeCostUsd"`
			CumulativeTotalTokens int     `json:"cumulativeTotalTokens"`
		} `json:"Codex CLI"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	total := response.Totals["Codex CLI"]
	if total.RequestCount != 4 || total.TotalTokens != 75 || total.TotalCostUSD != 1.25 {
		t.Fatalf("totals=%+v", total)
	}
	if len(response.Codex) != 2 {
		t.Fatalf("codex sessions len=%d want 2: %+v", len(response.Codex), response.Codex)
	}
	last := response.Codex[len(response.Codex)-1]
	if last.CumulativeTotalTokens != total.TotalTokens || last.CumulativeCostUSD != total.TotalCostUSD {
		t.Fatalf("last cumulative=%+v totals=%+v", last, total)
	}
	models := response.Models["Codex CLI"]
	if len(models) != 1 || models[0].Model != "gpt-5.5" || models[0].RequestCount != total.RequestCount || models[0].TotalTokens != total.TotalTokens {
		t.Fatalf("models=%+v totals=%+v", models, total)
	}
}

func TestHandler_APIIntegrationsSessions_CanSkipSessionGrouping(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	for i := 0; i < 3; i++ {
		line := `{"ts":"` + base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339) + `","integration":"Codex CLI","provider":"openai","model":"gpt-5.5","prompt_tokens":10,"completion_tokens":5,"cost_usd":0.25,"metadata":{"session_id":"chat-a"}}`
		insertAPIIntegrationEventForTest(t, s, line, "/tmp/api-integrations/codex-a.jsonl")
	}

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/sessions?range=24h&integration=Codex%20CLI&includeSessions=false", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := response["Codex CLI"]; ok {
		t.Fatal("session rows were returned even though includeSessions=false")
	}
	if len(response["_totals"]) == 0 || len(response["_models"]) == 0 {
		t.Fatalf("summary data missing: %s", rr.Body.String())
	}
}

func TestHandler_APIIntegrationsHistory_InvalidRange(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/history?range=2h", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsHistory(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestHandler_APIIntegrationsHistory_CustomRangeReportsHourlyArchive(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationEventForTest(t, s,
		`{"ts":"2026-01-15T12:05:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.6-sol","prompt_tokens":100,"completion_tokens":20,"cost_usd":0.25}`,
		"/tmp/api-integrations/archive.jsonl",
	)
	if _, err := s.CompactAPIIntegrationUsageEvents(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("CompactAPIIntegrationUsageEvents: %v", err)
	}

	h := NewHandler(s, nil, nil, nil, &config.Config{
		APIIntegrationsEnabled:   true,
		APIIntegrationsDir:       "/tmp/api-integrations",
		APIIntegrationsRetention: 30 * 24 * time.Hour,
	})
	req := httptest.NewRequest(http.MethodGet,
		"/api/api-integrations/history?range=custom&start=2026-01-15T00:00:00Z&end=2026-01-16T00:00:00Z",
		nil,
	)
	rr := httptest.NewRecorder()

	h.APIIntegrationsHistory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-OnWatch-Archived-Data") != "true" ||
		rr.Header().Get("X-OnWatch-Archive-Resolution") != "1h" ||
		rr.Header().Get("X-OnWatch-Raw-Retention-Days") != "30" {
		t.Fatalf("archive headers=%v", rr.Header())
	}
	var response map[string][]struct {
		RequestCount int `json:"requestCount"`
		TotalTokens  int `json:"totalTokens"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	codex := response["Codex CLI"]
	if len(codex) != 1 || codex[0].RequestCount != 1 || codex[0].TotalTokens != 120 {
		t.Fatalf("Codex history=%+v", codex)
	}
}

func TestHandler_APIIntegrationsHealth_StatusFilesAndAlerts(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.UpsertAPIIntegrationIngestState(&apiintegrations.IngestState{
		SourcePath:  "/tmp/api-integrations/notes.jsonl",
		Offset:      200,
		FileSize:    256,
		FileModTime: time.Date(2026, 4, 3, 12, 10, 0, 0, time.UTC),
		PartialLine: `{"ts":"2026`,
	}); err != nil {
		t.Fatalf("UpsertAPIIntegrationIngestState: %v", err)
	}
	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:09:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5}`, "/tmp/api-integrations/notes.jsonl")
	if _, err := s.CreateSystemAlert("api_integrations", "ingest_warning", "Malformed line", "Skipped one malformed event", "warning", `{"sourcePath":"/tmp/api-integrations/notes.jsonl"}`); err != nil {
		t.Fatalf("CreateSystemAlert: %v", err)
	}

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	h.agentManager = &mockProviderAgentController{running: map[string]bool{"api_integrations": true}}
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/health", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsHealth(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}

	var response struct {
		Enabled bool   `json:"enabled"`
		Dir     string `json:"dir"`
		Running bool   `json:"running"`
		Files   []struct {
			SourcePath     string `json:"sourcePath"`
			OffsetBytes    int64  `json:"offsetBytes"`
			FileSize       int64  `json:"fileSize"`
			PartialLine    string `json:"partialLine"`
			FileModTime    string `json:"fileModTime"`
			UpdatedAt      string `json:"updatedAt"`
			LastCapturedAt string `json:"lastCapturedAt"`
		} `json:"files"`
		Alerts []struct {
			ID        int64  `json:"id"`
			Type      string `json:"type"`
			Title     string `json:"title"`
			Message   string `json:"message"`
			Severity  string `json:"severity"`
			CreatedAt string `json:"createdAt"`
			Metadata  string `json:"metadata"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if !response.Enabled || !response.Running || response.Dir != "/tmp/api-integrations" {
		t.Fatalf("unexpected health response: %+v", response)
	}
	if len(response.Files) != 1 || response.Files[0].SourcePath != "/tmp/api-integrations/notes.jsonl" || response.Files[0].LastCapturedAt != "2026-04-03T12:09:00Z" {
		t.Fatalf("unexpected files payload: %+v", response.Files)
	}
	if len(response.Alerts) != 1 || response.Alerts[0].Type != "ingest_warning" {
		t.Fatalf("unexpected alerts payload: %+v", response.Alerts)
	}
}

func TestHandler_APIIntegrationsHealth_Disabled(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: false, APIIntegrationsDir: ""})
	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/health", nil)
	rr := httptest.NewRecorder()

	h.APIIntegrationsHealth(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response struct {
		Enabled bool          `json:"enabled"`
		Dir     string        `json:"dir"`
		Running bool          `json:"running"`
		Files   []interface{} `json:"files"`
		Alerts  []interface{} `json:"alerts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if response.Enabled || response.Running || len(response.Files) != 0 || len(response.Alerts) != 0 {
		t.Fatalf("unexpected disabled response: %+v", response)
	}
}

func TestHandler_Current_DoesNotIncludeAPIIntegrationsTelemetry(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5}`, "/tmp/api-integrations/notes.jsonl")

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/current", nil)
	rr := httptest.NewRecorder()

	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := response["notes"]; ok {
		t.Fatalf("unexpected API integrations telemetry in /api/current: %v", response)
	}
}

func TestServer_APIIntegrationsRoute_UsesAuthMiddleware(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	insertAPIIntegrationEventForTest(t, s, `{"ts":"2026-04-03T12:00:00Z","integration":"notes","provider":"anthropic","model":"claude-3-7-sonnet","prompt_tokens":10,"completion_tokens":5}`, "/tmp/api-integrations/notes.jsonl")

	h := NewHandler(s, nil, nil, nil, &config.Config{APIIntegrationsEnabled: true, APIIntegrationsDir: "/tmp/api-integrations"})
	passHash, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	server := NewServer(0, h, nil, "admin", passHash, "127.0.0.1", "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/api-integrations/current", nil)
	req.SetBasicAuth("admin", "secret123")
	rr := httptest.NewRecorder()

	server.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := response["notes"]; !ok {
		t.Fatalf("expected notes payload, got %v", response)
	}
}
