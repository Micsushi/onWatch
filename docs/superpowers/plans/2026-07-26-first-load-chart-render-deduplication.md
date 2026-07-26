# First-Load Chart Render Deduplication Implementation Plan

> REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or executing-plans.

Goal: Keep the instant saved graph while preventing identical startup payloads
from repainting cost and usage charts.

Architecture: Add one small render-signature helper shared by both Chart.js
history renderers. Each renderer derives a signature from its already bounded
visible data and display state, then performs the existing atomic Chart.js
update only when that signature changed. Cards, summaries, caches, queues, and
freshness continue updating independently.

Tech Stack: Go source-level regression tests, vanilla JavaScript, Chart.js 4,
headless Playwright CLI.

Execution note: The fix depends on the cumulative uncommitted dashboard work in
the current checkout. Creating a worktree from committed `HEAD` would omit that
required base, and copying uncommitted changes is prohibited, so execution must
remain in the current unoccupied `main` checkout. Git commits require separate
user authorization and are not part of execution.

## Task 1: Add Failing Render-Deduplication Contract Tests

Files:

- Modify `internal/web/dashboard_performance_test.go`

- [ ] Step 1: Add a source-contract test that isolates
  `updateChartWhenChanged`, `setMainChartDatasets`, and
  `renderPlatformCostChart`.

```go
func TestDashboardHistoryChartsSkipIdenticalConsecutiveRenders(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function updateChartWhenChanged(signatureKey, renderState, update)",
		"State[signatureKey] === signature",
		"updateChartWhenChanged('mainChartRenderSignature'",
		"updateChartWhenChanged('platformCostChartRenderSignature'",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("history chart render deduplication missing %q", marker)
		}
	}
}
```

- [ ] Step 2: Run the focused test and confirm it fails for the missing helper.

```powershell
go test ./internal/web -run TestDashboardHistoryChartsSkipIdenticalConsecutiveRenders -count=1
```

Expected: FAIL with missing render-deduplication markers.

## Task 2: Implement the Shared Safe Signature Guard

Files:

- Modify `internal/web/static/app.js` near `State` and the dashboard cache
  helpers.

- [ ] Step 1: Add independent state slots.

```js
mainChartRenderSignature: null,
platformCostChartRenderSignature: null,
```

- [ ] Step 2: Add a fail-open helper. A serialization failure must perform the
  update and must not store a misleading signature.

```js
function updateChartWhenChanged(signatureKey, renderState, update) {
  let signature = null;
  try {
    signature = JSON.stringify(renderState);
  } catch (e) {
    // Fail open: a signature problem must never suppress a visible update.
  }
  if (signature !== null && State[signatureKey] === signature) return false;
  update();
  State[signatureKey] = signature;
  return true;
}
```

- [ ] Step 3: Run the focused test. It should still fail because neither chart
  uses the helper yet.

```powershell
go test ./internal/web -run TestDashboardHistoryChartsSkipIdenticalConsecutiveRenders -count=1
```

Expected: FAIL with missing main-chart and platform-cost call markers.

## Task 3: Deduplicate Platform Cost Chart Paints

Files:

- Modify `internal/web/static/app.js:8023-8210`
- Modify `internal/web/static/app.js:7868-7876` where the cost chart is
  destroyed.

- [ ] Step 1: Build a render state after the cost and token series and axis
  maxima are calculated.

```js
const renderState = {
  provider,
  range,
  graphMode,
  empty: false,
  costPoints,
  tokenPoints,
  yMaxCost,
  yMaxTokens,
  colors,
  theme: document.documentElement.getAttribute('data-theme') || 'dark',
};
```

- [ ] Step 2: Wrap both existing-chart updates and first-chart construction so
  identical states leave the current canvas untouched.

```js
updateChartWhenChanged('platformCostChartRenderSignature', renderState, () => {
  if (State.platformCostChart) {
    State.platformCostChart.config.type = chartType;
    State.platformCostChart.data = chartData;
    State.platformCostChart.options = chartOptions;
    State.platformCostChart.update('none');
    return;
  }
  State.platformCostChart = new Chart(canvas, {
    type: chartType,
    data: chartData,
    options: chartOptions,
  });
});
```

