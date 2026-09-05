package agent

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// In read-only mode onWatch cannot refresh the token itself. When Claude Code's
// token goes cold, detection reports nothing - and the agent must stop using the
// token it already holds. Otherwise it spends every cycle earning 401s until it
// pauses polling, and only a restart brings it back.
func TestAnthropicAgent_ReadOnlyStopsPollingWhenTokenGoesCold(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("stale-token", logger, api.WithAnthropicBaseURL(server.URL))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	// Read-only mode: no credential owner, no fallback, and detection finds
	// nothing usable because the token on disk has expired.
	agent.SetTokenRefresh(func() string { return "" })

	agent.poll(context.Background())

	if got := calls.Load(); got != 0 {
		t.Fatalf("made %d API calls with a cold token, want 0", got)
	}

	msg := pollErrorMessage(t, str)
	if msg == "" {
		t.Fatal("no poll error recorded for a cold token")
	}
	if !strings.Contains(strings.ToLower(msg), "claude") {
		t.Fatalf("poll error = %q, want it to name Claude credentials", msg)
	}
}

// A live token must still poll normally.
func TestAnthropicAgent_ReadOnlyPollsWithLiveToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anthropicResponse(12, 34)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("", logger, api.WithAnthropicBaseURL(server.URL))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetTokenRefresh(func() string { return "live-token" })

	agent.poll(context.Background())

	if got := calls.Load(); got != 1 {
		t.Fatalf("made %d API calls with a live token, want 1", got)
	}
}
