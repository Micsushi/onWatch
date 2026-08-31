package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

type CursorResetCycle struct {
	ID              int64
	QuotaName       string
	CycleStart      time.Time
	CycleEnd        *time.Time
	ResetsAt        *time.Time
	PeakUtilization float64
	TotalDelta      float64
}

type CursorLatestQuota struct {
	Name        string
	Used        float64
	Limit       float64
	Utilization float64
	Format      string
	ResetsAt    *time.Time
	CapturedAt  time.Time
	AccountType string
	PlanName    string
}

func (s *Store) InsertCursorSnapshot(snapshot *api.CursorSnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO cursor_snapshots (captured_at, raw_json, account_type, plan_name, quota_count) VALUES (?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.RawJSON,
		string(snapshot.AccountType),
		snapshot.PlanName,
		len(snapshot.Quotas),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert cursor snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	for _, q := range snapshot.Quotas {
		var resetsAt interface{}
		if q.ResetsAt != nil {
			resetsAt = q.ResetsAt.Format(time.RFC3339Nano)
		}
		_, err := tx.Exec(
			`INSERT INTO cursor_quota_values (snapshot_id, quota_name, used, limit_value, utilization, format, resets_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			snapshotID, q.Name, q.Used, q.Limit, q.Utilization, string(q.Format), resetsAt,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert quota value %s: %w", q.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

func (s *Store) QueryLatestCursor() (*api.CursorSnapshot, error) {
	var snapshot api.CursorSnapshot
	var capturedAt, accountType, planName string

	err := s.db.QueryRow(
		`SELECT id, captured_at, account_type, plan_name, quota_count FROM cursor_snapshots ORDER BY captured_at COLLATE ONWATCH_RFC3339 DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt, &accountType, &planName, new(int))

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest cursor: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	snapshot.AccountType = api.CursorAccountType(accountType)
	snapshot.PlanName = planName

	rows, err := s.db.Query(
		`SELECT quota_name, used, limit_value, utilization, format, resets_at FROM cursor_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query quota values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var q api.CursorQuota
		var format string
		var resetsAt sql.NullString
		if err := rows.Scan(&q.Name, &q.Used, &q.Limit, &q.Utilization, &format, &resetsAt); err != nil {
			return nil, fmt.Errorf("failed to scan quota value: %w", err)
		}
		q.Format = api.CursorQuotaFormat(format)
		if resetsAt.Valid && resetsAt.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			q.ResetsAt = &t
		}
		snapshot.Quotas = append(snapshot.Quotas, q)
	}

	return &snapshot, rows.Err()
}

func (s *Store) QueryCursorRange(start, end time.Time, limit ...int) ([]*api.CursorSnapshot, error) {
	query := `SELECT id, captured_at, account_type, plan_name, quota_count FROM cursor_snapshots
		WHERE captured_at COLLATE ONWATCH_RFC3339 >= ? AND captured_at COLLATE ONWATCH_RFC3339 < ? ORDER BY captured_at COLLATE ONWATCH_RFC3339 ASC`
	args := []interface{}{start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query = `SELECT id, captured_at, account_type, plan_name, quota_count
			FROM (
				SELECT id, captured_at, account_type, plan_name, quota_count
				FROM cursor_snapshots
				WHERE captured_at COLLATE ONWATCH_RFC3339 >= ? AND captured_at COLLATE ONWATCH_RFC3339 < ?
				ORDER BY captured_at COLLATE ONWATCH_RFC3339 DESC
				LIMIT ?
			) recent
			ORDER BY captured_at COLLATE ONWATCH_RFC3339 ASC`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query cursor range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.CursorSnapshot
	for rows.Next() {
		var snap api.CursorSnapshot
		var capturedAt, accountType, planName string
		if err := rows.Scan(&snap.ID, &capturedAt, &accountType, &planName, new(int)); err != nil {
			return nil, fmt.Errorf("failed to scan cursor snapshot: %w", err)
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		snap.AccountType = api.CursorAccountType(accountType)
		snap.PlanName = planName
		snapshots = append(snapshots, &snap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, snap := range snapshots {
		qRows, err := s.db.Query(
			`SELECT quota_name, used, limit_value, utilization, format, resets_at FROM cursor_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query quota values for snapshot %d: %w", snap.ID, err)
		}
		for qRows.Next() {
			var q api.CursorQuota
			var format string
			var resetsAt sql.NullString
			if err := qRows.Scan(&q.Name, &q.Used, &q.Limit, &q.Utilization, &format, &resetsAt); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("failed to scan quota value: %w", err)
			}
			q.Format = api.CursorQuotaFormat(format)
			if resetsAt.Valid && resetsAt.String != "" {
				t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
				q.ResetsAt = &t
			}
			snap.Quotas = append(snap.Quotas, q)
		}
		qRows.Close()
	}

	return snapshots, nil
}

