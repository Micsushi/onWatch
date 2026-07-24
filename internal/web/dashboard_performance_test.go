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
	return string(data)
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
