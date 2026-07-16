/*
 * Space Data Network node console — vanilla JS, no build step, no framework.
 *
 * Faithful implementation of the SpaceAware.io "SDN Console" design system
 * (design/sdn_design). The shell (rail + header + tokens) lives in index.html /
 * styles.css; this file routes the six rail screens and wires each to the
 * node's OWN same-origin read-only API under /sdn/v1/* (served by the sdnapi
 * plugin on the same loopback listener as this page). There are NO
 * external-origin requests and NO fixture data: empty results render as the
 * design's explicit state language, never as fake rows.
 *
 * The Modules screen additionally issues a mutating PUT /sdn/v1/modules/{id}/config
 * to edit a module's cron schedule and reflects the applied result.
 */

const API_BASE = '/sdn/v1';

const routes = ['node', 'peers', 'data', 'channels', 'apps', 'modules'];
const titles = {
  node: 'NODE',
  peers: 'PEERS',
  data: 'DATA',
  channels: 'CHANNELS',
  apps: 'APPS',
  modules: 'MODULES',
};
const subs = {
  node: '· LOCAL NODE',
  peers: '· DISCOVERY & TRUST',
  data: '· LOCAL FLATSQL STORE',
  channels: '· PUBSUB FAN-OUT',
  apps: '· INSTALLED $APP RECORDS',
  modules: '· CRON SCHEDULER',
};

const state = {
  route: normalizeRoute(location.hash),
  selectedSource: null, // {source, type} row selected on the Data screen
  outputMode: 'table',
  selectedModule: null, // module id whose settings drawer is open
  moduleNotice: null, // {module, kind:'good'|'bad', text} after a config PUT
};

const screen = document.querySelector('#screen');
const screenTitle = document.querySelector('#screen-title');
const screenSub = document.querySelector('#screen-sub');
const statusStrip = document.querySelector('#status-strip');

// ---------------------------------------------------------------------------
// escaping
// ---------------------------------------------------------------------------

function escapeHtml(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function escapeAttribute(value) {
  return escapeHtml(value).replaceAll('`', '&#96;');
}

// ---------------------------------------------------------------------------
// fetch helpers — resolve to a tagged result, never throw to the caller.
// ---------------------------------------------------------------------------

async function fetchJSON(path) {
  try {
    const res = await fetch(API_BASE + path, { headers: { Accept: 'application/json' } });
    if (!res.ok) {
      return { ok: false, status: res.status, error: `HTTP ${res.status}` };
    }
    const data = await res.json();
    return { ok: true, status: res.status, data };
  } catch (err) {
    return { ok: false, status: 0, error: String(err && err.message ? err.message : err) };
  }
}

async function putJSON(path, body) {
  try {
    const res = await fetch(API_BASE + path, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
    });
    let data = null;
    try { data = await res.json(); } catch { data = null; }
    if (!res.ok) {
      const msg = data && data.error ? data.error : `HTTP ${res.status}`;
      return { ok: false, status: res.status, error: msg };
    }
    return { ok: true, status: res.status, data };
  } catch (err) {
    return { ok: false, status: 0, error: String(err && err.message ? err.message : err) };
  }
}

// ---------------------------------------------------------------------------
// shared state-language blocks (design idiom, never fake data)
// ---------------------------------------------------------------------------

function loadingBlock(label) {
  return `<div class="state-block loading"><strong><span class="spinner"></span>LOADING</strong><span>${escapeHtml(label)}</span></div>`;
}

function emptyBlock(title, detail) {
  return `<div class="state-block"><strong>${escapeHtml(title)}</strong><span>${escapeHtml(detail)}</span></div>`;
}

function unavailableBlock(detail) {
  return `<div class="state-block"><strong class="state-unavailable">UNAVAILABLE</strong><span>${escapeHtml(detail)}</span></div>`;
}

// ---------------------------------------------------------------------------
// interval formatting (module cron schedules)
// ---------------------------------------------------------------------------

function humanMs(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0) return '—';
  if (n % 3600000 === 0) return `${n / 3600000}h`;
  if (n % 60000 === 0) return `${n / 60000}m`;
  if (n % 1000 === 0) return `${n / 1000}s`;
  return `${n}ms`;
}

// ---------------------------------------------------------------------------
// routing / shell
// ---------------------------------------------------------------------------

