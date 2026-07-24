# Custom API Integrations Setup Guide

Track Custom API Integrations usage in onWatch with local JSONL ingestion.

Ingest local JSONL files to monitor Custom API Integrations usage in onWatch. This is not for subscription or quota tracking, but for logging token usage in custom scripts and programs that make API calls. Wrap your integrations with telemetry to record per-call token usage, cost, and latency data, and track everything in onWatch.

## Prerequisites

- onWatch with the Custom API Integrations backend enabled
- A script or automation that already calls a supported provider API
- Ability to write a JSONL file locally

Supported v1 providers:

- `anthropic`
- `openai`
- `mistral`
- `openrouter`
- `gemini`

*This list is just getting started... feel free to add more providers as you need them!*

## How It Works

1. Your script calls the provider API.
2. Your script reads the usage fields from the API response.
3. Your script appends one normalised JSON object per line to a file in `~/.onwatch/api-integrations/`.
4. onWatch tails `*.jsonl` files in that directory and stores the events in SQLite.

The source files are just the ingest input. The canonical persisted data lives in `~/.onwatch/data/onwatch.db`.

## Default Paths

- API Integrations directory: `~/.onwatch/api-integrations`
- Database: `~/.onwatch/data/onwatch.db`
- Log file: `~/.onwatch/data/.onwatch.log`

In containers, the default API Integrations directory is `/data/api-integrations`.

## Configuration

Custom API Integrations ingestion is enabled by default.

Optional environment variables:

```env
ONWATCH_API_INTEGRATIONS_ENABLED=true
ONWATCH_API_INTEGRATIONS_DIR=~/.onwatch/api-integrations
ONWATCH_API_INTEGRATIONS_RETENTION=1440h
ONWATCH_AGENT_USAGE_PRICING_JSON=
ONWATCH_CURSOR_USAGE_CSV=
```

If you change `ONWATCH_API_INTEGRATIONS_DIR`, point your scripts and onWatch at the same directory.

Retention notes:

- `ONWATCH_API_INTEGRATIONS_RETENTION` controls how long ingested API Integrations events are kept in SQLite
- default retention is `1440h` which is 60 days
- set `ONWATCH_API_INTEGRATIONS_RETENTION=0` to disable database pruning
- pruning applies only to the SQLite table, not to the source `.jsonl` files
- `ONWATCH_AGENT_USAGE_PRICING_JSON` can point to a LiteLLM-compatible pricing JSON file to override built-in model prices
- `ONWATCH_CURSOR_USAGE_CSV` can point to a Cursor usage export CSV for local Cursor token/cost ingestion

### Historical Pricing

Agent usage is priced using the event timestamp, not the date when the log is imported. Once a cost is stored in SQLite, it is treated as historical data: changing the built-in pricing table or a custom pricing file does not rewrite that cost. The maintenance backfill only fills rows where `cost_usd` is `NULL`.

Flat LiteLLM-compatible overrides remain supported. To describe a price change, use ordered or unordered `history` entries with RFC3339 `effective_from` timestamps:

```json
{
  "example-model": {
    "history": [
      {
        "effective_from": "2026-01-01T00:00:00Z",
        "input_cost_per_token": 0.000010,
        "output_cost_per_token": 0.000060
      },
      {
        "effective_from": "2026-07-01T00:00:00Z",
        "input_cost_per_token": 0.000005,
        "output_cost_per_token": 0.000030
      }
    ]
  }
}
```

The newest entry effective at the event timestamp is used. Events older than the first entry use the earliest known price; events without a timestamp use the latest price.

The built-in OpenAI entries reflect the published launch prices:

- GPT-5.5, effective 2026-04-23: $5 per million input tokens, $0.50 per million cached input tokens, and $30 per million output tokens. Fast/Priority processing is 2.5x the standard price.
- GPT-5.6 Sol, effective 2026-06-26: $5/$0.50/$30 per million input/cached/output tokens.
- GPT-5.6 Terra, effective 2026-06-26: $2.50/$0.25/$15 per million input/cached/output tokens.

OpenAI's published record describes Terra as 2x cheaper than GPT-5.5; it does not describe a retroactive GPT-5.5 price cut. Sources: [Introducing GPT-5.5](https://openai.com/index/introducing-gpt-5-5/), [GPT-5.5 model pricing](https://developers.openai.com/api/docs/models/gpt-5.5), and [Introducing GPT-5.6](https://openai.com/index/gpt-5-6/).

## Local Agent Usage Collector

onWatch also includes a local collector for agent log files. It runs beside API Integrations and writes normalized rows to daily `agent-usage-YYYY-MM-DD.jsonl` queue files in the same ingest directory.

Default local sources:

