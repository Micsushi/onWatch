package web

import (
	"os"
	"strings"
	"testing"
)

func dashboardAppSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read dashboard app: %v", err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func dashboardTemplateSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("templates/dashboard.html")
	if err != nil {
		t.Fatalf("read dashboard template: %v", err)
	}
	return string(data)
}

func dashboardStyleSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("read dashboard styles: %v", err)
	}
	return string(data)
}

func dashboardHandlerSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read dashboard handlers: %v", err)
	}
	return string(data)
}

func TestCombinedHistorySamplesExpensiveQuotaProvidersInSQL(t *testing.T) {
	t.Parallel()
	source := dashboardHandlerSource(t)

	for _, marker := range []string{
		"h.store.QueryAnthropicRangeSampled(start, now, maxChartPoints)",
		"h.store.QueryCodexRangeSampled(accountID, start, now, maxChartPoints)",
		"h.store.QueryGeminiRangeSampled(start, now, maxChartPoints)",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("combined history still loads every snapshot before downsampling: missing %q", marker)
		}
	}
}

func TestDashboardDoesNotWarmEveryProviderAndRange(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, forbidden := range []string{
		"PROVIDER_HISTORY_WARMUP_RANGES",
		"prefetchProviderAllRanges",
		"startProviderDataWarmup",
		"startProviderDataRollingRefresh",
		"PLATFORM_COST_PREFETCH_RANGES",
		"prefetchPlatformCostRanges",
		"startPlatformCostRefreshLoop",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("dashboard still contains eager warmup path %q", forbidden)
		}
	}
}

func TestDashboardCombinedHistoryRendersBeforeSecondaryHistory(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	renderMarker := "State.allProvidersHistory = data;\n      renderAllProvidersView();"
	secondaryMarker := "scheduleAPIIntegrationsHistoryRefresh(range, requestSeq);"

	renderAt := strings.Index(source, renderMarker)
	if renderAt < 0 {
		t.Fatalf("combined history immediate render path not found")
	}
	secondaryAt := strings.Index(source[renderAt:], secondaryMarker)
	if secondaryAt < 0 {
		t.Fatalf("secondary API Integrations refresh is not scheduled after combined render")
	}
}

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

func TestDashboardPersistsLastSuccessfulGraphsAcrossPageLoads(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"readJSONStorage(localStorage, PROVIDER_DATA_CACHE_KEY)",
		"writeJSONStorage(localStorage, PROVIDER_DATA_CACHE_KEY, cache)",
		"readJSONStorage(localStorage, PLATFORM_COST_HISTORY_CACHE_KEY)",
		"writeJSONStorage(localStorage, PLATFORM_COST_HISTORY_CACHE_KEY, cache)",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("dashboard cache is not page-load persistent: missing %q", marker)
		}
	}
}

func TestDashboardPresetHistoryCacheKeysRemainStableAcrossReloads(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	start := strings.Index(source, "function historySelectionKey(range, scope = 'chart')")
	end := strings.Index(source[start:], "\nfunction historyRequestQuery")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate historySelectionKey")
	}
	cacheKeyLogic := source[start : start+end]
	for _, marker := range []string{
		"if (normalized !== 'custom') return `${scope}:${normalized}`;",
		"`${scope}:${normalized}:${windowState.start || ''}:${windowState.end || ''}`",
	} {
		if !strings.Contains(cacheKeyLogic, marker) {
			t.Errorf("history cache key is not stable for presets while exact for Custom: missing %q", marker)
		}
	}
}

func TestDashboardCanReuseLegacyPresetSnapshotDuringRefresh(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function getStaleProviderHistory(provider, range, selectionKey, accountOverride)",
		"const cached = getStaleProviderHistory(requestProvider, range, requestRangeKey);",
		"setDashboardFreshness({ stale: Boolean(options.force || !freshCached), ts: cached.ts });",
		"pruneLegacyPresetHistoryEntries(cache);",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("saved preset graph is not reliably reused during refresh: missing %q", marker)
		}
	}
}

func TestPlatformCostRequestsAreSharedAndWarmLikelyRangesInBackground(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"platformCostPayloadInflight: {}",
		"const existing = State.platformCostPayloadInflight[requestKey];",
		"if (existing) return existing;",
		"function schedulePlatformCostWarmup(provider, activeRange)",
		"const preferredRanges = ['30d', '24h', '6h', '1h', '7d', 'all'];",
		"enqueueGraphRefresh(job, { foreground: false });",
		"function platformCostHistoryRequestQuery(range, scope = 'platformCost')",
		"const windowRange = presetHistoryWindow(normalized, new Date());",
		"const query = platformCostHistoryRequestQuery(range, scope);",
		"setCachedPlatformCostHistory(job.provider, job.range, payload, job.scope);",
		"setCachedPlatformCostHistory(job.provider, job.range, payload, alternateScope);",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("cost graph does not share or resource-cap background work: missing %q", marker)
		}
	}
}

