package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

type pollHealthCall struct {
	provider  string
	accountID string
	category  string
	message   string
}

type pollHealthNotifierSpy struct {
	mu              sync.Mutex
	registered      []pollHealthCall
	unregistered    []pollHealthCall
	failures        []pollHealthCall
	successes       []pollHealthCall
	skips           []pollHealthCall
	checks          []notify.QuotaStatus
	directAuthCalls int
}

func (s *pollHealthNotifierSpy) Check(status notify.QuotaStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = append(s.checks, status)
}

func (s *pollHealthNotifierSpy) RegisterPoller(provider, accountID string, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = append(s.registered, pollHealthCall{provider: provider, accountID: accountID})
}

func (s *pollHealthNotifierSpy) UnregisterPoller(provider, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregistered = append(s.unregistered, pollHealthCall{provider: provider, accountID: accountID})
}

func (s *pollHealthNotifierSpy) RecordPollFailure(provider, accountID, category, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, pollHealthCall{
		provider: provider, accountID: accountID, category: category, message: message,
	})
}

func (s *pollHealthNotifierSpy) RecordPollSuccess(provider, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successes = append(s.successes, pollHealthCall{provider: provider, accountID: accountID})
}

func (s *pollHealthNotifierSpy) RecordPollSkipped(provider, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skips = append(s.skips, pollHealthCall{provider: provider, accountID: accountID})
}

func (s *pollHealthNotifierSpy) SendAuthErrorNotification(notify.AuthErrorAlert) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directAuthCalls++
	return true
}

func (s *pollHealthNotifierSpy) snapshot() pollHealthNotifierSpy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return pollHealthNotifierSpy{
		registered:      append([]pollHealthCall(nil), s.registered...),
		unregistered:    append([]pollHealthCall(nil), s.unregistered...),
		failures:        append([]pollHealthCall(nil), s.failures...),
		successes:       append([]pollHealthCall(nil), s.successes...),
		skips:           append([]pollHealthCall(nil), s.skips...),
		checks:          append([]notify.QuotaStatus(nil), s.checks...),
		directAuthCalls: s.directAuthCalls,
	}
}

func newAgentTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func codexPollHealthAgent(t *testing.T, handler http.HandlerFunc, accountID int64) (*CodexAgent, *store.Store, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	st := newAgentTestStore(t)
	logger := slog.Default()
	client := api.NewCodexClient("token", logger, api.WithCodexBaseURL(server.URL))
	agent := NewCodexAgentWithAccount(client, st, nil, time.Hour, logger, nil, accountID)
	return agent, st, server
}

func anthropicPollHealthAgent(t *testing.T, handler http.HandlerFunc) (*AnthropicAgent, *store.Store, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	st := newAgentTestStore(t)
	logger := slog.Default()
	client := api.NewAnthropicClient("token", logger, api.WithAnthropicBaseURL(server.URL))
	agent := NewAnthropicAgent(client, st, nil, time.Hour, logger, nil)
	agent.SetCCDetectionEnabled(false)
	return agent, st, server
}

func TestCodexAgentPollHealthRegistersSkipsAndUnregisters(t *testing.T) {
	agent, _, _ := codexPollHealthAgent(t, func(http.ResponseWriter, *http.Request) {}, 42)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.interval = 10 * time.Millisecond
	agent.SetPollingCheck(func() bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for len(spy.snapshot().skips) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := spy.snapshot()
	if len(got.registered) != 1 || got.registered[0].provider != "codex" || got.registered[0].accountID != "42" {
		t.Fatalf("registration = %#v, want codex/42", got.registered)
	}
	if len(got.unregistered) != 1 || got.unregistered[0].accountID != "42" {
		t.Fatalf("unregistration = %#v, want codex/42", got.unregistered)
	}
	if len(got.skips) == 0 || len(got.failures) != 0 {
		t.Fatalf("skips=%d failures=%d, want skip without failure", len(got.skips), len(got.failures))
	}
}

func TestCodexAgentPollHealthReportsOneTerminalFailureAfterRetry(t *testing.T) {
	var requests atomic.Int32
	agent, _, _ := codexPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}, 7)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "still-bad" })

	agent.poll(context.Background())

	got := spy.snapshot()
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want initial request plus one retry", requests.Load())
	}
	if len(got.failures) != 1 || len(got.successes) != 0 {
		t.Fatalf("failures=%d successes=%d, want one final failure", len(got.failures), len(got.successes))
	}
	if got.failures[0].provider != "codex" || got.failures[0].accountID != "7" ||
		got.failures[0].category != "authentication" ||
		!strings.Contains(got.failures[0].message, "codex auth") {
		t.Fatalf("failure = %#v", got.failures[0])
	}
}

