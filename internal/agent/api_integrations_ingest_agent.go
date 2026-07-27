package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

const (
	apiIntegrationIngestIntervalDefault                = 5 * time.Second
	apiIntegrationIngestMaxReadBytes                   = 256 * 1024
	apiIntegrationIngestMaxInvalidAlertsPerFilePerScan = 10
	apiIntegrationIngestMaxFilesPerScan                = 100
	apiIntegrationPruneIntervalDefault                 = time.Hour
)

// APIIntegrationsIngestAgent tails normalized JSONL API integration usage files and stores the events.
type APIIntegrationsIngestAgent struct {
	store          *store.Store
	dir            string
	interval       time.Duration
	retention      time.Duration
	pruneInterval  time.Duration
	lastPrune      time.Time
	scanPathCursor int
	logger         *slog.Logger
}

// NewAPIIntegrationsIngestAgent creates a new API integrations file ingester.
func NewAPIIntegrationsIngestAgent(store *store.Store, dir string, retention time.Duration, logger *slog.Logger) *APIIntegrationsIngestAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &APIIntegrationsIngestAgent{
		store:         store,
		dir:           dir,
		interval:      apiIntegrationIngestIntervalDefault,
		retention:     retention,
		pruneInterval: apiIntegrationPruneIntervalDefault,
		logger:        logger,
	}
}

// SetInterval overrides the scan interval. Used in tests.
func (a *APIIntegrationsIngestAgent) SetInterval(interval time.Duration) {
	if interval > 0 {
		a.interval = interval
	}
}

// Run starts the periodic ingestion loop until context cancellation.
func (a *APIIntegrationsIngestAgent) Run(ctx context.Context) error {
	a.logger.Info("API integrations ingester started", "dir", a.dir, "interval", a.interval)
	defer a.logger.Info("API integrations ingester stopped")

	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("create API integrations dir: %w", err)
	}

	a.scan()

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.scan()
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *APIIntegrationsIngestAgent) scan() {
	pattern := filepath.Join(a.dir, "*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		a.logger.Error("API integrations ingester glob failed", "dir", a.dir, "error", err)
		return
	}
	sort.Strings(paths)
	if len(paths) > apiIntegrationIngestMaxFilesPerScan {
		start := a.scanPathCursor % len(paths)
		selected := make([]string, 0, apiIntegrationIngestMaxFilesPerScan)
		for i := 0; i < apiIntegrationIngestMaxFilesPerScan; i++ {
			selected = append(selected, paths[(start+i)%len(paths)])
		}
		a.logger.Warn(
			"API integrations ingester skipped files beyond scan cap",
			"dir", a.dir,
			"total_files", len(paths),
			"processed_files", apiIntegrationIngestMaxFilesPerScan,
			"skipped_files", len(paths)-apiIntegrationIngestMaxFilesPerScan,
		)
		a.scanPathCursor = (start + len(selected)) % len(paths)
		paths = selected
	} else {
		a.scanPathCursor = 0
	}

	for _, path := range paths {
		if err := a.scanFile(path); err != nil {
			a.logger.Error("API integrations ingester scan failed", "path", path, "error", err)
		}
	}

	if err := a.pruneExpiredUsageEvents(); err != nil {
		a.logger.Error("API integrations ingester prune failed", "error", err)
	}
}

