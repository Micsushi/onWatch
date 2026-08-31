package web

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func dashboardJavaScriptBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("%s not found", startMarker)
	}
	endOffset := strings.Index(source[start:], endMarker)
	if endOffset < 0 {
		t.Fatalf("%s not found after %s", endMarker, startMarker)
	}
	return source[start : start+endOffset]
}

func runDashboardNodeTest(t *testing.T, script string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is required for executable dashboard behavior tests: %v", err)
	}
	if output, err := exec.Command(nodePath, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("dashboard behavior failed: %v\n%s", err, output)
	}
}

func TestHistoryRequestQueryUsesTargetPresetWindow(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	normalize := dashboardJavaScriptBetween(t, source, "function normalizeChartRange(", "function normalizeStoredChartRange(")
	preset := dashboardJavaScriptBetween(t, source, "function presetHistoryWindow(", "function historyScopeWindow(")
	scope := dashboardJavaScriptBetween(t, source, "function historyScopeWindow(", "function persistHistoryWindow(")
	ensure := dashboardJavaScriptBetween(t, source, "function ensureHistoryWindow(", "function historySelectionKey(")
	query := dashboardJavaScriptBetween(t, source, "function historyRequestQuery(", "function platformCostHistoryRequestQuery(")

	script := fmt.Sprintf(`
const DEFAULT_CHART_RANGE = '7d';
const State = {
  currentRange: '7d',
  historyWindowStart: '2026-07-01T00:00:00.000Z',
  historyWindowEnd: '2026-07-08T00:00:00.000Z',
};
function applyPresetHistoryWindow() { throw new Error('existing window should not be replaced'); }
%s
%s
%s
%s
%s
function assert(condition, message) {
  if (!condition) throw new Error(message);
}
const presetParams = new URLSearchParams(historyRequestQuery('30d'));
const presetStart = new Date(presetParams.get('start'));
const presetEnd = new Date(presetParams.get('end'));
assert(presetParams.get('range') === '30d', 'target preset must be preserved');
assert(presetEnd - presetStart === 30 * 24 * 60 * 60 * 1000,
  '30d request must use a 30-day window');
assert(presetParams.get('start') !== State.historyWindowStart,
  'preset request must not reuse the active 7d window');

State.currentRange = 'custom';
State.historyWindowStart = '2026-06-01T00:00:00.000Z';
State.historyWindowEnd = '2026-06-03T00:00:00.000Z';
const customParams = new URLSearchParams(historyRequestQuery('custom'));
assert(customParams.get('start') === State.historyWindowStart
  && customParams.get('end') === State.historyWindowEnd,
  'custom request must preserve the selected explicit window');
`, normalize, preset, scope, ensure, query)

	runDashboardNodeTest(t, script)
}

func TestMainHistoryPreferencesAreProviderScoped(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	normalize := dashboardJavaScriptBetween(t, source, "function normalizeChartRange(", "function selectedChartRange(")
	load := dashboardJavaScriptBetween(t, source, "function loadAPIIntegrationsPreferences(", "function saveChartRange(")
	save := dashboardJavaScriptBetween(t, source, "function saveChartRange(", "function savePlatformCostRange(")
	scope := dashboardJavaScriptBetween(t, source, "function historyScopeWindow(", "function persistHistoryWindow(")
	persist := dashboardJavaScriptBetween(t, source, "function persistHistoryWindow(", "function applyPresetHistoryWindow(")

	script := fmt.Sprintf(`
const DEFAULT_CHART_RANGE = '7d';
const CHART_RANGE_STORAGE_KEY = 'chart-range';
const HISTORY_DATE_RANGE_STORAGE_KEY = 'history-dates';
const PLATFORM_COST_RANGE_STORAGE_KEY = 'cost-range';
const PLATFORM_COST_BREAKDOWN_RANGE_STORAGE_KEY = 'breakdown-range';
const PLATFORM_COST_DATE_RANGE_STORAGE_KEY = 'cost-dates';
const PLATFORM_COST_BREAKDOWN_DATE_RANGE_STORAGE_KEY = 'breakdown-dates';
const storage = new Map([
  [CHART_RANGE_STORAGE_KEY, '7d'],
  [HISTORY_DATE_RANGE_STORAGE_KEY, JSON.stringify({
    start: 'legacy-start', end: 'legacy-end', startDate: '2026-01-01', endDate: '2026-01-02',
  })],
]);
const localStorage = {
  getItem(key) { return storage.has(key) ? storage.get(key) : null; },
  setItem(key, value) { storage.set(key, String(value)); },
};
let provider = 'anthropic';
function getCurrentProvider() { return provider; }
function readJSONStorage(target, key) {
  const value = target.getItem(key);
  return value ? JSON.parse(value) : null;
}
function writeJSONStorage(target, key, value) {
  target.setItem(key, JSON.stringify(value));
}
function normalizePlatformCostRange(value) { return normalizeChartRange(value); }
function normalizeAPIIntegrationsMetric(value) { return value || 'tokenPerCall'; }
function normalizeGraphMode(value) { return value || 'cumulative'; }
const State = {};
%s
%s
%s
%s
%s
function resetState() {
  for (const key of Object.keys(State)) delete State[key];
}
function assert(condition, message) {
  if (!condition) throw new Error(message);
}

loadAPIIntegrationsPreferences();
assert(State.currentRange === '7d', 'legacy range should migrate as fallback');
saveChartRange('30d');
State.currentRange = 'custom';
State.historyWindowStart = 'anthropic-start';
State.historyWindowEnd = 'anthropic-end';
State.historyStartDate = '2026-02-01';
State.historyEndDate = '2026-02-02';
persistHistoryWindow('chart');

provider = 'codex';
resetState();
loadAPIIntegrationsPreferences();
assert(State.currentRange === '7d', 'Codex must not inherit Anthropic range');
assert(State.historyWindowStart === 'legacy-start', 'Codex must start from the legacy fallback');
saveChartRange('24h');
State.currentRange = 'custom';
State.historyWindowStart = 'codex-start';
State.historyWindowEnd = 'codex-end';
State.historyStartDate = '2026-03-01';
State.historyEndDate = '2026-03-02';
persistHistoryWindow('chart');

provider = 'anthropic';
resetState();
loadAPIIntegrationsPreferences();
assert(State.currentRange === '30d', 'Anthropic range must survive provider switching');
assert(State.historyWindowStart === 'anthropic-start', 'Anthropic dates must survive provider switching');
assert(storage.get(scopedRangeStorageKey(CHART_RANGE_STORAGE_KEY, 'codex')) === '24h',
  'Codex range must use its scoped key');
`, normalize, load, save, scope, persist)

	runDashboardNodeTest(t, script)
}

