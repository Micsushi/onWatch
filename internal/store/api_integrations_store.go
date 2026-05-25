package store

import (
	"database/sql"
	"errors"
	"fmt"
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

// InsertAPIIntegrationUsageEvent stores a normalized API integrations telemetry event.
func (s *Store) InsertAPIIntegrationUsageEvent(event *apiintegrations.UsageEvent) (int64, error) {
	if event == nil {
		return 0, fmt.Errorf("API integration usage event is nil")
	}
	res, err := s.db.Exec(`
		INSERT INTO api_integration_usage_events (
			captured_at, integration_name, provider, account_name, model, request_id,
			prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms,
			metadata_json, source_path, fingerprint, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		event.MetadataJSON,
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
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get API integration usage event id: %w", err)
	}
	s.bumpAPIIntegrationUsageVersion()
	return id, nil
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

func (s *Store) updateAPIIntegrationUsageEventMetadata(event *apiintegrations.UsageEvent) error {
	if event.MetadataJSON == "" && event.CostUSD == nil && event.LatencyMS == nil {
		return nil
	}
	_, err := s.db.Exec(`
		UPDATE api_integration_usage_events
		SET
			metadata_json = CASE
				WHEN ? != '' AND (metadata_json = '' OR metadata_json = '{}' OR metadata_json = 'null') THEN ?
				WHEN ? != '' AND json_valid(metadata_json) AND json_valid(?) THEN json_patch(metadata_json, ?)
				ELSE metadata_json
			END,
			cost_usd = COALESCE(cost_usd, ?),
			latency_ms = COALESCE(latency_ms, ?)
		WHERE fingerprint = ?
	`, event.MetadataJSON, event.MetadataJSON, event.MetadataJSON, event.MetadataJSON, event.MetadataJSON, event.CostUSD, event.LatencyMS, event.Fingerprint)
	if err != nil {
		return fmt.Errorf("failed to update duplicate API integration usage event metadata: %w", err)
	}
	return nil
}

// QueryAPIIntegrationUsageRange returns API integration usage events ordered by capture time ascending.
func (s *Store) QueryAPIIntegrationUsageRange(start, end time.Time, limit ...int) ([]apiintegrations.UsageEvent, error) {
	query := `
		SELECT captured_at, integration_name, provider, account_name, model, request_id,
		       prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms,
		       metadata_json, source_path, fingerprint
		FROM api_integration_usage_events
		WHERE captured_at BETWEEN ? AND ?
		ORDER BY captured_at ASC
	`
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
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.input_tokens'), prompt_tokens) AS INTEGER) ELSE prompt_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cached_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cache_creation_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.output_tokens'), completion_tokens) AS INTEGER) ELSE completion_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.reasoning_output_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
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
		WITH annotated AS (
			SELECT
				integration_name,
				provider,
				account_name,
				model,
				CASE
					WHEN json_valid(metadata_json) THEN COALESCE(NULLIF(json_extract(metadata_json, '$.reasoning_effort'), ''), 'unknown')
					ELSE 'unknown'
				END AS reasoning_effort,
				CASE
					WHEN json_valid(metadata_json) THEN COALESCE(NULLIF(json_extract(metadata_json, '$.mode'), ''), 'unknown')
					ELSE 'unknown'
				END AS mode,
				CASE
					WHEN json_valid(metadata_json) AND json_extract(metadata_json, '$.fast_mode') = 1 THEN 'fast'
					WHEN json_valid(metadata_json) AND json_extract(metadata_json, '$.fast_mode') = 0 THEN 'standard'
					WHEN json_valid(metadata_json) THEN COALESCE(NULLIF(json_extract(metadata_json, '$.speed_mode'), ''), 'unknown')
					ELSE 'unknown'
				END AS speed_mode,
				prompt_tokens,
				completion_tokens,
				total_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.input_tokens'), prompt_tokens) AS INTEGER)
					ELSE prompt_tokens
				END AS input_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cached_input_tokens'), 0) AS INTEGER)
					ELSE 0
				END AS cached_input_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cache_creation_input_tokens'), 0) AS INTEGER)
					ELSE 0
				END AS cache_creation_input_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.output_tokens'), completion_tokens) AS INTEGER)
					ELSE completion_tokens
				END AS output_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.reasoning_output_tokens'), 0) AS INTEGER)
					ELSE 0
				END AS reasoning_output_tokens,
				cost_usd,
				captured_at
			FROM api_integration_usage_events
		)
		SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
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
		FROM annotated
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
		WITH annotated AS (
			SELECT
				integration_name,
				provider,
				account_name,
				model,
				CASE
					WHEN json_valid(metadata_json) THEN COALESCE(NULLIF(json_extract(metadata_json, '$.reasoning_effort'), ''), 'unknown')
					ELSE 'unknown'
				END AS reasoning_effort,
				CASE
					WHEN json_valid(metadata_json) THEN COALESCE(NULLIF(json_extract(metadata_json, '$.mode'), ''), 'unknown')
					ELSE 'unknown'
				END AS mode,
				CASE
					WHEN json_valid(metadata_json) AND json_extract(metadata_json, '$.fast_mode') = 1 THEN 'fast'
					WHEN json_valid(metadata_json) AND json_extract(metadata_json, '$.fast_mode') = 0 THEN 'standard'
					WHEN json_valid(metadata_json) THEN COALESCE(NULLIF(json_extract(metadata_json, '$.speed_mode'), ''), 'unknown')
					ELSE 'unknown'
				END AS speed_mode,
				prompt_tokens,
				completion_tokens,
				total_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.input_tokens'), prompt_tokens) AS INTEGER)
					ELSE prompt_tokens
				END AS input_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cached_input_tokens'), 0) AS INTEGER)
					ELSE 0
				END AS cached_input_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cache_creation_input_tokens'), 0) AS INTEGER)
					ELSE 0
				END AS cache_creation_input_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.output_tokens'), completion_tokens) AS INTEGER)
					ELSE completion_tokens
				END AS output_tokens,
				CASE
					WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.reasoning_output_tokens'), 0) AS INTEGER)
					ELSE 0
				END AS reasoning_output_tokens,
				cost_usd,
				captured_at
			FROM api_integration_usage_events
			WHERE captured_at BETWEEN ? AND ?
	`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if integrationName != "" {
		query += ` AND integration_name = ?`
		args = append(args, integrationName)
	}
	query += `
		)
		SELECT integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode,
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
		FROM annotated
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
		SELECT integration_name,
		       strftime('%Y-%m-%dT%H:%M:%SZ', (CAST(strftime('%s', captured_at) AS INTEGER) / ?) * ?, 'unixepoch'),
		       COUNT(*),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.input_tokens'), prompt_tokens) AS INTEGER) ELSE prompt_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cached_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cache_creation_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.output_tokens'), completion_tokens) AS INTEGER) ELSE completion_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.reasoning_output_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(cost_usd), 0)
		FROM api_integration_usage_events
		WHERE captured_at BETWEEN ? AND ?
		GROUP BY integration_name, 2
		ORDER BY integration_name, 2
		LIMIT ?
	`, bucketSeconds, bucketSeconds, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), apiIntegrationUsageBucketsLimit)
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
		       COALESCE(NULLIF(json_extract(metadata_json, '$.session_id'), ''), source_path, 'unknown') AS session_id,
		       substr(captured_at, 1, 10) AS chat_date,
		       MIN(captured_at),
		       MAX(captured_at),
		       COUNT(*),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.input_tokens'), prompt_tokens) AS INTEGER) ELSE prompt_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cached_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cache_creation_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.output_tokens'), completion_tokens) AS INTEGER) ELSE completion_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.reasoning_output_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(cost_usd), 0)
		FROM api_integration_usage_events
		WHERE captured_at BETWEEN ? AND ?
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
		SELECT integration_name,
		       COUNT(*),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.input_tokens'), prompt_tokens) AS INTEGER) ELSE prompt_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cached_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.cache_creation_input_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.output_tokens'), completion_tokens) AS INTEGER) ELSE completion_tokens END), 0),
		       COALESCE(SUM(CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json, '$.reasoning_output_tokens'), 0) AS INTEGER) ELSE 0 END), 0),
		       COALESCE(SUM(cost_usd), 0),
		       MAX(captured_at)
		FROM api_integration_usage_events
		WHERE captured_at BETWEEN ? AND ?
	`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if integrationName != "" {
		query += ` AND integration_name = ?`
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
