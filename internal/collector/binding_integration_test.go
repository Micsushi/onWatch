package collector

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/ingestserver"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestBindingServerCollectorRoundTrip(t *testing.T) {
	email := "synthetic@example.invalid"
	groups := []api.AntigravityQuotaSummaryGroup{{GroupKey: "gemini", Buckets: []api.AntigravityQuotaSummaryBucket{{BucketID: "weekly", Window: "weekly", UsagePercent: 48.1}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dbPath := filepath.Join(t.TempDir(), "central.db")
	database, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	device, token, err := database.CreateDevice("synthetic", "windows")
	if err != nil {
		t.Fatal(err)
	}
	assignment := ingest.ProviderAssignment{Provider: "antigravity", ExternalID: "synthetic-opaque", PollInterval: "60s"}
	if err := database.SetDeviceDesiredConfig(device.ID, ingest.DesiredConfig{Assignments: []ingest.ProviderAssignment{assignment}}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetPollOwner("antigravity", assignment.ExternalID, "device", device.ID); err != nil {
		t.Fatal(err)
	}
	current, err := database.GetDevice(device.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision := current.DesiredConfigRevision
	if _, err := store.ConfigureAntigravityBinding(dbPath, device.ID, assignment.ExternalID, email, revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureAntigravityBinding(dbPath, device.ID, assignment.ExternalID, "other@example.invalid", revision, true); err == nil {
		t.Fatal("stale binding accepted")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := ingestserver.New("127.0.0.1", port, database, logger, "")
	ended := make(chan error, 1)
	go func() { ended <- server.Start() }()
	defer func() { server.Shutdown(context.Background()); <-ended }()
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	for {
		response, err := http.Get(url + "/healthz")
		if err == nil {
			response.Body.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("isolated server did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cfg := Config{ServerURL: url, DeviceID: device.ID, Token: token, SpoolDir: t.TempDir(), HomeDir: t.TempDir(), SpoolMaxBytes: 1 << 20, BatchSize: 20}
	r, err := NewRuntime(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	r.revision, r.desired = revision, current.DesiredConfig
	if err := r.saveLocalState(); err != nil {
		t.Fatal(err)
	}
	r, err = NewRuntime(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.heartbeatOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r.revision != revision+1 || r.desired.Assignments[0].AntigravityAccountEmail != email {
		t.Fatal("server binding did not replace stale persisted config")
	}
	t.Setenv("ANTIGRAVITY_SOURCE", "cli")
	fetches := 0
	r.antigravityFetch = func(_ context.Context, _ string) (*api.AntigravitySnapshot, error) {
		fetches++
		return &api.AntigravitySnapshot{Email: email, SummaryGroups: groups}, nil
	}
	event, err := r.pollQuota(ctx, r.desired.Assignments[0])
	if err != nil {
		t.Fatal(err)
	}
	var measured ingest.QuotaSnapshot
	if json.Unmarshal(event.Payload, &measured) != nil || len(measured.Metrics) == 0 {
		t.Fatal("measured conversion absent")
	}
	maxValue := measured.Metrics[0].Value
	for _, metric := range measured.Metrics {
		if metric.Value > maxValue {
			maxValue = metric.Value
		}
		t.Logf("measured value=%g unit=%s window=%s", metric.Value, metric.Unit, metric.Window)
	}
	if err := r.spool.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := r.uploadOnce(ctx); err != nil {
		t.Fatal(err)
	}
	readback, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer readback.Close()
	var count int
	if err := readback.QueryRow("SELECT count(*) FROM central_quota_snapshots WHERE provider='antigravity' AND external_account_id=?", assignment.ExternalID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attributed snapshots=%d error=%v", count, err)
	}
	response, _, err := r.client.Upload(ctx, []ingest.Event{event}, r.revision)
	if err != nil || len(response.Results) != 1 || response.Results[0].Status != "duplicate" {
		t.Fatal("replayed quota was not deduplicated", err)
	}
	var value float64
	if err := readback.QueryRow("SELECT count(*), max(value) FROM central_quota_values").Scan(&count, &value); err != nil || count != len(measured.Metrics) || value != maxValue {
		t.Fatalf("replay or measured quota changed: count=%d value=%v error=%v", count, value, err)
	}
	if _, err := store.ConfigureAntigravityBinding(dbPath, device.ID, assignment.ExternalID, "", revision+1, true); err != nil {
		t.Fatal(err)
	}
	if err := r.heartbeatOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pollQuota(ctx, r.desired.Assignments[0]); err == nil || fetches != 1 {
		t.Fatal("rollback did not reject before provider fetch")
	}
}
