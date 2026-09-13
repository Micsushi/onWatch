package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

func TestCentralCompactedUsageReplayPreservesReceiptsAndExport(t *testing.T) {
	s := newTransferTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	observed := now.Add(-48 * time.Hour)
	payload := json.RawMessage(`{"ts":"` + observed.Format(time.RFC3339Nano) + `","integration":"codex","provider":"openai","account":"default","model":"gpt-5","prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"metadata":{"event_key":"compacted-replay"}}`)
	record, err := apiintegrations.ParseUsageEventLine(payload, "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertAPIIntegrationUsageEvent(record); err != nil {
		t.Fatal(err)
	}
	if result, err := s.CompactAPIIntegrationUsageEvents(now.Add(-24 * time.Hour)); err != nil || result.CompactedEvents != 1 {
		t.Fatalf("compaction=%+v err=%v", result, err)
	}
	first, _, err := s.CreateDevice("first", "linux")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateDevice("second", "windows")
	if err != nil {
		t.Fatal(err)
	}
	event := ingest.Event{EventID: "evt_compacted_replay", Kind: "usage_event", CapturedAt: observed, Provider: "openai", Account: ingest.Account{ExternalID: "default"}, Payload: payload}
	for _, device := range []*Device{first, first, second} {
		results, err := s.StoreIngestBatch(device, []ingest.Event{event}, now)
		if err != nil || len(results) != 1 || results[0].Status != "duplicate" {
			t.Fatalf("replay device=%s results=%+v err=%v", device.ID, results, err)
		}
	}
	var targetTable, targetID string
	if err := s.db.QueryRow(`SELECT target_table, target_record_id FROM ingest_receipts WHERE device_id=? AND event_id=?`, first.ID, event.EventID).Scan(&targetTable, &targetID); err != nil {
		t.Fatal(err)
	}
	if targetTable != "api_integration_usage_compacted_fingerprints" || targetID != record.Fingerprint {
		t.Fatalf("receipt target=%s/%s", targetTable, targetID)
	}
	event.Payload = json.RawMessage(strings.Replace(string(payload), `"event_key":"compacted-replay"`, `"event_key":"compacted-replay","session_id":"later-metadata"`, 1))
	if results, err := s.StoreIngestBatch(first, []ingest.Event{event}, now); err != nil || results[0].Status != "duplicate" {
		t.Fatalf("archived enrichment=%+v err=%v", results, err)
	}
	event.Payload = json.RawMessage(strings.Replace(string(payload), `"total_tokens":3`, `"total_tokens":30`, 1))
	if results, err := s.StoreIngestBatch(first, []ingest.Event{event}, now); err != nil || results[0].Status != "rejected" || results[0].Code != "event_payload_conflict" {
		t.Fatalf("conflicting replay=%+v err=%v", results, err)
	}
	var raw, receipts, requests, tokens int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_integration_usage_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT SUM(request_count), SUM(total_tokens) FROM api_integration_usage_hourly`).Scan(&requests, &tokens); err != nil {
		t.Fatal(err)
	}
	if raw != 0 || receipts != 2 || requests != 1 || tokens != 3 {
		t.Fatalf("raw=%d receipts=%d hourly requests=%d tokens=%d", raw, receipts, requests, tokens)
	}
	var archive bytes.Buffer
	if _, err := s.ExportData(&archive, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	destination := newTransferTestStore(t)
	if summary, err := destination.ImportData(bytes.NewReader(archive.Bytes())); err != nil || summary.Tables["api_integration_usage_hourly"].Inserted != 1 {
		t.Fatalf("exported tombstone replay must not create dangling raw provenance: summary=%+v err=%v", summary, err)
	}
}

func TestCentralReceiptRetargetUsesIndex(t *testing.T) {
	s := newTransferTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN UPDATE ingest_receipts SET target_record_id=? WHERE target_table='api_integration_usage_events' AND target_record_id=?`, "2", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "idx_ingest_receipts_target")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("usage canonicalization scans every receipt")
	}
}
