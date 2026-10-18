package notify

import (
	"sync"
	"testing"
	"time"
)

func TestQuietWeeklyPolicyBoundariesAndRestart(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	discord, capture := newDiscordCapture(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	reset := now.Add(weeklyPaceWindow / 2)
	engine := newTestEngine(t, s)
	configure := func(e *NotificationEngine) {
		e.quietWeekly = true
		e.discord = discord
		e.now = func() time.Time { return now }
		e.cfg.Channels = NotificationChannels{Discord: true}
		e.cfg.Types = NotificationTypes{Underuse: true, Overuse: true, Reset: true, Warning: true, Critical: true, AuthError: true}
	}
	configure(engine)
	base := QuotaStatus{Provider: "codex", QuotaKey: "seven_day", Utilization: 49.9, ResetsAt: &reset}
	for _, provider := range []string{"gemini", "copilot", "antigravity", "minimax", "zai"} {
		status := base
		status.Provider, status.ResetOccurred = provider, true
		engine.Check(status)
		status.ResetOccurred = false
		engine.Check(status)
	}
	status := base
	status.Utilization = 50
	engine.Check(status)
	status = base
	status.QuotaKey = "seven_day_sonnet"
	engine.Check(status)
	status = base
	status.ResetOccurred = true
	engine.Check(status)
	now = now.Add(-time.Second)
	engine.Check(base)
	now = now.Add(time.Second)
	if len(capture.snapshot()) != 0 {
		t.Fatal("ineligible notifications delivered")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); engine.Check(base) }()
	}
	wg.Wait()
	engine = newTestEngine(t, s)
	configure(engine)
	base.AccountID = "other-account"
	engine.Check(base)
	if got := len(capture.snapshot()); got != 1 {
		t.Fatalf("restart/account/concurrency dedupe: got %d", got)
	}
	base.Provider = "anthropic"
	engine.Check(base)
	if got := len(capture.snapshot()); got != 2 {
		t.Fatalf("separate provider: got %d", got)
	}
	if engine.SendAuthErrorNotification(AuthErrorAlert{Provider: "gemini"}) {
		t.Fatal("auth alert escaped quiet policy")
	}
	cfg, _, _, _ := engine.pollHealthDeliverySnapshot()
	if cfg.NotifyPollFailure || cfg.NotifyPollRecovery {
		t.Fatal("poll health escaped quiet policy")
	}
	now = now.Add(weeklyPaceWindow)
	reset = reset.Add(weeklyPaceWindow)
	engine.Check(base)
	if got := len(capture.snapshot()); got != 3 {
		t.Fatalf("new week: got %d", got)
	}
}

func TestQuietWeeklyDoesNotCatchUpLateOrRetryUncertainDelivery(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	engine := newTestEngine(t, s)
	engine.quietWeekly = true
	discord, capture := newDiscordCapture(t)
	engine.discord = discord
	engine.cfg.Channels = NotificationChannels{Discord: true}
	engine.cfg.Types.Underuse = true
	now := time.Now().UTC()
	engine.now = func() time.Time { return now }
	reset := now.Add(weeklyPaceWindow/2 - 2*time.Hour)
	status := QuotaStatus{Provider: "codex", QuotaKey: "seven_day", Utilization: 10, ResetsAt: &reset}
	engine.Check(status)
	if len(capture.snapshot()) != 0 {
		t.Fatal("late checkpoint delivered")
	}
	reset = now.Add(weeklyPaceWindow / 2)
	// A persisted pre-delivery claim represents either success or an uncertain response.
	if err := s.SetSetting("quiet_weekly_underuse:codex", `{"cycle":"`+reset.Format(time.RFC3339Nano)+`","attempted_at":"`+now.Format(time.RFC3339Nano)+`"}`); err != nil {
		t.Fatal(err)
	}
	engine.Check(status)
	if len(capture.snapshot()) != 0 {
		t.Fatal("uncertain delivery replayed")
	}
}
