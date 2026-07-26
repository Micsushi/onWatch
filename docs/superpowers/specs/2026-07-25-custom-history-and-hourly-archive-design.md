# Custom History and Hourly Archive Design

## Goal

Let users select an exact calendar range for every dashboard history graph while
reducing SQLite growth without making older one-day views useless.

## Product behavior

Each usage, token-cost, and cost-breakdown view keeps its history controls in
its existing graph or table header. Each control contains the existing presets
in their existing order, followed by `Custom`:

`1h`, `6h`, `24h`, `7d`, `30d`, `All`, `Custom`

Each view has its own Start and End inputs and persisted range state. Changing
one view never changes another view's dates.

- Selecting a preset updates that view's date inputs and keeps that preset
  active.
- Editing either date activates `Custom` and reloads only that view.
- The start and end dates are inclusive calendar dates in the configured
  dashboard timezone.
- The backend uses half-open UTC intervals internally: start-of-start-day
  through start-of-day-after-end.
- Each selected range persists while switching providers and across reloads.
- Invalid or incomplete custom ranges do not clear the current graph. The UI
  explains that both dates are required and that the end cannot precede the
  start.

When a response contains compacted history, the dashboard shows one toast per
range selection:

`Older data is shown at hourly resolution because detailed records are kept for 30 days.`

The `30d` preset is the exact-retention boundary and never shows this toast.
`All` and `Custom` selections still show it when they include archived data.

The response also supplies archive metadata so the UI does not warn for an exact
provider history response that did not use compacted data.

## API contract

History endpoints continue accepting `range` for compatibility. They also
accept:

- `start`: RFC3339 timestamp, inclusive
- `end`: RFC3339 timestamp, exclusive

Both values are required together. Explicit bounds take precedence over
`range`. The shared parser validates:

- both or neither are present;
- timestamps are RFC3339;
- start is before end;
- the interval is no longer than 100 years.

The dashboard sends explicit bounds for presets and custom ranges so the dates
shown in the controls exactly match the data request.

API Integration history and session responses expose archive metadata without
changing the existing JSON shape:

- `X-OnWatch-Archived-Data`
- `X-OnWatch-Archive-Resolution`
- `X-OnWatch-Raw-Retention-Days`

## Storage

Detailed rows in `api_integration_usage_events` are retained for 30 days.
Older rows are aggregated into `api_integration_usage_hourly`.

The hourly natural key is:

- UTC hour
- integration name
- provider
- account name
- model
- reasoning effort
- mode
- speed mode

Each row stores:

- request count
- prompt, completion, total, input, cached-input, cache-creation-input, output,
  and reasoning token totals
- historical cost total
- first and last captured timestamps

The current database has 43,102 raw API events older than 60 days that group
into 395 hourly dimensional rows. Hourly aggregation therefore preserves useful
intraday history while eliminating nearly all old request-row and metadata
overhead.

Provider quota snapshots remain exact in this change. Their storage is much
smaller and their utilization semantics cannot be safely combined using token
sums. Their custom-date views use the same API range contract but do not display
the compressed-data warning unless a future provider-specific archive is
actually queried.

## Compaction

The API Integration ingest agent runs compaction at most hourly.

In one database transaction it:

1. selects detailed rows older than the 30-day cutoff;
2. upserts their totals into hourly archive rows;
3. verifies request and token totals for the candidate batch;
4. deletes only the verified detailed rows;
5. retains the compacted fingerprints in a narrow tombstone table.

The fingerprint tombstones prevent a truncated or replayed source file from
recreating already-compacted events without blocking legitimate late historical
imports. Portable transfer exports include hourly archive rows. Imported rows
are origin-scoped, so histories from different installations combine without a
natural-key collision and repeat imports stay idempotent.

Deleting rows makes pages reusable but does not immediately shrink the SQLite
file. A controlled one-time `VACUUM` is performed only after the live service is
stopped and a backup exists. Normal operation uses WAL checkpointing and page
reuse rather than recurring blocking vacuums.

## Query behavior

API Integration range queries merge:

- hourly archive rows before the compaction boundary;
- detailed rows at or after the boundary.

Ranges crossing the boundary contain no gap or overlap. Token and cost graphs
use zero for empty hourly usage buckets. Cumulative graphs sum the merged
buckets. Model/effort/mode/speed breakdown totals sum both storage tiers.

Chart bucketing never claims more precision than storage provides. An old
single-day range can show up to 24 hourly points. Recent ranges retain the
existing minute and five-minute detail.

## Caching and loading

Cache keys include provider, account, start, and end. Custom history entries in
browser storage use a bounded least-recently-used list so arbitrary date ranges
cannot grow local storage without limit.

The existing stale-while-refresh behavior remains: old chart data stays visible
while a new range loads, and the header refresh indicator remains active until
replacement data arrives.

## Verification

- Unit tests cover time-window parsing, inclusive date conversion, archive
  upsert/delete safety, mixed raw/archive totals, and watermark replay blocking.
- Handler tests cover preset compatibility, explicit custom bounds, invalid
  bounds, and archive response headers.
- JavaScript tests cover graph-local preset/date synchronization, automatic
  Custom selection, independent usage and cost requests, and one warning toast
  per archived selection.
- Smoke tests, JavaScript syntax checks, and headless dashboard checks run
  before deployment.
- Live verification uses the installed database, confirms the new binary and
  profile, exercises recent and archived custom ranges, and measures the
  resulting database size after safe compaction.
