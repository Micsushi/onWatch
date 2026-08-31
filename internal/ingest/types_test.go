package ingest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCentralS1F1T2(t *testing.T) {
	now := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"version":1,"metrics":[{"name":"weekly","value":42,"unit":"percent"}]}`)
	event := Event{EventID: "evt_12345", Kind: "quota_snapshot", CapturedAt: now, Provider: "openai", Account: Account{ExternalID: "account-1"}, Payload: payload}
	batch := Batch{SchemaVersion: 1, DeviceID: "dev_0123456789abcdef0123456789abcdef", SentAt: now, Events: []Event{event}}
	encoded, _ := json.Marshal(batch)
	decoded, err := DecodeBatch(encoded, now)
	if err != nil {
		t.Fatalf("DecodeBatch: %v", err)
	}
	if err := decoded.Events[0].Validate(now); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if HashPayload(json.RawMessage(`{"b":2,"a":1}`)) != HashPayload(json.RawMessage(`{"a":1,"b":2}`)) {
		t.Fatal("payload hash is not canonical")
	}
	bad := event
	bad.Payload = json.RawMessage(`{"version":1,"access_token":"secret","metrics":[]}`)
	if err := bad.Validate(now); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("secret payload accepted: %v", err)
	}
	old := event
	old.CapturedAt = now.Add(-91 * 24 * time.Hour)
	if err := old.Validate(now); err == nil {
		t.Fatal("old live observation accepted")
	}
}

func TestDecodeBatchRejectsTrailingJSON(t *testing.T) {
	now := time.Now().UTC()
	device := "dev_0123456789abcdef0123456789abcdef"
	payload := fmt.Sprintf(`{"schema_version":1,"device_id":%q,"sent_at":%q,"events":[{"event_id":"evt_valid","kind":"quota_snapshot","captured_at":%q,"provider":"codex","account":{"external_id":"1"},"payload":{"version":1,"metrics":[{"name":"weekly","value":1,"unit":"percent"}]}}]} {}`, device, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if _, err := DecodeBatch([]byte(payload), now); err == nil {
		t.Fatal("trailing JSON unexpectedly accepted")
	}
}