function normalizeRoute(hash) {
  const route = String(hash || '#node').replace(/^#\/?/, '');
  return routes.includes(route) ? route : 'node';
}

function navigate(route) {
  location.hash = `#/${route}`;
}

function render() {
  document.querySelectorAll('[data-route]').forEach((button) => {
    button.classList.toggle('active', button.dataset.route === state.route);
  });
  screenTitle.textContent = titles[state.route];
  screenSub.textContent = subs[state.route] || '';
  screen.dataset.screen = state.route;
  renderCurrentScreen();
}

function renderCurrentScreen() {
  if (state.route === 'peers') return renderPeers();
  if (state.route === 'data') return renderData();
  if (state.route === 'channels') return renderChannels();
  if (state.route === 'apps') return renderApps();
  if (state.route === 'modules') return renderModules();
  return renderNode();
}

// ---------------------------------------------------------------------------
// top status strip — driven by /node and /peers
// ---------------------------------------------------------------------------

async function refreshStatusStrip() {
  const [node, peers] = await Promise.all([fetchJSON('/node'), fetchJSON('/peers')]);
  if (!node.ok) {
    statusStrip.innerHTML = `<span class="status-chip bad"><span class="dot"></span>NODE UNAVAILABLE</span>`;
    return;
  }
  const chips = [`<span class="status-chip good"><span class="dot"></span>ONLINE</span>`];
  const sdnCount = peers.ok && Array.isArray(peers.data.sdn) ? peers.data.sdn.length : 0;
  chips.push(`<span class="status-chip" data-goto="peers" title="View peers">${sdnCount} SDN ${sdnCount === 1 ? 'PEER' : 'PEERS'}</span>`);
  const pubsub = node.data.pubsub_enabled;
  chips.push(pubsub
    ? `<span class="status-chip good"><span class="dot"></span>PUBSUB ONLINE</span>`
    : `<span class="status-chip warn"><span class="dot"></span>PUBSUB OFF</span>`);
  statusStrip.innerHTML = chips.join('');
  statusStrip.querySelectorAll('[data-goto]').forEach((el) => {
    el.style.cursor = 'pointer';
    el.addEventListener('click', () => navigate(el.dataset.goto));
  });
}

// ---------------------------------------------------------------------------
// Node screen — /sdn/v1/node + /sdn/v1/peers
// ---------------------------------------------------------------------------

async function renderNode() {
  screen.innerHTML = loadingBlock('reading node identity and health');
  const [res, peersRes] = await Promise.all([fetchJSON('/node'), fetchJSON('/peers')]);
  if (state.route !== 'node') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`The node HTTP API did not respond (${escapeHtml(res.error)}). Is the daemon running?`);
    return;
  }
  const node = res.data;
  const storage = node.storage || {};
  const sources = storage.sources ?? 0;
  const records = typeof storage.records === 'number' ? storage.records.toLocaleString() : 'not counted';
  const sdn = peersRes.ok && Array.isArray(peersRes.data.sdn) ? peersRes.data.sdn : [];
  const ipfs = peersRes.ok && Array.isArray(peersRes.data.ipfs) ? peersRes.data.ipfs : [];

  screen.innerHTML = `
    <div class="screen-kicker"><span>DASHBOARD</span><span class="spring"></span><span class="hint">read-only · same-origin /sdn/v1</span></div>
    <div class="grid">

      <section class="panel ticked span-4"><span class="tick-b"></span>
        <div class="panel-kicker">NODE HEALTH</div>
        <div class="readout"><span class="dot" style="color:#5ad6a0"></span><span class="big">ONLINE</span></div>
        <div class="readout-sub">MODE · DESKTOP-LOCAL</div>
        <dl class="dl">
          <div class="row"><dt>PEER ID</dt><dd class="mono wrap-any">${escapeHtml(node.peer_id || 'unknown')}</dd></div>
          <div class="row"><dt>PUBSUB</dt><dd>${node.pubsub_enabled ? '<span class="chip good">ONLINE</span>' : '<span class="chip warn">OFF</span>'}</dd></div>
          <div class="row"><dt>API</dt><dd class="mono">127.0.0.1:5020 · /sdn/v1</dd></div>
        </dl>
      </section>

      <section class="panel span-4">
        <div class="panel-head"><span class="panel-kicker">IDENTITY</span><span class="chip good">CONFIRMED</span></div>
        <div class="readout"><span class="big md">SDN OPERATOR</span></div>
        <div class="readout-sub">libp2p identity · self-issued</div>
        <dl class="dl">
          <div class="row"><dt>SDN FLAG NAMESPACE</dt><dd class="mono wrap-any">${escapeHtml(node.sdn_flag_namespace || 'unset')}</dd></div>
        </dl>
        <div class="divider"><span class="lbl2">EXPORT</span><span class="rule"></span></div>
        <div class="actions">
          <button class="btn" id="exp-epm" type="button" title="Show the node's signed EPM (JSON)">EPM</button>
          <button class="btn" id="exp-vcard" type="button" title="Download the node vCard (.vcf)">vCARD</button>
          <button class="btn" id="exp-qr" type="button" title="Render the node EPM QR code">QR</button>
        </div>
        <p class="p-note">The node's self-signed <span class="mono">$EPM</span> record, built from its libp2p identity. Served by this node · same-origin.</p>
        <div id="export-out" class="export-out" hidden></div>
      </section>

      <section class="panel span-4">
        <div class="panel-kicker">STORAGE · FLATSQL</div>
        <div class="readout"><span class="metric-num">${escapeHtml(String(sources))}</span><span style="font-size:13px;color:#7d929b;">SOURCE ${sources === 1 ? 'PAIR' : 'PAIRS'}</span></div>
        <div class="meter"><span style="width:${sources > 0 ? Math.min(100, sources * 8) : 2}%"></span></div>
        <dl class="dl" style="margin-top:12px">
          <div class="row"><dt>RECORDS</dt><dd>${escapeHtml(records)}</dd></div>
          <div class="row"><dt>STORE</dt><dd>${sources > 0 ? '<span class="chip good">POPULATED</span>' : '<span class="chip">EMPTY</span>'}</dd></div>
        </dl>
        <p class="p-note">Distinct (source, 3-letter standard) pairs in the local FlatSQL store.</p>
      </section>

      <section class="panel ticked span-12" style="--tick:#35c9d8"><span class="tick-b"></span>
        <div class="panel-head"><span class="panel-kicker">PEER SUMMARY</span>
          <span class="chip cyan">${sdn.length} SDN · ${ipfs.length} SWARM</span>
        </div>
        ${peerSummary(sdn, ipfs)}
      </section>

    </div>
  `;
  screen.querySelectorAll('[data-goto]').forEach((el) => el.addEventListener('click', () => navigate(el.dataset.goto)));
  wireIdentityExport();
}

