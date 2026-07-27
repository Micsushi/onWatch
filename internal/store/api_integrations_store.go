package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	apiIntegrationUsageSummaryLimit = 500
	apiIntegrationUsageBucketsLimit = 5000
)

// APIIntegrationUsageSummaryRow contains grouped usage totals for backend reporting.
type APIIntegrationUsageSummaryRow struct {
	IntegrationName   string
	Provider          string
	AccountName       string
	Model             string
	RequestCount      int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	InputTokens       int
	CachedTokens      int
	CacheCreateTokens int
	OutputTokens      int
	ReasoningTokens   int
	TotalCostUSD      float64
	LastCapturedAt    time.Time
}

// APIIntegrationUsageEffortSummaryRow groups usage by model plus recorded agent mode metadata.
type APIIntegrationUsageEffortSummaryRow struct {
	IntegrationName   string
	Provider          string
	AccountName       string
	Model             string
	ReasoningEffort   string
	Mode              string
	SpeedMode         string
	RequestCount      int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	InputTokens       int
	CachedTokens      int
	CacheCreateTokens int
	OutputTokens      int
	ReasoningTokens   int
	TotalCostUSD      float64
	LastCapturedAt    time.Time
}

// APIIntegrationUsageBucketRow contains aggregated usage for one integration and time bucket.
type APIIntegrationUsageBucketRow struct {
	IntegrationName   string
	BucketStart       time.Time
	RequestCount      int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	InputTokens       int
	CachedTokens      int
	CacheCreateTokens int
	OutputTokens      int
	ReasoningTokens   int
	TotalCostUSD      float64
}

// APIIntegrationUsageSessionRow groups usage by chat/session and day.
type APIIntegrationUsageSessionRow struct {
	IntegrationName   string
	SessionID         string
	ChatDate          string
	StartedAt         time.Time
	LastCapturedAt    time.Time
	RequestCount      int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	InputTokens       int
	CachedTokens      int
	CacheCreateTokens int
	OutputTokens      int
	ReasoningTokens   int
	TotalCostUSD      float64
}

// APIIntegrationUsageTotalsRow contains full range totals for one integration.
type APIIntegrationUsageTotalsRow struct {
	IntegrationName   string
	RequestCount      int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	InputTokens       int
	CachedTokens      int
	CacheCreateTokens int
	OutputTokens      int
	ReasoningTokens   int
	TotalCostUSD      float64
	LastCapturedAt    time.Time
}

// APIIntegrationIngestHealthRow contains persisted ingest state with last seen event time.
type APIIntegrationIngestHealthRow struct {
	SourcePath     string
	OffsetBytes    int64
	FileSize       int64
	FileModTime    *time.Time
	PartialLine    string
	UpdatedAt      time.Time
	LastCapturedAt *time.Time
}

type APIIntegrationCompactionResult struct {
	CompactedEvents int64
	HourlyRows      int64
	Cutoff          time.Time
}

type apiIntegrationNormalizedMetadata struct {
	SessionID                string `json:"session_id"`
	ReasoningEffort          string `json:"reasoning_effort"`
	Mode                     string `json:"mode"`
	SpeedMode                string `json:"speed_mode"`
	FastMode                 *bool  `json:"fast_mode"`
	InputTokens              *int   `json:"input_tokens"`
	CachedInputTokens        *int   `json:"cached_input_tokens"`
	CacheCreationInputTokens *int   `json:"cache_creation_input_tokens"`
	OutputTokens             *int   `json:"output_tokens"`
	ReasoningOutputTokens    *int   `json:"reasoning_output_tokens"`
}

func normalizedAPIIntegrationMetadata(event *apiintegrations.UsageEvent) (apiIntegrationNormalizedMetadata, string) {
	normalized := apiIntegrationNormalizedMetadata{
		ReasoningEffort: "unknown",
		Mode:            "unknown",
		SpeedMode:       "unknown",
	}
	if event == nil {
		return normalized, ""
	}

	var stored map[string]interface{}
	if event.MetadataJSON != "" && json.Unmarshal([]byte(event.MetadataJSON), &stored) == nil {
		_ = json.Unmarshal([]byte(event.MetadataJSON), &normalized)
		delete(stored, "event_key")
		delete(stored, "source_path")
		delete(stored, "source")
		delete(stored, "session_id")
		delete(stored, "reasoning_effort")
		delete(stored, "mode")
		delete(stored, "speed_mode")
		delete(stored, "fast_mode")
		delete(stored, "input_tokens")
		delete(stored, "cached_input_tokens")
		delete(stored, "cache_creation_input_tokens")
		delete(stored, "output_tokens")
		delete(stored, "reasoning_output_tokens")
	}
	normalized.SessionID = strings.TrimSpace(normalized.SessionID)
	normalized.ReasoningEffort = strings.TrimSpace(normalized.ReasoningEffort)
	if normalized.ReasoningEffort == "" {
		normalized.ReasoningEffort = "unknown"
	}
	normalized.Mode = strings.TrimSpace(normalized.Mode)
	if normalized.Mode == "" {
		normalized.Mode = "unknown"
	}
	if normalized.FastMode != nil {
		if *normalized.FastMode {
			normalized.SpeedMode = "fast"
		} else {
			normalized.SpeedMode = "standard"
		}
	} else {
		normalized.SpeedMode = strings.TrimSpace(normalized.SpeedMode)
		if normalized.SpeedMode == "" {
			normalized.SpeedMode = "unknown"
		}
	}

	storedJSON := ""
	if len(stored) > 0 {
		if encoded, err := json.Marshal(stored); err == nil && string(encoded) != "{}" {
			storedJSON = string(encoded)
		}
	}
	return normalized, storedJSON
}

func normalizedTokenValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func apiIntegrationMetadataJSONExpression(alias string) string {
	return fmt.Sprintf(`
		json_patch(
			CASE WHEN json_valid(%[1]s.metadata_json) THEN %[1]s.metadata_json ELSE '{}' END,
			json_object(
				'session_id', NULLIF(%[1]s.session_id, ''),
				'reasoning_effort', NULLIF(%[1]s.reasoning_effort, 'unknown'),
				'mode', NULLIF(%[1]s.mode, 'unknown'),
				'speed_mode', NULLIF(%[1]s.speed_mode, 'unknown'),
				'fast_mode', CASE
					WHEN %[1]s.speed_mode = 'fast' THEN json('true')
					WHEN %[1]s.speed_mode = 'standard' THEN json('false')
					ELSE NULL
				END,
				'input_tokens', %[1]s.input_tokens,
				'cached_input_tokens', %[1]s.cached_input_tokens,
				'cache_creation_input_tokens', %[1]s.cache_creation_input_tokens,
				'output_tokens', %[1]s.output_tokens,
				'reasoning_output_tokens', %[1]s.reasoning_output_tokens
			)
		)
	`, alias)
}

// InsertAPIIntegrationUsageEvent stores a normalized API integrations telemetry event.
func (s *Store) InsertAPIIntegrationUsageEvent(event *apiintegrations.UsageEvent) (int64, error) {
	if event == nil {
		return 0, fmt.Errorf("API integration usage event is nil")
	}
	metadata, storedMetadataJSON := normalizedAPIIntegrationMetadata(event)
	res, err := s.db.Exec(`
		INSERT INTO api_integration_usage_events (
			captured_at, integration_name, provider, account_name, model, request_id,
			prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms,
			metadata_json, session_id, reasoning_effort, mode, speed_mode,
			input_tokens, cached_input_tokens, cache_creation_input_tokens,
			output_tokens, reasoning_output_tokens,
			source_path, fingerprint, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.Timestamp.Format(time.RFC3339Nano),
		event.Integration,
		event.Provider,
		event.Account,
		event.Model,
		event.RequestID,
		event.PromptTokens,
		event.CompletionTokens,
		event.TotalTokens,
		event.CostUSD,
		event.LatencyMS,
		storedMetadataJSON,
		metadata.SessionID,
		metadata.ReasoningEffort,
		metadata.Mode,
		metadata.SpeedMode,
		normalizedTokenValue(metadata.InputTokens, event.PromptTokens),
		normalizedTokenValue(metadata.CachedInputTokens, 0),
		normalizedTokenValue(metadata.CacheCreationInputTokens, 0),
		normalizedTokenValue(metadata.OutputTokens, event.CompletionTokens),
		normalizedTokenValue(metadata.ReasoningOutputTokens, 0),
		event.SourcePath,
		event.Fingerprint,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isSQLiteUniqueConstraintError(err) {
			if updateErr := s.updateAPIIntegrationUsageEventMetadata(event); updateErr != nil {
				return 0, updateErr
			}
			s.bumpAPIIntegrationUsageVersion()
			return 0, ErrDuplicateAPIIntegrationUsageEvent
		}
		return 0, fmt.Errorf("failed to insert API integration usage event: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count inserted API integration usage event: %w", err)
	}
	if affected == 0 {
		return 0, ErrDuplicateAPIIntegrationUsageEvent
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get API integration usage event id: %w", err)
	}
	s.bumpAPIIntegrationUsageVersion()
	return id, nil
}

// CompactAPIIntegrationUsageEvents aggregates detailed events before cutoff into
// hourly rows and retains compacted fingerprints so source replays stay idempotent.
func (s *Store) CompactAPIIntegrationUsageEvents(cutoff time.Time) (APIIntegrationCompactionResult, error) {
	result := APIIntegrationCompactionResult{Cutoff: cutoff.UTC()}
	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin API integration compaction: %w", err)
	}
	defer tx.Rollback()

	cutoffRaw := result.Cutoff.Format(time.RFC3339Nano)
	var rawCount, rawTotalTokens int64
	var rawCost float64
	if err := tx.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
		FROM api_integration_usage_events
		WHERE captured_at < ?
	`, cutoffRaw).Scan(&rawCount, &rawTotalTokens, &rawCost); err != nil {
		return result, fmt.Errorf("inspect API integration compaction candidates: %w", err)
	}
	if rawCount == 0 {
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit empty API integration compaction: %w", err)
		}
		return result, nil
	}

	if _, err := tx.Exec(`DROP TABLE IF EXISTS temp_api_integration_usage_compaction`); err != nil {
		return result, fmt.Errorf("reset API integration compaction workspace: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TEMP TABLE temp_api_integration_usage_compaction AS
		WITH annotated AS (
			SELECT
				substr(captured_at, 1, 13) || ':00:00Z' AS hour_start,
				integration_name,
				provider,
				account_name,
				model,
				reasoning_effort,
				mode,
				speed_mode,
				prompt_tokens,
				completion_tokens,
				total_tokens,
				input_tokens,
				cached_input_tokens,
				cache_creation_input_tokens,
				output_tokens,
				reasoning_output_tokens,
				cost_usd,
				captured_at
			FROM api_integration_usage_events
			WHERE captured_at < ?
		)
		SELECT
			hour_start, integration_name, provider, account_name, model,
			reasoning_effort, mode, speed_mode,
			COUNT(*) AS request_count,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(cached_input_tokens), 0) AS cached_input_tokens,
			COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(reasoning_output_tokens), 0) AS reasoning_output_tokens,
			COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			MIN(captured_at) AS first_captured_at,
			MAX(captured_at) AS last_captured_at
		FROM annotated
		GROUP BY hour_start, integration_name, provider, account_name, model,
			reasoning_effort, mode, speed_mode
	`, cutoffRaw); err != nil {
		return result, fmt.Errorf("aggregate API integration compaction candidates: %w", err)
	}

	var compactedCount, compactedTotalTokens, hourlyRows int64
	var compactedCost float64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(request_count), 0), COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(total_cost_usd), 0), COUNT(*)
		FROM temp_api_integration_usage_compaction
	`).Scan(&compactedCount, &compactedTotalTokens, &compactedCost, &hourlyRows); err != nil {
		return result, fmt.Errorf("verify API integration compaction totals: %w", err)
	}
	if rawCount != compactedCount || rawTotalTokens != compactedTotalTokens || math.Abs(rawCost-compactedCost) > 0.000001 {
		return result, fmt.Errorf(
			"API integration compaction totals mismatch: raw=(%d,%d,%.6f) compacted=(%d,%d,%.6f)",
			rawCount, rawTotalTokens, rawCost, compactedCount, compactedTotalTokens, compactedCost,
		)
	}

	if _, err := tx.Exec(`
		INSERT INTO api_integration_usage_hourly (
			origin_scope, hour_start, integration_name, provider, account_name, model,
			reasoning_effort, mode, speed_mode, request_count,
			prompt_tokens, completion_tokens, total_tokens, input_tokens,
			cached_input_tokens, cache_creation_input_tokens, output_tokens,
			reasoning_output_tokens, total_cost_usd, first_captured_at, last_captured_at
		)
		SELECT
			'local', hour_start, integration_name, provider, account_name, model,
			reasoning_effort, mode, speed_mode, request_count,
			prompt_tokens, completion_tokens, total_tokens, input_tokens,
			cached_input_tokens, cache_creation_input_tokens, output_tokens,
			reasoning_output_tokens, total_cost_usd, first_captured_at, last_captured_at
		FROM temp_api_integration_usage_compaction
		WHERE 1
		ON CONFLICT (
			origin_scope, hour_start, integration_name, provider, account_name, model,
			reasoning_effort, mode, speed_mode
		) DO UPDATE SET
			request_count = request_count + excluded.request_count,
			prompt_tokens = prompt_tokens + excluded.prompt_tokens,
			completion_tokens = completion_tokens + excluded.completion_tokens,
			total_tokens = total_tokens + excluded.total_tokens,
			input_tokens = input_tokens + excluded.input_tokens,
			cached_input_tokens = cached_input_tokens + excluded.cached_input_tokens,
			cache_creation_input_tokens = cache_creation_input_tokens + excluded.cache_creation_input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			reasoning_output_tokens = reasoning_output_tokens + excluded.reasoning_output_tokens,
			total_cost_usd = total_cost_usd + excluded.total_cost_usd,
			first_captured_at = MIN(first_captured_at, excluded.first_captured_at),
			last_captured_at = MAX(last_captured_at, excluded.last_captured_at)
	`); err != nil {
		return result, fmt.Errorf("store API integration hourly archive: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO api_integration_usage_compacted_fingerprints (fingerprint, captured_at)
		SELECT fingerprint, captured_at
		FROM api_integration_usage_events
		WHERE captured_at < ?
	`, cutoffRaw); err != nil {
		return result, fmt.Errorf("store compacted API integration fingerprints: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM data_transfer_records
		WHERE table_name = 'api_integration_usage_events'
		  AND local_record_id IN (
			SELECT CAST(id AS TEXT)
			FROM api_integration_usage_events
			WHERE captured_at < ?
		  )
	`, cutoffRaw); err != nil {
		return result, fmt.Errorf("clean compacted API integration transfer records: %w", err)
	}
	deleteResult, err := tx.Exec(`
		DELETE FROM api_integration_usage_events
		WHERE captured_at < ?
	`, cutoffRaw)
	if err != nil {
		return result, fmt.Errorf("delete compacted API integration usage events: %w", err)
	}
	deleted, err := deleteResult.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("count compacted API integration usage events: %w", err)
	}
	if deleted != rawCount {
		return result, fmt.Errorf("API integration compaction deleted %d events, expected %d", deleted, rawCount)
	}
	if _, err := tx.Exec(`DROP TABLE temp_api_integration_usage_compaction`); err != nil {
		return result, fmt.Errorf("clear API integration compaction workspace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit API integration compaction: %w", err)
	}

	result.CompactedEvents = deleted
	result.HourlyRows = hourlyRows
	s.bumpAPIIntegrationUsageVersion()
	return result, nil
}

