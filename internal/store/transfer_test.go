package store

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTransferTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := New(filepath.Join(t.TempDir(), "onwatch.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustParseTransferTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

func unpackTransferArchive(t *testing.T, data []byte) (TransferManifest, string) {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var manifest TransferManifest
	dbPath := filepath.Join(t.TempDir(), "history.sqlite")
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		switch file.Name {
		case "manifest.json":
			if err := json.NewDecoder(r).Decode(&manifest); err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
		case "history.sqlite":
			out, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatalf("create transfer db: %v", err)
			}
			if _, err := io.Copy(out, r); err != nil {
				out.Close()
				t.Fatalf("copy transfer db: %v", err)
			}
			if err := out.Close(); err != nil {
				t.Fatalf("close transfer db: %v", err)
			}
		}
		if err := r.Close(); err != nil {
			t.Fatalf("close %s: %v", file.Name, err)
		}
	}
	if manifest.FormatVersion == 0 || dbPath == "" {
		t.Fatal("archive missing manifest or history database")
	}
	return manifest, dbPath
}

func TestExportDataContainsHistoryAndNoSecrets(t *testing.T) {
	s := newTransferTestStore(t)
	if _, err := s.db.Exec(`
		INSERT INTO quota_snapshots (
			provider, captured_at, sub_limit, sub_requests, sub_renews_at,
			search_limit, search_requests, search_renews_at,
			tool_limit, tool_requests, tool_renews_at
		) VALUES ('synthetic', '2026-07-22T12:00:00Z', 1000, 10, '2026-07-23T00:00:00Z', 100, 2, '2026-07-23T00:00:00Z', 50, 1, '2026-07-23T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE provider_accounts SET metadata = '{"api_key":"secret-key","region":"cn"}' WHERE provider = 'minimax' AND name = 'default'`); err != nil {
		t.Fatalf("seed account metadata: %v", err)
	}
	settings := map[string]string{
		"timezone":          "America/Denver",
		"notifications":     `{"warning_threshold":80,"channels":{"email":true,"push":true,"discord":true}}`,
		"provider_settings": `{"global":{"display_mode":"available"},"zai":{"region":"cn","api_key":"secret-key","base_url":"https://secret.invalid/?token=bad"}}`,
		"smtp":              `{"host":"smtp.example.com","password":"enc:secret"}`,
		"discord":           `{"enabled":true,"webhook_url":"enc:secret"}`,
		"gemini_tokens":     `{"refresh_token":"secret"}`,
		"vapid_keys":        `{"private_key":"secret"}`,
		"unknown_future":    "must-not-export",
	}
	for key, value := range settings {
		if err := s.SetSetting(key, value); err != nil {
			t.Fatalf("SetSetting(%s): %v", key, err)
		}
	}
	if err := s.UpsertUser("admin", "password-hash"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := s.SaveAuthToken("login-token", mustParseTransferTime(t, "2026-07-23T00:00:00Z")); err != nil {
		t.Fatalf("SaveAuthToken: %v", err)
	}

	var archive bytes.Buffer
	manifest, err := s.ExportData(&archive, ExportOptions{AppVersion: "test-version"})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	unpackedManifest, dbPath := unpackTransferArchive(t, archive.Bytes())
	if !reflect.DeepEqual(unpackedManifest, manifest) {
		t.Fatalf("manifest round trip mismatch: %#v != %#v", unpackedManifest, manifest)
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read transfer db: %v", err)
	}
	hash := sha256.Sum256(dbBytes)
	if manifest.DatabaseSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("manifest checksum %q does not match transfer DB", manifest.DatabaseSHA256)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open transfer db: %v", err)
	}
	defer db.Close()
	var payload string
	if err := db.QueryRow(`SELECT payload_json FROM transfer_rows WHERE table_name = 'quota_snapshots'`).Scan(&payload); err != nil {
		t.Fatalf("read exported snapshot: %v", err)
	}
	if !regexp.MustCompile(`"sub_requests":10`).MatchString(payload) {
		t.Fatalf("snapshot payload missing usage: %s", payload)
	}
	var notifications string
	if err := db.QueryRow(`SELECT value FROM transfer_settings WHERE key = 'notifications'`).Scan(&notifications); err != nil {
		t.Fatalf("read notifications: %v", err)
	}
	if notifications != `{"channels":{"discord":false,"email":false,"push":false},"warning_threshold":80}` {
		t.Fatalf("notifications = %s", notifications)
	}
	var providerSettings string
	if err := db.QueryRow(`SELECT value FROM transfer_settings WHERE key = 'provider_settings'`).Scan(&providerSettings); err != nil {
		t.Fatalf("read provider settings: %v", err)
	}
	if providerSettings != `{"global":{"display_mode":"available"},"zai":{"region":"cn"}}` {
		t.Fatalf("provider settings = %s", providerSettings)
	}
	for _, forbidden := range []string{"smtp", "discord", "gemini_tokens", "vapid_keys", "unknown_future"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM transfer_settings WHERE key = ?`, forbidden).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", forbidden, err)
		}
		if count != 0 {
			t.Fatalf("forbidden setting %s was exported", forbidden)
		}
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata FROM transfer_accounts WHERE provider = 'minimax' AND name = 'default' LIMIT 1`).Scan(&metadata); err != nil {
		t.Fatalf("read account metadata: %v", err)
	}
	if metadata != `{"region":"cn"}` {
		t.Fatalf("account metadata = %s", metadata)
	}
}