- Claude Code: `~/.claude/projects`
- Codex CLI: `~/.codex/sessions`
- Gemini CLI: `~/.gemini/tmp` or `GEMINI_DATA_DIR`
- Antigravity/Gemini: `~/.factory/sessions` or `DROID_SESSIONS_DIR`
- Cursor: a CSV file supplied with `ONWATCH_CURSOR_USAGE_CSV`

This keeps local Windows mode and server mode aligned. On a local Windows install, onWatch reads local logs directly. On a hosted server, run the Windows collector mode or sync/mount the API Integrations ingest directory so the server tails the same normalized JSONL.

### Windows Runner With A Linux Dashboard

Use this when the dashboard and SQLite database live on a Linux server, but Codex, Claude Code, Cursor, or other desktop tools run on Windows.

The Linux server should run the normal onWatch daemon with API Integrations enabled. Its ingest directory is usually `/data/api-integrations` in Docker or `~/.onwatch/api-integrations` on a native install. The Windows machine should run only the local agent usage collector and write to a directory that the Linux server can read.

Recommended layouts:

- SMB share: export the Linux ingest directory, then run the Windows runner with `--out \\server\onwatch-api-integrations`
- Sync tool: run the Windows runner into `C:\Users\<you>\.onwatch\api-integrations`, then sync that folder to the Linux ingest directory with Syncthing, Resilio, rclone, or another file sync tool
- Mounted volume: if the Linux server can mount a Windows share, keep the Windows runner local and point Linux `ONWATCH_API_INTEGRATIONS_DIR` at the mounted folder

Run the Windows collector once:

```powershell
onwatch agent-usage --once --out "\\server\onwatch-api-integrations"
```

Run it continuously:

```powershell
onwatch agent-usage --out "\\server\onwatch-api-integrations" --interval 15
```

The runner does not start the dashboard, does not need provider API keys, and does not open the SQLite database. It only reads local agent logs and writes normalized `agent-usage-YYYY-MM-DD.jsonl` queue rows. The Linux onWatch daemon remains responsible for ingesting those rows, storing them, and serving the dashboard. After the daemon has fully ingested an `agent-usage*.jsonl` queue file, it removes that file so SQLite remains the durable history and the ingest folder stays small. The collector also keeps a compact `.agent-usage-seen` cache in the ingest directory so archived sessions are not re-emitted after the queue file is removed.

If you need to scan a different Windows profile or portable Codex home:

```powershell
onwatch agent-usage --home "D:\Users\agent" --out "\\server\onwatch-api-integrations"
```

### Reasoning Effort And Fast Mode

Codex session logs expose model and effort context in `turn_context` records. onWatch records that data into usage metadata when present:

- `reasoning_effort`: values such as `low`, `medium`, `high`, or `xhigh`
- `mode`: the Codex collaboration mode, when logged
- `fast_mode` and `speed_mode`: whether the turn was recorded as fast or standard, when logged
- `speed_multiplier`: best-effort multiplier, currently `2.5` for GPT-5.5 and `2.0` for GPT-5.4 when Codex Desktop global state reports the fast service tier

The Cost tab groups model usage by effort, mode, and speed. Older rows or providers that do not expose this context appear as `unknown`. When the collector re-sees a duplicate event with richer metadata, onWatch updates the stored metadata instead of creating a duplicate row.

Codex logs do not currently include the x1.5 speed tier in each `turn_context`. onWatch therefore reads `~/.codex/.codex-global-state.json` as a best-effort side channel when parsing Codex sessions under `~/.codex/sessions`. This is accurate for the normal case where the service tier is set before the turn is collected. If you toggle speed while a chat is already running, old rows may inherit the current global setting because Codex does not timestamp that setting per token event.

### Timestamped Graphs

Every normalized usage event stores a timestamp in `ts`. onWatch persists that timestamp as `captured_at`, then the dashboard history API buckets it into time windows for graphs. The Cost tab can graph estimated cost, token volume, requests, and accumulated token use. Platform-specific tabs can graph estimated cost, total tokens, input tokens, and output tokens over time when timestamped usage rows exist.

## Event Format

Write one JSON object per line.

Required fields:

```json
{
  "ts": "2026-04-03T12:00:00Z",
  "integration": "notes-organiser",
  "provider": "anthropic",
  "model": "claude-3-7-sonnet",
  "prompt_tokens": 1200,
  "completion_tokens": 300
}
```

Optional fields:

- `total_tokens`
- `cost_usd`
- `latency_ms`
- `account`
- `request_id`
- `metadata`

Full example:

```json
{
  "ts": "2026-04-03T12:00:00Z",
  "integration": "notes-organiser",
  "provider": "anthropic",
  "account": "personal",
  "model": "claude-3-7-sonnet",
  "request_id": "req_123",
  "prompt_tokens": 1200,
  "completion_tokens": 300,
  "total_tokens": 1500,
  "cost_usd": 0.0123,
  "latency_ms": 1840,
  "metadata": {
    "task": "weekly-meeting-notes"
  }
}
```

Notes:

