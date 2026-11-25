package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubscriptionNavigation_AllView(t *testing.T) {
	cfg := createTestConfigWithAll()
	cfg.BasePath = "/watch"
	h := NewHandler(nil, nil, nil, nil, cfg)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, httptest.NewRequest(http.MethodGet, "/?provider=both", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-provider="subscription-value" role="tab"`) {
		t.Error("missing Subscription Value tab")
	}
	if strings.Contains(body, `id="subscription-value"`) || strings.Contains(body, `id="subscription-navigation"`) {
		t.Error("All view must keep subscription content on its own tab")
	}
}

func TestSubscriptionNavigation_ProviderView(t *testing.T) {
	cfg := createTestConfigWithAll()
	h := NewHandler(nil, nil, nil, nil, cfg)
	for _, provider := range []string{"codex", "anthropic", "antigravity"} {
		rr := httptest.NewRecorder()
		h.Dashboard(rr, httptest.NewRequest(http.MethodGet, "/?provider="+provider, nil))
		body := rr.Body.String()
		if rr.Code != http.StatusOK || !strings.Contains(body, `data-provider="subscription-value" role="tab"`) || strings.Contains(body, `id="subscription-value"`) {
			t.Errorf("%s should link to the dedicated tab without rendering the comparison", provider)
		}
	}
	rr := httptest.NewRecorder()
	h.Dashboard(rr, httptest.NewRequest(http.MethodGet, "/?provider=subscription-value&view=codex", nil))
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, `id="subscription-value" data-provider="codex"`) {
		t.Fatalf("missing dedicated comparison, status %d", rr.Code)
	}
	for _, provider := range []string{"anthropic", "codex", "antigravity"} {
		if !strings.Contains(body, `data-subscription-provider="`+provider+`"`) {
			t.Errorf("missing comparison row for %s", provider)
		}
	}
	if strings.Contains(body, `id="device-health-section"`) || strings.Contains(body, `id="usage-chart"`) {
		t.Error("subscription tab rendered unrelated dashboard sections")
	}
}

func TestDashboardShowsPrimaryGraphsBeforeSecondaryStatusSections(t *testing.T) {
	t.Parallel()
	template := dashboardTemplateSource(t)

	orderedMarkers := []string{
		`id="platform-cost-section"`,
		`<section class="chart-section`,
		`id="platform-cost-breakdown-section"`,
		`id="device-health-section"`,
	}
	previous := -1
	for _, marker := range orderedMarkers {
		index := strings.Index(template, marker)
		if index < 0 {
			t.Fatalf("dashboard template missing %q", marker)
		}
		if index <= previous {
			t.Fatalf("dashboard template section %q is out of priority order", marker)
		}
		previous = index
	}
}
