package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// When the isolated credential owner cannot refresh (its profile is signed out),
// a read-only fallback token keeps polling alive instead of going dark.
func TestAnthropicAgent_OwnerRefreshFailureUsesFallbackToken(t *testing.T) {
	var authHeader atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader.Store(r.Header.Get("Authorization"))
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
	client := api.NewAnthropicClient("expired-token", logger, api.WithAnthropicBaseURL(server.URL))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials { return nil })
	agent.SetTokenRefresh(func() string { return "" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		return errors.New("Claude login required")
	})
	agent.SetFallbackToken(func() string { return "main-profile-token" })

	agent.poll(context.Background())

	if agent.authPaused {
		t.Fatal("polling paused despite an available fallback token")
	}
	if got, _ := authHeader.Load().(string); got != "Bearer main-profile-token" {
		t.Fatalf("Authorization = %q, want the fallback token", got)
	}
}

// The fallback is read-only: it must never be handed to the OAuth rotation path.
func TestAnthropicAgent_FallbackTokenIsNotRotated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials { return nil })
	agent.SetTokenRefresh(func() string { return "" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		return errors.New("Claude login required")
	})
	agent.SetFallbackToken(func() string { return "main-profile-token" })

	agent.poll(context.Background())

	if agent.lastToken == "main-profile-token" {
		t.Fatal("fallback token was adopted as the rotatable token")
	}
}

// With no fallback available the agent still pauses, as before.
func TestAnthropicAgent_OwnerRefreshFailureWithoutFallbackStillPauses(t *testing.T) {
	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("expired-token", logger)
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials { return nil })
	agent.SetTokenRefresh(func() string { return "" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		return errors.New("Claude login required")
	})
	agent.SetFallbackToken(func() string { return "" })

	agent.poll(context.Background())

	if !agent.authPaused {
		t.Fatal("expected polling to pause without a fallback token")
	}
}

// Poll failures are persisted so the dashboard can explain why data is stale.
func TestAnthropicAgent_PersistsPollErrorAndClearsOnSuccess(t *testing.T) {
	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("expired-token", logger)
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials { return nil })
	agent.SetTokenRefresh(func() string { return "" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		return errors.New("Claude login required")
	})

	agent.poll(context.Background())

	raw, err := str.GetSetting(store.AnthropicPollErrorSetting)
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a persisted poll error after a failed poll")
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored poll error is not JSON: %v", err)
	}
	if stored["category"] != "authentication" {
		t.Fatalf("category = %q, want authentication", stored["category"])
	}
	if stored["message"] == "" || stored["at"] == "" {
		t.Fatalf("incomplete stored poll error: %v", stored)
	}

	agent.recordPollSuccess()

	if raw, err := str.GetSetting(store.AnthropicPollErrorSetting); err != nil || raw != "" {
		t.Fatalf("poll error not cleared on success: raw=%q err=%v", raw, err)
	}
}

// A fallback poll succeeds, but the dashboard must keep saying the isolated
// profile is signed out - otherwise the warning vanishes on the first poll.
func TestAnthropicAgent_FallbackKeepsDegradedWarningAfterSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials { return nil })
	agent.SetTokenRefresh(func() string { return "" })
	ownerFails := true
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		if ownerFails {
			return errors.New("Claude login required")
		}
		return nil
	})
	agent.SetFallbackToken(func() string { return "main-profile-token" })

	agent.poll(context.Background())

	raw, err := str.GetSetting(store.AnthropicPollErrorSetting)
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored poll error is not JSON (%q): %v", raw, err)
	}
	if stored["category"] != "credential_owner" {
		t.Fatalf("category = %q, want credential_owner", stored["category"])
	}

	// Once the isolated profile is re-authenticated, the warning clears.
	ownerFails = false
	agent.poll(context.Background())

	if raw, err := str.GetSetting(store.AnthropicPollErrorSetting); err != nil || raw != "" {
		t.Fatalf("warning not cleared after owner recovery: raw=%q err=%v", raw, err)
	}
}