func TestCustomHistoryCachesStayBounded(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	providerCache := dashboardJavaScriptBetween(t, source, "function providerDataCacheKey(", "function updateCachedProviderData(")
	platformCache := dashboardJavaScriptBetween(t, source, "function platformCostCacheKey(", "function applyPlatformCostHistoryPayload(")

	script := fmt.Sprintf(`
const PROVIDER_DATA_CACHE_KEY = 'provider-cache';
const PLATFORM_COST_HISTORY_CACHE_KEY = 'cost-cache';
const PROVIDER_CUSTOM_HISTORY_CACHE_LIMIT = 8;
const storage = new Map();
const localStorage = {
  getItem(key) { return storage.has(key) ? storage.get(key) : null; },
  setItem(key, value) { storage.set(key, String(value)); },
};
const State = {
  codexAccount: 1,
  minimaxAccount: '',
  platformCostHistoryCache: {},
  platformCostWindowStart: '',
  platformCostWindowEnd: '',
};
function readJSONStorage(target, key) {
  const value = target.getItem(key);
  return value ? JSON.parse(value) : null;
}
function writeJSONStorage(target, key, value) {
  target.setItem(key, JSON.stringify(value));
}
function normalizeChartRange(value) { return value; }
function normalizePlatformCostRange(value) { return value; }
function historySelectionKey(range, scope) {
  return scope + ':custom:' + State.platformCostWindowStart + ':' + State.platformCostWindowEnd;
}
%s
%s
function assert(condition, message) {
  if (!condition) throw new Error(message);
}

for (let index = 0; index < 12; index += 1) {
  setCachedProviderData(
    'history',
    'anthropic',
    'chart:custom:start-' + index + ':end-' + index,
    { data: [index] },
  );
}
const providerEntries = Object.keys(JSON.parse(storage.get(PROVIDER_DATA_CACHE_KEY)))
  .filter(key => key.startsWith('history:anthropic::chart:custom:'));
assert(providerEntries.length <= PROVIDER_CUSTOM_HISTORY_CACHE_LIMIT,
  'provider custom history cache must be bounded');

for (let index = 0; index < 12; index += 1) {
  State.platformCostWindowStart = 'start-' + index;
  State.platformCostWindowEnd = 'end-' + index;
  setCachedPlatformCostHistory('anthropic', 'custom', { data: [index] }, 'platformCost');
}
const platformEntries = Object.keys(State.platformCostHistoryCache)
  .filter(key => key.startsWith('anthropic:platformCost:platformCost:custom:'));
assert(platformEntries.length <= PROVIDER_CUSTOM_HISTORY_CACHE_LIMIT,
  'platform custom history cache must be bounded');
`, providerCache, platformCache)

	runDashboardNodeTest(t, script)
}

func TestCustomHistoryDatesAcceptUnixEpoch(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	apply := dashboardJavaScriptBetween(t, source, "function applyCustomHistoryDates(", "function setupGraphHistoryRangeControls(")
	script := fmt.Sprintf(`
const State = {};
function historyDateStartUTC(value) { return new Date(value + 'T00:00:00.000Z'); }
function historyDatePlusDays(value, days) {
  const date = new Date(value + 'T00:00:00.000Z');
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}
function setGraphHistoryRangeError() {}
function saveChartRange() {}
function savePlatformCostRange() {}
function savePlatformCostBreakdownRange() {}
function persistHistoryWindow() {}
function syncGraphHistoryRangeControl() {}
%s
if (!applyCustomHistoryDates('chart', '1970-01-01', '1970-01-01')) {
  throw new Error('Unix epoch is a valid date inside the supported history range');
}
if (applyCustomHistoryDates('chart', '1900-01-01', '2026-08-31')) {
  throw new Error('custom history must not submit a range beyond the server limit');
}
`, apply)
	runDashboardNodeTest(t, script)
}

func TestCustomGraphBucketsUseSelectedWindow(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	buildCumulative := dashboardJavaScriptBetween(t, source, "function buildPlatformCumulativeSeries(", "function downsamplePointSeriesForCumulative(")
	downsample := dashboardJavaScriptBetween(t, source, "function downsamplePointSeriesForCumulative(", "function processCappedDataWithGaps(")
	rangeStart := dashboardJavaScriptBetween(t, source, "function platformCostRangeStartTime(", "function platformCostRangeLabel(")
	script := fmt.Sprintf(`
const State = {
  graphMode: 'cumulative',
  historyWindowStart: '2026-04-30T00:00:00.000Z',
  historyWindowEnd: '2026-07-01T00:00:00.000Z',
  platformCostRange: 'custom',
  platformCostWindowStart: '2026-03-01T00:00:00.000Z',
  platformCostWindowEnd: '2026-07-01T00:00:00.000Z',
};
function normalizeGraphMode(value) { return value || 'cumulative'; }
%s
%s
%s
%s
function assertWindow(buckets, startValue, endValue, label) {
  const start = Date.parse(startValue);
  const end = Date.parse(endValue);
  const first = buckets[0];
  const last = buckets[buckets.length - 1];
  if (!(first.periodStart.getTime() <= start && first.periodEnd.getTime() > start)) {
    throw new Error(label + ' buckets must include the selected start');
  }
  if (!(last.periodStart.getTime() < end && last.periodEnd.getTime() >= end)) {
    throw new Error(label + ' buckets must include the selected end');
  }
}

assertWindow(
  graphBucketRange('custom', [{ x: new Date('2026-05-17T23:59:19.707Z') }], 'bucket'),
  State.historyWindowStart,
  State.historyWindowEnd,
  'usage',
);

const costStart = '2026-03-01T00:00:00.000Z';
const costEnd = '2026-07-01T00:00:00.000Z';
assertWindow(
  graphBucketRange('custom', [{ x: new Date('2026-04-04T19:00:00.000Z') }], 'bucket', costStart, costEnd),
  costStart,
  costEnd,
  'cost',
);

const costSeries = buildPlatformCumulativeSeries([
  { capturedAt: '2026-04-04T19:00:00.000Z', totalCostUsd: 100, totalTokens: 1000 },
  { capturedAt: '2026-06-01T00:00:00.000Z', totalCostUsd: 4400, totalTokens: 44000 },
  { capturedAt: '2026-06-30T00:00:00.000Z', totalCostUsd: 1100, totalTokens: 11000 },
], 'custom').cost;
if (costSeries[0].y !== 100) {
  throw new Error('custom cumulative cost must begin with the first real data');
}
if (costSeries.some(point => point.y === 0)) {
  throw new Error('custom cumulative cost must stay blank before the first real data');
}
if (costSeries[0].x.getTime() >= Date.parse('2026-06-06T00:00:00.000Z')) {
  throw new Error('cumulative cost must retain earlier in-window history');
}
`, buckets, buildCumulative, downsample, rangeStart)

	runDashboardNodeTest(t, script)
}

