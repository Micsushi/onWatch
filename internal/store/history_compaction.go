package store

import (
	"fmt"
	"time"
)

type ProviderSnapshotCompactionResult struct {
	DeletedSnapshots int64
	ClearedRawJSON   int64
	Cutoff           time.Time
}

type providerSnapshotCompactionSpec struct {
	table       string
	groupBy     string
	childTables []string
	hasRawJSON  bool
}

var providerSnapshotCompactionSpecs = []providerSnapshotCompactionSpec{
	{table: "quota_snapshots", groupBy: "provider"},
	{table: "zai_snapshots", groupBy: "provider"},
	{table: "anthropic_snapshots", childTables: []string{"anthropic_quota_values"}, hasRawJSON: true},
	{table: "copilot_snapshots", childTables: []string{"copilot_quota_values"}, hasRawJSON: true},
	{table: "codex_snapshots", groupBy: "account_id", childTables: []string{"codex_quota_values"}, hasRawJSON: true},
	{table: "antigravity_snapshots", childTables: []string{"antigravity_model_values", "antigravity_quota_summary_buckets"}, hasRawJSON: true},
	{table: "minimax_snapshots", groupBy: "account_id", childTables: []string{"minimax_model_values"}, hasRawJSON: true},
	{table: "gemini_snapshots", childTables: []string{"gemini_quota_values"}, hasRawJSON: true},
	{table: "openrouter_snapshots"},
	{table: "cursor_snapshots", childTables: []string{"cursor_quota_values"}, hasRawJSON: true},
}

// CompactProviderSnapshotHistory retains one representative snapshot per UTC
// day before cutoff. Recent snapshots remain untouched at full resolution.
func (s *Store) CompactProviderSnapshotHistory(cutoff time.Time) (ProviderSnapshotCompactionResult, error) {
	result := ProviderSnapshotCompactionResult{Cutoff: cutoff.UTC()}
	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin provider snapshot compaction: %w", err)
	}
	defer tx.Rollback()

	cutoffRaw := result.Cutoff.Format(time.RFC3339Nano)
	for _, spec := range providerSnapshotCompactionSpecs {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS temp_provider_snapshot_delete_ids`); err != nil {
			return result, fmt.Errorf("reset provider snapshot compaction workspace: %w", err)
		}
		partition := "substr(captured_at, 1, 10)"
		if spec.groupBy != "" {
			partition = spec.groupBy + ", " + partition
		}
		query := fmt.Sprintf(`
			CREATE TEMP TABLE temp_provider_snapshot_delete_ids AS
			SELECT id
			FROM (
				SELECT id,
				       ROW_NUMBER() OVER (
						PARTITION BY %s
						ORDER BY captured_at DESC, id DESC
					) AS daily_rank
				FROM %s
				WHERE captured_at < ?
			)
			WHERE daily_rank > 1
		`, partition, spec.table)
		if _, err := tx.Exec(query, cutoffRaw); err != nil {
			return result, fmt.Errorf("select compacted %s snapshots: %w", spec.table, err)
		}

		for _, childTable := range spec.childTables {
			deleteChildTransfers := fmt.Sprintf(`
				DELETE FROM data_transfer_records
				WHERE table_name = ?
				  AND local_record_id IN (
					SELECT CAST(id AS TEXT)
					FROM %s
					WHERE snapshot_id IN (SELECT id FROM temp_provider_snapshot_delete_ids)
				  )
			`, childTable)
			if _, err := tx.Exec(deleteChildTransfers, childTable); err != nil {
				return result, fmt.Errorf("delete compacted %s transfer rows: %w", childTable, err)
			}
			deleteChildren := fmt.Sprintf(`
				DELETE FROM %s
				WHERE snapshot_id IN (SELECT id FROM temp_provider_snapshot_delete_ids)
			`, childTable)
			if _, err := tx.Exec(deleteChildren); err != nil {
				return result, fmt.Errorf("delete compacted %s rows: %w", childTable, err)
			}
		}

		if _, err := tx.Exec(`
			DELETE FROM data_transfer_records
			WHERE table_name = ?
			  AND local_record_id IN (
				SELECT CAST(id AS TEXT) FROM temp_provider_snapshot_delete_ids
			  )
		`, spec.table); err != nil {
			return result, fmt.Errorf("delete compacted %s transfer rows: %w", spec.table, err)
		}
		deleteSnapshots := fmt.Sprintf(`
			DELETE FROM %s
			WHERE id IN (SELECT id FROM temp_provider_snapshot_delete_ids)
		`, spec.table)
		deleted, err := tx.Exec(deleteSnapshots)
		if err != nil {
			return result, fmt.Errorf("delete compacted %s rows: %w", spec.table, err)
		}
		deletedCount, err := deleted.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("count compacted %s rows: %w", spec.table, err)
		}
		result.DeletedSnapshots += deletedCount

		if spec.hasRawJSON {
			clearRaw := fmt.Sprintf(`
				UPDATE %s
				SET raw_json = ''
				WHERE captured_at < ? AND raw_json IS NOT NULL AND raw_json != ''
			`, spec.table)
			cleared, err := tx.Exec(clearRaw, cutoffRaw)
			if err != nil {
				return result, fmt.Errorf("clear unused %s raw JSON: %w", spec.table, err)
			}
			clearedCount, err := cleared.RowsAffected()
			if err != nil {
				return result, fmt.Errorf("count cleared %s raw JSON rows: %w", spec.table, err)
			}
			result.ClearedRawJSON += clearedCount
		}
	}

	if _, err := tx.Exec(`DROP TABLE IF EXISTS temp_provider_snapshot_delete_ids`); err != nil {
		return result, fmt.Errorf("clear provider snapshot compaction workspace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit provider snapshot compaction: %w", err)
	}
	return result, nil
}
