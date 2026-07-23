# Portable Data Transfer Implementation Plan

> REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**Goal:** Add secret-safe, additive, idempotent data export and import through both terminal commands and the Settings page.

**Architecture:** The SQLite store owns one versioned transfer format and all provenance/merge logic. CLI commands and authenticated web handlers are thin adapters over the same store methods. A ZIP contains a manifest plus a sanitized transfer SQLite database so large histories stream without exceeding the app's memory target.

**Tech Stack:** Go 1.25.7 standard library (`archive/zip`, `crypto/rand`, `crypto/sha256`, `database/sql`, `encoding/json`, `io`, `net/http`, `os`), modernc SQLite already in the repository, embedded HTML/CSS/JavaScript.

**Working-tree constraint:** Implement in the existing checkout without disturbing unrelated user changes. Commit and push only with explicit user authorization.

---

## Task 1: Transfer schema and stable provenance

**Files:**

- Modify: `internal/store/store.go`
- Create: `internal/store/transfer.go`
- Create: `internal/store/transfer_test.go`

- [ ] Write `TestTransferSchemaCreatesStableInstallationAndProvenance` in `internal/store/transfer_test.go`. Open a temporary store, call `TransferInstallationID()` twice, assert one non-empty 32-character lowercase hex ID, then insert provenance and assert the `(origin_id, table_name, origin_record_id)` unique key resolves to one local row.
- [ ] Run the focused store test through the repo build wrapper:

  ```bash
  ./app.sh --smoke
  ```

  Expected result: compile failure because transfer methods do not exist.

- [ ] Add schema creation to `createTables()`:

  ```sql
  CREATE TABLE IF NOT EXISTS data_transfer_state (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL
  );
  CREATE TABLE IF NOT EXISTS data_transfer_records (
      table_name TEXT NOT NULL,
      local_record_id TEXT NOT NULL,
      origin_id TEXT NOT NULL,
      origin_record_id TEXT NOT NULL,
      PRIMARY KEY (table_name, local_record_id),
      UNIQUE (table_name, origin_id, origin_record_id)
  );
  ```

- [ ] Implement `TransferInstallationID()` using 16 random bytes and `hex.EncodeToString`, persisted under `data_transfer_state.installation_id`. Use a transaction and `INSERT OR IGNORE` so concurrent first use returns one stable value.
- [ ] Implement internal provenance helpers:

  ```go
  type transferOrigin struct {
      OriginID       string
      OriginRecordID string
  }

  func (s *Store) transferOriginFor(table, localID, installationID string) (transferOrigin, error)
  func recordImportedOrigin(tx *sql.Tx, table, localID string, origin transferOrigin) error
  func findImportedLocalID(tx *sql.Tx, table string, origin transferOrigin) (string, bool, error)
  ```

- [ ] Rerun `./app.sh --smoke`; expect the schema/provenance tests and existing short suite to pass.

## Task 2: Sanitized archive export

**Files:**

- Modify: `internal/store/transfer.go`
- Modify: `internal/store/transfer_test.go`

- [ ] Write `TestExportDataContainsHistoryAndNoSecrets`. Seed snapshots with child rows, cycles, sessions, API Integration usage, provider accounts with `api_key` metadata, safe settings, `provider_settings` secrets, SMTP, Discord, users, auth tokens, push subscriptions, `gemini_tokens`, and `vapid_keys`. Export to a buffer, inspect both ZIP entries and the transfer database, and assert:

  - scoped history rows exist;
  - provider account `region` remains while `api_key` is absent;
  - allowed settings remain;
  - notification channels are disabled;
  - sensitive settings/tables are absent;
  - manifest checksum and counts match.

- [ ] Write `TestExportDataPreservesImportedOrigin`. Seed one local snapshot plus one imported provenance mapping, export, and assert the local row uses this installation ID while the imported row keeps its original origin ID.
- [ ] Run `./app.sh --smoke`; expect compile failures for `ExportData` and archive types.
- [ ] Define the public transfer contract:

  ```go
  const TransferFormatVersion = 1

  type ExportOptions struct { AppVersion string }
  type TransferManifest struct {
      FormatVersion  int            `json:"format_version"`
      AppVersion     string         `json:"app_version"`
      ExportedAt     time.Time      `json:"exported_at"`
      InstallationID string         `json:"installation_id"`
      Counts         map[string]int `json:"counts"`
      DatabaseSHA256 string         `json:"database_sha256"`
  }

  func (s *Store) ExportData(w io.Writer, opts ExportOptions) (TransferManifest, error)
  ```

