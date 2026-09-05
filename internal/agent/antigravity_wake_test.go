package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// newTestWakeRunner builds a runner with language server discovery stubbed out,
// so tests exercise the wake logic rather than whatever Antigravity is doing on
// the machine running them.
func newTestWakeRunner(store WakeSettingStore, cfg AntigravityWakeConfig, logger *slog.Logger) *AntigravityWakeRunner {
	runner := NewAntigravityWakeRunner(store, cfg, logger)
	runner.SetConnectionResolver(func(context.Context) (*api.AntigravityConnection, error) {
		return &api.AntigravityConnection{Port: 12345, CSRFToken: "test-csrf"}, nil
	})
	return runner
}

type memoryWakeStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemoryWakeStore() *memoryWakeStore {
	return &memoryWakeStore{data: make(map[string]string)}
}

func (m *memoryWakeStore) GetSetting(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key], nil
}

func (m *memoryWakeStore) SetSetting(key string, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func TestAntigravityResetDebouncer(t *testing.T) {
	cooldown := 50 * time.Millisecond
	debouncer := NewAntigravityResetDebouncer(cooldown)

	if !debouncer.ShouldFire("group1") {
		t.Fatalf("first call for group1 should fire")
	}
	if debouncer.ShouldFire("group1") {
		t.Fatalf("immediate second call for group1 should NOT fire")
	}
	if !debouncer.ShouldFire("group2") {
		t.Fatalf("call for group2 should fire independently")
	}

	time.Sleep(60 * time.Millisecond)
	if !debouncer.ShouldFire("group1") {
		t.Fatalf("call for group1 after cooldown should fire")
	}
}

func TestAntigravityWakeRunnerNewConversation(t *testing.T) {
	store := newMemoryWakeStore()
	cfg := AntigravityWakeConfig{
		Enabled:    true,
		Mode:       "new-conversation",
		Model:      "flash_lite",
		Prompt:     "hi",
		Title:      "Wake Window",
		Cooldown:   100 * time.Millisecond,
		BinaryPath: "C:\\FakePath\\language_server.exe",
	}

	runner := newTestWakeRunner(store, cfg, nil)
	runner.SetPathResolver(func(override string) (string, bool, error) {
		return override, true, nil // true = isDirectLS
	})

	var executedName string
	var executedArgs []string
	runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
		executedName = name
		executedArgs = args
		resp := `{"response":{"newConversation":{"conversationId":"conv-new-12345"}}}`
		return []byte(resp), nil
	})

	res, err := runner.Trigger(context.Background(), "test_trigger")
	if err != nil {
		t.Fatalf("Trigger() failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success=true")
	}
	if res.ConversationID != "conv-new-12345" {
		t.Errorf("expected conversation ID 'conv-new-12345', got %q", res.ConversationID)
	}
	if executedName != "C:\\FakePath\\language_server.exe" {
		t.Errorf("unexpected executable name: %q", executedName)
	}

	// Verify direct language server prepends 'agentapi'
	expectedArgs := []string{"agentapi", "new-conversation", "--model=flash_lite", "--title=Wake Window", "hi"}
	if len(executedArgs) != len(expectedArgs) {
		t.Fatalf("args length mismatch: got %v, want %v", executedArgs, expectedArgs)
	}
	for i := range expectedArgs {
		if executedArgs[i] != expectedArgs[i] {
			t.Errorf("arg[%d] mismatch: got %q, want %q", i, executedArgs[i], expectedArgs[i])
		}
	}

	// Check store persistence
	saved, err := store.GetSetting(AntigravityLastWakeSetting)
	if err != nil || saved == "" {
		t.Fatalf("expected saved result in store, got %q, err: %v", saved, err)
	}
	if !strings.Contains(saved, "conv-new-12345") {
		t.Errorf("saved JSON missing conversation ID: %s", saved)
	}

	// Verify cooldown skips immediate next call
	skippedRes, err := runner.Trigger(context.Background(), "test_trigger_2")
	if err != nil {
		t.Fatalf("second Trigger() returned error: %v", err)
	}
	if !skippedRes.Skipped || skippedRes.Reason != "cooldown active" {
		t.Errorf("expected skipped due to cooldown, got: %+v", skippedRes)
	}

	// Manual trigger should bypass cooldown
	manualRes, err := runner.Trigger(context.Background(), "manual:test")
	if err != nil {
		t.Fatalf("manual Trigger() returned error: %v", err)
	}
	if manualRes.Skipped {
		t.Errorf("manual trigger should not be skipped by cooldown")
	}
}

