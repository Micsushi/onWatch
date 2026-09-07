# Subscription value

The Claude, Codex and Antigravity dashboards compare recorded API-equivalent activity with observed quota consumption. This is a valuation estimate, not an invoice, cash savings, a quality score or a provider billing formula.

## Reading the view

- **Observed API value:** token vectors in the selected period priced using current API Standard short-context text rates. The period defaults to the last 30 days. It is not automatically the billing month.
- **Value / subscription fee:** period API value divided by the configured monthly USD fee. $1,000 of recorded value divided by a $200 fee is **5x**, not 5 dollars. Incomplete pricing produces a lower bound.
- **API / 100% quota:** matched API value divided by observed percentage-point movement, multiplied by 100. A 0% to 100% observation is near-full meter coverage; 38% to 100% covers 62 points and is extrapolated.
- **Normalized x1:** the per-100% value divided by the configured plan multiplier. This is a normalization assumption, not a measurement of a different plan.
- **Other plans:** linear x1/x5/x20 scenarios at $20/$100/$200 monthly list prices. Monthly capacity assumes 30/7 weekly allowances, no bonus resets, no unused allowance and unchanged rules. It differs from observed monthly value.

Each allowance is kept separate by quota, plan and reset boundary. A material meter drop also starts a separate observation. Small backwards readings up to five points with the same reset are rejected as stale concurrent counters or corrections; their count is reported. Raw telemetry remains stored. Reset timestamp jitter up to 60 seconds is tolerated. When task telemetry is available, nearby polling observations are excluded to avoid counting a stale poll as a reset. Early resets are observed events; the view does not claim they were redeemed, promotional or banked without external evidence.

Quota at 100% is saturated. Costs after its first observed 100% remain in period totals but cannot be calibrated against further quota movement. Rounded counters need at least five percentage points before an allowance estimate is shown. The displayed rounding range accounts only for one percentage point of endpoint uncertainty, not missing logs or unknown provider policy.

## Model comparisons

Model, effort and recorded speed rows show period token/value totals. Quota columns refer only to the selected allowance. Only intervals with one model/effort/speed combination and at least five points of weekly movement contribute to model calibration. Mixed intervals are not allocated in proportion to API cost. That allocation would assume the relationship the measurement is trying to test.

Output tokens per percentage point describe throughput, not successful work. Model selection still needs matched tasks, error/retry rates and result quality. Cheaper API tokens do not by themselves prove higher subscription value.

## Price basis and evidence

Rates checked 2026-09-07:

| Model | Input / M | Cached input / M | Cache write / M | Output / M |
| --- | ---: | ---: | ---: | ---: |
| GPT-6 Astra | $10 | $1 | $12.50 | $50 |
| GPT-5.6 Sol | $5 | $0.50 | $6.25 | $30 |
| GPT-5.6 Terra | $2 | $0.20 | $2.50 | $12 |
| GPT-5.6 Luna | $0.20 | $0.02 | $0.25 | $1.20 |

Sources: official model pages for [Astra](https://developers.openai.com/api/docs/models/gpt-6-astra), [Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol), [Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra) and [Luna](https://developers.openai.com/api/docs/models/gpt-5.6-luna). Other recognized models use the existing onWatch price catalog. Update the dated catalog when verifying new prices. Historical stored costs are preserved; this comparison reprices token vectors consistently.

Codex credit estimates use the [published credit rate card](https://learn.chatgpt.com/docs/pricing) independently. Astra Standard is 250/25/1,250 credits per million input/cached/output tokens. Its Codex Fast multiplier is 2.5x, while its API Fast multiplier is 2x. The API comparison deliberately excludes speed and long-context premiums. Cache reads/writes are not added to input twice; reasoning is not added to output twice. Config-derived historical speed is unverified, so affected credit estimates stay incomplete.

[Claude plans](https://support.claude.com/en/articles/11049762-choose-a-claude-plan) offer Pro and Max tiers. [Antigravity's plan announcement](https://antigravity.google/blog/changes-to-antigravity-plans) documents 1x/5x/20x scaling and separate non-Gemini quotas. Actual fees may differ through annual billing, promotions, taxes or region; the plan profile is editable with undo/redo.

## Coverage and operation

All three providers use stored quota data. Antigravity uses the separate summary groups and windows. No dollar value is manufactured from quota percentages when token records are absent. Missing price models and quota intervals with no matching local usage suppress allowance estimates. Hourly archives contribute period totals but do not provide reliable per-request timing for quota calibration.

Legacy token account names may not establish identity with a provider account. The API exposes an explicit `usage_account` filter and a Codex `account` selection. Plan-tagged new records can exclude a different subscription, but old records are unverified. Shared web, cloud and other-device activity can move the meter without local logs. Device labels describe source paths and do not prove hardware-based throttling.

`GET /api/subscription-value?provider=codex&days=30` returns comparison data. Providers are `codex`, `anthropic`, `antigravity`; ranges support 1–62 days or explicit RFC3339 `start`/`end`. Excessively large requests fail visibly instead of truncating totals. `PUT` saves the plan profile under the selected provider/account. The browser refreshes visible, inactive views every minute and preserves the prior display on failure.

The collector now preserves safe Codex plan/quota counters and explicit thread service-tier settings. Quota observations survive token compaction and full history export/import. For older logs, backfill against an explicit database after taking a backup:

```powershell
go run scripts/backfill_subscription_meters.go --db C:/path/onwatch.db --source C:/path/.codex/sessions --since 2026-09-01T00:00:00Z
```

This reads quota counters only, retains no transcript content and uses unique observation keys for replay. Import/replay does not change token costs.

## Community tools reviewed

No external tracker dependency or implementation was copied into onWatch.

- [NerfTrack](https://github.com/NerfTrack/NerfTrack), inspected at `2209711ee291867d1529eef56800be9fb88ee5fe`: useful reset-aware cost/percentage pairing, explicit speed evidence and confidence checks. GPL-3.0. Its live price refresh can reprice old history, so chart changes are not necessarily quota-policy changes. Single-device logs cannot establish a hardware penalty.
- [codex-usage](https://github.com/zJay26/codex-usage), inspected at `53bac40a5402c3f9958b0d4504b136630667a593`: useful machine ownership, token normalization, fixed-point estimates and explicit coverage. MIT. Standard API and optional Codex Fast-weighted views answer different questions; neither establishes a universal subscription denominator.
- [codex-usage-audit](https://github.com/razzededge/codex-usage-audit), inspected at `a3347b75dd598984f600887316a0030dec2d9886`: useful separate credit/API views, context/cache diagnostics and parent/subagent accounting. MIT. A task audit is not a monthly cross-device subscription measurement.
