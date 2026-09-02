package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestCodexResetIntelligenceSeparatesPublicAccountAndServiceEvidence(t *testing.T) {
	eventAt := time.Date(2026, 8, 24, 0, 46, 51, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracker":
			_, _ = w.Write([]byte(`<div class="reset-today-card reset-today-card--possible"><span class="reset-badge reset-badge--possible">RESET TEASER</span><p class="reset-today-text">Maybe tomorrow.</p><span class="reset-today-ts">2026-08-27 06:31:31 UTC</span><a class="reset-today-link" href="https://x.com/example/teaser">source</a></div><span class="hero-figure" data-datetime="2026-08-24T00:46:51.000Z"></span><p class="hero-tweet">Reset propagated.</p><a class="hero-link" href="https://x.com/example/reset">source</a><footer>last_sync: Thu, 27 Aug 2026 14:00:23 GMT</footer>`))
		case "/page":
			_, _ = w.Write([]byte(`{"summary":{"structure":{"items":[{"group":{"name":"Codex","components":[{"component_id":"codex-cli","name":"CLI"}]}}]}}}`))
		case "/events":
			_, _ = w.Write([]byte(`{"incidents":[{"id":"incident-1","name":"Codex errors","status":"investigating","published_at":"2026-08-27T12:00:00Z","affected_components":[{"component_id":"codex-cli","current_status":"partial_outage"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()
	oldReset := eventAt.Add(3 * 24 * time.Hour)
	newReset := eventAt.Add(7 * 24 * time.Hour)
	for _, snapshot := range []*api.CodexSnapshot{
		{CapturedAt: eventAt.Add(-time.Minute), AccountID: 1, PlanType: "pro", Quotas: []api.CodexQuota{{Name: "seven_day", Utilization: 82, ResetsAt: &oldReset}}},
		{CapturedAt: eventAt.Add(20 * time.Minute), AccountID: 1, PlanType: "pro", Quotas: []api.CodexQuota{{Name: "seven_day", Utilization: 4, ResetsAt: &newReset}}},
	} {
		if _, err := st.InsertCodexSnapshot(snapshot); err != nil {
			t.Fatalf("InsertCodexSnapshot: %v", err)
		}
	}

	h := NewHandler(st, nil, nil, nil, nil)
	h.resetIntel.client = server.Client()
	h.resetIntel.trackerURL = server.URL + "/tracker"
	h.resetIntel.pageURL = server.URL + "/page"
	h.resetIntel.eventsURL = server.URL + "/events"
	rr := httptest.NewRecorder()
	h.CodexResetIntelligence(rr, httptest.NewRequest(http.MethodGet, "/api/codex/reset-intelligence?account=1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Global  resetTrackerIntel    `json:"global"`
		Account accountResetEvidence `json:"account"`
		Service serviceIntel         `json:"service"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Global.Signal != "possible" || !response.Global.LastResetAt.Equal(eventAt) {
		t.Fatalf("global = %+v", response.Global)
	}
	if response.Account.Status != "observed" || !response.Account.MeterDropped || !response.Account.WindowChanged {
		t.Fatalf("account = %+v", response.Account)
	}
	if response.Service.Status != "partial_outage" || response.Service.IncidentCount != 1 {
		t.Fatalf("service = %+v", response.Service)
	}
}

func TestSnapshotShowsResetRequiresMaterialEvidence(t *testing.T) {
	resetAt := time.Now().UTC()
	before := &api.CodexSnapshot{Quotas: []api.CodexQuota{{Name: "seven_day", Utilization: 40, ResetsAt: &resetAt}}}
	after := &api.CodexSnapshot{Quotas: []api.CodexQuota{{Name: "seven_day", Utilization: 35, ResetsAt: &resetAt}}}
	if snapshotShowsReset(before, after) {
		t.Fatal("small meter movement was classified as reset evidence")
	}
}
