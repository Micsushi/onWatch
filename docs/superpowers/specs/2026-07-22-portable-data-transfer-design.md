# Portable Data Transfer Design

## Goal

Let users export onWatch history from macOS, Linux, or Windows into one portable ZIP file, transfer that file to another installation, and merge it through either the terminal or Settings.

Imports are additive. Records produced on different machines remain distinct even when provider, account, timestamp, and values overlap. Reimporting the same originating record must not duplicate it.

## User Workflow

Terminal export and import:

```text
onwatch data export --out mac-history.onwatch.zip
onwatch data export --out linux-history.onwatch.zip
onwatch data import mac-history.onwatch.zip linux-history.onwatch.zip
```

Settings provides the same operations:

- **Export data** downloads an `.onwatch.zip` archive.
- **Import data** accepts one or more `.onwatch.zip` archives and merges them sequentially.
- Import feedback reports inserted, updated, and skipped records for each file.

The default terminal export filename is `onwatch-history-YYYYMMDD-HHMMSS.onwatch.zip` in the current directory.

## Scope

Exported history includes:

- Provider snapshots and their child quota/model/bucket values.
- Reset cycles.
- Usage sessions.
- Z.ai hourly usage.
- API Integration usage events.
- Provider-account identities required to associate Codex and MiniMax history.
- An allowlist of non-secret display and notification preferences.

Exported settings may include:

- Timezone.
- Hidden insights.
- Dashboard fork preferences.
- Provider visibility.
- API Integrations visibility.
- Menubar preferences.
- Notification thresholds and per-quota overrides, with delivery channels disabled.
- Provider display mode, pace mode, source selection, detection preference, and region.

The archive never includes:

- Provider tokens, API keys, refresh tokens, or CSRF tokens.
- Dashboard users, password hashes, or login sessions.
- SMTP passwords or Discord webhooks.
- Push subscriptions, VAPID keys, or browser authentication material.
- Gemini token storage.
- `.env` files, Codex profiles, credential-store data, logs, PID files, or API Integration ingest cursors.
- Provider-account secret metadata. Only safe account metadata such as region may be copied.

Usage responses stored as historical raw JSON remain part of history. They may contain account labels, email addresses, request metadata, or usage details, so the UI and CLI must state that the archive is not a secrets backup but can still contain private usage data.

## Archive Format

An export is a ZIP created with Go standard-library packages. It contains:

```text
manifest.json
history.sqlite
```

`manifest.json` contains:

- Transfer format version.
- onWatch application version.
- Export timestamp in UTC.
- Source installation ID.
- Per-table record counts.
- SHA-256 checksum of `history.sqlite`.

`history.sqlite` is a sanitized transfer database. It contains only scoped history, safe settings, provider-account mapping data, and provenance metadata. It does not copy the live database wholesale.

The importer rejects archives with an unsupported format version, missing required entries, checksum mismatch, failed SQLite integrity check, or unexpected transfer schema.

## Multi-Device Identity and Deduplication

Each onWatch installation receives a random UUID-style installation ID when data transfer is first used. The ID is stored locally and is not imported as the destination installation ID.

Every transferable row has stable provenance:

```text
origin_installation_id + table_name + origin_record_id
```

The destination stores a mapping between that provenance key and its local row ID. Imported provenance is preserved in later exports, so records relayed through a third installation do not duplicate.

Example:

- Mac records 10 tokens at 12:00.
- Linux records 100 tokens at 12:00.
- Both rows have different origin installation IDs and are imported.
- Importing either archive again skips its already-known row.
- Exporting from the merged destination and importing elsewhere still produces two rows, not four.

Timestamps and payload equality are never cross-device deduplication keys.

## Merge Rules

Import executes in one SQLite transaction per archive.

Immutable records are inserted when their provenance is unseen and skipped when already known. These include snapshots, child values, hourly usage, and API Integration events.

Mutable records use monotonic merge behavior when their provenance already exists:

- Sessions retain the non-empty end time and maximum observed counters.
- Reset cycles retain a non-empty end/reset time and maximum observed peak and delta values.
- Existing destination provider-account names and safe metadata win. Imported accounts are created only when no matching external identity or provider/name exists.

Parent IDs are remapped during import:

