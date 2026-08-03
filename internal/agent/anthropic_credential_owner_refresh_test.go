package agent

import (
	"bytes"
	"context"
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

func TestAnthropicAgent_ExpiredCredentialsUseOwnerRefreshBeforePolling(t *testing.T) {
	var ownerRefreshCalls atomic.Int32
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
	client := api.NewAnthropicClient("expired-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)

	currentToken := "expired-token"
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials {
		return &api.AnthropicCredentials{
			AccessToken: currentToken,
			ExpiresAt:   time.Now().Add(-time.Minute),
			ExpiresIn:   -time.Minute,
		}
	})
	agent.SetTokenRefresh(func() string { return currentToken })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		ownerRefreshCalls.Add(1)
		currentToken = "owner-refreshed-token"
		return nil
	})

	agent.poll(context.Background())

	if got := ownerRefreshCalls.Load(); got != 1 {
		t.Fatalf("owner refresh calls = %d, want 1", got)
	}
	if got, _ := authHeader.Load().(string); got != "Bearer owner-refreshed-token" {
		t.Fatalf("Authorization = %q, want refreshed token", got)
	}
}

func TestAnthropicAgent_IsolatedOwnerDoesNotRotateBeforeExpiry(t *testing.T) {
	var oauthCalls atomic.Int32
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oauthCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer oauthServer.Close()
	api.SetOAuthURLForTest(oauthServer.URL)
	defer api.SetOAuthURLForTest(api.AnthropicOAuthTokenURL)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anthropicResponse(12, 34)))
	}))
	defer apiServer.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("valid-token", logger, api.WithAnthropicBaseURL(apiServer.URL+"/api/oauth/usage"))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials {
		return &api.AnthropicCredentials{
			AccessToken:  "valid-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(5 * time.Minute),
			ExpiresIn:    5 * time.Minute,
		}
	})
	agent.SetTokenRefresh(func() string { return "valid-token" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		t.Fatal("isolated owner called before token expiry")
		return nil
	})

	agent.poll(context.Background())

	if got := oauthCalls.Load(); got != 0 {
		t.Fatalf("direct OAuth refresh calls = %d, want 0 for isolated owner", got)
	}
}

func TestAnthropicAgent_IsolatedOwnerDoesNotRotateToBypassUsageRateLimit(t *testing.T) {
	var oauthCalls atomic.Int32
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oauthCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer oauthServer.Close()
	api.SetOAuthURLForTest(oauthServer.URL)
	defer api.SetOAuthURLForTest(api.AnthropicOAuthTokenURL)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer apiServer.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("valid-token", logger, api.WithAnthropicBaseURL(apiServer.URL+"/api/oauth/usage"))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials {
		return &api.AnthropicCredentials{
			AccessToken:  "valid-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(time.Hour),
			ExpiresIn:    time.Hour,
		}
	})
	agent.SetTokenRefresh(func() string { return "valid-token" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error {
		t.Fatal("isolated owner called for a valid token")
		return nil
	})

	agent.poll(context.Background())

	if got := oauthCalls.Load(); got != 0 {
		t.Fatalf("direct OAuth refresh calls = %d, want 0 for usage rate-limit handling", got)
	}
}
