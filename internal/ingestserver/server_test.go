package ingestserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestCentralS1F1T6(t *testing.T) {
	database, err := store.New(t.TempDir() + "/central.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	device, token, err := database.CreateDevice("test", "linux")
	if err != nil {
		t.Fatal(err)
	}
	server := New("127.0.0.1", 0, database, nil, "metrics-secret")
	httpServer := httptest.NewServer(server.httpServer.Handler)
	defer httpServer.Close()
	now := time.Now().UTC().Truncate(time.Second)
	payload := map[string]any{"ts": now.Format(time.RFC3339Nano), "integration": "Codex CLI", "provider": "openai", "account": "account-1", "model": "gpt-5", "prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "metadata": map[string]any{"event_key": "stable-key"}}
	payloadJSON, _ := json.Marshal(payload)
	batch := ingest.Batch{SchemaVersion: 1, DeviceID: device.ID, CollectorVersion: "test", SentAt: now, Events: []ingest.Event{{EventID: "evt_test123", Kind: "usage_event", CapturedAt: now, Provider: "openai", Account: ingest.Account{ExternalID: "account-1"}, Payload: payloadJSON}}}
	request := func(auth string) *http.Response {
		body, _ := json.Marshal(batch)
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/batches", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+auth)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request(token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first status %d", response.StatusCode)
	}
	var first ingest.BatchResponse
	_ = json.NewDecoder(response.Body).Decode(&first)
	response.Body.Close()
	if first.Results[0].Status != "accepted" {
		t.Fatalf("first result %#v", first.Results)
	}
	response = request(token)
	var replay ingest.BatchResponse
	_ = json.NewDecoder(response.Body).Decode(&replay)
	response.Body.Close()
	if replay.Results[0].Status != "duplicate" {
		t.Fatalf("replay result %#v", replay.Results)
	}
	if err := database.RevokeDevice(device.ID); err != nil {
		t.Fatal(err)
	}
	response = request(token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked status %d", response.StatusCode)
	}
	if response = request("wrong"); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("wrong token status %d", response.StatusCode)
	}
	response.Body.Close()
}