func TestCustomCumulativeCostPointsStayInsideSelectedWindow(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	buildCumulative := dashboardJavaScriptBetween(t, source, "function buildPlatformCumulativeSeries(", "function downsamplePointSeriesForCumulative(")
	downsample := dashboardJavaScriptBetween(t, source, "function downsamplePointSeriesForCumulative(", "function processCappedDataWithGaps(")

	script := fmt.Sprintf(`
const State = {
  graphMode: 'cumulative',
  historyWindowStart: '2026-08-27T07:00:00.000Z',
  historyWindowEnd: '2026-08-30T07:00:00.000Z',
  platformCostRange: 'custom',
  platformCostWindowStart: '2026-08-27T07:00:00.000Z',
  platformCostWindowEnd: '2026-08-30T07:00:00.000Z',
};
function normalizeGraphMode(value) { return value || 'cumulative'; }
function getPollIntervalMs() { return 5 * 60 * 1000; }
%s
%s
%s
const start = Date.parse(State.platformCostWindowStart);
const end = Date.parse(State.platformCostWindowEnd);
const series = buildPlatformCumulativeSeries([
  { capturedAt: State.platformCostWindowStart, totalCostUsd: 1, totalTokens: 100 },
  { capturedAt: '2026-08-29T23:45:00.000Z', totalCostUsd: 2, totalTokens: 200 },
], 'custom').cost;
if (series.some(point => point.x.getTime() < start || point.x.getTime() >= end)) {
  throw new Error('custom cumulative cost points must stay inside the selected axis window');
}
`, buckets, buildCumulative, downsample)

	runDashboardNodeTest(t, script)
}

func TestCumulativeUsageGraphKeepsReturnedObservations(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	style := dashboardJavaScriptBetween(t, source, "function graphLineStyle(", "function applyGraphModeToDatasets(")
	applyMode := dashboardJavaScriptBetween(t, source, "function applyGraphModeToDatasets(", "function formatUsagePercent(")

	script := fmt.Sprintf(`
const State = { graphMode: 'cumulative' };
function normalizeGraphMode(value) { return value || 'cumulative'; }
function isPeriodGraphMode(value) { return value === 'bucket'; }
%s
%s
const observed = Array.from({ length: 189 }, (_, index) => ({
  x: new Date(Date.UTC(2026, 7, 26, 0, index)),
  y: index %% 56,
}));
const segment = () => 'gap-style';
const [dataset] = applyGraphModeToDatasets([{
  label: 'Weekly All-Model',
  data: observed,
  pointRadius: observed.map(() => 2),
  segment,
}], '7d', 'cumulative');
if (dataset.data.length !== observed.length) {
  throw new Error('cumulative usage must keep every observation returned by the sampled history API');
}
if (dataset.data[0].x.getTime() !== observed[0].x.getTime()
  || dataset.data[dataset.data.length - 1].x.getTime() !== observed[observed.length - 1].x.getTime()) {
  throw new Error('cumulative usage must preserve real observation timestamps');
}
if (dataset.segment !== segment) {
  throw new Error('cumulative usage must preserve data-gap styling');
}
`, style, applyMode)

	runDashboardNodeTest(t, script)

	setDatasets := dashboardJavaScriptBetween(t, source, "function setMainChartDatasets(", "function initChart(")
	if !strings.Contains(setDatasets, "const summaryDatasets = isPeriodGraphMode(mode) ? chartDatasets : datasets;") ||
		!strings.Contains(setDatasets, "renderUsageSummary(summaryDatasets, range, mode);") {
		t.Fatal("cumulative usage summary must use observed API points, not reduced display points")
	}
	if !strings.Contains(source, "const countLabel = periodMode ? 'Periods' : 'Observed Samples';") {
		t.Fatal("cumulative usage summary must label its sample count as observed data")
	}
}

func TestUsageAndCostGraphsShareLineRendering(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	style := dashboardJavaScriptBetween(t, source, "function graphLineStyle(", "function applyGraphModeToDatasets(")

	script := fmt.Sprintf(`
%s
const cumulative = graphLineStyle(false);
if (!cumulative.fill || cumulative.tension !== 0.4 || cumulative.borderWidth !== 2
    || cumulative.pointRadius !== 2 || cumulative.pointHoverRadius !== 4
    || cumulative.spanGaps !== false) {
  throw new Error('cumulative usage and cost graphs must share the same line rendering');
}
const period = graphLineStyle(true);
if (period.fill || period.tension !== 0.25 || period.borderWidth !== 2
    || period.pointRadius !== 2 || period.pointHoverRadius !== 4
    || period.spanGaps !== false) {
  throw new Error('period usage and cost graphs must share the same line rendering');
}
`, style)

	runDashboardNodeTest(t, script)
	for _, expected := range []string{
		"...graphLineStyle(false)",
		"...graphLineStyle(periodMode)",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("usage and cost datasets must use the shared line rendering: missing %q", expected)
		}
	}
}

