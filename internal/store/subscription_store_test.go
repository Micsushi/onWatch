package store

import (
	"bytes"
	"context"
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
