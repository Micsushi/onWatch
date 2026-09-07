package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

func TestGeminiQuotaRefreshReusesTokenAndChangesWithCredential(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if err := request.ParseForm(); err != nil || request.Form.Get("grant_type") != "refresh_token" {
			t.Error("expected OAuth refresh")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	}))
	defer server.Close()
	api.SetGeminiOAuthTokenURLForTest(server.URL)
	t.Cleanup(func() { api.SetGeminiOAuthTokenURLForTest("") })
	now := time.Now()
	r := &Runtime{now: func() time.Time { return now }}
	credentials := &api.GeminiCredentials{AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: now.Add(-time.Hour)}
	for i := 0; i < 2; i++ {
		token, err := r.geminiAccessToken(context.Background(), credentials)
		if err != nil || token != "fresh" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	if calls != 1 {
		t.Fatalf("refreshed %d times", calls)
	}
	credentials.RefreshToken = "new-account"
	if _, err := r.geminiAccessToken(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("reused token after credential changed")
	}
	now = now.Add(time.Hour)
	if _, err := r.geminiAccessToken(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("reused expired token")
	}
	if credentials.AccessToken != "expired" {
		t.Fatal("modified source credentials")
	}
}

func TestQuotaBackoffSurvivesRestartAndSkipsEarlyPoll(t *testing.T) {
	cfg := Config{SpoolDir: t.TempDir(), SpoolMaxBytes: 1 << 20, HomeDir: t.TempDir()}
	r, err := NewRuntime(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	r.random = func() float64 { return 0.5 }
	r.desired.Assignments = []ingest.ProviderAssignment{{Provider: "unsupported-test-provider", ExternalID: "test", PollInterval: "1m"}}
	key := "unsupported-test-provider\x00test"
	for attempt := 1; attempt <= 3; attempt++ {
		if err := r.collectAssignedQuotas(context.Background()); err != nil {
			t.Fatal(err)
		}
		state := r.quotaPolls[key]
		if state.Failures != attempt {
			t.Fatalf("failures = %d, want %d", state.Failures, attempt)
		}
		if err := r.collectAssignedQuotas(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r.quotaPolls[key] != state {
			t.Fatal("early poll changed retry state")
		}
		now = state.NextPoll
	}
	restarted, err := NewRuntime(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.quotaPolls[key] != r.quotaPolls[key] {
		t.Fatal("restart lost retry state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restarted.collectAssignedQuotas(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if restarted.quotaPolls[key] != r.quotaPolls[key] {
		t.Fatal("cancellation counted as failure")
	}
}

func TestQuotaDelayBounds(t *testing.T) {
	for _, interval := range []time.Duration{time.Minute, 2 * time.Hour} {
		for _, attempt := range []int{0, 1, 3, 1000} {
			for _, jitter := range []float64{0, 0.5, 1} {
				delay := quotaPollDelay(interval, attempt, jitter)
				if delay < interval*8/10 || delay > max(time.Hour, interval*12/10) {
					t.Fatalf("interval=%v attempt=%d jitter=%v delay=%v", interval, attempt, jitter, delay)
				}
			}
		}
	}
}