// QueryCursorRangeSampled returns time-sampled Cursor snapshots and their quota
// values. It retains the range endpoints and any observed quota reset.
func (s *Store) QueryCursorRangeSampled(start, end time.Time, maxPoints int) ([]*api.CursorSnapshot, error) {
	if maxPoints < 2 {
		return nil, fmt.Errorf("cursor sample size must be at least 2")
	}

	rows, err := s.db.Query(`
		WITH bounded AS (
			SELECT id, captured_at, account_type, plan_name,
				julianday(captured_at) AS captured_day,
				MIN(julianday(captured_at)) OVER () AS first_day,
				MAX(julianday(captured_at)) OVER () AS last_day,
				COUNT(*) OVER () AS total_rows
			FROM cursor_snapshots
			WHERE captured_at COLLATE ONWATCH_RFC3339 >= ?
				AND captured_at COLLATE ONWATCH_RFC3339 < ?
		),
		bucketed AS (
			SELECT id, captured_at, account_type, plan_name, total_rows,
				CASE WHEN last_day = first_day THEN 0 ELSE
					MIN(? - 1, CAST((captured_day - first_day) * (? - 1) / (last_day - first_day) AS INTEGER))
				END AS time_bucket
			FROM bounded
		),
		quota_ordered AS (
			SELECT quota.snapshot_id, quota.quota_name, quota.utilization,
				LAG(quota.utilization) OVER (
					PARTITION BY quota.quota_name ORDER BY bounded.captured_at COLLATE ONWATCH_RFC3339 ASC
				) AS previous_utilization
			FROM bounded
			JOIN cursor_quota_values quota ON quota.snapshot_id = bounded.id
		),
		reset_ids AS (
			SELECT DISTINCT snapshot_id
			FROM quota_ordered
			WHERE previous_utilization IS NOT NULL AND utilization < previous_utilization
		),
		ranked AS (
			SELECT id, captured_at, account_type, plan_name, total_rows, time_bucket,
				ROW_NUMBER() OVER (
					PARTITION BY time_bucket
					ORDER BY CASE WHEN id IN (SELECT snapshot_id FROM reset_ids) THEN 0 ELSE 1 END,
						captured_at COLLATE ONWATCH_RFC3339 DESC
				) AS bucket_rank,
				ROW_NUMBER() OVER (ORDER BY captured_at COLLATE ONWATCH_RFC3339 ASC) AS overall_rank,
				ROW_NUMBER() OVER (ORDER BY captured_at COLLATE ONWATCH_RFC3339 DESC) AS overall_reverse_rank
			FROM bucketed
		),
		sampled AS (
			SELECT id, captured_at, account_type, plan_name
			FROM ranked
			WHERE total_rows <= ?
				OR overall_rank = 1
				OR overall_reverse_rank = 1
				OR (time_bucket > 0 AND time_bucket < ? - 1 AND bucket_rank = 1)
		)
		SELECT sampled.id, sampled.captured_at, sampled.account_type, sampled.plan_name,
			quota.quota_name, quota.used, quota.limit_value, quota.utilization, quota.format, quota.resets_at
		FROM sampled
		LEFT JOIN cursor_quota_values quota ON quota.snapshot_id = sampled.id
		ORDER BY sampled.captured_at COLLATE ONWATCH_RFC3339 ASC, quota.quota_name ASC`,
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
		maxPoints,
		maxPoints,
		maxPoints,
		maxPoints,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query sampled cursor range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.CursorSnapshot
	var current *api.CursorSnapshot
	for rows.Next() {
		var (
			id               int64
			capturedAt       string
			accountType      sql.NullString
			planName         sql.NullString
			quotaName        sql.NullString
			quotaUsed        sql.NullFloat64
			quotaLimit       sql.NullFloat64
			quotaUtilization sql.NullFloat64
			quotaFormat      sql.NullString
			quotaResetsAt    sql.NullString
		)
		if err := rows.Scan(
			&id,
			&capturedAt,
			&accountType,
			&planName,
			&quotaName,
			&quotaUsed,
			&quotaLimit,
			&quotaUtilization,
			&quotaFormat,
			&quotaResetsAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan sampled cursor range: %w", err)
		}

		if current == nil || current.ID != id {
			parsedCapturedAt, err := time.Parse(time.RFC3339Nano, capturedAt)
			if err != nil {
				return nil, fmt.Errorf("failed to parse cursor snapshot captured_at %q: %w", capturedAt, err)
			}
			current = &api.CursorSnapshot{ID: id, CapturedAt: parsedCapturedAt}
			if accountType.Valid {
				current.AccountType = api.CursorAccountType(accountType.String)
			}
			if planName.Valid {
				current.PlanName = planName.String
			}
			snapshots = append(snapshots, current)
		}

		if !quotaName.Valid {
			continue
		}
		quota := api.CursorQuota{
			Name:        quotaName.String,
			Used:        quotaUsed.Float64,
			Limit:       quotaLimit.Float64,
			Utilization: quotaUtilization.Float64,
			Format:      api.CursorQuotaFormat(quotaFormat.String),
		}
		if quotaResetsAt.Valid && quotaResetsAt.String != "" {
			parsedResetsAt, err := time.Parse(time.RFC3339Nano, quotaResetsAt.String)
			if err != nil {
				return nil, fmt.Errorf("failed to parse cursor quota resets_at %q: %w", quotaResetsAt.String, err)
			}
			quota.ResetsAt = &parsedResetsAt
		}
		current.Quotas = append(current.Quotas, quota)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate sampled cursor range: %w", err)
	}
	return snapshots, nil
}

func (s *Store) CreateCursorCycle(quotaName string, cycleStart time.Time, resetsAt *time.Time) (int64, error) {
	var resetsAtVal interface{}
	if resetsAt != nil {
		resetsAtVal = resetsAt.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO cursor_reset_cycles (quota_name, cycle_start, resets_at) VALUES (?, ?, ?)`,
		quotaName, cycleStart.Format(time.RFC3339Nano), resetsAtVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create cursor cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

func (s *Store) CloseCursorCycle(quotaName string, cycleEnd time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE cursor_reset_cycles SET cycle_end = ?, peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peak, delta, quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to close cursor cycle: %w", err)
	}
	return nil
}

func (s *Store) UpdateCursorCycle(quotaName string, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE cursor_reset_cycles SET peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		peak, delta, quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update cursor cycle: %w", err)
	}
	return nil
}

func (s *Store) QueryActiveCursorCycle(quotaName string) (*CursorResetCycle, error) {
	var cycle CursorResetCycle
	var cycleStart string
	var cycleEnd, resetsAt sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM cursor_reset_cycles WHERE quota_name = ? AND cycle_end IS NULL`,
		quotaName,
	).Scan(
		&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
		&cycle.PeakUtilization, &cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active cursor cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &t
	}
	if resetsAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
		cycle.ResetsAt = &t
	}

	return &cycle, nil
}

