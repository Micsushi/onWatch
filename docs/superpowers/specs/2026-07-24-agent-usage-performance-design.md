# Agent Usage Performance Design

## Project / Stage / Feature

- Project: onWatch
- Stage OW-S1: Lightweight background collection
- Feature OW-S1-F1: Incremental local agent-usage collection

## Outcome

onWatch keeps local Codex, Claude, Gemini, and Cursor usage current without periodic multi-core stalls. Normal quota polling and API Integration history remain enabled.

## Measured Problem

- The collector clamps its interval to 15 seconds even when the configured poll interval is 120 seconds.
- Each scan recursively rediscovers and sorts all source files.
- A changed Codex JSONL file is parsed again from byte zero. The active file measured 10 MB and caused 7-8 core bursts lasting 6-7 seconds.
- The SQLite WAL and fingerprint cache stayed unchanged during a repeated burst, isolating current cost to discovery, parsing, allocation, and garbage collection.

## Design

### Schedule

Honor the configured poll interval. Keep 15 seconds only as the fallback when callers provide no positive interval. Do not clamp valid slower intervals back to 15 seconds.

### Incremental Codex parsing

Maintain per-file Codex parser state inside the collector:

- next unread byte offset
- current model
- current reasoning effort
- current mode
- current fast-mode flag
- partial trailing line, if any

For an append-only file, seek to the stored offset and parse only complete newly appended JSONL records. Preserve turn context across chunks. Let the collector's persisted event keys remain the durable dedupe boundary.

If file identity changes, size shrinks, or stored offset exceeds size, discard incremental state and parse from byte zero. A malformed complete appended line remains an error; an incomplete trailing line is retained until completed.

Claude/Gemini/Cursor behavior stays unchanged in this delivery. Codex is the measured hot path and receives the stateful tail parser first.

### Source discovery cache

Cache expanded source paths per source for five minutes. Every collection still stats cached files to detect appends. Refresh immediately when no cache exists; otherwise delay discovery of brand-new session files by no more than five minutes.

No filesystem-watcher dependency is added.

## Errors and Recovery

- File truncation or replacement resets state safely.
- Missing files are removed from cached state on the next discovery refresh.
- Parser errors do not advance the stored byte offset.
- Existing initial-backfill and fingerprint persistence behavior remains intact.

## Non-goals

- Replacing the fingerprint cache with SQLite.
- Changing provider quota polling.
- Incremental parsing for non-Codex formats before measurements justify it.
- Coupling onWatch to GameGuard.

## Acceptance

- A configured 120-second collection interval remains 120 seconds.
- Appending a Codex token event parses only the appended region and preserves earlier turn context.
- Truncation causes a safe full reparse.
- New source files appear after cache refresh.
- Existing collector tests remain green.
- Live idle monitoring no longer produces 7-8 core bursts every 15 seconds.