func TestExportDataSupportsSingleConnectionMemoryStore(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New memory store: %v", err)
	}
	defer s.Close()
	seedSyntheticTransferSnapshot(t, s, 10)
	var archive bytes.Buffer
	if _, err := s.ExportData(&archive, ExportOptions{AppVersion: "test-version"}); err != nil {
		t.Fatalf("ExportData memory store: %v", err)
	}
	if archive.Len() == 0 {
		t.Fatal("memory store export is empty")
	}
}

func TestExportDataIncludesProviderParentsAndAccounts(t *testing.T) {
	s := newTransferTestStore(t)

	result, err := s.db.Exec(`INSERT INTO anthropic_snapshots (captured_at, raw_json, quota_count) VALUES ('2026-07-22T12:00:00Z', '{}', 1)`)
	if err != nil {
		t.Fatalf("insert anthropic snapshot: %v", err)
	}
	anthropicID, _ := result.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO anthropic_quota_values (snapshot_id, quota_name, utilization, resets_at) VALUES (?, 'five_hour', 10, '2026-07-22T17:00:00Z')`, anthropicID); err != nil {
		t.Fatalf("insert anthropic value: %v", err)
	}

	var codexAccountID int64
	if err := s.db.QueryRow(`SELECT id FROM provider_accounts WHERE provider = 'codex' AND name = 'default'`).Scan(&codexAccountID); err != nil {
		t.Fatalf("query codex account: %v", err)
	}
	result, err = s.db.Exec(`INSERT INTO codex_snapshots (captured_at, account_id, plan_type, credits_balance, raw_json, quota_count) VALUES ('2026-07-22T12:00:00Z', ?, 'plus', 5, '{}', 1)`, codexAccountID)
	if err != nil {
		t.Fatalf("insert codex snapshot: %v", err)
	}
	codexID, _ := result.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO codex_quota_values (snapshot_id, quota_name, utilization, resets_at, status) VALUES (?, 'primary', 20, '2026-07-22T17:00:00Z', 'ok')`, codexID); err != nil {
		t.Fatalf("insert codex value: %v", err)
	}

	var archive bytes.Buffer
	if _, err := s.ExportData(&archive, ExportOptions{AppVersion: "test-version"}); err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	_, dbPath := unpackTransferArchive(t, archive.Bytes())
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open transfer db: %v", err)
	}
	defer db.Close()

	var parentOriginID, parentRecordID string
	if err := db.QueryRow(`
		SELECT parent_origin_id, parent_origin_record_id
		FROM transfer_rows WHERE table_name = 'anthropic_quota_values'
	`).Scan(&parentOriginID, &parentRecordID); err != nil {
		t.Fatalf("read anthropic child: %v", err)
	}
	if parentOriginID == "" || parentRecordID == "" {
		t.Fatal("anthropic child did not retain parent provenance")
	}

	var accountOriginID, accountRecordID string
	if err := db.QueryRow(`
		SELECT account_origin_id, account_origin_record_id
		FROM transfer_rows WHERE table_name = 'codex_snapshots'
	`).Scan(&accountOriginID, &accountRecordID); err != nil {
		t.Fatalf("read codex snapshot: %v", err)
	}
	if accountOriginID == "" || accountRecordID == "" {
		t.Fatal("codex snapshot did not retain account provenance")
	}
	var accountCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transfer_accounts WHERE origin_id = ? AND origin_record_id = ?`, accountOriginID, accountRecordID).Scan(&accountCount); err != nil {
		t.Fatalf("find referenced account: %v", err)
	}
	if accountCount != 1 {
		t.Fatalf("referenced account count = %d, want 1", accountCount)
	}
}

func seedSyntheticTransferSnapshot(t *testing.T, s *Store, requests float64) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO quota_snapshots (
			provider, captured_at, sub_limit, sub_requests, sub_renews_at,
			search_limit, search_requests, search_renews_at,
			tool_limit, tool_requests, tool_renews_at
		) VALUES ('synthetic', '2026-07-22T12:00:00Z', 1000, ?, '2026-07-23T00:00:00Z', 100, 2, '2026-07-23T00:00:00Z', 50, 1, '2026-07-23T00:00:00Z')
	`, requests); err != nil {
		t.Fatalf("seed synthetic snapshot: %v", err)
	}
}

