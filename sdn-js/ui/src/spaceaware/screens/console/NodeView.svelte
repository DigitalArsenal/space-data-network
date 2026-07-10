<script lang="ts">
  /**
   * NODE dashboard (loop U3.1). Ground truth: the `<!-- ==== NODE ==== -->`
   * block in `SDN Console.dc.html` — a 12-col widget grid with an EDIT MODE
   * (drag-reorder, span cycle, remove, add-from-catalog, reset), layout
   * persisted to `localStorage['sdn_node_layout_v1']`.
   *
   * All widget BODIES render `TYPED PLACEHOLDER data (lib/console.ts)` —
   * real wiring (node/info, node/epm, stats, activity feed, throughput
   * counters) is loop task U3.2. The PEER MAP widget's actual canvas globe
   * (`globe.js`) is explicitly OUT of scope here too — that's U3.4 — so it
   * only shows its static caption chrome (connection/country counts,
   * 3D/2D tab state) around an inert canvas.
   *
   * SERVICE widget decision (D6, `SPACEAWARE_UI_WIRING_ANALYSIS.md`):
   * daemon lifecycle (restart/stop) has no HTTP control surface and the
   * wiring doc recommends read-only status v1 — so unlike the mock (which
   * fakes a restart animation), RESTART/STOP/CHECK render disabled with an
   * honest tooltip instead of a working mock action.
   */
  import { onMount } from 'svelte';
  import {
    NODE_ACTIVITY_PLACEHOLDER,
    NODE_DEFAULT_LAYOUT,
    NODE_HEALTH_PLACEHOLDER,
    NODE_IDENTITY_PLACEHOLDER,
    NODE_NETMAP_PLACEHOLDER,
    NODE_PEER_SUMMARY_PLACEHOLDER,
    NODE_SERVICE_PLACEHOLDER,
    NODE_STORAGE_PLACEHOLDER,
    NODE_THROUGHPUT_SPARK,
    NODE_WIDGETS,
    addNodeWidget,
    availableNodeWidgets,
    cycleWidgetSpan,
    loadNodeLayout,
    nodeMapTabStyle,
    removeNodeWidget,
    reorderNodeLayout,
    resetNodeLayout,
    saveNodeLayout,
    throughputBarGradient,
    widgetSpanLabel,
    type NodeLayout,
    type NodeMapMode,
    type NodeWidgetId,
  } from '../../lib/console';

  let { onOpenQr }: { onOpenQr: () => void } = $props();

  let layout = $state<NodeLayout>(NODE_DEFAULT_LAYOUT.map((w) => ({ ...w })));
  let editMode = $state(false);
  let mapMode = $state<NodeMapMode>('3d');

  // Native HTML5 drag-and-drop source id — plain (non-reactive) like the
  // mock's `this._dragId`, since it only matters mid-gesture.
  let dragId: NodeWidgetId | null = null;

  onMount(() => {
    try {
      layout = loadNodeLayout(window.localStorage);
    } catch {
      // localStorage unavailable — keep the in-memory default layout.
    }
  });

  function persist(next: NodeLayout) {
    layout = next;
    try {
      saveNodeLayout(window.localStorage, next);
    } catch {
      // localStorage unavailable — layout still works for this session.
    }
  }

  function toggleEdit() {
    editMode = !editMode;
  }

  function handleReset() {
    persist(resetNodeLayout());
  }

  function handleCycle(id: NodeWidgetId) {
    persist(cycleWidgetSpan(layout, id));
  }

  function handleRemove(id: NodeWidgetId) {
    persist(removeNodeWidget(layout, id));
  }

  function handleAdd(id: NodeWidgetId) {
    persist(addNodeWidget(layout, id));
  }

  function handleDragStart(id: NodeWidgetId) {
    dragId = id;
  }

  function handleDragEnter(id: NodeWidgetId) {
    if (!dragId || dragId === id) return;
    persist(reorderNodeLayout(layout, dragId, id));
  }

  function handleDragOver(event: DragEvent) {
    event.preventDefault();
  }

  function handleDragEnd() {
    dragId = null;
  }

  const availableWidgets = $derived(availableNodeWidgets(layout));
  const showAddTray = $derived(editMode && availableWidgets.length > 0);
  const mapTabStyle = $derived(nodeMapTabStyle(mapMode));
