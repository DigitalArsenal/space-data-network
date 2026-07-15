/*
 * Space Data Network node console — vanilla JS, no build step, no framework.
 *
 * Every data source is the node's OWN same-origin read-only API under
 * /sdn/v1/* (served by the sdnapi plugin on the same loopback listener as
 * this page). There are NO external-origin requests and NO fixture data:
 * empty results render as explicit empty-state language, never as fake rows.
 */

const API_BASE = '/sdn/v1';

const routes = ['node', 'peers', 'data', 'channels', 'conjunction'];
const titles = {
  node: 'Node',
  peers: 'Peers',
  data: 'Data',
  channels: 'Channels',
  conjunction: 'Conjunction',
};

const state = {
  route: normalizeRoute(location.hash),
  selectedSource: null, // {source, type} row selected on the Data screen
  outputMode: 'table',
};

const screen = document.querySelector('#screen');
const screenTitle = document.querySelector('#screen-title');
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
// fetch helper — resolves to a tagged result, never throws to the caller.
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

// ---------------------------------------------------------------------------
// shared state-language blocks
// ---------------------------------------------------------------------------

function loadingBlock(label) {
  return `<div class="state-block loading"><strong>loading…</strong><span>${escapeHtml(label)}</span></div>`;
}

function emptyBlock(title, detail) {
  return `<div class="state-block"><strong>${escapeHtml(title)}</strong><span>${escapeHtml(detail)}</span></div>`;
}

function unavailableBlock(detail) {
  return `<div class="state-block"><strong class="state-unavailable">unavailable</strong><span>${escapeHtml(detail)}</span></div>`;
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
  screen.dataset.screen = state.route;
  renderCurrentScreen();
}

function renderCurrentScreen() {
  if (state.route === 'peers') return renderPeers();
  if (state.route === 'data') return renderData();
  if (state.route === 'channels') return renderChannels();
  if (state.route === 'conjunction') return renderConjunction();
  return renderNode();
}

// ---------------------------------------------------------------------------
// top status strip — driven by /node and /peers
// ---------------------------------------------------------------------------

async function refreshStatusStrip() {
  const [node, peers] = await Promise.all([fetchJSON('/node'), fetchJSON('/peers')]);
  const chips = [];
  if (!node.ok) {
    chips.push(`<span class="status-chip bad">node unavailable</span>`);
    statusStrip.innerHTML = chips.join('');
    return;
  }
  chips.push(`<span class="status-chip good">online</span>`);
  const sdnCount = peers.ok && Array.isArray(peers.data.sdn) ? peers.data.sdn.length : 0;
  chips.push(`<span class="status-chip">${sdnCount} SDN ${sdnCount === 1 ? 'peer' : 'peers'}</span>`);
  const pubsub = node.data.pubsub_enabled;
  chips.push(`<span class="status-chip ${pubsub ? 'good' : 'warn'}">pubsub ${pubsub ? 'online' : 'off'}</span>`);
  const sources = node.data.storage && typeof node.data.storage.sources === 'number' ? node.data.storage.sources : 0;
  chips.push(`<span class="status-chip">${sources} data ${sources === 1 ? 'source' : 'sources'}</span>`);
  statusStrip.innerHTML = chips.join('');
}

// ---------------------------------------------------------------------------
// Node screen — /sdn/v1/node
// ---------------------------------------------------------------------------

