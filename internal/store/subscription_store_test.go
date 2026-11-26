package store

import (
	"bytes"
	"context"
	"fmt"
	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
	"github.com/onllm-dev/onwatch/v2/internal/api"
	ai "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/subscription"
	"testing"
	"time"
)

func TestSubscriptionTelemetrySurvivesReplayAndRepricesMissingCost(t *testing.T) {
	s, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC()
	reset := at.Add(7 * 24 * time.Hour)
	_, e = s.InsertCodexSnapshot(&api.CodexSnapshot{AccountID: 1, CapturedAt: at, PlanType: "pro"})
	if e != nil {
		t.Fatal(e)
	}
	event := agentusage.UsageEvent{Source: "codex", Provider: "openai", Timestamp: at, Model: "gpt-6-astra", InputTokens: 1000000, TotalTokens: 1000000, QuotaTelemetry: map[string]any{"plan": "pro", "seven_day": map[string]any{"used": 0, "reset": reset.Unix()}}}
	line, e := event.ToAPIIntegrationLine()
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := ai.ParseUsageEventLine(line, "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.InsertAPIIntegrationUsageEvent(parsed); e != nil {
		t.Fatal(e)
	}
	_, _ = s.InsertAPIIntegrationUsageEvent(parsed)
	events, meters, e := s.SubscriptionInputs(context.Background(), "codex", "default", 1, at.Add(-time.Minute), at.Add(time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if len(events) != 1 || events[0].Cost != 10 || !events[0].Priced || len(meters) != 1 || meters[0].Used != 0 {
		t.Fatalf("events %+v meters %+v", events, meters)
	}
}

func TestSubscriptionTelemetrySamplesUnchangedRoundedCounters(t *testing.T) {
	s := newTransferTestStore(t)
	at := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	reset := at.Add(7 * 24 * time.Hour)
	for i, used := range []float64{10, 10, 11, 11} {
		event := agentusage.UsageEvent{
			Source: "codex", Provider: "openai", Timestamp: at.Add(time.Duration(i) * time.Minute),
			RequestID: fmt.Sprintf("request-%d", i), Model: "gpt-6-astra", TotalTokens: 1,
			QuotaTelemetry: map[string]any{"plan": "pro", "seven_day": map[string]any{"used": used, "reset": reset.Unix()}},
		}
		line, err := event.ToAPIIntegrationLine()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ai.ParseUsageEventLine(line, "test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.InsertAPIIntegrationUsageEvent(parsed); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM subscription_meter_observations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("unchanged rounded counters stored %d rows; want 2 changed samples", count)
	}
}

func TestSubscriptionMetersTransferAndReplay(t *testing.T) {
	src := newTransferTestStore(t)
	dst := newTransferTestStore(t)
	at := time.Now().UTC()
	if err := src.InsertSubscriptionMeter("codex", "default", subscription.Meter{At: at, Reset: at.Add(7 * 24 * time.Hour), Used: 0, Quota: "seven_day", Plan: "pro"}); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := src.ExportData(&archive, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := dst.ImportData(bytes.NewReader(archive.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := dst.db.QueryRow("SELECT COUNT(*) FROM subscription_meter_observations").Scan(&n); err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestSubscriptionMeterImportOverlapsLiveTelemetry(t *testing.T) {
	src := newTransferTestStore(t)
	dst := newTransferTestStore(t)
	at := time.Now().UTC()
	meter := subscription.Meter{At: at, Reset: at.Add(7 * 24 * time.Hour), Used: 10, Quota: "seven_day", Plan: "pro"}
	if err := dst.InsertSubscriptionMeter("codex", "default", meter); err != nil {
		t.Fatal(err)
	}
	meter.Used = 20
	for _, account := range []string{"default", "work"} {
		if err := src.InsertSubscriptionMeter("codex", account, meter); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	if _, err := src.ExportData(&archive, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := dst.ImportData(bytes.NewReader(archive.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var used float64
	if err := dst.db.QueryRow("SELECT COUNT(*) FROM subscription_meter_observations").Scan(&count); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := dst.db.QueryRow("SELECT utilization FROM subscription_meter_observations WHERE account_name='default'").Scan(&used); err != nil || used != 10 {
		t.Fatalf("central reading=%v err=%v", used, err)
	}
}

func TestSubscriptionRetainsHistoricalPlansWithoutPollSnapshot(t *testing.T) {
	s := newTransferTestStore(t)
	at := time.Now().UTC()
	for i, plan := range []string{"plus", "pro"} {
		if err := s.InsertSubscriptionMeter("codex", "default", subscription.Meter{At: at.Add(time.Duration(i) * time.Minute), Quota: "seven_day", Plan: plan, Used: 20}); err != nil {
			t.Fatal(err)
		}
	}
	for _, withPoll := range []bool{false, true} {
		if withPoll {
			if _, err := s.InsertCodexSnapshot(&api.CodexSnapshot{AccountID: 1, CapturedAt: at.Add(2 * time.Minute), PlanType: "pro"}); err != nil {
				t.Fatal(err)
			}
		}
		_, meters, err := s.SubscriptionInputs(context.Background(), "codex", "default", 1, at.Add(-time.Minute), at.Add(time.Hour))
		if err != nil || len(meters) != 2 {
			t.Fatalf("withPoll=%v meters=%+v err=%v", withPoll, meters, err)
		}
	}
}

func TestSubscriptionBenchmarkUsesCanonicalStandardRateAndRequiresTokenBreakdown(t *testing.T) {
	s := newTransferTestStore(t)
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, event := range []agentusage.UsageEvent{
		{Timestamp: at, Model: "gpt-5.6-sol", InputTokens: 1000000, TotalTokens: 1000000, CostUSD: 5, SpeedMode: "fast"},
		{Timestamp: at.Add(time.Minute), Model: "gpt-5.6-sol", TotalTokens: 1000000, CostUSD: 9},
	} {
		event.Source, event.Provider = "codex", "openai"
		line, err := event.ToAPIIntegrationLine()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ai.ParseUsageEventLine(line, "test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.InsertAPIIntegrationUsageEvent(parsed); err != nil {
			t.Fatal(err)
		}
	}
	events, _, err := s.SubscriptionInputs(context.Background(), "codex", "default", 1, at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	report := subscription.Analyze(events, nil, subscription.Profile{MonthlyUSD: 20})
	if report.Cost != 4 || report.UnknownRequests != 1 || report.ValueMultiple != nil {
		t.Fatalf("benchmark coverage or pricing incorrect: %+v", report)
	}
	var stored float64
	if err := s.db.QueryRow("SELECT SUM(cost_usd) FROM api_integration_usage_events").Scan(&stored); err != nil || stored != 14 {
		t.Fatalf("stored cost = %v, err = %v; want $14 and no error", stored, err)
	}
}

func TestSubscriptionArchiveBoundaryIsReportedWithoutChargingWholeBucket(t *testing.T) {
	s := newTransferTestStore(t)
	for _, line := range []string{
		`{"ts":"2026-01-15T12:05:00Z","integration":"Codex CLI","provider":"openai","account":"default","model":"gpt-5.6-sol","prompt_tokens":100,"total_tokens":100,"cost_usd":0.25}`,
		`{"ts":"2026-01-15T12:45:00Z","integration":"Codex CLI","provider":"openai","account":"default","model":"gpt-5.6-sol","prompt_tokens":50,"total_tokens":50,"cost_usd":0.15}`,
	} {
		insertAPIIntegrationUsageEventForTest(t, s, line, "/tmp/boundary.jsonl")
	}
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	end := at.Add(time.Hour)
	if result, err := s.CompactAPIIntegrationUsageEvents(end); err != nil || result.HourlyRows != 1 {
		t.Fatalf("compaction: %+v, %v", result, err)
	}
	events, _, err := s.SubscriptionInputs(context.Background(), "codex", "default", 1, at.Add(15*time.Minute), end)
	if err != nil {
		t.Fatal(err)
	}
	report := subscription.Analyze(events, nil, subscription.Profile{})
	if report.ExcludedArchiveBuckets != 1 || report.Requests != 0 || report.Cost != 0 {
		t.Fatalf("partial hour must be disclosed without counting the entire hour: %+v", report)
	}
}