</script>

{#snippet cornerBrackets(color: string)}
  <div class="sdn-widget-corner sdn-widget-corner--tl" style={`border-color:${color};`}></div>
  <div class="sdn-widget-corner sdn-widget-corner--tr" style={`border-color:${color};`}></div>
  <div class="sdn-widget-corner sdn-widget-corner--bl" style={`border-color:${color};`}></div>
  <div class="sdn-widget-corner sdn-widget-corner--br" style={`border-color:${color};`}></div>
{/snippet}

<div class="sdn-node-toolbar">
  <span class="sdn-node-toolbar-kicker">DASHBOARD</span>
  <span class="sdn-node-toolbar-spacer"></span>
  {#if editMode}
    <span class="sdn-node-toolbar-hint">DRAG TO REORDER · ⤢ RESIZE · ✕ REMOVE</span>
    <button type="button" class="sdn-btn-ghost" title="Restore the default widget layout" onclick={handleReset}>
      RESET
    </button>
  {/if}
  <button
    type="button"
    class="sdn-btn-edit"
    class:is-active={editMode}
    title={editMode ? 'Finish editing the dashboard layout' : 'Edit the dashboard layout'}
    onclick={toggleEdit}
  >
    {editMode ? 'DONE' : 'EDIT LAYOUT'}
  </button>
</div>

<div class="sdn-node-grid">
  {#each layout as w (w.id)}
    <section
      class="sdn-widget"
      class:is-editing={editMode}
      style={`grid-column:span ${w.span};`}
      draggable={editMode}
      role="group"
      aria-label={NODE_WIDGETS[w.id].title}
      ondragstart={() => handleDragStart(w.id)}
      ondragenter={() => handleDragEnter(w.id)}
      ondragover={handleDragOver}
      ondragend={handleDragEnd}
    >
      {#if editMode}
        <div class="sdn-widget-editbar">
          <span class="sdn-widget-drag-handle" title="Drag to reorder">⠿</span>
          <button type="button" class="sdn-widget-cycle" title="Resize" onclick={() => handleCycle(w.id)}>
            ⤢ {widgetSpanLabel(w.span)}
          </button>
          <button type="button" class="sdn-widget-remove" title="Remove" onclick={() => handleRemove(w.id)}>✕</button>
        </div>
      {/if}

      {#if w.id === 'health'}
        {@render cornerBrackets('#4aa6e0')}
        <div class="sdn-widget-title">NODE HEALTH</div>
        <div class="sdn-widget-status-row">
          <span class="sdn-status-dot" style="background:#5ad6a0;box-shadow:0 0 9px #5ad6a0;"></span>
          <span class="sdn-widget-status-text">ONLINE</span>
        </div>
        <div class="sdn-widget-status-sub">{NODE_HEALTH_PLACEHOLDER.mode}</div>
        <div class="sdn-widget-field-stack">
          <div>
            <div class="sdn-widget-field-label">PEER ID</div>
            <div class="sdn-widget-field-value sdn-widget-field-value--wrap">{NODE_HEALTH_PLACEHOLDER.peerId}</div>
          </div>
          <div class="sdn-widget-field-row">
            <div class="sdn-widget-field-flex">
              <div class="sdn-widget-field-label">API</div>
              <div class="sdn-widget-field-value">{NODE_HEALTH_PLACEHOLDER.api}</div>
            </div>
            <div class="sdn-widget-field-flex">
              <div class="sdn-widget-field-label">GATEWAY</div>
              <div class="sdn-widget-field-value">{NODE_HEALTH_PLACEHOLDER.gateway}</div>
            </div>
          </div>
          <div class="sdn-widget-storage">
            <div class="sdn-widget-storage-row">
              <span class="sdn-widget-field-label">STORAGE</span>
              <span class="sdn-widget-storage-value"
                >{NODE_HEALTH_PLACEHOLDER.storageUsed}
                <span class="sdn-widget-storage-total">/ {NODE_HEALTH_PLACEHOLDER.storageTotal}</span></span
              >
            </div>
            <div class="sdn-widget-bar-track">
              <div class="sdn-widget-bar-fill" style={`width:${NODE_HEALTH_PLACEHOLDER.storagePercent}%;`}></div>
            </div>
          </div>
        </div>
      {:else if w.id === 'identity'}
        <div class="sdn-widget-header-row">
          <span class="sdn-widget-title">IDENTITY</span>
          <span class="sdn-widget-badge sdn-widget-badge--confirmed">CONFIRMED</span>
        </div>
        <div class="sdn-widget-heading">{NODE_IDENTITY_PLACEHOLDER.name}</div>
        <div class="sdn-widget-subheading">{NODE_IDENTITY_PLACEHOLDER.subtitle}</div>
        <div class="sdn-widget-field-stack">
          <div>
            <div class="sdn-widget-field-label">EPM CID</div>
            <div class="sdn-widget-field-value sdn-widget-field-value--wrap sdn-widget-field-value--dense">
              {NODE_IDENTITY_PLACEHOLDER.epmCid}
            </div>
          </div>
          <div>
            <div class="sdn-widget-field-label">vCARD</div>
            <div class="sdn-widget-field-value">{NODE_IDENTITY_PLACEHOLDER.vcard}</div>
          </div>
        </div>
        <div class="sdn-widget-button-row">
          <button type="button" class="sdn-btn-ghost sdn-btn-flex" title="Export identity as JSON">JSON</button>
          <button type="button" class="sdn-btn-ghost sdn-btn-flex" title="Export identity as CSV">CSV</button>
          <button type="button" class="sdn-btn-ghost sdn-btn-flex" title="Export identity as vCARD">vCARD</button>
          <button
            type="button"
            class="sdn-btn-accent sdn-btn-flex"
            title="Show EPM/vCARD QR code"
            onclick={onOpenQr}
          >
            QR
          </button>
        </div>
      {:else if w.id === 'service'}
        <div class="sdn-widget-title">SERVICE</div>
        <div class="sdn-widget-status-row">
          <span class="sdn-status-dot" style="background:#5ad6a0;box-shadow:0 0 9px #5ad6a0;"></span>
          <span class="sdn-widget-status-text sdn-widget-status-text--service">{NODE_SERVICE_PLACEHOLDER.state}</span>
        </div>
        <div class="sdn-widget-status-sub sdn-widget-status-sub--plain">{NODE_SERVICE_PLACEHOLDER.version}</div>
        <div class="sdn-widget-field-row sdn-widget-field-row--gap">
          <div class="sdn-widget-field-flex">
            <div class="sdn-widget-field-label">AUTOSTART</div>
            <div class="sdn-widget-field-value sdn-widget-field-value--green">{NODE_SERVICE_PLACEHOLDER.autostart}</div>
          </div>
          <div class="sdn-widget-field-flex">
            <div class="sdn-widget-field-label">UPTIME</div>
            <div class="sdn-widget-field-value">{NODE_SERVICE_PLACEHOLDER.uptime}</div>
          </div>
        </div>
        <div class="sdn-widget-button-row">
          <button
            type="button"
            class="sdn-btn-accent sdn-btn-flex"
            disabled
            title="Daemon lifecycle control is not available from this UI (systemd/desktop owns start/stop)"
          >
            RESTART
          </button>
          <button
            type="button"
            class="sdn-btn-danger sdn-btn-flex"
            disabled
            title="Daemon lifecycle control is not available from this UI (systemd/desktop owns start/stop)"
          >
            STOP
          </button>
          <button
            type="button"
            class="sdn-btn-ghost sdn-btn-flex"
            disabled
            title="Daemon lifecycle control is not available from this UI (systemd/desktop owns start/stop)"
          >
            CHECK
          </button>
        </div>
      {:else if w.id === 'netmap'}
        {@render cornerBrackets('#35c9d8')}
        <div class="sdn-netmap-header">
          <div class="sdn-netmap-header-left">
            <span class="sdn-widget-title">PEER MAP</span>
            <span class="sdn-netmap-header-tag">GEOIP · LIVE SWARM</span>
          </div>
          <div class="sdn-netmap-header-right">
            <span class="sdn-netmap-links">
              <span class="sdn-status-dot sdn-status-dot--sm" style="background:#5ad6a0;box-shadow:0 0 6px #5ad6a0;"
              ></span>{NODE_NETMAP_PLACEHOLDER.connectionCount} LINKS
            </span>
            <div class="sdn-netmap-tabs">
              <button
                type="button"
                class="sdn-netmap-tab"
                style={`background:${mapTabStyle.background3d};color:${mapTabStyle.color3d};`}
                title="3D globe view"
                onclick={() => (mapMode = '3d')}
              >
                3D
              </button>
              <button
                type="button"
                class="sdn-netmap-tab sdn-netmap-tab--split"
                style={`background:${mapTabStyle.background2d};color:${mapTabStyle.color2d};`}
                title="2D map view"
                onclick={() => (mapMode = '2d')}
              >
                2D
              </button>
            </div>
          </div>
        </div>
        <div class="sdn-netmap-canvas-wrap">
          <!--
            Intentionally inert: the real canvas globe (globe.js port,
            `../../../lib/globe/SdnGlobe.ts`) is wired here in loop U3.4,
            not this task — see the file-level doc comment.
          -->
          <canvas class="sdn-netmap-canvas"></canvas>
          <div class="sdn-netmap-caption-tl">
            <div>{NODE_NETMAP_PLACEHOLDER.connectionCount} CONNECTIONS</div>
            <div>{NODE_NETMAP_PLACEHOLDER.countryCount} COUNTRIES</div>
          </div>
          <div class="sdn-netmap-caption-br">DRAG TO ROTATE</div>
        </div>
        <div class="sdn-netmap-legend">
          <div class="sdn-netmap-legend-items">
            <span class="sdn-netmap-legend-item"
              ><span class="sdn-status-dot sdn-status-dot--sm" style="background:#ffd089;box-shadow:0 0 6px #ffd089;"
              ></span>THIS NODE</span
            >
            <span class="sdn-netmap-legend-item"
              ><span class="sdn-status-dot sdn-status-dot--sm" style="background:#35c9d8;box-shadow:0 0 6px #35c9d8;"
              ></span>PROVIDERS</span
            >
            <span class="sdn-netmap-legend-item"
              ><span class="sdn-status-dot sdn-status-dot--sm" style="background:#9fd4f5;box-shadow:0 0 6px #9fd4f5;"
              ></span>PEERS</span
            >
            <span class="sdn-netmap-legend-item"
              ><span class="sdn-status-dot sdn-status-dot--sm" style="background:#5ad6a0;box-shadow:0 0 6px #5ad6a0;"
              ></span>CLIENTS</span
            >
          </div>
          <span class="sdn-netmap-legend-note">Locations · MaxMind GeoLite2</span>
        </div>
      {:else if w.id === 'throughput'}
        <div class="sdn-widget-title">NETWORK THROUGHPUT</div>
        <div class="sdn-throughput-row">
          <span class="sdn-throughput-value">3.42</span><span class="sdn-throughput-unit">MB/s ↓</span>
          <span class="sdn-throughput-value sdn-throughput-value--up">0.88</span
          ><span class="sdn-throughput-unit">↑</span>
        </div>
        <div class="sdn-throughput-bars">
          {#each NODE_THROUGHPUT_SPARK as h, i (i)}
            <div class="sdn-throughput-bar" style={`height:${h}%;background:${throughputBarGradient(i)};`}></div>
          {/each}
        </div>
        <div class="sdn-throughput-axis"><span>−60s</span><span>NOW</span></div>
      {:else if w.id === 'peersum'}
        <div class="sdn-widget-title">PEER SUMMARY</div>
        <div class="sdn-peersum-list">
          {#each NODE_PEER_SUMMARY_PLACEHOLDER as pm (pm.name)}
            <div class="sdn-peersum-row">
              <span class="sdn-status-dot sdn-status-dot--sm" style={`background:${pm.trustColor};box-shadow:0 0 6px ${pm.trustColor};`}
              ></span>
              <span class="sdn-peersum-name">{pm.name}</span>
              <span class="sdn-peersum-feeds">{pm.feeds}</span>
              <span class="sdn-peersum-trust" style={`color:${pm.trustColor};`}>{pm.trust}</span>
            </div>
          {/each}
        </div>
      {:else if w.id === 'storage'}
        <div class="sdn-widget-title">STORAGE · FLATSQL</div>
        <div class="sdn-storage-value-row">
          <span class="sdn-storage-value">{NODE_STORAGE_PLACEHOLDER.used}</span>
          <span class="sdn-storage-unit">/ {NODE_STORAGE_PLACEHOLDER.total}</span>
        </div>
        <div class="sdn-widget-bar-track sdn-widget-bar-track--lg">
          <div class="sdn-widget-bar-fill" style={`width:${NODE_STORAGE_PLACEHOLDER.percent}%;`}></div>
        </div>
        <div class="sdn-storage-meta-row">
          <span>{NODE_STORAGE_PLACEHOLDER.standardsSynced}</span>
          <span class="sdn-storage-fresh">{NODE_STORAGE_PLACEHOLDER.freshness}</span>
        </div>
        <div class="sdn-storage-meta-row sdn-storage-meta-row--tight">
          <span>SCHEMA</span>
          <span class="sdn-storage-schema">{NODE_STORAGE_PLACEHOLDER.schema}</span>
        </div>
      {:else if w.id === 'activity'}
        <div class="sdn-widget-title">ACTIVITY LOG</div>
        <div class="sdn-activity-list">
          {#each NODE_ACTIVITY_PLACEHOLDER as ev (ev.time + ev.text)}
            <div class="sdn-activity-row">
              <span class="sdn-activity-time">{ev.time}</span>
              <span class="sdn-status-dot sdn-status-dot--xs" style={`background:${ev.color};`}></span>
              <span class="sdn-activity-text">{ev.text}</span>
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/each}
</div>

{#if showAddTray}
  <div class="sdn-add-widget-tray">
    <div class="sdn-add-widget-kicker">ADD WIDGET</div>
    <div class="sdn-add-widget-list">
      {#each availableWidgets as spec (spec.id)}
        <button type="button" class="sdn-add-widget-btn" title={`Add ${spec.title}`} onclick={() => handleAdd(spec.id)}>
          <span class="sdn-add-widget-plus">+</span>{spec.title}
        </button>
      {/each}
    </div>
  </div>
{/if}

<style>
  .sdn-node-toolbar {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 14px;
  }

  .sdn-node-toolbar-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }

  .sdn-node-toolbar-spacer {
    flex: 1;
  }

  .sdn-node-toolbar-hint {
    font-size: 10px;
    letter-spacing: 0.08em;
    color: #6f8693;
  }

  .sdn-btn-ghost {
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
    padding: 6px 12px;
    font-size: 11px;
    letter-spacing: 0.08em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-btn-ghost:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.08);
  }

  .sdn-btn-ghost:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-btn-edit {
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
    padding: 6px 14px;
    font-size: 11.5px;
    letter-spacing: 0.1em;
    cursor: pointer;
  }

  .sdn-btn-edit.is-active {
    background: rgba(74, 166, 224, 0.18);
    border-color: rgba(120, 190, 230, 0.55);
    color: #9fd4f5;
  }

  .sdn-node-grid {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    grid-auto-rows: min-content;
    grid-auto-flow: row dense;
    gap: 14px;
    align-content: start;
  }

  .sdn-widget {
    position: relative;
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow:
      inset 0 1px 0 rgba(150, 210, 240, 0.14),
      inset 0 -10px 30px rgba(0, 0, 0, 0.4);
    padding: 15px 16px;
    cursor: default;
  }

  .sdn-widget.is-editing {
    border: 1px dashed rgba(120, 190, 230, 0.5);
    cursor: move;
  }

  .sdn-widget-editbar {
    position: absolute;
    top: 7px;
    right: 7px;
    z-index: 6;
    display: flex;
    gap: 4px;
    align-items: center;
    background: rgba(6, 11, 17, 0.9);
    border: 1px solid rgba(90, 150, 180, 0.3);
    padding: 2px 3px;
  }

  .sdn-widget-drag-handle {
    font-size: 13px;
    color: #5a7a8a;
    cursor: move;
  }

  .sdn-widget-cycle {
    font-size: 9.5px;
    letter-spacing: 0.04em;
    background: rgba(74, 166, 224, 0.14);
    border: 1px solid rgba(120, 190, 230, 0.4);
    color: #9fd4f5;
    padding: 2px 6px;
    cursor: pointer;
  }

  .sdn-widget-remove {
    font-size: 11px;
    background: rgba(255, 107, 107, 0.14);
    border: 1px solid rgba(255, 107, 107, 0.4);
    color: #ff8d8d;
    padding: 2px 6px;
    cursor: pointer;
    line-height: 1.3;
  }

  .sdn-widget-corner {
    position: absolute;
    width: 9px;
    height: 9px;
    border-width: 0;
    border-style: solid;
  }

  .sdn-widget-corner--tl {
    top: -1px;
    left: -1px;
    border-top-width: 1px;
    border-left-width: 1px;
  }

  .sdn-widget-corner--tr {
    top: -1px;
    right: -1px;
    border-top-width: 1px;
    border-right-width: 1px;
  }

  .sdn-widget-corner--bl {
    bottom: -1px;
    left: -1px;
    border-bottom-width: 1px;
    border-left-width: 1px;
  }

  .sdn-widget-corner--br {
    bottom: -1px;
    right: -1px;
    border-bottom-width: 1px;
    border-right-width: 1px;
  }

  .sdn-widget-title {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 12px;
  }

  .sdn-widget-header-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
  }

  /* Inside a header row the row itself owns the 12px gap — the title's own
     bottom margin would inflate the flex row's box (ground truth: in-row
     title span has no margin). */
  .sdn-widget-header-row .sdn-widget-title {
    margin-bottom: 0;
  }

  .sdn-widget-badge {
    font-size: 10px;
    letter-spacing: 0.1em;
    padding: 1px 6px;
  }

  .sdn-widget-badge--confirmed {
    color: #5ad6a0;
    border: 1px solid rgba(90, 214, 160, 0.4);
  }

  .sdn-widget-status-row {
    display: flex;
    align-items: baseline;
    gap: 9px;
  }

  .sdn-widget-status-text {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 31px;
    letter-spacing: 0.06em;
    color: #eaf6f8;
  }

  /* Ground truth: SERVICE state renders smaller than NODE HEALTH's ONLINE
     (24px/0.05em vs 31px/0.06em). */
  .sdn-widget-status-text--service {
    font-size: 24px;
    letter-spacing: 0.05em;
  }

  .sdn-widget-status-sub {
    font-size: 11.5px;
    color: #7d929b;
    margin: 5px 0 14px;
    letter-spacing: 0.04em;
  }

  /* Ground truth: the SERVICE version sub-line has no letter-spacing
     (NODE HEALTH's mode sub-line keeps 0.04em). */
  .sdn-widget-status-sub--plain {
    letter-spacing: normal;
  }

  .sdn-status-dot {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    flex: none;
  }

  .sdn-status-dot--sm {
    width: 6px;
    height: 6px;
  }

  .sdn-status-dot--xs {
    width: 6px;
    height: 6px;
    align-self: center;
  }

  .sdn-widget-field-stack {
    display: flex;
    flex-direction: column;
    gap: 9px;
  }

  .sdn-widget-field-row {
    display: flex;
    gap: 18px;
  }

  .sdn-widget-field-row--gap {
    margin-bottom: 14px;
  }

  .sdn-widget-field-flex {
    flex: 1;
  }

  .sdn-widget-field-label {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-widget-field-value {
    font-size: 13px;
    color: #cfe3ec;
    margin-top: 2px;
  }

  .sdn-widget-field-value--wrap {
    overflow-wrap: anywhere;
  }

  .sdn-widget-field-value--dense {
    font-size: 12.5px;
  }

  .sdn-widget-field-value--green {
    color: #5ad6a0;
  }

  .sdn-widget-storage {
    margin-top: 2px;
  }

  .sdn-widget-storage-row {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }

  .sdn-widget-storage-value {
    font-size: 13px;
    color: #eaf6f8;
    font-variant-numeric: tabular-nums;
  }

  .sdn-widget-storage-total {
    color: #5a7a8a;
  }

  .sdn-widget-bar-track {
    height: 6px;
    background: rgba(90, 150, 180, 0.14);
    margin-top: 5px;
  }

  .sdn-widget-bar-track--lg {
    height: 7px;
    margin-top: 12px;
  }

  .sdn-widget-bar-fill {
    height: 100%;
    background: linear-gradient(90deg, #35c9d8, #4aa6e0);
  }

  .sdn-widget-heading {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 21.5px;
    letter-spacing: 0.04em;
    color: #eaf6f8;
  }

  .sdn-widget-subheading {
    font-size: 11.5px;
    color: #7d929b;
    margin: 4px 0 14px;
  }

  .sdn-widget-button-row {
    display: flex;
    gap: 6px;
    margin-top: 14px;
  }

  .sdn-btn-flex {
    flex: 1;
    padding: 7px 0;
    font-size: 11.5px;
    letter-spacing: 0.08em;
    text-align: center;
  }

  .sdn-btn-accent {
    background: rgba(74, 166, 224, 0.12);
    border: 1px solid rgba(120, 190, 230, 0.5);
    color: #9fd4f5;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-btn-accent:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.2);
  }

  .sdn-btn-accent:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-btn-danger {
    background: transparent;
    border: 1px solid rgba(255, 107, 107, 0.35);
    color: #ff8d8d;
    cursor: pointer;
  }

  .sdn-btn-danger:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-netmap-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
    margin-bottom: 2px;
  }

  .sdn-netmap-header-left {
    display: flex;
    align-items: baseline;
    gap: 9px;
    min-width: 0;
  }

  .sdn-netmap-header-left .sdn-widget-title {
    margin-bottom: 0;
  }

  .sdn-netmap-header-tag {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #4d6a78;
  }

  .sdn-netmap-header-right {
    display: flex;
    align-items: center;
    gap: 11px;
    flex: none;
  }

  .sdn-netmap-links {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    letter-spacing: 0.04em;
    color: #9fb3bc;
  }

  .sdn-netmap-tabs {
    display: flex;
    border: 1px solid rgba(90, 150, 180, 0.28);
  }

  .sdn-netmap-tab {
    border: 0;
    padding: 4px 11px;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 11px;
    letter-spacing: 0.08em;
    cursor: pointer;
  }

  .sdn-netmap-tab--split {
    border-left: 1px solid rgba(90, 150, 180, 0.28);
  }

  .sdn-netmap-canvas-wrap {
    position: relative;
    height: 322px;
    margin: 8px -4px 0;
  }

  .sdn-netmap-canvas {
    width: 100%;
    height: 100%;
    display: block;
  }

  .sdn-netmap-caption-tl {
    position: absolute;
    left: 8px;
    top: 6px;
    pointer-events: none;
    font-family: 'IBM Plex Mono', monospace;
    font-size: 9px;
    letter-spacing: 0.12em;
    color: #5a7f8f;
    line-height: 1.65;
  }

  .sdn-netmap-caption-br {
    position: absolute;
    right: 8px;
    bottom: 6px;
    pointer-events: none;
    font-family: 'IBM Plex Mono', monospace;
    font-size: 8.5px;
    letter-spacing: 0.1em;
    color: #3f5c6a;
  }

  .sdn-netmap-legend {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-top: 11px;
    flex-wrap: wrap;
  }

  .sdn-netmap-legend-items {
    display: flex;
    align-items: center;
    gap: 14px;
  }

  .sdn-netmap-legend-item {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 10px;
    letter-spacing: 0.06em;
    color: #9fb3bc;
  }

  .sdn-netmap-legend-note {
    font-size: 9.5px;
    letter-spacing: 0.04em;
    color: #4d6a78;
  }

  .sdn-throughput-row {
    display: flex;
    align-items: baseline;
    gap: 8px;
    margin-bottom: 3px;
  }

  .sdn-throughput-value {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-weight: 600;
    font-size: 26.5px;
    color: #eaf6f8;
    font-variant-numeric: tabular-nums;
  }

  .sdn-throughput-value--up {
    font-size: 13px;
    /* Ground truth: the small ↑ figure is regular weight, unlike the big
       ↓ figure which keeps 600. */
    font-weight: 400;
    color: #9fd4f5;
    margin-left: 4px;
  }

  .sdn-throughput-unit {
    font-size: 12px;
    color: #7d929b;
  }

  .sdn-throughput-bars {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    margin-top: 14px;
    height: 64px;
  }

  .sdn-throughput-bar {
    flex: 1;
  }

  .sdn-throughput-axis {
    display: flex;
    justify-content: space-between;
    font-size: 9.5px;
    color: #5a7a8a;
    margin-top: 8px;
    letter-spacing: 0.08em;
  }

  .sdn-peersum-list {
    display: flex;
    flex-direction: column;
    gap: 11px;
  }

  .sdn-peersum-row {
    display: flex;
    align-items: center;
    gap: 9px;
  }

  .sdn-peersum-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 14.5px;
    color: #eaf6f8;
    flex: none;
  }

  .sdn-peersum-feeds {
    font-size: 11px;
    color: #6f8693;
    flex: 1;
    text-align: right;
  }

  .sdn-peersum-trust {
    font-size: 10px;
    letter-spacing: 0.04em;
    width: 64px;
    text-align: right;
  }

  .sdn-storage-value-row {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }

  .sdn-storage-value {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-weight: 600;
    font-size: 29px;
    color: #eaf6f8;
    font-variant-numeric: tabular-nums;
  }

  .sdn-storage-unit {
    font-size: 13px;
    color: #7d929b;
  }

  .sdn-storage-meta-row {
    display: flex;
    justify-content: space-between;
    font-size: 11px;
    color: #7d929b;
    margin-top: 10px;
  }

  .sdn-storage-meta-row--tight {
    margin-top: 6px;
  }

  .sdn-storage-fresh {
    color: #5ad6a0;
  }

  .sdn-storage-schema {
    color: #cfe3ec;
  }

  .sdn-activity-list {
    display: flex;
    flex-direction: column;
    gap: 9px;
  }

  .sdn-activity-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
  }

  .sdn-activity-time {
    font-size: 11px;
    color: #5a7a8a;
    font-variant-numeric: tabular-nums;
    flex: none;
  }

  .sdn-activity-text {
    font-size: 11.5px;
    color: #9fb3bc;
    flex: 1;
  }

  .sdn-add-widget-tray {
    margin-top: 14px;
    border: 1px dashed rgba(90, 150, 180, 0.32);
    padding: 13px 16px;
  }

  .sdn-add-widget-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 11px;
  }

  .sdn-add-widget-list {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .sdn-add-widget-btn {
    display: flex;
    align-items: center;
    gap: 8px;
    background: rgba(74, 166, 224, 0.06);
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #cfe3ec;
    padding: 9px 13px;
    font-size: 12px;
    letter-spacing: 0.04em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-add-widget-btn:hover {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.08);
  }

  .sdn-add-widget-plus {
    font-size: 15.5px;
    color: #35c9d8;
    line-height: 1;
  }
</style>
