# Custom History and Hourly Archive Implementation Plan

> REQUIRED SUB-SKILL: Use superpowers:executing-plans.

Goal: Add independent custom calendar ranges to every history graph and retain
API Integration request detail for 30 days followed by hourly archives.

Architecture: A shared request-window parser gives every history handler exact
UTC bounds. The browser owns separate persisted range selections for usage,
token-cost, and cost-breakdown views. API Integration data uses a raw-plus-hourly storage model with
transactional compaction and response metadata that drives an accuracy toast.

Tech Stack: Go, SQLite, embedded HTML/CSS/JavaScript, Chart.js, Go tests, and
headless Playwright.

## Task 1: Shared request windows

Files:
- Modify `internal/web/handlers.go`
- Modify `internal/web/handlers_test.go`
- Modify provider handler tests where needed

- [ ] Write tests proving explicit `start` and `end` override presets, both are
  required, malformed timestamps fail, and start must precede end.
- [ ] Run `go test ./internal/web -run 'TimeRange|History.*Custom'` and confirm
  the new tests fail.
- [ ] Add a `historyWindow` parser returning exact start, end, duration, and a
  stable cache key.
- [ ] Replace duration-to-now calculations in every `/api/history` provider
  handler with the shared bounds.
- [ ] Run the focused tests and confirm they pass.

## Task 2: Hourly archive schema and compaction

Files:
- Modify `internal/store/store.go`
- Modify `internal/store/api_integrations_store.go`
- Modify `internal/store/api_integrations_store_test.go`
- Modify `internal/agent/api_integrations_ingest_agent.go`
- Modify `internal/agent/api_integrations_ingest_agent_test.go`
- Modify `internal/config/config.go`
- Modify `internal/config/config_test.go`

- [ ] Write store tests inserting raw events on both sides of a fixed 30-day
  cutoff and asserting hourly dimensional totals.
- [ ] Run the focused store test and confirm the archive table or compaction
  method is missing.
- [ ] Add `api_integration_usage_hourly` and a compaction watermark setting.
- [ ] Implement one-transaction aggregate, checksum verification, delete, and
  watermark advancement.
- [ ] Change the default detailed retention from unlimited to 30 days while
  retaining the existing environment override.
- [ ] Add agent tests proving compaction runs hourly and does not reprocess the
  same interval.
- [ ] Run focused store and agent tests and confirm they pass.

## Task 3: Mixed raw and archived queries

Files:
- Modify `internal/store/api_integrations_store.go`
- Modify `internal/store/api_integrations_store_test.go`
- Modify `internal/web/api_integrations_handlers.go`
- Modify `internal/web/api_integrations_handlers_test.go`

- [ ] Write tests for archive-only, raw-only, and boundary-crossing token, cost,
  model-breakdown, and graph queries.
- [ ] Run the tests and confirm archived rows are initially absent.
- [ ] Merge hourly archive and raw aggregates without overlap.
- [ ] Accept explicit request windows in API Integration history and session
  handlers.
- [ ] Add `_meta.usesArchivedData`, one-hour resolution, and 30-day retention
  to responses.
- [ ] Run focused store and handler tests and confirm they pass.

## Task 4: Graph-local date-range UI

Files:
- Modify `internal/web/templates/dashboard.html`
- Modify `internal/web/static/app.js`
- Modify `internal/web/static/style.css`
- Modify `internal/web/dashboard_performance_test.go`
- Modify `ui-design.md`

- [ ] Add static tests for graph-local range controls, Start/End date inputs,
  and Custom after All.
- [ ] Add JavaScript behavior tests for preset synchronization, date edits
  selecting Custom, and independent usage and cost request bounds.
- [ ] Run the focused web tests and confirm they fail.
- [ ] Keep each range control in the graph or table header it affects.
- [ ] Add persisted inclusive calendar-date state and explicit UTC query
  parameters using the configured timezone.
- [ ] Bound custom history cache entries and preserve old charts during loads.
- [ ] Show the archived-data toast once per selected range when response metadata
  says archived data was used.
- [ ] Update `ui-design.md` with the independent-range and archive-disclosure
  rules.
- [ ] Run focused web and JavaScript syntax tests and confirm they pass.

## Task 5: Transfer and replay safety

Files:
- Modify `internal/store/transfer.go`
- Modify `internal/store/transfer_test.go`
- Modify `internal/store/api_integrations_store.go`
- Modify `internal/agent/api_integrations_ingest_agent.go`

- [ ] Write tests proving hourly archive export/import uses natural-key upserts
  and an event at or before the compaction watermark is not reinserted.
- [ ] Run the focused tests and confirm they fail.
- [ ] Register the archive table in portable transfer.
- [ ] Enforce the compaction watermark in event insertion.
- [ ] Run focused transfer and ingestion tests and confirm they pass.

## Task 6: Full verification and live rollout

Files:
- No additional source files expected

- [ ] Run `node --check internal/web/static/app.js`.
- [ ] Run `git diff --check`.
- [ ] Run `C:\Program Files\Git\bin\bash.exe ./app.sh --smoke`.
- [ ] Run the fullest supported serialized test suite and record any environment
  limitation separately from code failures.
- [ ] Back up the live SQLite database.
- [ ] Rebuild and restart the installed Windows service.
- [ ] Verify executable path, version, data directory, startup logs, and login.
- [ ] Exercise presets, custom dates, provider switching, recent data, archived
  data, the warning toast, and stale-while-refresh behavior headlessly.
- [ ] Run compaction against the live database, checkpoint/VACUUM safely, and
  report before/after row counts and file sizes.
