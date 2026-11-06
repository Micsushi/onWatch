package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestClaudeRefreshResultProcess(t *testing.T) {
	mode := os.Getenv("ONWATCH_CLAUDE_RESULT_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "hold-pipes" {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}
	if mode == "spawn" {
		child := exec.Command(os.Args[0], "-test.run=^TestClaudeRefreshResultProcess$")
		child.Env = append(withoutEnvironmentVariable(os.Environ(), "ONWATCH_CLAUDE_RESULT_FIXTURE"), "ONWATCH_CLAUDE_RESULT_FIXTURE=hold-pipes")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("ONWATCH_CLAUDE_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
		os.Exit(0)
	}
	fmt.Println(mode)
	os.Exit(0)
}

func TestClaudeRefreshRejectsReportedFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		output    string
		wantError bool
	}{
		{`{"is_error":false,"result":"hi"}`, false},
		{`{"is_error":true,"result":"OAuth session expired"}`, true},
	} {
		t.Setenv("ONWATCH_CLAUDE_RESULT_FIXTURE", tc.output)
		err := runClaudeCredentialRefresh(context.Background(), executable, []string{"-test.run=^TestClaudeRefreshResultProcess$"}, os.Environ())
		if (err != nil) != tc.wantError {
			t.Fatalf("result %s: %v", tc.output, err)
		}
		if tc.wantError && !strings.Contains(err.Error(), "OAuth session expired") {
			t.Fatalf("missing reason: %v", err)
		}
	}
}

func TestClaudeRefreshBoundsInheritedPipes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONWATCH_CLAUDE_RESULT_FIXTURE", "spawn")
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("ONWATCH_CLAUDE_CHILD_PID", pidFile)
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			return
		}
		if child, err := os.FindProcess(pid); err == nil {
			_, _ = child.Wait()
		}
	})
	err = runClaudeCredentialRefresh(context.Background(), executable, []string{"-test.run=^TestClaudeRefreshResultProcess$"}, os.Environ())
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected pipe timeout, got %v", err)
	}
}

func TestClaudeRefreshRejectsExplicitSharedProfile(t *testing.T) {
	for _, dir := range []string{DefaultClaudeConfigDir(), filepath.Join(DefaultClaudeConfigDir(), "..", ".claude")} {
		err := refreshAnthropicCredentialsWithClaude(context.Background(), "unused", dir, func(context.Context, string, []string, []string) error { t.Fatal("shared profile invoked"); return nil })
		if !errors.Is(err, ErrAnthropicIsolatedProfileRequired) {
			t.Fatalf("expected shared-profile rejection, got %v", err)
		}
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
