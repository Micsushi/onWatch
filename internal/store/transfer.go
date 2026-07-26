package store

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
)

const TransferFormatVersion = 1

const (
	maxTransferArchiveSize  int64 = 1 << 30
	maxTransferDatabaseSize int64 = 4 << 30
)

type ExportOptions struct {
	AppVersion string
}

type TransferManifest struct {
	FormatVersion  int            `json:"format_version"`
	AppVersion     string         `json:"app_version"`
	ExportedAt     time.Time      `json:"exported_at"`
	InstallationID string         `json:"installation_id"`
	Counts         map[string]int `json:"counts"`
	DatabaseSHA256 string         `json:"database_sha256"`
}

type ImportTableSummary struct {
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
}

type ImportSummary struct {
	Tables map[string]ImportTableSummary `json:"tables"`
	Total  ImportTableSummary            `json:"total"`
}

type transferTable struct {
	name          string
	id            string
	columns       []string
	parentTable   string
	parentColumn  string
	accountColumn string
	textID        bool
	mutable       bool
}

var transferTables = []transferTable{
	{
		name: "quota_snapshots",
		id:   "id",
		columns: []string{
			"provider", "captured_at", "sub_limit", "sub_requests", "sub_renews_at",
			"search_limit", "search_requests", "search_renews_at",
			"tool_limit", "tool_requests", "tool_renews_at",
		},
	},
	{name: "reset_cycles", id: "id", columns: []string{"provider", "quota_type", "cycle_start", "cycle_end", "renews_at", "peak_requests", "total_delta"}, mutable: true},
	{name: "sessions", id: "id", columns: []string{"provider", "started_at", "ended_at", "poll_interval", "max_sub_requests", "max_search_requests", "max_tool_requests", "start_sub_requests", "start_search_requests", "start_tool_requests", "snapshot_count"}, textID: true, mutable: true},
	{name: "zai_snapshots", id: "id", columns: []string{"provider", "captured_at", "time_limit", "time_unit", "time_number", "time_usage", "time_current_value", "time_remaining", "time_percentage", "time_usage_details", "tokens_limit", "tokens_unit", "tokens_number", "tokens_usage", "tokens_current_value", "tokens_remaining", "tokens_percentage", "tokens_next_reset"}},
	{name: "zai_hourly_usage", id: "id", columns: []string{"provider", "hour", "model_calls", "tokens_used", "network_searches", "web_reads", "zreads", "fetched_at"}},
	{name: "zai_reset_cycles", id: "id", columns: []string{"quota_type", "cycle_start", "cycle_end", "next_reset", "peak_value", "total_delta"}, mutable: true},
	{name: "anthropic_snapshots", id: "id", columns: []string{"captured_at", "raw_json", "quota_count"}},
	{name: "anthropic_quota_values", id: "id", columns: []string{"snapshot_id", "quota_name", "utilization", "resets_at"}, parentTable: "anthropic_snapshots", parentColumn: "snapshot_id"},
	{name: "anthropic_reset_cycles", id: "id", columns: []string{"quota_name", "cycle_start", "cycle_end", "resets_at", "peak_utilization", "total_delta"}, mutable: true},
	{name: "copilot_snapshots", id: "id", columns: []string{"captured_at", "copilot_plan", "reset_date", "raw_json", "quota_count"}},
	{name: "copilot_quota_values", id: "id", columns: []string{"snapshot_id", "quota_name", "entitlement", "remaining", "percent_remaining", "unlimited", "overage_count"}, parentTable: "copilot_snapshots", parentColumn: "snapshot_id"},
	{name: "copilot_reset_cycles", id: "id", columns: []string{"quota_name", "cycle_start", "cycle_end", "reset_date", "peak_used", "total_delta"}, mutable: true},
	{name: "codex_snapshots", id: "id", columns: []string{"captured_at", "account_id", "plan_type", "credits_balance", "raw_json", "quota_count"}, accountColumn: "account_id"},
	{name: "codex_quota_values", id: "id", columns: []string{"snapshot_id", "quota_name", "utilization", "resets_at", "status"}, parentTable: "codex_snapshots", parentColumn: "snapshot_id"},
	{name: "codex_reset_cycles", id: "id", columns: []string{"account_id", "quota_name", "cycle_start", "cycle_end", "resets_at", "peak_utilization", "total_delta"}, accountColumn: "account_id", mutable: true},
	{name: "antigravity_snapshots", id: "id", columns: []string{"captured_at", "email", "plan_name", "prompt_credits", "monthly_credits", "raw_json", "model_count"}},
	{name: "antigravity_model_values", id: "id", columns: []string{"snapshot_id", "model_id", "label", "remaining_fraction", "remaining_percent", "is_exhausted", "reset_time"}, parentTable: "antigravity_snapshots", parentColumn: "snapshot_id"},
	{name: "antigravity_quota_summary_buckets", id: "id", columns: []string{"snapshot_id", "group_key", "group_display_name", "group_description", "bucket_id", "bucket_display_name", "bucket_description", "window_kind", "remaining_fraction", "remaining_percent", "reset_time"}, parentTable: "antigravity_snapshots", parentColumn: "snapshot_id"},
	{name: "antigravity_reset_cycles", id: "id", columns: []string{"model_id", "cycle_start", "cycle_end", "reset_time", "peak_usage", "total_delta"}, mutable: true},
	{name: "minimax_snapshots", id: "id", columns: []string{"captured_at", "raw_json", "model_count", "account_id"}, accountColumn: "account_id"},
	{name: "minimax_model_values", id: "id", columns: []string{"snapshot_id", "model_name", "total", "remain", "used", "used_percent", "reset_at", "window_start", "window_end", "weekly_total", "weekly_remain", "weekly_used", "weekly_used_percent", "weekly_reset_at", "weekly_window_start", "weekly_window_end"}, parentTable: "minimax_snapshots", parentColumn: "snapshot_id"},
	{name: "minimax_reset_cycles", id: "id", columns: []string{"model_name", "cycle_start", "cycle_end", "reset_at", "peak_used", "total_delta", "account_id"}, accountColumn: "account_id", mutable: true},
	{name: "gemini_snapshots", id: "id", columns: []string{"captured_at", "tier", "project_id", "raw_json", "quota_count"}},
	{name: "gemini_quota_values", id: "id", columns: []string{"snapshot_id", "model_id", "remaining_fraction", "usage_percent", "reset_time"}, parentTable: "gemini_snapshots", parentColumn: "snapshot_id"},
	{name: "gemini_reset_cycles", id: "id", columns: []string{"model_id", "cycle_start", "cycle_end", "reset_time", "peak_usage", "total_delta"}, mutable: true},
	{name: "openrouter_snapshots", id: "id", columns: []string{"captured_at", "label", "usage", "usage_daily", "usage_weekly", "usage_monthly", "credit_limit", "limit_remaining", "is_free_tier", "rate_limit_requests", "rate_limit_interval"}},
	{name: "openrouter_reset_cycles", id: "id", columns: []string{"quota_type", "cycle_start", "cycle_end", "peak_usage", "total_delta"}, mutable: true},
	{name: "cursor_snapshots", id: "id", columns: []string{"captured_at", "raw_json", "account_type", "plan_name", "quota_count"}},
	{name: "cursor_quota_values", id: "id", columns: []string{"snapshot_id", "quota_name", "used", "limit_value", "utilization", "format", "resets_at"}, parentTable: "cursor_snapshots", parentColumn: "snapshot_id"},
	{name: "cursor_reset_cycles", id: "id", columns: []string{"quota_name", "cycle_start", "cycle_end", "resets_at", "peak_utilization", "total_delta"}, mutable: true},
	{name: "api_integration_usage_events", id: "id", columns: []string{"captured_at", "integration_name", "provider", "account_name", "model", "request_id", "prompt_tokens", "completion_tokens", "total_tokens", "cost_usd", "latency_ms", "metadata_json", "source_path", "fingerprint", "created_at"}},
	{name: "api_integration_usage_hourly", id: "id", columns: []string{"origin_scope", "hour_start", "integration_name", "provider", "account_name", "model", "reasoning_effort", "mode", "speed_mode", "request_count", "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "cached_input_tokens", "cache_creation_input_tokens", "output_tokens", "reasoning_output_tokens", "total_cost_usd", "first_captured_at", "last_captured_at"}},
}

var transferSettingKeys = []string{
	"timezone",
	"hidden_insights",
	"fork_preferences",
	"provider_visibility",
	"api_integrations_visibility",
	"menubar",
	"notifications",
	"provider_settings",
}

var safeProviderSettingFields = map[string]bool{
	"display_mode": true,
	"pace_mode":    true,
	"source":       true,
	"cc_detection": true,
	"region":       true,
}

type transferOrigin struct {
	OriginID       string
	OriginRecordID string
}

// TransferInstallationID returns the stable random ID used to distinguish
// records produced by this installation from records produced elsewhere.
func (s *Store) TransferInstallationID() (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("store.TransferInstallationID: begin: %w", err)
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRow(`SELECT value FROM data_transfer_state WHERE key = 'installation_id'`).Scan(&id)
	if err == nil {
		return id, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store.TransferInstallationID: read: %w", err)
	}

	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return "", fmt.Errorf("store.TransferInstallationID: random ID: %w", err)
	}
	id = hex.EncodeToString(randomID)
	if _, err := tx.Exec(`INSERT OR IGNORE INTO data_transfer_state (key, value) VALUES ('installation_id', ?)`, id); err != nil {
		return "", fmt.Errorf("store.TransferInstallationID: save: %w", err)
	}
	if err := tx.QueryRow(`SELECT value FROM data_transfer_state WHERE key = 'installation_id'`).Scan(&id); err != nil {
		return "", fmt.Errorf("store.TransferInstallationID: reread: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("store.TransferInstallationID: commit: %w", err)
	}
	return id, nil
}

func recordImportedOrigin(tx *sql.Tx, table, localID string, origin transferOrigin) error {
	_, err := tx.Exec(`
		INSERT INTO data_transfer_records (table_name, local_record_id, origin_id, origin_record_id)
		VALUES (?, ?, ?, ?)
	`, table, localID, origin.OriginID, origin.OriginRecordID)
	if err != nil {
		return fmt.Errorf("store.recordImportedOrigin: %w", err)
	}
	return nil
}

func findImportedLocalID(tx *sql.Tx, table string, origin transferOrigin, localInstallationID string) (string, bool, error) {
	var localID string
	err := tx.QueryRow(`
		SELECT local_record_id
		FROM data_transfer_records
		WHERE table_name = ? AND origin_id = ? AND origin_record_id = ?
	`, table, origin.OriginID, origin.OriginRecordID).Scan(&localID)
	if errors.Is(err, sql.ErrNoRows) {
		if origin.OriginID != localInstallationID {
			return "", false, nil
		}
		query := fmt.Sprintf(`SELECT CAST(id AS TEXT) FROM %s WHERE id = ?`, table)
		err = tx.QueryRow(query, origin.OriginRecordID).Scan(&localID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("store.findImportedLocalID: find local origin: %w", err)
		}
		return localID, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store.findImportedLocalID: %w", err)
	}
	return localID, true, nil
}

// ExportData writes a versioned, secret-safe transfer archive.
func (s *Store) ExportData(w io.Writer, opts ExportOptions) (TransferManifest, error) {
	installationID, err := s.TransferInstallationID()
	if err != nil {
		return TransferManifest{}, err
	}
	hasImportedProvenance, err := s.prepareTransferProvenance(installationID)
	if err != nil {
		return TransferManifest{}, err
	}

	temp, err := os.CreateTemp("", "onwatch-transfer-*.sqlite")
	if err != nil {
		return TransferManifest{}, fmt.Errorf("store.ExportData: create temporary database: %w", err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return TransferManifest{}, fmt.Errorf("store.ExportData: close temporary database: %w", err)
	}
	defer os.Remove(tempPath)

	transferDB, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return TransferManifest{}, fmt.Errorf("store.ExportData: open temporary database: %w", err)
	}
	transferDB.SetMaxOpenConns(1)
	if _, err := transferDB.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL;`); err != nil {
		transferDB.Close()
		return TransferManifest{}, fmt.Errorf("store.ExportData: configure temporary database: %w", err)
	}
	if err := createTransferDatabase(transferDB); err != nil {
		transferDB.Close()
		return TransferManifest{}, err
	}
	exportTx, err := transferDB.Begin()
	if err != nil {
		transferDB.Close()
		return TransferManifest{}, fmt.Errorf("store.ExportData: begin transfer transaction: %w", err)
	}

	counts := make(map[string]int)
	if err := s.exportTransferAccounts(exportTx, installationID, hasImportedProvenance, counts); err != nil {
		exportTx.Rollback()
		transferDB.Close()
		return TransferManifest{}, err
	}
	for _, table := range transferTables {
		if err := s.exportTransferTable(exportTx, table, installationID, hasImportedProvenance, counts); err != nil {
			exportTx.Rollback()
			transferDB.Close()
			return TransferManifest{}, err
		}
	}
	if err := s.exportTransferSettings(exportTx, counts); err != nil {
		exportTx.Rollback()
		transferDB.Close()
		return TransferManifest{}, err
	}
	if err := exportTx.Commit(); err != nil {
		transferDB.Close()
		return TransferManifest{}, fmt.Errorf("store.ExportData: commit transfer transaction: %w", err)
	}
	if err := transferDB.Close(); err != nil {
		return TransferManifest{}, fmt.Errorf("store.ExportData: close temporary database: %w", err)
	}

	databaseHash, err := hashFile(tempPath)
	if err != nil {
		return TransferManifest{}, err
	}
	manifest := TransferManifest{
		FormatVersion:  TransferFormatVersion,
		AppVersion:     opts.AppVersion,
		ExportedAt:     time.Now().UTC(),
		InstallationID: installationID,
		Counts:         counts,
		DatabaseSHA256: databaseHash,
	}
	if err := writeTransferZIP(w, tempPath, manifest); err != nil {
		return TransferManifest{}, err
	}
	return manifest, nil
}

func (s *Store) prepareTransferProvenance(installationID string) (bool, error) {
	// A local row's fallback identity is already stable. Remove redundant
	// explicit copies left by older development builds so export does not pay
	// for a provenance lookup on every local history row.
	if _, err := s.db.Exec(`
		DELETE FROM data_transfer_records
		WHERE origin_id = ? AND origin_record_id = local_record_id
	`, installationID); err != nil {
		return false, fmt.Errorf("store.ExportData: prune redundant provenance: %w", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM data_transfer_records`).Scan(&count); err != nil {
		return false, fmt.Errorf("store.ExportData: count imported provenance: %w", err)
	}
	return count > 0, nil
}

func createTransferDatabase(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE transfer_accounts (
			origin_id TEXT NOT NULL,
			origin_record_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			metadata TEXT NOT NULL DEFAULT '',
			deleted_at TEXT,
			external_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (origin_id, origin_record_id)
		);
		CREATE TABLE transfer_rows (
			table_name TEXT NOT NULL,
			origin_id TEXT NOT NULL,
			origin_record_id TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			parent_origin_id TEXT,
			parent_origin_record_id TEXT,
			account_origin_id TEXT,
			account_origin_record_id TEXT,
			PRIMARY KEY (table_name, origin_id, origin_record_id)
		);
		CREATE TABLE transfer_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("store.ExportData: create transfer schema: %w", err)
	}
	return nil
}

