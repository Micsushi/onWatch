# Subscription value

Open the **Subscription Value** tab to compare Claude, Codex and Antigravity over one period. The provider selector under **Allowance details** shows the quota and model breakdowns. API-equivalent value estimates what recorded tokens would cost at standard API prices. It is not an invoice, cash savings, a quality score or a provider billing formula.

## Reading the view

The All view and the Subscription Value tab show known plan names and recorded billing dates. In **Allowance details**, select the provider and open **Plan settings** to enter the date shown in its billing settings. Leave it blank when there is no confirmed date. After the date passes, onWatch shows it as the last recorded renewal until you update it.

- **Output tokens / API dollar:** output token count divided by the recorded API-equivalent value in the same period. It is shown only when every recorded request in the comparison is priced and the total is positive. Task success is not available from the local usage logs.

- **Observed API value:** token vectors in the selected period priced using current API Standard short-context text rates. The period defaults to the last 30 days. It is not automatically the billing month.
- **API / 100% quota:** matched API value divided by observed percentage-point movement, multiplied by 100. A 0% to 100% observation is near-full meter coverage; 38% to 100% covers 62 points and is extrapolated.
- **Normalized x1:** the per-100% value divided by the configured plan multiplier. This is a normalization assumption, not a measurement of a different plan.

Each allowance is kept separate by quota, plan and reset boundary. A material meter drop also starts a separate observation. Small backwards readings up to five points with the same reset are rejected as stale concurrent counters or corrections; their count is reported. Raw telemetry remains stored. Reset timestamp jitter up to 60 seconds is tolerated. When task telemetry is available, nearby polling observations are excluded to avoid counting a stale poll as a reset. Early resets are observed events; the view does not claim they were redeemed, promotional or banked without external evidence.

Quota at 100% is saturated. Costs after its first observed 100% remain in period totals but cannot be calibrated against further quota movement. Rounded counters need at least five percentage points before an allowance estimate is shown. The displayed rounding range accounts only for one percentage point of endpoint uncertainty, not missing logs or unknown provider policy.

## Comparing early refills

The two charts accumulate priced usage matched to one primary weekly meter per provider. For Antigravity this is the shared Gemini pool. Both charts count scheduled weekly renewals. The observed series counts the full matched value. The estimate withholds an early refill until the previously scheduled deadline and uses remaining percentage points from the prior reading. It prorates the final interval by the fraction that fits, then resumes counting observed usage at the old deadline. The curves share a scale and summarize the selected range by UTC day.

The comparison requires two polls on the same plan and meter, a drop greater than five points and a new deadline more than a minute later than the old one, observed more than a minute before the old deadline. A published reset offer or a saved credit does not establish that an account used it. Meter movement without reliably timed, priced logs consumes quota in the estimate but supplies no dollars. Intervals spanning the old deadline are skipped in both curves, as are unknown-price and archive-overlap intervals. The two totals can agree when there was no confirmed early refill or when all matched usage fit within the original allowance. Outcomes and dollars from different models chosen under tighter limits are outside this estimate.

## Four-week value by model and effort

Each row estimates four ordinary weekly allowances spent entirely on one model, effort and speed. The tier selector shows the roughly $20, $100, and $200 tiers or the account's recorded plan. The observation period selects local samples. The projection always covers four weeks. Available configurations come from observed use and reviewed public data.

For a matching plan and weekly meter, sum tokens and their standard API-equivalent dollars across intervals with at least five quota percentage points and one identifiable model, effort and speed. Divide each sum by the total measured points and multiply by 100 × 4. A 10-point sample scales by 40. Intervals are measured within quota windows, and extra refills never add another allowance to this projection. Input includes fresh tokens, cache reads and cache writes. Prices use each token type's rate. API dollars per dollar paid divides the projected API value by the plan fee. If multiple weekly meters cover the same configuration, the lowest projected output capacity governs. The estimate assumes full use and a similar mix of input, output and caching. Shorter or unmeasured limits can reduce it.

