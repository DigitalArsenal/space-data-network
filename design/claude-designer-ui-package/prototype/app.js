const routes = ['node', 'peers', 'data', 'channels', 'conjunction'];
const titles = {
  node: 'Node',
  peers: 'Peers',
  data: 'Data',
  channels: 'Channels',
  conjunction: 'Conjunction'
};

const fallbackFixtures = {
  "node": {
    "name": "Local SDN Node",
    "peerId": "12D3KooWDesignerLocalNode",
    "status": "online",
    "mode": "desktop-local",
    "api": "http://127.0.0.1:5001",
    "gateway": "http://127.0.0.1:8080",
    "storage": "4.8 GB",
    "identity": {
      "state": "unlocked",
      "entity": "Space Data Network Operator",
      "epmCid": "bafkreidesignerpublicepmexample",
      "vcard": "Space Data Network Operator"
    },
    "service": {
      "state": "running",
      "autostart": true,
      "update": "0.47.0 current"
    }
  },
  "peers": [
    {
      "id": "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",
      "name": "SpaceAware.io",
      "role": "Provider",
      "trust": "trusted",
      "addr": "/ip4/159.203.150.8/tcp/4001",
      "agent": "spacedatanetwork/1.0.3",
      "feeds": ["EPM", "MPE", "PNM"],
      "epmCid": "bafkreiggawraezbltnl3anwmabtuhvmlhdiotx5pxuqa7zmxkfjjjq35d4"
    },
    {
      "id": "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3",
      "name": "CelesTrak Provider",
      "role": "Provider",
      "trust": "trusted",
      "addr": "/ip4/167.172.219.213/tcp/4001",
      "agent": "spacedatanetwork/1.0.3",
      "feeds": ["CAT", "OMM", "SPW"],
      "epmCid": "bafkreiekghfegduqfol5jemuagc7rpqnvfw5ilk67d5nybhred6ubfxwr4"
    }
  ],
  "standards": [
    { "id": "CAT", "label": "Satellite Catalog", "rows": 8462, "state": "synced" },
    { "id": "EPM", "label": "Entity Profile Metadata", "rows": 44, "state": "synced" },
    { "id": "MPE", "label": "Maneuver Ephemeris", "rows": 18, "state": "encrypted" },
    { "id": "OMM", "label": "Orbit Mean-Elements Message", "rows": 9120, "state": "synced" },
    { "id": "PNM", "label": "Provider Navigation Message", "rows": 236, "state": "synced" },
    { "id": "SPW", "label": "Space Weather", "rows": 96, "state": "fresh" }
  ],
  "channels": [
    {
      "id": "mpe-screening-alpha",
      "standard": "MPE",
      "visibility": "private",
      "subscription": "active",
      "grant": "granted",
      "encryption": "sealed",
      "recipient": "SpaceAware.io CA Assessor"
    },
    {
      "id": "provider-pnm-sync",
      "standard": "PNM",
      "visibility": "controlled",
      "subscription": "active",
      "grant": "not required",
      "encryption": "signed",
      "recipient": "Local SDN Node"
    }
  ],
  "conjunction": {
    "mode": "private-maneuver-ephemeris",
    "primary": "SpaceAware MPE grant",
    "secondary": "CelesTrak public catalog",
    "assessor": "SpaceAware.io CA Assessor",
    "module": "sdn-ca-screen/1.0.0",
    "resultChannel": "ca-results-private",
    "rows": [
      { "object": "SAT-44713", "tca": "2026-06-25T18:42:00Z", "missDistanceKm": 1.84, "pc": "2.1e-5", "state": "review" },
      { "object": "SAT-57944", "tca": "2026-06-26T03:10:00Z", "missDistanceKm": 8.92, "pc": "4.8e-7", "state": "clear" }
    ],
    "provenance": {
      "grant": "grant-mpe-alpha",
      "queryHash": "sha256:designerqueryexample",
      "resultHash": "sha256:designerresultexample"
    }
  }
};

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

