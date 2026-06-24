// Debug helper: screenshot the live onWatch dashboard via Chrome CDP.
// Inspectable, no external deps (Node 25 global WebSocket + fetch).
// Usage: node scripts/shot/shot.js <sessionToken> <outDir>
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const PORT = 9333;
const BASE = 'http://localhost:9211';
const token = process.argv[2];
const outDir = process.argv[3] || '.';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let msgId = 0;
function cdpCall(ws, method, params) {
  return new Promise((resolve, reject) => {
    const id = ++msgId;
    const onMsg = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch { return; }
      if (m.id === id) {
        ws.removeEventListener('message', onMsg);
        if (m.error) reject(new Error(method + ': ' + JSON.stringify(m.error)));
        else resolve(m.result);
      }
    };
    ws.addEventListener('message', onMsg);
    ws.send(JSON.stringify({ id, method, params: params || {} }));
  });
}

async function shot(ws, urlPath, file, waitMs) {
  await cdpCall(ws, 'Page.navigate', { url: BASE + urlPath });
  await sleep(waitMs || 4500);
  const res = await cdpCall(ws, 'Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  fs.writeFileSync(path.join(outDir, file), Buffer.from(res.data, 'base64'));
  console.log('saved', file);
}

(async () => {
  const userDir = path.join(outDir, 'chrome-prof');
  const proc = spawn(CHROME, [
    '--headless=new',
    '--remote-debugging-port=' + PORT,
    '--user-data-dir=' + userDir,
    '--no-first-run', '--no-default-browser-check',
    '--window-size=1400,2200',
    'about:blank',
  ], { stdio: 'ignore' });

  // wait for devtools endpoint
  let wsUrl = null;
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch('http://localhost:' + PORT + '/json/version');
      const j = await r.json();
      wsUrl = j.webSocketDebuggerUrl;
      if (wsUrl) break;
    } catch {}
    await sleep(250);
  }
  if (!wsUrl) { console.error('no devtools endpoint'); proc.kill(); process.exit(1); }

  const ws = new WebSocket(wsUrl);
  await new Promise((res) => ws.addEventListener('open', res, { once: true }));

  // create a page target and attach
  const { targetId } = await cdpCall(ws, 'Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdpCall(ws, 'Target.attachToTarget', { targetId, flatten: true });
  // wrap calls with sessionId by re-sending; simplest: connect directly to the page ws
  const listRes = await (await fetch('http://localhost:' + PORT + '/json')).json();
  const pageTarget = listRes.find((t) => t.id === targetId);
  ws.close();

  const pws = new WebSocket(pageTarget.webSocketDebuggerUrl);
  await new Promise((res) => pws.addEventListener('open', res, { once: true }));

  await cdpCall(pws, 'Network.enable');
  await cdpCall(pws, 'Page.enable');
  await cdpCall(pws, 'Runtime.enable');
  await cdpCall(pws, 'Network.setCookie', {
    name: 'onwatch_session', value: token, domain: 'localhost', path: '/', httpOnly: true,
  });

  // capture page console errors + failed api responses
  pws.addEventListener('message', (ev) => {
    let m; try { m = JSON.parse(ev.data); } catch { return; }
    if (m.method === 'Runtime.consoleAPICalled' && (m.params.type === 'error' || m.params.type === 'warning')) {
      const txt = (m.params.args || []).map((a) => a.value || a.description || '').join(' ');
      console.log('[console.' + m.params.type + ']', txt.slice(0, 300));
    }
    if (m.method === 'Runtime.exceptionThrown') {
      const d = m.params.exceptionDetails;
      console.log('[exception]', (d.exception && (d.exception.description || d.exception.value)) || d.text);
    }
    if (m.method === 'Network.responseReceived') {
      const r = m.params.response;
      if (r.url.includes('/api/') && r.status >= 400) console.log('[api ' + r.status + ']', r.url);
    }
  });

  await shot(pws, '/?provider=both', 'all-tab.png', 14000);
  await shot(pws, '/?provider=antigravity', 'antigravity.png', 14000);
  await shot(pws, '/?provider=gemini', 'gemini.png', 14000);

  pws.close();
  proc.kill();
  console.log('done');
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