// APIIntegrationUsageVersion changes whenever stored API integration usage changes.
func (s *Store) APIIntegrationUsageVersion() uint64 {
	if s == nil {
		return 0
	}
	return s.apiIntegrationUsageVersion.Load()
}

func (s *Store) bumpAPIIntegrationUsageVersion() {
	if s != nil {
		s.apiIntegrationUsageVersion.Add(1)
	}
}

// CompactAPIIntegrationMetadataJSON removes fields duplicated in normalized
// columns. New writes are already compact; the marker makes this a one-time
// cleanup for databases created by earlier versions.
func (s *Store) CompactAPIIntegrationMetadataJSON() (int64, error) {
	const marker = "api_integration_metadata_compaction_v1"
	var completed string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, marker).Scan(&completed)
	if err == nil && completed == "1" {
		return 0, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check API integration metadata compaction: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin API integration metadata compaction: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.Exec(`
		UPDATE api_integration_usage_events
		SET metadata_json = json_remove(
			metadata_json,
			'$.event_key', '$.source_path', '$.source',
			'$.session_id', '$.reasoning_effort', '$.mode', '$.speed_mode', '$.fast_mode',
			'$.input_tokens', '$.cached_input_tokens', '$.cache_creation_input_tokens',
			'$.output_tokens', '$.reasoning_output_tokens'
		)
		WHERE json_valid(metadata_json)
		  AND (
			json_type(metadata_json, '$.event_key') IS NOT NULL
			OR json_type(metadata_json, '$.source_path') IS NOT NULL
			OR json_type(metadata_json, '$.source') IS NOT NULL
			OR json_type(metadata_json, '$.session_id') IS NOT NULL
			OR json_type(metadata_json, '$.reasoning_effort') IS NOT NULL
			OR json_type(metadata_json, '$.mode') IS NOT NULL
			OR json_type(metadata_json, '$.speed_mode') IS NOT NULL
			OR json_type(metadata_json, '$.fast_mode') IS NOT NULL
			OR json_type(metadata_json, '$.input_tokens') IS NOT NULL
			OR json_type(metadata_json, '$.cached_input_tokens') IS NOT NULL
			OR json_type(metadata_json, '$.cache_creation_input_tokens') IS NOT NULL
			OR json_type(metadata_json, '$.output_tokens') IS NOT NULL
			OR json_type(metadata_json, '$.reasoning_output_tokens') IS NOT NULL
		  )
	`)
	if err != nil {
		return 0, fmt.Errorf("compact API integration metadata JSON: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count compacted API integration metadata JSON: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO settings (key, value) VALUES (?, '1')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, marker); err != nil {
		return 0, fmt.Errorf("record API integration metadata compaction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit API integration metadata compaction: %w", err)
	}
	return updated, nil
}

func (s *Store) updateAPIIntegrationUsageEventMetadata(event *apiintegrations.UsageEvent) error {
	if event.MetadataJSON == "" && event.CostUSD == nil && event.LatencyMS == nil {
		return nil
	}
	metadata, storedMetadataJSON := normalizedAPIIntegrationMetadata(event)
	_, err := s.db.Exec(`
		UPDATE api_integration_usage_events
		SET
			metadata_json = CASE
				WHEN ? != '' AND (metadata_json = '' OR metadata_json = '{}' OR metadata_json = 'null') THEN ?
				WHEN ? != '' AND json_valid(metadata_json) AND json_valid(?) THEN json_patch(metadata_json, ?)
				ELSE metadata_json
			END,
			session_id = CASE WHEN ? != '' THEN ? ELSE session_id END,
			reasoning_effort = CASE WHEN ? != 'unknown' THEN ? ELSE reasoning_effort END,
			mode = CASE WHEN ? != 'unknown' THEN ? ELSE mode END,
			speed_mode = CASE WHEN ? != 'unknown' THEN ? ELSE speed_mode END,
			input_tokens = ?,
			cached_input_tokens = ?,
			cache_creation_input_tokens = ?,
			output_tokens = ?,
			reasoning_output_tokens = ?,
			cost_usd = COALESCE(cost_usd, ?),
			latency_ms = COALESCE(latency_ms, ?)
			WHERE fingerprint = ?
	`,
		storedMetadataJSON,
		storedMetadataJSON,
		storedMetadataJSON,
		storedMetadataJSON,
		storedMetadataJSON,
		metadata.SessionID,
		metadata.SessionID,
		metadata.ReasoningEffort,
		metadata.ReasoningEffort,
		metadata.Mode,
		metadata.Mode,
		metadata.SpeedMode,
		metadata.SpeedMode,
		normalizedTokenValue(metadata.InputTokens, event.PromptTokens),
		normalizedTokenValue(metadata.CachedInputTokens, 0),
		normalizedTokenValue(metadata.CacheCreationInputTokens, 0),
		normalizedTokenValue(metadata.OutputTokens, event.CompletionTokens),
		normalizedTokenValue(metadata.ReasoningOutputTokens, 0),
		event.CostUSD,
		event.LatencyMS,
		event.Fingerprint,
	)
	if err != nil {
		return fmt.Errorf("failed to update duplicate API integration usage event metadata: %w", err)
	}
	return nil
}

// QueryAPIIntegrationUsageRange returns API integration usage events ordered by capture time ascending.
func (s *Store) QueryAPIIntegrationUsageRange(start, end time.Time, limit ...int) ([]apiintegrations.UsageEvent, error) {
	query := fmt.Sprintf(`
		SELECT source.captured_at, source.integration_name, source.provider, source.account_name,
		       source.model, source.request_id, source.prompt_tokens, source.completion_tokens,
		       source.total_tokens, source.cost_usd, source.latency_ms, %s,
		       source.source_path, source.fingerprint
		FROM api_integration_usage_events AS source
		WHERE source.captured_at >= ? AND source.captured_at < ?
		ORDER BY source.captured_at ASC
	`, apiIntegrationMetadataJSONExpression("source"))
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration usage range: %w", err)
	}
	defer rows.Close()

	var events []apiintegrations.UsageEvent
	for rows.Next() {
		var event apiintegrations.UsageEvent
		var capturedAt string
		var costUSD sql.NullFloat64
		var latencyMS sql.NullInt64
		if err := rows.Scan(
			&capturedAt,
			&event.Integration,
			&event.Provider,
			&event.Account,
			&event.Model,
			&event.RequestID,
			&event.PromptTokens,
			&event.CompletionTokens,
			&event.TotalTokens,
			&costUSD,
			&latencyMS,
			&event.MetadataJSON,
			&event.SourcePath,
			&event.Fingerprint,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration usage event: %w", err)
		}
		event.Timestamp, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if costUSD.Valid {
			v := costUSD.Float64
			event.CostUSD = &v
		}
		if latencyMS.Valid {
			v := int(latencyMS.Int64)
			event.LatencyMS = &v
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// DeleteAPIIntegrationUsageEventsOlderThan removes stored usage events older than the cutoff.
func (s *Store) DeleteAPIIntegrationUsageEventsOlderThan(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`
		DELETE FROM api_integration_usage_events
		WHERE captured_at < ?
	`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired API integration usage events: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count deleted API integration usage events: %w", err)
	}
	if deleted > 0 {
		s.bumpAPIIntegrationUsageVersion()
	}
	return deleted, nil
}

// QueryAPIIntegrationUsageSummary groups usage by integration/provider/account/model.
func (s *Store) QueryAPIIntegrationUsageSummary() ([]APIIntegrationUsageSummaryRow, error) {
	rows, err := s.db.Query(`
		SELECT integration_name, provider, account_name, model,
		       COUNT(*),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0),
		       COALESCE(SUM(cost_usd), 0),
		       MAX(captured_at)
		FROM api_integration_usage_events
		GROUP BY integration_name, provider, account_name, model
		ORDER BY integration_name, provider, account_name, model
		LIMIT ?
	`, apiIntegrationUsageSummaryLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration usage summary: %w", err)
	}
	defer rows.Close()

	var summary []APIIntegrationUsageSummaryRow
	for rows.Next() {
		var row APIIntegrationUsageSummaryRow
		var lastCapturedAt string
		if err := rows.Scan(
			&row.IntegrationName,
			&row.Provider,
			&row.AccountName,
			&row.Model,
			&row.RequestCount,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.InputTokens,
			&row.CachedTokens,
			&row.CacheCreateTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.TotalCostUSD,
			&lastCapturedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration usage summary: %w", err)
		}
		row.LastCapturedAt, _ = time.Parse(time.RFC3339Nano, lastCapturedAt)
		summary = append(summary, row)
	}
	return summary, rows.Err()
}

// QueryAPIIntegrationUsageEffortSummary groups usage by integration/provider/account/model/effort/mode.
func (s *Store) QueryAPIIntegrationUsageEffortSummary() ([]APIIntegrationUsageEffortSummaryRow, error) {
	rows, err := s.db.Query(`
		WITH raw_annotated AS (
			SELECT
				integration_name,
				provider,
				account_name,
				model,
				reasoning_effort,
				mode,
				speed_mode,
				prompt_tokens,
				completion_tokens,
				total_tokens,
				input_tokens,
				cached_input_tokens,
				cache_creation_input_tokens,
				output_tokens,
				reasoning_output_tokens,
				cost_usd,
				captured_at
			FROM api_integration_usage_events
		),
		combined AS (
			SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
			       COUNT(*) AS request_count,
			       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			       COALESCE(SUM(total_tokens), 0) AS total_tokens,
			       COALESCE(SUM(input_tokens), 0) AS input_tokens,
			       COALESCE(SUM(cached_input_tokens), 0) AS cached_input_tokens,
			       COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			       COALESCE(SUM(output_tokens), 0) AS output_tokens,
			       COALESCE(SUM(reasoning_output_tokens), 0) AS reasoning_output_tokens,
			       COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			       MAX(captured_at) AS last_captured_at
			FROM raw_annotated
			GROUP BY integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode
			UNION ALL
			SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
			       request_count, prompt_tokens, completion_tokens, total_tokens, input_tokens,
			       cached_input_tokens, cache_creation_input_tokens, output_tokens,
			       reasoning_output_tokens, total_cost_usd, last_captured_at
			FROM api_integration_usage_hourly
		)
		SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
		       COALESCE(SUM(request_count), 0),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0),
		       COALESCE(SUM(total_cost_usd), 0),
		       MAX(last_captured_at)
		FROM combined
		GROUP BY integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode
		ORDER BY integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode
		LIMIT ?
	`, apiIntegrationUsageSummaryLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration effort summary: %w", err)
	}
	defer rows.Close()

	var summary []APIIntegrationUsageEffortSummaryRow
	for rows.Next() {
		var row APIIntegrationUsageEffortSummaryRow
		var lastCapturedAt string
		if err := rows.Scan(
			&row.IntegrationName,
			&row.Provider,
			&row.AccountName,
			&row.Model,
			&row.ReasoningEffort,
			&row.Mode,
			&row.SpeedMode,
			&row.RequestCount,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.InputTokens,
			&row.CachedTokens,
			&row.CacheCreateTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.TotalCostUSD,
			&lastCapturedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration effort summary: %w", err)
		}
		row.LastCapturedAt, _ = time.Parse(time.RFC3339Nano, lastCapturedAt)
		summary = append(summary, row)
	}
	return summary, rows.Err()
}

// QueryAPIIntegrationUsageEffortTotals groups usage over a range by integration/provider/account/model/effort/mode.
func (s *Store) QueryAPIIntegrationUsageEffortTotals(start, end time.Time, integrationName string) ([]APIIntegrationUsageEffortSummaryRow, error) {
	query := `
		WITH raw_annotated AS (
			SELECT
				integration_name,
				provider,
				account_name,
				model,
				reasoning_effort,
				mode,
				speed_mode,
				prompt_tokens,
				completion_tokens,
				total_tokens,
				input_tokens,
				cached_input_tokens,
				cache_creation_input_tokens,
				output_tokens,
				reasoning_output_tokens,
				cost_usd,
				captured_at
			FROM api_integration_usage_events
			WHERE captured_at >= ? AND captured_at < ?
		),
		combined AS (
			SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
			       COUNT(*) AS request_count,
			       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			       COALESCE(SUM(total_tokens), 0) AS total_tokens,
			       COALESCE(SUM(input_tokens), 0) AS input_tokens,
			       COALESCE(SUM(cached_input_tokens), 0) AS cached_input_tokens,
			       COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			       COALESCE(SUM(output_tokens), 0) AS output_tokens,
			       COALESCE(SUM(reasoning_output_tokens), 0) AS reasoning_output_tokens,
			       COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			       MAX(captured_at) AS last_captured_at
			FROM raw_annotated
			GROUP BY integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode
			UNION ALL
			SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
			       request_count, prompt_tokens, completion_tokens, total_tokens, input_tokens,
			       cached_input_tokens, cache_creation_input_tokens, output_tokens,
			       reasoning_output_tokens, total_cost_usd, last_captured_at
			FROM api_integration_usage_hourly
			WHERE hour_start < ? AND last_captured_at >= ?
		)
		SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
		       COALESCE(SUM(request_count), 0),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0),
		       COALESCE(SUM(total_cost_usd), 0),
		       MAX(last_captured_at)
		FROM combined
	`
	startRaw := start.Format(time.RFC3339Nano)
	endRaw := end.Format(time.RFC3339Nano)
	args := []interface{}{startRaw, endRaw, endRaw, startRaw}
	if integrationName != "" {
		query += ` WHERE integration_name = ?`
		args = append(args, integrationName)
	}
	query += `
		GROUP BY integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode
		ORDER BY integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode
		LIMIT ?
	`
	args = append(args, apiIntegrationUsageSummaryLimit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration effort range totals: %w", err)
	}
	defer rows.Close()

	var summary []APIIntegrationUsageEffortSummaryRow
	for rows.Next() {
		var row APIIntegrationUsageEffortSummaryRow
		var lastCapturedAt string
		if err := rows.Scan(
			&row.IntegrationName,
			&row.Provider,
			&row.AccountName,
			&row.Model,
			&row.ReasoningEffort,
			&row.Mode,
			&row.SpeedMode,
			&row.RequestCount,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.InputTokens,
			&row.CachedTokens,
			&row.CacheCreateTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.TotalCostUSD,
			&lastCapturedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration effort range totals: %w", err)
		}
		row.LastCapturedAt, _ = time.Parse(time.RFC3339Nano, lastCapturedAt)
		summary = append(summary, row)
	}
	return summary, rows.Err()
}

// QueryAPIIntegrationUsageBuckets groups usage into time buckets over a range.
func (s *Store) QueryAPIIntegrationUsageBuckets(start, end time.Time, bucketSize time.Duration) ([]APIIntegrationUsageBucketRow, error) {
	if bucketSize <= 0 {
		return nil, fmt.Errorf("bucket size must be positive")
	}

	bucketSeconds := int64(bucketSize / time.Second)
	rows, err := s.db.Query(`
		WITH combined AS (
			SELECT integration_name,
			       strftime('%Y-%m-%dT%H:%M:%SZ', (CAST(strftime('%s', captured_at) AS INTEGER) / ?) * ?, 'unixepoch') AS bucket_start,
			       COUNT(*) AS request_count,
			       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			       COALESCE(SUM(total_tokens), 0) AS total_tokens,
			       COALESCE(SUM(input_tokens), 0) AS input_tokens,
			       COALESCE(SUM(cached_input_tokens), 0) AS cached_input_tokens,
			       COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			       COALESCE(SUM(output_tokens), 0) AS output_tokens,
			       COALESCE(SUM(reasoning_output_tokens), 0) AS reasoning_output_tokens,
			       COALESCE(SUM(cost_usd), 0) AS total_cost_usd
			FROM api_integration_usage_events
			WHERE captured_at >= ? AND captured_at < ?
			GROUP BY integration_name, bucket_start
			UNION ALL
			SELECT integration_name,
			       strftime('%Y-%m-%dT%H:%M:%SZ', (CAST(strftime('%s', hour_start) AS INTEGER) / ?) * ?, 'unixepoch') AS bucket_start,
			       COALESCE(SUM(request_count), 0),
			       COALESCE(SUM(prompt_tokens), 0),
			       COALESCE(SUM(completion_tokens), 0),
			       COALESCE(SUM(total_tokens), 0),
			       COALESCE(SUM(input_tokens), 0),
			       COALESCE(SUM(cached_input_tokens), 0),
			       COALESCE(SUM(cache_creation_input_tokens), 0),
			       COALESCE(SUM(output_tokens), 0),
			       COALESCE(SUM(reasoning_output_tokens), 0),
			       COALESCE(SUM(total_cost_usd), 0)
			FROM api_integration_usage_hourly
			WHERE hour_start < ? AND last_captured_at >= ?
			GROUP BY integration_name, bucket_start
		)
		SELECT integration_name, bucket_start,
		       COALESCE(SUM(request_count), 0),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0),
		       COALESCE(SUM(total_cost_usd), 0)
		FROM combined
		GROUP BY integration_name, bucket_start
		ORDER BY integration_name, bucket_start
		LIMIT ?
	`, bucketSeconds, bucketSeconds, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano),
		bucketSeconds, bucketSeconds, end.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano),
		apiIntegrationUsageBucketsLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration usage buckets: %w", err)
	}
	defer rows.Close()

	var buckets []APIIntegrationUsageBucketRow
	for rows.Next() {
		var row APIIntegrationUsageBucketRow
		var bucketStart string
		if err := rows.Scan(
			&row.IntegrationName,
			&bucketStart,
			&row.RequestCount,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.InputTokens,
			&row.CachedTokens,
			&row.CacheCreateTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.TotalCostUSD,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration usage bucket: %w", err)
		}
		row.BucketStart, _ = time.Parse(time.RFC3339Nano, bucketStart)
		buckets = append(buckets, row)
	}
	return buckets, rows.Err()
}

