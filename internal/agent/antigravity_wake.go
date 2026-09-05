package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// Setting keys for persistent quota wake state.
const (
	AntigravityLastWakeSetting   = "antigravity_last_wake"
	AntigravityWakeConfigSetting = "antigravity_wake_config"
)

// AntigravityWakeConfig holds configuration for Antigravity quota-wake triggering.
type AntigravityWakeConfig struct {
	Enabled     bool          `json:"enabled"`
	Mode        string        `json:"mode"`         // "new-conversation" or "send-message"
	RecipientID string        `json:"recipient_id"` // target conversation ID for send-message
	Model       string        `json:"model"`        // "flash_lite", "flash", "pro", or "" (default)
	Prompt      string        `json:"prompt"`       // prompt content to wake window
	Title       string        `json:"title"`        // title for new conversation or message
	BinaryPath  string        `json:"binary_path"`  // optional explicit executable path
	ProjectDir  string        `json:"project_dir"`  // directory agentapi binds the conversation to
	Cooldown    time.Duration `json:"cooldown"`     // cooldown between wake executions
}

// AntigravityWakeResult records the outcome of a wake execution.
type AntigravityWakeResult struct {
	Success        bool      `json:"success"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Output         string    `json:"output,omitempty"`
	Skipped        bool      `json:"skipped,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	ExecutedAt     time.Time `json:"executed_at"`
	TriggerSource  string    `json:"trigger_source,omitempty"`
}

// WakeSettingStore represents the persistent key-value store used for wake state.
type WakeSettingStore interface {
	GetSetting(key string) (string, error)
	SetSetting(key string, value string) error
}

// AntigravityResetDebouncer suppresses duplicate reset signals within a cooldown window.
type AntigravityResetDebouncer struct {
	mu       sync.Mutex
	cooldown time.Duration
	lastSeen map[string]time.Time
}

// NewAntigravityResetDebouncer creates a reset debouncer with the specified cooldown.
func NewAntigravityResetDebouncer(cooldown time.Duration) *AntigravityResetDebouncer {
	if cooldown <= 0 {
		cooldown = 15 * time.Minute
	}
	return &AntigravityResetDebouncer{
		cooldown: cooldown,
		lastSeen: make(map[string]time.Time),
	}
}

// ShouldFire reports whether an event for key should fire based on cooldown.
func (d *AntigravityResetDebouncer) ShouldFire(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if last, ok := d.lastSeen[key]; ok {
		if now.Sub(last) < d.cooldown {
			return false
		}
	}
	d.lastSeen[key] = now
	return true
}

// AntigravityWakeRunner executes Antigravity CLI commands to wake quota windows upon reset.
type AntigravityWakeRunner struct {
	store        WakeSettingStore
	logger       *slog.Logger
	mu           sync.RWMutex
	cfg          AntigravityWakeConfig
	lastWakes    map[string]time.Time
	lastWakeTime time.Time
	lastResult   *AntigravityWakeResult

	cmdExecutor  func(ctx context.Context, name string, env []string, args ...string) ([]byte, error)
	connResolver func(ctx context.Context) (*api.AntigravityConnection, error)
	pathResolver func(override string) (string, bool, error)
}

// NewAntigravityWakeRunner initializes a new quota-wake runner.
func NewAntigravityWakeRunner(store WakeSettingStore, cfg AntigravityWakeConfig, logger *slog.Logger) *AntigravityWakeRunner {
	if cfg.Mode == "" {
		cfg.Mode = "new-conversation"
	}
	if cfg.Prompt == "" {
		cfg.Prompt = "hi"
	}
	if cfg.Title == "" {
		cfg.Title = "Quota Wake"
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 15 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}

	runner := &AntigravityWakeRunner{
		store:        store,
		logger:       logger,
		cfg:          cfg,
		lastWakes:    make(map[string]time.Time),
		cmdExecutor:  defaultCommandExecutor,
		connResolver: defaultConnectionResolver,
		pathResolver: defaultPathResolver,
	}

	// Restore last result from store if available
	if store != nil {
		if raw, err := store.GetSetting(AntigravityLastWakeSetting); err == nil && raw != "" {
			var saved AntigravityWakeResult
			if err := json.Unmarshal([]byte(raw), &saved); err == nil {
				runner.lastResult = &saved
				runner.lastWakeTime = saved.ExecutedAt
			}
		}
	}

	return runner
}