func (s *Store) QueryCursorCycleHistory(quotaName string, limit ...int) ([]*CursorResetCycle, error) {
	query := `SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM cursor_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaName}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query cursor cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*CursorResetCycle
	for rows.Next() {
		var cycle CursorResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
			&cycle.PeakUtilization, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan cursor cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetsAt.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			cycle.ResetsAt = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

func (s *Store) QueryCursorCyclesSince(quotaName string, since time.Time) ([]*CursorResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM cursor_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL AND cycle_start >= ?
		ORDER BY cycle_start DESC`,
		quotaName, since.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query cursor cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*CursorResetCycle
	for rows.Next() {
		var cycle CursorResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
			&cycle.PeakUtilization, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan cursor cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetsAt.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			cycle.ResetsAt = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

func (s *Store) QueryCursorUtilizationSeries(quotaName string, since time.Time) ([]UtilizationPoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, qv.utilization
		FROM cursor_quota_values qv
		JOIN cursor_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name = ? AND s.captured_at COLLATE ONWATCH_RFC3339 >= ?
		ORDER BY s.captured_at COLLATE ONWATCH_RFC3339 ASC`,
		quotaName, since.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query utilization series: %w", err)
	}
	defer rows.Close()

	var points []UtilizationPoint
	for rows.Next() {
		var capturedAt string
		var util float64
		if err := rows.Scan(&capturedAt, &util); err != nil {
			return nil, fmt.Errorf("failed to scan utilization point: %w", err)
		}
		t, _ := time.Parse(time.RFC3339Nano, capturedAt)
		points = append(points, UtilizationPoint{CapturedAt: t, Utilization: util})
	}

	return points, rows.Err()
}

func (s *Store) QueryCursorLatestPerQuota() ([]CursorLatestQuota, error) {
	rows, err := s.db.Query(`
		SELECT qv.quota_name, qv.used, qv.limit_value, qv.utilization, qv.format, qv.resets_at,
		       s.captured_at, s.account_type, s.plan_name
		FROM cursor_quota_values qv
		JOIN cursor_snapshots s ON s.id = qv.snapshot_id
		WHERE s.id = (SELECT MAX(id) FROM cursor_snapshots)
		ORDER BY qv.quota_name ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed to query latest per-quota: %w", err)
	}
	defer rows.Close()

	var results []CursorLatestQuota
	for rows.Next() {
		var name, format, accountType, planName string
		var used, limitValue, utilization float64
		var resetsAt sql.NullString
		var capturedAt string

		if err := rows.Scan(&name, &used, &limitValue, &utilization, &format, &resetsAt, &capturedAt, &accountType, &planName); err != nil {
			return nil, fmt.Errorf("failed to scan latest quota: %w", err)
		}

		q := CursorLatestQuota{
			Name:        name,
			Used:        used,
			Limit:       limitValue,
			Utilization: utilization,
			Format:      format,
			AccountType: accountType,
			PlanName:    planName,
		}
		q.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if resetsAt.Valid && resetsAt.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			q.ResetsAt = &t
		}
		results = append(results, q)
	}
	return results, rows.Err()
}