func exportTransferBytes(t *testing.T, s *Store) []byte {
	t.Helper()
	var archive bytes.Buffer
	if _, err := s.ExportData(&archive, ExportOptions{AppVersion: "test-version"}); err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	return archive.Bytes()
}

func rewriteTransferArchive(t *testing.T, data []byte, mutateDatabase func(*sql.DB), mutateManifest func(*TransferManifest)) []byte {
	t.Helper()
	manifest, databasePath := unpackTransferArchive(t, data)
	if mutateDatabase != nil {
		db, err := sql.Open("sqlite", databasePath)
		if err != nil {
			t.Fatalf("open transfer database for rewrite: %v", err)
		}
		mutateDatabase(db)
		if err := db.Close(); err != nil {
			t.Fatalf("close rewritten transfer database: %v", err)
		}
		checksum, err := hashFile(databasePath)
		if err != nil {
			t.Fatalf("hash rewritten transfer database: %v", err)
		}
		manifest.DatabaseSHA256 = checksum
	}
	if mutateManifest != nil {
		mutateManifest(&manifest)
	}
	var rewritten bytes.Buffer
	if err := writeTransferZIP(&rewritten, databasePath, manifest); err != nil {
		t.Fatalf("write rewritten transfer archive: %v", err)
	}
	return rewritten.Bytes()
}