func TestDashboardShowsVisibleBackgroundRefreshStatus(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	template := dashboardTemplateSource(t)

	for _, marker := range []string{
		`id="dashboard-refresh-status"`,
		`aria-live="polite"`,
	} {
		if !strings.Contains(template, marker) {
			t.Errorf("dashboard refresh status missing template marker %q", marker)
		}
	}
	for _, marker := range []string{
		"function setDashboardRefreshState(",
		"setDashboardRefreshState(true",
		"setDashboardRefreshState(false",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("dashboard refresh status missing behavior marker %q", marker)
		}
	}
}

func TestDashboardCurrentCardsDoNotWaitForCostAggregation(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	start := strings.Index(source, "async function fetchCurrent(options = {})")
	end := strings.Index(source[start:], "\nasync function fetchCodexUsage")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate fetchCurrent")
	}
	fetchCurrent := source[start : start+end]
	applyAt := strings.Index(fetchCurrent, "applyProviderCurrentPayload(")
	waitAt := strings.Index(fetchCurrent, "await apiIntegrationsCurrentPromise")
	if applyAt < 0 || waitAt < 0 {
		t.Fatalf("fetchCurrent is missing progressive current-data markers: apply=%d wait=%d", applyAt, waitAt)
	}
	if applyAt > waitAt {
		t.Fatal("provider cards still wait for optional API integration cost aggregation")
	}
}

func TestDashboardHasIndependentGraphCustomDateRangeControls(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	template := dashboardTemplateSource(t)

	for _, marker := range []string{
		`data-history-scope="chart"`,
		`data-history-scope="platformCost"`,
		`data-history-scope="platformCostBreakdown"`,
		`id="chart-history-start-date"`,
		`id="chart-history-end-date"`,
		`id="platform-cost-history-start-date"`,
		`id="platform-cost-history-end-date"`,
		`id="platform-cost-breakdown-history-start-date"`,
		`id="platform-cost-breakdown-history-end-date"`,
		`data-range="custom">Custom`,
	} {
		if !strings.Contains(template, marker) {
			t.Errorf("graph-local custom range is missing template marker %q", marker)
		}
	}
	if strings.Contains(template, `id="history-range-toolbar"`) {
		t.Fatal("date controls must not render as a page-level toolbar")
	}
	if strings.Count(template, `data-range="custom">Custom`) != 3 {
		t.Fatal("each of the two graphs and the cost breakdown must have its own Custom option")
	}
	for _, marker := range []string{
		"function setupGraphHistoryRangeControls(",
		"function historyRequestQuery(range, scope = 'chart')",
		"fetchPlatformCostPayload(job.provider, job.range, signal, job.scope)",
		"createPlatformCostRefreshJob(range, provider, 'platformCost')",
		"createPlatformCostRefreshJob(range, provider, 'platformCostBreakdown')",
		"State.platformCostRange = 'custom'",
		"State.platformCostBreakdownRange = 'custom'",
		"X-OnWatch-Archived-Data",
		"Older data is shown at hourly resolution because detailed records are kept for 30 days.",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("independent graph ranges are missing behavior marker %q", marker)
		}
	}
}

func TestGraphRangeLoadingCompletionKeepsLatestSelectionActive(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function setPlatformCostRangeControlsLoading(range, loading) {\n  const selectedRange = selectedPlatformCostRange();",
		"function setPlatformCostBreakdownRangeControlsLoading(range, loading) {\n  const selectedRange = selectedPlatformCostBreakdownRange();",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("late range loading can overwrite the latest graph selection; missing %q", marker)
		}
	}
}

func TestCachedArchivedGraphRangeStillShowsPrecisionWarning(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function noteArchivedHistorySelection(range, scope = 'chart')",
		"if (cached.usesArchivedData) noteArchivedHistorySelection(range, 'platformCost');",
		"if (cached.usesArchivedData) noteArchivedHistorySelection(range, 'platformCostBreakdown');",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("cached archived history can hide the precision warning; missing %q", marker)
		}
	}
}

func TestThirtyDayPresetDoesNotShowArchivePrecisionWarning(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	start := strings.Index(source, "function shouldShowArchivedHistoryNotice(range, scope = 'chart')")
	end := strings.Index(source[start:], "\nfunction noteArchivedHistoryResponse")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate archived history selection warning")
	}
	warningLogic := source[start : start+end]
	if !strings.Contains(warningLogic, "if (normalized !== 'custom') return false;") {
		t.Fatal("30d preset can still show the archive precision warning")
	}
}