func TestCodexAgentPollHealthRetrySuccessAndRecovery(t *testing.T) {
	var requests atomic.Int32
	fail := atomic.Bool{}
	fail.Store(true)
	agent, _, _ := codexPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":25,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
	}, 9)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "token" })

	agent.poll(context.Background())
	fail.Store(false)
	requests.Store(0)
	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 1 || len(got.successes) != 1 {
		t.Fatalf("failures=%d successes=%d, want failure then recovery success", len(got.failures), len(got.successes))
	}
	if requests.Load() != 1 {
		t.Fatalf("recovery requests = %d, want one successful request", requests.Load())
	}
}

func TestCodexAgentPollHealthSuccessfulRetryReportsSuccessOnly(t *testing.T) {
	var requests atomic.Int32
	agent, _, _ := codexPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":25,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
	}, 11)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "fresh" })

	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 0 || len(got.successes) != 1 {
		t.Fatalf("failures=%d successes=%d, want retry to report success only", len(got.failures), len(got.successes))
	}
}

func TestCodexAgentPollHealthStoreFailureIsFinalFailure(t *testing.T) {
	agent, st, _ := codexPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":25,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
	}, 13)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 1 || got.failures[0].category != "storage" || len(got.successes) != 0 {
		t.Fatalf("failures=%#v successes=%#v", got.failures, got.successes)
	}
}

func TestCodexAgentPollHealthCancellationSkips(t *testing.T) {
	started := make(chan struct{})
	agent, _, _ := codexPollHealthAgent(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}, 15)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		agent.poll(ctx)
		close(done)
	}()
	<-started
	cancel()
	<-done

	got := spy.snapshot()
	if len(got.skips) != 1 || len(got.failures) != 0 {
		t.Fatalf("skips=%d failures=%d", len(got.skips), len(got.failures))
	}
}

func TestCodexAgentAuthPauseDoesNotSendDirectNotification(t *testing.T) {
	agent, _, _ := codexPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, 17)
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "bad" })

	for range maxCodexAuthFailures {
		agent.poll(context.Background())
	}
	agent.poll(context.Background())

	got := spy.snapshot()
	if !agent.authPaused {
		t.Fatal("expected auth pause")
	}
	if got.directAuthCalls != 0 {
		t.Fatalf("direct auth notifications = %d, want zero", got.directAuthCalls)
	}
	if len(got.failures) != maxCodexAuthFailures {
		t.Fatalf("poll health failures = %d, want %d", len(got.failures), maxCodexAuthFailures)
	}
	if len(got.skips) != 0 {
		t.Fatalf("paused poll skips = %d, want no outcome so missed-poll supervision can advance", len(got.skips))
	}
}

func TestAnthropicAgentPollHealthRegistersSkipsAndUnregisters(t *testing.T) {
	agent, _, _ := anthropicPollHealthAgent(t, func(http.ResponseWriter, *http.Request) {})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.interval = 10 * time.Millisecond
	agent.SetPollingCheck(func() bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for len(spy.snapshot().skips) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := spy.snapshot()
	if len(got.registered) != 1 || got.registered[0].provider != "anthropic" || got.registered[0].accountID != "default" {
		t.Fatalf("registration = %#v, want anthropic/default", got.registered)
	}
	if len(got.unregistered) != 1 || got.unregistered[0].accountID != "default" {
		t.Fatalf("unregistration = %#v, want anthropic/default", got.unregistered)
	}
	if len(got.skips) == 0 || len(got.failures) != 0 {
		t.Fatalf("skips=%d failures=%d, want skip without failure", len(got.skips), len(got.failures))
	}
}

func TestAnthropicAgentPollHealthReportsOneTerminalFailureAfterRetry(t *testing.T) {
	var requests atomic.Int32
	agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "still-bad" })

	agent.poll(context.Background())

	got := spy.snapshot()
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want initial request plus one retry", requests.Load())
	}
	if len(got.failures) != 1 || len(got.successes) != 0 {
		t.Fatalf("failures=%d successes=%d, want one final failure", len(got.failures), len(got.successes))
	}
	if got.failures[0].category != "authentication" ||
		!strings.Contains(got.failures[0].message, "claude auth") {
		t.Fatalf("failure = %#v", got.failures[0])
	}
}

