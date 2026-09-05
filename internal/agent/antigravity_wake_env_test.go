package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

func envValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	return value
}

// agentapi cannot reach a model without being told which language server to
// talk to, which token to present, and which project to attach the
// conversation to. Verified against the live server: without these the command
// fails with "ANTIGRAVITY_LS_ADDRESS is not set", then "project_id is required
// when providing project_env_config".
func TestAntigravityWake_PassesLanguageServerEnvironment(t *testing.T) {
	dir := t.TempDir()
	runner := NewAntigravityWakeRunner(nil, AntigravityWakeConfig{
		Enabled:    true,
		Model:      "flash_lite",
		Prompt:     "hi",
		ProjectDir: dir,
	}, slog.Default())
	runner.SetConnectionResolver(func(context.Context) (*api.AntigravityConnection, error) {
		return &api.AntigravityConnection{Port: 61663, CSRFToken: "csrf-abc"}, nil
	})
	runner.SetPathResolver(func(string) (string, bool, error) {
		return `C:\antigravity\language_server.exe`, true, nil
	})

	var gotEnv []string
	var gotArgs []string
	runner.SetExecutor(func(_ context.Context, _ string, env []string, args ...string) ([]byte, error) {
		gotEnv = env
		gotArgs = args
		return []byte(`{"response":{"newConversation":{"conversationId":"c1"}}}`), nil
	})

	if _, err := runner.Trigger(context.Background(), "manual-test"); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	if got := envValue(gotEnv, "ANTIGRAVITY_LS_ADDRESS"); got != "127.0.0.1:61663" {
		t.Fatalf("ANTIGRAVITY_LS_ADDRESS = %q, want the discovered port", got)
	}
	if got := envValue(gotEnv, "ANTIGRAVITY_CSRF_TOKEN"); got != "csrf-abc" {
		t.Fatalf("ANTIGRAVITY_CSRF_TOKEN = %q, want the discovered token", got)
	}
	if got := envValue(gotEnv, "ANTIGRAVITY_PROJECT_ID"); got != dir {
		t.Fatalf("ANTIGRAVITY_PROJECT_ID = %q, want %q", got, dir)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--model=flash_lite") {
		t.Fatalf("args = %v, want the Gemini model selected", gotArgs)
	}
}

// A configured project directory that does not exist is rejected by agentapi,
// so fall back to one that does rather than failing the wake.
func TestAntigravityWake_FallsBackToAnExistingProjectDir(t *testing.T) {
	runner := NewAntigravityWakeRunner(nil, AntigravityWakeConfig{
		Enabled:    true,
		ProjectDir: `Z:\does\not\exist`,
	}, slog.Default())
	runner.SetConnectionResolver(func(context.Context) (*api.AntigravityConnection, error) {
		return &api.AntigravityConnection{Port: 1, CSRFToken: "t"}, nil
	})
	runner.SetPathResolver(func(string) (string, bool, error) { return "ls.exe", true, nil })

	var gotEnv []string
	runner.SetExecutor(func(_ context.Context, _ string, env []string, _ ...string) ([]byte, error) {
		gotEnv = env
		return []byte("{}"), nil
	})

	if _, err := runner.Trigger(context.Background(), "manual-test"); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	got := envValue(gotEnv, "ANTIGRAVITY_PROJECT_ID")
	if got == "" || got == `Z:\does\not\exist` {
		t.Fatalf("ANTIGRAVITY_PROJECT_ID = %q, want an existing fallback directory", got)
	}
}

// With Antigravity closed there is no server to talk to, so the wake must
// report that rather than shelling out to a command that cannot work.
func TestAntigravityWake_FailsWhenLanguageServerIsNotRunning(t *testing.T) {
	runner := NewAntigravityWakeRunner(nil, AntigravityWakeConfig{Enabled: true}, slog.Default())
	runner.SetConnectionResolver(func(context.Context) (*api.AntigravityConnection, error) {
		return nil, errors.New("no listening port found")
	})
	runner.SetPathResolver(func(string) (string, bool, error) { return "ls.exe", true, nil })

	executed := false
	runner.SetExecutor(func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
		executed = true
		return []byte("{}"), nil
	})

	if _, err := runner.Trigger(context.Background(), "manual-test"); err == nil {
		t.Fatal("Trigger returned no error with the language server down")
	}
	if executed {
		t.Fatal("ran the wake command with no language server to talk to")
	}
}
