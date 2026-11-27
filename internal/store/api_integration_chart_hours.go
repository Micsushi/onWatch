package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const chartHourLayout = "2006-01-02T15:00:00Z"

// The source hour follows the exact timestamp. The plotted hour retains the
// existing SQLite bucketing, including rounding at subsecond boundaries.
const chartSourceHour = "strftime('%Y-%m-%dT%H:00:00Z', substr(TIME, 1, 19) || CASE WHEN substr(TIME, -1) = 'Z' THEN 'Z' ELSE substr(TIME, -6) END)"

const chartEventTotals = `
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
	COUNT(cost_usd) AS priced_requests,
	MIN(captured_at COLLATE ONWATCH_RFC3339) AS first_at,
	MAX(captured_at COLLATE ONWATCH_RFC3339) AS last_at`

const chartHourInsert = `
	INSERT INTO api_integration_chart_hours (
		source_hour, chart_hour, integration_name, request_count, prompt_tokens,
		completion_tokens, total_tokens, input_tokens, cached_input_tokens,
		cache_creation_input_tokens, output_tokens, reasoning_output_tokens,
		total_cost_usd, priced_requests, first_at, last_at
	)
	SELECT `

func (s *Store) ensureAPIIntegrationChartHours() error {
	var exists bool
	if err := s.db.QueryRow("SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'api_integration_chart_hours')").Scan(&exists); err != nil || exists {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	oldHour := strings.ReplaceAll(chartSourceHour, "TIME", "OLD.captured_at")
	newHour := strings.ReplaceAll(chartSourceHour, "TIME", "NEW.captured_at")
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS api_integration_chart_hours (
			source_hour TEXT NOT NULL,
			chart_hour INTEGER NOT NULL,
			integration_name TEXT NOT NULL,
			request_count INTEGER NOT NULL,
			prompt_tokens INTEGER NOT NULL,
			completion_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			input_tokens INTEGER NOT NULL,
			cached_input_tokens INTEGER NOT NULL,
			cache_creation_input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_output_tokens INTEGER NOT NULL,
			total_cost_usd REAL NOT NULL,
			priced_requests INTEGER NOT NULL,
			first_at TEXT NOT NULL,
			last_at TEXT NOT NULL,
			PRIMARY KEY (source_hour, chart_hour, integration_name)
		) WITHOUT ROWID;

		CREATE TABLE IF NOT EXISTS api_integration_chart_dirty_hours (
			source_hour TEXT PRIMARY KEY
		) WITHOUT ROWID;

		CREATE TRIGGER IF NOT EXISTS api_integration_chart_insert
		AFTER INSERT ON api_integration_usage_events BEGIN
			INSERT OR IGNORE INTO api_integration_chart_dirty_hours VALUES (` + newHour + `);
		END;

		CREATE TRIGGER IF NOT EXISTS api_integration_chart_delete
		AFTER DELETE ON api_integration_usage_events BEGIN
			INSERT OR IGNORE INTO api_integration_chart_dirty_hours VALUES (` + oldHour + `);
		END;

		CREATE TRIGGER IF NOT EXISTS api_integration_chart_update
		AFTER UPDATE OF captured_at, integration_name, prompt_tokens, completion_tokens, total_tokens,
			input_tokens, cached_input_tokens, cache_creation_input_tokens, output_tokens,
			reasoning_output_tokens, cost_usd ON api_integration_usage_events BEGIN
			INSERT OR IGNORE INTO api_integration_chart_dirty_hours VALUES (` + oldHour + `), (` + newHour + `);
		END;
	`); err != nil {
		return err
	}
	hour := strings.ReplaceAll(chartSourceHour, "TIME", "captured_at")
	if _, err := tx.Exec(strings.Replace(chartHourInsert, "INSERT INTO", "INSERT OR IGNORE INTO", 1) + hour + ` AS source_hour,
		CAST(strftime('%s', captured_at) AS INTEGER) / 3600 * 3600 AS chart_hour, integration_name, ` + chartEventTotals + `
		FROM api_integration_usage_events
		WHERE ` + hour + ` IS NOT NULL AND strftime('%s', captured_at) IS NOT NULL
		GROUP BY source_hour, chart_hour, integration_name`); err != nil {
		return fmt.Errorf("populate API integration chart hours: %w", err)
	}
	return tx.Commit()
}

func rebuildAPIIntegrationChartHours(tx *sql.Tx, from, to string) error {
	const dirtyRange = "source_hour >= ? AND source_hour < ?"
	// Begin with a write so a concurrent ingest can never invalidate the
	// snapshot between reading the dirty hours and saving rebuilt totals.
	if _, err := tx.Exec(`DELETE FROM api_integration_chart_hours WHERE source_hour IN (
		SELECT source_hour FROM api_integration_chart_dirty_hours WHERE `+dirtyRange+`)`, from, to); err != nil {
		return err
	}
	if _, err := tx.Exec(chartHourInsert+`source_hour,
		CAST(strftime('%s', captured_at) AS INTEGER) / 3600 * 3600 AS chart_hour, integration_name, `+chartEventTotals+`
		FROM api_integration_chart_dirty_hours
		CROSS JOIN api_integration_usage_events
			ON captured_at COLLATE ONWATCH_RFC3339 >= source_hour
			AND captured_at COLLATE ONWATCH_RFC3339 < strftime('%Y-%m-%dT%H:00:00Z', source_hour, '+1 hour')
		WHERE `+dirtyRange+` AND strftime('%s', captured_at) IS NOT NULL
		GROUP BY source_hour, chart_hour, integration_name`, from, to); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM api_integration_chart_dirty_hours WHERE "+dirtyRange, from, to)
	return err
}

func (s *Store) queryAPIIntegrationChartHours(start, end, fullStart, fullEnd time.Time, bucketSize time.Duration, integration string) ([]APIIntegrationUsageBucketRow, error) {
	from, to := fullStart.Format(chartHourLayout), fullEnd.Format(chartHourLayout)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var dirty bool
	if err := tx.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM api_integration_chart_dirty_hours WHERE source_hour >= ? AND source_hour < ?
	)`, from, to).Scan(&dirty); err != nil {
		return nil, err
	}
	if dirty {
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		tx, err = s.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err := rebuildAPIIntegrationChartHours(tx, from, to); err != nil {
			return nil, fmt.Errorf("rebuild API integration chart hours: %w", err)
		}
	}

	rows, err := queryAPIIntegrationUsageBuckets(tx, start, end, bucketSize, integration, -1, fullStart, fullEnd)
	if err != nil {
		return nil, err
	}
	return rows, tx.Commit()
}