func TestSparseUsageCoverageStaysReadable(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	template := dashboardTemplateSource(t)
	styles := dashboardStyleSource(t)
	lineStyle := dashboardJavaScriptBetween(t, source, "function graphLineStyle(", "function applyGraphModeToDatasets(")
	usageDataset := dashboardJavaScriptBetween(t, source, "function usageLineDataset(", "function buildFlatUsageDatasets(")

	script := fmt.Sprintf(`
const State = { hiddenQuotas: new Set() };
%s
function processDataWithGaps(rawData) {
  const hasGap = rawData.length > 100;
  return {
    data: rawData,
    gapSegments: hasGap ? new Set([50]) : new Set(),
    pointRadii: rawData.map(() => 2),
  };
}
function getSegmentStyle() { return {}; }
%s
const dense = usageLineDataset('Weekly', 'weekly', Array.from({ length: 101 }, (_, index) => ({ x: index, y: index })), '#14B8A6', '7d');
if (dense.fill !== false) {
  throw new Error('disconnected usage coverage must not render heavy filled islands');
}
if (dense.pointRadius.some(radius => radius !== 0)) {
  throw new Error('dense usage history must hide point beads while preserving the line');
}
const short = usageLineDataset('Weekly', 'weekly', [{ x: 1, y: 1 }, { x: 2, y: 2 }], '#14B8A6', '1h');
if (short.fill !== true || short.pointRadius.join(',') !== '2,2') {
  throw new Error('short continuous usage history must keep cost-style fill and points');
}
`, lineStyle, usageDataset)

	runDashboardNodeTest(t, script)
	if !strings.Contains(template, `id="usage-chart-coverage-note"`) {
		t.Fatal("usage graph must provide an inline explanation for blank coverage")
	}
	if !strings.Contains(source, "Blank spans mean no samples were collected.") {
		t.Fatal("usage graph must explain blank collection spans")
	}
	if !strings.Contains(styles, ".chart-coverage-note") {
		t.Fatal("usage graph coverage explanation must use the dashboard visual system")
	}
}

func TestCappedUsageSeriesPreservesObservedTimesAndGaps(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	processCapped := dashboardJavaScriptBetween(t, source, "function processCappedDataWithGaps(", "function buildPlatformBucketSeries(")

	script := fmt.Sprintf(`
function graphBucketMaxCount() { return 3; }
function graphBucketIntervalMs() { return 60 * 60 * 1000; }
function graphBucketStart(date, range, mode, intervalMs) {
  return new Date(Math.floor(new Date(date).getTime() / intervalMs) * intervalMs);
}
function processDataWithGaps(points) {
  return { data: points, gapSegments: new Set(), pointRadii: points.map(() => 2) };
}
%s
const observations = [
  { x: new Date('2026-08-26T00:01:00.000Z'), y: 10 },
  { x: new Date('2026-08-26T00:59:00.000Z'), y: 20 },
  { x: new Date('2026-08-26T01:59:00.000Z'), y: 30 },
  { x: new Date('2026-08-26T02:30:00.000Z'), y: null },
  { x: new Date('2026-08-26T05:01:00.000Z'), y: 40 },
];
const result = processCappedDataWithGaps(observations, '7d');
const times = result.data.map(point => point.x.toISOString());
if (times[0] !== observations[0].x.toISOString() || times[times.length - 1] !== observations[4].x.toISOString()) {
  throw new Error('capped quota series must preserve real endpoint timestamps');
}
const observedNonNullTimes = new Set(observations.filter(point => point.y != null).map(point => point.x.toISOString()));
if (result.data.some(point => point.y != null && !observedNonNullTimes.has(point.x.toISOString()))) {
  throw new Error('capped quota series must not create synthetic observation timestamps');
}
if (!times.includes(observations[2].x.toISOString())) {
  throw new Error('capped quota series must preserve the last observation in each occupied bucket');
}
if (!result.data.some(point => point.y === null)) {
  throw new Error('capped quota series must retain an explicit unknown observation');
}
`, processCapped)

	runDashboardNodeTest(t, script)
}

func TestMissingQuotaValuesStayUnknown(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	if !strings.Contains(source, "y: row[key] ?? null") {
		t.Fatal("missing quota values must stay unknown instead of rendering as zero usage")
	}
}

func TestEventCostSeriesDoesNotInventCollectionGaps(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buildDatasets := dashboardJavaScriptBetween(t, source, "function buildAPIIntegrationsChartDatasets(", "function renderAPIIntegrationsChart(")
	if strings.Contains(buildDatasets, "processDataWithGaps(rawData, range)") {
		t.Fatal("event-driven cost history must not treat time without requests as a collector outage")
	}
	for _, expected := range []string{
		"data: rawData",
		"gapSegments: new Set()",
		"pointRadii: rawData.map(() => 2)",
	} {
		if !strings.Contains(buildDatasets, expected) {
			t.Fatalf("event-driven cost history must keep observed request points: missing %q", expected)
		}
	}
}

func TestUsageRefreshUsesItsExactRequestWindow(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	createJob := dashboardJavaScriptBetween(t, source, "function createUsageGraphRefreshJob(", "function scheduleUsageGraphWarmup(")
	applyJob := dashboardJavaScriptBetween(t, source, "function applyUsageGraphJob(", "async function runUsageGraphRefreshJob(")
	for _, expected := range []string{
		"const requestWindow = historyRequestWindow(range);",
		"windowStart: requestWindow.start",
		"windowEnd: requestWindow.end",
		"xBounds: Number.isFinite(min)",
	} {
		if !strings.Contains(createJob+applyJob, expected) {
			t.Fatalf("usage refresh and axis must share one exact request window: missing %q", expected)
		}
	}
}

func TestPerPeriodEventTotalsKeepVisibleBaseline(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	aggregate := dashboardJavaScriptBetween(t, source, "function aggregateDatasetForBuckets(", "function graphLineStyle(")
	script := fmt.Sprintf(`
const State = {
  graphMode: 'bucket',
  historyWindowStart: '2026-08-29T00:00:00.000Z',
  historyWindowEnd: '2026-08-29T01:00:00.000Z',
};
function normalizeGraphMode(value) { return value || 'bucket'; }
function getPollIntervalMs() { return 5 * 60 * 1000; }
%s
%s
const periods = aggregateDatasetForBuckets({
  data: [
    { x: new Date('2026-08-29T00:05:00.000Z'), y: 110 },
    { x: new Date('2026-08-29T00:10:00.000Z'), y: 130 },
  ],
  _barStrategy: 'delta',
  _deltaBaseline: 100,
}, 'custom', 'bucket');
const total = periods.reduce((sum, point) => sum + Number(point.y || 0), 0);
if (total !== 30) throw new Error('per-period total must include the first visible 10-unit delta');
`, buckets, aggregate)
	runDashboardNodeTest(t, script)
}

