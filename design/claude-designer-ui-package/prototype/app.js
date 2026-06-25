const routes = ['node', 'peers', 'data', 'channels', 'conjunction'];
const titles = {
  node: 'Node',
  peers: 'Peers',
  data: 'Data',
  channels: 'Channels',
  conjunction: 'Conjunction'
};

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

async function init() {
  fixtures = await fetch('./data/fixtures.json').then((response) => response.json());
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
        <div class="metric">${node.status}</div>
        <p class="muted">Mode: ${node.mode}</p>
        <dl class="detail-list">
          <div><dt>Peer ID</dt><dd class="mono">${node.peerId}</dd></div>
          <div><dt>API</dt><dd>${node.api}</dd></div>
          <div><dt>Gateway</dt><dd>${node.gateway}</dd></div>
        </dl>
      </section>
      <section class="panel span-4">
        <h2>Identity</h2>
        <div class="metric">${node.identity.state}</div>
        <p class="muted">${node.identity.entity}</p>
        <dl class="detail-list">
          <div><dt>EPM CID</dt><dd class="mono">${node.identity.epmCid}</dd></div>
          <div><dt>vCard</dt><dd>${node.identity.vcard}</dd></div>
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
        <div class="metric">${node.service.state}</div>
        <p class="muted">${node.service.update}</p>
        <dl class="detail-list">
          <div><dt>Autostart</dt><dd>${node.service.autostart ? 'enabled' : 'disabled'}</dd></div>
          <div><dt>Storage</dt><dd>${node.storage}</dd></div>
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
                <tr data-selectable data-peer-id="${peer.id}" class="${peer.id === selected.id ? 'selected' : ''}">
                  <td><strong>${peer.name}</strong><br><span class="muted mono">${peer.id}</span></td>
                  <td>${peer.trust}</td>
                  <td>${peer.feeds.join(', ')}</td>
                  <td class="mono">${peer.addr}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-4">
        <h2>Provider Detail</h2>
        <dl class="detail-list">
          <div><dt>Name</dt><dd>${selected.name}</dd></div>
          <div><dt>Role</dt><dd>${selected.role}</dd></div>
          <div><dt>Agent</dt><dd>${selected.agent}</dd></div>
          <div><dt>EPM CID</dt><dd class="mono">${selected.epmCid}</dd></div>
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
          <input class="input" id="data-query" value="${state.dataQuery}" placeholder="Search providers, standards, schemas">
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
                <tr><td><strong>${standard.id}</strong><br><span class="muted">${standard.label}</span></td><td>${standard.rows.toLocaleString()}</td><td>${standard.state}</td></tr>
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
                <tr data-selectable data-channel-id="${channel.id}" class="${channel.id === selected.id ? 'selected' : ''}">
                  <td>${channel.id}<br><span class="muted">${channel.recipient}</span></td>
                  <td>${channel.standard}</td>
                  <td>${channel.grant}</td>
                  <td>${channel.encryption}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-5">
        <h2>Channel Monitor</h2>
        <dl class="detail-list">
          <div><dt>Selected channel</dt><dd>${selected.id}</dd></div>
          <div><dt>Visibility</dt><dd>${selected.visibility}</dd></div>
          <div><dt>Subscription</dt><dd>${selected.subscription}</dd></div>
          <div><dt>Recipient</dt><dd>${selected.recipient}</dd></div>
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
          <select class="select"><option>${result.primary}</option></select>
          <select class="select"><option>${result.secondary}</option></select>
          <input class="input" value="${result.assessor}">
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
          <div><dt>Mode</dt><dd>${result.mode}</dd></div>
          <div><dt>Module</dt><dd>${result.module}</dd></div>
          <div><dt>Result channel</dt><dd>${result.resultChannel}</dd></div>
          <div><dt>Grant</dt><dd>${result.provenance.grant}</dd></div>
          <div><dt>Query hash</dt><dd class="mono">${result.provenance.queryHash}</dd></div>
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
  if (state.outputMode === 'json') return JSON.stringify(fixtures.standards, null, 2);
  if (state.outputMode === 'csv') return ['id,label,rows,state', ...fixtures.standards.map((standard) => `${standard.id},${standard.label},${standard.rows},${standard.state}`)].join('\n');
  return 'Select JSON or CSV to preview export data.';
}

function renderConjunctionTable() {
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Object</th><th>TCA</th><th>Miss km</th><th>Pc</th><th>State</th></tr></thead>
        <tbody>
          ${fixtures.conjunction.rows.map((row) => `<tr><td>${row.object}</td><td>${row.tca}</td><td>${row.missDistanceKm}</td><td>${row.pc}</td><td>${row.state}</td></tr>`).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function renderConjunctionOutput() {
  if (state.outputMode === 'json') return JSON.stringify(fixtures.conjunction.rows, null, 2);
  if (state.outputMode === 'csv') return ['object,tca,missDistanceKm,pc,state', ...fixtures.conjunction.rows.map((row) => `${row.object},${row.tca},${row.missDistanceKm},${row.pc},${row.state}`)].join('\n');
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