func TestImportDataKeepsDifferentOriginsAndIsIdempotent(t *testing.T) {
	mac := newTransferTestStore(t)
	linux := newTransferTestStore(t)
	seedSyntheticTransferSnapshot(t, mac, 10)
	seedSyntheticTransferSnapshot(t, linux, 100)
	if err := mac.SetSetting("timezone", "America/Los_Angeles"); err != nil {
		t.Fatalf("set mac timezone: %v", err)
	}

	macArchive := exportTransferBytes(t, mac)
	linuxArchive := exportTransferBytes(t, linux)
	destination := newTransferTestStore(t)
	if err := destination.SetSetting("timezone", "America/Denver"); err != nil {
		t.Fatalf("set destination timezone: %v", err)
	}

	macSummary, err := destination.ImportData(bytes.NewReader(macArchive))
	if err != nil {
		t.Fatalf("import mac: %v", err)
	}
	if macSummary.Total.Inserted == 0 {
		t.Fatalf("mac summary = %#v, want inserted rows", macSummary)
	}
	linuxSummary, err := destination.ImportData(bytes.NewReader(linuxArchive))
	if err != nil {
		t.Fatalf("import linux: %v", err)
	}
	if linuxSummary.Total.Inserted == 0 {
		t.Fatalf("linux summary = %#v, want inserted rows", linuxSummary)
	}

	rows, err := destination.db.Query(`SELECT sub_requests FROM quota_snapshots WHERE captured_at = '2026-07-22T12:00:00Z' ORDER BY sub_requests`)
	if err != nil {
		t.Fatalf("query imported snapshots: %v", err)
	}
	defer rows.Close()
	var values []float64
	for rows.Next() {
		var value float64
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan imported value: %v", err)
		}
		values = append(values, value)
	}
	if !reflect.DeepEqual(values, []float64{10, 100}) {
		t.Fatalf("imported values = %#v, want [10 100]", values)
	}

	repeatSummary, err := destination.ImportData(bytes.NewReader(macArchive))
	if err != nil {
		t.Fatalf("reimport mac: %v", err)
	}
	if repeatSummary.Total.Inserted != 0 || repeatSummary.Total.Skipped == 0 {
		t.Fatalf("repeat summary = %#v, want only skipped rows", repeatSummary)
	}
	var count int
	if err := destination.db.QueryRow(`SELECT COUNT(*) FROM quota_snapshots WHERE captured_at = '2026-07-22T12:00:00Z'`).Scan(&count); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if count != 2 {
		t.Fatalf("snapshot count after reimport = %d, want 2", count)
	}
	timezone, err := destination.GetSetting("timezone")
	if err != nil {
		t.Fatalf("get destination timezone: %v", err)
	}
	if timezone != "America/Denver" {
		t.Fatalf("destination timezone overwritten with %q", timezone)
	}
}

func TestTransferPreservesCompactedAPIIntegrationHistory(t *testing.T) {
	source := newTransferTestStore(t)
	insertAPIIntegrationUsageEventForTest(t, source,
		`{"ts":"2026-01-15T12:05:00Z","integration":"Codex CLI","provider":"openai","model":"gpt-5.6-sol","prompt_tokens":100,"completion_tokens":20,"cost_usd":0.25}`,
		"/tmp/api-integrations/transfer.jsonl",
	)
	if _, err := source.CompactAPIIntegrationUsageEvents(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("CompactAPIIntegrationUsageEvents: %v", err)
	}

	archive := exportTransferBytes(t, source)
	destination := newTransferTestStore(t)
	first, err := destination.ImportData(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("ImportData first: %v", err)
	}
	if first.Tables["api_integration_usage_hourly"].Inserted != 1 {
		t.Fatalf("first summary=%+v", first.Tables["api_integration_usage_hourly"])
	}

	totals, err := destination.QueryAPIIntegrationUsageTotals(
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		"Codex CLI",
	)
	if err != nil {
		t.Fatalf("QueryAPIIntegrationUsageTotals: %v", err)
	}
	if len(totals) != 1 || totals[0].RequestCount != 1 || totals[0].TotalTokens != 120 || totals[0].TotalCostUSD != 0.25 {
		t.Fatalf("totals=%+v", totals)
	}

	repeat, err := destination.ImportData(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("ImportData repeat: %v", err)
	}
	if repeat.Tables["api_integration_usage_hourly"].Skipped != 1 {
		t.Fatalf("repeat summary=%+v", repeat.Tables["api_integration_usage_hourly"])
	}
}

