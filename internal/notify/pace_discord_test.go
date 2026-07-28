package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type discordCapture struct {
	mu       sync.Mutex
	messages []string
}

func newDiscordCapture(t *testing.T) (*DiscordSender, *discordCapture) {
	t.Helper()
	capture := &discordCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload struct {
			Content string `json:"content"`
			Embeds  []struct {
				Description string `json:"description"`
			} `json:"embeds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Discord payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		message := payload.Content
		if len(payload.Embeds) > 0 {
			message += "\n" + payload.Embeds[0].Description
		}
		capture.mu.Lock()
		capture.messages = append(capture.messages, message)
		capture.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return &DiscordSender{webhookURL: server.URL, client: server.Client()}, capture
}

func (c *discordCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.messages...)
}

func TestEvaluateWeeklyPaceMatchesDashboardStates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	tests := []struct {
		name string
		util float64
		want PaceTier
	}{
		{name: "purple", util: 28, want: PaceVeryUnder},
		{name: "orange", util: 53, want: PaceOver},
		{name: "red", util: 72, want: PaceVeryOver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := EvaluateWeeklyPace("seven_day", tt.util, reset, now)
			if !ok {
				t.Fatal("weekly quota was not evaluated")
			}
			if got.Tier != tt.want {
				t.Fatalf("tier = %q, want %q", got.Tier, tt.want)
			}
		})
	}
}

func TestNotificationEngine_DiscordSkipsSubPercentPaceDifferenceAfterReset(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 28, 22, 1, 0, 0, time.UTC)
	reset := now.Add(weeklyPaceWindow - 45*time.Minute)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Underuse: true}
	engine.cfg.UnderuseTimes = []string{"22:00"}
	engine.Check(QuotaStatus{
		Provider:    "codex",
		QuotaKey:    "seven_day",
		Utilization: 0,
		ResetsAt:    &reset,
	})

	if messages := capture.snapshot(); len(messages) != 0 {
		t.Fatalf("Discord messages = %d, want 0 for sub-percent pace difference: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceEntryAndTenPointSteps(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Overuse: true}

	check := func(provider string, utilization float64) {
		engine.Check(QuotaStatus{
			Provider:    provider,
			QuotaKey:    "seven_day",
			Utilization: utilization,
			ResetsAt:    &reset,
		})
	}

	check("anthropic", 72)
	check("anthropic", 75)
	check("anthropic", 82)
	check("anthropic", 53)
	check("anthropic", 72)
	check("codex", 72)

	messages := capture.snapshot()
	if len(messages) != 4 {
		t.Fatalf("Discord messages = %d, want 4: %v", len(messages), messages)
	}
	for _, want := range []string{"72.0%", "82.0%", "[codex]"} {
		if !strings.Contains(strings.Join(messages, "\n"), want) {
			t.Errorf("Discord messages missing %q: %v", want, messages)
		}
	}
	if joined := strings.Join(messages, "\n"); !strings.Contains(joined,
		"[codex] very over pace: 72% instead of 43%, resets in 4d") {
		t.Errorf("Discord messages missing detailed Codex title: %v", messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceUsesConfiguredRepeatStep(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Overuse: true}
	engine.cfg.OveruseRepeatPercent = 5

	for _, utilization := range []float64{72, 76, 77} {
		engine.Check(QuotaStatus{
			Provider:    "anthropic",
			QuotaKey:    "seven_day",
			Utilization: utilization,
			ResetsAt:    &reset,
		})
	}

	if messages := capture.snapshot(); len(messages) != 2 {
		t.Fatalf("Discord messages = %d, want 2 with a 5%% repeat step: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceCanBeDisabledSeparately(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Critical: true, Overuse: false}
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "seven_day",
		Utilization: 72,
		ResetsAt:    &reset,
	})

	if messages := capture.snapshot(); len(messages) != 0 {
		t.Fatalf("Discord messages = %d, want 0 when red pace alerts are disabled: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceSurvivesRestart(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)
	status := QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "seven_day",
		Utilization: 72,
		ResetsAt:    &reset,
	}

	newEngine := func() *NotificationEngine {
		engine := newTestEngine(t, s)
		engine.discord = discord
		engine.now = func() time.Time { return now }
		engine.cfg.Channels = NotificationChannels{Discord: true}
		engine.cfg.Types = NotificationTypes{Overuse: true}
		return engine
	}

	newEngine().Check(status)
	status.Utilization = 75
	restarted := newEngine()
	restarted.Check(status)

	if messages := capture.snapshot(); len(messages) != 1 {
		t.Fatalf("Discord messages after restart = %d, want 1: %v", len(messages), messages)
	}
	status.Utilization = 53
	restarted.Check(status)
	status.Utilization = 72
	restarted.Check(status)
	if messages := capture.snapshot(); len(messages) != 2 {
		t.Fatalf("Discord messages after red re-entry = %d, want 2: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceReentrySurvivesRestart(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)
	status := QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "seven_day",
		Utilization: 72,
		ResetsAt:    &reset,
	}

	newEngine := func() *NotificationEngine {
		engine := newTestEngine(t, s)
		engine.discord = discord
		engine.now = func() time.Time { return now }
		engine.cfg.Channels = NotificationChannels{Discord: true}
		engine.cfg.Types = NotificationTypes{Overuse: true}
		return engine
	}

	engine := newEngine()
	engine.Check(status)
	status.Utilization = 53
	engine.Check(status)
	status.Utilization = 72
	newEngine().Check(status)

	if messages := capture.snapshot(); len(messages) != 2 {
		t.Fatalf("Discord messages after restart and red re-entry = %d, want 2: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceConcurrentChecksDedupe(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	var requests atomic.Int32
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
		}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	engine := newTestEngine(t, s)
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Overuse: true}
	status := QuotaStatus{Provider: "anthropic", QuotaKey: "seven_day", Utilization: 72, ResetsAt: &reset}

	var checks sync.WaitGroup
	checks.Add(2)
	go func() {
		defer checks.Done()
		engine.Check(status)
	}()
	<-firstStarted
	go func() {
		defer checks.Done()
		engine.Check(status)
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	checks.Wait()

	if got := requests.Load(); got != 1 {
		t.Fatalf("concurrent Discord requests = %d, want 1", got)
	}
}

func TestNotificationEngine_DiscordVeryUnderPaceAtDefaultTimes(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 10, 5, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Underuse: true}

	status := QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "seven_day",
		Utilization: 20,
		ResetsAt:    &reset,
	}
	engine.Check(status)
	engine.Check(status)
	now = time.Date(2026, 7, 23, 22, 5, 0, 0, time.UTC)
	engine.Check(status)
	status.Provider = "codex"
	engine.Check(status)

	messages := capture.snapshot()
	if len(messages) != 3 {
		t.Fatalf("Discord messages = %d, want 3: %v", len(messages), messages)
	}
	if !strings.Contains(strings.Join(messages, "\n"), "Very under pace") {
		t.Fatalf("under-usage message missing pace state: %v", messages)
	}
	if !strings.Contains(messages[0],
		"[claude] very under pace: 20% instead of 43%, resets in 4d") {
		t.Errorf("under-usage message missing detailed Claude title: %v", messages)
	}
}

func TestNotificationEngine_DiscordVeryUnderPaceDoesNotBackfillOldSlot(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 20, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Underuse: true}
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "seven_day",
		Utilization: 20,
		ResetsAt:    &reset,
	})

	if messages := capture.snapshot(); len(messages) != 0 {
		t.Fatalf("Discord messages = %d, want 0 for stale schedule: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordSkipsFixedThresholdWarnings(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)

	engine.discord = discord
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Warning: true, Critical: true, Overuse: true}
	engine.Check(QuotaStatus{Provider: "anthropic", QuotaKey: "five_hour", Utilization: 99})

	if messages := capture.snapshot(); len(messages) != 0 {
		t.Fatalf("fixed threshold sent %d Discord messages, want 0: %v", len(messages), messages)
	}
}

func TestNotificationEngine_DiscordVeryOverPaceHonorsQuotaOverride(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reset := now.Add(4 * 24 * time.Hour)

	engine.discord = discord
	engine.now = func() time.Time { return now }
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types = NotificationTypes{Overuse: true}
	engine.cfg.Overrides = map[string]ThresholdOverride{
		"anthropic:seven_day": {DisableCrit: true},
	}
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "seven_day",
		Utilization: 72,
		ResetsAt:    &reset,
	})

	if messages := capture.snapshot(); len(messages) != 0 {
		t.Fatalf("Discord messages = %d, want 0 for disabled quota: %v", len(messages), messages)
	}
}

func TestNotificationEngine_ReloadUnderuseSchedule(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	enabled := true
	storeNotificationConfig(t, s, notificationSettingsJSON{
		NotifyUnderuse: &enabled,
		UnderuseTimes:  []string{"09:15", "21:45"},
	})

	if err := engine.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	cfg := engine.Config()
	if !cfg.Types.Underuse {
		t.Fatal("under-usage notifications should be enabled")
	}
	if got := strings.Join(cfg.UnderuseTimes, ","); got != "09:15,21:45" {
		t.Fatalf("under-usage times = %q, want 09:15,21:45", got)
	}
}

func TestNotificationEngine_ReloadDiscordPaceControls(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	enabled := false
	repeat := 7.5
	storeNotificationConfig(t, s, notificationSettingsJSON{
		NotifyOveruse:        &enabled,
		OveruseRepeatPercent: &repeat,
	})

	if err := engine.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	cfg := engine.Config()
	if cfg.Types.Overuse {
		t.Fatal("red pace notifications should be disabled")
	}
	if cfg.OveruseRepeatPercent != 7.5 {
		t.Fatalf("red pace repeat step = %.1f, want 7.5", cfg.OveruseRepeatPercent)
	}
}