func TestCustomPlatformCostChartPinsSelectedXAxis(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	for _, expected := range []string{
		"const costTimeBounds = platformCostChartTimeBounds(range);",
		"const costPeriodBounds = periodMode && bucketed.cost.length > 0",
		"min: costTimeBounds?.min ?? costPeriodBounds?.min,",
		"max: costTimeBounds?.max ?? costPeriodBounds?.max,",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("platform cost chart must preserve exact selected bounds in both modes: missing %q", expected)
		}
	}
}

func TestGraphRefreshFailuresKeepDisplayedLabelsHonest(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	for _, expected := range []string{
		"function handleGraphRefreshFailure(job)",
		"restoreDisplayedUsageGraphSelection(job);",
		"restoreDisplayedCostGraphSelection(job);",
		"syncGraphHistoryRangeControl('chart');",
		"Showing saved ${shownLabel} data.",
		"setDashboardFreshness({ stale: true });",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("failed graph refresh must keep the last successful range visible: missing %q", expected)
		}
	}
}

func TestGraphControlsAndCanvasesExposeAccessibleState(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	template := dashboardTemplateSource(t)
	styles := dashboardStyleSource(t)
	for _, expected := range []string{
		`id="usage-chart" role="img"`,
		`id="usage-chart-accessible-summary"`,
		`id="platform-cost-chart" role="img"`,
		`id="platform-cost-chart-accessible-summary"`,
		`aria-pressed="true">Cumulative`,
	} {
		if !strings.Contains(template, expected) {
			t.Fatalf("graph canvas and controls need accessible state: missing %q", expected)
		}
	}
	for _, expected := range []string{
		"function renderUsageAccessibleSummary(",
		"function renderPlatformCostAccessibleSummary(",
		"button.setAttribute('aria-pressed', active ? 'true' : 'false');",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("graph accessibility state must update with rendered data: missing %q", expected)
		}
	}
	if !strings.Contains(styles, ".sr-only") {
		t.Fatal("accessible graph summaries must remain available to assistive technology")
	}
}

func TestCustomGraphTimeScaleUsesSelectedDuration(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	duration := dashboardJavaScriptBetween(t, source, "function graphRangeDurationMs(", "function graphBucketIntervalMs(")
	timeScale := dashboardJavaScriptBetween(t, source, "function updateTimeScale(", "// Cycles Table")
	script := fmt.Sprintf(`
const State = {
  graphMode: 'cumulative',
  historyWindowStart: '2026-04-30T00:00:00.000Z',
  historyWindowEnd: '2026-07-01T00:00:00.000Z',
};
function normalizeGraphMode(value) { return value || 'cumulative'; }
function isPeriodGraphMode() { return false; }
%s
%s
const chart = {
  data: { datasets: [{ data: [{ x: new Date('2026-05-17T23:59:19.707Z'), y: 2 }] }] },
  options: { scales: { x: {} } },
};
updateTimeScale(chart, 'custom');
if (chart.options.scales.x.time.unit !== 'day') {
  throw new Error('multi-day custom history must use a date axis');
}
`, duration, timeScale)

	runDashboardNodeTest(t, script)
}

func TestAllPeriodBucketsReachTheCurrentTime(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	script := fmt.Sprintf(`
const State = { graphMode: 'bucket', historyWindowStart: null, historyWindowEnd: null };
function normalizeGraphMode(value) { return value || 'bucket'; }
function getPollIntervalMs() { return 5 * 60 * 1000; }
const realDateNow = Date.now;
Date.now = () => Date.parse('2026-08-31T12:00:00.000Z');
%s
const result = graphBucketRange('all', [
  { x: new Date('2025-08-31T12:00:00.000Z'), y: 10 },
], 'bucket');
Date.now = realDateNow;
if (result.length === 0 || result.length > graphBucketMaxCount('bucket')) {
  throw new Error('All period buckets must remain bounded');
}
const finalPeriod = result[result.length - 1];
if (finalPeriod.periodStart.getTime() > Date.parse('2026-08-31T12:00:00.000Z')
    || finalPeriod.periodEnd.getTime() < Date.parse('2026-08-31T12:00:00.000Z')) {
  throw new Error('All period buckets must cover the current time even when the newest observation is stale');
}
`, buckets)
	runDashboardNodeTest(t, script)
}

func TestPeriodBucketsClampToExactSelectedWindow(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	script := fmt.Sprintf(`
const State = {
  graphMode: 'bucket',
  historyWindowStart: '2026-08-29T00:02:00.000Z',
  historyWindowEnd: '2026-08-29T00:58:00.000Z',
};
function normalizeGraphMode(value) { return value || 'bucket'; }
function getPollIntervalMs() { return 5 * 60 * 1000; }
%s
const start = Date.parse(State.historyWindowStart);
const end = Date.parse(State.historyWindowEnd);
const result = graphBucketRange('custom', [
  { x: new Date('2026-08-29T00:05:00.000Z'), y: 1 },
  { x: new Date('2026-08-29T00:55:00.000Z'), y: 2 },
], 'bucket');
if (result[0].periodStart.getTime() !== start
    || result[result.length - 1].periodEnd.getTime() !== end) {
  throw new Error('partial edge periods must use the exact selected boundaries');
}
if (result.some(period => period.x.getTime() < start || period.x.getTime() > end)) {
  throw new Error('period centers must stay inside the selected chart window');
}
const cost = graphBucketRange(
  'custom',
  [{ x: new Date('2026-08-29T00:55:00.000Z'), y: 2 }],
  'bucket',
  State.historyWindowStart,
  State.historyWindowEnd,
);
if (cost[0].periodStart.getTime() !== start || cost[cost.length - 1].periodEnd.getTime() !== end) {
  throw new Error('cost and usage periods must share exact edge boundaries');
}
`, buckets)
	runDashboardNodeTest(t, script)
}