func (a *APIIntegrationsIngestAgent) scanFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return nil
	}

	state, err := a.store.GetAPIIntegrationIngestState(path)
	if err != nil {
		return err
	}
	if state == nil {
		state = &apiintegrations.IngestState{SourcePath: path}
	}
	if state.PartialLineOversized {
		a.logger.Warn(
			"API integrations ingester discarded oversized persisted partial line",
			"path", path,
			"partial_line_bytes", state.PartialLineBytes,
			"max_partial_line_bytes", apiintegrations.MaxIngestPartialLineBytes,
		)
		state.PartialLine = ""
	}

	if info.Size() < state.Offset {
		state.Offset = 0
		state.PartialLine = ""
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}

	if _, err := file.Seek(state.Offset, io.SeekStart); err != nil {
		_ = file.Close()
		return fmt.Errorf("seek file: %w", err)
	}

	data, err := io.ReadAll(io.LimitReader(file, apiIntegrationIngestMaxReadBytes))
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close file: %w", closeErr)
	}

	if len(data) == 0 {
		state.FileSize = info.Size()
		state.FileModTime = info.ModTime().UTC()
		return a.store.UpsertAPIIntegrationIngestState(state)
	}

	state.Offset += int64(len(data))
	state.FileSize = info.Size()
	state.FileModTime = info.ModTime().UTC()

	combined := state.PartialLine + string(data)
	lines, remainder := splitCompleteLines(combined)
	state.PartialLine = remainder
	if len(state.PartialLine) > apiintegrations.MaxIngestPartialLineBytes {
		a.logger.Warn(
			"API integrations ingester discarded oversized partial line",
			"path", path,
			"partial_line_bytes", len(state.PartialLine),
			"max_partial_line_bytes", apiintegrations.MaxIngestPartialLineBytes,
		)
		state.PartialLine = ""
	}

	invalidAlertsCreated := 0
	invalidAlertsSuppressed := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := apiintegrations.ParseUsageEventLine([]byte(trimmed), path)
		if err != nil {
			if invalidAlertsCreated < apiIntegrationIngestMaxInvalidAlertsPerFilePerScan {
				a.recordInvalidLine(path, trimmed, err)
				invalidAlertsCreated++
			} else {
				invalidAlertsSuppressed++
			}
			continue
		}
		if _, err := a.store.InsertAPIIntegrationUsageEvent(event); err != nil {
			if errors.Is(err, store.ErrDuplicateAPIIntegrationUsageEvent) {
				continue
			}
			return err
		}
	}
	if invalidAlertsSuppressed > 0 {
		a.logger.Warn(
			"API integrations ingester suppressed invalid line alerts",
			"path", path,
			"alert_limit", apiIntegrationIngestMaxInvalidAlertsPerFilePerScan,
			"alerts_created", invalidAlertsCreated,
			"alerts_suppressed", invalidAlertsSuppressed,
		)
	}

	if err := a.store.UpsertAPIIntegrationIngestState(state); err != nil {
		return err
	}
	a.removeConsumedAgentUsageFile(path, state)
	return nil
}

func (a *APIIntegrationsIngestAgent) removeConsumedAgentUsageFile(path string, state *apiintegrations.IngestState) {
	if state == nil || state.PartialLine != "" || !isAgentUsageQueueFile(path) {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if info.Size() != state.Offset || info.Size() != state.FileSize {
		return
	}
	if err := os.Remove(path); err != nil {
		a.logger.Debug("API integrations ingester left consumed agent usage file in place", "path", path, "error", err)
		return
	}
	a.logger.Debug("API integrations ingester removed consumed agent usage file", "path", path)
}

func isAgentUsageQueueFile(path string) bool {
	name := filepath.Base(path)
	return name == "agent-usage.jsonl" || (strings.HasPrefix(name, "agent-usage-") && strings.HasSuffix(name, ".jsonl"))
}

func splitCompleteLines(data string) ([]string, string) {
	if data == "" {
		return nil, ""
	}
	if strings.HasSuffix(data, "\n") {
		lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
		return lines, ""
	}
	lines := strings.Split(data, "\n")
	if len(lines) == 1 {
		return nil, data
	}
	return lines[:len(lines)-1], lines[len(lines)-1]
}

func (a *APIIntegrationsIngestAgent) recordInvalidLine(path, line string, err error) {
	const provider = "api_integrations"
	const alertType = "ingest_error"
	const title = "API integrations ingest skipped invalid event"
	msg := fmt.Sprintf("%s: %v", filepath.Base(path), err)
	if exists, existsErr := a.store.HasActiveSystemAlert(provider, alertType, title, msg); existsErr != nil {
		a.logger.Warn("Failed to check duplicate API integrations ingest alert", "path", path, "error", existsErr)
	} else if exists {
		return
	}
	if len(line) > 180 {
		line = line[:180] + "..."
	}
	metadata := fmt.Sprintf(`{"source_path":%q,"line":%q}`, path, line)
	if _, createErr := a.store.CreateSystemAlert(provider, alertType, title, msg, "warning", metadata); createErr != nil {
		a.logger.Warn("Failed to create API integrations ingest alert", "path", path, "error", createErr)
	}
}

func (a *APIIntegrationsIngestAgent) pruneExpiredUsageEvents() error {
	if a.store == nil || a.retention <= 0 {
		return nil
	}

	now := time.Now().UTC()
	if !a.lastPrune.IsZero() && now.Sub(a.lastPrune) < a.pruneInterval {
		return nil
	}

	cutoff := now.Add(-a.retention)
	result, err := a.store.CompactAPIIntegrationUsageEvents(cutoff)
	if err != nil {
		return err
	}
	compactedMetadata, err := a.store.CompactAPIIntegrationMetadataJSON()
	if err != nil {
		return err
	}
	a.lastPrune = now
	if result.CompactedEvents > 0 {
		a.logger.Info(
			"API integrations usage retention compacted events",
			"compacted_events", result.CompactedEvents,
			"hourly_rows", result.HourlyRows,
			"cutoff", cutoff.Format(time.RFC3339),
		)
	}
	if compactedMetadata > 0 {
		a.logger.Info("API integration metadata compacted", "updated_events", compactedMetadata)
	}
	return nil
}