- [ ] Create a restricted temporary transfer database, build an explicit v1 schema, and copy rows in bounded batches inside a consistent read transaction. Do not clone the live database.
- [ ] Add explicit table descriptors for every scoped history table, including parent order and mutable-kind metadata. Preserve local/imported provenance in the transfer database.
- [ ] Add strict sanitizers:

  - setting allowlist: `timezone`, `hidden_insights`, `fork_preferences`, `provider_visibility`, `api_integrations_visibility`, `menubar`, `notifications`, `provider_settings`;
  - provider-settings field allowlist: `display_mode`, `pace_mode`, `source`, `cc_detection`, `region`;
  - provider-account metadata allowlist: `region`;
  - force notification `channels.email`, `channels.push`, and `channels.discord` false;
  - omit SMTP, Discord, encryption salt, VAPID, Gemini tokens, migration flags, users, auth tokens, subscriptions, alerts, notification log, and ingest state.

- [ ] Hash `history.sqlite`, write `manifest.json` and `history.sqlite` to ZIP, close all writers, and remove temporary files on every path.
- [ ] Rerun `./app.sh --smoke`; expect export and secret-exclusion tests to pass.

## Task 3: Transactional additive import

**Files:**

- Modify: `internal/store/transfer.go`
- Modify: `internal/store/transfer_test.go`

- [ ] Write `TestImportDataKeepsSameTimestampFromDifferentOrigins`. Build two archives whose snapshots share provider/account/timestamp but contain 10 and 100 token values and different installation IDs. Import both and assert two destination rows.
- [ ] Write `TestImportDataIsIdempotentAndRelaySafe`. Import archive A twice, export destination B, then import A and B into C; assert A's record appears once.
- [ ] Write `TestImportDataRemapsParents`. Cover Anthropic, Copilot, Codex, Antigravity, MiniMax, Gemini, and Cursor parent/child IDs plus Codex/MiniMax account IDs.
- [ ] Write `TestImportDataMonotonicallyMergesMutableRows`. Import an open then closed version of the same session/cycle, then reimport the older file; assert end times remain populated and max counters never regress.
- [ ] Write table-driven validation tests for missing entries, unsupported version, checksum mismatch, failed integrity check, unexpected schema, compressed/expanded size excess, corrupt ZIP, and malformed JSON. Compare destination counts before/after each error.
- [ ] Run `./app.sh --smoke`; expect failures because `ImportData` is missing.
- [ ] Define result types:

  ```go
  type ImportTableSummary struct {
      Inserted int `json:"inserted"`
      Updated  int `json:"updated"`
      Skipped  int `json:"skipped"`
  }
  type ImportSummary struct {
      Tables map[string]ImportTableSummary `json:"tables"`
      Total  ImportTableSummary            `json:"total"`
  }
  func (s *Store) ImportData(r io.Reader) (ImportSummary, error)
  ```

- [ ] Stream fixed ZIP entries to restricted temporary files with 1 GiB compressed and 4 GiB expanded limits. Validate manifest, checksum, integrity, and exact transfer schema before opening a destination transaction.
- [ ] Resolve provider accounts first by external identity, then provider/name, stripping metadata to `region`. Record source-account to destination-account mappings.
- [ ] Insert parent snapshots before children and record each source origin to destination local-ID mapping.
- [ ] Deduplicate only by provenance. Never deduplicate across origin IDs by timestamp or payload.
- [ ] Namespace imported API Integration fingerprints with SHA-256 of origin plus original fingerprint and rewrite source paths as `import:<short-origin>/<basename>`.
- [ ] For existing provenance, skip immutable rows. Merge sessions and cycles fieldwise using non-empty terminal times and maxima for counters/peak/delta.
- [ ] Insert safe settings only when the destination key does not exist.
- [ ] Commit only after all scoped tables succeed; otherwise roll back and return a classified error.
- [ ] Rerun `./app.sh --smoke`; expect all transfer-store tests to pass.

## Task 4: Terminal commands

**Files:**

- Create: `data_command.go`
- Create: `data_command_test.go`
- Modify: `main.go`
- Modify: `main_test.go`

- [ ] Write parser tests for:

  ```text
  data export
  data export --out custom.onwatch.zip
  data export --db /tmp/source.db --out custom.onwatch.zip
  data import one.onwatch.zip two.onwatch.zip
  data import --db /tmp/destination.db one.onwatch.zip
  data --help
  ```

  Assert missing subcommands/files, unknown flags, and missing flag values return clear errors.

- [ ] Write command tests using temporary stores and files. Assert export refuses overwrite, default name format is correct, import accepts multiple files, summaries contain inserted/updated/skipped totals, and the first failed file returns nonzero without importing later files.
- [ ] Run `./app.sh --smoke`; expect compile/test failures for the data command.
- [ ] Implement `runDataCommand(args []string) error` with `flag.FlagSet` subcommand parsing, `config.Load()`-compatible DB selection, atomic export via same-directory temporary file plus rename, and sequential import.
- [ ] Route `data` before daemon startup and token detection in `run()`.
- [ ] Add help text and examples:

  ```text
  data export [--out FILE]   Export portable history
  data import FILE...        Merge portable history
  ```

