package store

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

func TestCentralIngestMirrorsQuotaAndTracksEnrichment(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	device, _, err := s.CreateDevice("laptop", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPollOwner("codex", "1", "device", device.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	quotaPayload, _ := json.Marshal(ingest.QuotaSnapshot{Version: 1, Metrics: []ingest.QuotaMetric{{Name: "weekly", Value: 42, Unit: "percent"}}})
	quota := ingest.Event{EventID: "evt_quota_test", Kind: "quota_snapshot", CapturedAt: now, Provider: "codex", Account: ingest.Account{ExternalID: "1"}, Payload: quotaPayload}
	results, err := s.StoreIngestBatch(device, []ingest.Event{quota}, now)
	if err != nil || len(results) != 1 || results[0].Status != "accepted" {
		t.Fatalf("quota result=%v err=%v", results, err)
	}
	var utilization float64
	if err := s.db.QueryRow(`SELECT utilization FROM codex_quota_values`).Scan(&utilization); err != nil || utilization != 42 {
		t.Fatalf("mirrored utilization=%v err=%v", utilization, err)
	}
	results, err = s.StoreIngestBatch(device, []ingest.Event{quota}, now.Add(time.Second))
	if err != nil || results[0].Status != "duplicate" {
		t.Fatalf("duplicate result=%v err=%v", results, err)
	}

	basePayload := json.RawMessage(`{"ts":"` + now.Format(time.RFC3339Nano) + `","integration":"codex","provider":"openai","account":"default","model":"gpt-5","prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"metadata":{"event_key":"usage-test"}}`)
	usage := ingest.Event{EventID: "evt_usage_test", Kind: "usage_event", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: "default"}, Payload: basePayload}
	results, err = s.StoreIngestBatch(device, []ingest.Event{usage}, now)
	if err != nil || results[0].Status != "accepted" {
		t.Fatalf("usage result=%v err=%v", results, err)
	}
	usage.Payload = json.RawMessage(`{"ts":"` + now.Format(time.RFC3339Nano) + `","integration":"codex","provider":"openai","account":"default","model":"gpt-5","prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"metadata":{"event_key":"usage-test","mode":"agent"}}`)
	results, err = s.StoreIngestBatch(device, []ingest.Event{usage}, now.Add(time.Second))
	if err != nil || results[0].Status != "enriched" {
		t.Fatalf("enrichment result=%v err=%v", results, err)
	}
}

func TestCentralPollOwnershipFailsClosed(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, _, _ := s.CreateDevice("first", "linux")
	second, _, _ := s.CreateDevice("second", "darwin")
	if err := s.SetPollOwner("openai", "account", "device", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPollOwner("openai", "account", "device", second.ID); err == nil {
		t.Fatal("second active owner was accepted")
	}
	payload, _ := json.Marshal(ingest.QuotaSnapshot{Version: 1, Metrics: []ingest.QuotaMetric{{Name: "weekly", Value: 10, Unit: "percent"}}})
	now := time.Now().UTC().Truncate(time.Second)
	event := ingest.Event{EventID: "evt_wrong_owner", Kind: "quota_snapshot", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: "account"}, Payload: payload}
	results, err := s.StoreIngestBatch(second, []ingest.Event{event}, now)
	if err != nil || results[0].Code != "ownership_conflict" {
		t.Fatalf("wrong owner result=%v err=%v", results, err)
	}
	if err := s.ClearPollOwner("openai", "account"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPollOwner("openai", "account", "device", second.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCentralDeviceRevocationIsIdempotent(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	device, _, err := s.CreateDevice("revoke", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(device.ID); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetDevice(device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(device.ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.GetDevice(device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.DesiredConfigRevision != second.DesiredConfigRevision || !first.RevokedAt.Equal(*second.RevokedAt) {
		t.Fatalf("second revoke changed state: first=%+v second=%+v", first, second)
	}
}

func TestCentralS3F2T4(t *testing.T) {
	source, err := New(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	device, token, err := source.CreateDevice("portable", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	payload := json.RawMessage(`{"ts":"` + now.Format(time.RFC3339Nano) + `","integration":"codex","provider":"openai","account":"portable-account","model":"gpt-5","prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"metadata":{"event_key":"portable-event"}}`)
	event := ingest.Event{EventID: "evt_portable_event", Kind: "usage_event", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: "portable-account"}, Payload: payload}
	if results, err := source.StoreIngestBatch(device, []ingest.Event{event}, now); err != nil || results[0].Status != "accepted" {
		t.Fatalf("store event results=%v err=%v", results, err)
	}
	quotaPayload, _ := json.Marshal(ingest.QuotaSnapshot{Version: 1, Metrics: []ingest.QuotaMetric{{Name: "weekly", Value: 21, Unit: "percent"}}})
	quota := ingest.Event{EventID: "evt_portable_quota", Kind: "quota_snapshot", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: "portable-account"}, Payload: quotaPayload}
	if err := source.SetPollOwner("openai", "portable-account", "device", device.ID); err != nil {
		t.Fatal(err)
	}
	if results, err := source.StoreIngestBatch(device, []ingest.Event{quota}, now); err != nil || results[0].Status != "accepted" {
		t.Fatalf("store quota results=%v err=%v", results, err)
	}
	if err := source.ClearPollOwner("openai", "portable-account"); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	manifest, err := source.ExportData(&archive, ExportOptions{AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 2 || bytes.Contains(archive.Bytes(), []byte(token)) {
		t.Fatal("central export version or secret boundary is wrong")
	}
	destination, err := New(filepath.Join(t.TempDir(), "destination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	first, err := destination.ImportData(bytes.NewReader(archive.Bytes()))
	if err != nil || first.Tables["devices"].Inserted != 1 || first.Tables["observation_provenance"].Inserted != 2 || first.Tables["central_quota_snapshots"].Inserted != 1 || first.Tables["central_quota_values"].Inserted != 1 {
		t.Fatalf("first import=%+v err=%v", first, err)
	}
	var importedQuota float64
	if err := destination.db.QueryRow(`SELECT value FROM central_quota_values WHERE metric_name = 'weekly'`).Scan(&importedQuota); err != nil || importedQuota != 21 {
		t.Fatalf("imported quota=%v err=%v", importedQuota, err)
	}
	imported, err := destination.GetDevice(device.ID)
	if err != nil || imported.RevokedAt == nil {
		t.Fatalf("imported device=%+v err=%v", imported, err)
	}
	if _, err := destination.AuthenticateDevice(device.ID, token); err == nil {
		t.Fatal("portable import restored an enrollment credential")
	}
	second, err := destination.ImportData(bytes.NewReader(archive.Bytes()))
	if err != nil || second.Tables["devices"].Skipped != 1 || second.Total.Inserted != 0 {
		t.Fatalf("second import=%+v err=%v", second, err)
	}
}

func TestOnlineBackupIntegrity(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.CreateDevice("backup-device", "linux"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "backups", "snapshot.db")
	metadata, err := s.Backup(destination)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SHA256 == "" || metadata.Size == 0 {
		t.Fatalf("invalid metadata: %+v", metadata)
	}
	restored, err := New(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	devices, err := restored.ListDevices()
	if err != nil || len(devices) != 1 {
		t.Fatalf("restore devices=%v err=%v", devices, err)
	}
	if _, err := s.Backup(dbPath); err == nil {
		t.Fatal("same-path backup unexpectedly succeeded")
	}
}

func TestCentralImportRejectsAccountLabelCollision(t *testing.T) {
	destination, err := New(filepath.Join(t.TempDir(), "destination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if _, err := destination.db.Exec(`INSERT INTO provider_accounts(provider, name, external_id, created_at) VALUES('codex', 'personal', 'destination-id', ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	source, err := New(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.db.Exec(`INSERT INTO provider_accounts(provider, name, external_id, created_at) VALUES('codex', 'personal', 'source-id', ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := source.ExportData(&archive, ExportOptions{AppVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.ImportData(bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("distinct external accounts merged by display label")
	}
}
