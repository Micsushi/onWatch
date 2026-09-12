package web

import (
	"crypto/sha256"
	"fmt"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubscriptionScriptInvalidatesOldImmutableCache(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, createTestConfigWithSynthetic())
	rec := httptest.NewRecorder()
	h.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, name := range []string{"subscription.js", "theme-init.js"} {
		data, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.Body.String(), fmt.Sprintf("h=%x", sha256.Sum256(data))) {
			t.Fatalf("missing content hash for %s", name)
		}
		r := httptest.NewRecorder()
		contentTypeHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/static/"+name, nil))
		if r.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s can remain stale", name)
		}
	}
}

func TestInvalidSubscriptionRequestDoesNotSavePlan(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())
	req := httptest.NewRequest(http.MethodPut, "/api/subscription-value?provider=codex&usage_account="+strings.Repeat("x", 257), strings.NewReader(`{"name":"Changed","monthlyUsd":500,"multiplier":20}`))
	rec := httptest.NewRecorder()
	h.SubscriptionValue(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status: %d", rec.Code)
	}
	saved, _ := s.GetSetting("subscription_profile_codex_1")
	if saved != "" {
		t.Fatal("rejected request changed the saved plan")
	}
}

func TestSubscriptionRefreshPreservesDraftAndChart(t *testing.T) {
	runDashboardNodeTest(t, `
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const nodes=new Map();
function node(id){if(!nodes.has(id))nodes.set(id,{value:'',innerHTML:'',textContent:'',dataset:{provider:'codex'},listeners:{},setAttribute(){},contains(){return false},querySelector(){return node('submit')},querySelectorAll(){return []},addEventListener(k,f){this.listeners[k]=f}});return nodes.get(id)}
let charts=0,destroyed=0,pending=[];
node('subscription-days').value='30';
const cycle={start:'2026-09-01',end:'2026-09-02',quota:'weekly',complete:true,startUsed:0,endUsed:100,cost:10,per100:10,x1:1,points:[{used:0,cost:0},{used:100,cost:10}]};
const payload={start:cycle.start,end:cycle.end,basis:'test',report:{requests:1,unknownRequests:0,cost:10,valueMultiple:1,cycles:[cycle],models:[],devices:[],warnings:[],profile:{name:'Saved',monthlyUsd:0,multiplier:1}}};
const ctx={document:{getElementById:node,documentElement:{getAttribute(){return 'dark'}},activeElement:null},location:{search:''},escapeHTML:String,Intl,URLSearchParams,AbortController,API_BASE:'',MutationObserver:class{observe(){}},getComputedStyle(){return {getPropertyValue(){return '#fff'}}},setInterval(){},Chart:class{constructor(){charts++}destroy(){destroyed++}},authFetch:async()=>({ok:true,json:()=>new Promise(resolve=>pending.push(resolve))})};
vm.runInNewContext(fs.readFileSync('static/subscription.js','utf8'),ctx);
const flush=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
 await flush();pending.shift()(payload);await flush();
 assert.equal(charts,1);assert.equal(node('subscription-price').value,0);
 node('subscription-plan-name').value='Unsaved';node('subscription-plan-form').listeners.input();
 node('subscription-refresh').listeners.click();await flush();pending.shift()(payload);await flush();
 assert.equal(node('subscription-plan-name').value,'Unsaved');assert.equal(charts,1);assert.equal(destroyed,0);
 node('subscription-refresh').listeners.click();await flush();const old=pending.shift();
 node('subscription-days').value='7';node('subscription-days').listeners.change();await flush();
 pending.shift()({...payload,report:{...payload.report,cost:77}});await flush();
 old({...payload,report:{...payload.report,cost:999}});await flush();
 assert(node('subscription-summary').innerHTML.includes('77.00'));assert(!node('subscription-summary').innerHTML.includes('999.00'));
})().catch(e=>{console.error(e);process.exitCode=1});
`)
}
