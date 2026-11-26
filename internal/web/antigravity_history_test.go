package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestAntigravityDashboardAccountsKeepCentralReadingsSeparate(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	device, _, err := s.CreateDevice("quota-fixture", "windows")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i, usage := range []float64{25, 75} {
		id := fmt.Sprintf("account-%d", i)
		if err := s.SetPollOwner("antigravity", id, "device", device.ID); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(ingest.QuotaSnapshot{Version: 1, Plan: "Pro", Metrics: []ingest.QuotaMetric{{
			Name: "weekly", Group: api.AntigravityQuotaGroupGeminiPro, Window: "weekly", Value: usage, Unit: "percent",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		event := ingest.Event{EventID: fmt.Sprintf("evt_account_view_%d", i), Kind: "quota_snapshot", CapturedAt: now.Add(-time.Minute),
			Provider: "antigravity", Account: ingest.Account{ExternalID: id}, Payload: body}
		results, err := s.StoreIngestBatch(device, []ingest.Event{event}, now)
		if err != nil || len(results) != 1 || results[0].Status != "accepted" {
			t.Fatalf("ingest %s: %v, %v", id, results, err)
		}
	}
	h := NewHandler(s, nil, nil, nil, createTestConfigWithAntigravity())
	accounts := h.dashboardAccounts("antigravity", nil)
	if len(accounts) != 2 || accounts[0].ID == accounts[1].ID {
		t.Fatalf("expected two independent accounts, got %v", accounts)
	}
	pair := httptest.NewRecorder()
	h.Dashboard(pair, httptest.NewRequest(http.MethodGet, "/?provider=antigravity&account="+accounts[0].ID+"&account="+accounts[1].ID, nil))
	if pair.Code != 200 || strings.Count(pair.Body.String(), "data-account-plot") != 2 {
		t.Fatalf("wanted two account panels, got HTTP %d", pair.Code)
	}
	for i, account := range accounts {
		want := float64(25 + 50*i)
		rr := httptest.NewRecorder()
		h.History(rr, httptest.NewRequest(http.MethodGet, "/api/history?provider=antigravity&range=24h&account="+account.ID, nil))
		var history struct {
			Datasets []struct {
				Data []float64
			}
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &history); err != nil || len(history.Datasets) != 1 ||
			len(history.Datasets[0].Data) != 1 || history.Datasets[0].Data[0] != want {
			t.Fatalf("account history is mixed or unavailable: %s, %v", rr.Body.String(), err)
		}
	}
}

func TestAntigravityHistoryUsesSeparateSummaryWindows(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		buckets := []api.AntigravityQuotaSummaryBucket{
			{BucketID: "short", Window: api.AntigravityWindowFiveHour, RemainingFraction: 0.8},
		}
		if i != 1 {
			buckets = append(buckets, api.AntigravityQuotaSummaryBucket{BucketID: "week", Window: api.AntigravityWindowWeekly, RemainingFraction: 0.2})
		}
		_, err := s.InsertAntigravitySnapshot(&api.AntigravitySnapshot{
			CapturedAt:    now.Add(time.Duration(i-3) * time.Minute),
			Models:        []api.AntigravityModelQuota{{ModelID: "gemini-pro", RemainingFraction: 0.01}},
			SummaryGroups: []api.AntigravityQuotaSummaryGroup{{GroupKey: api.AntigravityQuotaGroupGeminiPro, DisplayName: "Gemini models", Buckets: buckets}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(s, nil, nil, nil, createTestConfigWithAntigravity())
	rr := httptest.NewRecorder()
	h.History(rr, httptest.NewRequest(http.MethodGet, "/api/history?provider=antigravity&range=24h", nil))
	var result struct {
		Labels   []string `json:"labels"`
		Datasets []struct {
			Window string     `json:"windowKind"`
			Data   []*float64 `json:"data"`
		} `json:"datasets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Datasets) != 2 || len(result.Labels) != 3 {
		t.Fatalf("history: %s", rr.Body.String())
	}
	for _, ds := range result.Datasets {
		want := 20.0
		if ds.Window == api.AntigravityWindowWeekly {
			want = 80
			if ds.Data[1] != nil {
				t.Fatal("missing weekly observation must remain null")
			}
		}
		if ds.Data[0] == nil || *ds.Data[0] < want-0.001 || *ds.Data[0] > want+0.001 {
			t.Fatalf("wrong window values: %s", rr.Body.String())
		}
	}
}

func TestAntigravityCurrentExposesSnapshotFreshness(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := NewHandler(s, nil, nil, nil, createTestConfigWithAntigravity())
	for _, age := range []time.Duration{24 * time.Hour, time.Minute} {
		at := time.Now().UTC().Add(-age).Truncate(time.Second)
		_, err := s.InsertAntigravitySnapshot(&api.AntigravitySnapshot{CapturedAt: at, SummaryGroups: []api.AntigravityQuotaSummaryGroup{{GroupKey: api.AntigravityQuotaGroupGeminiPro, DisplayName: "Gemini models", Buckets: []api.AntigravityQuotaSummaryBucket{{BucketID: "short", Window: api.AntigravityWindowFiveHour, RemainingFraction: 0.8}}}}})
		if err != nil {
			t.Fatal(err)
		}
		result := h.buildAntigravityCurrent()
		q := result["quotas"].([]map[string]interface{})[0]
		if q["isStale"] != (age > 30*time.Minute) || q["lastUpdatedAt"] != at.Format(time.RFC3339) {
			t.Fatalf("incorrect snapshot freshness: %+v", q)
		}
	}
}

func TestAntigravityDashboardIncludesCostAndWindowControls(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := NewHandler(s, nil, nil, nil, createTestConfigWithAntigravity())
	rr := httptest.NewRecorder()
	h.Dashboard(rr, httptest.NewRequest(http.MethodGet, "/?provider=antigravity", nil))
	for _, id := range []string{"platform-cost-section", "platform-cost-chart", "platform-cost-breakdown-section", "antigravity-window-select"} {
		if !strings.Contains(rr.Body.String(), `id="`+id+`"`) {
			t.Errorf("missing %s", id)
		}
	}
}

func TestAntigravityWindowSelectionPreservesOtherWindows(t *testing.T) {
	source := dashboardJavaScriptBetween(t, dashboardAppSource(t), "function antigravityWindowDatasets(", "function renderAntigravityUsageSummary(")
	runDashboardNodeTest(t, `
const State = {};
const buttons = ['weekly', 'five_hour', 'unknown'].map(quotaWindow => ({dataset:{quotaWindow},classList:{toggle(){}},setAttribute(){}}));
const document = {querySelectorAll:()=>buttons};
const selectedChartRange=()=> '7d';
let rendered;
const setMainChartDatasets=(data)=>{rendered=antigravityWindowDatasets(data);};
function assert(ok,message){if(!ok)throw new Error(message);}
`+source+`
antigravityWindowDatasets([]);
assert(!State.antigravityQuotaWindow,'empty loading response must not select a window');
const all=[{_antigravityWindow:'weekly'},{_antigravityWindow:'five_hour'}];
assert(antigravityWindowDatasets(all)[0]===all[0],'weekly default');
buttons[1].onclick();
assert(rendered[0]===all[1],'five-hour selection');
buttons[0].onclick();
assert(rendered[0]===all[0],'weekly preserved after switching');
delete State.antigravityQuotaWindow;
assert(antigravityWindowDatasets([{_antigravityWindow:'unknown'}]).length===1,'legacy unknown window visible');
`)
}

func TestAntigravityCentralGroupLabel(t *testing.T) {
	quota := antigravityGroupedQuotaToMap(api.AntigravityGroupedQuota{GroupKey: api.AntigravityQuotaGroupClaudeGPT, DisplayName: api.AntigravityQuotaGroupClaudeGPT}, "usage")
	if quota["displayName"] != "Claude + GPT Quota" {
		t.Fatalf("internal group ID leaked into title: %v", quota["displayName"])
	}
}