func (s *Store) exportTransferAccounts(dest *sql.Tx, installationID string, hasImportedProvenance bool, counts map[string]int) error {
	query := `
		SELECT p.id, p.provider, p.name, p.created_at, COALESCE(p.metadata, ''), p.deleted_at,
		       COALESCE(p.external_id, ''), ?, CAST(p.id AS TEXT)
		FROM provider_accounts AS p
		ORDER BY p.id`
	if hasImportedProvenance {
		query = `
			SELECT p.id, p.provider, p.name, p.created_at, COALESCE(p.metadata, ''), p.deleted_at,
			       COALESCE(p.external_id, ''), COALESCE(r.origin_id, ?), COALESCE(r.origin_record_id, CAST(p.id AS TEXT))
			FROM provider_accounts AS p
			LEFT JOIN data_transfer_records AS r
			  ON r.table_name = 'provider_accounts' AND r.local_record_id = CAST(p.id AS TEXT)
			ORDER BY p.id, r.origin_id, r.origin_record_id`
	}
	rows, err := s.db.Query(query, installationID)
	if err != nil {
		return fmt.Errorf("store.ExportData: query accounts: %w", err)
	}
	defer rows.Close()
	insert, err := dest.Prepare(`
		INSERT INTO transfer_accounts (
			origin_id, origin_record_id, provider, name, created_at, metadata, deleted_at, external_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("store.ExportData: prepare account insert: %w", err)
	}
	defer insert.Close()

	for rows.Next() {
		var id int64
		var provider, name, createdAt, metadata, externalID, originID, originRecordID string
		var deletedAt sql.NullString
		if err := rows.Scan(&id, &provider, &name, &createdAt, &metadata, &deletedAt, &externalID, &originID, &originRecordID); err != nil {
			return fmt.Errorf("store.ExportData: scan account: %w", err)
		}
		safeMetadata := sanitizeAccountMetadata(metadata)
		if _, err := insert.Exec(originID, originRecordID, provider, name, createdAt, safeMetadata, nullableStringValue(deletedAt), externalID); err != nil {
			return fmt.Errorf("store.ExportData: save account %d: %w", id, err)
		}
		counts["provider_accounts"]++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store.ExportData: iterate accounts: %w", err)
	}
	return nil
}

func (s *Store) exportTransferTable(dest *sql.Tx, table transferTable, installationID string, hasImportedProvenance bool, counts map[string]int) error {
	dataColumns := append([]string{table.id}, table.columns...)
	selectColumns := make([]string, 0, len(dataColumns)+6)
	for _, column := range dataColumns {
		if table.name == "api_integration_usage_events" && column == "metadata_json" {
			selectColumns = append(selectColumns, apiIntegrationMetadataJSONExpression("source")+" AS metadata_json")
		} else {
			selectColumns = append(selectColumns, "source."+column)
		}
	}
	joins := ""
	args := make([]any, 0, 3)
	if hasImportedProvenance {
		selectColumns = append(selectColumns, "record.origin_id", "record.origin_record_id")
		joins = `
			LEFT JOIN data_transfer_records AS record ON record.rowid = (
				SELECT candidate.rowid FROM data_transfer_records AS candidate
				WHERE candidate.table_name = ? AND candidate.local_record_id = CAST(source.` + table.id + ` AS TEXT)
				ORDER BY candidate.origin_id, candidate.origin_record_id LIMIT 1
			)`
		args = append(args, table.name)
	} else {
		selectColumns = append(selectColumns, "?", "CAST(source."+table.id+" AS TEXT)")
		args = append(args, installationID)
	}
	if table.parentColumn != "" {
		if hasImportedProvenance {
			selectColumns = append(selectColumns, "parent_record.origin_id", "parent_record.origin_record_id")
			joins += `
				LEFT JOIN data_transfer_records AS parent_record ON parent_record.rowid = (
					SELECT candidate.rowid FROM data_transfer_records AS candidate
					WHERE candidate.table_name = ? AND candidate.local_record_id = CAST(source.` + table.parentColumn + ` AS TEXT)
					ORDER BY candidate.origin_id, candidate.origin_record_id LIMIT 1
				)`
			args = append(args, table.parentTable)
		} else {
			selectColumns = append(selectColumns, "?", "CAST(source."+table.parentColumn+" AS TEXT)")
			args = append(args, installationID)
		}
	}
	if table.accountColumn != "" {
		if hasImportedProvenance {
			selectColumns = append(selectColumns, "account_record.origin_id", "account_record.origin_record_id")
			joins += `
				LEFT JOIN data_transfer_records AS account_record ON account_record.rowid = (
					SELECT candidate.rowid FROM data_transfer_records AS candidate
					WHERE candidate.table_name = 'provider_accounts' AND candidate.local_record_id = CAST(source.` + table.accountColumn + ` AS TEXT)
					ORDER BY candidate.origin_id, candidate.origin_record_id LIMIT 1
				)`
		} else {
			selectColumns = append(selectColumns, "?", "CAST(source."+table.accountColumn+" AS TEXT)")
			args = append(args, installationID)
		}
	}
	query := fmt.Sprintf("SELECT %s FROM %s AS source %s ORDER BY source.%s", strings.Join(selectColumns, ", "), table.name, joins, table.id)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("store.ExportData: query %s: %w", table.name, err)
	}
	defer rows.Close()
	insert, err := dest.Prepare(`
		INSERT INTO transfer_rows (
			table_name, origin_id, origin_record_id, payload_json,
			parent_origin_id, parent_origin_record_id, account_origin_id, account_origin_record_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("store.ExportData: prepare %s insert: %w", table.name, err)
	}
	defer insert.Close()

	for rows.Next() {
		values := make([]any, len(selectColumns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return fmt.Errorf("store.ExportData: scan %s: %w", table.name, err)
		}
		localID := sqlValueString(values[0])
		position := len(dataColumns)
		origin := transferOrigin{
			OriginID:       transferStringOr(values[position], installationID),
			OriginRecordID: transferStringOr(values[position+1], localID),
		}
		position += 2
		payload := make(map[string]any, len(table.columns))
		var parentOrigin, accountOrigin *transferOrigin
		for i, column := range table.columns {
			value := normalizeSQLValue(values[i+1])
			switch column {
			case table.parentColumn:
				resolved := transferOrigin{
					OriginID:       transferStringOr(values[position], installationID),
					OriginRecordID: transferStringOr(values[position+1], sqlValueString(value)),
				}
				parentOrigin = &resolved
				position += 2
			case table.accountColumn:
				resolved := transferOrigin{
					OriginID:       transferStringOr(values[position], installationID),
					OriginRecordID: transferStringOr(values[position+1], sqlValueString(value)),
				}
				accountOrigin = &resolved
				position += 2
			default:
				payload[column] = value
			}
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("store.ExportData: encode %s %s: %w", table.name, localID, err)
		}
		var parentOriginID, parentRecordID, accountOriginID, accountRecordID any
		if parentOrigin != nil {
			parentOriginID = parentOrigin.OriginID
			parentRecordID = parentOrigin.OriginRecordID
		}
		if accountOrigin != nil {
			accountOriginID = accountOrigin.OriginID
			accountRecordID = accountOrigin.OriginRecordID
		}
		if _, err := insert.Exec(table.name, origin.OriginID, origin.OriginRecordID, string(payloadJSON),
			parentOriginID, parentRecordID, accountOriginID, accountRecordID); err != nil {
			return fmt.Errorf("store.ExportData: save %s %s: %w", table.name, localID, err)
		}
		counts[table.name]++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store.ExportData: iterate %s: %w", table.name, err)
	}
	return nil
}

func (s *Store) exportTransferSettings(dest *sql.Tx, counts map[string]int) error {
	insert, err := dest.Prepare(`INSERT INTO transfer_settings (key, value) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("store.ExportData: prepare setting insert: %w", err)
	}
	defer insert.Close()
	for _, key := range transferSettingKeys {
		value, err := s.GetSetting(key)
		if err != nil {
			return fmt.Errorf("store.ExportData: read setting %s: %w", key, err)
		}
		if value == "" {
			continue
		}
		sanitized, ok := sanitizeTransferSetting(key, value)
		if !ok {
			continue
		}
		if _, err := insert.Exec(key, sanitized); err != nil {
			return fmt.Errorf("store.ExportData: save setting %s: %w", key, err)
		}
		counts["settings"]++
	}
	return nil
}

func sanitizeTransferSetting(key, value string) (string, bool) {
	switch key {
	case "notifications":
		var decoded map[string]any
		if json.Unmarshal([]byte(value), &decoded) != nil {
			return "", false
		}
		decoded["channels"] = map[string]bool{"email": false, "push": false, "discord": false}
		encoded, err := json.Marshal(decoded)
		return string(encoded), err == nil
	case "provider_settings":
		var providers map[string]any
		if json.Unmarshal([]byte(value), &providers) != nil {
			return "", false
		}
		safeProviders := make(map[string]any)
		for provider, raw := range providers {
			fields, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			safeFields := make(map[string]any)
			for field, fieldValue := range fields {
				if safeProviderSettingFields[field] {
					safeFields[field] = fieldValue
				}
			}
			if len(safeFields) > 0 {
				safeProviders[provider] = safeFields
			}
		}
		encoded, err := json.Marshal(safeProviders)
		return string(encoded), err == nil
	default:
		return value, true
	}
}

func sanitizeAccountMetadata(value string) string {
	var metadata map[string]any
	if json.Unmarshal([]byte(value), &metadata) != nil {
		return ""
	}
	safe := make(map[string]any)
	if region, ok := metadata["region"]; ok {
		safe["region"] = region
	}
	if len(safe) == 0 {
		return ""
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("store.ExportData: open database for checksum: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("store.ExportData: checksum database: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeTransferZIP(w io.Writer, databasePath string, manifest TransferManifest) error {
	zw := zip.NewWriter(w)
	manifestEntry, err := zw.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("store.ExportData: create manifest entry: %w", err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("store.ExportData: encode manifest: %w", err)
	}
	if _, err := manifestEntry.Write(manifestJSON); err != nil {
		return fmt.Errorf("store.ExportData: write manifest: %w", err)
	}

	databaseEntry, err := zw.Create("history.sqlite")
	if err != nil {
		return fmt.Errorf("store.ExportData: create database entry: %w", err)
	}
	database, err := os.Open(databasePath)
	if err != nil {
		return fmt.Errorf("store.ExportData: open database entry: %w", err)
	}
	_, copyErr := io.Copy(databaseEntry, database)
	closeErr := database.Close()
	if copyErr != nil {
		return fmt.Errorf("store.ExportData: write database: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("store.ExportData: close database entry: %w", closeErr)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("store.ExportData: close archive: %w", err)
	}
	return nil
}

func normalizeSQLValue(value any) any {
	if raw, ok := value.([]byte); ok {
		return string(raw)
	}
	return value
}

func sqlValueString(value any) string {
	switch value := normalizeSQLValue(value).(type) {
	case string:
		return value
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func transferStringOr(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	resolved := sqlValueString(value)
	if resolved == "" {
		return fallback
	}
	return resolved
}

func nullableStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

// ImportData validates and additively merges one portable transfer archive.
func (s *Store) ImportData(r io.Reader) (ImportSummary, error) {
	summary := ImportSummary{Tables: make(map[string]ImportTableSummary)}
	databasePath, manifest, cleanup, err := unpackTransferData(r)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return summary, err
	}
	if manifest.FormatVersion != TransferFormatVersion {
		return summary, fmt.Errorf("store.ImportData: unsupported transfer format version %d", manifest.FormatVersion)
	}
	checksum, err := hashFile(databasePath)
	if err != nil {
		return summary, err
	}
	if checksum != manifest.DatabaseSHA256 {
		return summary, fmt.Errorf("store.ImportData: transfer database checksum mismatch")
	}

	source, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return summary, fmt.Errorf("store.ImportData: open transfer database: %w", err)
	}
	source.SetMaxOpenConns(1)
	defer source.Close()
	if err := validateTransferDatabase(source); err != nil {
		return summary, err
	}
	localInstallationID, err := s.TransferInstallationID()
	if err != nil {
		return summary, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return summary, fmt.Errorf("store.ImportData: begin destination transaction: %w", err)
	}
	defer tx.Rollback()

	accountMap, err := importTransferAccounts(tx, source, localInstallationID, &summary)
	if err != nil {
		return summary, err
	}
	for _, table := range transferTables {
		if err := importTransferTable(tx, source, table, accountMap, localInstallationID, &summary); err != nil {
			return summary, err
		}
	}
	if err := importTransferSettings(tx, source, &summary); err != nil {
		return summary, err
	}
	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("store.ImportData: commit destination transaction: %w", err)
	}
	if summary.Tables["api_integration_usage_events"].Inserted > 0 ||
		summary.Tables["api_integration_usage_hourly"].Inserted > 0 {
		s.apiIntegrationUsageVersion.Add(1)
	}
	return summary, nil
}

func unpackTransferData(r io.Reader) (databasePath string, manifest TransferManifest, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "onwatch-import-*")
	if err != nil {
		return "", manifest, nil, fmt.Errorf("store.ImportData: create temporary directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	archivePath := filepath.Join(dir, "archive.onwatch.zip")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return "", manifest, nil, fmt.Errorf("store.ImportData: create temporary archive: %w", err)
	}
	written, copyErr := io.Copy(archive, io.LimitReader(r, maxTransferArchiveSize+1))
	closeErr := archive.Close()
	if copyErr != nil {
		cleanup()
		return "", manifest, nil, fmt.Errorf("store.ImportData: copy archive: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return "", manifest, nil, fmt.Errorf("store.ImportData: close archive: %w", closeErr)
	}
	if written > maxTransferArchiveSize {
		cleanup()
		return "", manifest, nil, fmt.Errorf("store.ImportData: archive exceeds 1 GiB limit")
	}

	archiveReader, err := zip.OpenReader(archivePath)
	if err != nil {
		cleanup()
		return "", manifest, nil, fmt.Errorf("store.ImportData: invalid ZIP archive: %w", err)
	}
	defer archiveReader.Close()
	seen := make(map[string]bool)
	databasePath = filepath.Join(dir, "history.sqlite")
	for _, entry := range archiveReader.File {
		if seen[entry.Name] {
			cleanup()
			return "", manifest, nil, fmt.Errorf("store.ImportData: duplicate ZIP entry %q", entry.Name)
		}
		seen[entry.Name] = true
		switch entry.Name {
		case "manifest.json":
			if entry.UncompressedSize64 > 1<<20 {
				cleanup()
				return "", manifest, nil, fmt.Errorf("store.ImportData: manifest exceeds 1 MiB limit")
			}
			entryReader, openErr := entry.Open()
			if openErr != nil {
				cleanup()
				return "", manifest, nil, fmt.Errorf("store.ImportData: open manifest: %w", openErr)
			}
			decodeErr := json.NewDecoder(io.LimitReader(entryReader, 1<<20)).Decode(&manifest)
			entryReader.Close()
			if decodeErr != nil {
				cleanup()
				return "", manifest, nil, fmt.Errorf("store.ImportData: invalid manifest: %w", decodeErr)
			}
		case "history.sqlite":
			entryReader, openErr := entry.Open()
			if openErr != nil {
				cleanup()
				return "", manifest, nil, fmt.Errorf("store.ImportData: open transfer database: %w", openErr)
			}
			database, createErr := os.OpenFile(databasePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if createErr != nil {
				entryReader.Close()
				cleanup()
				return "", manifest, nil, fmt.Errorf("store.ImportData: create transfer database: %w", createErr)
			}
			expanded, copyErr := io.Copy(database, io.LimitReader(entryReader, maxTransferDatabaseSize+1))
			databaseCloseErr := database.Close()
			entryReader.Close()
			if copyErr != nil || databaseCloseErr != nil {
				cleanup()
				if copyErr != nil {
					return "", manifest, nil, fmt.Errorf("store.ImportData: extract transfer database: %w", copyErr)
				}
				return "", manifest, nil, fmt.Errorf("store.ImportData: close transfer database: %w", databaseCloseErr)
			}
			if expanded > maxTransferDatabaseSize {
				cleanup()
				return "", manifest, nil, fmt.Errorf("store.ImportData: expanded database exceeds 4 GiB limit")
			}
		default:
			cleanup()
			return "", manifest, nil, fmt.Errorf("store.ImportData: unexpected ZIP entry %q", entry.Name)
		}
	}
	if !seen["manifest.json"] || !seen["history.sqlite"] {
		cleanup()
		return "", manifest, nil, fmt.Errorf("store.ImportData: archive must contain manifest.json and history.sqlite")
	}
	return databasePath, manifest, cleanup, nil
}

func validateTransferDatabase(db *sql.DB) error {
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("store.ImportData: check transfer database integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("store.ImportData: transfer database integrity check failed: %s", integrity)
	}
	required := map[string]bool{"transfer_accounts": false, "transfer_rows": false, "transfer_settings": false}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("store.ImportData: inspect transfer schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("store.ImportData: scan transfer schema: %w", err)
		}
		if _, ok := required[name]; !ok {
			return fmt.Errorf("store.ImportData: unexpected transfer table %q", name)
		}
		required[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store.ImportData: iterate transfer schema: %w", err)
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("store.ImportData: missing transfer table %q", name)
		}
	}
	knownTables := make(map[string]bool, len(transferTables))
	for _, table := range transferTables {
		knownTables[table.name] = true
	}
	rowNames, err := db.Query(`SELECT DISTINCT table_name FROM transfer_rows`)
	if err != nil {
		return fmt.Errorf("store.ImportData: inspect transfer row tables: %w", err)
	}
	defer rowNames.Close()
	for rowNames.Next() {
		var name string
		if err := rowNames.Scan(&name); err != nil {
			return fmt.Errorf("store.ImportData: scan transfer row table: %w", err)
		}
		if !knownTables[name] {
			return fmt.Errorf("store.ImportData: unsupported history table %q", name)
		}
	}
	if err := rowNames.Err(); err != nil {
		return fmt.Errorf("store.ImportData: iterate transfer row tables: %w", err)
	}
	return validateTransferTableColumns(db)
}

func validateTransferTableColumns(db *sql.DB) error {
	requiredColumns := map[string][]string{
		"transfer_accounts": {"origin_id", "origin_record_id", "provider", "name", "created_at", "metadata", "deleted_at", "external_id"},
		"transfer_rows":     {"table_name", "origin_id", "origin_record_id", "payload_json", "parent_origin_id", "parent_origin_record_id", "account_origin_id", "account_origin_record_id"},
		"transfer_settings": {"key", "value"},
	}
	for table, columns := range requiredColumns {
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return fmt.Errorf("store.ImportData: inspect %s columns: %w", table, err)
		}
		found := make(map[string]bool, len(columns))
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("store.ImportData: scan %s columns: %w", table, err)
			}
			found[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("store.ImportData: iterate %s columns: %w", table, err)
		}
		rows.Close()
		for _, column := range columns {
			if !found[column] {
				return fmt.Errorf("store.ImportData: transfer table %q missing column %q", table, column)
			}
		}
	}
	return nil
}

func importTransferAccounts(tx *sql.Tx, source *sql.DB, localInstallationID string, summary *ImportSummary) (map[string]string, error) {
	accountMap := make(map[string]string)
	rows, err := source.Query(`
		SELECT origin_id, origin_record_id, provider, name, created_at, metadata, deleted_at, external_id
		FROM transfer_accounts ORDER BY origin_id, origin_record_id
	`)
	if err != nil {
		return nil, fmt.Errorf("store.ImportData: query accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var origin, provider, name, createdAt, metadata, externalID string
		var originRecordID string
		var deletedAt sql.NullString
		if err := rows.Scan(&origin, &originRecordID, &provider, &name, &createdAt, &metadata, &deletedAt, &externalID); err != nil {
			return nil, fmt.Errorf("store.ImportData: scan account: %w", err)
		}
		provenance := transferOrigin{OriginID: origin, OriginRecordID: originRecordID}
		key := transferOriginKey(provenance)
		if localID, ok, err := findImportedLocalID(tx, "provider_accounts", provenance, localInstallationID); err != nil {
			return nil, err
		} else if ok {
			accountMap[key] = localID
			incrementImportSummary(summary, "provider_accounts", "skipped")
			continue
		}

		localID, found, err := findDestinationAccount(tx, provider, name, externalID)
		if err != nil {
			return nil, err
		}
		if !found {
			result, err := tx.Exec(`
				INSERT INTO provider_accounts (provider, name, created_at, metadata, deleted_at, external_id)
				VALUES (?, ?, ?, ?, ?, ?)
			`, provider, name, createdAt, sanitizeAccountMetadata(metadata), nullableStringValue(deletedAt), emptyStringAsNil(externalID))
			if err != nil {
				return nil, fmt.Errorf("store.ImportData: insert account %s/%s: %w", provider, name, err)
			}
			insertedID, err := result.LastInsertId()
			if err != nil {
				return nil, fmt.Errorf("store.ImportData: account ID %s/%s: %w", provider, name, err)
			}
			localID = strconv.FormatInt(insertedID, 10)
			incrementImportSummary(summary, "provider_accounts", "inserted")
		} else {
			incrementImportSummary(summary, "provider_accounts", "skipped")
		}
		if err := recordImportedOrigin(tx, "provider_accounts", localID, provenance); err != nil {
			return nil, err
		}
		accountMap[key] = localID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ImportData: iterate accounts: %w", err)
	}
	return accountMap, nil
}

func findDestinationAccount(tx *sql.Tx, provider, name, externalID string) (string, bool, error) {
	var localID string
	var err error
	if externalID != "" {
		err = tx.QueryRow(`SELECT CAST(id AS TEXT) FROM provider_accounts WHERE provider = ? AND external_id = ? LIMIT 1`, provider, externalID).Scan(&localID)
		if err == nil {
			return localID, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("store.ImportData: find account external identity: %w", err)
		}
	}
	err = tx.QueryRow(`SELECT CAST(id AS TEXT) FROM provider_accounts WHERE provider = ? AND name = ? LIMIT 1`, provider, name).Scan(&localID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store.ImportData: find account name: %w", err)
	}
	return localID, true, nil
}

func importTransferTable(tx *sql.Tx, source *sql.DB, table transferTable, accountMap map[string]string, localInstallationID string, summary *ImportSummary) error {
	rows, err := source.Query(`
		SELECT origin_id, origin_record_id, payload_json,
		       parent_origin_id, parent_origin_record_id, account_origin_id, account_origin_record_id
		FROM transfer_rows WHERE table_name = ? ORDER BY origin_id, origin_record_id
	`, table.name)
	if err != nil {
		return fmt.Errorf("store.ImportData: query %s: %w", table.name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var originID, originRecordID, payloadJSON string
		var parentOriginID, parentRecordID, accountOriginID, accountRecordID sql.NullString
		if err := rows.Scan(&originID, &originRecordID, &payloadJSON, &parentOriginID, &parentRecordID, &accountOriginID, &accountRecordID); err != nil {
			return fmt.Errorf("store.ImportData: scan %s: %w", table.name, err)
		}
		origin := transferOrigin{OriginID: originID, OriginRecordID: originRecordID}
		payload, err := decodeTransferPayload(payloadJSON)
		if err != nil {
			return fmt.Errorf("store.ImportData: decode %s %s: %w", table.name, originRecordID, err)
		}
		if localID, ok, err := findImportedLocalID(tx, table.name, origin, localInstallationID); err != nil {
			return err
		} else if ok {
			if table.mutable {
				updated, err := mergeMutableTransferRow(tx, table, localID, payload)
				if err != nil {
					return err
				}
				if updated {
					incrementImportSummary(summary, table.name, "updated")
				} else {
					incrementImportSummary(summary, table.name, "skipped")
				}
			} else {
				incrementImportSummary(summary, table.name, "skipped")
			}
			continue
		}
		columns := make([]string, 0, len(table.columns)+1)
		values := make([]any, 0, len(table.columns)+1)
		if table.textID {
			columns = append(columns, table.id)
			values = append(values, importedTextID(origin))
		}
		for _, column := range table.columns {
			switch column {
			case table.parentColumn:
				if !parentOriginID.Valid || !parentRecordID.Valid {
					return fmt.Errorf("store.ImportData: %s %s missing parent provenance", table.name, originRecordID)
				}
				parentID, ok, err := findImportedLocalID(tx, table.parentTable, transferOrigin{OriginID: parentOriginID.String, OriginRecordID: parentRecordID.String}, localInstallationID)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("store.ImportData: %s %s parent not found", table.name, originRecordID)
				}
				columns = append(columns, column)
				values = append(values, parentID)
			case table.accountColumn:
				if !accountOriginID.Valid || !accountRecordID.Valid {
					return fmt.Errorf("store.ImportData: %s %s missing account provenance", table.name, originRecordID)
				}
				accountID, ok := accountMap[transferOriginKey(transferOrigin{OriginID: accountOriginID.String, OriginRecordID: accountRecordID.String})]
				if !ok {
					return fmt.Errorf("store.ImportData: %s %s account not found", table.name, originRecordID)
				}
				columns = append(columns, column)
				values = append(values, accountID)
			default:
				value, ok := payload[column]
				if !ok {
					return fmt.Errorf("store.ImportData: %s %s missing column %s", table.name, originRecordID, column)
				}
				columns = append(columns, column)
				values = append(values, value)
			}
		}
		if table.name == "api_integration_usage_events" {
			namespaceAPIIntegrationPayload(payload, origin)
			for index, column := range columns {
				if column == "fingerprint" || column == "source_path" {
					values[index] = payload[column]
				}
			}
			event := &apiintegrations.UsageEvent{
				PromptTokens:     transferInteger(payload["prompt_tokens"]),
				CompletionTokens: transferInteger(payload["completion_tokens"]),
			}
			if metadataJSON, ok := payload["metadata_json"].(string); ok {
				event.MetadataJSON = metadataJSON
			}
			metadata, storedMetadataJSON := normalizedAPIIntegrationMetadata(event)
			for index, column := range columns {
				if column == "metadata_json" {
					values[index] = storedMetadataJSON
				}
			}
			columns = append(
				columns,
				"session_id",
				"reasoning_effort",
				"mode",
				"speed_mode",
				"input_tokens",
				"cached_input_tokens",
				"cache_creation_input_tokens",
				"output_tokens",
				"reasoning_output_tokens",
			)
			values = append(
				values,
				metadata.SessionID,
				metadata.ReasoningEffort,
				metadata.Mode,
				metadata.SpeedMode,
				normalizedTokenValue(metadata.InputTokens, event.PromptTokens),
				normalizedTokenValue(metadata.CachedInputTokens, 0),
				normalizedTokenValue(metadata.CacheCreationInputTokens, 0),
				normalizedTokenValue(metadata.OutputTokens, event.CompletionTokens),
				normalizedTokenValue(metadata.ReasoningOutputTokens, 0),
			)
		}
		if table.name == "api_integration_usage_hourly" {
			payload["origin_scope"] = origin.OriginID
			for index, column := range columns {
				if column == "origin_scope" {
					values[index] = payload[column]
				}
			}
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table.name, strings.Join(columns, ","), placeholders)
		result, err := tx.Exec(query, values...)
		if err != nil {
			return fmt.Errorf("store.ImportData: insert %s %s: %w", table.name, originRecordID, err)
		}
		localID := ""
		if table.textID {
			localID = values[0].(string)
		} else {
			insertedID, err := result.LastInsertId()
			if err != nil {
				return fmt.Errorf("store.ImportData: local ID %s %s: %w", table.name, originRecordID, err)
			}
			localID = strconv.FormatInt(insertedID, 10)
		}
		if err := recordImportedOrigin(tx, table.name, localID, origin); err != nil {
			return err
		}
		incrementImportSummary(summary, table.name, "inserted")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store.ImportData: iterate %s: %w", table.name, err)
	}
	return nil
}

func mergeMutableTransferRow(tx *sql.Tx, table transferTable, localID string, payload map[string]any) (bool, error) {
	terminalFields := []string{"ended_at", "cycle_end", "next_reset", "resets_at", "reset_date", "reset_time", "reset_at"}
	maximumFields := []string{
		"max_sub_requests", "max_search_requests", "max_tool_requests", "snapshot_count",
		"peak_requests", "peak_value", "peak_utilization", "peak_used", "peak_usage", "total_delta",
	}
	allowed := make(map[string]bool, len(table.columns))
	for _, column := range table.columns {
		allowed[column] = true
	}
	setClauses := make([]string, 0)
	args := make([]any, 0)
	for _, field := range terminalFields {
		value, ok := payload[field]
		if !ok || !allowed[field] {
			continue
		}
		setClauses = append(setClauses, fmt.Sprintf(`%s = CASE
			WHEN ? IS NULL OR ? = '' THEN %s
			WHEN %s IS NULL OR %s = '' OR ? > %s THEN ?
			ELSE %s END`, field, field, field, field, field, field))
		args = append(args, value, value, value, value)
	}
	for _, field := range maximumFields {
		value, ok := payload[field]
		if !ok || !allowed[field] || value == nil {
			continue
		}
		setClauses = append(setClauses, fmt.Sprintf(`%s = MAX(%s, ?)`, field, field))
		args = append(args, value)
	}
	if len(setClauses) == 0 {
		return false, nil
	}
	before, err := mutableTransferValues(tx, table, localID, terminalFields, maximumFields)
	if err != nil {
		return false, err
	}
	args = append(args, localID)
	query := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = ?`, table.name, strings.Join(setClauses, ", "), table.id)
	if _, err := tx.Exec(query, args...); err != nil {
		return false, fmt.Errorf("store.ImportData: merge %s %s: %w", table.name, localID, err)
	}
	after, err := mutableTransferValues(tx, table, localID, terminalFields, maximumFields)
	if err != nil {
		return false, err
	}
	return !reflect.DeepEqual(before, after), nil
}

func mutableTransferValues(tx *sql.Tx, table transferTable, localID string, fieldGroups ...[]string) ([]any, error) {
	allowed := make(map[string]bool, len(table.columns))
	for _, column := range table.columns {
		allowed[column] = true
	}
	fields := make([]string, 0)
	for _, group := range fieldGroups {
		for _, field := range group {
			if allowed[field] {
				fields = append(fields, field)
			}
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	values := make([]any, len(fields))
	destinations := make([]any, len(fields))
	for index := range values {
		destinations[index] = &values[index]
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", strings.Join(fields, ","), table.name, table.id)
	if err := tx.QueryRow(query, localID).Scan(destinations...); err != nil {
		return nil, fmt.Errorf("store.ImportData: read mutable %s %s: %w", table.name, localID, err)
	}
	for index := range values {
		values[index] = normalizeSQLValue(values[index])
	}
	return values, nil
}

func importTransferSettings(tx *sql.Tx, source *sql.DB, summary *ImportSummary) error {
	rows, err := source.Query(`SELECT key, value FROM transfer_settings ORDER BY key`)
	if err != nil {
		return fmt.Errorf("store.ImportData: query settings: %w", err)
	}
	defer rows.Close()
	allowed := make(map[string]bool, len(transferSettingKeys))
	for _, key := range transferSettingKeys {
		allowed[key] = true
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("store.ImportData: scan setting: %w", err)
		}
		if !allowed[key] {
			return fmt.Errorf("store.ImportData: unsupported setting %q", key)
		}
		sanitized, ok := sanitizeTransferSetting(key, value)
		if !ok {
			return fmt.Errorf("store.ImportData: invalid setting %q", key)
		}
		var exists int
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM settings WHERE key = ?)`, key).Scan(&exists); err != nil {
			return fmt.Errorf("store.ImportData: check setting %s: %w", key, err)
		}
		if exists != 0 {
			incrementImportSummary(summary, "settings", "skipped")
			continue
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, key, sanitized); err != nil {
			return fmt.Errorf("store.ImportData: insert setting %s: %w", key, err)
		}
		incrementImportSummary(summary, "settings", "inserted")
	}
	return rows.Err()
}

func decodeTransferPayload(value string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	for key, raw := range payload {
		if number, ok := raw.(json.Number); ok {
			if integer, err := number.Int64(); err == nil {
				payload[key] = integer
			} else if decimal, err := number.Float64(); err == nil {
				payload[key] = decimal
			} else {
				return nil, fmt.Errorf("invalid number for %s", key)
			}
		}
	}
	return payload, nil
}

func namespaceAPIIntegrationPayload(payload map[string]any, origin transferOrigin) {
	fingerprint, _ := payload["fingerprint"].(string)
	hash := sha256.Sum256([]byte(origin.OriginID + ":" + fingerprint))
	payload["fingerprint"] = hex.EncodeToString(hash[:])
	sourcePath, _ := payload["source_path"].(string)
	shortOrigin := origin.OriginID
	if len(shortOrigin) > 8 {
		shortOrigin = shortOrigin[:8]
	}
	payload["source_path"] = "import:" + shortOrigin + "/" + filepath.Base(sourcePath)
}

func transferInteger(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func importedTextID(origin transferOrigin) string {
	hash := sha256.Sum256([]byte(origin.OriginID + ":" + origin.OriginRecordID))
	return "import-" + hex.EncodeToString(hash[:16])
}

func transferOriginKey(origin transferOrigin) string {
	return origin.OriginID + "\x00" + origin.OriginRecordID
}

func incrementImportSummary(summary *ImportSummary, table, field string) {
	row := summary.Tables[table]
	switch field {
	case "inserted":
		row.Inserted++
		summary.Total.Inserted++
	case "updated":
		row.Updated++
		summary.Total.Updated++
	case "skipped":
		row.Skipped++
		summary.Total.Skipped++
	}
	summary.Tables[table] = row
}

func emptyStringAsNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
