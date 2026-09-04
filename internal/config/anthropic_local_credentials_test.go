package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Read-only mode is the zero-maintenance setup: no rotation, no isolated
// profile, just whatever token Claude Code keeps on disk. The dashboard must
// still show Anthropic, or the provider vanishes while it is polling fine.
func TestHasProvider_AnthropicFromLocalClaudeCredentials(t *testing.T) {
	cfg := Config{AnthropicSource: "auto", AnthropicLocalCredentials: true}

	if !cfg.HasProvider("anthropic") {
		t.Fatal("HasProvider(anthropic) = false with local Claude credentials")
	}
	if !slices.Contains(cfg.AvailableProviders(), "anthropic") {
		t.Fatal("AvailableProviders omits anthropic with local Claude credentials")
	}
}

// Nothing on disk and nothing configured means nothing to poll.
func TestHasProvider_AnthropicAbsentWithoutAnyCredentials(t *testing.T) {
	cfg := Config{AnthropicSource: "auto"}

	if cfg.HasProvider("anthropic") {
		t.Fatal("HasProvider(anthropic) = true with no credentials at all")
	}
}

func TestLoad_DetectsLocalClaudeCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_TOKEN_ROTATION", "")
	t.Setenv("ANTHROPIC_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AnthropicLocalCredentials {
		t.Fatal("AnthropicLocalCredentials = true before any credentials file exists")
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	credsPath := filepath.Join(claudeDir, ".credentials.json")
	if err := os.WriteFile(credsPath, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	cfg, err = Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !cfg.AnthropicLocalCredentials {
		t.Fatal("AnthropicLocalCredentials = false with a credentials file present")
	}
	if !cfg.HasProvider("anthropic") {
		t.Fatal("HasProvider(anthropic) = false in read-only mode")
	}
}