func TestImportDataRecognizesRowsFromSameInstallationWithoutMappings(t *testing.T) {
	s := newTransferTestStore(t)
	seedSyntheticTransferSnapshot(t, s, 10)
	archive := exportTransferBytes(t, s)

	var provenanceCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM data_transfer_records`).Scan(&provenanceCount); err != nil {
		t.Fatalf("count provenance: %v", err)
	}
	if provenanceCount != 0 {
		t.Fatalf("local export created %d redundant provenance rows", provenanceCount)
	}

	summary, err := s.ImportData(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("self import: %v", err)
	}
	if summary.Total.Inserted != 0 || summary.Total.Skipped == 0 {
		t.Fatalf("self import summary = %#v, want only skipped rows", summary)
	}
	var snapshotCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quota_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("self import left %d snapshots, want 1", snapshotCount)
	}
}

func TestImportDataRejectsUnsupportedFormatAndChecksumMismatch(t *testing.T) {
	source := newTransferTestStore(t)
	archive := exportTransferBytes(t, source)
	destination := newTransferTestStore(t)

	unsupported := rewriteTransferArchive(t, archive, nil, func(manifest *TransferManifest) {
		manifest.FormatVersion = TransferFormatVersion + 1
	})
	if _, err := destination.ImportData(bytes.NewReader(unsupported)); err == nil || !strings.Contains(err.Error(), "unsupported transfer format") {
		t.Fatalf("unsupported format error = %v", err)
	}

	badChecksum := rewriteTransferArchive(t, archive, nil, func(manifest *TransferManifest) {
		manifest.DatabaseSHA256 = strings.Repeat("0", 64)
	})
	if _, err := destination.ImportData(bytes.NewReader(badChecksum)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
}

func TestImportDataValidatesSchemaAndRollsBackMalformedPayload(t *testing.T) {
	source := newTransferTestStore(t)
	seedSyntheticTransferSnapshot(t, source, 10)
	seedSyntheticTransferSnapshot(t, source, 100)
	archive := exportTransferBytes(t, source)
	destination := newTransferTestStore(t)

	badSchema := rewriteTransferArchive(t, archive, func(db *sql.DB) {
		if _, err := db.Exec(`ALTER TABLE transfer_settings DROP COLUMN value`); err != nil {
			t.Fatalf("remove required transfer column: %v", err)
		}
	}, nil)
	if _, err := destination.ImportData(bytes.NewReader(badSchema)); err == nil || !strings.Contains(err.Error(), `missing column "value"`) {
		t.Fatalf("schema validation error = %v", err)
	}

	malformed := rewriteTransferArchive(t, archive, func(db *sql.DB) {
		var originID string
		if err := db.QueryRow(`SELECT origin_id FROM transfer_rows WHERE table_name = 'quota_snapshots' LIMIT 1`).Scan(&originID); err != nil {
			t.Fatalf("find transfer origin: %v", err)
		}
		if _, err := db.Exec(`
			UPDATE transfer_rows
			SET payload_json = '{"provider":"synthetic"}'
			WHERE table_name = 'quota_snapshots' AND origin_id = ? AND origin_record_id = '2'
		`, originID); err != nil {
			t.Fatalf("corrupt second transfer payload: %v", err)
		}
	}, nil)
	if _, err := destination.ImportData(bytes.NewReader(malformed)); err == nil || !strings.Contains(err.Error(), "missing column") {
		t.Fatalf("malformed payload error = %v", err)
	}
	var count int
	if err := destination.db.QueryRow(`SELECT COUNT(*) FROM quota_snapshots`).Scan(&count); err != nil {
		t.Fatalf("count destination snapshots: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction rollback left %d snapshots, want 0", count)
	}
}

func TestImportDataMonotonicallyMergesSessionsAndCycles(t *testing.T) {
	source := newTransferTestStore(t)
	if _, err := source.db.Exec(`
		INSERT INTO sessions (
			id, provider, started_at, ended_at, poll_interval,
			max_sub_requests, max_search_requests, max_tool_requests,
			start_sub_requests, start_search_requests, start_tool_requests, snapshot_count
		) VALUES ('session-a', 'synthetic', '2026-07-22T12:00:00Z', NULL, 60, 10, 2, 1, 0, 0, 0, 1)
	`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	result, err := source.db.Exec(`
		INSERT INTO reset_cycles (provider, quota_type, cycle_start, cycle_end, renews_at, peak_requests, total_delta)
		VALUES ('synthetic', 'subscription', '2026-07-22T12:00:00Z', NULL, '2026-07-23T00:00:00Z', 10, 10)
	`)
	if err != nil {
		t.Fatalf("insert cycle: %v", err)
	}
	cycleID, _ := result.LastInsertId()
	openArchive := exportTransferBytes(t, source)

	if _, err := source.db.Exec(`UPDATE sessions SET ended_at = '2026-07-22T13:00:00Z', max_sub_requests = 100, snapshot_count = 5 WHERE id = 'session-a'`); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if _, err := source.db.Exec(`UPDATE reset_cycles SET cycle_end = '2026-07-22T13:00:00Z', peak_requests = 100, total_delta = 90 WHERE id = ?`, cycleID); err != nil {
		t.Fatalf("close cycle: %v", err)
	}
	closedArchive := exportTransferBytes(t, source)

	destination := newTransferTestStore(t)
	if _, err := destination.ImportData(bytes.NewReader(openArchive)); err != nil {
		t.Fatalf("import open archive: %v", err)
	}
	closedSummary, err := destination.ImportData(bytes.NewReader(closedArchive))
	if err != nil {
		t.Fatalf("import closed archive: %v", err)
	}
	if closedSummary.Total.Updated < 2 {
		t.Fatalf("closed summary = %#v, want session and cycle updates", closedSummary)
	}
	if _, err := destination.ImportData(bytes.NewReader(openArchive)); err != nil {
		t.Fatalf("reimport old archive: %v", err)
	}

	var endedAt sql.NullString
	var maxRequests float64
	var snapshotCount int
	if err := destination.db.QueryRow(`SELECT ended_at, max_sub_requests, snapshot_count FROM sessions LIMIT 1`).Scan(&endedAt, &maxRequests, &snapshotCount); err != nil {
		t.Fatalf("query imported session: %v", err)
	}
	if endedAt.String != "2026-07-22T13:00:00Z" || maxRequests != 100 || snapshotCount != 5 {
		t.Fatalf("session regressed: ended=%q max=%v count=%d", endedAt.String, maxRequests, snapshotCount)
	}
	var cycleEnd sql.NullString
	var peak, delta float64
	if err := destination.db.QueryRow(`SELECT cycle_end, peak_requests, total_delta FROM reset_cycles LIMIT 1`).Scan(&cycleEnd, &peak, &delta); err != nil {
		t.Fatalf("query imported cycle: %v", err)
	}
	if cycleEnd.String != "2026-07-22T13:00:00Z" || peak != 100 || delta != 90 {
		t.Fatalf("cycle regressed: end=%q peak=%v delta=%v", cycleEnd.String, peak, delta)
	}
}

func TestImportDataRemapsParentsAndRelayDoesNotDuplicate(t *testing.T) {
	source := newTransferTestStore(t)
	result, err := source.db.Exec(`INSERT INTO anthropic_snapshots (captured_at, raw_json, quota_count) VALUES ('2026-07-22T12:00:00Z', '{}', 1)`)
	if err != nil {
		t.Fatalf("insert anthropic snapshot: %v", err)
	}
	snapshotID, _ := result.LastInsertId()
	if _, err := source.db.Exec(`INSERT INTO anthropic_quota_values (snapshot_id, quota_name, utilization, resets_at) VALUES (?, 'five_hour', 10, '2026-07-22T17:00:00Z')`, snapshotID); err != nil {
		t.Fatalf("insert anthropic value: %v", err)
	}
	var accountID int64
	if err := source.db.QueryRow(`SELECT id FROM provider_accounts WHERE provider = 'codex' AND name = 'default'`).Scan(&accountID); err != nil {
		t.Fatalf("query codex account: %v", err)
	}
	if _, err := source.db.Exec(`INSERT INTO codex_snapshots (captured_at, account_id, plan_type, credits_balance, raw_json, quota_count) VALUES ('2026-07-22T12:00:00Z', ?, 'plus', 5, '{}', 0)`, accountID); err != nil {
		t.Fatalf("insert codex snapshot: %v", err)
	}

	sourceArchive := exportTransferBytes(t, source)
	middle := newTransferTestStore(t)
	if _, err := middle.ImportData(bytes.NewReader(sourceArchive)); err != nil {
		t.Fatalf("import source: %v", err)
	}
	var linkedChildren int
	if err := middle.db.QueryRow(`
		SELECT COUNT(*) FROM anthropic_quota_values AS q
		JOIN anthropic_snapshots AS s ON s.id = q.snapshot_id
		WHERE q.quota_name = 'five_hour' AND s.captured_at = '2026-07-22T12:00:00Z'
	`).Scan(&linkedChildren); err != nil {
		t.Fatalf("query linked children: %v", err)
	}
	if linkedChildren != 1 {
		t.Fatalf("linked child count = %d, want 1", linkedChildren)
	}
	var linkedAccounts int
	if err := middle.db.QueryRow(`
		SELECT COUNT(*) FROM codex_snapshots AS s
		JOIN provider_accounts AS a ON a.id = s.account_id
		WHERE a.provider = 'codex' AND a.name = 'default' AND s.captured_at = '2026-07-22T12:00:00Z'
	`).Scan(&linkedAccounts); err != nil {
		t.Fatalf("query linked accounts: %v", err)
	}
	if linkedAccounts != 1 {
		t.Fatalf("linked account count = %d, want 1", linkedAccounts)
	}

	relayArchive := exportTransferBytes(t, middle)
	final := newTransferTestStore(t)
	if _, err := final.ImportData(bytes.NewReader(sourceArchive)); err != nil {
		t.Fatalf("final import source: %v", err)
	}
	if _, err := final.ImportData(bytes.NewReader(relayArchive)); err != nil {
		t.Fatalf("final import relay: %v", err)
	}
	var snapshotCount, valueCount, codexCount int
	if err := final.db.QueryRow(`SELECT COUNT(*) FROM anthropic_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count anthropic snapshots: %v", err)
	}
	if err := final.db.QueryRow(`SELECT COUNT(*) FROM anthropic_quota_values`).Scan(&valueCount); err != nil {
		t.Fatalf("count anthropic values: %v", err)
	}
	if err := final.db.QueryRow(`SELECT COUNT(*) FROM codex_snapshots`).Scan(&codexCount); err != nil {
		t.Fatalf("count codex snapshots: %v", err)
	}
	if snapshotCount != 1 || valueCount != 1 || codexCount != 1 {
		t.Fatalf("relay duplicated rows: snapshots=%d values=%d codex=%d", snapshotCount, valueCount, codexCount)
	}
}