let fixtures = null;
let state = {
  route: normalizeRoute(location.hash),
  selectedPeerId: '',
  selectedChannelId: '',
  outputMode: 'table',
  dataQuery: ''
};

const screen = document.querySelector('#screen');
const screenTitle = document.querySelector('#screen-title');

async function loadFixtures() {
  try {
    const response = await fetch('./data/fixtures.json');
    if (!response.ok) throw new Error(`fixture load failed: ${response.status}`);
    return await response.json();
  } catch {
    return fallbackFixtures;
  }
}

async function init() {
  fixtures = await loadFixtures();
  state.selectedPeerId = fixtures.peers[0]?.id ?? '';
  state.selectedChannelId = fixtures.channels[0]?.id ?? '';
  document.querySelectorAll('[data-route]').forEach((button) => {
    button.addEventListener('click', () => navigate(button.dataset.route));
  });
  window.addEventListener('hashchange', () => {
    state.route = normalizeRoute(location.hash);
    render();
  });
  render();
}

function normalizeRoute(hash) {
  const route = String(hash || '#node').replace(/^#\/?/, '');
  return routes.includes(route) ? route : 'node';
}

function navigate(route) {
  location.hash = `#/${route}`;
}

function setOutputMode(mode) {
  state.outputMode = mode;
  render();
}

function render() {
  document.querySelectorAll('[data-route]').forEach((button) => {
    button.classList.toggle('active', button.dataset.route === state.route);
  });
  screenTitle.textContent = titles[state.route];
  screen.dataset.screen = state.route;
  screen.innerHTML = renderCurrentScreen();
  attachScreenHandlers();
}

function renderCurrentScreen() {
  if (state.route === 'peers') return renderPeers();
  if (state.route === 'data') return renderData();
  if (state.route === 'channels') return renderChannels();
  if (state.route === 'conjunction') return renderConjunction();
  return renderNode();
}

function renderNode() {
  const node = fixtures.node;
  return `
    <div class="grid">
      <section class="panel span-4">
        <h2>Node Health</h2>
        <div class="metric">${escapeHtml(node.status)}</div>
        <p class="muted">Mode: ${escapeHtml(node.mode)}</p>
        <dl class="detail-list">
          <div><dt>Peer ID</dt><dd class="mono">${escapeHtml(node.peerId)}</dd></div>
          <div><dt>API</dt><dd>${escapeHtml(node.api)}</dd></div>
          <div><dt>Gateway</dt><dd>${escapeHtml(node.gateway)}</dd></div>
        </dl>
      </section>
      <section class="panel span-4">
        <h2>Identity</h2>
        <div class="metric">${escapeHtml(node.identity.state)}</div>
        <p class="muted">${escapeHtml(node.identity.entity)}</p>
        <dl class="detail-list">
          <div><dt>EPM CID</dt><dd class="mono">${escapeHtml(node.identity.epmCid)}</dd></div>
          <div><dt>vCard</dt><dd>${escapeHtml(node.identity.vcard)}</dd></div>
        </dl>
        <div class="actions">
          <button class="button">JSON</button>
          <button class="button">CSV</button>
          <button class="button">vCard</button>
          <button class="button">QR</button>
        </div>
      </section>
      <section class="panel span-4">
        <h2>Service</h2>
        <div class="metric">${escapeHtml(node.service.state)}</div>
        <p class="muted">${escapeHtml(node.service.update)}</p>
        <dl class="detail-list">
          <div><dt>Autostart</dt><dd>${escapeHtml(node.service.autostart ? 'enabled' : 'disabled')}</dd></div>
          <div><dt>Storage</dt><dd>${escapeHtml(node.storage)}</dd></div>
        </dl>
        <div class="actions">
          <button class="button primary">Restart</button>
          <button class="button">Stop</button>
          <button class="button">Check update</button>
        </div>
      </section>
    </div>
  `;
}

function renderPeers() {
  const selected = fixtures.peers.find((peer) => peer.id === state.selectedPeerId) ?? fixtures.peers[0];
  return `
    <div class="grid">
      <section class="panel span-8">
        <h2>Trusted And Observed Peers</h2>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Name</th><th>Trust</th><th>Feeds</th><th>Address</th></tr></thead>
            <tbody>
              ${fixtures.peers.map((peer) => `
                <tr data-selectable data-peer-id="${escapeAttribute(peer.id)}" class="${peer.id === selected.id ? 'selected' : ''}">
                  <td><strong>${escapeHtml(peer.name)}</strong><br><span class="muted mono">${escapeHtml(peer.id)}</span></td>
                  <td>${escapeHtml(peer.trust)}</td>
                  <td>${peer.feeds.map((feed) => escapeHtml(feed)).join(', ')}</td>
                  <td class="mono">${escapeHtml(peer.addr)}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-4">
        <h2>Provider Detail</h2>
        <dl class="detail-list">
          <div><dt>Name</dt><dd>${escapeHtml(selected.name)}</dd></div>
          <div><dt>Role</dt><dd>${escapeHtml(selected.role)}</dd></div>
          <div><dt>Agent</dt><dd>${escapeHtml(selected.agent)}</dd></div>
          <div><dt>EPM CID</dt><dd class="mono">${escapeHtml(selected.epmCid)}</dd></div>
        </dl>
        <div class="actions">
          <button class="button primary">Connect</button>
          <button class="button">vCard</button>
          <button class="button">QR</button>
        </div>
      </section>
    </div>
  `;
}

function renderData() {
  return `
    <div class="grid">
      <section class="panel span-12">
        <h2>Data Workbench</h2>
        <div class="filters">
          <input class="input" id="data-query" value="${escapeAttribute(state.dataQuery)}" placeholder="Search providers, standards, schemas">
          <select class="select"><option>All providers</option><option>SpaceAware.io</option><option>CelesTrak Provider</option></select>
          <button class="button primary">Search</button>
        </div>
      </section>
      <section class="panel span-7">
        <h2>Standards</h2>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Standard</th><th>Rows</th><th>State</th></tr></thead>
            <tbody>
              ${fixtures.standards.map((standard) => `
                <tr><td><strong>${escapeHtml(standard.id)}</strong><br><span class="muted">${escapeHtml(standard.label)}</span></td><td>${escapeHtml(standard.rows.toLocaleString())}</td><td>${escapeHtml(standard.state)}</td></tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-5">
        <h2>Query Output</h2>
        ${renderOutputModeControls()}
        <textarea readonly>${renderDataOutput()}</textarea>
      </section>
    </div>
  `;
}

function renderChannels() {
  const selected = fixtures.channels.find((channel) => channel.id === state.selectedChannelId) ?? fixtures.channels[0];
  return `
    <div class="grid">
      <section class="panel span-7">
        <h2>Encrypted Channels</h2>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Channel</th><th>Standard</th><th>Grant</th><th>Encryption</th></tr></thead>
            <tbody>
              ${fixtures.channels.map((channel) => `
                <tr data-selectable data-channel-id="${escapeAttribute(channel.id)}" class="${channel.id === selected.id ? 'selected' : ''}">
                  <td>${escapeHtml(channel.id)}<br><span class="muted">${escapeHtml(channel.recipient)}</span></td>
                  <td>${escapeHtml(channel.standard)}</td>
                  <td>${escapeHtml(channel.grant)}</td>
                  <td>${escapeHtml(channel.encryption)}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-5">
        <h2>Channel Monitor</h2>
        <dl class="detail-list">
          <div><dt>Selected channel</dt><dd>${escapeHtml(selected.id)}</dd></div>
          <div><dt>Visibility</dt><dd>${escapeHtml(selected.visibility)}</dd></div>
          <div><dt>Subscription</dt><dd>${escapeHtml(selected.subscription)}</dd></div>
          <div><dt>Recipient</dt><dd>${escapeHtml(selected.recipient)}</dd></div>
        </dl>
        <div class="actions">
          <button class="button primary">Open stream</button>
          <button class="button">Issue grant</button>
          <button class="button">Key envelope</button>
        </div>
      </section>
    </div>
  `;
}

function renderConjunction() {
  const result = fixtures.conjunction;
  return `
    <div class="grid">
      <section class="panel span-12">
        <h2>Private Maneuver Ephemeris Screening</h2>
        <p class="muted">Screen maneuvers without broadcasting maneuver intent to competitors.</p>
        <div class="filters">
          <select class="select"><option>${escapeHtml(result.primary)}</option></select>
          <select class="select"><option>${escapeHtml(result.secondary)}</option></select>
          <input class="input" value="${escapeAttribute(result.assessor)}">
          <button class="button primary">Screen</button>
        </div>
      </section>
      <section class="panel span-7">
        <h2>Results</h2>
        ${renderOutputModeControls()}
        ${state.outputMode === 'table' ? renderConjunctionTable() : `<textarea readonly>${renderConjunctionOutput()}</textarea>`}
      </section>
      <section class="panel span-5">
        <h2>Provenance</h2>
        <dl class="detail-list">
          <div><dt>Mode</dt><dd>${escapeHtml(result.mode)}</dd></div>
          <div><dt>Module</dt><dd>${escapeHtml(result.module)}</dd></div>
          <div><dt>Result channel</dt><dd>${escapeHtml(result.resultChannel)}</dd></div>
          <div><dt>Grant</dt><dd>${escapeHtml(result.provenance.grant)}</dd></div>
          <div><dt>Query hash</dt><dd class="mono">${escapeHtml(result.provenance.queryHash)}</dd></div>
        </dl>
      </section>
    </div>
  `;
}

function renderOutputModeControls() {
  return `
    <div class="segment">
      ${['table', 'json', 'csv'].map((mode) => `<button class="button ${state.outputMode === mode ? 'primary' : ''}" data-output-mode="${mode}" type="button">${mode.toUpperCase()}</button>`).join('')}
    </div>
  `;
}

function renderDataOutput() {
  if (state.outputMode === 'json') return escapeHtml(JSON.stringify(fixtures.standards, null, 2));
  if (state.outputMode === 'csv') return escapeHtml(['id,label,rows,state', ...fixtures.standards.map((standard) => `${standard.id},${standard.label},${standard.rows},${standard.state}`)].join('\n'));
  return 'Select JSON or CSV to preview export data.';
}

function renderConjunctionTable() {
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Object</th><th>TCA</th><th>Miss km</th><th>Pc</th><th>State</th></tr></thead>
        <tbody>
          ${fixtures.conjunction.rows.map((row) => `<tr><td>${escapeHtml(row.object)}</td><td>${escapeHtml(row.tca)}</td><td>${escapeHtml(row.missDistanceKm)}</td><td>${escapeHtml(row.pc)}</td><td>${escapeHtml(row.state)}</td></tr>`).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function renderConjunctionOutput() {
  if (state.outputMode === 'json') return escapeHtml(JSON.stringify(fixtures.conjunction.rows, null, 2));
  if (state.outputMode === 'csv') return escapeHtml(['object,tca,missDistanceKm,pc,state', ...fixtures.conjunction.rows.map((row) => `${row.object},${row.tca},${row.missDistanceKm},${row.pc},${row.state}`)].join('\n'));
  return '';
}

function attachScreenHandlers() {
  document.querySelectorAll('[data-peer-id]').forEach((row) => {
    row.addEventListener('click', () => {
      state.selectedPeerId = row.dataset.peerId;
      render();
    });
  });
  document.querySelectorAll('[data-channel-id]').forEach((row) => {
    row.addEventListener('click', () => {
      state.selectedChannelId = row.dataset.channelId;
      render();
    });
  });
  document.querySelectorAll('[data-output-mode]').forEach((button) => {
    button.addEventListener('click', () => setOutputMode(button.dataset.outputMode));
  });
  const dataQuery = document.querySelector('#data-query');
  if (dataQuery) {
    dataQuery.addEventListener('input', () => {
      state.dataQuery = dataQuery.value;
    });
  }
}

void init();
