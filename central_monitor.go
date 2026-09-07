package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

const centralMonitorCursor = "central_quota_monitor_cursor"

// Run outside the HTTP handler: notification delivery must not block uploads.
// The database is the durable queue, so a restart does not discard observations.
func startCentralQuotaMonitor(ctx context.Context, db *store.Store, logger *slog.Logger, check func(notify.QuotaStatus)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		process := centralQuotaProcessor(db, logger, check)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			if err := processCentralQuotaUpdates(db, time.Now(), process); err != nil {
				logger.Error("central quota monitor failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

func processCentralQuotaUpdates(db *store.Store, now time.Time, process func(store.CentralQuotaObservation) error) error {
	value, err := db.GetSetting(centralMonitorCursor)
	if err != nil {
		return err
	}
	cursor := int64(0)
	if value != "" {
		cursor, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
	}
	observations, err := db.CentralQuotaObservations(cursor)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		// Historical imports and delayed offline traffic must not send live alerts.
		age := now.Sub(observation.CapturedAt)
		if age >= -time.Minute && age <= 5*time.Minute {
			if err := process(observation); err != nil {
				return err
			}
		}
		if err := db.SetSetting(centralMonitorCursor, strconv.FormatInt(observation.ID, 10)); err != nil {
			return err
		}
	}
	return nil
}

func centralQuotaProcessor(db *store.Store, logger *slog.Logger, check func(notify.QuotaStatus)) func(store.CentralQuotaObservation) error {
	codex := tracker.NewCodexTracker(db, logger)
	claude := tracker.NewAnthropicTracker(db, logger)
	gemini := tracker.NewGeminiTracker(db, logger)
	antigravity := tracker.NewAntigravityTracker(db, logger)
	return func(o store.CentralQuotaObservation) error {
		provider := o.Provider
		account := o.ExternalID
		if provider == "openai" {
			provider = "codex"
			account = strconv.FormatInt(o.AccountID, 10)
		}
		statuses := make(map[string]notify.QuotaStatus)
		for _, metric := range o.Snapshot.Metrics {
			status := notify.QuotaStatus{Provider: provider, AccountID: account, QuotaKey: metric.Name, Utilization: metric.Value, ResetsAt: metric.ResetsAt}
			if metric.Limit != nil {
				status.Limit = *metric.Limit
			}
			if metric.Unit != "percent" {
				if status.Limit <= 0 {
					continue
				}
				status.Utilization = 100 * metric.Value / status.Limit
			}
			statuses[metric.Name] = status
		}
		onReset := func(name string) {
			if status, ok := statuses[name]; ok {
				status.ResetOccurred = true
				check(status)
			}
		}
		var err error
		switch provider {
		case "codex":
			codex.SetOnReset(onReset)
			snapshot := &api.CodexSnapshot{CapturedAt: o.CapturedAt, AccountID: o.AccountID}
			for _, q := range o.Snapshot.Metrics {
				snapshot.Quotas = append(snapshot.Quotas, api.CodexQuota{Name: q.Name, Utilization: q.Value, ResetsAt: q.ResetsAt, Status: q.Status})
			}
			err = codex.Process(snapshot)
		case "anthropic":
			claude.SetOnReset(onReset)
			snapshot := &api.AnthropicSnapshot{CapturedAt: o.CapturedAt}
			for _, q := range o.Snapshot.Metrics {
				snapshot.Quotas = append(snapshot.Quotas, api.AnthropicQuota{Name: q.Name, Utilization: q.Value, ResetsAt: q.ResetsAt})
			}
			err = claude.Process(snapshot)
		case "gemini":
			gemini.SetOnReset(onReset)
			snapshot := &api.GeminiSnapshot{CapturedAt: o.CapturedAt}
			for _, q := range o.Snapshot.Metrics {
				snapshot.Quotas = append(snapshot.Quotas, api.GeminiQuota{ModelID: q.Name, UsagePercent: q.Value, RemainingFraction: 1 - q.Value/100, ResetTime: q.ResetsAt})
			}
			err = gemini.Process(snapshot)
		case "antigravity":
			antigravity.SetOnReset(onReset)
			snapshot := &api.AntigravitySnapshot{CapturedAt: o.CapturedAt}
			for _, q := range o.Snapshot.Metrics {
				snapshot.Models = append(snapshot.Models, api.AntigravityModelQuota{ModelID: q.Name, Label: q.Name, RemainingPercent: 100 - q.Value, RemainingFraction: 1 - q.Value/100, IsExhausted: q.Value >= 100, ResetTime: q.ResetsAt})
			}
			err = antigravity.Process(snapshot)
		}
		if err != nil {
			return fmt.Errorf("central %s cycles: %w", provider, err)
		}
		for _, status := range statuses {
			check(status)
		}
		return nil
	}
}
