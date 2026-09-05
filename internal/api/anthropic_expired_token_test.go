package api

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeClaudeCredentials(t *testing.T, dir string, expiresAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"claudeAiOauth":{"accessToken":"tok","refreshToken":"ref","expiresAt":` +
		strconv.FormatInt(expiresAt.UnixMilli(), 10) + `}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
}

// Handing an expired access token to the usage API just earns a 401, and after
// three of them the agent pauses polling. Read-only mode cannot refresh the
// token itself, so an expired one must read as "no token" and let onWatch wait
// for Claude Code to renew it.
func TestDetectAnthropicToken_IgnoresExpiredToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	writeClaudeCredentials(t, dir, time.Now().Add(-time.Hour))
	if token := DetectAnthropicToken(logger); token != "" {
		t.Fatalf("DetectAnthropicToken returned a token for expired credentials")
	}

	writeClaudeCredentials(t, dir, time.Now().Add(time.Hour))
	if token := DetectAnthropicToken(logger); token != "tok" {
		t.Fatalf("DetectAnthropicToken = %q, want the live token", token)
	}
}

// Credentials that record no expiry make no claim to be expired.
func TestDetectAnthropicToken_KeepsTokenWithUnknownExpiry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if token := DetectAnthropicToken(logger); token != "tok" {
		t.Fatalf("DetectAnthropicToken = %q, want the token when no expiry is recorded", token)
	}
}
