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
	for _, provider := range []string{"codex", "anthropic", "antigravity"} {
		link := `href="/watch/?provider=` + provider + `#subscription-value"`
		if !strings.Contains(body, link) {
			t.Errorf("missing comparison link for %s", provider)
		}
	}
	if strings.Contains(body, `id="subscription-value"`) {
		t.Error("all view must not initialize a comparison with unsupported provider both")
	}
}

func TestSubscriptionNavigation_ProviderView(t *testing.T) {
	cfg := createTestConfigWithAll()
	h := NewHandler(nil, nil, nil, nil, cfg)
	for _, provider := range []string{"codex", "anthropic", "antigravity"} {
		rr := httptest.NewRecorder()
		h.Dashboard(rr, httptest.NewRequest(http.MethodGet, "/?provider="+provider, nil))
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `id="subscription-value" data-provider="`+provider+`"`) {
			t.Errorf("missing comparison for %s", provider)
		}
	}
}