// QueryAPIIntegrationUsageSessions groups usage by chat/session and calendar day.
func (s *Store) QueryAPIIntegrationUsageSessions(start, end time.Time, integrationName string, limit int) ([]APIIntegrationUsageSessionRow, error) {
	if limit <= 0 {
		limit = apiIntegrationUsageBucketsLimit
	}
	query := `
		SELECT integration_name,
		       COALESCE(NULLIF(session_id, ''), source_path, 'unknown') AS session_id,
		       substr(captured_at, 1, 10) AS chat_date,
		       MIN(captured_at),
		       MAX(captured_at),
		       COUNT(*),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0),
		       COALESCE(SUM(cost_usd), 0)
		FROM api_integration_usage_events
		WHERE captured_at >= ? AND captured_at < ?
	`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if integrationName != "" {
		query += ` AND integration_name = ?`
		args = append(args, integrationName)
	}
	query += `
		GROUP BY integration_name, session_id, chat_date
		ORDER BY MIN(captured_at) ASC
		LIMIT ?
	`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration usage sessions: %w", err)
	}
	defer rows.Close()

	var sessions []APIIntegrationUsageSessionRow
	for rows.Next() {
		var row APIIntegrationUsageSessionRow
		var startedAt, lastCapturedAt string
		if err := rows.Scan(
			&row.IntegrationName,
			&row.SessionID,
			&row.ChatDate,
			&startedAt,
			&lastCapturedAt,
			&row.RequestCount,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.InputTokens,
			&row.CachedTokens,
			&row.CacheCreateTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.TotalCostUSD,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration usage session: %w", err)
		}
		row.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		row.LastCapturedAt, _ = time.Parse(time.RFC3339Nano, lastCapturedAt)
		sessions = append(sessions, row)
	}
	return sessions, rows.Err()
}

