package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

func init() {
	if os.Getenv("ONWATCH_RENEWAL_PROBE_CHILD") != "1" || len(os.Args) < 2 || os.Args[1] != "-p" {
		return
	}
	if os.Getenv("ONWATCH_RENEWAL_PROBE_FAIL") == "1" {
		os.Exit(1)
	}
	if os.Getenv("ONWATCH_RENEWAL_PROBE_FAIL") == "noop" {
		os.Exit(0)
	}
	path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
	data := map[string]any{"claudeAiOauth": map[string]any{"accessToken": "renewed-fixture", "expiresAt": time.Now().Add(time.Hour).UnixMilli()}}
	b, _ := json.Marshal(data)
	if os.WriteFile(path, b, 0600) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCollectorClaudeRenewal(t *testing.T) {
	for _, tc := range []struct {
		name, rotation           string
		healthy, fail, wantRenew bool
	}{
		{"expired opt-in", "on", false, false, true},
		{"healthy untouched", "on", true, false, false},
		{"read-only default", "", false, false, false},
		{"explicit off", "off", false, false, false},
		{"failed CLI", "on", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", dir)
			t.Setenv("ANTHROPIC_TOKEN_ROTATION", tc.rotation)
			executable, _ := os.Executable()
			t.Setenv("ANTHROPIC_CLAUDE_PATH", executable)
			t.Setenv("ONWATCH_RENEWAL_PROBE_CHILD", "1")
			if tc.fail {
				t.Setenv("ONWATCH_RENEWAL_PROBE_FAIL", "1")
			}
			expiry := time.Now().Add(-time.Hour)
			if tc.healthy {
				expiry = time.Now().Add(time.Hour)
			}
			data, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "original-fixture", "expiresAt": expiry.UnixMilli()}})
			path := filepath.Join(dir, ".credentials.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			r := &Runtime{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			_, err := r.anthropicAccessToken(ctx)
			if (err == nil) != (tc.wantRenew || tc.healthy) {
				t.Fatalf("unexpected error: %v", err)
			}
			after, _ := os.ReadFile(path)
			if tc.wantRenew {
				if api.DetectAnthropicTokenInDir(dir) != "renewed-fixture" {
					t.Fatal("expired login did not renew")
				}
			} else if string(after) != string(data) {
				t.Fatal("unexpected credential mutation")
			}
			if tc.fail && !strings.Contains(err.Error(), "renew dedicated Anthropic login") {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectorRejectsNoOpRenewal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_TOKEN_ROTATION", "on")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_CLAUDE_PATH", executable)
	t.Setenv("ONWATCH_RENEWAL_PROBE_CHILD", "1")
	t.Setenv("ONWATCH_RENEWAL_PROBE_FAIL", "noop")
	r := &Runtime{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if token, err := r.anthropicAccessToken(context.Background()); err == nil || token != "" {
		t.Fatalf("no-op renewal accepted: %q, %v", token, err)
	}
}
