package ingestserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/collector"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestMultiDeviceOfflineReplayAndRevocation(t *testing.T) {
	db, err := store.New(t.TempDir() + "/central.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New("127.0.0.1", 0, db, nil, "test-metrics")
	host := httptest.NewServer(s.httpServer.Handler)
	defer host.Close()
	for _, platform := range []string{"windows", "darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			device, token, err := db.CreateDevice(platform, platform)
			if err != nil {
				t.Fatal(err)
			}
			client := collector.NewClient(host.URL, device.ID, token)
			spool, err := collector.NewSpool(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			payload, _ := json.Marshal(map[string]any{"ts": now.Format(time.RFC3339Nano), "integration": "Codex CLI", "provider": "openai", "account": platform, "model": "test", "prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "metadata": map[string]any{"event_key": platform}})
			event := ingest.Event{EventID: "evt_" + platform + "_offline", Kind: "usage_event", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: platform}, Payload: payload}
			if err := spool.Append(event); err != nil {
				t.Fatal(err)
			}
			records, err := spool.Batch(10, 1<<20)
			if err != nil || len(records) != 1 {
				t.Fatalf("offline batch: %v %d", err, len(records))
			}
			for _, expected := range []string{"accepted", "duplicate"} {
				response, code, err := client.Upload(context.Background(), []ingest.Event{records[0].Event}, 0)
				if err != nil || code != http.StatusOK || len(response.Results) != 1 || response.Results[0].Status != expected {
					t.Fatalf("upload %s: code=%d err=%v response=%+v", expected, code, err, response)
				}
			}
			if err := spool.Ack(records, now); err != nil {
				t.Fatal(err)
			}
			status, err := spool.Status()
			if err != nil || status.PendingEvents != 0 {
				t.Fatalf("drain: %+v %v", status, err)
			}
			if err := db.RevokeDevice(device.ID); err != nil {
				t.Fatal(err)
			}
			_, code, err := client.Upload(context.Background(), []ingest.Event{event}, 0)
			if code != http.StatusUnauthorized || err == nil {
				t.Fatalf("revocation: %d %v", code, err)
			}
		})
	}
}
