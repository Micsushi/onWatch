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
`, apply)
	runDashboardNodeTest(t, script)
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