// IsEnabled reports whether automated quota-wake is currently enabled.
func (r *AntigravityWakeRunner) IsEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Enabled
}

// Config returns a copy of current runner configuration.
func (r *AntigravityWakeRunner) Config() AntigravityWakeConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// UpdateConfig updates runtime configuration.
func (r *AntigravityWakeRunner) UpdateConfig(cfg AntigravityWakeConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cfg.Mode == "" {
		cfg.Mode = "new-conversation"
	}
	if cfg.Prompt == "" {
		cfg.Prompt = "hi"
	}
	if cfg.Title == "" {
		cfg.Title = "Quota Wake"
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 15 * time.Minute
	}
	r.cfg = cfg
}

// LastResult returns the most recent wake execution result.
func (r *AntigravityWakeRunner) LastResult() *AntigravityWakeResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.lastResult == nil {
		return nil
	}
	copy := *r.lastResult
	return &copy
}

// SetExecutor replaces the command executor (used in tests).
func (r *AntigravityWakeRunner) SetExecutor(fn func(ctx context.Context, name string, env []string, args ...string) ([]byte, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmdExecutor = fn
}

// SetPathResolver replaces the binary path resolver (used in tests).
func (r *AntigravityWakeRunner) SetPathResolver(fn func(override string) (string, bool, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pathResolver = fn
}

// OnReset handles quota reset events for a given model.
func (r *AntigravityWakeRunner) OnReset(modelID string) {
	if !r.IsEnabled() {
		return
	}

	groupKey := api.AntigravityQuotaGroupForModel(modelID, "")
	r.mu.Lock()
	cooldown := r.cfg.Cooldown
	if cooldown <= 0 {
		cooldown = 15 * time.Minute
	}
	lastGroup, ok := r.lastWakes[groupKey]
	now := time.Now()
	if ok && now.Sub(lastGroup) < cooldown {
		r.mu.Unlock()
		r.logger.Debug("Antigravity quota wake skipped (group cooldown active)",
			"model", modelID,
			"group", groupKey,
			"elapsed", now.Sub(lastGroup),
		)
		return
	}
	r.lastWakes[groupKey] = now
	r.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if _, err := r.Trigger(ctx, "reset:"+groupKey); err != nil {
			r.logger.Warn("Antigravity quota wake trigger failed", "group", groupKey, "error", err)
		}
	}()
}

// Trigger executes the wake command immediately.
func (r *AntigravityWakeRunner) Trigger(ctx context.Context, triggerSource string) (*AntigravityWakeResult, error) {
	r.mu.Lock()
	cfg := r.cfg
	now := time.Now()

	// Check global cooldown unless forced/manual trigger
	if !strings.HasPrefix(triggerSource, "manual") && !r.lastWakeTime.IsZero() && now.Sub(r.lastWakeTime) < cfg.Cooldown {
		res := &AntigravityWakeResult{
			Success:       true,
			Skipped:       true,
			Reason:        "cooldown active",
			ExecutedAt:    now,
			TriggerSource: triggerSource,
		}
		r.mu.Unlock()
		r.logger.Debug("Antigravity quota wake skipped (global cooldown active)",
			"source", triggerSource,
			"elapsed", now.Sub(r.lastWakeTime),
		)
		return res, nil
	}
	r.mu.Unlock()

	exePath, isDirectLS, err := r.pathResolver(cfg.BinaryPath)
	if err != nil {
		r.logger.Warn("Antigravity binary not found for quota wake", "error", err)
		return nil, fmt.Errorf("resolve antigravity binary: %w", err)
	}

	// Prepare command arguments
	mode := cfg.Mode
	if mode == "send-message" && strings.TrimSpace(cfg.RecipientID) == "" {
		r.logger.Warn("send-message mode specified without recipient ID, falling back to new-conversation")
		mode = "new-conversation"
	}

	var args []string
	if isDirectLS {
		args = append(args, "agentapi")
	}

	switch mode {
	case "send-message":
		args = append(args, "send-message")
		if strings.TrimSpace(cfg.Title) != "" {
			args = append(args, "--title="+cfg.Title)
		}
		args = append(args, cfg.RecipientID, cfg.Prompt)
	default:
		args = append(args, "new-conversation")
		if strings.TrimSpace(cfg.Model) != "" {
			args = append(args, "--model="+cfg.Model)
		}
		if strings.TrimSpace(cfg.Title) != "" {
			args = append(args, "--title="+cfg.Title)
		}
		args = append(args, cfg.Prompt)
	}

	r.logger.Info("Executing Antigravity quota wake",
		"binary", exePath,
		"direct_ls", isDirectLS,
		"mode", mode,
		"source", triggerSource,
	)

	env, envErr := r.wakeEnvironment(ctx)
	if envErr != nil {
		r.logger.Warn("Antigravity quota wake cannot reach the language server", "error", envErr)
		return nil, envErr
	}

	output, err := r.cmdExecutor(ctx, exePath, env, args...)
	execTime := time.Now()
	outputStr := strings.TrimSpace(string(output))

	result := &AntigravityWakeResult{
		ExecutedAt:    execTime,
		TriggerSource: triggerSource,
		Output:        outputStr,
	}

	if err != nil {
		result.Success = false
		result.Reason = err.Error()
		r.mu.Lock()
		r.lastResult = result
		r.mu.Unlock()
		r.persistResult(result)
		return result, fmt.Errorf("execute antigravity %s: %w (output: %s)", mode, err, outputStr)
	}

	result.Success = true
	result.ConversationID = extractConversationID(output)

	r.mu.Lock()
	r.lastWakeTime = execTime
	r.lastResult = result
	r.mu.Unlock()

	r.persistResult(result)

	r.logger.Info("Antigravity quota wake executed successfully",
		"mode", mode,
		"conversation_id", result.ConversationID,
		"source", triggerSource,
	)

	return result, nil
}

func (r *AntigravityWakeRunner) persistResult(res *AntigravityWakeResult) {
	if r.store == nil || res == nil {
		return
	}
	if data, err := json.Marshal(res); err == nil {
		_ = r.store.SetSetting(AntigravityLastWakeSetting, string(data))
	}
}

func defaultCommandExecutor(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	return cmd.CombinedOutput()
}

func defaultConnectionResolver(ctx context.Context) (*api.AntigravityConnection, error) {
	return api.NewAntigravityClient(slog.Default()).Detect(ctx)
}

// SetConnectionResolver replaces language server discovery (used in tests).
func (r *AntigravityWakeRunner) SetConnectionResolver(fn func(ctx context.Context) (*api.AntigravityConnection, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connResolver = fn
}

// wakeEnvironment builds the environment agentapi needs. Without it the command
// fails before reaching a model: it has no idea which language server to talk
// to, and refuses to start a conversation that is not bound to a project.
//
//	ANTIGRAVITY_LS_ADDRESS  - host:port of the running language server
//	ANTIGRAVITY_CSRF_TOKEN  - that server's token, taken from its command line
//	ANTIGRAVITY_PROJECT_ID  - an existing directory to attach the conversation to
func (r *AntigravityWakeRunner) wakeEnvironment(ctx context.Context) ([]string, error) {
	r.mu.Lock()
	resolver := r.connResolver
	projectDir := strings.TrimSpace(r.cfg.ProjectDir)
	r.mu.Unlock()

	if resolver == nil {
		return nil, errors.New("antigravity wake: no language server resolver configured")
	}
	conn, err := resolver(ctx)
	if err != nil {
		return nil, fmt.Errorf("antigravity wake: locate language server: %w", err)
	}
	if conn == nil || conn.Port == 0 {
		return nil, errors.New("antigravity wake: language server is not running")
	}

	projectDir, err = resolveWakeProjectDir(projectDir)
	if err != nil {
		return nil, err
	}

	env := append(os.Environ(),
		fmt.Sprintf("ANTIGRAVITY_LS_ADDRESS=127.0.0.1:%d", conn.Port),
		"ANTIGRAVITY_PROJECT_ID="+projectDir,
	)
	if conn.CSRFToken != "" {
		env = append(env, "ANTIGRAVITY_CSRF_TOKEN="+conn.CSRFToken)
	}
	return env, nil
}

// resolveWakeProjectDir returns a directory that exists. agentapi rejects a
// project id it cannot stat, so fall back to a scratch directory rather than
// failing the wake outright.
func resolveWakeProjectDir(configured string) (string, error) {
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && info.IsDir() {
			return configured, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("antigravity wake: resolve project directory: %w", err)
	}
	fallback := filepath.Join(home, ".onwatch", "antigravity-wake")
	if err := os.MkdirAll(fallback, 0o700); err != nil {
		return "", fmt.Errorf("antigravity wake: create project directory: %w", err)
	}
	return fallback, nil
}

func defaultPathResolver(override string) (string, bool, error) {
	if strings.TrimSpace(override) != "" {
		if _, err := os.Stat(override); err == nil {
			isDirect := isLanguageServerBinary(override)
			return override, isDirect, nil
		}
		return override, false, nil
	}

	// Environment variable overrides
	if env := strings.TrimSpace(os.Getenv("ANTIGRAVITY_AGENTAPI_EXE")); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, isLanguageServerBinary(env), nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("ANTIGRAVITY_AGENTAPI_PATH")); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, isLanguageServerBinary(env), nil
		}
	}

	// Check direct language_server.exe in standard Windows install location
	if runtime.GOOS == "windows" {
		if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
			candidate := filepath.Join(localApp, "Programs", "Antigravity", "resources", "bin", "language_server.exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true, nil
			}
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidate := filepath.Join(home, "AppData", "Local", "Programs", "Antigravity", "resources", "bin", "language_server.exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true, nil
			}
			batCandidate := filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi.bat")
			if _, err := os.Stat(batCandidate); err == nil {
				return batCandidate, false, nil
			}
		}
	} else {
		// Non-Windows standard paths
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidate := filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, false, nil
			}
		}
	}

	// Lookup PATH shim
	if path, err := exec.LookPath("agentapi"); err == nil {
		return path, false, nil
	}
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("agentapi.bat"); err == nil {
			return path, false, nil
		}
	}

	return "", false, errors.New("neither language_server.exe nor agentapi found in standard paths or PATH")
}

