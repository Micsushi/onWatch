package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

const quotaChartDay = 24 * time.Hour
const quotaChartPointsPerDay = 72

// Use seconds and the original offset to avoid SQLite rounding a reading across midnight.
const quotaChartDaySQL = "unixepoch(date(substr(TIME, 1, 19) || CASE WHEN substr(TIME, -1) = 'Z' THEN 'Z' ELSE substr(TIME, -6) END))"

type quotaChartSource struct {
	provider string
	table    string
	// timestamp is an expression using ROW. Rows outside the source return NULL.
	timestamp string
	seed      string
}

var quotaChartSources = []quotaChartSource{
	{"anthropic", "anthropic_snapshots", "ROW.captured_at", "SELECT captured_at FROM anthropic_snapshots"},
	{"anthropic", "anthropic_quota_values", "(SELECT captured_at FROM anthropic_snapshots WHERE id=ROW.snapshot_id)", ""},
	{"codex", "codex_snapshots", "ROW.captured_at", "SELECT captured_at FROM codex_snapshots"},
	{"codex", "codex_quota_values", "(SELECT captured_at FROM codex_snapshots WHERE id=ROW.snapshot_id)", ""},
	{"antigravity", "antigravity_snapshots", "ROW.captured_at", "SELECT captured_at FROM antigravity_snapshots"},
	{"antigravity", "antigravity_model_values", "(SELECT captured_at FROM antigravity_snapshots WHERE id=ROW.snapshot_id)", ""},
	{"antigravity", "antigravity_quota_summary_buckets", "(SELECT captured_at FROM antigravity_snapshots WHERE id=ROW.snapshot_id)", ""},
	{"antigravity", "central_quota_snapshots", "CASE WHEN ROW.provider='antigravity' THEN ROW.captured_at END", "SELECT captured_at FROM central_quota_snapshots WHERE provider='antigravity'"},
	{"antigravity", "observation_provenance", "(SELECT captured_at FROM central_quota_snapshots WHERE ROW.target_table='central_quota_snapshots' AND id=CAST(ROW.target_record_id AS INTEGER) AND provider='antigravity')", ""},
	{"antigravity", "ingest_receipts", "(SELECT captured_at FROM central_quota_snapshots WHERE ROW.target_table='central_quota_snapshots' AND id=CAST(ROW.target_record_id AS INTEGER) AND provider='antigravity')", ""},
}