func TestPeriodUsageGraphKeepsMissingCoverageUnknown(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	aggregate := dashboardJavaScriptBetween(t, source, "function aggregateDatasetForBuckets(", "function applyGraphModeToDatasets(")

	script := fmt.Sprintf(`
const State = {
  graphMode: 'bucket',
  historyWindowStart: '2026-08-29T00:00:00.000Z',
  historyWindowEnd: '2026-08-29T01:00:00.000Z',
};
function normalizeGraphMode(value) { return value || 'cumulative'; }
function getPollIntervalMs() { return 5 * 60 * 1000; }
%s
%s
const interval = graphBucketIntervalMs('custom', 'bucket');
if (interval < getPollIntervalMs()) {
  throw new Error('period buckets must not be shorter than the collection interval');
}
const result = aggregateDatasetForBuckets({
  label: 'Weekly All-Model',
  data: [
    { x: new Date('2026-08-29T00:00:00.000Z'), y: 10 },
    { x: new Date('2026-08-29T00:05:00.000Z'), y: 20 },
    { x: new Date('2026-08-29T00:55:00.000Z'), y: 30 },
  ],
}, 'custom', 'bucket');
const observedDelta = result.find(point => point.periodStart.getTime() === Date.parse('2026-08-29T00:05:00.000Z'));
if (!observedDelta || observedDelta.y !== 10) {
  throw new Error('continuous observed usage must remain attributed to its period');
}
const emptyPeriod = result.find(point => point.periodStart.getTime() === Date.parse('2026-08-29T00:10:00.000Z'));
if (!emptyPeriod || emptyPeriod.y !== null) {
  throw new Error('an unobserved usage period must stay unknown instead of becoming zero');
}
const firstAfterGap = result.find(point => point.periodStart.getTime() === Date.parse('2026-08-29T00:55:00.000Z'));
if (!firstAfterGap || firstAfterGap.y !== null) {
  throw new Error('usage accumulated across a collection gap must not be assigned to the first later period');
}
`, buckets, aggregate)

	runDashboardNodeTest(t, script)
}

func TestPeriodUsageGraphMarksObservedResets(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	buckets := dashboardJavaScriptBetween(t, source, "const graphBucketTargets =", "function formatPeriodTooltipTitle(")
	aggregate := dashboardJavaScriptBetween(t, source, "function aggregateDatasetForBuckets(", "function applyGraphModeToDatasets(")

	script := fmt.Sprintf(`
const State = {
  graphMode: 'bucket',
  historyWindowStart: '2026-08-29T00:00:00.000Z',
  historyWindowEnd: '2026-08-29T01:00:00.000Z',
};
function normalizeGraphMode(value) { return value || 'bucket'; }
function getPollIntervalMs() { return 5 * 60 * 1000; }
%s
%s
const result = aggregateDatasetForBuckets({
  label: 'Weekly All-Model',
  data: [
    { x: new Date('2026-08-29T00:00:00.000Z'), y: 50 },
    { x: new Date('2026-08-29T00:05:00.000Z'), y: 0 },
    { x: new Date('2026-08-29T00:10:00.000Z'), y: 5 },
  ],
}, 'custom', 'bucket');
const resetPeriod = result.find(point => point.periodStart.getTime() === Date.parse('2026-08-29T00:05:00.000Z'));
if (!resetPeriod || resetPeriod.y !== 0 || !resetPeriod.resetObserved) {
  throw new Error('an observed quota drop must remain visible as a reset marker');
}
const postReset = result.find(point => point.periodStart.getTime() === Date.parse('2026-08-29T00:10:00.000Z'));
if (!postReset || postReset.y !== 5) {
  throw new Error('post-reset usage must be attributed to its visible period');
}
`, buckets, aggregate)

	runDashboardNodeTest(t, script)
}

func TestCumulativeGraphLeavesCollectionGapsBlank(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	if strings.Contains(source, "spanGaps: true") {
		t.Fatal("line datasets must not reconnect across collection-gap markers")
	}
	gapProcessing := dashboardJavaScriptBetween(t, source, "function processDataWithGaps(", "function getSegmentStyle(")

	script := fmt.Sprintf(`
const document = {
  querySelector() { return { dataset: { pollInterval: '120' } }; },
};
function getPollIntervalMs() { return 2 * 60 * 1000; }
function graphBucketIntervalMs() { return 3 * 60 * 60 * 1000; }
%s
const beforeGap = { x: new Date('2026-08-27T17:01:53.540Z'), y: 0 };
const afterGap = { x: new Date('2026-08-29T20:25:45.041Z'), y: 35 };
for (const range of ['1h', '6h', '24h', '7d', '30d', 'all', 'custom']) {
  const rangeResult = processDataWithGaps([beforeGap, afterGap], range);
  if (rangeResult.data.length !== 3 || rangeResult.data[1].y !== null) {
    throw new Error(range + ' must leave the observed collection gap blank');
  }
}
const result = processDataWithGaps([beforeGap, afterGap], '7d');
if (result.data.length !== 3) {
  throw new Error('a collection gap must insert one non-observation break point');
}
if (result.data[0] !== beforeGap || result.data[2] !== afterGap) {
  throw new Error('observed points and timestamps must remain unchanged around a gap');
}
const breakPoint = result.data[1];
if (breakPoint.y !== null
    || breakPoint.x.getTime() <= beforeGap.x.getTime()
    || breakPoint.x.getTime() >= afterGap.x.getTime()) {
  throw new Error('the inserted point must break the line inside the unobserved interval');
}
if (result.pointRadii.join(',') !== '2,0,2') {
  throw new Error('the gap marker must stay invisible while observed samples stay visible');
}
const continuous = processDataWithGaps([
  beforeGap,
  { x: new Date(beforeGap.x.getTime() + (2 * 60 * 1000)), y: 1 },
], '1h');
if (continuous.data.length !== 2 || continuous.data.some(point => point.y === null)) {
  throw new Error('normal adjacent observations must remain connected');
}
const withinBucket = processDataWithGaps([
  beforeGap,
  { x: new Date(beforeGap.x.getTime() + (150 * 60 * 1000)), y: 1 },
], '7d');
if (withinBucket.data.length !== 2 || withinBucket.data.some(point => point.y === null)) {
  throw new Error('server sampling inside one display bucket must not create a false gap');
}
const missingBucket = processDataWithGaps([
  beforeGap,
  { x: new Date(beforeGap.x.getTime() + (3.2 * 60 * 60 * 1000)), y: 1 },
], '7d');
if (missingBucket.data.length !== 3 || missingBucket.data[1].y !== null) {
  throw new Error('a gap beyond the selected bucket width must remain blank');
}
`, gapProcessing)

	runDashboardNodeTest(t, script)
}

