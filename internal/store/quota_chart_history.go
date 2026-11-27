package store

import "strings"

const historyDetailBatchSize = 500

func historyPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// resetAwareQuotaHistory selects points before loading the full snapshots. A
// numeric sort key handles most comparisons, with the exact timestamp breaking
// ties within the same millisecond. Quota drops retain priority inside a bucket.
func resetAwareQuotaHistory(snapshotTable, quotaTable, accountFilter string) string {
	return `WITH bounded AS MATERIALIZED (
		SELECT id, captured_at, julianday(captured_at) AS captured_day
		FROM ` + snapshotTable + `
		WHERE ` + accountFilter + ` captured_at COLLATE ONWATCH_RFC3339 >= ?
			AND captured_at COLLATE ONWATCH_RFC3339 < ?
	),
	bounds AS (
		SELECT COUNT(*) AS total, MIN(captured_day) AS first_day,
			MAX(captured_day) AS last_day, ? AS point_limit FROM bounded
	),
	quota_ordered AS (
		SELECT bounded.id, q.utilization, LAG(q.utilization) OVER (
			PARTITION BY q.quota_name ORDER BY bounded.captured_day,
				bounded.captured_at COLLATE ONWATCH_RFC3339, bounded.id
		) AS previous
		FROM bounds CROSS JOIN bounded
		JOIN ` + quotaTable + ` AS q ON q.snapshot_id = bounded.id
		WHERE bounds.total > bounds.point_limit
	),
	bucketed AS (
		SELECT id, captured_at, captured_day, point_limit,
			CASE WHEN last_day = first_day THEN 0 ELSE
				MIN(point_limit - 1, CAST((captured_day - first_day) * (point_limit - 1)
					/ (last_day - first_day) AS INTEGER))
			END AS bucket
		FROM bounds CROSS JOIN bounded WHERE total > point_limit
	),
	ranked AS (
		SELECT id, ROW_NUMBER() OVER (
			PARTITION BY bucket ORDER BY
				CASE WHEN id IN (SELECT id FROM quota_ordered WHERE utilization < previous) THEN 0 ELSE 1 END,
				captured_day DESC, captured_at COLLATE ONWATCH_RFC3339 DESC, id DESC
		) AS position
		FROM bucketed WHERE bucket > 0 AND bucket < point_limit - 1
	),
	selected AS (
		SELECT id FROM bounds CROSS JOIN bounded WHERE total <= point_limit
		UNION ALL SELECT id FROM ranked WHERE position = 1
		UNION SELECT id FROM (
			SELECT id FROM bounds CROSS JOIN bounded WHERE total > point_limit
			ORDER BY captured_day, captured_at COLLATE ONWATCH_RFC3339, id LIMIT 1
		)
		UNION SELECT id FROM (
			SELECT id FROM bounds CROSS JOIN bounded WHERE total > point_limit
			ORDER BY captured_day DESC, captured_at COLLATE ONWATCH_RFC3339 DESC, id DESC LIMIT 1
		)
	)`
}

// Keep the first, last, and every ceil(count/maxPoints)-th observation.
func sampleStridedHistory(query, tieOrder string) string {
	return `WITH ranked AS (
		SELECT *, ROW_NUMBER() OVER (ORDER BY captured_at COLLATE ONWATCH_RFC3339 ASC,` + tieOrder + `) AS position,
			COUNT(*) OVER () AS total
		FROM (` + query + `)
	)
	SELECT * FROM ranked
	WHERE position = total OR (position - 1) % ((total + ? - 1) / ?) = 0`
}

// Select only the recorded timestamps first so wide ranges never decode or load
// children for observations that the dashboard will discard.
func stridedQuotaHistory(table, columns, accountFilter string) string {
	timeline := `SELECT id, captured_at FROM ` + table + `
		WHERE ` + accountFilter + ` captured_at COLLATE ONWATCH_RFC3339 >= ? AND captured_at COLLATE ONWATCH_RFC3339 < ?`
	return `SELECT ` + columns + ` FROM ` + table + `
		WHERE id IN (SELECT id FROM (` + sampleStridedHistory(timeline, "id ASC") + `))
		ORDER BY captured_at COLLATE ONWATCH_RFC3339 ASC, id ASC`
}
