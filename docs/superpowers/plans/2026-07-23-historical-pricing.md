# Historical Pricing Implementation Plan

> REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**Goal:** Select model pricing by event timestamp while preserving every existing non-null stored cost.

**Architecture:** Extend `PricingMap` from one rate per model to ordered effective-dated periods, retaining compatibility with existing flat JSON. Route parser and manual backfill calculations through `CalculateCostAt`, and make the backfill script null-only so normal maintenance cannot rewrite history.

**Tech Stack:** Go, SQLite, JSON, existing `internal/agentusage` parser and pricing packages.

## Task 1: Effective-dated pricing registry

**Files:**

- Modify `internal/agentusage/pricing.go`
- Modify `internal/agentusage/pricing_test.go`

- [ ] Add a failing test that loads:

```go
{
  "gpt-test": {
    "history": [
      {"effective_from":"2026-01-01T00:00:00Z","input_cost_per_token":0.000010},
      {"effective_from":"2026-07-01T00:00:00Z","input_cost_per_token":0.000005}
    ]
  }
}
```

and asserts `CalculateCostAt` returns the old rate before July, the new rate after July, the earliest rate before all periods, and the latest rate for a zero timestamp.

- [ ] Run:

```powershell
go test ./internal/agentusage -run TestPricingMapCalculateCostAt -count=1
```

Expected: compilation failure because `CalculateCostAt` does not exist.

- [ ] Implement `pricingPeriod`, compatible JSON decoding, sorted histories, effective-date selection, and a `CalculateCostAt` method. Keep `CalculateCost` as a latest-period wrapper.

- [ ] Add validation tests for malformed dates, empty histories, flat JSON compatibility, provider-prefixed lookup, and cost multipliers.

- [ ] Seed authoritative effective dates for GPT-5.5, GPT-5.6 Sol, and GPT-5.6 Terra in `DefaultPricingMap`.

- [ ] Run:

```powershell
go test ./internal/agentusage -run PricingMap -count=1
```

Expected: pass.

- [ ] Commit:

```text
Add effective-dated pricing
```

## Task 2: Timestamp-aware parsers

**Files:**

- Modify `internal/agentusage/parsers.go`
- Modify `internal/agentusage/parsers_test.go`

- [ ] Add a failing Codex parser test with two usage events on opposite sides of a synthetic rate change:

```go
{"type":"turn.completed","timestamp":"2026-06-30T23:59:59Z","model":"gpt-test","usage":{"input_tokens":100}}
{"type":"turn.completed","timestamp":"2026-07-01T00:00:01Z","model":"gpt-test","usage":{"input_tokens":100}}
```

Assert the first event uses the old cost and the second event uses the new cost.

- [ ] Run:

```powershell
go test ./internal/agentusage -run TestParseCodexUsageFileUsesHistoricalPricing -count=1
```

Expected: failure because the parser uses the latest rate for both events.

- [ ] Change every generated-agent cost calculation to call `CalculateCostAt` with the parsed event timestamp.

- [ ] Add focused Claude and Gemini timestamp tests so all parser families are covered.

- [ ] Run:

```powershell
go test ./internal/agentusage -count=1
```

Expected: pass.

- [ ] Commit:

```text
Price events by timestamp
```

## Task 3: Immutable maintenance backfill

**Files:**

- Modify `scripts/backfill_costs.go`
- Add `scripts/backfill_costs_test.go` only if logic must be extracted into a testable package; otherwise enforce behavior with a source-contract test under `internal/agentusage`.

- [ ] Add a failing test or source-contract check proving the backfill does not issue a blanket `SET cost_usd = NULL`.

- [ ] Add `captured_at` to the backfill row model and query.

- [ ] Remove the reset-all update. Select only `cost_usd IS NULL`.

- [ ] Calculate missing cost with:

```go
pricing.CalculateCostAt(r.model, r.capturedAt, counts, opts)
```

- [ ] Run the focused test and:

```powershell
go test ./... -run 'HistoricalPricing|BackfillCost' -count=1
```

Expected: pass.

- [ ] Commit:

```text
Keep recorded costs immutable
```

## Task 4: Documentation and verification

**Files:**

- Modify `docs/API_INTEGRATIONS_SETUP.md`
- Modify `README.md`

- [ ] Document that:

  - `cost_usd` is fixed when recorded;
  - backfilled logs use the rate effective at the event timestamp;
  - flat custom pricing JSON remains supported;
  - versioned custom history uses `history` plus `effective_from`;
  - estimates are API-equivalent and not subscription billing.

- [ ] Run formatting:

```powershell
gofmt -w internal/agentusage/pricing.go internal/agentusage/pricing_test.go internal/agentusage/parsers.go internal/agentusage/parsers_test.go scripts/backfill_costs.go
```

- [ ] Run targeted tests:

```powershell
go test ./internal/agentusage -count=1
```

- [ ] Run repository smoke verification:

```powershell
& 'C:\Program Files\Git\bin\bash.exe' app.sh --smoke
```

- [ ] Run full verification:

```powershell
& 'C:\Program Files\Git\bin\bash.exe' app.sh --test
```

- [ ] Inspect `git diff --check`, `git status --short`, and the final diff.

- [ ] Commit:

```text
Document historical cost estimates
```

## Task 5: Integrate and publish

- [ ] Merge or cherry-pick the isolated branch onto `main` without staging or overwriting unrelated working-tree changes.
- [ ] Re-run focused pricing tests from the main checkout.
- [ ] Push `main` to `origin`.
- [ ] Report that the installed running binary still uses the previous build unless it is rebuilt and restarted.
