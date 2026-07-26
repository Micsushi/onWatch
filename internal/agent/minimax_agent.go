package agent

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// MiniMaxAgent manages background polling for MiniMax remains.
type MiniMaxAgent struct {
	client       *api.MiniMaxClient
	store        *store.Store
	tracker      *tracker.MiniMaxTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     agentNotifier
	pollingCheck func() bool
	accountID    int64
}

// NewMiniMaxAgent creates a new MiniMax polling agent.
func NewMiniMaxAgent(client *api.MiniMaxClient, store *store.Store, tr *tracker.MiniMaxTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *MiniMaxAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &MiniMaxAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// NewMiniMaxAgentWithAccount creates a new MiniMax polling agent scoped to a specific account.
func NewMiniMaxAgentWithAccount(client *api.MiniMaxClient, store *store.Store, tr *tracker.MiniMaxTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager, accountID int64) *MiniMaxAgent {
	ag := NewMiniMaxAgent(client, store, tr, interval, logger, sm)
	ag.accountID = accountID
	return ag
}

// SetPollingCheck sets a provider polling guard function.
func (a *MiniMaxAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for quota notifications.
func (a *MiniMaxAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

func (a *MiniMaxAgent) pollHealthAccountID() string {
	return strconv.FormatInt(a.accountID, 10)
}

func (a *MiniMaxAgent) recordPollFailure(category, message string) {
	if a.notifier != nil {
		a.notifier.RecordPollFailure("minimax", a.pollHealthAccountID(), category, message)
	}
}

func (a *MiniMaxAgent) recordPollSuccess() {
	if a.notifier != nil {
		a.notifier.RecordPollSuccess("minimax", a.pollHealthAccountID())
	}
}

func (a *MiniMaxAgent) recordPollSkipped() {
	if a.notifier != nil {
		a.notifier.RecordPollSkipped("minimax", a.pollHealthAccountID())
	}
}

// Run starts the polling loop until context cancellation.
func (a *MiniMaxAgent) Run(ctx context.Context) error {
	a.logger.Info("MiniMax agent started", "interval", a.interval)
	if a.notifier != nil {
		a.notifier.RegisterPoller("minimax", a.pollHealthAccountID(), a.interval)
	}
	defer func() {
		if a.notifier != nil {
			a.notifier.UnregisterPoller("minimax", a.pollHealthAccountID())
		}
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("MiniMax agent stopped")
	}()

	a.poll(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *MiniMaxAgent) poll(ctx context.Context) {
	if ctx.Err() != nil {
		a.recordPollSkipped()
		return
	}
	if a.pollingCheck != nil && !a.pollingCheck() {
		a.recordPollSkipped()
		return
	}

	resp, err := a.client.FetchRemains(ctx)
	if err != nil {
		if ctx.Err() != nil {
			a.recordPollSkipped()
			return
		}
		a.logger.Error("Failed to fetch MiniMax remains", "error", err)
		a.recordPollFailure("provider_request",
			"MiniMax quotas could not be fetched. Check connectivity, credentials, and provider availability.")
		return
	}

	now := time.Now().UTC()
	snapshot := resp.ToSnapshot(now)

	if _, err := a.store.InsertMiniMaxSnapshot(snapshot, a.accountID); err != nil {
		a.logger.Error("Failed to insert MiniMax snapshot", "error", err)
		a.recordPollFailure("storage",
			"MiniMax usage was fetched but could not be saved. Check onWatch database access.")
	} else {
		a.recordPollSuccess()
	}

	if err := a.tracker.Process(snapshot, a.accountID); err != nil {
		a.logger.Error("MiniMax tracker processing failed", "error", err)
	}

	if a.notifier != nil {
		accountID := ""
		if a.accountID > 1 {
			accountID = strconv.FormatInt(a.accountID, 10)
		}
		if snapshot.IsSharedQuota() {
			// Shared pool: send one notification for the entire plan
			if merged := snapshot.MergedQuota(); merged != nil && merged.Total > 0 {
				a.notifier.Check(notify.QuotaStatus{
					Provider:    "minimax",
					QuotaKey:    "coding_plan",
					AccountID:   accountID,
					Utilization: merged.UsedPercent,
					Limit:       float64(merged.Total),
					ResetsAt:    merged.ResetAt,
				})
				if merged.HasWeeklyQuota && merged.WeeklyTotal > 0 {
					a.notifier.Check(notify.QuotaStatus{
						Provider:    "minimax",
						QuotaKey:    "weekly_coding_plan",
						AccountID:   accountID,
						Utilization: merged.WeeklyUsedPercent,
						Limit:       float64(merged.WeeklyTotal),
						ResetsAt:    merged.WeeklyResetAt,
					})
				}
			}
		} else {
			for _, m := range snapshot.Models {
				if m.Total == 0 {
					continue
				}
				a.notifier.Check(notify.QuotaStatus{
					Provider:    "minimax",
					QuotaKey:    m.ModelName,
					AccountID:   accountID,
					Utilization: m.UsedPercent,
					Limit:       float64(m.Total),
					ResetsAt:    m.ResetAt,
				})
				if m.HasWeeklyQuota && m.WeeklyTotal > 0 {
					a.notifier.Check(notify.QuotaStatus{
						Provider:    "minimax",
						QuotaKey:    "weekly_" + m.ModelName,
						AccountID:   accountID,
						Utilization: m.WeeklyUsedPercent,
						Limit:       float64(m.WeeklyTotal),
						ResetsAt:    m.WeeklyResetAt,
					})
				}
			}
		}
	}

	if a.sm != nil {
		values := make([]float64, 0, len(snapshot.Models))
		for _, m := range snapshot.Models {
			values = append(values, float64(m.Used))
		}
		a.sm.ReportPoll(values)
	}

	for _, m := range snapshot.Models {
		a.logger.Info("MiniMax poll complete",
			"model", m.ModelName,
			"used", m.Used,
			"total", m.Total,
			"remain", m.Remain,
		)
	}
}