- [ ] Print the privacy note on export and concise per-file/totals summaries on import.
- [ ] Rerun `./app.sh --smoke`; expect terminal-command tests to pass.

## Task 5: Authenticated transfer endpoints

**Files:**

- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Create: `internal/web/data_transfer_test.go`

- [ ] Write handler tests for `GET /api/data/export`: method rejection, ZIP content type, attachment filename, archive signature, and a store failure response.
- [ ] Write handler tests for `POST /api/data/import`: method rejection, missing multipart file, wrong extension, oversize request, corrupt archive, valid archive summary, and transaction rollback.
- [ ] Add server-route tests proving unauthenticated requests redirect/reject and authenticated requests reach both endpoints, including base-path routing.
- [ ] Run `./app.sh --smoke`; expect missing-handler failures.
- [ ] Implement:

  ```go
  func (h *Handler) ExportData(w http.ResponseWriter, r *http.Request)
  func (h *Handler) ImportData(w http.ResponseWriter, r *http.Request)
  ```

- [ ] Stream export through a restricted temporary file so errors can be returned before response headers. Set `Content-Type: application/zip`, `Content-Disposition`, and `Cache-Control: no-store`.
- [ ] Limit multipart upload to 1 GiB, stream to a temporary file, call shared store import, return JSON summary, and clean up on every path.
- [ ] Register `/api/data/export` and `/api/data/import` before middleware wrapping so existing session and CSRF protection applies.
- [ ] Rerun `./app.sh --smoke`; expect endpoint tests to pass.

## Task 6: Settings Data tab

**Files:**

- Modify: `internal/web/templates/settings.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/style.css`
- Modify: `internal/web/handlers_test.go`
- Modify: `tests/js/dashboard-test.js`

- [ ] Add template assertions for a `Data` tab, export button, multi-file `.onwatch.zip` input, privacy note, additive-merge note, and live result region.
- [ ] Add JavaScript tests for download URL, sequential multi-file upload, per-file results, aggregate totals, input reset, double-submit prevention, and server/network errors.
- [ ] Run `./app.sh --smoke`; expect template/JavaScript assertions to fail.
- [ ] Add compact Data tab markup using existing settings section/button/feedback classes and accessible labels/status semantics.
- [ ] Implement browser behavior:

  ```js
  function initDataTransferSettings()
  async function importDataArchives(files)
  function formatImportSummary(filename, summary)
  ```

  Export navigates to `${API_BASE}/api/data/export`. Import sends one `FormData` request per selected file to `${API_BASE}/api/data/import`, stops at first error, and renders counts for completed files.

- [ ] Add only transfer-specific layout styles needed for file controls and summaries; preserve compact dashboard rhythm and responsive behavior.
- [ ] Run `./app.sh --smoke`; expect template and JavaScript checks to pass.

## Task 7: User documentation

**Files:**

- Modify: `README.md`
- Modify: `docs/WINDOWS_SETUP.md`
- Modify: `MACOS_COMPATIBILITY.md`
- Modify: `LINUX_COMPATIBILITY.md`

- [ ] Add a concise portable-data section with terminal examples, Settings workflow, additive/idempotent behavior, and privacy exclusions.
- [ ] Document that Mac/Linux archives should be exported after pulling/building a version containing transfer format v1.
- [ ] State that archives contain private usage history but no provider/dashboard credentials.
- [ ] Confirm all added prose uses hyphens, not em dashes.

## Task 8: Full verification and live application

**Files:**

- Modify only if verification finds a defect in files already owned by this plan.

- [ ] Run formatting and static checks:

  ```bash
  ./app.sh --smoke
  ```

- [ ] Run full required suite:

  ```bash
  ./app.sh --test
  ```

- [ ] Run JavaScript syntax validation used by the repository.
- [ ] Build the production binary with:

  ```bash
  ./app.sh --build
  ```

- [ ] Exercise CLI end to end with two temporary source databases containing same-time 10-token and 100-token records. Export both, import both, reimport both, and query destination counts and values.
- [ ] Start the built app against an isolated temporary database, authenticate, download an archive from Settings/API, import both archives through the web endpoint/UI, and verify summary counts plus dashboard/API history.
- [ ] Rebuild the installed Windows `C:\Users\sushi\.onwatch\bin\onwatch.exe`, restart the intended installed process after the final change, verify executable path, installed database path, healthy post-restart logs, authenticated dashboard HTTP 200, terminal export, Settings export download, and one Settings import of an idempotent synthetic archive.
- [ ] Record exact passed commands and any manual-only verification remaining. Do not claim the feature is live if the installed binary or end-to-end Settings flow is unverified.
