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

// With no fallback available the agent skips the API rather than sending a token
// it knows is dead - but it must not latch itself off.
func TestAnthropicAgent_OwnerRefreshFailureWithoutFallbackSkipsAPI(t *testing.T) {
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

	if client.HasToken() {
		t.Fatal("dead token kept after owner refresh failure without a fallback")
	}
	if agent.authPaused {
		t.Fatal("polling latched off instead of retrying on the next cycle")
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
	if stored["category"] != "credential_owner" {
		t.Fatalf("category = %q, want credential_owner", stored["category"])
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

// The dead-profile state must not be permanent: the borrowed token comes and
// goes with its owner's refresh cycle, so a fallback that appears later has to
// bring polling back without a restart. This is the regression that left the
// dashboard stale for hours.
func TestAnthropicAgent_ResumesWhenFallbackTokenReappears(t *testing.T) {
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
	fallback := ""
	agent.SetFallbackToken(func() string { return fallback })

	agent.poll(context.Background())
	if authHeader.Load() != nil {
		t.Fatal("polled the API with no usable credentials")
	}

	fallback = "refreshed-main-profile-token"
	agent.poll(context.Background())

	if got, _ := authHeader.Load().(string); got != "Bearer refreshed-main-profile-token" {
		t.Fatalf("Authorization = %q, want the recovered fallback token", got)
	}
}

// Once the isolated profile becomes usable again, the next primary token must
// clear the degraded state instead of leaving the dashboard warning forever.
func TestAnthropicAgent_PrimaryTokenRecoveryClearsFallback(t *testing.T) {
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
	client := api.NewAnthropicClient("", logger, api.WithAnthropicBaseURL(server.URL))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	isolatedAvailable := false
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials {
		if !isolatedAvailable {
			return nil
		}
		return &api.AnthropicCredentials{
			AccessToken: "isolated-profile-token",
			ExpiresAt:   time.Now().Add(time.Hour),
			ExpiresIn:   time.Hour,
		}
	})
	agent.SetTokenRefresh(func() string {
		if isolatedAvailable {
			return "isolated-profile-token"
		}
		return ""
	})
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		return errors.New("Claude login required")
	})
	agent.SetFallbackToken(func() string { return "main-profile-token" })

	agent.poll(context.Background())
	if !agent.usingFallbackToken {
		t.Fatal("expected the first poll to use fallback credentials")
	}

	isolatedAvailable = true
	agent.poll(context.Background())

	if agent.usingFallbackToken {
		t.Fatal("primary token recovery left the agent in degraded mode")
	}
	if got, _ := authHeader.Load().(string); got != "Bearer isolated-profile-token" {
		t.Fatalf("Authorization = %q, want the recovered primary token", got)
	}
	if raw, err := str.GetSetting(store.AnthropicPollErrorSetting); err != nil || raw != "" {
		t.Fatalf("degraded warning not cleared after primary recovery: raw=%q err=%v", raw, err)
	}
}

// A signed-out profile fails forever and every attempt spawns a Claude
// subprocess, so attempts must be spaced out once the failure looks durable.
func TestAnthropicAgent_OwnerRefreshBacksOffAfterRepeatedFailures(t *testing.T) {
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
	var ownerCalls atomic.Int32
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		ownerCalls.Add(1)
		return errors.New("Claude login required")
	})
	agent.SetFallbackToken(func() string { return "main-profile-token" })

	for i := 0; i < maxOwnerRefreshFailures+3; i++ {
		agent.poll(context.Background())
	}

	if got := ownerCalls.Load(); got != int32(maxOwnerRefreshFailures) {
		t.Fatalf("owner refresh calls = %d, want %d", got, maxOwnerRefreshFailures)
	}
	if !agent.usingFallbackToken {
		t.Fatal("backoff cycles stopped using the fallback token")
	}
}

// Backoff must not outlive the problem: once the owner can refresh again, the
// next attempt after the window succeeds and the degraded warning clears.
func TestAnthropicAgent_OwnerRefreshRecoversAfterBackoffWindow(t *testing.T) {
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

	for i := 0; i < maxOwnerRefreshFailures; i++ {
		agent.poll(context.Background())
	}
	if agent.ownerRetryAt.IsZero() {
		t.Fatal("expected a backoff window after repeated owner failures")
	}

	// The profile is re-authenticated and the window elapses.
	ownerFails = false
	agent.ownerRetryAt = time.Now().Add(-time.Second)
	agent.poll(context.Background())

	if agent.usingFallbackToken {
		t.Fatal("still on borrowed credentials after the owner recovered")
	}
	if raw, err := str.GetSetting(store.AnthropicPollErrorSetting); err != nil || raw != "" {
		t.Fatalf("degraded warning not cleared after recovery: raw=%q err=%v", raw, err)
	}
}