// QueryAPIIntegrationUsageTotals groups usage into unsampled range totals by integration.
func (s *Store) QueryAPIIntegrationUsageTotals(start, end time.Time, integrationName string) ([]APIIntegrationUsageTotalsRow, error) {
	query := `
		WITH combined AS (
			SELECT integration_name,
			       COUNT(*) AS request_count,
			       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			       COALESCE(SUM(total_tokens), 0) AS total_tokens,
			       COALESCE(SUM(input_tokens), 0) AS input_tokens,
			       COALESCE(SUM(cached_input_tokens), 0) AS cached_input_tokens,
			       COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			       COALESCE(SUM(output_tokens), 0) AS output_tokens,
			       COALESCE(SUM(reasoning_output_tokens), 0) AS reasoning_output_tokens,
			       COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			       MAX(captured_at) AS last_captured_at
			FROM api_integration_usage_events
			WHERE captured_at >= ? AND captured_at < ?
			GROUP BY integration_name
			UNION ALL
			SELECT integration_name,
			       COALESCE(SUM(request_count), 0),
			       COALESCE(SUM(prompt_tokens), 0),
			       COALESCE(SUM(completion_tokens), 0),
			       COALESCE(SUM(total_tokens), 0),
			       COALESCE(SUM(input_tokens), 0),
			       COALESCE(SUM(cached_input_tokens), 0),
			       COALESCE(SUM(cache_creation_input_tokens), 0),
			       COALESCE(SUM(output_tokens), 0),
			       COALESCE(SUM(reasoning_output_tokens), 0),
			       COALESCE(SUM(total_cost_usd), 0),
			       MAX(last_captured_at)
			FROM api_integration_usage_hourly
			WHERE hour_start < ? AND last_captured_at >= ?
			GROUP BY integration_name
		)
		SELECT integration_name,
		       COALESCE(SUM(request_count), 0),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0),
		       COALESCE(SUM(total_cost_usd), 0),
		       MAX(last_captured_at)
		FROM combined
	`
	startRaw := start.Format(time.RFC3339Nano)
	endRaw := end.Format(time.RFC3339Nano)
	args := []interface{}{startRaw, endRaw, endRaw, startRaw}
	if integrationName != "" {
		query += ` WHERE integration_name = ?`
		args = append(args, integrationName)
	}
	query += `
		GROUP BY integration_name
		ORDER BY integration_name
	`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration usage totals: %w", err)
	}
	defer rows.Close()

	var totals []APIIntegrationUsageTotalsRow
	for rows.Next() {
		var row APIIntegrationUsageTotalsRow
		var lastCapturedAt sql.NullString
		if err := rows.Scan(
			&row.IntegrationName,
			&row.RequestCount,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.InputTokens,
			&row.CachedTokens,
			&row.CacheCreateTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.TotalCostUSD,
			&lastCapturedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration usage totals: %w", err)
		}
		if lastCapturedAt.Valid {
			row.LastCapturedAt, _ = time.Parse(time.RFC3339Nano, lastCapturedAt.String)
		}
		totals = append(totals, row)
	}
	return totals, rows.Err()
}

