package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

func TestCentralS2F1T2(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"evt_one00", "evt_two00"} {
		event := ingest.Event{EventID: id, Kind: "quota_snapshot", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: "a"}, Payload: json.RawMessage(`{"version":1,"metrics":[{"name":"weekly","value":1,"unit":"percent"}]}`)}
		if err := spool.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	records, err := spool.Batch(1, 2<<20)
	if err != nil || len(records) != 1 {
		t.Fatalf("batch: %d %v", len(records), err)
	}
	if err := spool.Ack(records, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	records, err = reopened.Batch(10, 2<<20)
	if err != nil || len(records) != 1 || records[0].Event.EventID != "evt_two00" {
		t.Fatalf("recovery: %#v %v", records, err)
	}
	path := filepath.Join(dir, "events-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = file.WriteString(`{"event_id":"partial"`)
	_ = file.Close()
	records, err = reopened.Batch(10, 2<<20)
	if err != nil || len(records) != 1 {
		t.Fatalf("partial line changed batch: %d %v", len(records), err)
	}
}

func TestCentralS2F1T5(t *testing.T) {
	spool, err := NewSpool(t.TempDir(), 256)
	if err != nil {
		t.Fatal(err)
	}
	event := ingest.Event{EventID: "evt_full00", Kind: "quota_snapshot", CapturedAt: time.Now().UTC(), Provider: "openai", Account: ingest.Account{ExternalID: "a"}, Payload: json.RawMessage(`{"version":1,"metrics":[{"name":"weekly","value":1,"unit":"percent"}]}`)}
	if err := spool.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := spool.Append(event); err == nil {
		t.Fatal("spool cap did not stop production")
	}
	records, err := spool.Batch(10, 2<<20)
	if err != nil || len(records) != 1 {
		t.Fatalf("unacknowledged event was lost: %d %v", len(records), err)
	}
}