async function renderNode() {
  screen.innerHTML = loadingBlock('reading node identity and health');
  const res = await fetchJSON('/node');
  if (state.route !== 'node') return;
  if (!res.ok) {
    screen.innerHTML = unavailableBlock(`The node HTTP API did not respond (${escapeHtml(res.error)}). Is the daemon running?`);
    return;
  }
  const node = res.data;
  const storage = node.storage || {};
  const records = typeof storage.records === 'number' ? storage.records.toLocaleString() : 'not counted';
  screen.innerHTML = `
    <div class="grid">
      <section class="panel span-4">
        <h2>Node Health</h2>
        <div class="metric state-online">online</div>
        <p class="muted">Local SDN node, reachable over loopback.</p>
        <dl class="detail-list">
          <div><dt>Peer ID</dt><dd class="mono wrap-any">${escapeHtml(node.peer_id || 'unknown')}</dd></div>
          <div><dt>PubSub</dt><dd>${node.pubsub_enabled ? '<span class="chip state-active">online</span>' : '<span class="chip state-idle">off</span>'}</dd></div>
        </dl>
      </section>
      <section class="panel span-4">
        <h2>Identity</h2>
        <div class="metric">node key</div>
        <p class="muted">The node's libp2p identity is its SDN peer identity.</p>
        <dl class="detail-list">
          <div><dt>SDN flag namespace</dt><dd class="mono wrap-any">${escapeHtml(node.sdn_flag_namespace || 'unset')}</dd></div>
        </dl>
        <h3>Export</h3>
        <div class="actions">
          <button class="button" disabled title="Not available in this build">EPM</button>
          <button class="button" disabled title="Not available in this build">vCard</button>
          <button class="button" disabled title="Not available in this build">QR</button>
        </div>
        <p class="subtle" style="margin-top:8px">EPM / vCard / QR export is <span class="state-unavailable">unavailable</span> in this build.</p>
      </section>
      <section class="panel span-4">
        <h2>Local Storage</h2>
        <div class="metric">${escapeHtml(String(storage.sources ?? 0))}</div>
        <p class="muted">Distinct (source, standard) pairs in the FlatSQL store.</p>
        <dl class="detail-list">
          <div><dt>Records</dt><dd>${escapeHtml(records)}</dd></div>
          <div><dt>Store</dt><dd>${(storage.sources ?? 0) > 0 ? '<span class="chip state-active">populated</span>' : '<span class="chip">empty</span>'}</dd></div>
        </dl>
      </section>
    </div>
  `;
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
        <div class="panel-head">
          <h2>SDN Peers</h2>
          <span class="chip state-trusted">flag-discovered</span>
        </div>
        <p class="muted">Peers advertising the SDN membership flag namespace.</p>
        ${peerTable(sdn, 'No SDN peers yet', 'No peers have advertised the SDN flag on this node.')}
      </section>
      <section class="panel span-6">
        <div class="panel-head">
          <h2>IPFS Swarm Peers</h2>
          <span class="chip">observed</span>
        </div>
        <p class="muted">Raw libp2p swarm connections (not necessarily SDN nodes).</p>
        ${peerTable(ipfs, 'No swarm peers yet', 'The node is not connected to any libp2p peers.')}
      </section>
    </div>
  `;
}

function peerTable(ids, emptyTitle, emptyDetail) {
  if (!ids.length) return emptyBlock(emptyTitle, emptyDetail);
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Peer ID</th></tr></thead>
        <tbody>
          ${ids.map((id) => `<tr><td class="mono wrap-any">${escapeHtml(id)}</td></tr>`).join('')}
        </tbody>
      </table>
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
  // Keep / repair the current selection against the live catalog.
  if (sources.length && !sources.some((s) => state.selectedSource && s.source === state.selectedSource.source && s.type === state.selectedSource.type)) {
    state.selectedSource = { source: sources[0].source, type: sources[0].type };
  }
  if (!sources.length) state.selectedSource = null;

  screen.innerHTML = `
    <div class="grid">
      <section class="panel span-5">
        <div class="panel-head">
          <h2>Data Sources</h2>
          <span class="chip">${sources.length} pair${sources.length === 1 ? '' : 's'}</span>
        </div>
        <p class="muted">Each row is a (source, 3-letter standard) pair in the local FlatSQL store.</p>
        ${sourcesTable(sources)}
      </section>
      <section class="panel span-7">
        <div class="panel-head">
          <h2>Records</h2>
          <div class="segment" id="output-modes"></div>
        </div>
        <div id="records-region">${state.selectedSource ? loadingBlock('reading records') : emptyBlock('No source selected', 'Store data on this node, or pick a (source, standard) pair.')}</div>
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
    <div class="table-wrap">
      <table>
        <thead><tr><th>Source</th><th>Standard</th></tr></thead>
        <tbody>
          ${sources.map((s) => {
            const active = sel && s.source === sel.source && s.type === sel.type;
            return `<tr data-selectable data-source="${escapeAttribute(s.source)}" data-type="${escapeAttribute(s.type)}" class="${active ? 'selected' : ''}">
              <td class="mono wrap-any">${escapeHtml(s.source)}</td>
              <td><span class="chip">${escapeHtml(s.type)}</span></td>
            </tr>`;
          }).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function attachDataHandlers(sources) {
  document.querySelectorAll('[data-source][data-selectable]').forEach((row) => {
    row.addEventListener('click', () => {
      state.selectedSource = { source: row.dataset.source, type: row.dataset.type };
      document.querySelectorAll('[data-source][data-selectable]').forEach((r) => r.classList.remove('selected'));
      row.classList.add('selected');
      loadRecords();
    });
  });
  renderOutputModes(document.querySelector('#output-modes'));
}

function renderOutputModes(container) {
  if (!container) return;
  container.innerHTML = ['table', 'json', 'csv']
    .map((mode) => `<button class="button ${state.outputMode === mode ? 'primary' : ''}" data-output-mode="${mode}" type="button">${mode.toUpperCase()}</button>`)
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
  // Guard against a stale response after the selection changed.
  if (!state.selectedSource || state.selectedSource.source !== sel.source || state.selectedSource.type !== sel.type) return;
  if (!res.ok) {
    region.innerHTML = unavailableBlock(`Records for ${escapeHtml(sel.source)} / ${escapeHtml(sel.type)} are unavailable (${escapeHtml(res.error)}).`);
    return;
  }
  const data = res.data;
  const recs = Array.isArray(data.records) ? data.records : [];
  const meta = `<p class="muted">${escapeHtml(String(data.returned ?? recs.length))} of ${escapeHtml(String(data.total ?? recs.length))} records — <span class="mono">${escapeHtml(sel.source)}</span> / <span class="chip">${escapeHtml(sel.type)}</span> (limit ${escapeHtml(String(data.limit ?? ''))})</p>`;
  if (!recs.length) {
    region.innerHTML = meta + emptyBlock('No records', 'This (source, standard) pair has no stored records yet.');
    return;
  }
  region.innerHTML = meta + renderRecordsOutput(recs);
}

function renderRecordsOutput(recs) {
  if (state.outputMode === 'json') {
    return `<textarea readonly>${escapeHtml(JSON.stringify(recs, null, 2))}</textarea>`;
  }
  if (state.outputMode === 'csv') {
    const rows = ['cid,size,file_id', ...recs.map((r) => `${r.cid},${r.size},${r.file_id ?? ''}`)];
    return `<textarea readonly>${escapeHtml(rows.join('\n'))}</textarea>`;
  }
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>CID</th><th>Size</th><th>File ID</th></tr></thead>
        <tbody>
          ${recs.map((r) => `<tr>
            <td class="mono wrap-any">${escapeHtml(r.cid)}</td>
            <td>${escapeHtml(String(r.size))} B</td>
            <td class="mono">${escapeHtml(r.file_id || '—')}</td>
          </tr>`).join('')}
        </tbody>
      </table>
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
          <h2>Channels</h2>
          <span class="chip">${activeCount} streaming / ${channels.length} known</span>
        </div>
        <p class="muted">One pubsub channel per (source, standard). <span class="state-active">streaming</span> means the node is actively subscribed; <span class="state-idle">idle</span> means known but not fanned out.</p>
        ${channelsTable(channels)}
      </section>
    </div>
  `;
}

function channelsTable(channels) {
  if (!channels.length) {
    return emptyBlock('No channels yet', 'No (source, standard) pairs are stored, so there are no channels to stream.');
  }
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Source</th><th>Standard</th><th>State</th><th>Topic</th></tr></thead>
        <tbody>
          ${channels.map((c) => `<tr>
            <td class="mono wrap-any">${escapeHtml(c.source)}</td>
            <td><span class="chip">${escapeHtml(c.standard)}</span></td>
            <td>${c.active ? '<span class="chip state-active">streaming</span>' : '<span class="chip state-idle">idle</span>'}</td>
            <td class="mono wrap-any">${escapeHtml(c.topic)}</td>
          </tr>`).join('')}
        </tbody>
      </table>
    </div>
  `;
}

// ---------------------------------------------------------------------------
// Conjunction screen — separate app; honest "unavailable" stub.
// Lists any installed apps from the real /sdn/v1/apps endpoint.
// ---------------------------------------------------------------------------

async function renderConjunction() {
  screen.innerHTML = loadingBlock('checking for the conjunction screening app');
  const res = await fetchJSON('/apps');
  if (state.route !== 'conjunction') return;
  const apps = res.ok && Array.isArray(res.data) ? res.data : [];
  const appsBlock = res.ok
    ? (apps.length ? installedAppsTable(apps) : emptyBlock('No apps installed', 'No SDN apps are installed on this node.'))
    : unavailableBlock(`The apps catalog is unavailable (${escapeHtml(res.error)}).`);
  screen.innerHTML = `
    <div class="grid">
      <section class="panel span-12">
        <div class="panel-head">
          <h2>Private Maneuver Ephemeris Screening</h2>
          <span class="chip state-unavailable">unavailable</span>
        </div>
        <p class="muted">Conjunction screening lets an operator screen private maneuver ephemeris without broadcasting maneuver intent to competitors.</p>
        <div class="notice">This is a separate SDN app. It is <strong class="state-unavailable">unavailable</strong> in this node console build — no screening data is shown because none is being faked. Install the conjunction app to enable it.</div>
      </section>
      <section class="panel span-12">
        <h2>Installed Apps</h2>
        <p class="muted">SDN apps stored on this node (from <span class="mono">/sdn/v1/apps</span>).</p>
        ${appsBlock}
      </section>
    </div>
  `;
}

function installedAppsTable(apps) {
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>App</th><th>Version</th><th>Source</th><th>Modules</th><th>Pages</th></tr></thead>
        <tbody>
          ${apps.map((a) => `<tr>
            <td><strong>${escapeHtml(a.name || a.id || 'unknown')}</strong><br><span class="muted mono wrap-any">${escapeHtml(a.cid || '')}</span></td>
            <td>${escapeHtml(a.version || '—')}</td>
            <td class="mono wrap-any">${escapeHtml(a.source || '')}</td>
            <td>${escapeHtml(String(a.modules ?? 0))}</td>
            <td>${escapeHtml(String(a.pages ?? 0))}</td>
          </tr>`).join('')}
        </tbody>
      </table>
    </div>
  `;
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
  render();
  refreshStatusStrip();
}

init();
