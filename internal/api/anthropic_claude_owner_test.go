package api

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestRefreshAnthropicCredentialsWithClaudeUsesIsolatedRestrictedProbe(t *testing.T) {
	var gotExecutable string
	var gotArgs []string
	var gotEnv []string

	err := refreshAnthropicCredentialsWithClaude(
		context.Background(),
		"claude-test-binary",
		`C:\profiles\onwatch`,
		func(_ context.Context, executable string, args, env []string) error {
			gotExecutable = executable
			gotArgs = append([]string(nil), args...)
			gotEnv = append([]string(nil), env...)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("refresh credentials: %v", err)
	}
	if gotExecutable != "claude-test-binary" {
		t.Fatalf("executable = %q, want configured Claude binary", gotExecutable)
	}
	if !slices.Contains(gotArgs, "--no-session-persistence") ||
		!slices.Contains(gotArgs, "--strict-mcp-config") ||
		!slices.Contains(gotArgs, "--tools=") ||
		!slices.Contains(gotArgs, "--setting-sources=") {
		t.Fatalf("Claude refresh arguments are not restricted: %q", gotArgs)
	}

	foundConfigDir := false
	for _, entry := range gotEnv {
		if strings.EqualFold(entry, `CLAUDE_CONFIG_DIR=C:\profiles\onwatch`) {
			foundConfigDir = true
			break
		}
	}
	if !foundConfigDir {
		t.Fatalf("CLAUDE_CONFIG_DIR missing from command environment: %q", gotEnv)
	}
}

func TestRefreshAnthropicCredentialsWithClaudeRejectsSharedDefaultProfile(t *testing.T) {
	called := false
	err := refreshAnthropicCredentialsWithClaude(
		context.Background(),
		"claude-test-binary",
		"",
		func(context.Context, string, []string, []string) error {
			called = true
			return nil
		},
	)
	if err == nil {
		t.Fatal("refresh credentials succeeded without an isolated CLAUDE_CONFIG_DIR")
	}
	if called {
		t.Fatal("Claude process started without an isolated profile")
	}
}