func (s *Store) QueryAllCursorQuotaNames() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT quota_name FROM cursor_reset_cycles ORDER BY quota_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query cursor quota names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan quota name: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}

func (s *Store) QueryCursorCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if limit <= 0 {
		limit = 50
	}

	var cycles []*CursorResetCycle
	activeCycle, err := s.QueryActiveCursorCycle(groupBy)
	if err != nil {
		return nil, fmt.Errorf("store.QueryCursorCycleOverview: active: %w", err)
	}
	if activeCycle != nil {
		cycles = append(cycles, activeCycle)
		limit--
	}

	completedCycles, err := s.QueryCursorCycleHistory(groupBy, limit)
	if err != nil {
		return nil, fmt.Errorf("store.QueryCursorCycleOverview: %w", err)
	}
	cycles = append(cycles, completedCycles...)

	var overviewRows []CycleOverviewRow
	for _, c := range cycles {
		row := CycleOverviewRow{
			CycleID:    c.ID,
			QuotaType:  c.QuotaName,
			CycleStart: c.CycleStart,
			CycleEnd:   c.CycleEnd,
			PeakValue:  c.PeakUtilization,
			TotalDelta: c.TotalDelta,
		}

		var endBoundary time.Time
		if c.CycleEnd != nil {
			endBoundary = *c.CycleEnd
		} else {
			endBoundary = time.Now().Add(time.Minute)
		}

		var snapshotID int64
		var capturedAt string
		err := s.db.QueryRow(
			`SELECT s.id, s.captured_at FROM cursor_snapshots s
			JOIN cursor_quota_values qv ON qv.snapshot_id = s.id
			WHERE qv.quota_name = ? AND s.captured_at COLLATE ONWATCH_RFC3339 >= ? AND s.captured_at COLLATE ONWATCH_RFC3339 < ?
			ORDER BY qv.utilization DESC LIMIT 1`,
			groupBy,
			c.CycleStart.Format(time.RFC3339Nano),
			endBoundary.Format(time.RFC3339Nano),
		).Scan(&snapshotID, &capturedAt)

		if err == sql.ErrNoRows {
			overviewRows = append(overviewRows, row)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store.QueryCursorCycleOverview: peak snapshot: %w", err)
		}

		row.PeakTime, _ = time.Parse(time.RFC3339Nano, capturedAt)

		qRows, err := s.db.Query(
			`SELECT quota_name, utilization, used, limit_value FROM cursor_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snapshotID,
		)
		if err != nil {
			return nil, fmt.Errorf("store.QueryCursorCycleOverview: quota values: %w", err)
		}
		for qRows.Next() {
			var entry CrossQuotaEntry
			if err := qRows.Scan(&entry.Name, &entry.Percent, &entry.Value, new(float64)); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("store.QueryCursorCycleOverview: scan quota: %w", err)
			}
			row.CrossQuotas = append(row.CrossQuotas, entry)
		}
		qRows.Close()

		overviewRows = append(overviewRows, row)
	}

	return overviewRows, nil
}
