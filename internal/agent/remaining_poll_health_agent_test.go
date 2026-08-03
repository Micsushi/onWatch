package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

type remainingPollHealthHarness struct {
	provider   string
	accountID  string
	spy        *pollHealthNotifierSpy
	store      *store.Store
	poll       func(context.Context)
	run        func(context.Context) error
	setEnabled func(bool)
}

func newRemainingPollHealthHarness(t *testing.T, provider string, fail *atomic.Bool) remainingPollHealthHarness {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			if provider == "antigravity" {
				_, _ = w.Write([]byte(`{"malformed"`))
				return
			}
			http.Error(w, "temporary provider failure", http.StatusServiceUnavailable)
			return
		}

		switch provider {
		case "synthetic":
			_ = json.NewEncoder(w).Encode(testResponse())
		case "zai":
			_, _ = w.Write([]byte(zaiResponse(200000000, 50000000, 1000, 19)))
		case "copilot":
			_ = json.NewEncoder(w).Encode(copilotTestResponse())
		case "antigravity":
			_, _ = w.Write([]byte(antigravityTestResponse()))
		case "minimax":
			_, _ = fmt.Fprint(w, `{
				"base_resp":{"status_code":0,"status_msg":"success"},
				"model_remains":[{
					"model_name":"MiniMax-M2",
					"start_time":1771218000000,
					"end_time":1771236000000,
					"remains_time":205310,
					"current_interval_total_count":15000,
					"current_interval_usage_count":14077
				}]
			}`)
		case "openrouter":
			_, _ = fmt.Fprint(w, `{"data":{
				"label":"test","usage":4.5,"usage_daily":1.25,
				"usage_weekly":3.5,"usage_monthly":4.5,
				"limit":10,"limit_remaining":5.5,"is_free_tier":false,
				"rate_limit":{"requests":200,"interval":"10s"}
			}}`)
		case "gemini":
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_, _ = fmt.Fprint(w, `{"tier":"standard","cloudAICompanionProject":"test-project"}`)
			case "/v1internal:retrieveUserQuota":
				_, _ = fmt.Fprint(w, `{"buckets":[{
					"modelId":"gemini-2.5-pro",
					"remainingFraction":0.75,
					"resetTime":"2026-08-01T00:00:00Z"
				}]}`)
			default:
				http.NotFound(w, r)
			}
		case "cursor":
			switch r.URL.Path {
			case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
				_, _ = fmt.Fprint(w, `{
					"billingCycleStart":"1768399334000",
					"billingCycleEnd":"1771077734000",
					"planUsage":{"totalSpend":5000,"remaining":35000,"limit":40000,"totalPercentUsed":12.5},
					"enabled":true
				}`)
			case "/aiserver.v1.DashboardService/GetPlanInfo":
				_, _ = fmt.Fprint(w, `{"planInfo":{"planName":"Pro","includedAmountCents":2000,"price":"$20/mo"}}`)
			case "/aiserver.v1.DashboardService/GetCreditGrantsBalance":
				_, _ = fmt.Fprint(w, `{"hasCreditGrants":false,"totalCents":"0","usedCents":"0"}`)
			default:
				http.NotFound(w, r)
			}
		default:
			http.Error(w, "unsupported provider", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := slog.Default()
	spy := &pollHealthNotifierSpy{}
	enabled := atomic.Bool{}
	enabled.Store(true)

	h := remainingPollHealthHarness{
		provider:   provider,
		accountID:  "default",
		spy:        spy,
		store:      st,
		setEnabled: enabled.Store,
	}
	pollingCheck := func() bool { return enabled.Load() }

	switch provider {
	case "synthetic":
		client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
		ag := New(client, st, tracker.New(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	case "zai":
		client := api.NewZaiClient("test-key", logger, api.WithZaiBaseURL(server.URL))
		ag := NewZaiAgent(client, st, tracker.NewZaiTracker(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	case "copilot":
		client := api.NewCopilotClient("test-token", logger, api.WithCopilotBaseURL(server.URL))
		ag := NewCopilotAgent(client, st, tracker.NewCopilotTracker(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	case "antigravity":
		conn := &api.AntigravityConnection{BaseURL: server.URL, CSRFToken: "test", Protocol: "http"}
		client := api.NewAntigravityClient(logger, api.WithAntigravityConnection(conn))
		ag := NewAntigravityAgent(client, st, tracker.NewAntigravityTracker(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	case "minimax":
		client := api.NewMiniMaxClient("test-token", logger, api.WithMiniMaxBaseURL(server.URL))
		ag := NewMiniMaxAgentWithAccount(client, st, tracker.NewMiniMaxTracker(st, logger), time.Hour, logger, nil, 42)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.accountID = "42"
		h.poll, h.run = ag.poll, ag.Run
	case "openrouter":
		client := api.NewOpenRouterClient("test-token", logger, api.WithOpenRouterBaseURL(server.URL))
		ag := NewOpenRouterAgent(client, st, tracker.NewOpenRouterTracker(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	case "gemini":
		client := api.NewGeminiClient("test-token", logger, api.WithGeminiBaseURL(server.URL))
		ag := NewGeminiAgent(client, st, tracker.NewGeminiTracker(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	case "cursor":
		client := api.NewCursorClient("test-token", logger, api.WithCursorBaseURL(server.URL))
		ag := NewCursorAgent(client, st, tracker.NewCursorTracker(st, logger), time.Hour, logger, nil)
		ag.notifier = spy
		ag.SetPollingCheck(pollingCheck)
		h.poll, h.run = ag.poll, ag.Run
	default:
		t.Fatalf("unsupported provider %q", provider)
	}

	return h
}

func TestRemainingProviderPollHealthFailureAndRecovery(t *testing.T) {
	for _, provider := range []string{
		"synthetic", "zai", "copilot", "minimax", "openrouter", "gemini", "cursor",
	} {
		t.Run(provider, func(t *testing.T) {
			var fail atomic.Bool
			fail.Store(true)
			h := newRemainingPollHealthHarness(t, provider, &fail)

			h.poll(context.Background())
			fail.Store(false)
			h.poll(context.Background())

			got := h.spy.snapshot()
			if len(got.failures) != 1 || len(got.successes) != 1 {
				t.Fatalf("failures=%#v successes=%#v, want one failure then one success", got.failures, got.successes)
			}
			if got.failures[0].provider != h.provider || got.failures[0].accountID != h.accountID {
				t.Fatalf("failure identity = %#v, want %s/%s", got.failures[0], h.provider, h.accountID)
			}
			if got.failures[0].category != "provider_request" {
				t.Fatalf("failure category = %q, want provider_request", got.failures[0].category)
			}
		})
	}
}

func TestRemainingProviderPollHealthRegistersAndSkipsDisabled(t *testing.T) {
	for _, provider := range []string{
		"synthetic", "zai", "copilot", "minimax", "openrouter", "gemini", "cursor",
	} {
		t.Run(provider, func(t *testing.T) {
			var fail atomic.Bool
			h := newRemainingPollHealthHarness(t, provider, &fail)
			h.setEnabled(false)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- h.run(ctx) }()
			deadline := time.Now().Add(time.Second)
			for len(h.spy.snapshot().skips) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run: %v", err)
			}

			got := h.spy.snapshot()
			if len(got.registered) != 1 ||
				got.registered[0].provider != h.provider ||
				got.registered[0].accountID != h.accountID {
				t.Fatalf("registration = %#v, want %s/%s", got.registered, h.provider, h.accountID)
			}
			if len(got.unregistered) != 1 ||
				got.unregistered[0].provider != h.provider ||
				got.unregistered[0].accountID != h.accountID {
				t.Fatalf("unregistration = %#v, want %s/%s", got.unregistered, h.provider, h.accountID)
			}
			if len(got.skips) == 0 || len(got.failures) != 0 {
				t.Fatalf("skips=%d failures=%d, want disabled skip only", len(got.skips), len(got.failures))
			}
		})
	}
}

func TestRemainingProviderPollHealthCanceledPollIsSkipped(t *testing.T) {
	for _, provider := range []string{
		"synthetic", "zai", "copilot", "minimax", "openrouter", "gemini", "cursor",
	} {
		t.Run(provider, func(t *testing.T) {
			var fail atomic.Bool
			h := newRemainingPollHealthHarness(t, provider, &fail)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			h.poll(ctx)

			got := h.spy.snapshot()
			if len(got.skips) != 1 || len(got.failures) != 0 || len(got.successes) != 0 {
				t.Fatalf("skips=%d failures=%d successes=%d, want one skip", len(got.skips), len(got.failures), len(got.successes))
			}
		})
	}
}

func TestRemainingProviderPollHealthStoreFailurePreservesExistingDownstreamFlow(t *testing.T) {
	preservesDownstream := map[string]bool{
		"copilot": true,
		"minimax": true,
		"cursor":  true,
	}
	for _, provider := range []string{
		"synthetic", "zai", "copilot", "minimax", "openrouter", "gemini", "cursor",
	} {
		t.Run(provider, func(t *testing.T) {
			var fail atomic.Bool
			h := newRemainingPollHealthHarness(t, provider, &fail)
			if err := h.store.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			h.poll(context.Background())

			got := h.spy.snapshot()
			if len(got.failures) != 1 || got.failures[0].category != "storage" || len(got.successes) != 0 {
				t.Fatalf("failures=%#v successes=%#v, want one storage failure and no success", got.failures, got.successes)
			}
			if preservesDownstream[provider] && len(got.checks) == 0 {
				t.Fatalf("quota checks = 0, want preexisting downstream processing after %s storage failure", provider)
			}
		})
	}
}

func TestAntigravityDoesNotReportPollHealth(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	h := newRemainingPollHealthHarness(t, "antigravity", &fail)

	h.poll(context.Background())
	fail.Store(false)
	h.poll(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := h.spy.snapshot()
	if len(got.registered)+len(got.unregistered)+len(got.failures)+len(got.successes)+len(got.skips) != 0 {
		t.Fatalf(
			"poll health calls = registered:%d unregistered:%d failures:%d successes:%d skips:%d, want none",
			len(got.registered), len(got.unregistered), len(got.failures), len(got.successes), len(got.skips),
		)
	}
	if len(got.checks) == 0 {
		t.Fatal("quota notifications were disabled with poll health")
	}
}

func TestSyntheticAgentPollHealthInFlightCancellationIsSkipped(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	client := api.NewClient("test-key", slog.Default(),
		api.WithBaseURL(server.URL), api.WithTimeout(time.Second))
	ag := New(client, st, tracker.New(st, slog.Default()), time.Hour, slog.Default(), nil)
	spy := &pollHealthNotifierSpy{}
	ag.notifier = spy

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ag.poll(ctx)
		close(done)
	}()
	<-started
	cancel()
	<-done

	got := spy.snapshot()
	if len(got.skips) != 1 || len(got.failures) != 0 || len(got.successes) != 0 {
		t.Fatalf("skips=%d failures=%d successes=%d, want one in-flight cancellation skip",
			len(got.skips), len(got.failures), len(got.successes))
	}
}

func TestGeminiPollHealthSuccessfulRetryReportsSuccessOnly(t *testing.T) {
	var quotaRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = fmt.Fprint(w, `{"tier":"standard","cloudAICompanionProject":"test-project"}`)
		case "/v1internal:retrieveUserQuota":
			if quotaRequests.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprint(w, `{"buckets":[{
				"modelId":"gemini-2.5-pro","remainingFraction":0.75,
				"resetTime":"2026-08-01T00:00:00Z"
			}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	st := newTestGeminiStore(t)
	client := api.NewGeminiClient("stale-token", slog.Default(), api.WithGeminiBaseURL(server.URL))
	ag := NewGeminiAgent(client, st, tracker.NewGeminiTracker(st, slog.Default()), time.Hour, slog.Default(), nil)
	ag.SetCredentialsRefresh(func() *api.GeminiCredentials {
		return &api.GeminiCredentials{
			AccessToken:  "stale-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(time.Hour),
			ExpiresIn:    time.Hour,
		}
	})
	ag.SetClientCredentials(&api.GeminiClientCredentials{ClientID: "id", ClientSecret: "secret"})
	ag.refreshRequest = func(context.Context, string, string, string) (*api.GeminiOAuthTokenResponse, error) {
		return &api.GeminiOAuthTokenResponse{AccessToken: "fresh-token", ExpiresIn: 3600}, nil
	}
	spy := &pollHealthNotifierSpy{}
	ag.notifier = spy

	ag.poll(context.Background())

	got := spy.snapshot()
	if quotaRequests.Load() != 2 {
		t.Fatalf("quota requests = %d, want initial request plus retry", quotaRequests.Load())
	}
	if len(got.failures) != 0 || len(got.successes) != 1 || got.directAuthCalls != 0 {
		t.Fatalf("failures=%#v successes=%#v directAuth=%d, want retry success only",
			got.failures, got.successes, got.directAuthCalls)
	}
}

func TestGeminiPollHealthAuthPauseLeavesLaterTickForMissedIntervalMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = fmt.Fprint(w, `{"tier":"standard"}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)

	st := newTestGeminiStore(t)
	client := api.NewGeminiClient("bad-token", slog.Default(), api.WithGeminiBaseURL(server.URL))
	ag := NewGeminiAgent(client, st, tracker.NewGeminiTracker(st, slog.Default()), time.Hour, slog.Default(), nil)
	ag.SetCredentialsRefresh(func() *api.GeminiCredentials {
		return &api.GeminiCredentials{
			AccessToken:  "bad-token",
			RefreshToken: "bad-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
			ExpiresIn:    time.Hour,
		}
	})
	ag.SetClientCredentials(&api.GeminiClientCredentials{ClientID: "id", ClientSecret: "secret"})
	ag.refreshRequest = func(context.Context, string, string, string) (*api.GeminiOAuthTokenResponse, error) {
		return nil, api.ErrGeminiOAuthRefreshFailed
	}
	spy := &pollHealthNotifierSpy{}
	ag.notifier = spy

	for range maxGeminiAuthFailures {
		ag.poll(context.Background())
	}
	ag.poll(context.Background())

	got := spy.snapshot()
	if !ag.authPaused {
		t.Fatal("expected Gemini polling to pause")
	}
	if len(got.failures) != maxGeminiAuthFailures || len(got.skips) != 0 || got.directAuthCalls != 0 {
		t.Fatalf("failures=%d skips=%d directAuth=%d, paused tick must leave monitor deadline untouched",
			len(got.failures), len(got.skips), got.directAuthCalls)
	}
}

func TestCursorPollHealthSuccessfulRetryReportsSuccessOnly(t *testing.T) {
	var usageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			usageRequests.Add(1)
			if r.Header.Get("Authorization") != "Bearer fresh-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprint(w, `{
				"billingCycleStart":"1768399334000","billingCycleEnd":"1771077734000",
				"planUsage":{"totalSpend":5000,"remaining":35000,"limit":40000,"totalPercentUsed":12.5},
				"enabled":true
			}`)
		case "/aiserver.v1.DashboardService/GetPlanInfo":
			_, _ = fmt.Fprint(w, `{"planInfo":{"planName":"Pro"}}`)
		case "/aiserver.v1.DashboardService/GetCreditGrantsBalance":
			_, _ = fmt.Fprint(w, `{"hasCreditGrants":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	st, tr, _ := newTestCursorDeps(t)
	client := api.NewCursorClient("stale-token", slog.Default(), api.WithCursorBaseURL(server.URL))
	ag := NewCursorAgent(client, st, tr, time.Hour, slog.Default(), nil)
	ag.authFailCount = cursorMaxAuthFailures - 1
	ag.SetCredentialsRefresh(func() *api.CursorCredentials {
		return &api.CursorCredentials{AccessToken: "stale-token", RefreshToken: "refresh-token"}
	})
	ag.refreshRequest = func(context.Context, string) (*api.CursorOAuthResponse, error) {
		return &api.CursorOAuthResponse{AccessToken: "fresh-token", RefreshToken: "next-refresh"}, nil
	}
	ag.SetTokenSave(func(string, string) error { return nil })
	spy := &pollHealthNotifierSpy{}
	ag.notifier = spy

	ag.poll(context.Background())

	got := spy.snapshot()
	if usageRequests.Load() != 2 {
		t.Fatalf("usage requests = %d, want initial request plus retry", usageRequests.Load())
	}
	if len(got.failures) != 0 || len(got.successes) != 1 {
		t.Fatalf("failures=%#v successes=%#v, want retry success only", got.failures, got.successes)
	}
}

func TestCursorPollHealthRetryReportsFinalProviderFailureOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fresh-token" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	st, tr, _ := newTestCursorDeps(t)
	client := api.NewCursorClient("stale-token", slog.Default(), api.WithCursorBaseURL(server.URL))
	ag := NewCursorAgent(client, st, tr, time.Hour, slog.Default(), nil)
	ag.authFailCount = cursorMaxAuthFailures - 1
	ag.SetCredentialsRefresh(func() *api.CursorCredentials {
		return &api.CursorCredentials{AccessToken: "stale-token", RefreshToken: "refresh-token"}
	})
	ag.refreshRequest = func(context.Context, string) (*api.CursorOAuthResponse, error) {
		return &api.CursorOAuthResponse{AccessToken: "fresh-token", RefreshToken: "next-refresh"}, nil
	}
	ag.SetTokenSave(func(string, string) error { return nil })
	spy := &pollHealthNotifierSpy{}
	ag.notifier = spy

	ag.poll(context.Background())

	got := spy.snapshot()
	if len(got.failures) != 1 || len(got.successes) != 0 {
		t.Fatalf("failures=%#v successes=%#v, want one final failure", got.failures, got.successes)
	}
	if got.failures[0].category != "provider_request" {
		t.Fatalf("failure category = %q, want provider_request", got.failures[0].category)
	}
	if ag.authPaused {
		t.Fatal("non-auth retry failure must not pause authentication")
	}
}

func TestCursorPollHealthAuthPauseLeavesLaterTickForMissedIntervalMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	st, tr, _ := newTestCursorDeps(t)
	client := api.NewCursorClient("bad-token", slog.Default(), api.WithCursorBaseURL(server.URL))
	ag := NewCursorAgent(client, st, tr, time.Hour, slog.Default(), nil)
	ag.authFailCount = cursorMaxAuthFailures - 1
	spy := &pollHealthNotifierSpy{}
	ag.notifier = spy

	ag.poll(context.Background())
	ag.poll(context.Background())

	got := spy.snapshot()
	if !ag.authPaused {
		t.Fatal("expected Cursor polling to pause")
	}
	if len(got.failures) != 1 || len(got.skips) != 0 {
		t.Fatalf("failures=%d skips=%d, paused tick must leave monitor deadline untouched",
			len(got.failures), len(got.skips))
	}
}
