package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

type Client struct {
	serverURL, deviceID, token string
	http                       *http.Client
}

func NewClient(serverURL, deviceID, token string) *Client {
	return &Client{serverURL: serverURL, deviceID: deviceID, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Upload(ctx context.Context, events []ingest.Event, revision int64) (*ingest.BatchResponse, int, error) {
	request := ingest.Batch{SchemaVersion: ingest.SchemaVersion, DeviceID: c.deviceID, CollectorVersion: Version(), ConfigRevision: revision, SentAt: time.Now().UTC(), Events: events}
	var response ingest.BatchResponse
	status, err := c.post(ctx, "/v1/batches", request, &response)
	return &response, status, err
}
func (c *Client) Heartbeat(ctx context.Context, heartbeat ingest.Heartbeat) (*ingest.HeartbeatResponse, int, error) {
	var response ingest.HeartbeatResponse
	status, err := c.post(ctx, "/v1/heartbeat", heartbeat, &response)
	return &response, status, err
}
func (c *Client) post(ctx context.Context, path string, payload, output any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("collector server returned %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return response.StatusCode, err
	}
	return response.StatusCode, nil
}

func retryDelay(attempt int) time.Duration {
	delay := time.Second << min(attempt, 8)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(delay) * jitter)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func Version() string { return "1" }
