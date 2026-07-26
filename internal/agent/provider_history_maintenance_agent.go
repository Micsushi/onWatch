package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

const (
	providerHistoryMaintenanceInitialDelay = 5 * time.Second
	providerHistoryMaintenanceInterval     = time.Hour
)

// ProviderHistoryMaintenanceAgent compacts old provider snapshots after the
// web server has had time to become available, then repeats at a low cadence.
type ProviderHistoryMaintenanceAgent struct {
	store        *store.Store
	retention    time.Duration
	initialDelay time.Duration
	interval     time.Duration
	logger       *slog.Logger
}

func NewProviderHistoryMaintenanceAgent(st *store.Store, retention time.Duration, logger *slog.Logger) *ProviderHistoryMaintenanceAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProviderHistoryMaintenanceAgent{
		store:        st,
		retention:    retention,
		initialDelay: providerHistoryMaintenanceInitialDelay,
		interval:     providerHistoryMaintenanceInterval,
		logger:       logger,
	}
}

func (a *ProviderHistoryMaintenanceAgent) Run(ctx context.Context) error {
	if a.store == nil || a.retention <= 0 {
		<-ctx.Done()
		return nil
	}

	timer := time.NewTimer(a.initialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C:
	}

	a.compact()
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			a.compact()
		}
	}
}

func (a *ProviderHistoryMaintenanceAgent) compact() {
	cutoff := time.Now().UTC().Add(-a.retention)
	compactedMetadata, err := a.store.CompactAPIIntegrationMetadataJSON()
	if err != nil {
		a.logger.Error("API integration metadata compaction failed", "error", err)
	} else if compactedMetadata > 0 {
		a.logger.Info("API integration metadata compacted", "updated_events", compactedMetadata)
	}
	result, err := a.store.CompactProviderSnapshotHistory(cutoff)
	if err != nil {
		a.logger.Error("Provider snapshot retention compaction failed", "error", err)
		return
	}
	if result.DeletedSnapshots == 0 && result.ClearedRawJSON == 0 {
		return
	}
	a.logger.Info(
		"Provider snapshot retention compacted history",
		"deleted_snapshots", result.DeletedSnapshots,
		"cleared_raw_json", result.ClearedRawJSON,
		"cutoff", cutoff.Format(time.RFC3339),
	)
}