func TestTransferSchemaCreatesStableInstallationAndProvenance(t *testing.T) {
	s := newTransferTestStore(t)

	first, err := s.TransferInstallationID()
	if err != nil {
		t.Fatalf("TransferInstallationID first: %v", err)
	}
	second, err := s.TransferInstallationID()
	if err != nil {
		t.Fatalf("TransferInstallationID second: %v", err)
	}
	if first != second {
		t.Fatalf("installation ID changed: %q != %q", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(first) {
		t.Fatalf("installation ID %q is not 32 lowercase hex characters", first)
	}

	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	origin := transferOrigin{OriginID: "source-a", OriginRecordID: "42"}
	if err := recordImportedOrigin(tx, "quota_snapshots", "7", origin); err != nil {
		t.Fatalf("recordImportedOrigin: %v", err)
	}
	localID, ok, err := findImportedLocalID(tx, "quota_snapshots", origin, first)
	if err != nil {
		t.Fatalf("findImportedLocalID: %v", err)
	}
	if !ok || localID != "7" {
		t.Fatalf("findImportedLocalID = %q, %v; want 7, true", localID, ok)
	}

	if err := recordImportedOrigin(tx, "quota_snapshots", "8", origin); err == nil {
		t.Fatal("duplicate origin unexpectedly succeeded")
	}
	if _, ok, err := findImportedLocalID(tx, "quota_snapshots", transferOrigin{OriginID: "missing", OriginRecordID: "1"}, first); err != nil || ok {
		t.Fatalf("missing origin = ok %v, err %v; want false, nil", ok, err)
	}

	var state string
	if err := tx.QueryRow(`SELECT value FROM data_transfer_state WHERE key = 'installation_id'`).Scan(&state); err != nil && err != sql.ErrNoRows {
		t.Fatalf("query transfer state: %v", err)
	}
}
