package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestProviderHistoryMaintenanceAgentCompactsAfterDelay(t *testing.T) {
	t.Parallel()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()

	base := time.Now().UTC().Add(-45 * 24 * time.Hour).Truncate(time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := st.InsertGeminiSnapshot(&api.GeminiSnapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Minute),
			Quotas:     []api.GeminiQuota{{ModelID: "gemini-test"}},
		}); err != nil {
			t.Fatalf("InsertGeminiSnapshot: %v", err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ag := NewProviderHistoryMaintenanceAgent(st, 30*24*time.Hour, logger)
	ag.initialDelay = time.Millisecond
	ag.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ag.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, queryErr := st.QueryGeminiRange(base.Add(-time.Hour), base.Add(time.Hour))
		if queryErr == nil && len(rows) == 1 {
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("provider history was not compacted")
}
