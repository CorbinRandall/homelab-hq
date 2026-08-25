let CONFIG = {
  siteName: 'Homelab HQ',
  hostIp: 'Unraid',
  unraidUrl: '',
  headerLinks: [],
  pollIntervalMs: 30000,
};

const dot = document.getElementById('dot');
const statusText = document.getElementById('statusText');
const wakeBtn = document.getElementById('wakeBtn');
const startArrayBtn = document.getElementById('startArrayBtn');
const sleepBtn = document.getElementById('sleepBtn');
const shutdownBtn = document.getElementById('shutdownBtn');
const refreshBtn = document.getElementById('refreshBtn');
const arrayProgress = document.getElementById('arrayProgress');
const arrayProgressTitle = document.getElementById('arrayProgressTitle');
const arrayProgressDetail = document.getElementById('arrayProgressDetail');
const plugDot = document.getElementById('plugDot');
const plugStatusText = document.getElementById('plugStatusText');
const plugOnBtn = document.getElementById('plugOnBtn');
const plugOffBtn = document.getElementById('plugOffBtn');
const plugCycleBtn = document.getElementById('plugCycleBtn');
const toast = document.getElementById('toast');
const grid = document.getElementById('grid');
const hiddenSection = document.getElementById('hiddenSection');
const hiddenList = document.getElementById('hiddenList');
const subline = document.getElementById('subline');
const footer = document.getElementById('footer');

let lastOnline = null;
let hidden = {};
let wakeRefreshTimer = null;
let workflowActive = false;
let dashboardPollTimer = null;

function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

async function loadPublicConfig() {
  const r = await fetch('/api/config', { cache: 'no-store' });
  if (!r.ok) throw new Error('configuration unavailable');
  const config = await r.json();
  CONFIG = {
    siteName: config.site_name || 'Homelab HQ',
    hostIp: config.unraid_hostname || 'Unraid',
    unraidUrl: config.unraid_url || '',
    headerLinks: Array.isArray(config.header_links) ? config.header_links : [],
    pollIntervalMs: Number(config.poll_interval_ms) || 30000,
  };
  document.title = CONFIG.siteName;
  document.getElementById('siteTitle').textContent = CONFIG.siteName;
  document.getElementById('hostLabel').textContent = `· ${CONFIG.hostIp}`;
  footer.textContent = `Apps from the cached Unraid scan. Power controls act on ${CONFIG.hostIp}.`;

  const links = [...CONFIG.headerLinks];
  if (CONFIG.unraidUrl) links.push({ label: 'Unraid WebUI', url: CONFIG.unraidUrl });
  document.getElementById('headerLinks').innerHTML = links
    .filter((link) => link && link.label && /^https?:\/\//i.test(link.url || ''))
    .map((link, index) => `<a class="header-link ${index ? 'secondary' : ''}" href="${esc(link.url)}" target="_blank" rel="noopener">${esc(link.label)}</a>`)
    .join('');
}

function showToast(msg) {
  toast.textContent = msg;
  toast.classList.add('show');
  setTimeout(() => toast.classList.remove('show'), 3000);
}

function formatUpdated(iso) {
  if (!iso) return 'not yet';
  try {
    const d = new Date(iso);
    return d.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    });
  } catch {
    return iso;
  }
}

