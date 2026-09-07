package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

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