func TestPeriodGraphKeepsExactSelectedBounds(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	applyBounds := dashboardJavaScriptBetween(t, source, "function applyChartGraphMode(", "function setMainChartDatasets(")

	script := fmt.Sprintf(`
function normalizeGraphMode(value) { return value || 'cumulative'; }
function isPeriodGraphMode(value) { return normalizeGraphMode(value) === 'bucket'; }
function usageChartTimeBounds() { return null; }
%s
const firstStart = new Date('2026-08-29T00:00:00.000Z');
const lastEnd = new Date('2026-08-29T01:00:00.000Z');
const chart = {
  config: { type: 'line' },
  options: { scales: { x: {} } },
  data: { datasets: [{ data: [
    { x: new Date('2026-08-29T00:02:30.000Z'), y: 1, periodStart: firstStart, periodEnd: new Date('2026-08-29T00:05:00.000Z') },
    { x: new Date('2026-08-29T00:57:30.000Z'), y: 2, periodStart: new Date('2026-08-29T00:55:00.000Z'), periodEnd: lastEnd },
  ] }] },
};
applyChartGraphMode(chart, 'custom', 'bucket', {
  min: Date.parse('2026-08-29T00:02:00.000Z'),
  max: Date.parse('2026-08-29T00:58:00.000Z'),
});
	const selectedStart = Date.parse('2026-08-29T00:02:00.000Z');
	const selectedEnd = Date.parse('2026-08-29T00:58:00.000Z');
	if (new Date(chart.options.scales.x.min).getTime() !== selectedStart) {
  throw new Error('period graph must keep the exact selected start');
}
	if (new Date(chart.options.scales.x.max).getTime() !== selectedEnd) {
  throw new Error('period graph must keep the exact selected end');
}
`, applyBounds)

	runDashboardNodeTest(t, script)
}

func TestEmptyCostPeriodsDoNotCountAsUsage(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	hasUsage := dashboardJavaScriptBetween(t, source, "function pointSeriesHaveUsage(", "function renderPlatformCostChart(")

	script := fmt.Sprintf(`
%s
if (pointSeriesHaveUsage([{ y: 0 }], [{ y: 0 }])) {
  throw new Error('all-zero cost periods must remain an empty range');
}
if (!pointSeriesHaveUsage([{ y: 0.01 }], [{ y: 0 }])) {
  throw new Error('a positive cost period must count as usage');
}
`, hasUsage)

	runDashboardNodeTest(t, script)
}

func TestLargeChartAxisValuesUseCompactLabels(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	formatters := dashboardJavaScriptBetween(t, source, "function formatNumber(", "function formatCurrencyUSD(")

	script := fmt.Sprintf(`
%s
if (formatChartAxisNumber(250000000) !== '250M') {
  throw new Error('large chart ticks must use compact labels');
}
if (formatChartAxisNumber(2500) !== '2,500') {
  throw new Error('small chart ticks must keep their readable full value');
}
`, formatters)

	runDashboardNodeTest(t, script)
	if !strings.Contains(source, "callback: value => formatChartAxisNumber(Number(value || 0))") {
		t.Fatal("platform token cost axis must use compact labels")
	}
}

func TestCombinedHistoryRendersPrimaryBeforeOptionalCostHistory(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	refresh := dashboardJavaScriptBetween(t, source, "async function runUsageGraphRefreshJob(", "function createUsageGraphRefreshJob(")
	script := fmt.Sprintf(`
const API_BASE = '/api';
const State = { apiIntegrationsVisibility: { dashboard: true } };
const signal = { aborted: false };
const job = {
  provider: 'both',
  accountKey: '',
  selectionKey: 'chart:7d',
  range: '7d',
  query: 'range=7d',
};
const applications = [];
let resolveCostHistory;
const costHistoryResponse = new Promise(resolve => { resolveCostHistory = resolve; });
function response(data) {
  return {
    ok: true,
    headers: { get() { return null; } },
    async json() { return data; },
  };
}
let requestCount = 0;
async function authFetch() {
  requestCount += 1;
  if (requestCount === 1) return response({ primary: true });
  return costHistoryResponse;
}
function providerParamFor() { return 'provider=both'; }
function getStaleProviderHistory() { return { apiIntegrationsHistory: { cached: true } }; }
function setCachedProviderData() {}
function applyUsageGraphJob(_job, payload) { applications.push(payload); }
function buildProviderHistoryDatasets() { throw new Error('both view does not build a line chart'); }
%s
(async () => {
  const run = runUsageGraphRefreshJob(job, signal);
  await new Promise(resolve => setImmediate(resolve));
  if (applications.length !== 1 || !applications[0].apiIntegrationsHistory.cached) {
    throw new Error('primary provider history must render with cached cost history before its refresh resolves');
  }
  resolveCostHistory(response({ cost: true }));
  await run;
  if (applications.length !== 2 || !applications[1].apiIntegrationsHistory.cost) {
    throw new Error('cost history must enrich the combined view when it arrives');
  }
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
`, refresh)

	runDashboardNodeTest(t, script)
}

func TestChartRenderGuardBehavior(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	guard := dashboardJavaScriptBetween(t, source, "function updateChartWhenChanged(", "const API_INTEGRATIONS_CURRENT_CACHE_KEY")
	script := fmt.Sprintf(`
const State = { testSignature: null };
%s
let updates = 0;
const base = {
  provider: 'anthropic',
  range: '30d',
  mode: 'cumulative',
  data: [{ x: 1, y: 2 }],
  scale: 100,
  theme: 'dark',
  empty: false,
};
updateChartWhenChanged('testSignature', base, () => { updates += 1; });
updateChartWhenChanged('testSignature', JSON.parse(JSON.stringify(base)), () => { updates += 1; });
if (updates !== 1) throw new Error('identical render state must update once');
for (const [key, value] of [
  ['range', '7d'],
  ['mode', 'bucket'],
  ['data', [{ x: 1, y: 3 }]],
  ['scale', 200],
  ['theme', 'light'],
  ['empty', true],
]) {
  const changed = { ...base, [key]: value };
  updateChartWhenChanged('testSignature', changed, () => { updates += 1; });
  updateChartWhenChanged('testSignature', base, () => { updates += 1; });
}
if (updates !== 13) throw new Error('every material render-state change must update');
`, guard)
	runDashboardNodeTest(t, script)
}