- [ ] Step 3: Give the empty state its own signature and clear the stored
  signature whenever the chart is destroyed.

```js
updateChartWhenChanged('platformCostChartRenderSignature', {
  provider,
  range,
  graphMode,
  empty: true,
}, () => {
  State.platformCostChart.data = { datasets: [] };
  State.platformCostChart.update('none');
});
```

```js
State.platformCostChartRenderSignature = null;
```

- [ ] Step 4: Run the focused contract test.

```powershell
go test ./internal/web -run TestDashboardHistoryChartsSkipIdenticalConsecutiveRenders -count=1
```

Expected: still FAIL only for the missing main usage chart guard.

## Task 4: Deduplicate Main Usage Chart Paints

Files:

- Modify `internal/web/static/app.js:5703-5741`
- Modify chart-destroy paths that assign `State.chart = null`

- [ ] Step 1: Calculate the next Y maximum without first mutating the active
  chart, then create the visible render state.

```js
const nextYMax = computeYMax(chartDatasets, State.chart, { cap });
const renderState = {
  provider: getCurrentProvider(),
  range,
  mode,
  empty: false,
  datasets: chartDatasets,
  yMax: nextYMax,
  colors: getThemeColors(),
  theme: document.documentElement.getAttribute('data-theme') || 'dark',
};
```

- [ ] Step 2: Keep summary, cache, and selected-range bookkeeping outside the
  guard, but put all Chart.js mutations inside it.

```js
updateChartWhenChanged('mainChartRenderSignature', renderState, () => {
  State.chart.data.datasets = chartDatasets;
  applyChartGraphMode(State.chart, range, mode);
  updateTimeScale(State.chart, range);
  State.chartYMax = nextYMax;
  State.chart.options.scales.y.max = State.chartYMax;
  State.chart.update('none');
});
```

- [ ] Step 3: Add an explicit empty signature for the empty-data branch and
  clear `mainChartRenderSignature` when the chart is destroyed or recreated.

- [ ] Step 4: Ensure direct theme updates invalidate the matching signature
  before repainting, so a later identical data refresh does not block theme
  application.

- [ ] Step 5: Run the focused test.

```powershell
go test ./internal/web -run TestDashboardHistoryChartsSkipIdenticalConsecutiveRenders -count=1
```

Expected: PASS.

## Task 5: Static and Repository Verification

Files:

- Modify `ui-design.md`

- [ ] Step 1: Add the durable UI rule:

```markdown
- Do not repaint a visible history chart when startup or refresh paths produce
  an identical render state. Cards and freshness may update independently.
```

- [ ] Step 2: Run JavaScript syntax, dashboard regression, diff, and smoke
  checks.

```powershell
node --check internal/web/static/app.js
go test ./internal/web -run TestDashboard -count=1
git diff --check
& 'C:\Program Files\Git\bin\bash.exe' ./app.sh --smoke
```

Expected: all commands exit 0.

## Task 6: Rebuild and Verify the First-Open Flow

Files:

- No persistent verification artifacts.

- [ ] Step 1: Rebuild and restart the installed Windows service.

```powershell
& 'C:\Users\sushi\.onwatch\onwatch.cmd' restart
```

- [ ] Step 2: Verify the listener uses
  `C:\Users\sushi\.onwatch\bin\onwatch.exe`, the app uses
  `C:\Users\sushi\.onwatch\data\onwatch.db`, and startup logs after the restart
  are healthy.

- [ ] Step 3: In a headless Playwright session, make saved 30d cache entries
  older than the two-minute fresh-cache TTL, instrument
  `Chart.prototype.update`, open Codex, and select 30d.

- [ ] Step 4: Confirm:

  - the saved graph appears immediately;
  - no two consecutive updates have identical signatures;
  - a materially changed refreshed response causes at most one replacement
    update;
  - the sticky header remains 67 px;
  - the chart container remains 280 px;
  - subsequent range clicks still update normally.

- [ ] Step 5: Close the headless browser and remove only the temporary
  Playwright artifacts created by this verification.