Same-tier samples take precedence. Untagged Claude meters use the saved plan, and the evidence labels that assumption. The [Claude Ultra weekly ratio](https://support.claude.com/en/articles/17728952-what-is-the-ultra-plan) is 10× Pro. [Google's published ratios](https://antigravity.google/blog/changes-to-antigravity-plans/) are 5× and 20× and apply here to the shared Gemini quota. The [current Codex plan description](https://help.openai.com/en/articles/9793128-about-chatgpt-pro-tiers) supplies a tier ordering, so each tier uses independent measurements.

The chart marks local samples, published studies and modeled estimates with
different symbols. **Evidence** shows each applicable source and compares
its four-week API dollars and output with the local estimate when both apply
to the same tier. The two deltas can point in different directions because
cache reads, fresh input and output have different prices. A one-point
published meter change is a particularly coarse estimate. The model-specific
table keeps whole-plan figures such as [Wokey's mixed Codex traffic](https://wokey.ai/codex-usage-limits)
out of per-effort rows.

When a setting has a priced token profile and no matched quota, the chart can
estimate it using the median four-week API value from other efforts of the
same model at Standard speed. It prefers same-tier local measurements. For
Standard GPT-6 Astra and Sol family settings on Plus or $200 Pro, it can also
use a same-tier Astra/Sol budget as a proxy. Their Standard API rates are
proportional to the published credit weights, but the [Codex rate card](https://learn.chatgpt.com/docs/pricing?codex-pricing-speed=ultrafast)
does not publish the included weekly allowance in credits. These projections
assume a similar allowance per API dollar. A recorded or VulcanBench token
profile controls how much of that API value becomes output at the selected
effort. When either input is missing, the estimate stays unavailable.

The combined estimate uses an equal mix of attempts from the eligible suites
available for that exact model and effort. For one attempt on each suite, add
its pass probability and mean output tokens. Multiply four-week output capacity
by the sum of pass probabilities divided by the sum of mean output tokens.
Failures count in the source averages. The metric describes benchmark tasks.
The harness, scoring rule, task length and difficulty can differ between suites.

The **Suites** column counts inputs to each estimate. Open **Evidence** to see
the suite names, passes and mean output per attempt for a row, or select one
suite to compare the same workload across models. Coverage differs by model and
effort. Compare rows that use the same suites. onWatch leaves a forecast blank
when the matching quota or benchmark sample is missing.

The chart uses API dollars per subscription dollar on the horizontal axis and
estimated solves per four weeks on the vertical axis. Its coverage count reflects
how many visible settings have both estimates. Select a plan, provider, model,
or suite to focus the plot. Hover or tap a point for its model, effort, quota
source and suites. Lines group effort levels with the same provider, model,
speed, quota source and suite mix. Local and
published quota projections appear as a range when both exist, and as separate
chart points. The range describes agreement between those sources. **CSV**
exports the individual estimates, four-week token counts, matched quota points
and suite names. Raw local observations stay in onWatch's SQLite history.

## Benchmark audit

The reviewed snapshot lives in `internal/web/static/subscription-benchmarks.json`.
Benchmark rows store model, effort, passes, attempts and mean output tokens
per attempt. Use whole evaluated cohorts for passes and tokens. Keep benchmark
versions and harnesses distinct. The combined forecast uses three suites:

| Suite | Data and review |
| --- | --- |
| [VulcanBench Frontier v4](https://vulcanbench.com/leaderboard.html) | Recomputed functional passes and mean output from the published per-run CSV. The [scorer](https://github.com/morganlinton/VulcanBench/blob/d7ca724f11566eb29171ee50b0f2f32ae1b68d57/harness/evaluator/scorer.py) also computes a quality and efficiency score. The forecast uses functional results. [Astra Max](https://vulcanbench.com/benchmarks/swe-v4-gpt62-astra-v333.html) passed all 23 tasks. The [Neon report](https://vulcanbench.com/benchmarks/swe-v4-gemini4-neon-v332.html) logs 40 quota denials that were rerun outside the scored sample. An [earlier Opus audit](https://vulcanbench.com/assets/reports/vulcanbench-opus5-effort-report.pdf) found a harness token cap caused five of six apparent failures. |
| [Terminal-Bench 4.1](https://www.tbench.ai/?efforts=all&version=4.1) | The official leaderboard payload supplies successes, trial count and separate output totals. The [metrics code](https://github.com/harbor-framework/terminal-bench/blob/v4.1.0/leaderboard/src/leaderboard/core/metrics.py) retains token spend when a trial is disqualified and assigns it zero credit. The [4.1 release](https://github.com/harbor-framework/terminal-bench/releases/tag/v4.1.0) regrades runs with revised isolated verifiers. This snapshot uses 330 trials per native-harness setting. Astra Max passed 205 of 330 here. |
| [Ramp SWE-Bench v2.1](https://labs.ramp.com/swebench#leaderboard) | Recomputed from the [official trial CSV](https://labs.ramp.com/swebench/v2/data/v21/results.csv?v=v2.1). Each of the 13 imported native-harness settings had the same 500 evaluated task IDs and complete output counts. Astra High passed 382, Neon High passed 381 and Sonnet High passed 316. The [methodology](https://builders.ramp.com/post/building-llm-judges-to-judge-llms) includes an LLM judge for additional verification on private engineering tasks. Those tasks and judges cannot be independently reproduced from the CSV. |

The [published Opus 5.5 traces](https://github.com/morganlinton/vulcanbench-opus55-traces/tree/f4e0f2f131fcf7c78910633c12192eb53a179a1f)
distinguish serving models. The VulcanBench rows keep all 23 attempts per
effort, count mixed-model attempts as unsolved for Opus 5.5 alone, and
include only Opus 5.5's output tokens. Medium has 20 solo solves and 23
with fallbacks.

Additional sources reviewed:

- [AI Coding Daily](https://aicodingdaily.com/subscription-usage) supplies November 19 quota deltas, token buckets, and reported API-equivalent dollars for controlled Claude and Codex settings on $20 and $100 plans. Gemini rows report the quota delta without tokens or API dollars. Readings below 1% lack a usable measured delta. Published speed was unspecified. The study's quality scores are rubric totals. The solve forecast uses the coding suites above.
- [Real API Pricing](https://github.com/Fei-Craft/real-api-pricing/blob/332d14ed903c3b7b042bb5a9ec2a3d224757476d/data/research/chatgpt-gpt6luna-plus-round2-2026-09-28.json) supplies a historical GPT-6 Luna Max Plus observation with a six-point weekly delta and all token buckets. Its four-week estimate of $135 can be compared with roughly $106 from the November 19 controlled sample. The difference is within the rounding range of the two-point November reading.
- [DeepSWE v1.1](https://deepswe.datacurve.ai/) is available as a standalone comparison. Its [public verifier](https://github.com/datacurve-ai/deep-swe/blob/0b9fabbb63b9104d678fe965e1632f2dd9eaa2ea/tasks/abs-module-cache-flags/tests/test.sh) grades submitted patches in a verifier environment. [Epoch AI](https://epoch.ai/benchmarks/deepswe/review) confirmed false negatives on at least 23 of 113 tasks, so the suite is excluded from the combined projection.
- [CursorBench](https://cursor.com/cursorbench) uses private Cursor workloads. [SWE-Marathon](https://www.swe-marathon.org/) reports task-weighted pass rates and total tokens, with one task held out. [WorkloadBench](https://workloadbench.brenhq.com/numbers) publishes small samples and its quick suite has a ceiling. [Sherpa prod-bench-1](https://www.sherpalabs.ai/benchmarks/coding) uses private tasks and the leaderboard lacks a reproducible output-token denominator. [Argo-Bench](https://argo-bench.com/) publishes solves and output tokens for a separate enterprise analytics workload. None contribute to the coding forecast.
- [Artificial Analysis Coding Agent Index](https://artificialanalysis.ai/agents/coding-agents) already incorporates other benchmark workloads. Adding the index would duplicate evidence across suites.
- [SubscriptionBench](https://github.com/data-john/SubscriptionBench/blob/bf513a7b42198fba2311cc5fdb35b707f413abcd/site/data/benchmark_data.json) contributes matched Antigravity Pro quota observations only. Those rows store model, effort, meter, quota points, input/cache/output counts, API cost and attempt data. [Wokey](https://wokey.ai/coding-plan-comparison#ranking) aggregates a mix of models and efforts and cannot calibrate a single setting.

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

Standard API rates checked Nov 24, 2026 for the three benchmarked models are $2 input, $0.10 cached input, $2.50 short cache write, and $10 output per million tokens for [GPT-6.1 Sol](https://developers.openai.com/api/docs/models/gpt-6.1-sol) and [Claude Sonnet 5.5](https://platform.claude.com/docs/en/models/sonnet-5-5/overview). Sonnet's one-hour cache write is $4. [Gemini 4 Hydrogen](https://ai.google.dev/gemini-api/docs/pricing#gemini-4-hydrogen) is $1.50 input, $0.075 cached input, and $7.50 output with implicit caching.

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

- [CodexBar](https://github.com/steipete/CodexBar/blob/main/docs/claude.md) distinguishes saved reset credits from scheduled windows. Its [burndown example](https://github.com/steipete/CodexBar/blob/main/docs/widgets/burndown-proof.md) compares observations against an explicit baseline.
- [token-tracker](https://github.com/pepperonas/token-tracker/blob/main/README_EN.md) matches Claude meter movement with observed API costs and labels its forecasts.
- [NerfTrack](https://github.com/NerfTrack/NerfTrack), inspected at `2209711ee291867d1529eef56800be9fb88ee5fe`: useful reset-aware cost/percentage pairing, explicit speed evidence and confidence checks. GPL-3.0. Its live price refresh can reprice old history, so chart changes are not necessarily quota-policy changes. Single-device logs cannot establish a hardware penalty.
- [codex-usage](https://github.com/zJay26/codex-usage), inspected at `53bac40a5402c3f9958b0d4504b136630667a593`: useful machine ownership, token normalization, fixed-point estimates and explicit coverage. MIT. Standard API and optional Codex Fast-weighted views answer different questions; neither establishes a universal subscription denominator.
- [codex-usage-audit](https://github.com/razzededge/codex-usage-audit), inspected at `a3347b75dd598984f600887316a0030dec2d9886`: useful separate credit/API views, context/cache diagnostics and parent/subagent accounting. MIT. A task audit is not a monthly cross-device subscription measurement.