func TestStaleQuotaStateStaysVisibleOnCards(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	freshness := dashboardJavaScriptBetween(t, source, "function freshnessLabel(", "// Anthropic display names")
	renderAll := dashboardJavaScriptBetween(t, source, "function renderProviderKPIHTML(", "// In-place update for a single KPI card")
	updateAnthropic := dashboardJavaScriptBetween(t, source, "function updateAnthropicCard(", "// Anthropic quota detail modal")
	updateCodex := dashboardJavaScriptBetween(t, source, "function updateCodexCard(", "function openCodexModal(")
	updateAll := dashboardJavaScriptBetween(t, source, "function updateProviderKPICard(", "function sortItemsByPreference(")

	script := fmt.Sprintf(`
const State = { currentQuotas: {} };
const statusConfig = { healthy: { icon: '' } };
const anthropicQuotaIcons = { seven_day: '' };
const quotaIcons = {};
function statusLabelFor() { return 'Healthy'; }
function minimaxSharedSubtitle() { return ''; }
function paceTargetLabelFor() { return ''; }
function formatDateTime(value) { return value; }
function formatDuration() { return '2h'; }
function formatNumber(value) { return String(value); }
function escapeHTML(value) { return String(value); }
function sanitizeProviderCardKey(value) { return String(value).replace(/[^a-z0-9_-]+/gi, '-'); }
function animateValue(element, _from, to, _duration, formatter) {
  element.textContent = formatter(to);
}
function makeClassList() {
  const values = new Set();
  return {
    toggle(name, enabled) {
      if (enabled) values.add(name);
      else values.delete(name);
    },
    contains(name) { return values.has(name); },
  };
}
function makeElement() {
  return {
    classList: makeClassList(),
    style: {},
    textContent: '',
    innerHTML: '',
    setAttribute() {},
  };
}
const elements = new Map();
function addElement(id, element = makeElement()) {
  elements.set(id, element);
  return element;
}
const anthropicProgress = addElement('progress-anth-seven_day');
anthropicProgress.parentElement = makeElement();
addElement('percent-anth-seven_day');
addElement('status-anth-seven_day');
addElement('reset-anth-seven_day');
addElement('countdown-anth-seven_day');
const anthropicCard = addElement('card-anth-seven_day');
const anthropicFreshness = addElement('freshness-anth-seven_day');

const codexProgress = addElement('progress-codex-seven_day');
codexProgress.parentElement = makeElement();
addElement('percent-codex-seven_day');
addElement('status-codex-seven_day');
addElement('reset-codex-seven_day');
addElement('countdown-codex-seven_day');
const codexCard = addElement('card-codex-seven_day');
const codexFreshness = addElement('freshness-codex-seven_day');

const allProgress = addElement('progress-kpiv-anthropic-seven_day');
allProgress.parentElement = makeElement();
addElement('percent-kpiv-anthropic-seven_day');
addElement('status-kpiv-anthropic-seven_day');
addElement('reset-kpiv-anthropic-seven_day');
addElement('countdown-kpiv-anthropic-seven_day');
addElement('pace-target-kpiv-anthropic-seven_day');
const allCard = addElement('card-kpiv-anthropic-seven_day');
const allFreshness = addElement('freshness-kpiv-anthropic-seven_day');

const document = {
  getElementById(id) { return elements.get(id) || null; },
};
%s
%s
%s
%s
%s
function assert(condition, message) {
  if (!condition) throw new Error(message);
}

const staleQuota = {
  name: 'seven_day',
  displayName: 'Weekly All-Model',
  utilization: 40,
  cardPercent: 40,
  status: 'healthy',
  source: 'api',
  ageSeconds: 7200,
  isStale: true,
  timeUntilResetSeconds: 7200,
  resetsAt: '2026-07-31T03:00:00Z',
};
const html = renderProviderKPIHTML([staleQuota], 'anthropic');
assert(html.includes('stale-card'), 'All dashboard card must render stale-card');
assert(html.includes('card-freshness stale'), 'All dashboard card must render stale freshness');
assert(html.includes('Stale data'), 'All dashboard card must label stale data explicitly');

updateAnthropicCard(staleQuota);
assert(anthropicCard.classList.contains('stale-card'), 'Anthropic card must become stale in place');
assert(anthropicFreshness.classList.contains('stale'), 'Anthropic freshness must become stale in place');
assert(anthropicFreshness.textContent.includes('Stale data'), 'Anthropic card must say stale data');

updateCodexCard(staleQuota);
assert(codexCard.classList.contains('stale-card'), 'Codex card must become stale in place');
assert(codexFreshness.classList.contains('stale'), 'Codex freshness must become stale in place');
assert(codexFreshness.textContent.includes('Stale data'), 'Codex card must say stale data');

updateProviderKPICard(staleQuota, 'anthropic');
assert(allCard.classList.contains('stale-card'), 'All dashboard card must become stale in place');
assert(allFreshness.classList.contains('stale'), 'All dashboard freshness must become stale in place');
assert(allFreshness.textContent.includes('Stale data'), 'All dashboard card must say stale data after refresh');

const freshQuota = { ...staleQuota, ageSeconds: 0, isStale: false };
updateAnthropicCard(freshQuota);
updateCodexCard(freshQuota);
updateProviderKPICard(freshQuota, 'anthropic');
assert(!anthropicCard.classList.contains('stale-card'), 'Anthropic card must clear stale state');
assert(!codexCard.classList.contains('stale-card'), 'Codex card must clear stale state');
assert(!allCard.classList.contains('stale-card'), 'All dashboard card must clear stale state');
assert(!anthropicFreshness.textContent.includes('Stale data'), 'Anthropic stale label must clear');
assert(!codexFreshness.textContent.includes('Stale data'), 'Codex stale label must clear');
assert(!allFreshness.textContent.includes('Stale data'), 'All dashboard stale label must clear');
`, freshness, renderAll, updateAnthropic, updateCodex, updateAll)

	runDashboardNodeTest(t, script)
}