function linkButtons(urls) {
  if (!urls || urls.length === 0) return '';
  if (urls.length === 1) {
    const href = esc(urls[0]);
    return `<div class="link-buttons"><a class="link-btn" href="${href}" target="_blank" rel="noopener">Open</a></div>`;
  }
  return urls.slice(0, 2)
    .map((url, index) => {
      const href = esc(url);
      const label = esc(url.replace(/^https?:\/\//, ''));
      const kind = index === 0 ? 'MagicDNS' : 'LAN';
      return `<div class="url-row"><span class="url-label"><strong>${kind}</strong> · ${label}</span><div class="link-buttons"><a class="link-btn" href="${href}" target="_blank" rel="noopener">Open</a></div></div>`;
    })
    .join('');
}

function renderApps(appsData) {
  const apps = appsData.apps || [];
  const updated = formatUpdated(appsData.updated);
  const visible = apps.filter((a) => !hidden[a.raw_name]);

  const stale = appsData.stale ? ' · cached (scan failed)' : '';
  const err = appsData.error ? ' · scan error' : '';
  subline.textContent = `${visible.length} apps · Last updated ${updated}${stale}${err}`;

  if (visible.length === 0) {
    grid.innerHTML = '<p class="empty">No visible apps right now.</p>';
  } else {
    grid.innerHTML = visible
      .map((it) => `
        <div class="card-wrap">
          <div class="card up">
            <div class="name">${esc(it.name)}</div>
            <div class="meta">${esc(it.image)}</div>
            ${linkButtons(it.urls)}
          </div>
          <div class="card-actions">
            <button type="button" class="btn-rename" data-container="${esc(it.raw_name)}" data-name="${esc(it.name)}" title="Rename display name">Rename</button>
            <button type="button" class="btn-hide" data-container="${esc(it.raw_name)}" title="Hide from list">Hide</button>
          </div>
        </div>`)
      .join('');
    grid.querySelectorAll('.btn-hide').forEach((btn) => {
      btn.addEventListener('click', () => hideApp(btn.dataset.container));
    });
    grid.querySelectorAll('.btn-rename').forEach((btn) => {
      btn.addEventListener('click', () => renameApp(btn.dataset.container, btn.dataset.name));
    });
  }

  const hiddenKeys = Object.keys(hidden).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
  if (hiddenKeys.length === 0) {
    hiddenSection.hidden = true;
    hiddenList.innerHTML = '';
  } else {
    hiddenSection.hidden = false;
    hiddenList.innerHTML = hiddenKeys
      .map((raw) => {
        const h = hidden[raw];
        return `
          <div class="hidden-row">
            <span class="hidden-name">${esc(h.name || raw)}</span>
            <span class="hidden-meta">${esc(h.url || '')}</span>
            <button type="button" class="btn-rename" data-container="${esc(raw)}" data-name="${esc(h.name || raw)}">Rename</button>
            <button type="button" class="btn-show" data-container="${esc(raw)}">Show</button>
          </div>`;
      })
      .join('');
    hiddenList.querySelectorAll('.btn-show').forEach((btn) => {
      btn.addEventListener('click', () => unhideApp(btn.dataset.container));
    });
    hiddenList.querySelectorAll('.btn-rename').forEach((btn) => {
      btn.addEventListener('click', () => renameApp(btn.dataset.container, btn.dataset.name));
    });
  }
}

async function loadData() {
  await reloadFromServer();
}

function applyStatus(online) {
  dot.className = 'dot ' + (online ? 'on' : 'off');
  statusText.textContent = 'Unraid: ' + (online ? 'Online' : 'Asleep / unresponsive');
  wakeBtn.disabled = false;
  startArrayBtn.disabled = !online;
  sleepBtn.disabled = !online;
  shutdownBtn.disabled = !online;
  refreshBtn.disabled = false;

  if (lastOnline === online) return;

  if (online && !lastOnline) {
    clearTimeout(wakeRefreshTimer);
    wakeRefreshTimer = setTimeout(() => loadData(), 2000);
  } else if (!online && lastOnline) {
    loadData();
  }
  lastOnline = online;
}

async function poll(force) {
  try {
    const url = force ? '/unraid/status?force=1' : '/unraid/status';
    const r = await fetch(url);
    const j = await r.json();
    applyStatus(j.online);
  } catch {
    /* ignore */
  }
}

function applyArrayStatus(j) {
  const workflow = j.workflow || { state: 'idle', message: 'No dashboard start attempt is active' };
  const active = !['idle', 'succeeded', 'failed'].includes(workflow.state);
  workflowActive = active;
  let state = workflow.state;
  const powerAction = (workflow.reason || '').split(':')[0];
  const isPowerOffWorkflow = powerAction === 'shutdown' || powerAction === 'sleep';
  let title = isPowerOffWorkflow
    ? `Server: ${j.online ? (powerAction === 'shutdown' ? 'Shutting down' : 'Going to sleep') : 'Offline'}`
    : `Array: ${j.array_state || 'Unknown'}`;
  let detail = workflow.message || '';
  if (!active && !isPowerOffWorkflow) {
    if (j.array_state === 'Started') {
      state = 'succeeded';
      detail = workflow.state === 'succeeded' ? workflow.message : 'No dashboard start attempt is active';
    } else if (j.array_state === 'Starting') {
      state = 'array_starting';
      detail = 'Unraid is starting it; no dashboard retry workflow is active';
    } else if (!j.online) {
      state = 'idle';
      detail = 'Server is offline; no dashboard start attempt is active';
    }
  }
  if (workflow.attempts > 0 && active) detail += ` · attempt ${workflow.attempts}`;
  arrayProgress.dataset.state = state;
  arrayProgressTitle.textContent = title;
  arrayProgressDetail.textContent = detail ? ` — ${detail}` : '';
}

async function pollArrayStatus() {
  try {
    const r = await fetch('/unraid/array-status', { cache: 'no-store' });
    applyArrayStatus(await r.json());
  } catch {
    arrayProgress.dataset.state = 'failed';
    arrayProgressTitle.textContent = 'Array: status unavailable';
    arrayProgressDetail.textContent = ' — dashboard could not read workflow status';
  }
}

async function ctl(action) {
  const verb = ({wake:'Waking',sleep:'Sleeping',start:'Starting array',shutdown:'Shutting down'})[action] || 'Working';
  showToast(verb + ' Unraid…');
  try {
    const r = await fetch('/unraid/' + action, { method: 'POST' });
    const j = await r.json();
    if (!j.ok) {
      showToast(j.error || `${verb} command failed`);
      return;
    }
  } catch {
    /* ignore */
  }
  setTimeout(() => poll(true), 1500);
  setTimeout(pollArrayStatus, 250);
  if (action === 'wake' || action === 'start') {
    for (const ms of [5000, 12000, 22000, 40000]) {
      setTimeout(() => poll(true), ms);
    }
    // The server refreshes discovery 40 seconds after the array reaches
    // Started. Re-read its cache during the boot window so the page updates
    // without requiring a manual Refresh apps click.
    for (const ms of [60000, 120000, 240000, 360000]) {
      setTimeout(() => loadData(), ms);
    }
  } else {
    for (const ms of [4000, 8000, 15000, 30000, 60000, 120000, 240000, 300000]) {
      setTimeout(() => {
        poll(true);
        pollArrayStatus();
      }, ms);
    }
    // Sleep and Shutdown refresh discovery on the server after 40 seconds.
    // Re-read the cache automatically so the offline state appears without
    // requiring the Refresh apps button.
    for (const ms of [45000, 60000, 90000]) {
      setTimeout(() => loadData(), ms);
    }
  }
}

async function refreshApps(opts = {}) {
  const silent = !!opts.silent;
  if (!silent) showToast('Refreshing app list…');
  try {
    await fetch('/api/refresh', { method: 'POST' });
    const delays = opts.delays || [3000, 10000];
    delays.forEach((ms) => setTimeout(loadData, ms));
  } catch {
    if (!silent) showToast('Refresh failed');
  }
}

async function pollPlug() {
  try {
    const r = await fetch('/plug/status');
    const j = await r.json();
    applyPlugStatus(j);
  } catch {
    plugStatusText.textContent = 'Shelly plug: unreachable';
    plugOnBtn.disabled = true;
    plugOffBtn.disabled = true;
    plugCycleBtn.disabled = true;
  }
}

function applyPlugStatus(j) {
  if (!j.configured) {
    plugDot.className = 'dot off';
    plugStatusText.textContent = 'Shelly plug: not configured';
    plugOnBtn.disabled = true;
    plugOffBtn.disabled = true;
    plugCycleBtn.disabled = true;
    return;
  }
  if (j.error) {
    plugDot.className = 'dot off';
    plugStatusText.textContent = 'Shelly plug: error';
    plugOnBtn.disabled = false;
    plugOffBtn.disabled = false;
    plugCycleBtn.disabled = false;
    return;
  }
  const on = !!j.on;
  plugDot.className = 'dot ' + (on ? 'on' : 'off');
  plugStatusText.textContent = 'Shelly: ' + (on ? 'On' : 'Off');
  plugOnBtn.disabled = on;
  plugOffBtn.disabled = !on;
  plugCycleBtn.disabled = false;
}

async function plugCtl(action) {
  const labels = { on: 'Turning plug on…', off: 'Turning plug off…', cycle: 'Cycling plug power…' };
  showToast(labels[action] || 'Plug…');
  plugOnBtn.disabled = true;
  plugOffBtn.disabled = true;
  plugCycleBtn.disabled = true;
  try {
    const r = await fetch('/plug/' + action, { method: 'POST' });
    const j = await r.json();
    if (!j.ok && j.error) showToast(j.error);
  } catch {
    showToast('Plug command failed');
  }
  setTimeout(pollPlug, 1500);
}

async function hideApp(container) {
  const body = new URLSearchParams({ container });
  try {
    await fetch('/hide', { method: 'POST', body });
    await reloadFromServer();
  } catch {
    showToast('Hide failed');
  }
}

async function unhideApp(container) {
  const body = new URLSearchParams({ container });
  try {
    await fetch('/unhide', { method: 'POST', body });
    await reloadFromServer();
  } catch {
    showToast('Show failed');
  }
}

async function renameApp(container, currentName) {
  const next = prompt(
    'Display name for this app (clear to reset to auto name):',
    currentName
  );
  if (next === null) return;
  const trimmed = next.trim();
  if (trimmed === currentName) return;
  try {
    const r = await fetch('/rename', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ container, name: trimmed }),
    });
    const j = await r.json();
    if (!j.ok) {
      showToast(j.error || 'Rename failed');
      return;
    }
    showToast(trimmed ? 'Renamed' : 'Resetting name…');
    await reloadFromServer();
    if (!trimmed) setTimeout(loadData, 3000);
  } catch {
    showToast('Rename failed');
  }
}

