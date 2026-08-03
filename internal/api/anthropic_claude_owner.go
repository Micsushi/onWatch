package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	ErrAnthropicIsolatedProfileRequired = errors.New("anthropic: isolated CLAUDE_CONFIG_DIR is required")
	ErrClaudeExecutableNotFound         = errors.New("anthropic: Claude executable not found")
)

type claudeCredentialRefreshRunner func(context.Context, string, []string, []string) error

var claudeCredentialRefreshArgs = []string{
	"-p",
	"Reply with exactly: hi",
	"--model",
	"haiku",
	"--max-turns",
	"1",
	"--no-session-persistence",
	"--no-chrome",
	"--tools=",
	"--disable-slash-commands",
	"--strict-mcp-config",
	"--setting-sources=",
	"--system-prompt",
	"You are a credential refresh probe. Do not use tools or external context. Return only the requested literal text.",
	"--output-format",
	"json",
}

// RefreshAnthropicCredentialsWithClaude asks the official Claude executable to
// rotate an expired token in a dedicated onWatch profile. It never runs against
// Claude's shared default profile.
func RefreshAnthropicCredentialsWithClaude(ctx context.Context, executable, configDir string) error {
	resolved, err := resolveClaudeExecutable(executable)
	if err != nil {
		return err
	}
	return refreshAnthropicCredentialsWithClaude(ctx, resolved, configDir, runClaudeCredentialRefresh)
}

func refreshAnthropicCredentialsWithClaude(
	ctx context.Context,
	executable string,
	configDir string,
	run claudeCredentialRefreshRunner,
) error {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return ErrAnthropicIsolatedProfileRequired
	}
	if strings.TrimSpace(executable) == "" {
		return ErrClaudeExecutableNotFound
	}

	env := withoutEnvironmentVariable(os.Environ(), "CLAUDE_CONFIG_DIR")
	env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	return run(ctx, executable, append([]string(nil), claudeCredentialRefreshArgs...), env)
}

func resolveClaudeExecutable(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured, nil
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			path := filepath.Join(home, ".local", "bin", "claude.exe")
			if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	return "", ErrClaudeExecutableNotFound
}

func runClaudeCredentialRefresh(ctx context.Context, executable string, args, env []string) error {
	refreshCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(refreshCtx, executable, args...)
	cmd.Env = env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if refreshCtx.Err() != nil {
			return fmt.Errorf("anthropic: Claude credential refresh timed out: %w", refreshCtx.Err())
		}
		return fmt.Errorf("anthropic: Claude credential refresh failed: %w", err)
	}
	return nil
}

func withoutEnvironmentVariable(env []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.EqualFold(entry[:min(len(entry), len(prefix))], prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