func TestArchivePrecisionWarningOnlyFollowsSelectedAllOrOldCustomRange(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function shouldShowArchivedHistoryNotice(range, scope = 'chart')",
		"if (normalized === 'all') return true;",
		"if (normalized !== 'custom') return false;",
		"if (!shouldShowArchivedHistoryNotice(range, scope)) return;",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("archive precision warning can appear for an exact preset: missing %q", marker)
		}
	}

	payloadStart := strings.Index(source, "async function fetchPlatformCostPayload(")
	payloadEnd := strings.Index(source[payloadStart:], "\nfunction schedulePlatformCostWarmup")
	if payloadStart < 0 || payloadEnd < 0 {
		t.Fatal("could not isolate platform cost payload fetch")
	}
	payloadLogic := source[payloadStart : payloadStart+payloadEnd]
	if strings.Contains(payloadLogic, "noteArchivedHistoryResponse(") {
		t.Fatal("background cost warmup can display a toast for a range that is not selected")
	}
}

func TestDashboardGraphRefreshQueueFinishesActiveWorkAndPromotesLatestSelection(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"graphRefreshQueue: []",
		"graphRefreshActive: null",
		"function enqueueGraphRefresh(job, options = {})",
		"State.graphRefreshQueue.unshift(job);",
		"State.graphRefreshQueue.push(job);",
		"await job.run(job.controller.signal);",
		"State.graphRefreshActive = null;",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("graph refresh queue is missing non-preemptive priority behavior: %q", marker)
		}
	}
}

func TestDashboardPresetGraphsUseSavedFallbackAfterAtMostOneSecond(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"const GRAPH_PRESET_FALLBACK_DELAY_MS = 1000;",
		"function schedulePresetGraphFallback(job)",
		"if (job.range === 'custom') return;",
		"window.setTimeout(() =>",
		"showBestSavedGraphFallback(job)",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("preset graph fallback timing is missing: %q", marker)
		}
	}
}

func TestDashboardCustomGraphsUseASeparateAbortableFastLane(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"customGraphRefreshControllers: {}",
		"function runCustomGraphRefresh(job)",
		"previousController.abort();",
		"job.controller = new AbortController();",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("Custom graph fast lane is missing: %q", marker)
		}
	}
}

func TestDashboardRefreshHeaderDescribesSelectedGraphQueueState(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function graphRefreshStatusMessage(job)",
		"`Finishing ${active.label} before refreshing ${job.label}.`",
		"`Refreshing data for ${job.label} graph.`",
		"renderDashboardRefreshStatus()",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("selected graph refresh status is missing: %q", marker)
		}
	}
}

func TestDashboardPresetRefreshNeverDimsAnExistingGraph(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	start := strings.Index(source, "async function fetchPlatformCostHistory(range, provider")
	end := strings.Index(source[start:], "\nfunction renderAPIIntegrationsHealth")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate platform cost history refresh")
	}
	refreshLogic := source[start : start+end]
	if strings.Contains(refreshLogic, "setPlatformCostChartLoading(\n        true") {
		t.Fatal("preset cost refresh still paints the full-chart loading overlay")
	}
	if !strings.Contains(refreshLogic, "range === 'custom'") {
		t.Fatal("cost refresh does not isolate Custom loading behavior")
	}
}

func TestDashboardHistoryRangeChangesSwapChartsWithoutAnimation(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	usageStart := strings.Index(source, "function setMainChartDatasets(")
	usageEnd := strings.Index(source[usageStart:], "\nfunction initChart")
	if usageStart < 0 || usageEnd < 0 {
		t.Fatal("could not isolate main usage chart update")
	}
	usageUpdate := source[usageStart : usageStart+usageEnd]
	if !strings.Contains(usageUpdate, "State.chart.update('none');") {
		t.Fatal("main usage range changes still animate between old and new datasets")
	}
	if strings.Contains(usageUpdate, "State.chart.update();") {
		t.Fatal("main usage range update still contains a default animated update")
	}

	costStart := strings.Index(source, "function renderPlatformCostChart(")
	costEnd := strings.Index(source[costStart:], "\nfunction bindPlatformCostRangeControls")
	if costStart < 0 || costEnd < 0 {
		t.Fatal("could not isolate platform cost chart update")
	}
	costUpdate := source[costStart : costStart+costEnd]
	if !strings.Contains(costUpdate, "animation: false,") {
		t.Fatal("platform cost chart does not disable range-transition animation")
	}
	if !strings.Contains(costUpdate, "State.platformCostChart.update('none');") {
		t.Fatal("platform cost range changes still animate between old and new datasets")
	}
	if strings.Contains(costUpdate, "State.platformCostChart.update();") {
		t.Fatal("platform cost range update still contains a default animated update")
	}
}