func (s *Store) ensureQuotaChartDays() error {
	var exists bool
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='quota_chart_days')").Scan(&exists); err != nil || exists {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS quota_chart_days (
			provider TEXT NOT NULL, series TEXT NOT NULL, day INTEGER NOT NULL, points BLOB NOT NULL,
			PRIMARY KEY(provider, day, series)
		) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS quota_chart_dirty_days (
			provider TEXT NOT NULL, day INTEGER NOT NULL, revision INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY(provider, day)
		) WITHOUT ROWID;`); err != nil {
		return err
	}

	for _, source := range quotaChartSources {
		if source.seed != "" {
			day := strings.ReplaceAll(quotaChartDaySQL, "TIME", "captured_at")
			if _, err := tx.Exec(`INSERT OR IGNORE INTO quota_chart_dirty_days(provider, day)
				SELECT ?, `+day+` FROM (`+source.seed+`) WHERE `+day+` IS NOT NULL GROUP BY 2`, source.provider); err != nil {
				return err
			}
		}
		mark := func(row string) string {
			timestamp := strings.ReplaceAll(source.timestamp, "ROW", row)
			return `INSERT INTO quota_chart_dirty_days(provider, day)
				SELECT '` + source.provider + `', day FROM (SELECT ` + strings.ReplaceAll(quotaChartDaySQL, "TIME", "captured") +
				` AS day FROM (SELECT ` + timestamp + ` AS captured)) WHERE day IS NOT NULL
				ON CONFLICT(provider, day) DO UPDATE SET revision=revision+1;`
		}
		for operation, body := range map[string]string{
			"INSERT": mark("NEW"),
			"UPDATE": mark("OLD") + mark("NEW"),
			"DELETE": mark("OLD"),
		} {
			if _, err := tx.Exec(`CREATE TRIGGER IF NOT EXISTS quota_chart_` + source.table + "_" + operation +
				` AFTER ` + operation + ` ON ` + source.table + ` BEGIN ` + body + ` END`); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type quotaChartChunk struct {
	series string
	points []byte
}

func encodeQuotaChart[T any](points []*T) ([]byte, error) {
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(points)
	return buf.Bytes(), err
}

// MaintainQuotaChartHistory prepares dirty days outside the request path.
func (s *Store) MaintainQuotaChartHistory(ctx context.Context, logger *slog.Logger) {
	for ctx.Err() == nil {
		worked, err := s.prepareQuotaChartDay(ctx)
		delay := 5 * time.Second
		if err != nil && ctx.Err() == nil {
			logger.Warn("could not prepare quota chart history", "error", err)
		} else if worked {
			delay = 10 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Store) prepareQuotaChartDay(ctx context.Context) (bool, error) {
	var provider string
	var day, revision int64
	err := s.db.QueryRowContext(ctx, "SELECT provider, day, revision FROM quota_chart_dirty_days ORDER BY day DESC, provider LIMIT 1").Scan(&provider, &day, &revision)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	start := time.Unix(day, 0).UTC()
	chunks, err := s.buildQuotaChartChunks(ctx, provider, start, start.Add(quotaChartDay))
	if err != nil {
		return true, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	// Publish only when the source day still has the revision we read.
	result, err := tx.ExecContext(ctx, "DELETE FROM quota_chart_dirty_days WHERE provider=? AND day=? AND revision=?", provider, day, revision)
	if err != nil {
		return true, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM quota_chart_days WHERE provider=? AND day=?", provider, day); err != nil {
		return true, err
	}
	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, "INSERT INTO quota_chart_days(provider, day, series, points) VALUES(?, ?, ?, ?)",
			provider, day, chunk.series, chunk.points); err != nil {
			return true, err
		}
	}
	return true, tx.Commit()
}

type quotaChartRange struct{ start, end time.Time }

type quotaChartSnapshot interface {
	api.AnthropicSnapshot | api.CodexSnapshot | api.AntigravitySnapshot
}

// Keep narrow ranges exact. Wide ranges read prepared days plus the exact
// readings at partial boundaries and any small number of still-dirty days.
func readQuotaChart[T quotaChartSnapshot](s *Store, provider string, series []string, start, end time.Time, limit int,
	raw func(time.Time, time.Time, int) ([]*T, error)) ([]*T, error) {
	if len(series) == 0 || limit < 2 || end.Sub(start) <= quotaChartDay {
		return raw(start, end, limit)
	}
	first := start.UTC().Truncate(quotaChartDay)
	if first.Before(start) {
		first = first.Add(quotaChartDay)
	}
	last := end.UTC().Truncate(quotaChartDay)
	if !first.Before(last) {
		return raw(start, end, limit)
	}
	points, dirty, err := quotaChartCoveredDays[T](s, provider, series, first, last)
	if err != nil || len(dirty) > 3 {
		return raw(start, end, limit)
	}
	if start.Before(first) {
		dirty = append(dirty, quotaChartRange{start, first})
	}
	if last.Before(end) {
		dirty = append(dirty, quotaChartRange{last, end})
	}
	for _, span := range dirty {
		exact, err := raw(span.start, span.end, limit)
		if err != nil {
			return nil, err
		}
		points = append(points, exact...)
	}
	slices.SortStableFunc(points, func(a, b *T) int { return quotaChartTimestamp(a).Compare(quotaChartTimestamp(b)) })
	return sampleQuotaChart(points, limit), nil
}

func quotaChartCoveredDays[T quotaChartSnapshot](s *Store, provider string, series []string, first, last time.Time) ([]*T, []quotaChartRange, error) {
	prefix := []any{provider, first.Unix(), last.Unix()}
	args := append([]any{}, prefix...)
	args = append(args, prefix...)
	for _, key := range series {
		args = append(args, key)
	}
	rows, err := s.db.Query(`SELECT day, 0 AS prepared, '' AS series, NULL AS points
		FROM quota_chart_dirty_days WHERE provider=? AND day>=? AND day<?
		UNION ALL SELECT day, 1, series, points FROM quota_chart_days c
		WHERE provider=? AND day>=? AND day<? AND series IN (`+historyPlaceholders(len(series))+`)
		AND NOT EXISTS(SELECT 1 FROM quota_chart_dirty_days d WHERE d.provider=c.provider AND d.day=c.day)
		ORDER BY prepared, day, series`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var points []*T
	var dirty []quotaChartRange
	for rows.Next() {
		var day int64
		var prepared int
		var series string
		var data []byte
		if err := rows.Scan(&day, &prepared, &series, &data); err != nil {
			return nil, nil, err
		}
		if prepared == 0 {
			start := time.Unix(day, 0).UTC()
			dirty = append(dirty, quotaChartRange{start, start.Add(quotaChartDay)})
			if len(dirty) > 3 {
				return nil, dirty, nil
			}
			continue
		}
		var chunk []*T
		if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&chunk); err != nil {
			return nil, nil, fmt.Errorf("decode quota chart chunk: %w", err)
		}
		points = append(points, chunk...)
	}
	return points, dirty, rows.Err()
}

func quotaChartTimestamp(snapshot any) time.Time {
	switch snapshot := snapshot.(type) {
	case *api.AnthropicSnapshot:
		return snapshot.CapturedAt
	case *api.CodexSnapshot:
		return snapshot.CapturedAt
	case *api.AntigravitySnapshot:
		return snapshot.CapturedAt
	}
	panic("unsupported quota chart series")
}

func quotaChartValues(snapshot any, visit func(string, float64)) {
	switch snapshot := snapshot.(type) {
	case *api.AnthropicSnapshot:
		for _, quota := range snapshot.Quotas {
			visit(quota.Name, quota.Utilization)
		}
	case *api.CodexSnapshot:
		for _, quota := range snapshot.Quotas {
			visit(quota.Name, quota.Utilization)
		}
	case *api.AntigravitySnapshot:
		for _, group := range snapshot.SummaryGroups {
			for _, quota := range group.Buckets {
				visit(group.GroupKey+"\x00"+quota.Window, quota.RemainingFraction*-100)
			}
		}
		for _, quota := range snapshot.Models {
			visit(quota.ModelID+"\x00"+quota.WindowKind, -quota.RemainingPercent)
		}
	}
}

// Time buckets keep the first and last observations and prefer observed drops
// within each bucket, so a short reset is not replaced by an arbitrary point.
func sampleQuotaChart[T quotaChartSnapshot](points []*T, limit int) []*T {
	if len(points) <= limit {
		return points
	}
	first, last := points[0], points[len(points)-1]
	start := quotaChartTimestamp(first)
	span := quotaChartTimestamp(last).Sub(start)
	if limit == 2 || span <= 0 {
		return []*T{first, last}
	}
	type bucket struct {
		point *T
		drop  bool
	}
	buckets := make([]bucket, limit-2)
	previous := make(map[string]float64)
	for i, point := range points {
		drop := false
		quotaChartValues(point, func(key string, usage float64) {
			if prior, ok := previous[key]; ok && usage < prior {
				drop = true
			}
			previous[key] = usage
		})
		if i == 0 || i == len(points)-1 {
			continue
		}
		index := min(len(buckets)-1, int(float64(quotaChartTimestamp(point).Sub(start))/float64(span)*float64(len(buckets))))
		pick := &buckets[index]
		if pick.point == nil || drop || !pick.drop {
			*pick = bucket{point, drop}
		}
	}
	out := make([]*T, 0, limit)
	out = append(out, first)
	for _, pick := range buckets {
		if pick.point != nil {
			out = append(out, pick.point)
		}
	}
	return append(out, last)
}