func (s *Store) APIIntegrationUsageUsesArchive(start, end time.Time) (bool, error) {
	var usesArchive bool
	err := s.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM api_integration_usage_hourly
			WHERE hour_start < ? AND last_captured_at >= ?
		)
	`, end.UTC().Format(time.RFC3339Nano), start.UTC().Format(time.RFC3339Nano)).Scan(&usesArchive)
	if err != nil {
		return false, fmt.Errorf("check API integration hourly archive range: %w", err)
	}
	return usesArchive, nil
}

// GetAPIIntegrationIngestState returns the persisted tail cursor for a source file.
func (s *Store) GetAPIIntegrationIngestState(sourcePath string) (*apiintegrations.IngestState, error) {
	var state apiintegrations.IngestState
	var modTime sql.NullString
	var partialLineBytes int64
	var updatedAt string
	err := s.db.QueryRow(`
		SELECT source_path, offset_bytes, file_size, file_mod_time,
		       CASE
		           WHEN length(CAST(partial_line AS BLOB)) > ? THEN ''
		           ELSE partial_line
		       END,
		       length(CAST(partial_line AS BLOB)),
		       updated_at
		FROM api_integration_ingest_state
		WHERE source_path = ?
	`, apiintegrations.MaxIngestPartialLineBytes, sourcePath).Scan(
		&state.SourcePath,
		&state.Offset,
		&state.FileSize,
		&modTime,
		&state.PartialLine,
		&partialLineBytes,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get API integration ingest state: %w", err)
	}
	state.PartialLineBytes = int(partialLineBytes)
	state.PartialLineOversized = partialLineBytes > apiintegrations.MaxIngestPartialLineBytes
	if modTime.Valid {
		state.FileModTime, _ = time.Parse(time.RFC3339Nano, modTime.String)
	}
	state.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &state, nil
}

// UpsertAPIIntegrationIngestState persists the current tail cursor for a source file.
func (s *Store) UpsertAPIIntegrationIngestState(state *apiintegrations.IngestState) error {
	if state == nil {
		return fmt.Errorf("API integration ingest state is nil")
	}
	var modTime interface{}
	if !state.FileModTime.IsZero() {
		modTime = state.FileModTime.Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO api_integration_ingest_state (source_path, offset_bytes, file_size, file_mod_time, partial_line, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_path) DO UPDATE SET
			offset_bytes = excluded.offset_bytes,
			file_size = excluded.file_size,
			file_mod_time = excluded.file_mod_time,
			partial_line = excluded.partial_line,
			updated_at = excluded.updated_at
	`, state.SourcePath, state.Offset, state.FileSize, modTime, state.PartialLine, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("failed to upsert API integration ingest state: %w", err)
	}
	return nil
}