func TestAntigravityWakeRunnerSendMessage(t *testing.T) {
	store := newMemoryWakeStore()
	cfg := AntigravityWakeConfig{
		Enabled:     true,
		Mode:        "send-message",
		RecipientID: "conv-target-999",
		Prompt:      "resume work",
		Title:       "Wake Ping",
		Cooldown:    100 * time.Millisecond,
		BinaryPath:  "agentapi.bat",
	}

	runner := newTestWakeRunner(store, cfg, nil)
	runner.SetPathResolver(func(override string) (string, bool, error) {
		return override, false, nil // false = batch/shim
	})

	var executedArgs []string
	runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
		executedArgs = args
		resp := `{"response":{"sendMessage":{"conversationId":"conv-target-999"}}}`
		return []byte(resp), nil
	})

	res, err := runner.Trigger(context.Background(), "manual:test")
	if err != nil {
		t.Fatalf("Trigger() failed: %v", err)
	}
	if res.ConversationID != "conv-target-999" {
		t.Errorf("expected conversation ID 'conv-target-999', got %q", res.ConversationID)
	}

	expectedArgs := []string{"send-message", "--title=Wake Ping", "conv-target-999", "resume work"}
	if len(executedArgs) != len(expectedArgs) {
		t.Fatalf("args length mismatch: got %v, want %v", executedArgs, expectedArgs)
	}
	for i := range expectedArgs {
		if executedArgs[i] != expectedArgs[i] {
			t.Errorf("arg[%d] mismatch: got %q, want %q", i, executedArgs[i], expectedArgs[i])
		}
	}
}

func TestAntigravityWakeRunnerSendMessageFallback(t *testing.T) {
	store := newMemoryWakeStore()
	// RecipientID is empty -> should fall back to new-conversation
	cfg := AntigravityWakeConfig{
		Enabled:     true,
		Mode:        "send-message",
		RecipientID: "",
		Prompt:      "fallback prompt",
		Title:       "Wake Title",
		Cooldown:    100 * time.Millisecond,
		BinaryPath:  "agentapi.bat",
	}

	runner := newTestWakeRunner(store, cfg, nil)
	runner.SetPathResolver(func(override string) (string, bool, error) {
		return override, false, nil
	})

	var executedArgs []string
	runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
		executedArgs = args
		return []byte(`{"conversationId":"conv-fallback-1"}`), nil
	})

	res, err := runner.Trigger(context.Background(), "manual:test")
	if err != nil {
		t.Fatalf("Trigger() failed: %v", err)
	}
	if res.ConversationID != "conv-fallback-1" {
		t.Errorf("expected conv-fallback-1, got %q", res.ConversationID)
	}
	if len(executedArgs) == 0 || executedArgs[0] != "new-conversation" {
		t.Errorf("expected fallback to new-conversation, got args: %v", executedArgs)
	}
}

func TestAntigravityWakeRunnerExecutionFailure(t *testing.T) {
	store := newMemoryWakeStore()
	cfg := AntigravityWakeConfig{
		Enabled:  true,
		Mode:     "new-conversation",
		Prompt:   "hi",
		Cooldown: 100 * time.Millisecond,
	}

	runner := newTestWakeRunner(store, cfg, nil)
	runner.SetPathResolver(func(override string) (string, bool, error) {
		return "agentapi", false, nil
	})

	runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
		return []byte("connection refused to language server"), errors.New("exit status 1")
	})

	res, err := runner.Trigger(context.Background(), "manual:test")
	if err == nil {
		t.Fatalf("expected error from failed execution")
	}
	if res.Success {
		t.Errorf("expected res.Success=false")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected error to include process output, got %v", err)
	}

	last := runner.LastResult()
	if last == nil || last.Success {
		t.Errorf("expected recorded failed LastResult, got %+v", last)
	}
}

func TestAntigravityWakeRunnerBinaryNotFound(t *testing.T) {
	runner := newTestWakeRunner(nil, AntigravityWakeConfig{Enabled: true}, nil)
	runner.SetPathResolver(func(override string) (string, bool, error) {
		return "", false, fmt.Errorf("no binary available")
	})

	_, err := runner.Trigger(context.Background(), "manual:test")
	if err == nil || !strings.Contains(err.Error(), "no binary available") {
		t.Fatalf("expected binary resolution error, got %v", err)
	}
}

func TestAntigravityWakeRunnerOnReset(t *testing.T) {
	store := newMemoryWakeStore()
	cfg := AntigravityWakeConfig{
		Enabled:  true,
		Mode:     "new-conversation",
		Prompt:   "hi",
		Cooldown: 50 * time.Millisecond,
	}

	runner := newTestWakeRunner(store, cfg, nil)
	runner.SetPathResolver(func(override string) (string, bool, error) {
		return "agentapi", false, nil
	})

	executed := make(chan string, 5)
	runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
		executed <- strings.Join(args, " ")
		return []byte(`{"conversationId":"conv-reset-1"}`), nil
	})

	// Fire OnReset for model "gemini-2.5-pro"
	runner.OnReset("gemini-2.5-pro")

	select {
	case argStr := <-executed:
		if !strings.Contains(argStr, "new-conversation") {
			t.Errorf("expected new-conversation in args: %s", argStr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for OnReset trigger")
	}

	// Immediate repeat for same group ("gemini-1.5-pro" -> same "antigravity_gemini_pro" group) should be debounced
	runner.OnReset("gemini-1.5-pro")
	select {
	case unexpected := <-executed:
		t.Fatalf("immediate repeat in same group should have been debounced, executed: %s", unexpected)
	case <-time.After(100 * time.Millisecond):
		// Expected: no execution during cooldown
	}
}