- `ts` must be RFC3339 in UTC, for example `2026-04-03T12:00:00Z`
- `provider` must be one of the supported v1 provider names
- `metadata` must be a JSON object if present
- If `account` is omitted, onWatch stores it as `default`
- If `total_tokens` is omitted, onWatch computes `prompt_tokens + completion_tokens`

## Python Examples

Python-first examples are included here:

- `examples/api_integrations/python/onwatch_api_integrations.py`
- `examples/api_integrations/python/anthropic_example.py`
- `examples/api_integrations/python/openai_example.py`
- `examples/api_integrations/python/mistral_example.py`
- `examples/api_integrations/python/openrouter_example.py`
- `examples/api_integrations/python/gemini_example.py`

The helper in `examples/api_integrations/python/onwatch_api_integrations.py` appends normalised JSONL events to the API Integrations directory.

These are initial examples to show the general use-case, but the logic can be expanded to any API-driven custom integration you want.

Included example utilities currently include:

- `examples/api_integrations/python/generate_practice_dataset.py`

You can also build your own wrapper around the helper and write any number of integration-specific JSONL files, as long as they end in `.jsonl`.

## Dashboard and API

Once events are being ingested, open the `API Integrations` tab in the dashboard.

The dashboard shows:

- per-integration cards with request counts, token totals, providers, and optional cost
- all-time and recent usage insight panels
- a shared usage chart with metric modes for tokens per call, API calls, accumulated tokens, and cost
- ingest health, tailed files, and recent alerts

API Integrations can also be queried through the read-only backend API:

- `GET /api/api-integrations/current`
- `GET /api/api-integrations/history?range=6h`
- `GET /api/api-integrations/health`

Dashboard visibility is controlled through the normal settings API via `api_integrations_visibility`, but ingestion itself is controlled by `ONWATCH_API_INTEGRATIONS_ENABLED`.

## Start onWatch

Foreground mode is easiest for first-time verification:

```bash
onwatch --debug
```

You should see a log line showing that the API integrations ingester started.

## Verify File Output

Check that your script is writing JSONL events:

```bash
ls -la ~/.onwatch/api-integrations
tail -n 20 ~/.onwatch/api-integrations/*.jsonl
```

Each API call should append one valid JSON line.

## Verify Database Ingestion

Check recently ingested API integrations usage events:

```bash
sqlite3 ~/.onwatch/data/onwatch.db "select integration_name, provider, account_name, model, prompt_tokens, completion_tokens, total_tokens, captured_at from api_integration_usage_events order by id desc limit 20;"
```

Check ingest cursor state:

```bash
sqlite3 ~/.onwatch/data/onwatch.db "select source_path, offset_bytes, file_size, partial_line from api_integration_ingest_state;"
```

Expected result:

- `api_integration_usage_events` contains one row per ingested event
- `api_integration_ingest_state` contains one row per tailed file
- `offset_bytes` increases as the file grows

## Troubleshooting

### No rows appear in `api_integration_usage_events`

Check:

- onWatch is running
- `ONWATCH_API_INTEGRATIONS_ENABLED` is not set to `false`
- your script writes into the same directory that onWatch is tailing
- the file name ends with `.jsonl`
- each line is valid JSON

Run:

```bash
tail -f ~/.onwatch/data/.onwatch.log
```

### Invalid lines are skipped

onWatch skips malformed or schema-invalid lines and creates a system alert instead of stopping ingestion.

Check recent alerts:

```bash
sqlite3 ~/.onwatch/data/onwatch.db "select provider, alert_type, title, message, created_at from system_alerts where provider = 'api_integrations' order by id desc limit 20;"
```

### Duplicate rows

onWatch deduplicates ingested events using a derived fingerprint based on the source path and stable event fields.

This protects against:

- daemon restart
- file reread after truncation
- repeated scans of the same already-ingested lines

If you intentionally want two events, they must differ in at least one meaningful field such as timestamp or request id.

### Rotating source files

If you want to start a fresh source log for new events, move or rename the active `.jsonl` file and let your wrapper create a new one.

Notes:

- onWatch will treat the new file as a new ingest source
- previously ingested history remains in SQLite until you clear or replace the stored database
- rotating the source file changes future ingestion, but it does not erase existing chart history by itself

## Backend Storage

Custom API Integrations data is stored in separate SQLite tables from the existing subscription/quota tracking tables:

- `api_integration_usage_events`
- `api_integration_ingest_state`

This means Custom API Integrations telemetry is identifiable and queryable independently from provider quota snapshots and reset cycles.

Database retention behavior:

- onWatch automatically prunes old rows from `api_integration_usage_events`
- the pruning cutoff is controlled by `ONWATCH_API_INTEGRATIONS_RETENTION`
- the default is 60 days
- source `.jsonl` files are not pruned or compacted by onWatch
- if you want smaller source logs, rotate or remove the JSONL files manually
