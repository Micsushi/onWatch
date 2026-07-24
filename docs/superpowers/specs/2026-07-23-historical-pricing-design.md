# Historical Pricing Design

## Goal

Keep previously recorded usage costs stable when model prices change, while calculating newly discovered historical events with the price that was active when the event occurred.

## Current behavior

onWatch stores `cost_usd` on each `api_integration_usage_events` row and dashboard totals sum that stored value. Imports also preserve the source `cost_usd`. Those paths already avoid display-time repricing.

The remaining gap is historical collection and maintenance:

- the pricing map contains one undated rate per model;
- backfilled session logs use whichever rate is currently bundled;
- the manual cost backfill script clears existing generated costs before recomputing them.

That can make two otherwise identical old events receive different estimates depending on when they were discovered or manually repriced.

## Considered approaches

### 1. Preserve stored cost only

Continue storing `cost_usd` without adding dated rates.

This protects rows already in SQLite but leaves old session-log backfills dependent on today's price. It does not fully meet the requirement.

### 2. Effective-dated price registry

Store an ordered price history for each model. Select the newest rate whose `effective_from` is not after the event timestamp. Keep existing stored costs immutable.

This supports correct historical backfills, future price changes, and deterministic tests without changing the database schema. This is the selected approach.

### 3. Store a full pricing snapshot on every event

Add all component rates and their source to every usage row.

This provides maximum audit detail but requires a database migration, increases row size, and does not solve missing historical prices by itself. It is unnecessary for the present requirement.

## Architecture

### Pricing model

`PricingMap` will map each normalized model name to an ordered list of price periods.

Each period contains:

- `effective_from`, in RFC 3339;
- input cost per token;
- output cost per token;
- cached-input cost per token;
- cache-creation costs.

An entry without `effective_from` is a timeless fallback. Existing custom pricing JSON remains valid and behaves as a single timeless rate.

The bundled map may use either the existing single-rate object or a versioned object:

```json
{
  "gpt-5.5": {
    "history": [
      {
        "effective_from": "2026-04-23T00:00:00Z",
        "input_cost_per_token": 0.000005,
        "output_cost_per_token": 0.00003,
        "cache_read_input_token_cost": 0.0000005,
        "cache_creation_input_token_cost": 0.000005
      }
    ]
  }
}
```

### Selection rules

`CalculateCostAt(model, eventTime, counts, options)` will:

1. resolve the normalized model or provider-prefixed alias;
2. choose the most recent period effective at `eventTime`;
3. use the earliest known period when an event predates every dated period, so legacy events remain estimable;
4. use the latest period when `eventTime` is zero.

`CalculateCost` remains as a compatibility wrapper that uses the latest period.

### Event collection

Every parser already discovers an event timestamp. Cost calculation will use that timestamp instead of an undated rate.

The timestamp is selected before calculating cost for:

- Codex CLI;
- Claude Code;
- Gemini CLI;
- Antigravity;
- Cursor-derived usage where pricing applies.

### Stored-cost immutability

No normal query, dashboard render, import, or collector pass will update a non-null `cost_usd`.

The manual backfill script will stop clearing generated costs. It will fill only rows whose cost is null, selecting the rate by `captured_at`.

If explicit repricing is ever needed, it must be introduced as a separate opt-in command with a preview; it is outside this change.

### Verified initial OpenAI rates

The bundled history will record:

- GPT-5.5 effective 2026-04-23: $5/M input, $0.50/M cached input, $30/M output.
- GPT-5.6 Sol effective 2026-06-26: $5/M input, $30/M output.
- GPT-5.6 Terra effective 2026-06-26: $2.50/M input, $15/M output.

Other existing model entries remain timeless until an authoritative historical change is known. No speculative historical rates will be invented.

Fast/Priority multipliers remain event metadata and continue to multiply the selected base period.

## Compatibility

- Existing pricing override files continue to parse.
- Existing database rows and transfer archives require no migration.
- Existing callers of `CalculateCost` retain latest-price behavior.
- Imported `cost_usd` remains unchanged.

## Testing

Tests will prove:

- legacy single-rate JSON still works;
- versioned histories select old and new rates by timestamp;
- zero timestamps select the latest rate;
- events before the first period use the earliest known rate;
- parser output uses the event timestamp;
- fast-mode multipliers compose with historical rates;
- the backfill path no longer clears existing costs;
- transfer/import continues preserving stored costs.

## Out of scope

- Converting API-equivalent estimates into ChatGPT or Codex subscription credits.
- Guessing historical rates not supported by an authoritative source.
- Retroactively changing existing non-null costs.
- Long-context and regional surcharges; these need explicit per-event applicability data and separate design.