func TestAnthropicAgentPollHealthSuccessfulRetryReportsSuccessOnly(t *testing.T) {
	var requests atomic.Int32
	agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse(30, 20))
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "fresh" })

	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 0 || len(got.successes) != 1 {
		t.Fatalf("failures=%d successes=%d, want retry to report success only", len(got.failures), len(got.successes))
	}
}

func TestAnthropicAgentPollHealthReportsRecoverySuccess(t *testing.T) {
	fail := atomic.Bool{}
	fail.Store(true)
	agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse(30, 20))
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "token" })

	agent.poll(context.Background())
	fail.Store(false)
	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 1 || len(got.successes) != 1 {
		t.Fatalf("failures=%d successes=%d, want failure then recovery success", len(got.failures), len(got.successes))
	}
}

func TestAnthropicAgentPollHealthStatuslineSuccessWinsOverOptionalHybridFailure(t *testing.T) {
	agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	path := filepath.Join(t.TempDir(), "statusline.json")
	payload := `{"rate_limits":{"five_hour":{"used_percentage":42.5,"resets_at":1766000000}}}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write statusline: %v", err)
	}
	agent.statuslinePath = path
	agent.statuslineStaleness = time.Minute
	agent.apiPollCycleInterval = 1

	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.successes) != 1 || len(got.failures) != 0 {
		t.Fatalf("successes=%d failures=%d, usable statusline must win", len(got.successes), len(got.failures))
	}
}

func TestAnthropicAgentPollHealthStoreFailureIsFinalFailure(t *testing.T) {
	agent, st, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicResponse(30, 20))
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	agent.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 1 || got.failures[0].category != "storage" || len(got.successes) != 0 {
		t.Fatalf("failures=%#v successes=%#v", got.failures, got.successes)
	}
}

func TestAnthropicAgentPollHealthRateLimitAndMissingCredentialsAreCategorized(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		spy := &pollHealthNotifierSpy{}
		agent.notifier = spy
		agent.poll(context.Background())
		got := spy.snapshot()
		if len(got.failures) != 1 || got.failures[0].category != "rate_limit" {
			t.Fatalf("failures = %#v", got.failures)
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		agent, _, _ := anthropicPollHealthAgent(t, func(http.ResponseWriter, *http.Request) {})
		spy := &pollHealthNotifierSpy{}
		agent.notifier = spy
		agent.client.SetToken("")
		agent.poll(context.Background())
		got := spy.snapshot()
		if len(got.failures) != 1 || got.failures[0].category != "missing_credentials" ||
			!strings.Contains(got.failures[0].message, "claude auth") {
			t.Fatalf("failures = %#v", got.failures)
		}
	})
}

func TestAnthropicAgentPollHealthCancellationSkips(t *testing.T) {
	started := make(chan struct{})
	agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		agent.poll(ctx)
		close(done)
	}()
	<-started
	cancel()
	<-done

	got := spy.snapshot()
	if len(got.skips) != 1 || len(got.failures) != 0 {
		t.Fatalf("skips=%d failures=%d", len(got.skips), len(got.failures))
	}
}

func TestAnthropicAgentAuthPauseDoesNotSendDirectNotification(t *testing.T) {
	agent, _, _ := anthropicPollHealthAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	spy := &pollHealthNotifierSpy{}
	agent.notifier = spy
	agent.SetTokenRefresh(func() string { return "bad" })

	for range maxAuthFailures {
		agent.poll(context.Background())
	}
	agent.poll(context.Background())

	got := spy.snapshot()
	if !agent.authPaused {
		t.Fatal("expected auth pause")
	}
	if got.directAuthCalls != 0 {
		t.Fatalf("direct auth notifications = %d, want zero", got.directAuthCalls)
	}
	if len(got.failures) != maxAuthFailures {
		t.Fatalf("poll health failures = %d, want %d", len(got.failures), maxAuthFailures)
	}
	if len(got.skips) != 0 {
		t.Fatalf("paused poll skips = %d, want no outcome so missed-poll supervision can advance", len(got.skips))
	}
}

func TestAnthropicAgentInvalidGrantAllowsMonitorEscalationPastAuthPause(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"rate_limited"}`)
	}))
	defer apiServer.Close()
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token revoked"}`)
	}))
	defer oauthServer.Close()

	api.SetOAuthURLForTest(oauthServer.URL)
	defer api.SetOAuthURLForTest(api.AnthropicOAuthTokenURL)

	type monitoredAgent struct {
		name      string
		threshold int
		store     *store.Store
		notifier  *notify.NotificationEngine
		cancel    context.CancelFunc
		done      chan error
	}
	cases := []struct {
		name      string
		threshold int
	}{
		{name: "default threshold", threshold: 3},
		{name: "configured threshold above provider pause count", threshold: 5},
	}
	monitored := make([]monitoredAgent, 0, len(cases))
	for _, testCase := range cases {
		st := newAgentTestStore(t)
		settings := fmt.Sprintf(
			`{"notify_auth_error":true,"notify_poll_failure":true,"poll_failure_threshold":%d,"channels":{"email":false,"push":false,"discord":false}}`,
			testCase.threshold,
		)
		if err := st.SetSetting("notifications", settings); err != nil {
			t.Fatalf("%s SetSetting: %v", testCase.name, err)
		}
		notifier := notify.New(st, slog.Default())
		if err := notifier.Reload(); err != nil {
			t.Fatalf("%s notifier.Reload: %v", testCase.name, err)
		}
		client := api.NewAnthropicClient("token", slog.Default(),
			api.WithAnthropicBaseURL(apiServer.URL))
		agent := NewAnthropicAgent(client, st, nil, 200*time.Millisecond, slog.Default(), nil)
		agent.SetNotifier(notifier)
		agent.SetCCDetectionEnabled(false)
		agent.SetCredentialsRefresh(func() *api.AnthropicCredentials {
			return &api.AnthropicCredentials{
				AccessToken:  "token",
				RefreshToken: "revoked",
				ExpiresIn:    time.Hour,
				ExpiresAt:    time.Now().Add(time.Hour),
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- agent.Run(ctx) }()
		monitored = append(monitored, monitoredAgent{
			name: testCase.name, threshold: testCase.threshold,
			store: st, notifier: notifier, cancel: cancel, done: done,
		})
	}
	defer func() {
		for _, item := range monitored {
			item.cancel()
		}
		for _, item := range monitored {
			if err := <-item.done; err != nil {
				t.Errorf("%s Run: %v", item.name, err)
			}
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for _, item := range monitored {
		for {
			state, err := item.store.GetPollHealthState("anthropic", "default")
			if err != nil {
				t.Fatalf("%s GetPollHealthState: %v", item.name, err)
			}
			if state != nil && state.ConsecutiveFailures == 1 && state.LastErrorCategory == "authentication" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not record immediate invalid_grant failure: %#v", item.name, state)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// The monitor's scheduling grace is ten seconds. Waiting four extra poll
	// intervals lets the configured threshold of five advance past the
	// provider's internal auth pause without another provider request.
	time.Sleep(10*time.Second + 5*200*time.Millisecond)
	for _, item := range monitored {
		item.notifier.EvaluatePollHealth()
		state, err := item.store.GetPollHealthState("anthropic", "default")
		if err != nil {
			t.Fatalf("%s GetPollHealthState after evaluation: %v", item.name, err)
		}
		if state == nil || state.ConsecutiveFailures < item.threshold {
			t.Fatalf("%s failure count = %#v, want at least %d", item.name, state, item.threshold)
		}
		if state.LastExternalAttemptAt == nil {
			t.Fatalf("%s did not reach external escalation threshold: %#v", item.name, state)
		}
		alerts, err := item.store.GetActiveSystemAlerts()
		if err != nil {
			t.Fatalf("%s GetActiveSystemAlerts: %v", item.name, err)
		}
		for _, alert := range alerts {
			if alert.AlertType == "auth_error" || alert.AlertType == "token_refresh_failed" {
				t.Fatalf("%s created duplicate legacy auth alert: %#v", item.name, alert)
			}
		}
	}
}