// QueryAPIIntegrationIngestHealth returns ingest cursor state plus last event timestamp per file.
func (s *Store) QueryAPIIntegrationIngestHealth() ([]APIIntegrationIngestHealthRow, error) {
	rows, err := s.db.Query(`
		SELECT s.source_path, s.offset_bytes, s.file_size, s.file_mod_time, s.partial_line, s.updated_at,
		       MAX(e.captured_at) as last_captured_at
		FROM api_integration_ingest_state s
		LEFT JOIN api_integration_usage_events e ON e.source_path = s.source_path
		GROUP BY s.source_path, s.offset_bytes, s.file_size, s.file_mod_time, s.partial_line, s.updated_at
		ORDER BY s.source_path
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query API integration ingest health: %w", err)
	}
	defer rows.Close()

	var result []APIIntegrationIngestHealthRow
	for rows.Next() {
		var row APIIntegrationIngestHealthRow
		var fileModTime sql.NullString
		var updatedAt string
		var lastCapturedAt sql.NullString
		if err := rows.Scan(
			&row.SourcePath,
			&row.OffsetBytes,
			&row.FileSize,
			&fileModTime,
			&row.PartialLine,
			&updatedAt,
			&lastCapturedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan API integration ingest health row: %w", err)
		}
		if fileModTime.Valid {
			t, _ := time.Parse(time.RFC3339Nano, fileModTime.String)
			row.FileModTime = &t
		}
		row.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if lastCapturedAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, lastCapturedAt.String)
			row.LastCapturedAt = &t
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// GetActiveSystemAlertsByProvider returns active alerts for a provider, most recent first.
func (s *Store) GetActiveSystemAlertsByProvider(provider string, limit int) ([]SystemAlert, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT id, provider, alert_type, title, message, severity, created_at, metadata
		FROM system_alerts
		WHERE dismissed_at IS NULL AND provider = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("store.GetActiveSystemAlertsByProvider: %w", err)
	}
	defer rows.Close()

	var alerts []SystemAlert
	for rows.Next() {
		var a SystemAlert
		var createdAt, metadata string
		if err := rows.Scan(&a.ID, &a.Provider, &a.AlertType, &a.Title, &a.Message, &a.Severity, &createdAt, &metadata); err != nil {
			return nil, fmt.Errorf("store.GetActiveSystemAlertsByProvider: scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			a.CreatedAt = t
		}
		a.Metadata = metadata
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

func isSQLiteUniqueConstraintError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
