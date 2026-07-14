package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

type runningAgent struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// AgentRunner runs a provider polling loop until context cancellation.
type AgentRunner interface {
	Run(ctx context.Context) error
}

// RunnerFactory builds a fresh AgentRunner instance for a provider.
// A factory can return an error when the provider is not currently configurable.
type RunnerFactory func() (AgentRunner, error)

// AgentManager manages dynamic provider agent start/stop lifecycle.
type AgentManager struct {
	mu        sync.RWMutex
	factories map[string]RunnerFactory
	running   map[string]*runningAgent
	logger    *slog.Logger
}

// NewAgentManager creates a new manager.
func NewAgentManager(logger *slog.Logger) *AgentManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentManager{
		factories: make(map[string]RunnerFactory),
		running:   make(map[string]*runningAgent),
		logger:    logger,
	}
}

// RegisterFactory registers or replaces the runner factory for a provider key.
func (m *AgentManager) RegisterFactory(key string, factory RunnerFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factories[key] = factory
}

// RegisteredProviders returns sorted provider keys with registered factories.
func (m *AgentManager) RegisteredProviders() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.factories))
	for key := range m.factories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Start starts a provider agent if not already running.
func (m *AgentManager) Start(key string) error {
	m.mu.Lock()
	if _, running := m.running[key]; running {
		m.mu.Unlock()
		return nil
	}
	factory, ok := m.factories[key]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("agent %s not registered", key)
	}

	runner, err := factory()
	if err != nil {
		return err
	}
	if runner == nil {
		return fmt.Errorf("agent %s factory returned nil runner", key)
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &runningAgent{cancel: cancel, done: make(chan struct{})}

	m.mu.Lock()
	if _, running := m.running[key]; running {
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.running[key] = run
	m.mu.Unlock()

	go func() {
		defer close(run.done)
		m.logger.Info("Starting agent", "provider", key)
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			m.logger.Error("Agent error", "provider", key, "error", err)
		}
		m.mu.Lock()
		if m.running[key] == run {
			delete(m.running, key)
		}
		m.mu.Unlock()
	}()

	return nil
}

// Stop cancels the running provider agent, if present.
func (m *AgentManager) Stop(key string) {
	m.mu.Lock()
	run, running := m.running[key]
	if running {
		delete(m.running, key)
	}
	m.mu.Unlock()
	if running {
		run.cancel()
		<-run.done
		m.logger.Info("Stopped agent", "provider", key)
	}
}

// StopAll stops all running providers.
func (m *AgentManager) StopAll() {
	m.mu.Lock()
	runs := make([]*runningAgent, 0, len(m.running))
	for key, run := range m.running {
		delete(m.running, key)
		m.logger.Info("Stopped agent", "provider", key)
		runs = append(runs, run)
	}
	m.mu.Unlock()

	for _, run := range runs {
		run.cancel()
	}
	for _, run := range runs {
		<-run.done
	}
}

// IsRunning returns whether a provider is currently running.
func (m *AgentManager) IsRunning(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, running := m.running[key]
	return running
}

// TriggerPoll restarts a running agent, causing an immediate poll cycle.
// Returns true if the agent was running and was restarted.
func (m *AgentManager) TriggerPoll(key string) bool {
	m.mu.RLock()
	_, running := m.running[key]
	m.mu.RUnlock()
	if !running {
		return false
	}
	m.Stop(key)
	return m.Start(key) == nil
}

// TriggerPollAll restarts all currently running agents.
func (m *AgentManager) TriggerPollAll() {
	m.mu.RLock()
	keys := make([]string, 0, len(m.running))
	for key := range m.running {
		keys = append(keys, key)
	}
	m.mu.RUnlock()
	for _, key := range keys {
		m.TriggerPoll(key)
	}
}
