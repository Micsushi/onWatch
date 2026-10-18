package notify

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// The central owner's quiet policy leaves dashboard history intact. Only the
// all-model weekly quota is eligible, never model-specific or five-hour limits.
func (e *NotificationEngine) checkQuietWeekly(status QuotaStatus) {
	provider := normalizeNotificationProvider(status.Provider)
	if (provider != "codex" && provider != "anthropic") || status.QuotaKey != "seven_day" ||
		status.ResetsAt == nil || status.ResetOccurred || math.IsNaN(status.Utilization) ||
		status.Utilization < 0 || status.Utilization >= 50 {
		return
	}
	now := e.now()
	midpoint := status.ResetsAt.Add(-weeklyPaceWindow / 2)
	// Allow normal polling delay, but do not turn a missed checkpoint into daily alerts.
	if now.Before(midpoint) || now.Sub(midpoint) > underuseNotificationWindow {
		return
	}
	e.mu.RLock()
	cfg, mailer, push, discord := e.cfg, e.mailer, e.pushSender, e.discord
	e.mu.RUnlock()
	if !cfg.Types.Underuse || !(cfg.Channels.Email && mailer != nil ||
		cfg.Channels.Push && push != nil || cfg.Channels.Discord && discord != nil) {
		return
	}
	e.weeklyMu.Lock()
	defer e.weeklyMu.Unlock()
	key := "quiet_weekly_underuse:" + provider
	var previous struct {
		Cycle       time.Time `json:"cycle"`
		AttemptedAt time.Time `json:"attempted_at"`
	}
	raw, err := e.store.GetSetting(key)
	if err != nil || (raw != "" && json.Unmarshal([]byte(raw), &previous) != nil) {
		e.logger.Error("cannot read weekly notification checkpoint", "provider", provider)
		return
	}
	if previous.Cycle.Equal(*status.ResetsAt) || (!previous.AttemptedAt.IsZero() && now.Sub(previous.AttemptedAt) < weeklyPaceWindow) {
		return
	}
	previous.Cycle, previous.AttemptedAt = *status.ResetsAt, now
	data, _ := json.Marshal(previous)
	// Claim before delivery: a crash or uncertain webhook response must not repeat
	// the alert. Failed delivery waits for the next weekly checkpoint.
	if err := e.store.SetSetting(key, string(data)); err != nil {
		e.logger.Error("cannot save weekly notification checkpoint", "provider", provider)
		return
	}
	subject := fmt.Sprintf("[%s] weekly halfway check: %.0f%% used", paceDiscordProvider(provider), status.Utilization)
	body := fmt.Sprintf("Weekly usage is below 50%% at the halfway point.\nUsage: %.1f%%\nNext reset: %s\nThis is the only underusage alert for this provider this week.", status.Utilization, status.ResetsAt.UTC().Format(time.RFC3339))
	e.sendViaChannels(mailer, push, discord, cfg.Channels, subject, body, "provider", provider, "type", "weekly_underuse")
}
