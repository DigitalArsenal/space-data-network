import {
  ecosystemShapeLegend,
  type EcosystemEdge,
  type EcosystemItem,
  type EcosystemShape,
  type EcosystemState,
} from './model';

interface GraphPoint {
  x: number;
  y: number;
}

const graphPoints: Record<string, GraphPoint> = {
  'node-browser': { x: 90, y: 250 },
  'node-celestrak': { x: 220, y: 105 },
  'data-omm': { x: 360, y: 210 },
  'data-dpm': { x: 360, y: 105 },
  'data-pnm': { x: 360, y: 300 },
  'node-consumer': { x: 570, y: 300 },
  'node-pinning': { x: 570, y: 105 },
  'node-archive': { x: 720, y: 105 },
  'node-spaceaware': { x: 220, y: 420 },
  'badge-plg': { x: 360, y: 420 },
  'module-sgp4': { x: 520, y: 420 },
  'badge-enc': { x: 680, y: 420 },
};

export function renderNetworkEcosystemDemo(state: EcosystemState): string {
  const selectedItem = state.items.find((item) => item.id === state.selectedItemId) ?? state.items[0];

  return `<div class="ecosystem-demo" data-mode="${escapeHtml(state.mode)}">
  <header class="ecosystem-demo__toolbar" aria-label="Network ecosystem controls">
    <div class="ecosystem-demo__mode-controls" role="group" aria-label="Mode">
      <button type="button" data-action="set-sandbox" aria-pressed="${state.mode === 'sandbox'}">Sandbox mode</button>
      <button type="button" data-action="set-live" aria-pressed="${state.mode === 'live'}">Live mode</button>
    </div>
    <div class="ecosystem-demo__scenario-controls" role="group" aria-label="Scenario actions">
      <button type="button" data-action="create-test-data">Create test OMM</button>
      <button type="button" data-action="subscribe-channel">Subscribe</button>
      <button type="button" data-action="pin-product">Pin</button>
    </div>
    <div class="ecosystem-demo__module-controls" role="group" aria-label="Module actions">
      <label>Module name <input type="text" data-module-name value="${escapeHtml(
        state.moduleListings.at(-1)?.name ?? 'Demo SGP4',
      )}" /></label>
      <button type="button" data-action="create-module-listing">Create module</button>
      <button type="button" data-action="simulate-module-invocation">Run module</button>
    </div>
  </header>
  <section class="ecosystem-demo__graph-panel" aria-label="Network graph">
    ${renderGraph(state)}
  </section>
  <section class="ecosystem-demo__legend" aria-label="Shape legend">
    <h2>Shape legend</h2>
    <ul>
      ${ecosystemShapeLegend.map(renderLegendEntry).join('')}
    </ul>
  </section>
  ${renderSelectedItemDetail(selectedItem, state)}
  ${renderEventLog(state)}
</div>`;
}

function renderGraph(state: EcosystemState): string {
  return `<svg data-ecosystem-graph viewBox="0 0 820 520" role="img" aria-label="SDN network ecosystem graph" xmlns="http://www.w3.org/2000/svg">
  <g class="ecosystem-demo__edges">
    ${state.edges.map(renderEdge).join('')}
  </g>
  <g class="ecosystem-demo__items">
    ${state.items.map((item) => renderItem(item, item.id === state.selectedItemId)).join('')}
  </g>
</svg>`;
}

function renderEdge(edge: EcosystemEdge): string {
  const from = graphPoints[edge.from];
  const to = graphPoints[edge.to];

  if (!from || !to) {
    return '';
  }

  const midpoint = {
    x: Math.round((from.x + to.x) / 2),
    y: Math.round((from.y + to.y) / 2) - 8,
  };
  const dashAttribute = edge.style === 'dashed' ? ' stroke-dasharray="6 5"' : '';

  return `<g class="ecosystem-demo__edge" data-edge-id="${escapeHtml(edge.id)}">
    <line x1="${from.x}" y1="${from.y}" x2="${to.x}" y2="${to.y}" stroke="currentColor" stroke-width="2"${dashAttribute} />
    <text x="${midpoint.x}" y="${midpoint.y}" text-anchor="middle" font-size="11">${escapeHtml(edge.label)}</text>
  </g>`;
}