func isLanguageServerBinary(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, "language_server")
}

type agentAPIResponse struct {
	Response struct {
		NewConversation struct {
			ConversationID string `json:"conversationId"`
		} `json:"newConversation"`
		SendMessage struct {
			ConversationID string `json:"conversationId"`
		} `json:"sendMessage"`
		ConversationMetadata struct {
			Metadata struct {
				RootConversationID string `json:"rootConversationId"`
			} `json:"metadata"`
		} `json:"conversationMetadata"`
		ConversationID string `json:"conversationId"`
	} `json:"response"`
	ConversationID string `json:"conversationId"`
}

func extractConversationID(output []byte) string {
	var resp agentAPIResponse
	if err := json.Unmarshal(output, &resp); err == nil {
		if resp.Response.NewConversation.ConversationID != "" {
			return resp.Response.NewConversation.ConversationID
		}
		if resp.Response.SendMessage.ConversationID != "" {
			return resp.Response.SendMessage.ConversationID
		}
		if resp.Response.ConversationMetadata.Metadata.RootConversationID != "" {
			return resp.Response.ConversationMetadata.Metadata.RootConversationID
		}
		if resp.Response.ConversationID != "" {
			return resp.Response.ConversationID
		}
		if resp.ConversationID != "" {
			return resp.ConversationID
		}
	}
	return ""
}