func TestDashboardUsageChartKeepsSelectedPresetOnTimeAxis(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)

	for _, marker := range []string{
		"function usageChartTimeBounds(range)",
		"const windowState = historyScopeWindow('chart');",
		"chart.options.scales.x.min = bounds.min;",
		"chart.options.scales.x.max = bounds.max;",
		"const xBounds = usageChartTimeBounds(range);",
		"xBounds,",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("usage chart can auto-fit a preset to only the available samples: missing %q", marker)
		}
	}
}

func TestDashboardRefreshStatusDoesNotResizeTheStickyHeader(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	styles := dashboardStyleSource(t)

	if !strings.Contains(source, "headerActions.classList.toggle('is-refreshing', active);") {
		t.Fatal("refresh status does not expose stable header layout state")
	}
	for _, marker := range []string{
		".header-actions.is-refreshing .header-meta",
		".dashboard-refresh-message",
		"text-overflow: ellipsis;",
	} {
		if !strings.Contains(styles, marker) {
			t.Errorf("refresh status can still wrap and resize the header: missing %q", marker)
		}
	}
}

func TestDashboardPollHealthAlertsToastOnceAndRefreshQuickly(t *testing.T) {
	t.Parallel()
	source := dashboardAppSource(t)
	handlerSource := dashboardHandlerSource(t)

	for _, marker := range []string{
		"const POLL_HEALTH_SEEN_ALERTS_KEY = 'onwatch-seen-poll-health-alerts-v1';",
		"const POLL_HEALTH_SEEN_ALERT_LIMIT = 100;",
		"const POLL_HEALTH_TOAST_QUEUE_LIMIT = 10;",
		"const POLL_HEALTH_TOAST_DURATION_MS = 6500;",
		"const DEFERRED_DASHBOARD_TOAST_QUEUE_LIMIT = 10;",
		"let _seenPollHealthAlertIDs = null;",
		"const _queuedPollHealthAlertIDs = new Set();",
		"const _pollHealthToastQueue = [];",
		"const _deferredDashboardToastQueue = [];",
		"function deferDashboardToast(message, type, timeoutMs)",
		"function drainDeferredDashboardToastQueue()",
		"function rememberDisplayedPollHealthAlertID(alertID)",
		"writeJSONStorage(localStorage, POLL_HEALTH_SEEN_ALERTS_KEY, [...seenIDs]);",
		"function drainPollHealthToastQueue()",
		"rememberDisplayedPollHealthAlertID(alertID);",
		"window.setTimeout(() =>",
		"[...alerts].reverse().forEach(alert =>",
		"['poll_failure', 'poll_recovered'].includes(alert.type)",
		"seenIDs.has(alertID) || _queuedPollHealthAlertIDs.has(alertID)",
		"outstandingToasts >= POLL_HEALTH_TOAST_QUEUE_LIMIT",
		"_pollHealthToastQueue.push(alert);",
		"alert.type === 'poll_recovered' ? 'success'",
		"alert.severity === 'error' ? 'error' : 'warning'",
		"renderDashboardToast(message, toastType, POLL_HEALTH_TOAST_DURATION_MS);",
		"if (deferDashboardToast(message, type, timeoutMs)) return;",
		"showNewPollHealthAlertToasts(alerts);",
		"setInterval(updateNotificationCenter, 10000);",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("poll-health toast behavior missing %q", marker)
		}
	}

	if strings.Contains(source, "setInterval(updateNotificationCenter, 60000);") {
		t.Error("notification center still waits sixty seconds between poll-health checks")
	}

	for _, marker := range []string{
		"_notificationAlerts = alerts;",
		"list.innerHTML = alerts.map(renderNotificationItem).join('');",
		"await dismissAlert(id);",
	} {
		if !strings.Contains(source, marker) {
			t.Errorf("notification-center behavior changed while adding poll-health toasts: missing %q", marker)
		}
	}

	if !strings.Contains(handlerSource, `"type":      alert.AlertType,`) {
		t.Fatal("dashboard test no longer matches the /api/alerts response field for alert type")
	}
	if strings.Contains(source, "alert.alert_type") {
		t.Fatal("dashboard reads the store field name instead of the /api/alerts type field")
	}
}