// ---------------------------------------------------------------------------
// Identity export — EPM (JSON), vCard (.vcf download), QR (inline PNG). Every
// request is same-origin to this node's own /sdn/v1/node/* endpoints; the QR is
// generated by the node, never a remote chart service.
// ---------------------------------------------------------------------------

const EPM_JSON_URL = '/sdn/v1/node/epm?format=json';
const VCARD_URL = '/sdn/v1/node/vcard';
const QR_URL = '/sdn/v1/node/qr';

function wireIdentityExport() {
  const out = document.querySelector('#export-out');
  const epmBtn = document.querySelector('#exp-epm');
  const vcardBtn = document.querySelector('#exp-vcard');
  const qrBtn = document.querySelector('#exp-qr');
  if (!out) return;

  if (epmBtn) epmBtn.addEventListener('click', () => showNodeEPM(out));
  if (vcardBtn) vcardBtn.addEventListener('click', () => downloadPath(VCARD_URL, 'sdn-node.vcf'));
  if (qrBtn) qrBtn.addEventListener('click', () => showNodeQR(out));
}

async function showNodeEPM(out) {
  out.hidden = false;
  out.innerHTML = loadingBlock('reading the node EPM');
  let res;
  try {
    res = await fetch(EPM_JSON_URL, { headers: { Accept: 'application/json' } });
  } catch (err) {
    out.innerHTML = unavailableBlock(`Could not read the node EPM (${escapeHtml(String(err && err.message ? err.message : err))}).`);
    return;
  }
  if (!res.ok) {
    out.innerHTML = unavailableBlock(`The node EPM is unavailable (HTTP ${res.status}).`);
    return;
  }
  const data = await res.json();
  out.innerHTML = `
    <div class="divider"><span class="lbl2">NODE EPM · JSON</span><span class="rule"></span></div>
    <pre class="data export-json">${escapeHtml(JSON.stringify(data, null, 2))}</pre>
    <div class="actions">
      <button class="btn" id="epm-download" type="button" title="Download the size-prefixed EPM FlatBuffer">DOWNLOAD .EPM</button>
      <button class="btn" id="export-close" type="button" title="Close">CLOSE</button>
    </div>`;
  const dl = document.querySelector('#epm-download');
  if (dl) dl.addEventListener('click', () => downloadPath('/sdn/v1/node/epm?format=fb', 'sdn-node.epm'));
  wireExportClose(out);
}

