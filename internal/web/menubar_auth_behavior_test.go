package web

import (
	"fmt"
	"testing"
)

func TestMenubarCapabilityCoversReadsAndBothSavePaths(t *testing.T) {
	data, err := staticFS.ReadFile("static/menubar.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	capability := dashboardJavaScriptBetween(t, source, "const capability =", "const MENUBAR_VERSION")
	reads := dashboardJavaScriptBetween(t, source, "async function loadPreferences()", "async function refreshData()")
	save := dashboardJavaScriptBetween(t, source, "async function savePreferences()", "function scheduleAutoSave(")
	auto := dashboardJavaScriptBetween(t, source, "async function autoSavePreferences()", "function openSettings()")
	runDashboardNodeTest(t, fmt.Sprintf(`
const assert=require('node:assert/strict');
const memory=new Map();const sessionStorage={getItem:k=>memory.get(k),setItem:(k,v)=>memory.set(k,v)};
let cleanURL='';const window={location:{hash:'#token=fixture-capability',pathname:'/menubar',search:''},history:{replaceState:(_,__,url)=>{cleanURL=url}}};
%s
const state={preferences:{providers_order:[],visible_providers:[],default_view:'detailed'},providerOptions:[],view:'standard',snapshot:null};
const requestedView='';const settingsNote={};const requests=[];
const normalizePreferences=v=>({...v,providers_order:v.providers_order||[],visible_providers:v.visible_providers||[]});
const normalizeView=v=>v;const mergeProviderOrder=v=>v;
const readStoredStatusDisplay=()=>null,readStoredProviderOrder=()=>[];
const writeStoredThemeMode=()=>{},writeStoredStatusDisplay=()=>{},writeStoredProviderOrder=()=>{},writeStoredViewMode=()=>{},applyTheme=()=>{},render=()=>{},renderSettings=()=>{},resetRefreshTimer=()=>{};
const fetch=async(url,options)=>{requests.push({url,...options});return {ok:true,json:async()=>({providers:[],providers_order:[],visible_providers:[],default_view:'detailed'})}};
%s
%s
%s
(async()=>{await loadPreferences();assert.equal(state.view,'detailed');await loadSnapshot();await savePreferences();state.draft={providers_order:[]};await autoSavePreferences();assert.equal(cleanURL,'/menubar');assert.equal(memory.get('onwatch.menubar.capability'),'fixture-capability');assert.equal(requests.filter(r=>r.method==='PUT').length,2);for(const r of requests)assert.equal(r.headers['X-Onwatch-Menubar'],'fixture-capability');assert.equal(state.saving,false);assert.equal(settingsNote.textContent,'saved')})().catch(e=>{console.error(e);process.exitCode=1});
`, capability, reads, save, auto))
}