- Provider accounts are resolved before account-scoped history.
- Parent snapshots are inserted before child quota/model/bucket values.
- Child records reference the destination snapshot ID, never the source integer ID.

API Integration fingerprints remain unique within the destination. Imported fingerprints are namespaced by record origin so two distinct devices can retain otherwise identical events. Imported source paths are rewritten to a portable origin label plus basename instead of copying an absolute home-directory path.

Safe settings only fill keys absent on the destination. Existing destination settings are never overwritten. Notification delivery channels are disabled in exported settings because delivery credentials are excluded.

## Core Implementation Shape

Transfer logic lives beside the SQLite store so terminal and web surfaces share one implementation and can use the existing single database connection.

Core interfaces and responsibilities:

- `Store.ExportData(writer, options)` creates the sanitized archive.
- `Store.ImportData(reader)` validates and merges one archive, returning a structured summary.
- Provenance schema and migration live in the store package.
- CLI commands are thin argument-parsing wrappers.
- Web handlers stream uploads/downloads and call the same store methods.

No new third-party dependency is required.

## Web API and Settings UI

Authenticated routes:

- `GET /api/data/export` streams the generated archive with an attachment filename.
- `POST /api/data/import` accepts one multipart file and returns the import summary as JSON.

Uploads are streamed to a restricted temporary file instead of loaded fully into memory. The route accepts at most 1 GiB of compressed archive data and 4 GiB of expanded `history.sqlite` data, and removes temporary files on every exit path. CLI import applies the same expansion limit.

Settings gains a compact **Data** tab containing:

- Export description, privacy note, and download button.
- Multi-file import picker accepting `.onwatch.zip`.
- Additive-merge explanation.
- Per-file progress and final inserted/updated/skipped counts.
- Clear corrupt, incompatible, oversized, and failed-import errors.

Import does not delete or replace destination data. A failed archive transaction rolls back without changing history.

## CLI Behavior

`onwatch data export`:

- Uses the configured `ONWATCH_DB_PATH` or `--db` path.
- Can run while the daemon is active; SQLite provides a consistent read transaction.
- Writes through a temporary file and atomically renames on success.
- Refuses to overwrite an existing output file.

`onwatch data import FILE...`:

- Uses the configured destination database.
- Imports files sequentially and prints one summary per file plus totals.
- Continues only while imports succeed; the first invalid or failed archive returns a nonzero exit code.
- Can run while the daemon is active. SQLite transaction locking serializes the import with daemon writes. The daemon sees imported history immediately; imported missing settings may require a page refresh.

## Security and Failure Handling

- Export uses strict setting and metadata allowlists. Unknown future settings are excluded by default.
- Archive paths are fixed; ZIP entry names are never written directly to caller-controlled filesystem paths.
- ZIP expansion is bounded by compressed and uncompressed size limits.
- SQLite integrity and transfer-schema validation happen before destination writes.
- Import SQL is parameterized.
- Web routes require the existing authenticated session and existing request protections.
- Logs contain filenames, counts, and error classes, never archive row payloads.

## Verification

Automated checks cover:

- Two origins with the same provider/account/timestamp both import.
- Reimport of the same archive is idempotent.
- Re-export of imported records preserves origin and does not multiply records.
- Parent/child and provider-account IDs remap correctly.
- Mutable sessions and cycles merge without regression.
- Every prohibited secret category is absent from archive contents.
- Unknown settings and account metadata are excluded.
- Corrupt, incompatible, checksum-mismatched, oversized, and malformed archives make no destination changes.
- CLI parsing, default filenames, multi-file summaries, and existing-file refusal.
- Authenticated export/import handlers and multipart validation.
- Settings import/export interactions and JavaScript syntax.

Repository verification uses `./app.sh --smoke` and `./app.sh --test` as required by the project. After the final change, rebuild and restart the installed Windows binary, confirm it is running from the repo-backed build with the intended database, then exercise one terminal export and one Settings import end to end with two synthetic origins.

Real Mac and Linux archives can be imported after those installations pull and build the feature. They are user data and are not committed to the repository.

## Non-Goals

- Automatic cloud sync or continuous multi-device replication.
- Credential or `.env` backup.
- Conflict-by-conflict settings UI.
- CSV reporting or human-readable analytics export.
- Importing arbitrary SQLite databases or legacy onWatch databases directly.
- Deleting or replacing destination history.