async function showNodeQR(out) {
  out.hidden = false;
  out.innerHTML = loadingBlock('rendering the node QR');
  let res;
  try {
    res = await fetch(QR_URL, { headers: { Accept: 'image/png' } });
  } catch (err) {
    out.innerHTML = unavailableBlock(`Could not render the node QR (${escapeHtml(String(err && err.message ? err.message : err))}).`);
    return;
  }
  if (!res.ok) {
    out.innerHTML = unavailableBlock(`The node QR is unavailable (HTTP ${res.status}).`);
    return;
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  out.innerHTML = `
    <div class="divider"><span class="lbl2">NODE EPM · QR</span><span class="rule"></span></div>
    <div class="qr-wrap"><img class="qr-img" alt="Node EPM QR code" src="${url}"></div>
    <p class="p-note">Scan reconstructs the signed node EPM. Generated by this node.</p>
    <div class="actions"><button class="btn" id="export-close" type="button" title="Close">CLOSE</button></div>`;
  const img = out.querySelector('.qr-img');
  if (img) img.addEventListener('load', () => URL.revokeObjectURL(url), { once: true });
  wireExportClose(out);
}

function wireExportClose(out) {
  const close = document.querySelector('#export-close');
  if (close) close.addEventListener('click', () => { out.hidden = true; out.innerHTML = ''; });
}

// downloadPath triggers a same-origin file download without leaving the page.
function downloadPath(path, filename) {
  const a = document.createElement('a');
  a.href = path;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function peerSummary(sdn, ipfs) {
  if (!sdn.length && !ipfs.length) {
    return emptyBlock('No peers connected', 'This node has not discovered any SDN peers and holds no libp2p swarm connections yet. Empty, not unreachable.');
  }
  const legend = `
    <div class="actions" style="gap:14px;margin-bottom:12px">
      <span class="chip cyan"><span class="dot" style="width:7px;height:7px;border-radius:50%;background:#35c9d8;box-shadow:0 0 6px #35c9d8"></span> ${sdn.length} SDN FLAG-DISCOVERED</span>
      <span class="chip"><span class="dot" style="width:7px;height:7px;border-radius:50%;background:#9fd4f5;box-shadow:0 0 6px #9fd4f5"></span> ${ipfs.length} IPFS SWARM</span>
    </div>`;
  const rows = [];
  sdn.slice(0, 6).forEach((id) => rows.push(peerMiniRow(id, 'SDN', '#35c9d8')));
  ipfs.slice(0, 6).forEach((id) => rows.push(peerMiniRow(id, 'SWARM', '#9fd4f5')));
  return legend + `<div class="rows" style="--c:1fr">${rows.join('')}</div>
    <p class="p-note"><span class="link" data-goto="peers">Open the full peers directory</span> for every discovered peer.</p>`;
}

function peerMiniRow(id, tag, color) {
  return `<div class="row" style="grid-template-columns:64px minmax(0,1fr)">
    <span style="font-size:10px;letter-spacing:0.1em;color:${color}">${escapeHtml(tag)}</span>
    <span class="cell-mono">${escapeHtml(id)}</span>
  </div>`;
}

// ---------------------------------------------------------------------------
// Peers screen — /sdn/v1/peers  (SDN peers vs IPFS swarm peers)
// ---------------------------------------------------------------------------

async function renderPeers() {
  screen.innerHTML = loadingBlock('reading SDN and swarm peers');
  const res = await fetchJSON('/peers');
  if (state.route !== 'peers') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`Peer state is unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const sdn = Array.isArray(res.data.sdn) ? res.data.sdn : [];
  const ipfs = Array.isArray(res.data.ipfs) ? res.data.ipfs : [];
  screen.innerHTML = `
    <div class="grid">
      <section class="panel span-6">
        <div class="panel-head"><span class="panel-kicker">SDN PEERS</span><span class="chip cyan">FLAG-DISCOVERED</span></div>
        <p class="p-note top0">Peers advertising the SDN membership flag namespace.</p>
        ${peerTable(sdn, '#35c9d8', 'No SDN peers yet', 'No peers have advertised the SDN flag on this node.')}
      </section>
      <section class="panel span-6">
        <div class="panel-head"><span class="panel-kicker">IPFS SWARM PEERS</span><span class="chip">OBSERVED</span></div>
        <p class="p-note top0">Raw libp2p swarm connections (not necessarily SDN nodes).</p>
        ${peerTable(ipfs, '#9fd4f5', 'No swarm peers yet', 'The node is not connected to any libp2p peers.')}
      </section>
    </div>
  `;
}

function peerTable(ids, color, emptyTitle, emptyDetail) {
  if (!ids.length) return emptyBlock(emptyTitle, emptyDetail);
  return `
    <div class="rows">
      <div class="head" style="grid-template-columns:14px minmax(0,1fr)"><span></span><span>PEER ID</span></div>
      ${ids.map((id) => `<div class="row" style="grid-template-columns:14px minmax(0,1fr)">
        <span class="dot" style="width:7px;height:7px;border-radius:50%;background:${color};box-shadow:0 0 6px ${color}"></span>
        <span class="cell-mono">${escapeHtml(id)}</span>
      </div>`).join('')}
    </div>
  `;
}

// ---------------------------------------------------------------------------
// Data screen — /sdn/v1/data/sources then /sdn/v1/data?source=&type=
// ---------------------------------------------------------------------------

async function renderData() {
  screen.innerHTML = loadingBlock('reading local data catalog');
  const res = await fetchJSON('/data/sources');
  if (state.route !== 'data') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`The local data catalog is unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const sources = Array.isArray(res.data) ? res.data : [];
  if (sources.length && !sources.some((s) => state.selectedSource && s.source === state.selectedSource.source && s.type === state.selectedSource.type)) {
    state.selectedSource = { source: sources[0].source, type: sources[0].type };
  }
  if (!sources.length) state.selectedSource = null;

  screen.innerHTML = `
    <section class="panel" style="display:flex;align-items:center;gap:14px;flex-wrap:wrap;padding:13px 16px;margin-bottom:14px">
      <div style="display:flex;align-items:baseline;gap:9px">
        <span style="font-family:var(--font-display);font-weight:700;font-size:18px;letter-spacing:0.02em;color:#eaf6f8">spacedatastandards<span style="color:#35c9d8">.org</span></span>
      </div>
      <span class="subtle" style="font-size:11px;letter-spacing:0.04em">${sources.length} local (source, standard) ${sources.length === 1 ? 'pair' : 'pairs'} · FlatBuffers IDL</span>
      <span class="spring" style="flex:1"></span>
      <div style="display:flex;align-items:center;gap:7px;font-size:11px;color:#7d929b"><span class="dot" style="width:7px;height:7px;border-radius:50%;background:#5ad6a0;box-shadow:0 0 6px #5ad6a0"></span>FLATSQL STORE · SCHEMA SYNCED</div>
    </section>
    <div class="grid">
      <section class="panel span-5">
        <div class="panel-head"><span class="panel-kicker">DATA SOURCES</span><span class="chip">${sources.length} ${sources.length === 1 ? 'PAIR' : 'PAIRS'}</span></div>
        ${sourcesTable(sources)}
      </section>
      <section class="panel flush span-7" style="display:flex;flex-direction:column;overflow:hidden">
        <div class="panel-head" style="padding:13px 16px;border-bottom:1px solid rgba(90,150,180,0.16);margin-bottom:0">
          <span class="panel-kicker">RECORDS</span>
          <div class="segment" id="output-modes"></div>
        </div>
        <div id="records-region" style="padding:14px 16px">${state.selectedSource ? loadingBlock('reading records') : emptyBlock('No source selected', 'Store data on this node, or pick a (source, standard) pair.')}</div>
      </section>
    </div>
  `;

  attachDataHandlers(sources);
  if (state.selectedSource) loadRecords();
}

function sourcesTable(sources) {
  if (!sources.length) {
    return emptyBlock('No data yet', 'This node has not stored any records. The store is empty, not unreachable.');
  }
  const sel = state.selectedSource;
  return `
    <div class="rows">
      <div class="head" style="grid-template-columns:minmax(0,1fr) 60px"><span>SOURCE</span><span>STD</span></div>
      ${sources.map((s) => {
        const active = sel && s.source === sel.source && s.type === sel.type;
        return `<div class="row click ${active ? 'selected' : ''}" data-source="${escapeAttribute(s.source)}" data-type="${escapeAttribute(s.type)}" style="grid-template-columns:minmax(0,1fr) 60px">
          <span class="cell-mono">${escapeHtml(s.source)}</span>
          <span><span class="chip cyan">${escapeHtml(s.type)}</span></span>
        </div>`;
      }).join('')}
    </div>
  `;
}

function attachDataHandlers(sources) {
  document.querySelectorAll('[data-source][data-type].click').forEach((row) => {
    row.addEventListener('click', () => {
      state.selectedSource = { source: row.dataset.source, type: row.dataset.type };
      document.querySelectorAll('[data-source][data-type].click').forEach((r) => r.classList.remove('selected'));
      row.classList.add('selected');
      loadRecords();
    });
  });
  renderOutputModes(document.querySelector('#output-modes'));
}

function renderOutputModes(container) {
  if (!container) return;
  container.innerHTML = ['table', 'json', 'csv']
    .map((mode) => `<button class="${state.outputMode === mode ? 'active' : ''}" data-output-mode="${mode}" type="button">${mode.toUpperCase()}</button>`)
    .join('');
  container.querySelectorAll('[data-output-mode]').forEach((button) => {
    button.addEventListener('click', () => {
      state.outputMode = button.dataset.outputMode;
      renderOutputModes(container);
      if (state.selectedSource) loadRecords();
    });
  });
}

async function loadRecords() {
  const region = document.querySelector('#records-region');
  const sel = state.selectedSource;
  if (!region || !sel) return;
  region.innerHTML = loadingBlock(`reading ${sel.source} / ${sel.type}`);
  const res = await fetchJSON(`/data?source=${encodeURIComponent(sel.source)}&type=${encodeURIComponent(sel.type)}`);
  if (state.route !== 'data') return;
  if (!state.selectedSource || state.selectedSource.source !== sel.source || state.selectedSource.type !== sel.type) return;
  if (!res.ok) {
    region.innerHTML = unavailableBlock(`Records for ${escapeHtml(sel.source)} / ${escapeHtml(sel.type)} are unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const data = res.data;
  const recs = Array.isArray(data.records) ? data.records : [];
  const meta = `<p class="p-note top0" style="margin-bottom:12px">${escapeHtml(String(data.returned ?? recs.length))} of ${escapeHtml(String(data.total ?? recs.length))} records · <span class="mono">${escapeHtml(sel.source)}</span> / <span class="chip cyan">${escapeHtml(sel.type)}</span> · limit ${escapeHtml(String(data.limit ?? ''))}</p>`;
  if (!recs.length) {
    region.innerHTML = meta + emptyBlock('No records', 'This (source, standard) pair has no stored records yet.');
    return;
  }
  region.innerHTML = meta + renderRecordsOutput(recs);
}

function renderRecordsOutput(recs) {
  if (state.outputMode === 'json') {
    return `<pre class="data">${escapeHtml(JSON.stringify(recs, null, 2))}</pre>`;
  }
  if (state.outputMode === 'csv') {
    const rows = ['cid,size,file_id', ...recs.map((r) => `${r.cid},${r.size},${r.file_id ?? ''}`)];
    return `<pre class="data">${escapeHtml(rows.join('\n'))}</pre>`;
  }
  return `
    <div class="rows">
      <div class="head" style="grid-template-columns:minmax(0,1.6fr) 72px 88px"><span>CID</span><span>SIZE</span><span>FILE ID</span></div>
      ${recs.map((r) => `<div class="row" style="grid-template-columns:minmax(0,1.6fr) 72px 88px">
        <span class="cell-mono">${escapeHtml(r.cid)}</span>
        <span class="cell-dim">${escapeHtml(String(r.size))} B</span>
        <span class="cell-mono">${escapeHtml(r.file_id || '—')}</span>
      </div>`).join('')}
    </div>
  `;
}

// ---------------------------------------------------------------------------
// Channels screen — /sdn/v1/channels
// ---------------------------------------------------------------------------

async function renderChannels() {
  screen.innerHTML = loadingBlock('reading channel fan-out');
  const res = await fetchJSON('/channels');
  if (state.route !== 'channels') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`Channel state is unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const channels = Array.isArray(res.data) ? res.data : [];
  const activeCount = channels.filter((c) => c.active).length;
  screen.innerHTML = `
    <div class="grid">
      <section class="panel span-12">
        <div class="panel-head">
          <span class="panel-kicker">ENCRYPTED DATA CHANNELS</span>
          <span class="chip">${activeCount} STREAMING / ${channels.length} KNOWN</span>
        </div>
        <p class="p-note top0" style="margin-bottom:14px">One pubsub channel per (source, standard). <span class="state-active">STREAMING</span> means the node is actively subscribed; <span class="state-idle">IDLE</span> means known but not fanned out.</p>
        ${channelsTable(channels)}
      </section>
    </div>
  `;
}

function channelsTable(channels) {
  if (!channels.length) {
    return emptyBlock('No channels yet', 'No (source, standard) pairs are stored, so there are no channels to stream.');
  }
  const cols = '14px minmax(0,1.6fr) 60px 120px minmax(0,1.4fr)';
  return `
    <div class="rows">
      <div class="head" style="grid-template-columns:${cols}"><span></span><span>SOURCE</span><span>STD</span><span>STATE</span><span>TOPIC</span></div>
      ${channels.map((c) => `<div class="row" style="grid-template-columns:${cols}">
        <span class="dot" style="width:7px;height:7px;border-radius:50%;background:${c.active ? '#5ad6a0' : '#ffb24d'};box-shadow:0 0 6px ${c.active ? '#5ad6a0' : '#ffb24d'}"></span>
        <span class="cell-mono">${escapeHtml(c.source)}</span>
        <span><span class="chip cyan">${escapeHtml(c.standard)}</span></span>
        <span>${c.active ? '<span class="chip good">STREAMING</span>' : '<span class="chip warn">IDLE</span>'}</span>
        <span class="cell-mono">${escapeHtml(c.topic)}</span>
      </div>`).join('')}
    </div>
  `;
}

// ---------------------------------------------------------------------------
// Apps screen — /sdn/v1/apps  (installed $APP records; each may serve a UI)
// ---------------------------------------------------------------------------

async function renderApps() {
  screen.innerHTML = loadingBlock('reading installed SDN apps');
  const res = await fetchJSON('/apps');
  if (state.route !== 'apps') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`The apps catalog is unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const apps = Array.isArray(res.data) ? res.data : [];
  if (!apps.length) {
    screen.innerHTML = `
      <div class="grid"><section class="panel span-12">
        <div class="panel-kicker">INSTALLED APPS</div>
        ${emptyBlock('No apps installed', 'No SDN $APP records are stored on this node. Empty, not unreachable.')}
      </section></div>`;
    return;
  }
  screen.innerHTML = `
    <div class="screen-kicker"><span>ANALYSIS &amp; DATA APPS</span><span class="spring"></span><span class="hint">${apps.length} installed · from /sdn/v1/apps</span></div>
    <div class="grid">
      ${apps.map(appCard).join('')}
    </div>
  `;
}

function appCard(a) {
  const name = a.name || a.id || 'unknown app';
  const canOpen = a.id && (a.pages ?? 0) > 0;
  const openBtn = canOpen
    ? `<a class="btn ice" href="${escapeAttribute(API_BASE + '/apps/' + encodeURIComponent(a.id))}" target="_blank" rel="noopener" title="Open this app's UI, served by this node"><span class="dot" style="width:7px;height:7px;border-radius:50%;background:#9fd4f5;box-shadow:0 0 7px #9fd4f5;margin-right:7px"></span>OPEN APP</a>`
    : `<button class="btn" disabled title="This app ships no inline UI page">NO UI PAGE</button>`;
  return `
    <section class="panel span-6">
      <div class="panel-head">
        <div style="display:flex;align-items:baseline;gap:9px;min-width:0">
          <span style="font-family:var(--font-display);font-weight:700;font-size:19px;letter-spacing:0.03em;color:#eaf6f8">${escapeHtml(name)}</span>
          <span class="chip cyan">${escapeHtml(a.version || 'v?')}</span>
        </div>
      </div>
      <dl class="dl">
        <div class="row"><dt>SOURCE</dt><dd class="mono wrap-any">${escapeHtml(a.source || '—')}</dd></div>
        <div class="row"><dt>CID</dt><dd class="mono wrap-any">${escapeHtml(a.cid || '—')}</dd></div>
      </dl>
      <div class="divider"><span class="lbl2">MANIFEST</span><span class="rule"></span></div>
      <div class="actions" style="gap:8px">
        <span class="tag">MODULES · ${escapeHtml(String(a.modules ?? 0))}</span>
        <span class="tag">PAGES · ${escapeHtml(String(a.pages ?? 0))}</span>
        <span class="tag">SIZE · ${escapeHtml(String(a.size ?? 0))} B</span>
      </div>
      <div class="actions" style="margin-top:14px">${openBtn}</div>
    </section>
  `;
}

// ---------------------------------------------------------------------------
// Modules screen — /sdn/v1/modules (list) + PUT /sdn/v1/modules/{id}/config
//
// Every registered module is listed with its effective timers, running flag,
// last run and stored config. Selecting a module opens a settings drawer that
// edits the cron schedule (per-timer interval) and PUTs the config, then
// reflects the applied result.
// ---------------------------------------------------------------------------

async function renderModules() {
  screen.innerHTML = loadingBlock('reading registered modules and cron timers');
  const res = await fetchJSON('/modules');
  if (state.route !== 'modules') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`The module runtime is unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const mods = Array.isArray(res.data) ? res.data : [];
  if (!mods.length) {
    screen.innerHTML = `
      <div class="grid"><section class="panel span-12">
        <div class="panel-kicker">SELF-SCHEDULING MODULES</div>
        ${emptyBlock('No modules registered', 'No WASM/native cron modules are registered on this node. Empty, not unreachable.')}
      </section></div>`;
    return;
  }
  if (!state.selectedModule || !mods.some((m) => m.id === state.selectedModule)) {
    state.selectedModule = mods[0].id;
  }
  const selected = mods.find((m) => m.id === state.selectedModule);

  screen.innerHTML = `
    <div class="screen-kicker"><span>SELF-SCHEDULING MODULES</span><span class="spring"></span><span class="hint">${mods.length} registered · cron scheduler live</span></div>
    <div class="grid">
      <section class="panel span-5">
        <div class="panel-kicker">MODULES</div>
        <div style="display:flex;flex-direction:column;gap:10px">
          ${mods.map(moduleCard).join('')}
        </div>
      </section>
      <section class="panel ticked span-7" style="--tick:#35c9d8"><span class="tick-b"></span>
        <div class="panel-kicker">MODULE SETTINGS</div>
        <div id="module-settings">${moduleSettings(selected)}</div>
      </section>
    </div>
  `;

  screen.querySelectorAll('[data-module]').forEach((el) => {
    el.addEventListener('click', () => {
      state.selectedModule = el.dataset.module;
      state.moduleNotice = null;
      renderModules();
    });
  });
  attachModuleSettingsHandlers();
}

function moduleCard(m) {
  const active = m.id === state.selectedModule;
  const eff = m.timers && m.timers.length ? humanMs(m.timers[0].interval_ms) : '—';
  const timerCount = m.timers ? m.timers.length : 0;
  return `
    <div class="card ${active ? 'selected' : ''}" data-module="${escapeAttribute(m.id)}">
      <div class="grow">
        <div style="display:flex;align-items:center;gap:7px">
          <span class="card-title">${escapeHtml(m.name || m.id)}</span>
          ${m.version ? `<span class="chip">v${escapeHtml(m.version)}</span>` : ''}
        </div>
        <div class="card-meta">
          <span class="tag" style="color:#9fd4f5">${escapeHtml(String(timerCount))} TIMER${timerCount === 1 ? '' : 'S'}</span>
          <span style="font-size:11px;color:#6f8693">every ${escapeHtml(eff)}</span>
        </div>
      </div>
      <div class="card-side">
        <span style="display:inline-flex;align-items:center;gap:6px;font-size:11px;letter-spacing:0.06em;color:${m.running ? '#5ad6a0' : '#ffb24d'}">
          <span class="dot" style="width:7px;height:7px;border-radius:50%;background:currentColor;box-shadow:0 0 6px currentColor"></span>${m.running ? 'RUNNING' : 'IDLE'}
        </span>
        <span style="font-size:9.5px;letter-spacing:0.1em;color:#6f8693;font-family:var(--font-mono)">${escapeHtml(m.id)}</span>
      </div>
    </div>
  `;
}

function moduleSettings(m) {
  if (!m) return emptyBlock('No module selected', 'Select a module from the list to edit its cron schedule.');
  const timers = Array.isArray(m.timers) ? m.timers : [];
  const notice = state.moduleNotice && state.moduleNotice.module === m.id
    ? `<div class="notice ${state.moduleNotice.kind}">${escapeHtml(state.moduleNotice.text)}</div>`
    : '';
  const configJSON = escapeHtml(JSON.stringify(m.config || {}, null, 2));
  const timerInputs = timers.length
    ? timers.map((t) => {
        const inputId = `timer-${escapeAttribute(t.id)}`;
        return `
        <div class="timer-row">
          <div class="field">
            <span class="fld-label">TIMER · ${escapeHtml(t.id)}</span>
            <span class="hint">effective every ${escapeHtml(humanMs(t.interval_ms))} · manifest default overridable</span>
          </div>
          <div class="field">
            <label for="${inputId}">INTERVAL (ms)</label>
            <input class="input" id="${inputId}" name="${inputId}" type="number" min="1" step="1" inputmode="numeric"
                   data-timer="${escapeAttribute(t.id)}" value="${escapeAttribute(String(t.interval_ms))}">
          </div>
        </div>`;
      }).join('')
    : `<p class="p-note top0">This module declares no cron timers.</p>`;

  return `
    <div style="display:flex;align-items:baseline;gap:10px;margin-bottom:4px">
      <span style="font-family:var(--font-display);font-weight:700;font-size:21px;letter-spacing:0.03em;color:#eaf6f8">${escapeHtml(m.name || m.id)}</span>
      ${m.version ? `<span class="chip ice">v${escapeHtml(m.version)}</span>` : ''}
    </div>
    <div class="readout-sub" style="margin:2px 0 12px">module id · <span class="mono">${escapeHtml(m.id)}</span></div>
    <div class="kv">
      <div><div class="k">STATE</div><div class="v" style="color:${m.running ? '#5ad6a0' : '#ffb24d'}">${m.running ? 'RUNNING' : 'IDLE'}</div></div>
      <div><div class="k">LAST RUN</div><div class="v">${escapeHtml(m.last_run || 'never')}</div></div>
    </div>

    <div class="settings-block">
      <div class="divider"><span class="lbl2">CRON SCHEDULE</span><span class="rule"></span></div>
      <p class="p-note top0">Edit a timer's firing interval. Saving PUTs <span class="mono">/sdn/v1/modules/${escapeHtml(m.id)}/config</span> and reschedules the live ticker; the applied interval is reflected below.</p>
      ${timerInputs}
      ${timers.length ? `
      <div class="actions">
        <button class="btn primary" id="mod-save" type="button" data-save-id="${escapeAttribute(m.id)}">SAVE SCHEDULE</button>
        <button class="btn" id="mod-reset" type="button">RESET</button>
      </div>` : ''}
      ${notice}
    </div>

    <div class="settings-block">
      <div class="divider"><span class="lbl2">STORED CONFIG</span><span class="rule"></span></div>
      <pre class="data" style="max-height:180px">${configJSON}</pre>
    </div>
  `;
}

function attachModuleSettingsHandlers() {
  const saveBtn = document.querySelector('#mod-save');
  if (saveBtn) saveBtn.addEventListener('click', () => saveModuleSchedule(saveBtn.dataset.saveId));
  const resetBtn = document.querySelector('#mod-reset');
  if (resetBtn) resetBtn.addEventListener('click', () => { state.moduleNotice = null; renderModules(); });
}

async function saveModuleSchedule(id) {
  // Read the input values SYNCHRONOUSLY up front — before any await or DOM
  // re-render — so the operator's edits can never be lost to a race.
  const edits = {};
  let bad = null;
  document.querySelectorAll('#module-settings [data-timer]').forEach((el) => {
    const method = el.dataset.timer;
    const n = parseInt(el.value, 10);
    if (!Number.isFinite(n) || n <= 0) { bad = method; return; }
    edits[method] = n;
  });
  if (bad) {
    state.moduleNotice = { module: id, kind: 'bad', text: `Interval for timer "${bad}" must be a positive integer in milliseconds.` };
    renderModules();
    return;
  }

  const region = document.querySelector('#module-settings');
  if (region) region.innerHTML = loadingBlock(`applying schedule to ${id}`);

  // Re-read the current stored config so opaque keys round-trip untouched.
  const listRes = await fetchJSON('/modules');
  const mods = listRes.ok && Array.isArray(listRes.data) ? listRes.data : [];
  const m = mods.find((x) => x.id === id);
  const baseConfig = m && m.config ? m.config : {};
  const cfg = { ...baseConfig, timers: { ...(baseConfig.timers || {}), ...edits } };

  const res = await putJSON(`/modules/${encodeURIComponent(id)}/config`, cfg);
  if (state.route !== 'modules') return;
  if (!res.ok) {
    state.moduleNotice = { module: id, kind: 'bad', text: `Config rejected (${res.error}).` };
  } else {
    const appliedTimers = res.data && res.data.timers ? res.data.timers : {};
    const summary = Object.keys(appliedTimers).length
      ? Object.entries(appliedTimers).map(([k, v]) => `${k} = ${humanMs(v)}`).join(' · ')
      : 'applied';
    state.moduleNotice = { module: id, kind: 'good', text: `Schedule applied and rescheduled: ${summary}.` };
  }
  renderModules();
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

function init() {
  document.querySelectorAll('[data-route]').forEach((button) => {
    button.addEventListener('click', () => navigate(button.dataset.route));
  });
  window.addEventListener('hashchange', () => {
    state.route = normalizeRoute(location.hash);
    render();
  });
  setupRailCollapse();
  render();
  refreshStatusStrip();
}

// setupRailCollapse pins the icon rail open when the operator interacts with it
// (so it stays expanded while navigating) and collapses/unpins it on any click
// or touch OUTSIDE the rail — e.g. in the main content area. Hover-to-expand
// (CSS :hover) and nav item clicks are untouched: a nav click is inside the
// rail, so it navigates AND keeps the rail pinned; only outside input collapses.
function setupRailCollapse() {
  const rail = document.getElementById('rail');
  if (!rail) return;
  rail.addEventListener('click', () => rail.classList.add('pinned'));
  const collapseIfOutside = (e) => {
    if (!rail.contains(e.target)) rail.classList.remove('pinned');
  };
  document.addEventListener('click', collapseIfOutside);
  document.addEventListener('touchstart', collapseIfOutside, { passive: true });
}

init();
