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
  const capacityProvider = capacityTier && el('capacity-provider');
  const capacityModel = capacityTier && el('capacity-model');
  const capacityBenchmark = capacityTier && el('capacity-benchmark');
  const capacityMetric = capacityTier && el('capacity-metric');
  const capacityReports = new Map();
  const hiddenCapacityModels = new Set();
  const capacityModelLegend = capacityTier && el('capacity-model-legend');
  let highlightedCapacityModel = '';
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
      {id:'max', name:'Claude Max', fee:100},
    ],
    codex: [
      {id:'plus', name:'ChatGPT Plus', fee:20}, {id:'pro', name:'ChatGPT Pro', fee:200},
      {id:'pro100', name:'ChatGPT Pro 100', fee:100},
    ],
    antigravity: [
      {id:'pro', name:'Google AI Pro', fee:19.99}, {id:'ultra', name:'Google AI Ultra', fee:200, scale:20},
      {id:'ultra5', name:'Google AI Ultra 5×', fee:99.99, scale:5},
    ],
  };
  let capacityChart, benchmarkData, benchmarkError, capacityExport = [];
  const modelID = model => model.toLowerCase().replace(/^((?:gpt|gemini)-\d+)-(\d+)(?=-|$)/, '$1.$2').replace(/-\d{8}$/, '');
  const tokens = n => new Intl.NumberFormat(undefined, {notation:'compact',maximumFractionDigits:2}).format(n);
  const codingBenchmarks = {
    vulcanBench: {name:'VulcanBench Frontier v4', url:'https://vulcanbench.com/leaderboard.html'},
    terminalBench: {name:'Terminal-Bench 4.1', url:'https://www.tbench.ai/?efforts=all&version=4.1'},
    rampBench: {name:'Ramp SWE-Bench v2.1', url:'https://labs.ramp.com/swebench#leaderboard'},
    deepSWE: {name:'DeepSWE v1.1', url:'https://deepswe.datacurve.ai/', flagged:true},
  };
  const studyURL = 'https://github.com/data-john/SubscriptionBench/blob/bf513a7b42198fba2311cc5fdb35b707f413abcd/site/data/benchmark_data.json';
  const quotaSources = {
    local: {name:'Local'},
    study: {name:'SubscriptionBench', url:studyURL},
    daily: {name:'AI Coding Daily', url:'https://aicodingdaily.com/subscription-usage'},
    pricing: {name:'Real API Pricing', url:'https://github.com/Fei-Craft/real-api-pricing/blob/332d14ed903c3b7b042bb5a9ec2a3d224757476d/data/research/chatgpt-gpt6luna-plus-round2-2026-09-28.json'},
    estimate: {name:'Estimated'},
  };
  const modelKey = ({model,effort,speed}) => [model,effort,speed].join('|');
  const codexCreditRates = {
    'gpt-6.2-astra':[200,10,1000], 'gpt-6-astra':[250,25,1250],
    'gpt-6.2-sol':[50,2.5,250], 'gpt-6.1-sol':[50,2.5,250], 'gpt-6-sol':[50,5,250],
    'gpt-6.1-terra':[25,1.25,125], 'gpt-6-luna':[2.5,0.25,12.5],
  };
  function quotaCredits(model, input, cached, output) {
    const rates = codexCreditRates[model];
    return rates ? (Math.max(0,input-cached)*rates[0] + cached*rates[1] + output*rates[2])/1e6 : 0;
  }
  const median = values => {
    const sorted = values.slice().sort((a,b)=>a-b);
    const middle = (sorted.length-1)/2;
    return (sorted[Math.floor(middle)] + sorted[Math.ceil(middle)])/2;
  };
  const modelName = model => model.replace(/^gpt/, 'GPT').replace(/^gemini/, 'Gemini').replace(/^claude/, 'Claude')
    .replace(/-(\d+)-(\d+)(?=-|$)/, '-$1.$2').replaceAll('-', ' ').replace(/\b(astra|sol|sonnet|neon|opus|haiku|hydrogen|fable|terra|luna|carbon|argon|flash)\b/g, word => word[0].toUpperCase() + word.slice(1));
  function modelConfig(model, recordedEffort, speed = '') {
    let id = modelID(model);
    const effortSuffix = id.startsWith('gemini-') && id.match(/-(low|medium|high|xhigh)$/);
    let effort = recordedEffort?.toLowerCase() || effortSuffix?.[1] || '';
    if (effortSuffix) id = id.slice(0, -effortSuffix[0].length);
    if (['extra','extra-high','extra_high','extra high','x-high'].includes(effort)) effort = 'xhigh';
    return {model:id, effort, speed:speed === 'standard' ? '' : speed};
  }

  function capacityPlanID(provider, plan) {
    const id = plan?.trim().toLowerCase() || '';
    if (provider === 'antigravity') {
      if (/^(?:google ai )?pro$/.test(id)) return 'pro';
      if (/^(?:google ai )?ultra(?: \$?200| 20x)$/.test(id)) return 'ultra';
      if (/^(?:google ai )?ultra(?: \$?100| 5x| 5×)$/.test(id)) return 'ultra5';
      if (id === 'ultra' || id === 'google ai ultra') return 'ultra-unspecified';
    }
    if (provider === 'codex') {
      if (['plus','chatgpt plus'].includes(id)) return 'plus';
      if (['pro','pro 20x','pro $200','chatgpt pro $200'].includes(id)) return 'pro';
      if (['prolite','pro100','pro 100','pro $100','chatgpt pro 100','chatgpt pro $100'].includes(id)) return 'pro100';
    }
    if (provider === 'anthropic') {
      if (['pro','claude pro'].includes(id)) return 'pro';
      if (['ultra','claude ultra','max 20x'].includes(id)) return 'ultra';
      if (['max 5x','claude max 5x','claude max','max'].includes(id)) return 'max';
    }
    return id;
  }

  function buildCapacityRows(provider, report, tier, suite) {
    const plans = capacityPlans[provider];
    const [base,higher,mid] = plans;
    const profile = report?.profile;
    const currentID = capacityPlanID(provider, profile?.planType || profile?.name);
    const plan = tier === 'current'
      ? {id:currentID, name:profile?.name || 'Current plan', fee:profile?.monthlyUsd}
      : tier === 'base' ? base : tier === 'mid' ? mid : higher;
    const tierScale = id => id === base.id ? 1 : plans.find(option => option.id === id)?.scale;
    const configs = new Map();
    const config = (model, effort, speed) => {
      if (provider === 'antigravity' && model.startsWith('gemini-') && speed === 'unknown') speed = '';
      const value = modelConfig(model, effort, speed);
      const key = modelKey(value);
      if (!configs.has(key)) configs.set(key, {...value, pools:new Map(), benchmarks:[]});
      return configs.get(key);
    };
    const include = model => model.startsWith(provider === 'codex' ? 'gpt-' : provider === 'anthropic' ? 'claude-' : 'gemini-');
    for (const [model,efforts] of Object.entries(capacityModels[provider])) {
      for (const effort of efforts) config(model,effort);
    }
    for (const [id, source] of Object.entries(codingBenchmarks)) {
      for (const [model,effort,passes,attempts,meanOutput] of benchmarkData?.[id] || []) {
        if (!include(model)) continue;
        const entry = config(model, effort);
        if ((suite === id || suite === 'all' && !source.flagged) && attempts > 0 && meanOutput > 0) {
          entry.benchmarks.push({id, passes, attempts, meanOutput});
        }
      }
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
        const hasTokens = sample.matchedInput || sample.matchedCached || sample.matchedWrites || sample.matchedOutput;
        if (!sample.matchedIntervals || !hasTokens) continue;
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
      for (const [model,effort,pool,points,input,cached,writes,output,cost,,attempts] of benchmarkData?.subscriptionBench || []) {
        addSample(config(model,effort), {plan:'pro', pool, source:'study', points, input:input+cached+writes, cached, writes, output, cost, unpriced:0, runs:1, requests:attempts});
      }
    }
    for (const [owner,tier,model,effort,points,input,cached,writes,output,cost] of benchmarkData?.codingDailyQuota || []) {
      if (owner !== provider) continue;
      addSample(config(model,effort), {plan:tier, pool:'weekly', source:'daily', points, input:input+cached+writes, cached, writes,
        output, cost, unpriced:0, runs:1, requests:0});
    }
    if (provider === 'codex') {
      for (const [model,effort,points,input,cached,output,cost] of benchmarkData?.pricingQuota || []) {
        addSample(config(model,effort), {plan:'plus', pool:'weekly', source:'pricing', points,
          input:input+cached, cached, writes:0, output, cost, unpriced:0, runs:1, requests:0});
      }
    }

    const rows = [];
    for (const item of configs.values()) {
      if (!item.pools.size && !item.benchmarks.length && !item.speed && [...configs.values()].some(other => other.model === item.model && other.effort === item.effort && other.speed)) continue;
      const eligible = [...item.pools.values()].flatMap(sample => {
        if (!(sample.points > 0 && sample.output > 0)) return [];
        const googleShared = provider !== 'antigravity' || ['gemini-weekly','antigravity_gemini_pro:weekly'].includes(sample.pool);
        const scale = sample.plan === plan.id && (plan.id || tier === 'current') ? 1
          : googleShared ? tierScale(plan.id) / tierScale(sample.plan) : null;
        return scale ? [{sample, scale}] : [];
      });
      // One equally weighted attempt from each available suite. Suite size
      // changes the evidence behind a pass rate, never the workload mix.
      const outputPerMix = item.benchmarks.reduce((sum, sample) => sum + sample.meanOutput, 0);
      const solvesPerMix = item.benchmarks.reduce((sum, sample) => sum + sample.passes / sample.attempts, 0);
      // Find the limiting meter within each source. Keep estimates from independent sources.
      const estimates = ['local','study','daily','pricing'].flatMap(source => {
        const bySource = eligible.filter(candidate => candidate.sample.source === source);
        const sameTier = bySource.filter(candidate => candidate.scale === 1);
        const candidates = sameTier.length ? sameTier : bySource;
        const limiting = candidates.map(({sample,scale}) => ({
          sample, scale, factor:4 * 100 * scale / sample.points,
        })).sort((a,b) => a.sample.output * a.factor - b.sample.output * b.factor)[0];
        if (!limiting) return [];
        const {sample,scale,factor} = limiting;
        const inTokens = sample.input * factor;
        const outTokens = sample.output * factor;
        const api = sample.unpriced ? null : sample.cost * factor;
        return [{sample, scale, inTokens, outTokens, api, cachedTokens:sample.cached * factor}];
      });
      const passRate = item.benchmarks.length ? solvesPerMix/item.benchmarks.length : null;
      rows.push({...item, provider, plan, estimates, passRate,
        outputPerAttempt:outputPerMix > 0 ? outputPerMix/item.benchmarks.length : null});
    }

    const workloads = new Map((benchmarkData?.codexWorkloads || []).map(([model,effort,input,cached,output]) =>
      [modelKey(modelConfig(model,effort)), {input,cached,output}]));
    const anchors = rows.filter(row => !row.speed).flatMap(row => row.estimates.map(estimate => ({model:row.model, estimate})));
    for (const row of rows) {
      if (row.estimates.length || row.speed || !row.outputPerAttempt) continue;
      const matches = anchors.filter(anchor => anchor.model === row.model);
      const local = matches.filter(({estimate}) => estimate.sample.source === 'local' && estimate.scale === 1);
      const sameModel = local.length ? local : matches;
      const workload = workloads.get(modelKey(row));
      // Purchased-credit weights stand in for relative included Codex usage only in this estimate.
      const workloadCredits = workload ? quotaCredits(row.model,workload.input,workload.cached,workload.output) : 0;
      const creditAnchor = ({model,estimate}) => quotaCredits(model,estimate.inTokens,estimate.cachedTokens,estimate.outTokens);
      const ownCredits = sameModel.map(creditAnchor).filter(credits => credits > 0);
      const poolCredits = (ownCredits.length ? ownCredits : anchors.map(creditAnchor).filter(credits => credits > 0));
      if (workloadCredits > 0 && poolCredits.length) {
        const factor = median(poolCredits)/workloadCredits;
        row.estimates.push({sample:{source:'estimate', basis:`${ownCredits.length ? 'Same model' : 'Same plan'} · Codex credit weights`},
          scale:1, inTokens:workload.input * factor, outTokens:workload.output * factor, api:null});
      } else if (sameModel.length) {
        row.estimates.push({sample:{source:'estimate',basis:'Same model · output per quota point'}, scale:1,
          inTokens:median(sameModel.map(({estimate})=>estimate.inTokens)),
          outTokens:median(sameModel.map(({estimate})=>estimate.outTokens)),api:null});
      }
    }
    for (const row of rows) {
      if (!row.outputPerAttempt) continue;
      for (const estimate of row.estimates) {
        const attempts = estimate.outTokens / row.outputPerAttempt;
        estimate.solves = attempts * row.passRate;
        estimate.quotaPerAttempt = 400 / attempts;
        estimate.quotaPerSolve = estimate.solves > 0 ? 400 / estimate.solves : null;
        estimate.subscriptionPerSolve = estimate.solves > 0 && row.plan.fee > 0 ? row.plan.fee / estimate.solves : null;
      }
    }
    return rows;
  }


  function highlightCapacityModel(key = '') {
    if (hiddenCapacityModels.has(key)) key = '';
    if (key === highlightedCapacityModel) return;
    highlightedCapacityModel = key;
    for (const button of capacityModelLegend.children) {
      button.toggleAttribute('data-highlight',button.dataset.model === key);
    }
    capacityChart?.update('none');
  }

  function renderCapacityModelLegend(datasets) {
    const models = [...new Map(datasets.map(set => [set.modelKey,set])).values()];
    const signature = JSON.stringify(models.map(set => [set.modelKey,set.pointStyle,set.shade]));
    if (capacityModelLegend.dataset.signature === signature) return;
    capacityModelLegend.dataset.signature = signature;
    const focused = capacityModelLegend.contains(document.activeElement) ? document.activeElement.dataset.model : '';
    const buttons = models.map(set => {
      const key = set.modelKey;
      const button = document.createElement('button');
      button.type = 'button';
      button.dataset.model = key;
      button.style.setProperty('--series-color',set.shade);
      button.setAttribute('aria-pressed',String(!hiddenCapacityModels.has(key)));
      button.toggleAttribute('data-highlight',key === highlightedCapacityModel);
      const marker = document.createElement('canvas');
      marker.width = marker.height = 17;
      marker.setAttribute('aria-hidden','true');
      const context = marker.getContext('2d');
      context.fillStyle = context.strokeStyle = set.shade;
      Chart.helpers.drawPoint(context,{pointStyle:set.pointStyle,radius:5,borderWidth:1},8.5,8.5);
      button.append(marker,set.label);
      button.addEventListener('pointerenter',() => highlightCapacityModel(key));
      button.addEventListener('pointerleave',() => highlightCapacityModel());
      button.addEventListener('focus',() => highlightCapacityModel(key));
      button.addEventListener('blur',() => highlightCapacityModel());
      button.addEventListener('click',() => {
        const visible = hiddenCapacityModels.delete(key);
        if (!visible) hiddenCapacityModels.add(key);
        button.setAttribute('aria-pressed',String(visible));
        capacityChart.data.datasets.forEach((dataset,index) => {
          if (dataset.modelKey === key) capacityChart.setDatasetVisibility(index,visible);
        });
        highlightCapacityModel(key);
        capacityChart.update('none');
      });
      return button;
    });
    capacityModelLegend.replaceChildren(...buttons);
    buttons.find(button => button.dataset.model === focused)?.focus({preventScroll:true});
  }

  const capacityEffortLabels = {
    id:'capacityEffortLabels',
    afterEvent(chart,{event,inChartArea,replay}) {
      if (replay || !['mousemove','mouseout','touchstart','touchmove'].includes(event.type)) return;
      const [item] = inChartArea ? chart.getActiveElements() : [];
      highlightCapacityModel(item ? chart.data.datasets[item.datasetIndex].modelKey : '');
    },
    afterDatasetsDraw(chart) {
      if (!highlightedCapacityModel) return;
      const {ctx,chartArea} = chart;
      const occupied = [];
      ctx.save();
      ctx.font = `600 11px ${Chart.defaults.font.family}`;
      ctx.textBaseline = 'middle';
      ctx.lineWidth = 3;
      ctx.lineJoin = 'round';
      for (const meta of chart.getSortedVisibleDatasetMetas()) {
        const set = chart.data.datasets[meta.index];
        if (set.modelKey !== highlightedCapacityModel) continue;
        ctx.fillStyle = set.shade;
        ctx.strokeStyle = set.surface;
        meta.data.forEach(({x,y,skip},index) => {
          if (skip) return;
          const label = set.data[index].effort;
          const width = ctx.measureText(label).width + 6;
          const left = Math.max(chartArea.left,Math.min(x + 10,chartArea.right-width));
          const center = [-9,11,-25,27,-41,43,-57,59].map(offset => y+offset)
            .find(pos => pos >= chartArea.top+6 && pos <= chartArea.bottom-6 &&
              !occupied.some(box => Math.abs(pos-box.y)<14 && left<box.right && left+width>box.left));
          if (center == null) return;
          occupied.push({left,right:left+width,y:center});
          ctx.strokeText(label,left+3,center);
          ctx.fillText(label,left+3,center);
        });
      }
      ctx.restore();
    },
  };

  function renderCapacity() {
    if (!capacityTier) return;
    const key = capacityProvider.value;
    const providers = key === 'all' ? [...comparisonRows.keys()] : [key];
    const suite = capacityBenchmark.value;
    const qualityMode = capacityMetric.value === 'quality';
    const cohort = qualityMode && benchmarkData?.[suite]?.filter(row => row[3] > 0 && row[4] > 0);
    const baseline = cohort?.length ? median(cohort.map(row => row[2]/row[3])) : null;
    const workMetric = qualityMode ? 'workUnits' : 'solves';
    const allRows = providers.flatMap(id => buildCapacityRows(id,capacityReports.get(id),capacityTier.value,suite));
    for (const row of allRows) {
      if (baseline != null && row.passRate != null) {
        row.qualityWeight = Math.min(8, 4 ** ((row.passRate-baseline)/0.05));
      }
      for (const estimate of row.estimates) {
        if (estimate.solves == null) continue;
        if (row.qualityWeight != null) estimate.workUnits = estimate.solves * row.qualityWeight;
        if (row.plan.fee > 0 && estimate[workMetric] != null) estimate.workPerDollar = estimate[workMetric] / row.plan.fee;
      }
    }
    const ids = ['*', ...[...new Set(allRows.map(row => row.model))].sort()];
    const model = ids.includes(requestedModel) && requestedModel ? requestedModel
      : ids.includes(capacityModel.value) ? capacityModel.value : '*';
    if (model === requestedModel) requestedModel = '';
    if (ids.join('|') !== [...capacityModel.options].map(option => option.value).join('|')) {
      capacityModel.replaceChildren(...ids.map(id => new Option(id === '*' ? 'All models' : modelName(id), id)));
    }
    capacityModel.value = model;
    const order = effort => effortOrder.includes(effort) ? effortOrder.indexOf(effort) : effortOrder.length;
    const featured = {codex:'gpt-6.2-astra', anthropic:'claude-sonnet-5-5', antigravity:'gemini-4-neon'};
    const isFeatured = row => row.model === featured[row.provider];
    const rows = allRows.filter(row => model === '*'
      ? row.benchmarks.length || row.estimates.length || Object.hasOwn(capacityModels[row.provider],row.model)
      : row.model === model);
    rows.sort((a,b) => providers.indexOf(a.provider)-providers.indexOf(b.provider) ||
      Number(isFeatured(b))-Number(isFeatured(a)) || a.model.localeCompare(b.model) ||
      order(a.effort)-order(b.effort) || a.speed.localeCompare(b.speed));
    const effortName = row => {
      const names = {low:'Low', medium:'Medium', high:'High', xhigh:'Extra High', max:'Max', thinking:'Thinking'};
      const speed = row.speed === 'unknown' ? 'Speed unknown' : row.speed;
      return (names[row.effort] || row.effort || 'Unknown') + (speed ? ' · '+speed : '');
    };
    const range = (items, metric, format) => {
      const values = items.map(item => item[metric]).filter(value => value != null);
      if (!values.length) return '—';
      const start = format(Math.min(...values));
      const end = format(Math.max(...values));
      return (items.some(item => item.sample.source === 'estimate') ? '≈' : '') + (start === end ? start : start + '–' + end);
    };
    const scopeLabel = row => comparisonRows.get(row.provider).dataset.label;
    const modelLabel = row => modelName(row.model) + ' · ' + effortName(row);
    const suiteNames = row => row.benchmarks.map(sample => codingBenchmarks[sample.id].name).join(' + ');
    const whole = n => num(Math.round(n));
    const passes = n => n == null ? '—' : num(Math.round(n*100)) + '%';
    const decimals = new Intl.NumberFormat(undefined,{maximumFractionDigits:2});
    const small = n => n > 0 && n < 0.01 ? '<0.01' : decimals.format(n);
    const formatWork = n => n < 1 ? small(n) : num(n);
    const workLabel = qualityMode ? 'work units' : 'solves';
    const qualityNote = el('capacity-quality-note');
    qualityNote.hidden = !qualityMode || baseline == null;
    qualityNote.textContent = baseline == null ? '' : `Hypothetical work units = estimated solves × weight. Median ${num(baseline*100)}% = 1×. Five points up gives 4×, five down gives ¼× (8× cap).`;
    el('capacity-work-column').textContent = qualityMode ? 'Work units / $1' : 'Solves / $1';
    el('capacity-chart-title').textContent = qualityMode ? 'Quota vs. quality-adjusted work' : 'Quota vs. solves';
    el('capacity-corner').textContent = `Less quota + more ${workLabel} per $1`;
    el('capacity-chart').setAttribute('aria-label', `${workLabel} per subscription dollar by percent of weekly quota per attempt. Less quota and more work are toward the upper left. Values are also in the table below.`);
    const quotaNumber = new Intl.NumberFormat(undefined,{maximumSignificantDigits:3});
    const quota = n => `${quotaNumber.format(n)}%`;
    const feePerSolve = n => n < 0.01 ? '<$0.01' : usd(n);
    el('capacity-rows').innerHTML = providers.map(id => {
      const group = rows.filter(row => row.provider === id);
      if (!group.length) return '';
      const first = group[0];
      const fee = first.plan.fee > 0 ? usd(first.plan.fee) : '';
      const heading = [first.plan.name, fee].filter(Boolean).join(' · ');
      return `<tr class="subscription-provider-row"><th scope="row" colspan="5">${esc(scopeLabel(first))}<span>${esc(heading)}</span></th></tr>` +
        group.map((row,index) => `<tr><th scope="row" aria-label="${esc(modelLabel(row))}">${row.model !== group[index-1]?.model ? esc(modelName(row.model)) : ''}</th><td>${esc(effortName(row))}</td>` +
          [range(row.estimates,'quotaPerAttempt',quota), range(row.estimates,'workPerDollar',formatWork)]
            .map(value=>`<td>${esc(value)}</td>`).join('') +
          `<td title="${esc(row.qualityWeight == null ? suiteNames(row) : `${small(row.qualityWeight)}× work weight · ${suiteNames(row)}`)}">${passes(row.passRate)}</td></tr>`).join('');
    }).join('');

    const status = el('capacity-status');
    capacityExport = rows.flatMap(row => row.estimates.map(estimate => [
      row.provider, row.model, row.plan.name, row.effort, row.speed, quotaSources[estimate.sample.source].name,
      estimate.sample.basis, estimate.sample.points,
      estimate.inTokens, estimate.outTokens, estimate.api, estimate.api != null && row.plan.fee > 0 ? estimate.api / row.plan.fee : null,
      estimate.solves, row.passRate, suiteNames(row), estimate.quotaPerAttempt, estimate.quotaPerSolve, estimate.subscriptionPerSolve, estimate.workPerDollar,
      ...(qualityMode ? [baseline,row.qualityWeight,estimate.workUnits] : []),
    ]));
    el('capacity-export').disabled = !capacityExport.length;
    status.textContent = providers.some(id => !capacityReports.has(id)) || (!benchmarkData && !benchmarkError) ? 'Loading…'
      : benchmarkError ? 'Benchmark data unavailable'
      : !rows.some(row => row.estimates.length) ? 'No matching quota sample' : '';
    status.hidden = !status.textContent;
    const link = el('capacity-link');
    const linkSource = (label, url) => {
      const a = document.createElement('a');
      a.textContent = label;
      a.href = url;
      return a;
    };
    link.replaceChildren(...Object.entries(codingBenchmarks).map(([id,source]) => linkSource(source.name,
      id === 'vulcanBench' && benchmarkData?.vulcanSources?.[model] || source.url)),
      ...Object.values(quotaSources).filter(source=>source.url).map(source=>linkSource(source.name,source.url)));
    el('capacity-scores').innerHTML = rows.flatMap(row => row.benchmarks.map(sample => cells([
      modelLabel(row), codingBenchmarks[sample.id].name, `${sample.passes} / ${sample.attempts}`, whole(sample.meanOutput),
    ]))).join('');
    const gap = (value, reference) => {
      if (value == null || !(reference > 0)) return '—';
      const pct = Math.round((value/reference-1)*100);
      return (pct > 0 ? '+' : '')+num(pct)+'%';
    };
    el('capacity-evidence').innerHTML = rows.flatMap(row => {
      const local = row.estimates.find(item => item.sample.source === 'local' && item.scale === 1);
      return row.estimates.map(({sample,scale,api,inTokens,outTokens}) => {
        const source = quotaSources[sample.source].name;
        const detail = sample.basis || `${num(sample.runs)} / ${num(sample.points)} pp${sample.source === 'daily' && sample.points <= 2 ? ' · coarse meter' : ''}${scale !== 1 ? ' · scaled' : ''}${sample.assumedPlan ? ' · saved plan' : ''}`;
        const relative = local && sample.source !== 'local' && scale === 1
          ? `${gap(api,local.api)} / ${gap(outTokens,local.outTokens)}` : '—';
        return cells([modelLabel(row), `${source} · ${detail}`, api == null ? '—' : usd(api), `${tokens(inTokens)} / ${tokens(outTokens)}`, relative]);
      });
    }).join('');

    capacityChart?.destroy();
    capacityChart = null;
    const groups = new Map();
    const plottedByProvider = new Map(providers.map(id => [id,0]));
    const evidenceRank = estimate => estimate.sample.source === 'estimate' ? 4
      : (estimate.scale === 1 ? 0 : 2) + (estimate.sample.source === 'local' ? 0 : 1);
    for (const row of rows) {
      const [estimate] = row.estimates.filter(estimate => Number.isFinite(estimate.quotaPerAttempt) && Number.isFinite(estimate.workPerDollar))
        .sort((a,b)=>evidenceRank(a)-evidenceRank(b) || b.sample.points-a.sample.points);
      if (!estimate) continue;
      const suites = suiteNames(row);
      const group = [row.provider,row.model,row.speed,suites].join('|');
      if (!groups.has(group)) groups.set(group, []);
      const sample = estimate.sample;
      const basis = sample.basis || (sample.source === 'daily' && sample.points <= 2 ? `${sample.points} pp · coarse meter` : '');
      groups.get(group).push({x:estimate.quotaPerAttempt,y:estimate.workPerDollar,solves:estimate.solves,
        quotaPerSolve:estimate.quotaPerSolve,subscriptionPerSolve:estimate.subscriptionPerSolve,
        qualityWeight:row.qualityWeight,effort:effortName(row),passRate:row.passRate,
        source:sample.source,basis,scale:estimate.scale});
      plottedByProvider.set(row.provider,plottedByProvider.get(row.provider)+1);
    }
    const canPlot = groups.size > 0 && typeof Chart !== 'undefined';
    const plotted = [...plottedByProvider.values()].reduce((sum,count) => sum+count, 0);
    const benchmarked = rows.filter(row => row.benchmarks.length).length;
    const loading = status.textContent === 'Loading…';
    const needsWorkload = qualityMode && suite === 'all';
    el('capacity-coverage').textContent = !loading && benchmarked && !needsWorkload ? `${plotted}/${benchmarked} benchmark settings` : '';
    el('capacity-chart-wrap').hidden = !canPlot;
    const empty = el('capacity-chart-empty');
    empty.hidden = canPlot || loading;
    empty.textContent = needsWorkload ? 'Choose one benchmark workload for quality adjustment.'
      : groups.size ? 'Chart unavailable' : 'No settings have both estimates';
    const legend = el('capacity-legend');
    legend.hidden = !canPlot;
    capacityModelLegend.hidden = !canPlot;
    el('capacity-source-key').hidden = !canPlot;
    if (!canPlot || !el('capacity-chart-details').open) return;
    const theme = getComputedStyle(root);
    const color = theme.getPropertyValue('--text-secondary').trim();
    const grid = theme.getPropertyValue('--border-light').trim();
    const surface = theme.getPropertyValue('--surface-card').trim();
    const light = document.documentElement.getAttribute('data-theme') === 'light';
    legend.replaceChildren(...[...plottedByProvider].filter(([,count]) => count > 0).map(([id]) => {
      const item = document.createElement('li');
      item.textContent = comparisonRows.get(id).dataset.label;
      item.style.setProperty('--plot-color',resetColors[id][light ? 0 : 1]);
      return item;
    }));
    const shapes = ['circle','triangle','rectRot','rectRounded','star','rect','crossRot','cross','dash','line'];
    const modelOrder = new Map(providers.map(id => [id, [...new Set(allRows.filter(row=>row.provider===id && row.estimates.length)
      .map(row=>row.model))].sort()]));
    const seriesColor = (key,shade) => highlightedCapacityModel && highlightedCapacityModel !== key ? shade+'35' : shade;
    const datasets = [...groups.entries()].map(([key,data]) => {
      const [id,model,,suites] = key.split('|');
      const shade = resetColors[id][light ? 0 : 1];
      const pointStyle = shapes[modelOrder.get(id).indexOf(model) % shapes.length];
      const modelKey = id+'|'+model;
      return {label:modelName(model), provider:comparisonRows.get(id).dataset.label,
        modelKey, shade, surface, pointStyle, hidden:hiddenCapacityModels.has(modelKey),
        borderColor:() => seriesColor(modelKey,shade),
        pointBackgroundColor:({dataIndex}) => {
          const source = data[dataIndex]?.source;
          if (source === 'estimate') return shade+(highlightedCapacityModel && highlightedCapacityModel !== modelKey ? '1a' : '55');
          return source === 'local' ? seriesColor(modelKey,shade) : surface;
        },
        pointBorderColor:() => seriesColor(modelKey,shade), pointBorderWidth:1.5,
        pointRadius:() => highlightedCapacityModel === modelKey ? 7 : 5,
        pointHitRadius:8, pointHoverRadius:9,
        showLine:data.length>1, borderWidth:() => highlightedCapacityModel === modelKey ? 2.6 : 1.6,
        segment:{borderDash:({p0DataIndex,p1DataIndex})=>{
          const first = data[p0DataIndex], second = data[p1DataIndex];
          return first.source === second.source && first.source !== 'estimate' && first.scale === second.scale ? [] : [4,4];
        }},
        data, suites};
    });
    if (!datasets.some(set => set.modelKey === highlightedCapacityModel)) highlightCapacityModel();
    capacityChart = new Chart(el('capacity-chart'), {
      type:'scatter', data:{datasets},
      plugins:[capacityEffortLabels],
      options:{responsive:true, maintainAspectRatio:false, animation:false,
        layout:{padding:{top:28}},
        plugins:{legend:{display:false},tooltip:{callbacks:{
          title:([item]) => `${item.dataset.label} · ${item.raw.effort}`,
          label:item => `${quota(item.parsed.x)} weekly quota per attempt · ${formatWork(item.parsed.y)} ${workLabel} per $1`,
          afterLabel:item => [item.raw.quotaPerSolve != null ? `${quota(item.raw.quotaPerSolve)} weekly quota · ${feePerSolve(item.raw.subscriptionPerSolve)} of plan per solve` : '',
            qualityMode ? `${whole(item.raw.solves)} est. solves / 4 wk · ${small(item.raw.qualityWeight)}× weight` : `${whole(item.raw.solves)} est. solves / 4 wk`,
            `${passes(item.raw.passRate)} passed · ${quotaSources[item.raw.source].name}`, item.raw.basis, ...item.dataset.suites.split(' + ')].filter(Boolean),
        }}},
        scales:{
          x:{type:'logarithmic',grid:{color:grid},title:{display:true,text:'Weekly quota per attempt',color},
            ticks:{color,maxTicksLimit:8,callback:(value,index,ticks) => ticks[index].major ? quota(value) : ''}},
          y:{beginAtZero:true,grid:{color:grid},title:{display:true,text:`${qualityMode ? 'Work units' : 'Est. solves'} / $1 subscription`,color},
            ticks:{color,maxTicksLimit:6,callback:value=>tokens(value)}},
        },
      },
    });
    renderCapacityModelLegend(datasets);
  }
  const capacityParams = new URLSearchParams(location.search);
  let requestedModel = modelID(capacityParams.get('model') || '');
  if (capacityTier) {
    const requestedProvider = requestedModel.startsWith('gpt-') ? 'codex'
      : requestedModel.startsWith('claude-') ? 'anthropic'
      : requestedModel.startsWith('gemini-') ? 'antigravity' : '';
    if ([...capacityProvider.options].some(option => option.value === requestedProvider)) {
      capacityProvider.value = requestedProvider;
    }
    const tier = capacityParams.get('tier');
    if (['base','mid','higher','current'].includes(tier)) capacityTier.value = tier;
    const workload = capacityParams.get('benchmark');
    if ([...capacityBenchmark.options].some(option => option.value === workload)) capacityBenchmark.value = workload;
    if (capacityParams.get('metric') === 'quality') capacityMetric.value = 'quality';
    [capacityTier,capacityProvider,capacityModel,capacityBenchmark,capacityMetric].forEach(control => control.addEventListener('change',renderCapacity));
    el('capacity-chart').addEventListener('pointerleave',() => highlightCapacityModel());
    el('capacity-chart-details').addEventListener('toggle',renderCapacity);
    el('capacity-export').addEventListener('click', () => {
      const header = ['Provider','Model','Plan','Effort','Speed','Quota source','Estimate basis','Quota points','Input tokens / 4 weeks','Output tokens / 4 weeks','API USD / 4 weeks','API USD per USD paid','Estimated solves / 4 weeks','Benchmark pass fraction','Benchmark'];
      header.push('Weekly quota percent per attempt','Weekly quota percent per solve','Subscription USD per solve',capacityMetric.value === 'quality' ? 'Hypothetical work units per subscription USD' : 'Estimated solves per subscription USD');
      if (capacityMetric.value === 'quality') header.push('Median benchmark pass fraction','Relative quality weight','Hypothetical work units / 4 weeks');
      const quote = value => {
        const plain = String(value ?? '');
        const safe = typeof value === 'string' && /^[=+\-@\s]/.test(plain) ? "'" + plain : plain;
        return '"' + safe.replaceAll('"', '""') + '"';
      };
      const url = URL.createObjectURL(new Blob([[header,...capacityExport].map(row => row.map(quote).join(',')).join('\r\n')], {type:'text/csv;charset=utf-8'}));
      const download = document.createElement('a');
      download.href = url;
      download.download = 'onwatch-subscription-value.csv';
      download.click();
      setTimeout(()=>URL.revokeObjectURL(url), 1000);
    });
    authFetch(root.dataset.benchmarkUrl)
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
    resetStatus.textContent = !done ? 'Loading…' : !series.length ? 'Weekly usage unavailable'
      : !matched.length ? 'No matched weekly usage' : !showCharts ? 'Charts unavailable' : '';
    resetStatus.hidden = !resetStatus.textContent;
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
      row.innerHTML = `${label}<td colspan="3">${esc(message)}</td>`;
      return;
    }
    const output = (report.models || []).reduce((total, model) => total + model.output, 0);
    const outputPerDollar = report.unknownRequests === 0 && report.cost > 0 ? Math.round(output / report.cost) : null;
    const fee = plan?.source || plan?.name ? usd(plan.monthlyUsd) + (plan.source?.includes('list price') ? ' (list)' : '') : 'Not set';
    const value = pricedValue(report.cost, report.requests, report.unknownRequests);
    row.innerHTML = label + [
      fee, value === '--' ? 'No priced usage' : value,
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
    el('models').innerHTML=r.models.length?r.models.map(m=>{const q=(cycle?.models||[]).find(v=>v.model===m.model&&v.effort===m.effort&&v.speed===m.speed);return cells([`${m.model} / ${m.effort||'unknown'} / ${m.speed||'unknown'}`,pricedValue(m.cost,m.requests,m.unknownRequests),provider==='codex'?(m.creditsUnknown===m.requests?'-- (rate unavailable)':m.creditsUnknown?`${num(m.credits)} (partial)` :num(m.credits)):'Not applicable',`${num(m.requests-m.unknownRequests)} / ${num(m.requests)}`,m.input?num(m.cached/m.input*100)+'%':'--',q?num(q.quotaPoints)+' pp':'--',usd(q?.per100),num(q?.outputPerPoint)]);}).join(''):'<tr><td colspan="8">No token usage recorded for this provider.</td></tr>';
    el('summary').innerHTML=metric('Observed API value',actual)+metric('API / 100% quota',usd(cycle?.per100))+metric('Normalized x1 / 100%',usd(cycle?.x1));
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
