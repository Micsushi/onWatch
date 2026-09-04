package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

func anthropicOwnerAgent(t *testing.T, creds *api.AnthropicCredentials) (*AnthropicAgent, *store.Store) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anthropicResponse(12, 34)))
	}))
	t.Cleanup(server.Close)

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = str.Close() })

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("owner-token", logger, api.WithAnthropicBaseURL(server.URL))
	agent := NewAnthropicAgent(client, str, tracker.NewAnthropicTracker(str, logger), time.Minute, logger, nil)
	agent.SetCredentialsRefresh(func() *api.AnthropicCredentials { return creds })
	agent.SetTokenRefresh(func() string { return "owner-token" })
	agent.SetCredentialOwnerRefresh(func(context.Context) error { return nil })
	agent.SetCredentialOwnerProfile(`C:\profiles\onwatch`)
	return agent, str
}

func pollErrorMessage(t *testing.T, str *store.Store) string {
	t.Helper()
	raw, err := str.GetSetting(store.AnthropicPollErrorSetting)
	if err != nil {
		t.Fatalf("get poll error: %v", err)
	}
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode poll error: %v", err)
	}
	return payload["message"]
}

// The isolated profile goes silently dead once its refresh token expires. Warn
// while it still works, naming the profile and the command that fixes it.
func TestAnthropicAgent_WarnsBeforeCredentialOwnerLoginExpires(t *testing.T) {
	creds := &api.AnthropicCredentials{
		AccessToken:           "owner-token",
		RefreshToken:          "owner-refresh",
		ExpiresAt:             time.Now().Add(time.Hour),
		ExpiresIn:             time.Hour,
		RefreshTokenExpiresAt: time.Now().Add(60 * time.Hour),
	}
	agent, str := anthropicOwnerAgent(t, creds)

	agent.poll(context.Background())

	msg := pollErrorMessage(t, str)
	if msg == "" {
		t.Fatal("no dashboard warning stored before the login expires")
	}
	if !strings.Contains(msg, "2 days") {
		t.Fatalf("warning = %q, want the remaining time", msg)
	}
	if !strings.Contains(msg, `C:\profiles\onwatch`) {
		t.Fatalf("warning = %q, want the isolated profile named", msg)
	}
	if !strings.Contains(msg, "/login") {
		t.Fatalf("warning = %q, want the login instruction", msg)
	}
	if strings.Contains(msg, "claude auth") {
		t.Fatalf("warning = %q, must not name the nonexistent 'claude auth'", msg)
	}
}

// A login with plenty of life left must not nag, and must clear a stale banner.
func TestAnthropicAgent_NoWarningWhenCredentialOwnerLoginIsHealthy(t *testing.T) {
	creds := &api.AnthropicCredentials{
		AccessToken:           "owner-token",
		RefreshToken:          "owner-refresh",
		ExpiresAt:             time.Now().Add(time.Hour),
		ExpiresIn:             time.Hour,
		RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	agent, str := anthropicOwnerAgent(t, creds)
	agent.persistPollError("credential_owner", "stale banner")

	agent.poll(context.Background())

	if msg := pollErrorMessage(t, str); msg != "" {
		t.Fatalf("poll error = %q, want it cleared for a healthy login", msg)
	}
}

// Credentials without a recorded refresh expiry must not produce a warning.
func TestAnthropicAgent_NoWarningWhenRefreshExpiryUnknown(t *testing.T) {
	creds := &api.AnthropicCredentials{
		AccessToken:  "owner-token",
		RefreshToken: "owner-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		ExpiresIn:    time.Hour,
	}
	agent, str := anthropicOwnerAgent(t, creds)

	agent.poll(context.Background())

	if msg := pollErrorMessage(t, str); msg != "" {
		t.Fatalf("poll error = %q, want none when the expiry is unknown", msg)
	}
}