function renderItem(item: EcosystemItem, selected: boolean): string {
  const point = graphPoints[item.id] ?? { x: 410, y: 260 };

  return `<g class="ecosystem-demo__item" data-item-id="${escapeHtml(item.id)}" data-item-kind="${escapeHtml(
    item.kind,
  )}" data-selected="${selected}" transform="translate(${point.x} ${point.y})" role="button" tabindex="0">
    <title>${escapeHtml(item.title)}</title>
    ${renderItemShape(item)}
    <text x="0" y="42" text-anchor="middle" font-size="12">${escapeHtml(shortGraphLabel(item.title))}</text>
  </g>`;
}

function renderItemShape(item: EcosystemItem): string {
  const selectedStroke = item.status ? ' stroke-width="3"' : '';

  if (item.shape === 'circle') {
    return `<circle cx="0" cy="0" r="24" fill="none" stroke="currentColor"${selectedStroke} />`;
  }

  if (item.shape === 'triangle') {
    return `<polygon points="0,-28 26,20 -26,20" fill="none" stroke="currentColor"${selectedStroke} />`;
  }

  if (item.shape === 'square') {
    return `<rect x="-24" y="-24" width="48" height="48" rx="4" fill="none" stroke="currentColor"${selectedStroke} />`;
  }

  return `<polygon points="0,-28 28,0 0,28 -28,0" fill="none" stroke="currentColor"${selectedStroke} />`;
}

function renderLegendEntry(entry: (typeof ecosystemShapeLegend)[number]): string {
  const shapeName = shapeDisplayName(entry.shape);

  return `<li>
    <strong>${escapeHtml(shapeName)}</strong>
    <span>${escapeHtml(entry.label)}</span>
    <span>${escapeHtml(entry.description)}</span>
  </li>`;
}

function renderSelectedItemDetail(item: EcosystemItem | undefined, state: EcosystemState): string {
  if (!item) {
    return `<section class="ecosystem-demo__details" aria-label="Selected item detail">
  <h2>No item selected</h2>
</section>`;
  }

  return `<section class="ecosystem-demo__details" aria-label="Selected item detail">
  <h2>${escapeHtml(item.title)}</h2>
  <p>${escapeHtml(item.description)}</p>
  <dl>
    <dt>Type</dt>
    <dd>${escapeHtml(capitalize(item.kind))}</dd>
    <dt>Shape</dt>
    <dd>${escapeHtml(shapeDisplayName(item.shape))}</dd>
    <dt>Mode</dt>
    <dd>${escapeHtml(capitalize(state.mode))}</dd>
    ${item.status ? `<dt>Status</dt><dd>${escapeHtml(item.status)}</dd>` : ''}
  </dl>
</section>`;
}

function renderEventLog(state: EcosystemState): string {
  const entries =
    state.events.length > 0
      ? state.events.map((event) => `<li><strong>${escapeHtml(event.title)}</strong><span>${escapeHtml(event.detail)}</span></li>`).join('')
      : '<li><span>No events yet.</span></li>';

  return `<section class="ecosystem-demo__events" aria-label="Event log">
  <h2>Event log</h2>
  <ol>${entries}</ol>
</section>`;
}

function shapeDisplayName(shape: EcosystemShape): string {
  if (shape === 'circle') {
    return 'Circle';
  }
  if (shape === 'triangle') {
    return 'Triangle';
  }
  if (shape === 'square') {
    return 'Square';
  }
  return 'Diamond';
}

function shortGraphLabel(value: string): string {
  return value
    .replace(/\bSDN\b/g, '')
    .replace(/\bNode\b/g, '')
    .replace(/\bRecord\b/g, '')
    .replace(/\bProduct\b/g, '')
    .replace(/\bModule\b/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function capitalize(value: string): string {
  return value.length === 0 ? value : `${value[0].toUpperCase()}${value.slice(1)}`;
}

function escapeHtml(value: unknown): string {
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}
