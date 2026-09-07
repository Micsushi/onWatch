package store

import (
	"encoding/json"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"testing"
	"time"
)

func TestCentralQuotaPreservesPlanAndSummaryWindows(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	device, _, err := s.CreateDevice("test", "windows")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for _, provider := range []string{"openai", "antigravity"} {
		if err := s.SetPollOwner(provider, "account", "device", device.ID); err != nil {
			t.Fatal(err)
		}
		metrics := []ingest.QuotaMetric{{Name: "seven_day", Value: 40, Unit: "percent"}}
		plan := "pro"
		if provider == "antigravity" {
			plan = "Pro"
			metrics = []ingest.QuotaMetric{{Name: "claude_week", Group: "claude", Window: "weekly", Value: 40, Unit: "percent"}, {Name: "claude_five", Group: "claude", Window: "five_hour", Value: 10, Unit: "percent"}}
		}
		payload, _ := json.Marshal(ingest.QuotaSnapshot{Version: 1, Plan: plan, Metrics: metrics})
		event := ingest.Event{EventID: "evt_" + provider, Kind: "quota_snapshot", CapturedAt: at, Provider: provider, Account: ingest.Account{ExternalID: "account"}, Payload: payload}
		results, err := s.StoreIngestBatch(device, []ingest.Event{event}, at)
		if err != nil || len(results) != 1 || results[0].Status != "accepted" {
			t.Fatalf("%+v %v", results, err)
		}
	}
	var plan string
	if err := s.db.QueryRow(`SELECT plan_type FROM codex_snapshots ORDER BY id DESC LIMIT 1`).Scan(&plan); err != nil || plan != "pro" {
		t.Fatalf("plan %s %v", plan, err)
	}
	latest, err := s.QueryLatestAntigravity()
	if err != nil || latest.PlanName != "Pro" || len(latest.SummaryGroups) != 1 || len(latest.SummaryGroups[0].Buckets) != 2 {
		t.Fatalf("summary %+v %v", latest, err)
	}
}
