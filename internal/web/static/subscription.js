/* Subscription comparisons use their own range; quota and cost charts keep theirs. */
(() => {
  'use strict';
  const root = document.getElementById('subscription-value');
  if (!root) return;
  const el = id => document.getElementById('subscription-' + id);
  let provider = root.dataset.provider;
  const comparison = root.dataset.comparison === 'true';
  const comparisonRows = new Map(comparison
    ? Array.from(root.querySelectorAll('[data-subscription-provider]'), row => [row.dataset.subscriptionProvider, row])
    : []);
  const scopeControls = comparison ? [el('provider'), el('account')].filter(Boolean) : [];
  const usd = value => value == null ? '--' : new Intl.NumberFormat(undefined, {style:'currency',currency:'USD',maximumFractionDigits:2}).format(value);
  const num = value => value == null ? '--' : new Intl.NumberFormat(undefined,{maximumFractionDigits:1}).format(value);
  const esc = value => escapeHTML(String(value ?? ''));
  const date = value => new Date(value).toLocaleString(undefined,{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'});
  const quotaName = value => ({five_hour:'5-hour',seven_day:'Weekly',seven_day_sonnet:'Weekly Sonnet',seven_day_opus:'Weekly Opus',weekly:'Weekly'})[value]||value.replace('antigravity_','').replaceAll('_',' ').replace(':',' · ');
  const CACHE_KEY='onwatch-subscription-value-cache-v1';
  const CACHE_LIMIT=4;
  let data, chart, controller, previousProfile, selection, chartSignature, saving = false, profileDirty = false;
  let account = comparison ? el('account')?.value : new URLSearchParams(location.search).get('account') || '1';
  const edits = new Map();
  const scope = () => provider === 'codex' ? `codex:${account}` : provider;
  const cacheIdentity = () => `${scope()}:${el('days').value}`;
  const readProfileForm = () => ({
    name: el('plan-name').value,
    nextBillingDate: el('next-billing-date').value,
    monthlyUsd: el('price').value,
    multiplier: el('multiplier').value,
  });
  function writeProfileForm(profile = {}) {
    el('plan-name').value = profile.name || '';
    el('next-billing-date').value = profile.nextBillingDate || '';
    el('price').value = profile.monthlyUsd ?? '';
    el('multiplier').value = profile.multiplier ?? '';
  }
  function readCache() {
    if(typeof localStorage==='undefined')return null;
    try{return JSON.parse(localStorage.getItem(CACHE_KEY)||'{}')[cacheIdentity()]?.data||null;}catch(_){return null;}
  }
  function writeCache(value) {
    if(typeof localStorage==='undefined')return;
    try {
      const cache=JSON.parse(localStorage.getItem(CACHE_KEY)||'{}');
      cache[cacheIdentity()]={ts:Date.now(),data:value};
      Object.entries(cache).sort((a,b)=>(b[1]?.ts||0)-(a[1]?.ts||0)).slice(CACHE_LIMIT).forEach(([key])=>delete cache[key]);
      localStorage.setItem(CACHE_KEY,JSON.stringify(cache));
    } catch(_) { /* Storage is an optimization; a live response still renders. */ }
  }
  const metric = (label,value) => `<div class="platform-cost-metric"><span class="platform-cost-label">${esc(label)}</span><strong>${esc(value)}</strong></div>`;
  const cells = values => '<tr>'+values.map(value=>`<td>${esc(value)}</td>`).join('')+'</tr>';
  const pricedValue = (cost, requests, unknown) => requests > unknown ? usd(cost)+(unknown?' (partial)':'') : '--';
  const capacityTier = comparison && el('capacity-tier');
  const capacityReports = new Map();
  const effortOrder = ['minimal','low','medium','high','xhigh','max','ultra'];
  const standardEfforts = ['low','medium','high','xhigh','max'];
  const capacityModels = {
    anthropic: {
      'claude-sonnet-5-5':standardEfforts, 'claude-opus-5-5':standardEfforts,
      'claude-haiku-5-5':standardEfforts.slice(0,3), 'claude-fable-5-2':standardEfforts,
    },
    codex: {
      'gpt-6.2-astra':standardEfforts, 'gpt-6.2-sol':standardEfforts,
      'gpt-6.1-sol':standardEfforts, 'gpt-6.1-terra':standardEfforts, 'gpt-6-luna':standardEfforts,
    },
    antigravity: {
      'gemini-4-hydrogen':standardEfforts.slice(0,4), 'gemini-4-neon':standardEfforts.slice(0,4),
      'gemini-4-carbon':standardEfforts.slice(0,3), 'gemini-4-argon':standardEfforts.slice(0,3),
      'gemini-3.8-flash':standardEfforts.slice(0,3),
    },
  };
  const capacityPlans = {
    anthropic: [
      {id:'pro', name:'Claude Pro', fee:20}, {id:'ultra', name:'Claude Ultra', fee:200, scale:10},
    ],
    codex: [
      {id:'plus', name:'ChatGPT Plus', fee:20}, {id:'pro', name:'ChatGPT Pro $200', fee:200},
    ],
    antigravity: [
      {id:'pro', name:'Google AI Pro', fee:19.99}, {id:'ultra', name:'Google AI Ultra $200', fee:200, scale:20},
    ],
  };
  let capacityChart, benchmarkData, benchmarkError;
  const modelID = model => model.toLowerCase().replace(/^((?:gpt|gemini)-\d+)-(\d+)(?=-|$)/, '$1.$2').replace(/-\d{8}$/, '');
  const tokens = n => new Intl.NumberFormat(undefined, {notation:'compact',maximumFractionDigits:2}).format(n);
  function modelConfig(model, recordedEffort, speed = '') {
    let id = modelID(model);
    const effortSuffix = id.startsWith('gemini-') && id.match(/-(low|medium|high|xhigh)$/);
    let effort = recordedEffort?.toLowerCase() || effortSuffix?.[1] || '';
    if (effortSuffix) id = id.slice(0, -effortSuffix[0].length);
    if (['extra','extra-high','extra_high','extra high','x-high'].includes(effort)) effort = 'xhigh';
    return {model:id, effort, speed};
  }

  function capacityPlanID(provider, plan) {
    const id = plan?.trim().toLowerCase() || '';
    if (provider === 'antigravity') {
      if (/^(?:google ai )?pro$/.test(id)) return 'pro';
      if (/^(?:google ai )?ultra(?: \$?200| 20x)$/.test(id)) return 'ultra';
      if (id === 'ultra' || id === 'google ai ultra') return 'ultra-unspecified';
    }
    if (provider === 'codex') {
      if (['plus','chatgpt plus'].includes(id)) return 'plus';
      if (['pro','pro 20x','pro $200','chatgpt pro $200'].includes(id)) return 'pro';
    }
    if (provider === 'anthropic') {
      if (['pro','claude pro'].includes(id)) return 'pro';
      if (['ultra','claude ultra','max 20x'].includes(id)) return 'ultra';
    }
    return id;
  }

  function buildCapacityRows(provider, report, tier, suite) {
    const [base, higher] = capacityPlans[provider];
    const profile = report?.profile;
    const currentID = capacityPlanID(provider, profile?.planType || profile?.name);
    const plan = tier === 'current'
      ? {id:currentID, name:profile?.name || 'Current plan', fee:profile?.monthlyUsd}
      : tier === 'base' ? base : higher;
    const configs = new Map();
    const config = (model, effort, speed) => {
      const value = modelConfig(model, effort, speed);
      const key = [value.model,value.effort,value.speed].join('|');
      if (!configs.has(key)) configs.set(key, {...value, pools:new Map()});
      return configs.get(key);
    };
    const include = model => model.startsWith(provider === 'codex' ? 'gpt-' : provider === 'anthropic' ? 'claude-' : 'gemini-');
    const benchmarks = new Map();
    for (const [model,efforts] of Object.entries(capacityModels[provider])) {
      for (const effort of efforts) config(model,effort);
    }
    const addBenchmark = (model, effort, passes, attempts, output) => {
      const key = [modelID(model),effort].join('|');
      const sample = benchmarks.get(key) || {passes:0, attempts:0, output:0};
      sample.passes += passes;
      sample.attempts += attempts;
      sample.output += output;
      benchmarks.set(key, sample);
    };
    for (const [model,effort,passes,attempts,meanOutput] of benchmarkData?.deepSWE || []) {
      if (!include(model)) continue;
      config(model, effort);
      if (suite === 'deepSWE') addBenchmark(model, effort, passes, attempts, meanOutput * attempts);
    }
    const addSample = (entry, sample) => {
      const key = [sample.plan,sample.pool,sample.source].join('|');
      const total = entry.pools.get(key) || {...sample, points:0, input:0, output:0, cost:0, runs:0, cached:0, writes:0, unpriced:0, requests:0};
      for (const field of ['points','input','output','cost','runs','cached','writes','unpriced','requests']) total[field] += sample[field];
      entry.pools.set(key,total);
    };
    for (const model of report?.models || []) config(model.model, model.effort, model.speed);
    for (const cycle of report?.cycles || []) {
      // Claude snapshots don't carry tier. A manually recorded subscription labels untagged samples.
      const measuredPlan = capacityPlanID(provider, cycle.plan || (provider === 'anthropic' ? profile?.name : ''));
      for (const sample of cycle.models || []) {
        if (!sample.matchedIntervals) continue;
        addSample(config(sample.model, sample.effort, sample.speed), {
          plan:measuredPlan, pool:cycle.quota, source:'local', points:sample.quotaPoints,
          assumedPlan:!cycle.plan && provider === 'anthropic',
          input:sample.matchedInput, output:sample.matchedOutput, cost:sample.matchedCost,
          cached:sample.matchedCached, writes:sample.matchedWrites, unpriced:sample.matchedUnpriced, runs:sample.matchedIntervals,
          requests:sample.matchedRequests,
        });
      }
    }
    if (provider === 'antigravity') {
      for (const [model,effort,pool,points,input,cached,writes,output,cost,passes,attempts] of benchmarkData?.subscriptionBench || []) {
        addSample(config(model,effort), {plan:'pro', pool, source:'study', points, input:input+cached+writes, cached, writes, output, cost, unpriced:0, runs:1, requests:attempts});
        if (suite === 'subscriptionBench') addBenchmark(model, effort, passes, attempts, output);
      }
    }

    const rows = [];
    for (const item of configs.values()) {
      if (!item.pools.size && !item.speed && [...configs.values()].some(other => other.model === item.model && other.effort === item.effort && other.speed)) continue;
      const eligible = [...item.pools.values()].flatMap(sample => {
        const googleShared = provider !== 'antigravity' || ['gemini-weekly','antigravity_gemini_pro:weekly'].includes(sample.pool);
        const scale = sample.plan === plan.id && (plan.id || tier === 'current') ? 1
          : sample.plan === base.id && plan.id === higher.id && googleShared ? higher.scale
          : sample.plan === higher.id && plan.id === base.id && googleShared && higher.scale ? 1 / higher.scale
          : null;
        return scale ? [{sample, scale}] : [];
      });
      // Prefer same-tier observations. If caps overlap, use the tightest output limit.
      const local = eligible.filter(candidate => candidate.scale === 1 && candidate.sample.source === 'local');
      const sameTier = eligible.filter(candidate => candidate.scale === 1);
      const candidates = local.length ? local : sameTier.length ? sameTier : eligible;
      const limiting = candidates.map(({sample,scale}) => ({
        sample, scale, factor:100 * 30 / 7 * scale / sample.points,
      })).sort((a,b) => a.sample.output * a.factor - b.sample.output * b.factor)[0];
      const benchmark = benchmarks.get([item.model,item.effort].join('|'));
      const monthly = limiting ? {
        inTokens:limiting.sample.input * limiting.factor,
        outTokens:limiting.sample.output * limiting.factor,
        api:limiting.sample.unpriced ? null : limiting.sample.cost * limiting.factor,
      } : null;
      const solves = monthly && benchmark?.output > 0 ? monthly.outTokens * benchmark.passes / benchmark.output : null;
      const perDollar = monthly?.api != null && plan.fee > 0 ? monthly.api / plan.fee : null;
      rows.push({...item, provider, plan, limiting, benchmark, monthly, solves, perDollar});
    }
    return rows;
  }

  function renderCapacity() {
    if (!capacityTier) return;
    const providerFilter = el('capacity-provider').value;
    const suite = el('capacity-benchmark').value;
    const providerNames = {anthropic:'Claude', codex:'Codex', antigravity:'Antigravity'};
    const providers = [...comparisonRows.keys()].filter(provider => providerFilter === 'all' || providerFilter === provider);
    const rows = providers.flatMap(provider => buildCapacityRows(provider,capacityReports.get(provider),capacityTier.value,suite));
    const order = effort => effortOrder.includes(effort) ? effortOrder.indexOf(effort) : effortOrder.length;
    rows.sort((a,b) => Number(!!b.monthly) - Number(!!a.monthly) ||
      a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model) || order(a.effort) - order(b.effort));
    const compact = value => value == null ? '—' : tokens(value);
    const money = value => value == null ? '—' : usd(value);
    el('capacity-rows').innerHTML = rows.map(row => {
      const {monthly, limiting, benchmark} = row;
      const details = [];
      if (limiting) {
        const {sample,scale} = limiting;
        const label = sample.source === 'local' ? 'local intervals' : 'published runs';
        details.push(`${num(sample.runs)} ${label} · ${num(sample.points)} quota points`);
        if (sample.source === 'local') details.push(`${num(sample.requests)} requests`);
        details.push(`${tokens(sample.cached)} cached · ${tokens(sample.writes)} cache writes`);
        if (sample.assumedPlan) details.push('Tier from saved plan');
        if (scale !== 1) details.push(`${num(scale)}× weekly tier ratio from ${sample.plan.toUpperCase()}`);
      } else details.push('No matched quota sample for this plan');
      if (benchmark) details.push(`${num(benchmark.passes)} / ${num(benchmark.attempts)} passes · ${tokens(benchmark.output / benchmark.attempts)} output / attempt`);
      else details.push('No matching benchmark effort');
      return `<tr><th scope="row">${esc(row.model)}<span class="subscription-comparison-plan">${esc(providerNames[row.provider])} · ${esc(row.effort || 'effort unrecorded')}${row.speed && row.speed !== 'unknown' ? ' · '+esc(row.speed) : ''}</span></th>` +
        [row.plan.name, monthly ? `${compact(monthly.inTokens)} / ${compact(monthly.outTokens)}` : '—', money(monthly?.api),
          money(row.perDollar), row.solves == null ? '—' : num(Math.round(row.solves)), details.join(' · ')]
          .map(value => `<td>${esc(value)}</td>`).join('') + '</tr>';
    }).join('');
    const plotted = rows.filter(row => row.perDollar != null && row.solves != null);
    const status = el('capacity-status');
    if (benchmarkError) status.textContent = 'Benchmark data unavailable. Token and API projections can still use local samples.';
    else if (!benchmarkData || capacityReports.size < comparisonRows.size) status.textContent = 'Loading model observations…';
    else if (!plotted.length) status.textContent = 'No model and effort has both a priced quota sample for this plan and a result in this benchmark.';
    else status.textContent = `${plotted.length} model and effort settings can be compared on this benchmark.`;

    capacityChart?.destroy();
    capacityChart = null;
    const canPlot = plotted.length && typeof Chart !== 'undefined';
    el('capacity-chart-wrap').hidden = !canPlot;
    if (!canPlot) return;
    const groups = new Map();
    const chartColors = ['#148f84','#c36825','#687ddd','#bb5183','#4180b4','#805fb0','#70862b','#aa7040'];
    for (const row of plotted) {
      const key = [row.provider,row.model,row.speed].join('|');
      if (!groups.has(key)) groups.set(key,[]);
      groups.get(key).push(row);
    }
    const color = getComputedStyle(root).getPropertyValue('--text-secondary').trim();
    const datasets = [...groups.values()].map((group,index) => {
      group.sort((a,b) => order(a.effort)-order(b.effort));
      return {label:`${providerNames[group[0].provider]} · ${group[0].model}${group[0].speed && group[0].speed !== 'unknown' ? ' · '+group[0].speed : ''}`,
        borderColor:chartColors[index % chartColors.length], backgroundColor:chartColors[index % chartColors.length],
        pointStyle:group.map(row => ['circle','rect','triangle','rectRot','crossRot','star','cross'][order(row.effort)] || 'circle'),
        pointRadius:5, showLine:true, borderWidth:2,
        data:group.map(row => ({x:row.perDollar,y:row.solves,effort:row.effort,cost:row.monthly.api,tokens:row.monthly.outTokens})),
      };
    });
    capacityChart = new Chart(el('capacity-chart'), {
      type:'scatter', data:{datasets},
      options:{responsive:true, maintainAspectRatio:false, animation:false,
        plugins:{legend:{labels:{color,usePointStyle:true,boxWidth:10}},tooltip:{callbacks:{
          label:item => `${item.dataset.label} · ${item.raw.effort} · ${num(Math.round(item.parsed.y))} solves · ${usd(item.raw.cost)} API value · ${tokens(item.raw.tokens)} output tokens`,
        }}},
        scales:{
          x:{beginAtZero:true,title:{display:true,text:'API dollars per $1 paid',color},ticks:{color}},
          y:{beginAtZero:true,title:{display:true,text:'Estimated benchmark solves / 30 days',color},ticks:{color}},
        },
      },
    });
  }
  if (capacityTier) {
    [capacityTier,el('capacity-provider'),el('capacity-benchmark')].forEach(control => control.addEventListener('change',renderCapacity));
    authFetch(API_BASE + '/static/subscription-benchmarks.json')
      .then(response => {if (!response.ok) throw new Error('Benchmark data unavailable'); return response.json();})
      .then(value => {benchmarkData=value;})
      .catch(() => {benchmarkError=true;})
      .finally(renderCapacity);
  }
  const resetReports = new Map();
  const resetCharts = [];
  const resetStatus = el('reset-status');
  let resetBounds;
  const dayLabel = new Intl.DateTimeFormat(undefined, {month:'short',day:'numeric',timeZone:'UTC'});
  const hourLabel = new Intl.DateTimeFormat(undefined, {month:'short',day:'numeric',hour:'numeric',timeZone:'UTC'});
  const resetColors = {
    anthropic: ['#b54708', '#f6ad73'],
    codex: ['#087f72', '#2dd4bf'],
    antigravity: ['#3856a7', '#9ab1fd'],
  };
  function renderResetValue() {
    if (!resetStatus) return;
    resetCharts.splice(0).forEach(chart => chart.destroy());
    const series = [...comparisonRows.keys()].map(key => resetReports.get(key)).filter(Boolean);
    const matched = series.filter(item => item.points?.length);
    const done = resetReports.size === comparisonRows.size;
    el('reset-summary').hidden = !series.length;
    el('reset-totals').innerHTML = series.map(item => {
      const last = item.points?.at(-1);
      return `<tr><th scope="row">${esc(item.label)}</th><td>${last ? usd(last.observedUsd) : '—'}</td><td>${last ? usd(last.withoutRefillUsd) : '—'}</td><td>${num(item.earlyRefills)}</td><td>${num(item.skippedIntervals)}</td></tr>`;
    }).join('');
    const showCharts = matched.length > 0 && typeof Chart !== 'undefined';
    el('reset-charts').hidden = !showCharts;
    if (!done) resetStatus.textContent = 'Loading weekly observations…';
    else if (!series.length) resetStatus.textContent = 'Weekly observations are unavailable. Use Refresh to retry.';
    else if (!matched.length) resetStatus.textContent = 'No priced usage could be matched to a weekly meter in this period.';
    else if (!showCharts) resetStatus.textContent = 'Charts are unavailable. See the totals below.';
    else if (matched.every(item => item.earlyRefills === 0)) resetStatus.textContent = 'The charts match where weekly usage was matched and no early refill was confirmed.';
    else resetStatus.textContent = 'Cumulative API value by UTC day.';
    if (!showCharts) return;

    const light = document.documentElement.getAttribute('data-theme') === 'light';
    const textColor = getComputedStyle(root).getPropertyValue('--text-secondary').trim() || '#8791a0';
    const yMax = Math.max(1, ...matched.map(item => item.points.at(-1).observedUsd));
    const tickLabel = el('days').value === '1' ? hourLabel : dayLabel;
    for (const [id, metric] of [['reset-observed','observedUsd'], ['reset-standard','withoutRefillUsd']]) {
      const datasets = matched.map(item => {
        const color = resetColors[item.provider][light ? 0 : 1];
        return {
          label:item.label, borderColor:color, backgroundColor:color, stepped:'after',
          borderWidth:2, pointRadius:2.5, pointHoverRadius:5,
          data:item.points.map(point => ({x:Date.parse(point.at), y:point[metric]})),
        };
      });
      resetCharts.push(new Chart(el(id), {
        type:'line', data:{datasets},
        options:{
          responsive:true, maintainAspectRatio:false, animation:false,
          plugins:{
            legend:{labels:{color:textColor,usePointStyle:true}},
            tooltip:{callbacks:{title:items=>dayLabel.format(items[0].parsed.x), label:item=>`${item.dataset.label} · ${usd(item.parsed.y)}`}},
          },
          scales:{
            x:{type:'linear', min:Date.parse(resetBounds.start), max:Date.parse(resetBounds.end), ticks:{color:textColor,maxTicksLimit:5,callback:value=>tickLabel.format(value)}},
            y:{beginAtZero:true, suggestedMax:yMax, ticks:{color:textColor,callback:value=>usd(value)}},
          },
        },
      }));
    }
  }
  function renderComparison(key, report, message = 'Loading usage…') {
    const row = comparisonRows.get(key);
    if (!row) return;
    if (capacityTier) {
      capacityReports.set(key, report);
      renderCapacity();
    }
    if (resetStatus && resetBounds) {
      const weekly = report?.weeklyValue?.[0];
      resetReports.set(key, report ? {...weekly, provider:key, label:row.dataset.label} : null);
      renderResetValue();
    }
    const plan = report?.profile;
    const renewal = plan?.nextBillingDate ? subscriptionRenewalLabel(plan.nextBillingDate) : '';
    const label = `<th scope="row">${esc(row.dataset.label)}${plan?.name ? `<span class="subscription-comparison-plan">${esc(plan.name)}</span>` : ''}${renewal ? `<time class="subscription-comparison-plan" datetime="${esc(plan.nextBillingDate)}">${esc(renewal)}</time>` : ''}</th>`;
    if (!report) {
      row.innerHTML = `${label}<td colspan="4">${esc(message)}</td>`;
      return;
    }
    const output = (report.models || []).reduce((total, model) => total + model.output, 0);
    const outputPerDollar = report.unknownRequests === 0 && report.cost > 0 ? Math.round(output / report.cost) : null;
    const multiple = report.valueMultiple;
    const fee = plan?.source || plan?.name ? usd(plan.monthlyUsd) + (plan.source?.includes('list price') ? ' (list)' : '') : 'Not set';
    const value = pricedValue(report.cost, report.requests, report.unknownRequests);
    row.innerHTML = label + [
      fee, value === '--' ? 'No priced usage' : value,
      multiple == null ? '—' : `${num(multiple)}×${report.unknownRequests ? ' (partial)' : ''}`,
      outputPerDollar == null ? '—' : num(outputPerDollar),
    ].map(value => `<td>${esc(value)}</td>`).join('');
  }
  function reportParams(key, bounds) {
    const params = new URLSearchParams({provider:key, days:el('days').value, ...bounds});
    if (key === 'codex' && account) params.set('account', account);
    return params;
  }
  function refreshComparison(bounds, requestController) {
    for (const key of comparisonRows.keys()) {
      if (key === provider) continue;
      const params = reportParams(key, bounds);
      authFetch(`${API_BASE}/api/subscription-value?${params}`, {signal:requestController.signal})
        .then(response => {
          if (!response.ok) throw new Error('Observation unavailable');
          return response.json();
        })
        .then(next => {
          if (requestController === controller) renderComparison(key, next.report);
        })
        .catch(() => {
          if (requestController === controller) renderComparison(key, null, 'Usage unavailable. Use Refresh to retry.');
        });
    }
  }
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
      chart=new Chart(el('chart'),{type:'scatter',data:{datasets:[{label:'Observed API value',data:points.map(p=>({x:p.used,y:p.cost})),borderColor:'#14b8a6',backgroundColor:'#14b8a6',borderWidth:2,pointRadius:3,pointHoverRadius:5,tension:.2,showLine:true}]},options:{responsive:true,maintainAspectRatio:false,animation:false,plugins:{legend:{display:false},tooltip:{callbacks:{label:c=>`${num(c.parsed.x)}% used · ${usd(c.parsed.y)} recorded`}}},scales:{x:{min:0,max:100,title:{display:true,text:'Quota used (%)',color},ticks:{color}},y:{beginAtZero:true,title:{display:true,text:'API value since first observation (USD)',color},ticks:{color,callback:v=>usd(v)}}}}});
    }
  }
  function render(forceProfile = false) {
    const r=data.report;
    renderComparison(provider, r);
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
    if (forceProfile || !profileDirty) {writeProfileForm(r.profile);profileDirty=false;}
    el('status').textContent=`${date(data.start)} → ${date(data.end)} (local time). ${r.profile.name||'Plan unconfigured'}; ${num(r.requests)} requests (${num(r.unknownRequests)} unpriced).`;
    el('status').textContent+=` ${r.profile.nextBillingDate ? subscriptionRenewalLabel(r.profile.nextBillingDate) : 'Billing date not recorded'}.`;
    if(data.meter_stale)el('status').textContent+=` Quota data is delayed: last observation ${date(data.latest_meter_at)}. Check collector polling before relying on allowance estimates.`;
    renderCycle();
  }
  async function load(profile, preserveDraft = false) {
    if(saving)return false;
    const focusTarget=document.activeElement;
    saving=!!profile;
    controller?.abort();controller=new AbortController();
    const requestController=controller;
    root.setAttribute('aria-busy','true');el('refresh').disabled=true;
    el('days').disabled=saving;
    scopeControls.forEach(control=>{control.disabled=saving;});
    el('plan-form').querySelector('button[type="submit"]').disabled=saving;
    el('undo').disabled=saving||!previousProfile;
    el('plan-form').querySelectorAll('input').forEach(input=>{input.disabled=saving;});
    el('status').textContent=saving?'Saving plan...':'Refreshing subscription observations...';
    const end = new Date();
    const bounds = comparison ? {
      start:new Date(end.getTime() - Number(el('days').value)*86400000).toISOString(),
      end:end.toISOString(),
    } : {};
    if (resetStatus) {
      resetBounds = bounds;
      resetReports.clear();
      renderResetValue();
    }
    if (capacityTier) {
      capacityReports.clear();
      renderCapacity();
    }
    const params=reportParams(provider, bounds);
    refreshComparison(bounds, requestController);
    try {
      const response=await authFetch(`${API_BASE}/api/subscription-value?${params}`,{signal:requestController.signal,...(profile?{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(profile)}:{})});
      if(requestController!==controller)return;
      if(!response.ok){const problem=await response.json().catch(()=>null);throw new Error(problem?.error||'Could not load subscription data. Try again.');}
      const next=await response.json();
      if(requestController!==controller)return false;
      data=next;writeCache(next);render(!!profile && !preserveDraft);
      return true;
    } catch(error) {
      if(error.name!=='AbortError' && requestController===controller) {
        el('status').textContent=data?`${error.message} Showing saved subscription data.`:error.message;
        resetReports.set(provider, null);
        renderResetValue();
        if (!data) renderComparison(provider, null, 'Usage unavailable. Use Refresh to retry.');
        else if (capacityTier) {
          capacityReports.set(provider, null);
          renderCapacity();
        }
      }
    }
    finally {if(requestController===controller){saving=false;root.setAttribute('aria-busy','false');el('refresh').disabled=false;el('days').disabled=false;scopeControls.forEach(control=>{control.disabled=false;});el('plan-form').querySelector('button[type="submit"]').disabled=false;el('plan-form').querySelectorAll('input').forEach(input=>{input.disabled=false;});el('undo').disabled=!previousProfile;if(document.activeElement===document.body&&focusTarget?.isConnected&&root.contains(focusTarget))(focusTarget.disabled?el('plan-name'):focusTarget).focus();}}
  }
  el('cycle').addEventListener('change',renderCycle);
  el('days').addEventListener('change',()=>{
    selection=null;
    for (const key of comparisonRows.keys()) renderComparison(key, null);
    const saved=readCache();
    if(saved){data=saved;render();el('status').textContent+=' Refreshing in the background.';}
    load();
  });
  el('refresh').addEventListener('click',()=>load());
  el('plan-form').addEventListener('input',()=>{profileDirty=true;});
  el('plan-form').addEventListener('submit',async event=>{
    event.preventDefault();
    const before=data?.report.profile, draft=readProfileForm();
    if(await load({...draft, monthlyUsd:Number(draft.monthlyUsd), multiplier:Number(draft.multiplier)})){
      previousProfile=before;el('undo').disabled=!previousProfile;el('undo').textContent='Undo plan change';
    }
  });
  el('undo').addEventListener('click',async ()=>{if(previousProfile){const redo=data?.report.profile;if(await load({...previousProfile,nextBillingDate:previousProfile.nextBillingDate||''},profileDirty)){previousProfile=redo;el('undo').textContent=el('undo').textContent.startsWith('Undo')?'Redo plan change':'Undo plan change';}}});
  function selectScope(nextProvider, nextAccount) {
    if (provider === nextProvider && account === nextAccount) return;
    const oldScope = scope();
    const accountChanged = nextAccount !== account;
    edits.set(oldScope, {draft:profileDirty ? readProfileForm() : null, previousProfile, undoLabel:el('undo').textContent});
    provider = nextProvider;
    account = nextAccount;
    if (accountChanged) renderComparison('codex', null);
    if (scope() === oldScope) {
      load();
      return;
    }
    controller?.abort();
    saving = false;
    const savedEdit = edits.get(scope());
    data = readCache();
    selection = undefined;
    previousProfile = savedEdit?.previousProfile;
    profileDirty = Boolean(savedEdit?.draft);
    writeProfileForm(savedEdit?.draft);
    el('undo').textContent = savedEdit?.undoLabel || 'Undo plan change';
    if (data) render();
    else {
      chart?.destroy();
      chart = null;
      for (const id of ['summary','models','cycles','devices','warnings','basis','plan-source','cycle','cycle-note']) {
        el(id).textContent = '';
      }
      el('chart').hidden = true;
      el('chart-empty').hidden = true;
    }
    load();
  }
  if (comparison) {
    el('provider').addEventListener('change', event => selectScope(event.target.value, account));
    el('account')?.addEventListener('change', event => selectScope(provider, event.target.value));
  }
  new MutationObserver(()=>{if(data)renderCycle();renderResetValue();renderCapacity();}).observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
  setInterval(()=>{if(!document.hidden&&!el('plan-details').open&&!root.contains(document.activeElement))load();},60000);
  const saved=readCache();
  if(saved){data=saved;render();el('status').textContent+=' Refreshing in the background.';}
  load();
})();
