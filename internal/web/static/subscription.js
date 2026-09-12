/* Subscription comparisons use their own range; quota and cost charts keep theirs. */
(() => {
  'use strict';
  const root = document.getElementById('subscription-value');
  if (!root) return;
  const el = id => document.getElementById('subscription-' + id);
  const provider = root.dataset.provider;
  const usd = value => value == null ? '--' : new Intl.NumberFormat(undefined, {style:'currency',currency:'USD',maximumFractionDigits:2}).format(value);
  const num = value => value == null ? '--' : new Intl.NumberFormat(undefined,{maximumFractionDigits:1}).format(value);
  const esc = value => escapeHTML(String(value ?? ''));
  const date = value => new Date(value).toLocaleString(undefined,{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'});
  const quotaName = value => ({five_hour:'5-hour',seven_day:'Weekly',seven_day_sonnet:'Weekly Sonnet',seven_day_opus:'Weekly Opus',weekly:'Weekly'})[value]||value.replace('antigravity_','').replaceAll('_',' ').replace(':',' · ');
  let data, chart, controller, previousProfile, selection, chartSignature, saving = false, profileDirty = false;
  const metric = (label,value) => `<div class="platform-cost-metric"><span class="platform-cost-label">${esc(label)}</span><strong>${esc(value)}</strong></div>`;
  const cells = values => '<tr>'+values.map(value=>`<td>${esc(value)}</td>`).join('')+'</tr>';
  const pricedValue = (cost, requests, unknown) => requests > unknown ? usd(cost)+(unknown?' (partial)':'') : '--';
  function renderCycle() {
    if (!data) return;
    const r=data.report, cycle=r.cycles[Number(el('cycle').value)];
    const actual=pricedValue(r.cost,r.requests,r.unknownRequests);
    el('models').innerHTML=r.models.length?r.models.map(m=>{const q=(cycle?.models||[]).find(v=>v.model===m.model&&v.effort===m.effort&&v.speed===m.speed);return cells([`${m.model} / ${m.effort||'unknown'} / ${m.speed||'unknown'}`,pricedValue(m.cost,m.requests,m.unknownRequests),provider==='codex'?(m.creditsUnknown===m.requests?'-- (speed unknown)':m.creditsUnknown?`${num(m.credits)} (partial)` :num(m.credits)):'Not applicable',`${num(m.requests-m.unknownRequests)} / ${num(m.requests)}`,m.input?num(m.cached/m.input*100)+'%':'--',q?num(q.quotaPoints)+' pp':'--',usd(q?.per100),num(q?.outputPerPoint)]);}).join(''):'<tr><td colspan="8">No token usage recorded for this provider.</td></tr>';
    el('summary').innerHTML=metric('Observed API value',actual)+metric('Period value / monthly fee',r.valueMultiple == null?'--':num(r.valueMultiple)+'x'+(r.unknownRequests?' (partial)':''))+metric('API / 100% quota',usd(cycle?.per100))+metric('Normalized x1 / 100%',usd(cycle?.x1));
    if (!cycle) {
      el('cycle-note').textContent='No quota observations in this range. Cost records alone cannot establish subscription allowance.';
    } else {
      selection=cycle.start+'|'+cycle.quota+'|'+cycle.plan;
      el('cycle-note').textContent=`${num(cycle.startUsed)}% → ${num(cycle.endUsed)}%; ${usd(cycle.cost)} matched. ${cycle.complete?'Near-full allowance observed.':'Partial allowance observed.'} ${cycle.per100 == null?'Not enough priced, matched usage to estimate value.':`Per-100% estimate ${usd(cycle.low)}–${usd(cycle.high)} (meter rounding only).`} ${cycle.boundary==='first observation'?'':cycle.boundary+'.'}`;
    }
    const points=cycle?.points||[];
    const hasPoints=points.length>1 && cycle.cost>0;
    el('chart-empty').hidden=hasPoints;
    el('chart').hidden=!hasPoints;
    const signature=JSON.stringify([points,hasPoints,document.documentElement.getAttribute('data-theme')]);
    if (signature !== chartSignature && chart) {chart.destroy();chart=null;}
    chartSignature=signature;
    if (hasPoints && !chart) {
      const color=getComputedStyle(root).getPropertyValue('--text-secondary').trim()||'#8791a0';
      chart=new Chart(el('chart'),{type:'scatter',data:{datasets:[{label:'Observed API value',data:points.map(p=>({x:p.used,y:p.cost})),borderColor:'#14b8a6',backgroundColor:'#14b8a6',pointRadius:3,showLine:false}]},options:{responsive:true,maintainAspectRatio:false,animation:false,plugins:{legend:{display:false},tooltip:{callbacks:{label:c=>`${num(c.parsed.x)}% used · ${usd(c.parsed.y)} recorded`}}},scales:{x:{min:0,max:100,title:{display:true,text:'Quota used (%)',color},ticks:{color}},y:{beginAtZero:true,title:{display:true,text:'API value since first observation (USD)',color},ticks:{color,callback:v=>usd(v)}}}}});
    }
    const weekly=cycle && (cycle.quota==='seven_day'||cycle.quota==='weekly'||cycle.quota.endsWith(':weekly'));
    el('plans').innerHTML=weekly && cycle.x1!=null ? [[1,20],[5,100],[20,200]].map(([mult,fee])=>{const week=cycle.x1*mult,month=week*30/7;return cells([`x${mult} linear scenario`,usd(fee),usd(week),usd(month),num(month/fee)+'x']);}).join('') : '<tr><td colspan="5">Choose a priced weekly allowance and configure its plan multiplier.</td></tr>';
  }
  function render(forceProfile = false) {
    const r=data.report;
    const old=selection;
    el('cycle').innerHTML=r.cycles.map((c,i)=>`<option value="${i}">${esc(`${date(c.start)} · ${quotaName(c.quota)} · ${num(c.startUsed)}→${num(c.endUsed)}%`)}</option>`).join('')||'<option value="-1">No observations</option>';
    el('cycle').disabled=!r.cycles.length;
    let chosen=r.cycles.findIndex(c=>c.start+'|'+c.quota+'|'+c.plan===old);
    if(chosen<0){r.cycles.forEach((c,i)=>{if(c.complete && (c.quota==='seven_day'||c.quota==='weekly'||c.quota.endsWith(':weekly')) && (chosen<0||Date.parse(c.end)>Date.parse(r.cycles[chosen].end)))chosen=i;});}
    if(chosen<0){r.cycles.forEach((c,i)=>{if(chosen<0||Date.parse(c.end)>Date.parse(r.cycles[chosen].end))chosen=i;});}
    el('cycle').value=String(chosen);

    el('cycles').innerHTML=r.cycles.slice().sort((a,b)=>Date.parse(b.start)-Date.parse(a.start)).map(c=>cells([`${date(c.start)} → ${date(c.end)}`,`${c.quota} / ${c.plan||'unknown'}`,`${num(c.startUsed)} → ${num(c.endUsed)}%`,usd(c.cost),usd(c.per100),c.complete?'Near-full observed':c.boundary])).join('')||'<tr><td colspan="6">No quota observations in this period.</td></tr>';
    el('devices').innerHTML=r.devices.map(d=>`<p>${esc(d.name)}: ${esc(usd(d.cost))} · ${esc(num(d.requests))} requests</p>`).join('');
    el('warnings').innerHTML=r.warnings.map(w=>`<li>${esc(w)}</li>`).join('');
    el('basis').textContent=data.basis;
    el('plan-source').textContent=r.profile.source||'Plan not detected. Set the monthly fee you actually pay; discounts and taxes can differ from list prices.';
    if (forceProfile || !profileDirty) {el('plan-name').value=r.profile.name;el('price').value=r.profile.monthlyUsd??'';el('multiplier').value=r.profile.multiplier??'';profileDirty=false;}
    el('status').textContent=`${date(data.start)} → ${date(data.end)} (local time). ${r.profile.name||'Plan unconfigured'}; ${num(r.requests)} requests (${num(r.unknownRequests)} unpriced).`;
    if(data.meter_stale)el('status').textContent+=` Quota data is delayed: last observation ${date(data.latest_meter_at)}. Check collector polling before relying on allowance estimates.`;
    renderCycle();
  }
  async function load(profile) {
    if(saving)return false;
    const focusTarget=document.activeElement;
    saving=!!profile;
    controller?.abort();controller=new AbortController();
    const requestController=controller;
    root.setAttribute('aria-busy','true');el('refresh').disabled=true;
    el('days').disabled=saving;
    el('plan-form').querySelector('button[type="submit"]').disabled=saving;
    el('undo').disabled=saving||!previousProfile;
    el('plan-form').querySelectorAll('input').forEach(input=>{input.disabled=saving;});
    el('status').textContent=saving?'Saving plan...':'Refreshing subscription observations...';
    const params=new URLSearchParams({provider,days:el('days').value});
    const accountID=new URLSearchParams(location.search).get('account');
    if(accountID)params.set('account',accountID);
    try {
      const response=await authFetch(`${API_BASE}/api/subscription-value?${params}`,{signal:requestController.signal,...(profile?{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(profile)}:{})});
      if(requestController!==controller)return;
      if(!response.ok){const problem=await response.json().catch(()=>null);throw new Error(problem?.error||'Could not load subscription data. Try again.');}
      const next=await response.json();
      if(requestController!==controller)return false;
      data=next;render(!!profile);
      return true;
    } catch(error) {if(error.name!=='AbortError' && requestController===controller)el('status').textContent=error.message;}
    finally {if(requestController===controller){saving=false;root.setAttribute('aria-busy','false');el('refresh').disabled=false;el('days').disabled=false;el('plan-form').querySelector('button[type="submit"]').disabled=false;el('plan-form').querySelectorAll('input').forEach(input=>{input.disabled=false;});el('undo').disabled=!previousProfile;if(document.activeElement===document.body&&focusTarget?.isConnected&&root.contains(focusTarget))(focusTarget.disabled?el('plan-name'):focusTarget).focus();}}
  }
  el('cycle').addEventListener('change',renderCycle);
  el('days').addEventListener('change',()=>{selection=null;load();});
  el('refresh').addEventListener('click',()=>load());
  el('plan-form').addEventListener('input',()=>{profileDirty=true;});
  el('plan-form').addEventListener('submit',async event=>{event.preventDefault();const before=data?.report.profile;if(await load({name:el('plan-name').value,monthlyUsd:Number(el('price').value),multiplier:Number(el('multiplier').value)})){previousProfile=before;el('undo').disabled=!previousProfile;el('undo').textContent='Undo plan change';}});
  el('undo').addEventListener('click',async ()=>{if(previousProfile){const redo=data?.report.profile;if(await load(previousProfile)){previousProfile=redo;el('undo').textContent=el('undo').textContent.startsWith('Undo')?'Redo plan change':'Undo plan change';}}});
  new MutationObserver(()=>{if(data)renderCycle();}).observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
  setInterval(()=>{if(!document.hidden&&!el('plan-details').open&&!root.contains(document.activeElement))load();},60000);
  load();
})();
