package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCredentialsFile(t *testing.T, dir, accessToken string, expiresAt time.Time) {
	t.Helper()
	payload := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken": accessToken,
			"expiresAt":   expiresAt.UnixMilli(),
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), data, 0600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
}

func TestDetectAnthropicTokenInDir(t *testing.T) {
	dir := t.TempDir()
	writeCredentialsFile(t, dir, "profile-token", time.Now().Add(time.Hour))

	if got := DetectAnthropicTokenInDir(dir); got != "profile-token" {
		t.Fatalf("token = %q, want profile-token", got)
	}
}

func TestDetectAnthropicTokenInDir_RejectsExpiredAndMissing(t *testing.T) {
	expired := t.TempDir()
	writeCredentialsFile(t, expired, "old-token", time.Now().Add(-time.Hour))
	if got := DetectAnthropicTokenInDir(expired); got != "" {
		t.Fatalf("token = %q, want empty for an expired credential", got)
	}

	empty := t.TempDir()
	writeCredentialsFile(t, empty, "", time.Now().Add(time.Hour))
	if got := DetectAnthropicTokenInDir(empty); got != "" {
		t.Fatalf("token = %q, want empty for a signed-out profile", got)
	}

	if got := DetectAnthropicTokenInDir(t.TempDir()); got != "" {
		t.Fatalf("token = %q, want empty when no credentials file exists", got)
	}
	if got := DetectAnthropicTokenInDir(""); got != "" {
		t.Fatalf("token = %q, want empty for an empty dir", got)
	}
}

func TestDefaultClaudeConfigDir_IgnoresEnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "isolated"))
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if got := DefaultClaudeConfigDir(); got != filepath.Join(home, ".claude") {
		t.Fatalf("DefaultClaudeConfigDir() = %q, want the home profile", got)
	}
}
