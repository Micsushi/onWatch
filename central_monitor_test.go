package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestCentralMonitorDurableFreshAccounts(t *testing.T) {
	db, err := store.New(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	device, _, err := db.CreateDevice("test", "windows")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sequence := 0
	add := func(account string, captured time.Time) {
		t.Helper()
		if err := db.SetPollOwner("codex", account, "device", device.ID); err != nil {
			t.Fatal(err)
		}
		reset := now.Add(time.Hour)
		payload, _ := json.Marshal(ingest.QuotaSnapshot{Version: 1, Metrics: []ingest.QuotaMetric{{Name: "five_hour", Unit: "percent", Value: 96, ResetsAt: &reset}}})
		sequence++
		results, err := db.StoreIngestBatch(device, []ingest.Event{{EventID: fmt.Sprintf("evt_%d", sequence), Kind: "quota_snapshot", Provider: "codex", Account: ingest.Account{ExternalID: account}, CapturedAt: captured, Payload: payload}}, now)
		if err != nil || results[0].Status != "accepted" {
			t.Fatalf("results=%v err=%v", results, err)
		}
	}
	add("work", now)
	add("personal", now)
	add("offline", now.Add(-time.Hour))
	var statuses []notify.QuotaStatus
	processor := centralQuotaProcessor(db, slog.Default(), func(status notify.QuotaStatus) { statuses = append(statuses, status) })
	if err := processCentralQuotaUpdates(db, now, processor); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].AccountID == statuses[1].AccountID {
		t.Fatalf("statuses=%+v", statuses)
	}
	// A new monitor instance reads the persisted cursor rather than alerting twice.
	if err := processCentralQuotaUpdates(db, now, processor); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatal("replayed notifications")
	}
	// Even a recent offline sample must not replace a newer captured observation.
	add("work", now.Add(-time.Minute))
	if err := processCentralQuotaUpdates(db, now, processor); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatal("late sample notified")
	}
	add("work", now.Add(time.Second))
	if err := processCentralQuotaUpdates(db, now, func(store.CentralQuotaObservation) error { return errors.New("temporarily unavailable") }); err == nil {
		t.Fatal("missing processor error")
	}
	if err := processCentralQuotaUpdates(db, now, processor); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 3 {
		t.Fatalf("failed observation lost: %d", len(statuses))
	}
}
