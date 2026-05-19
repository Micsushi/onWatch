(function () {
  const bridge = createBridge();

  function createBridge() {
    if (window.go && window.go.menubar && window.go.menubar.App) {
      const app = window.go.menubar.App;
      return {
        mode: 'wails',
        getSnapshot: () => app.GetSnapshot(),
        getSettings: () => app.GetSettings(),
        openExternal: (url) => app.OpenExternal(url),
        refresh: () => app.Refresh(),
      };
    }

    const browserBridge = window.__ONWATCH_MENUBAR_BRIDGE__ || {};
    return {
      mode: browserBridge.mode || 'browser',
      requestedView: browserBridge.view || '',
      getSettings: async () => {
        const settings = Object.assign({}, browserBridge.settings || {});
        if (browserBridge.view) {
          settings.default_view = browserBridge.view;
        }
        return settings;
      },
      getSnapshot: async () => {
        const view = encodeURIComponent(browserBridge.view || 'standard');
        const resp = await fetch(`/api/menubar/summary?view=${view}`, { credentials: 'same-origin' });
        if (!resp.ok) {
          const err = new Error(`menubar summary failed: ${resp.status}`);
          err.status = resp.status;
          throw err;
        }
        return resp.json();
      },
      openExternal: (url) => window.open(url, '_blank', 'noopener,noreferrer'),
      refresh: async () => {},
    };
  }

  function escapeHTML(value) {
    return String(value || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function severityClass(status) {
    return status || 'healthy';
  }

  function circumference(radius) {
    return 2 * Math.PI * radius;
  }

  function meterMarkup(quota) {
    const radius = 24;
    const length = circumference(radius);
    const percent = Math.max(0, Math.min(100, Number(quota.percent || 0)));
    const dashOffset = length - (length * percent / 100);
    const ageTag = quota.source ? `<div class="meter-age">${escapeHTML(quotaAgeLabel(quota))}</div>` : '';
    return `
      <div class="quota-meter status-${severityClass(quota.status)}">
        <div class="meter-shell">
          <svg class="meter-svg" viewBox="0 0 64 64" aria-hidden="true">
            <circle class="meter-track" cx="32" cy="32" r="${radius}"></circle>
            <circle class="meter-ring" cx="32" cy="32" r="${radius}" stroke-dasharray="${length}" stroke-dashoffset="${dashOffset}"></circle>
          </svg>
          <div class="meter-value">${escapeHTML(quota.display_value || `${percent.toFixed(0)}%`)}</div>
        </div>
        <div class="meter-label">${escapeHTML(quota.label)}</div>
        ${ageTag}
      </div>
    `;
  }

  function trendMarkup(series) {
    const points = Array.isArray(series.points) ? series.points : [];
    if (!points.length) {
      return '';
    }
    const max = Math.max(...points, 100);
    const min = Math.min(...points, 0);
    const range = Math.max(max - min, 1);
    const coords = points.map((value, index) => {
      const x = points.length === 1 ? 0 : (index / (points.length - 1)) * 100;
      const y = 22 - (((value - min) / range) * 22);
      return `${x},${y.toFixed(2)}`;
    }).join(' ');
    return `
      <div class="trend-row status-${severityClass(series.status)}">
        <div class="trend-label">${escapeHTML(series.label)}</div>
        <svg class="trend-line" viewBox="0 0 100 24" preserveAspectRatio="none" aria-hidden="true">
          <polyline points="${coords}"></polyline>
        </svg>
      </div>
    `;
  }

  function quotaAgeLabel(quota) {
    const age = quota.age_seconds || 0;
    const src = quota.source === 'statusline' ? 'Live' : 'API';
    if (age < 60) return `${src}`;
    if (age < 3600) return `${src} ${Math.floor(age / 60)}m ago`;
    if (age < 86400) return `${src} ${Math.floor(age / 3600)}h ago`;
    return `${src} ${Math.floor(age / 86400)}d ago`;
  }

  function staleMeterMarkup(quota) {
    const percent = Math.max(0, Math.min(100, Number(quota.percent || 0)));
    const ring = severityClass(quota.status);
    return `
      <div class="stale-meter status-${ring}">
        <div class="stale-meter-ring">
          <svg viewBox="0 0 36 36" aria-hidden="true">
            <circle class="stale-ring-track" cx="18" cy="18" r="14"></circle>
            <circle class="stale-ring-fill" cx="18" cy="18" r="14"
              stroke-dasharray="${2 * Math.PI * 14}"
              stroke-dashoffset="${2 * Math.PI * 14 * (1 - percent / 100)}"></circle>
          </svg>
          <span class="stale-meter-pct">${percent.toFixed(0)}%</span>
        </div>
        <div class="stale-meter-info">
          <span class="stale-meter-name">${escapeHTML(quota.label)}</span>
          <span class="stale-meter-age">${escapeHTML(quotaAgeLabel(quota))}</span>
        </div>
      </div>
    `;
  }

  function staleCardMarkup(staleQuotas) {
    if (!staleQuotas.length) return '';
    // Find the most recent age among supplementary quotas for the header
    const maxAge = Math.max(...staleQuotas.map(q => q.age_seconds || 0));
    let ageHint = 'from last API poll';
    if (maxAge > 0 && maxAge < 3600) ageHint = `updated ${Math.floor(maxAge / 60)}m ago`;
    else if (maxAge >= 3600) ageHint = `updated ${Math.floor(maxAge / 3600)}h ago`;
    return `
      <div class="stale-card">
        <div class="stale-card-header">
          <span class="stale-card-icon">\uD83D\uDD0D</span>
          <span class="stale-card-title">Supplementary</span>
          <span class="stale-card-hint">${escapeHTML(ageHint)}</span>
        </div>
        <div class="stale-card-meters">${staleQuotas.map(staleMeterMarkup).join('')}</div>
      </div>
    `;
  }

  function providerCardMarkup(provider, view) {
    const allQuotas = provider.quotas || [];
    // Split by source: statusline (live) quotas as primary meters,
    // API-sourced quotas in a separate supplementary card.
    // For non-Anthropic providers (no source field), all quotas are primary.
    const hasSourceInfo = allQuotas.some(q => q.source);
    const freshQuotas = hasSourceInfo
      ? allQuotas.filter(q => q.source !== 'api')
      : allQuotas;
    const staleQuotas = hasSourceInfo
      ? allQuotas.filter(q => q.source === 'api')
      : [];
    const freshMarkup = freshQuotas.map(meterMarkup).join('');
    const showTrends = view === 'detailed';
    const trends = showTrends ? (provider.trends || []).map(trendMarkup).join('') : '';
    const percent = Number(provider.highest_percent || 0).toFixed(0);
    const meta = [
      provider.subtitle,
      provider.updated_at ? `Updated ${provider.updated_at}` : '',
    ].filter(Boolean).join(' \u00B7 ');
    return `
      <details class="provider-card status-${severityClass(provider.status)}" data-view="${escapeHTML(view)}" ${view === 'detailed' ? 'open' : ''}>
        <summary>
          <div>
            <div class="provider-name-row">
              <span class="menubar-status-dot"></span>
              <span class="provider-name">${escapeHTML(provider.label)}</span>
            </div>
            ${meta ? `<div class="provider-meta">${escapeHTML(meta)}</div>` : ''}
          </div>
          <div class="provider-percent">${percent}%</div>
        </summary>
        <div class="provider-body">
          <div class="provider-quotas">${freshMarkup || '<div class="provider-empty">No quota data available yet.</div>'}</div>
          ${staleCardMarkup(staleQuotas)}
          ${trends ? `<div class="provider-trends">${trends}</div>` : ''}
        </div>
      </details>
    `;
  }

  function aggregateLabel(snapshot) {
    if (snapshot.aggregate && snapshot.aggregate.label) {
      return snapshot.aggregate.label;
    }
    return 'Watching your active providers';
  }

  function summaryMarkup(snapshot, providers) {
    const status = severityClass(snapshot.aggregate && snapshot.aggregate.status);
    const aggregate = snapshot.aggregate || {};
    return `
      <section class="menubar-summary">
        <div class="aggregate-card status-${status}">
          <div class="aggregate-percent">${Number(aggregate.highest_percent || 0).toFixed(0)}%</div>
          <div class="aggregate-label">${escapeHTML(snapshot.updated_ago || 'Waiting for quota data')}</div>
        </div>
        <div class="aggregate-stats">
          <div class="aggregate-stat-list">
            <div class="aggregate-stat">
              <strong>${escapeHTML(String(aggregate.provider_count || providers.length || 0))}</strong>
              <span class="aggregate-label">Providers</span>
            </div>
            <div class="aggregate-stat">
              <strong>${escapeHTML(String(aggregate.warning_count || 0))}</strong>
              <span class="aggregate-label">Warnings</span>
            </div>
            <div class="aggregate-stat">
              <strong>${escapeHTML(String(aggregate.critical_count || 0))}</strong>
              <span class="aggregate-label">Critical</span>
            </div>
          </div>
        </div>
      </section>
    `;
  }

  function minimalMarkup(snapshot, providers) {
    const aggregate = snapshot.aggregate || {};
    const status = severityClass(aggregate.status);
    return `
      <section class="minimal-view">
        <div class="aggregate-circle status-${status}">
          <span class="aggregate-percent">${Number(aggregate.highest_percent || 0).toFixed(0)}%</span>
        </div>
        <div class="aggregate-label">${escapeHTML(snapshot.updated_ago || 'Waiting for quota data')}</div>
        <div class="aggregate-status status-${status}">${escapeHTML(aggregate.label || 'All Good')}</div>
        <div class="minimal-stats">
          <span>${escapeHTML(String(aggregate.provider_count || providers.length || 0))} providers</span>
          <span>${escapeHTML(String(aggregate.warning_count || 0))} warnings</span>
          <span>${escapeHTML(String(aggregate.critical_count || 0))} critical</span>
        </div>
      </section>
    `;
  }

  function providerListMarkup(providers, view) {
    if (!providers.length) {
      return '<section class="provider-list"><div class="provider-empty-state">No provider quota data is available yet.</div></section>';
    }
    return `
      <section class="provider-list">
        ${providers.map((provider) => providerCardMarkup(provider, view)).join('')}
      </section>
    `;
  }

  function footerMarkup() {
    return `
      <footer class="menubar-footer" id="footer">
        <div class="menubar-subtitle">Status is computed from the highest-pressure quota in each provider card.</div>
      </footer>
    `;
  }

  function render(snapshot, settings) {
    const root = document.getElementById('menubar-root');
    const status = severityClass(snapshot.aggregate && snapshot.aggregate.status);
    const providers = Array.isArray(snapshot.providers) ? snapshot.providers : [];
    const view = settings.default_view || bridge.requestedView || 'standard';
    let bodyMarkup = '';
    if (view === 'minimal') {
      bodyMarkup = minimalMarkup(snapshot, providers);
    } else if (view === 'detailed') {
      bodyMarkup = summaryMarkup(snapshot, providers) + providerListMarkup(providers, view);
    } else {
      bodyMarkup = summaryMarkup(snapshot, providers) + providerListMarkup(providers, view);
    }

    root.innerHTML = `
      <section class="menubar-panel menubar-view menubar-view-${escapeHTML(view)}">
        <header class="menubar-header status-${status}">
          <div>
            <div class="menubar-title">onWatch</div>
            <div class="menubar-subtitle">${escapeHTML(aggregateLabel(snapshot))}</div>
          </div>
          <div class="menubar-status-badge">
            <span class="menubar-status-dot"></span>
            <span>${escapeHTML((snapshot.aggregate && snapshot.aggregate.label) || 'All Good')}</span>
          </div>
        </header>
        ${bodyMarkup}
        ${footerMarkup()}
      </section>
    `;

    root.querySelectorAll('[data-external="true"]').forEach((el) => {
      el.addEventListener('click', (event) => {
        event.preventDefault();
        bridge.openExternal(el.dataset.url);
      });
    });
  }

  function renderError(error) {
    const root = document.getElementById('menubar-root');
    root.innerHTML = `
      <section class="menubar-panel menubar-error">
        <div>
          <div>Menubar data is temporarily unavailable.</div>
          <div class="menubar-subtitle">${escapeHTML(error && error.message ? error.message : 'Unable to reach the local onWatch data source.')}</div>
          <button type="button" id="menubar-retry">Retry</button>
        </div>
      </section>
    `;
    const retry = document.getElementById('menubar-retry');
    if (retry) {
      retry.addEventListener('click', () => init());
    }
  }

  let refreshTimer = null;

  async function init() {
    const settings = await bridge.getSettings();
    try {
      const snapshot = await bridge.getSnapshot();
      render(snapshot, settings || {});
      const intervalSeconds = Number(settings && settings.refresh_seconds ? settings.refresh_seconds : 60);
      if (refreshTimer) {
        clearInterval(refreshTimer);
      }
      refreshTimer = setInterval(async () => {
        try {
          const nextSnapshot = await bridge.getSnapshot();
          render(nextSnapshot, settings || {});
        } catch (error) {
          renderError(error);
        }
      }, Math.max(intervalSeconds, 10) * 1000);
    } catch (error) {
      renderError(error);
    }
  }

  document.addEventListener('DOMContentLoaded', init);
}());