async function reloadFromServer() {
  const [appsRes, hiddenRes] = await Promise.all([
    fetch('/data/apps.json', { cache: 'no-store' }),
    fetch('/data/hidden.json', { cache: 'no-store' }),
  ]);
  const appsData = appsRes.ok ? await appsRes.json() : { apps: [], updated: null };
  hidden = hiddenRes.ok ? await hiddenRes.json() : {};
  if (typeof hidden !== 'object') hidden = {};
  renderApps(appsData);
  if (lastOnline === null && typeof appsData.online === 'boolean') {
    applyStatus(appsData.online);
  }
}

wakeBtn.addEventListener('click', () => ctl('wake'));
startArrayBtn.addEventListener('click', () => ctl('start'));
sleepBtn.addEventListener('click', () => ctl('sleep'));
shutdownBtn.addEventListener('click', () => ctl('shutdown'));
refreshBtn.addEventListener('click', refreshApps);
plugOnBtn.addEventListener('click', () => plugCtl('on'));
plugOffBtn.addEventListener('click', () => plugCtl('off'));
plugCycleBtn.addEventListener('click', () => plugCtl('cycle'));

async function startDashboard() {
  try {
    await loadPublicConfig();
  } catch {
    footer.textContent = 'Dashboard configuration could not be loaded.';
  }
  loadData();
  runDashboardPoll();
}

async function runDashboardPoll() {
  clearTimeout(dashboardPollTimer);
  if (document.hidden) return;
  await Promise.all([poll(false), pollArrayStatus(), pollPlug()]);
  dashboardPollTimer = setTimeout(runDashboardPoll, workflowActive ? 3000 : CONFIG.pollIntervalMs);
}

document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    clearTimeout(dashboardPollTimer);
  } else {
    runDashboardPoll();
  }
});

startDashboard();
