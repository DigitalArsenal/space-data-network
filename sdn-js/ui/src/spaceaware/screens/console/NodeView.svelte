<script lang="ts">
  /**
   * NODE dashboard (loop U3.1 layout, U3.2 real data, U4.1 cycle C adds
   * storage capacity/uptime/throughput). Ground truth: the
   * `<!-- ==== NODE ==== -->` block in `SDN Console.dc.html` — a 12-col
   * widget grid with an EDIT MODE (drag-reorder, span cycle, remove,
   * add-from-catalog, reset), layout persisted to
   * `localStorage['sdn_node_layout_v1']`. That layout engine is untouched by
   * U3.2/U4.1 — only the widget BODIES below changed, from U3.1's typed
   * placeholder data to real reads of `node/info`, `node/epm/json`,
   * `node/epm/vcard`, `/v1/stats`, `/v1/peers`, and (U4.1, session-gated)
   * `/node/status` (`lib/node-data.ts` does all the fetching/parsing/
   * formatting; this file only renders the resulting view-model strings
   * verbatim). NODE HEALTH's storage capacity, SERVICE's UPTIME, and NETWORK
   * THROUGHPUT are real as of U4.1; AUTOSTART and ACTIVITY LOG still have no
   * backing surface and render an honest no-data state instead of a
   * fabricated number — see `lib/node-data.ts`'s doc comments for which
   * endpoint, if any, is missing each remaining one.
   *
   * The PEER MAP widget (loop U3.4) renders a real canvas globe —
   * `../../../lib/globe/SdnGlobe.ts` (the `globe.js` port; same engine the
   * retired `GlobeDemoPanel.svelte` demo used) fed by `lib/netmap-data.ts`'s
   * honest point-placement pipeline: every peer's first PUBLIC ip4/ip6
   * multiaddr (private/loopback/dns/`/p2p-circuit` addrs excluded) is looked
   * up against a small vendored static table of DOCUMENTED production infra
   * (decision D8 v1 — no runtime GeoIP calls, no CDN tiles); a peer with no
   * table hit gets a deterministic peer-id-hashed placement instead,
   * rendered dimmer by the engine with an honest "location unresolved"
   * tooltip rather than a fabricated country. COUNTRIES counts DISTINCT
   * countries among RESOLVED peers only (still the honest `—` when nothing
   * has resolved).
   *
   * SERVICE widget decision (D6, `SPACEAWARE_UI_WIRING_ANALYSIS.md`):
   * daemon lifecycle (restart/stop) has no HTTP control surface and the
   * wiring doc recommends read-only status v1 — so unlike the mock (which
   * fakes a restart animation), RESTART/STOP/CHECK render disabled with an
   * honest tooltip instead of a working mock action.
   */
  import { onMount } from 'svelte';
  import {
    NODE_DEFAULT_LAYOUT,
    NODE_WIDGETS,
    addNodeWidget,
    availableNodeWidgets,
    consoleHealthChipStyle,
    cycleWidgetSpan,
    loadNodeLayout,
    nodeMapTabStyle,
    removeNodeWidget,
    reorderNodeLayout,
    resetNodeLayout,
    saveNodeLayout,
    widgetSpanLabel,
    type ConsoleHealthChipState,
    type NodeLayout,
    type NodeMapMode,
    type NodeWidgetId,
  } from '../../lib/console';
  import {
    NODE_PEER_SUMMARY_EMPTY_LABEL,
    buildEpmDownloadFilename,
    buildNodeHealthView,
    buildNodeIdentityView,
    buildNodeNetmapView,
    buildNodePeerSummary,
    buildNodeServiceView,
    buildNodeStorageView,
    buildActivityRows,
    buildNodeThroughputView,
    flattenJsonToCsv,
    loadNodeDashboardData,
    serviceStatusDotColor,
  } from '../../lib/node-data';
  import {
    buildCredentialRows,
    clearCredential,
    loadCredentialStatuses,
    saveCredential,
    type CredentialRow,
  } from '../../lib/credentials-data';
  import {
    buildNetmapPoints,
    formatNetmapCountryCount,
    netmapPointColor,
  } from '../../lib/netmap-data';
  import { sdnGlobe } from '../../../lib/globe/SdnGlobe';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  let {
    onOpenQr,
    apiClient,
    healthState,
  }: { onOpenQr: () => void; apiClient: SdnApiClient; healthState: ConsoleHealthChipState } = $props();

  let layout = $state<NodeLayout>(NODE_DEFAULT_LAYOUT.map((w) => ({ ...w })));
  let editMode = $state(false);
  let mapMode = $state<NodeMapMode>('3d');

  // Native HTML5 drag-and-drop source id — plain (non-reactive) like the
  // mock's `this._dragId`, since it only matters mid-gesture.
  let dragId: NodeWidgetId | null = null;

  // ---------------------------------------------------------------------
  // Real dashboard data (loop U3.2). Fetched once on mount — see
  // `lib/node-data.ts`'s `loadNodeDashboardData` doc comment for why this
  // never throws/rejects even when the daemon is fully offline.
  // ---------------------------------------------------------------------
  let dashboard = $state<Awaited<ReturnType<typeof loadNodeDashboardData>> | null>(null);

  const healthView = $derived(
    buildNodeHealthView(
      dashboard?.nodeInfo ?? null,
      dashboard?.stats?.totalBytes ?? null,
      dashboard?.status?.disk?.capacityBytes ?? null,
    ),
  );
  const identityView = $derived(buildNodeIdentityView(dashboard?.identity ?? null, dashboard?.vcardText ?? null));
  const serviceView = $derived(buildNodeServiceView(dashboard?.nodeInfo ?? null, dashboard?.status?.uptimeSeconds ?? null));
  // Loop U4.1 cycle C: real NETWORK THROUGHPUT telemetry from `/node/status`'s
  // `bandwidth` — see `lib/node-data.ts`'s `buildNodeThroughputView` doc
  // comment for the collecting/no-telemetry honest-degradation rules.
  const throughputView = $derived(buildNodeThroughputView(dashboard?.status?.bandwidth ?? null));
  const netmapView = $derived(buildNodeNetmapView(dashboard?.stats ?? null));
  // Loop U3.4: the real point set + honest resolved-only country count for
  // the PEER MAP canvas globe — see `lib/netmap-data.ts`'s doc comment.
  // `netmapView.countryCount` above stays the U3.2 honest-dash placeholder
  // (that builder has no geo surface); this widget renders the real count
  // computed here instead, without touching `node-data.ts`.
  const netmapModel = $derived(buildNetmapPoints(dashboard?.nodeInfo ?? null, dashboard?.peers ?? []));
  const netmapCountryCount = $derived(formatNetmapCountryCount(netmapModel.points));
  const netmapGlobeOptions = $derived({
    home: { ...netmapModel.home, label: 'THIS NODE' },
    points: netmapModel.points,
    colorFor: netmapPointColor,
    // Reading mapMode HERE (not just inside a closure) makes this derived
    // object invalidate on toggle, so the action's update applies the mode
    // — same rationale as the retired GlobeDemoPanel demo's identical comment.
    mode: mapMode,
  });
  const peerSummaryRows = $derived(buildNodePeerSummary(dashboard?.peers ?? []));
  const activityRows = $derived(buildActivityRows(dashboard?.activity ?? []));
  const storageView = $derived(buildNodeStorageView(dashboard?.stats ?? null, dashboard?.nodeInfo?.standardsVersion));
  const healthChipStyle = $derived(consoleHealthChipStyle(healthState));

  // --- PROVIDER CREDENTIALS (write-only) --------------------------------
  //
  // The daemon exposes credential STATUS only; there is no endpoint that
  // returns a stored secret. Nothing below ever holds a stored password: the
  // password input is a write buffer, cleared the moment a save resolves, and
  // never pre-filled from the node.
  let credentialRows = $state<CredentialRow[]>(buildCredentialRows([]));
  let credentialNotice = $state<string | null>(null);
  let credentialBusy = $state<string | null>(null);
  let credentialMessages = $state<Record<string, string>>({});
  let credentialUsername = $state<Record<string, string>>({});
  let credentialSecret = $state<Record<string, string>>({});
  let credentialVerify = $state<Record<string, boolean>>({});

  async function refreshCredentials() {
    const result = await loadCredentialStatuses(apiClient);
    credentialRows = result.rows;
    credentialNotice = result.notice;
  }

  async function onSaveCredential(id: string) {
    const username = (credentialUsername[id] ?? '').trim();
    const secret = credentialSecret[id] ?? '';
    if (!username || !secret) {
      credentialMessages = { ...credentialMessages, [id]: 'IDENTITY AND SECRET ARE REQUIRED' };
      return;
    }
    credentialBusy = id;
    const result = await saveCredential(apiClient, id, username, secret, credentialVerify[id] === true);
    // Clear the write buffer immediately — the secret must not linger in
    // component state after the request resolves.
    credentialSecret = { ...credentialSecret, [id]: '' };
    credentialMessages = { ...credentialMessages, [id]: result.message };
    credentialBusy = null;
    if (result.ok) {
      credentialUsername = { ...credentialUsername, [id]: '' };
      await refreshCredentials();
    }
  }

  async function onClearCredential(id: string) {
    credentialBusy = id;
    const result = await clearCredential(apiClient, id);
    credentialMessages = { ...credentialMessages, [id]: result.message };
    credentialBusy = null;
    if (result.ok) await refreshCredentials();
  }

  onMount(() => {
    try {
      layout = loadNodeLayout(window.localStorage);
    } catch {
      // localStorage unavailable — keep the in-memory default layout.
    }
    void loadNodeDashboardData(apiClient).then((data) => {
      dashboard = data;
    });
    void refreshCredentials();
  });

  function triggerDownload(content: string, filename: string, mimeType: string) {
    try {
      const blob = new Blob([content], { type: mimeType });
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = filename;
      link.click();
      URL.revokeObjectURL(url);
    } catch {
      // Browser download APIs unavailable in this context — the button is a no-op rather than a crash.
    }
  }

  function downloadJson() {
    const raw = dashboard?.epmJsonRaw;
    if (!raw) return;
    triggerDownload(
      JSON.stringify(raw, null, 2),
      buildEpmDownloadFilename(dashboard?.identity?.dn, 'json'),
      'application/json',
    );
  }

  function downloadCsv() {
    const raw = dashboard?.epmJsonRaw;
    if (!raw) return;
    const csv = flattenJsonToCsv(raw);
    if (!csv) return;
    triggerDownload(csv, buildEpmDownloadFilename(dashboard?.identity?.dn, 'csv'), 'text/csv');
  }

  function downloadVCard() {
    const vcard = dashboard?.vcardText;
    if (!vcard) return;
    triggerDownload(vcard, buildEpmDownloadFilename(dashboard?.identity?.dn, 'vcf'), 'text/vcard');
  }

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
          <span class="sdn-status-dot" style={`background:${healthChipStyle.color};box-shadow:0 0 9px ${healthChipStyle.color};`}
          ></span>
          <span class="sdn-widget-status-text">{healthChipStyle.label}</span>
        </div>
        <div class="sdn-widget-status-sub">{healthView.mode}</div>
        <div class="sdn-widget-field-stack">
          <div>
            <div class="sdn-widget-field-label">PEER ID</div>
            <div class="sdn-widget-field-value sdn-widget-field-value--wrap">{healthView.peerId}</div>
          </div>
          <div class="sdn-widget-field-row">
            <div class="sdn-widget-field-flex">
              <div class="sdn-widget-field-label">API</div>
              <div class="sdn-widget-field-value">{healthView.api}</div>
            </div>
            <div class="sdn-widget-field-flex">
              <div class="sdn-widget-field-label">GATEWAY</div>
              <div class="sdn-widget-field-value">{healthView.gateway}</div>
            </div>
          </div>
          <div class="sdn-widget-storage">
            <div class="sdn-widget-storage-row">
              <span class="sdn-widget-field-label">STORAGE</span>
              <span class="sdn-widget-storage-value"
                >{healthView.storageUsed}
                <span class="sdn-widget-storage-total">/ {healthView.storageTotal}</span></span
              >
            </div>
            <div class="sdn-widget-bar-track">
              <div class="sdn-widget-bar-fill" style={`width:${healthView.storagePercent}%;`}></div>
            </div>
          </div>
        </div>
      {:else if w.id === 'identity'}
        <div class="sdn-widget-header-row">
          <span class="sdn-widget-title">IDENTITY</span>
          <span class="sdn-widget-badge sdn-widget-badge--confirmed">CONFIRMED</span>
        </div>
        <div class="sdn-widget-heading">{identityView.name}</div>
        <div class="sdn-widget-subheading">{identityView.subtitle}</div>
        <div class="sdn-widget-field-stack">
          <div>
            <div class="sdn-widget-field-label">EPM CID</div>
            <div class="sdn-widget-field-value sdn-widget-field-value--wrap sdn-widget-field-value--dense">
              {identityView.epmCid}
            </div>
          </div>
          <div>
            <div class="sdn-widget-field-label">vCARD</div>
            <div class="sdn-widget-field-value">{identityView.vcard}</div>
          </div>
        </div>
        <div class="sdn-widget-button-row">
          <button
            type="button"
            class="sdn-btn-ghost sdn-btn-flex"
            disabled={!dashboard?.epmJsonRaw}
            title="Export identity as JSON"
            onclick={downloadJson}
          >
            JSON
          </button>
          <button
            type="button"
            class="sdn-btn-ghost sdn-btn-flex"
            disabled={!dashboard?.epmJsonRaw}
            title="Export identity as CSV"
            onclick={downloadCsv}
          >
            CSV
          </button>
          <button
            type="button"
            class="sdn-btn-ghost sdn-btn-flex"
            disabled={!dashboard?.vcardText}
            title="Export identity as vCARD"
            onclick={downloadVCard}
          >
            vCARD
          </button>
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
          <span
            class="sdn-status-dot"
            style={`background:${serviceStatusDotColor(serviceView.state)};box-shadow:0 0 9px ${serviceStatusDotColor(serviceView.state)};`}
          ></span>
          <span class="sdn-widget-status-text sdn-widget-status-text--service">{serviceView.state}</span>
        </div>
        <div class="sdn-widget-status-sub sdn-widget-status-sub--plain">{serviceView.version}</div>
        <div class="sdn-widget-field-row sdn-widget-field-row--gap">
          <div class="sdn-widget-field-flex">
            <div class="sdn-widget-field-label">AUTOSTART</div>
            <!--
              Loop U4.1: `/node/status`'s `service.autostart_known` IS wired
              in now (see lib/node-data.ts's NodeStatusServiceSnapshot), but
              it only ever says whether the daemon KNOWS its autostart
              config, not what that config IS — there is still no
              `autostart_enabled`-shaped value in the payload to render (and
              `autostart_known` is always `false` on this build besides), so
              this stays the honest dash rather than a fabricated
              ENABLED/DISABLED — dropped the green "confirmed" styling since
              nothing confirms it.
            -->
            <div class="sdn-widget-field-value">{serviceView.autostart}</div>
          </div>
          <div class="sdn-widget-field-flex">
            <div class="sdn-widget-field-label">UPTIME</div>
            <!-- Loop U4.1: real `/node/status` `uptime_seconds`, `formatUptime`'s "Nd HH:MM" style; still the honest dash when the surface is unavailable (anonymous session, offline daemon). -->
            <div class="sdn-widget-field-value">{serviceView.uptime}</div>
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
              ></span>{netmapView.connectionCount} LINKS
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
          <canvas class="sdn-netmap-canvas" use:sdnGlobe={netmapGlobeOptions}></canvas>
          <div class="sdn-netmap-caption-tl">
            <div>{netmapView.connectionCount} CONNECTIONS</div>
            <!--
              Loop U3.4: real distinct-country count among RESOLVED peers
              only (`lib/netmap-data.ts`'s countResolvedCountries) — still
              the honest '—' when nothing has resolved, never a fabricated
              number.
            -->
            <div>{netmapCountryCount} COUNTRIES</div>
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
          <!-- The mock credits "MaxMind GeoLite2", but D8 v1 uses a vendored
               static host table + deterministic fallback (lib/netmap-data.ts)
               — crediting a database we don't ship would be a false claim. -->
          <span class="sdn-netmap-legend-note">Locations · static map · approximate</span>
        </div>
      {:else if w.id === 'throughput'}
        <div class="sdn-widget-title">NETWORK THROUGHPUT</div>
        {#if !throughputView.hasData}
          <!--
            Loop U4.1: `/node/status`'s `bandwidth` is itself nullable on the
            wire (fresh/constrained daemon, or an anonymous/broken session —
            `lib/node-data.ts`'s `fetchNodeStatus` degrades either to `null`)
            — same honest no-data line the widget has always shown in that
            case, matching the dim `.sdn-widget-status-sub` styling used
            elsewhere for "we don't know this yet" fields.
          -->
          <div class="sdn-widget-status-sub sdn-widget-status-sub--plain">NO TELEMETRY · pending M1</div>
        {:else}
          <div class="sdn-throughput-row">
            <span class="sdn-throughput-value">{throughputView.downValue}</span>
            <span class="sdn-throughput-unit">{throughputView.downUnit} ↓</span>
            <span class="sdn-throughput-value--up">{throughputView.upValue} ↑</span>
          </div>
          {#if throughputView.collecting}
            <!-- Fewer than 2 history samples yet (a just-started daemon) — real bars would be drawn from noise, so an honest "still collecting" line replaces the mock's fabricated sparkline instead of faking one from 0-1 points. -->
            <div class="sdn-widget-status-sub sdn-widget-status-sub--plain">COLLECTING · first samples in ~10s</div>
          {:else}
            <div class="sdn-throughput-bars">
              {#each throughputView.bars as bar, i (i)}
                <div class="sdn-throughput-bar" style={`height:${bar.percent}%;background:${bar.gradient};`}></div>
              {/each}
            </div>
            <div class="sdn-throughput-axis">
              <!-- The REAL span the bar chart covers (history length × 5s cadence), replacing the mock's fixed "−60s" — see `formatThroughputAxisLabel`'s doc comment. -->
              <span>{throughputView.axisStart}</span>
              <span>{throughputView.axisEnd}</span>
            </div>
          {/if}
        {/if}
      {:else if w.id === 'peersum'}
        <div class="sdn-widget-title">PEER SUMMARY</div>
        <div class="sdn-peersum-list">
          {#each peerSummaryRows as pm (pm.name)}
            <div class="sdn-peersum-row">
              <span class="sdn-status-dot sdn-status-dot--sm" style={`background:${pm.trustColor};box-shadow:0 0 6px ${pm.trustColor};`}
              ></span>
              <span class="sdn-peersum-name">{pm.name}</span>
              <span class="sdn-peersum-feeds">{pm.feeds}</span>
              <span class="sdn-peersum-trust" style={`color:${pm.trustColor};`}>{pm.trust}</span>
            </div>
          {:else}
            <div class="sdn-peersum-row">
              <span class="sdn-peersum-name">{NODE_PEER_SUMMARY_EMPTY_LABEL}</span>
            </div>
          {/each}
        </div>
      {:else if w.id === 'storage'}
        <div class="sdn-widget-title">STORAGE · FLATSQL</div>
        <div class="sdn-storage-value-row">
          <span class="sdn-storage-value">{storageView.used}</span>
          <span class="sdn-storage-unit">/ {storageView.total}</span>
        </div>
        <!-- FlatSQL's cumulative byte count has no capacity to measure against (unlike a disk quota) — bar always renders 0-width rather than a fabricated percent. -->
        <div class="sdn-widget-bar-track sdn-widget-bar-track--lg">
          <div class="sdn-widget-bar-fill" style={`width:${storageView.percent}%;`}></div>
        </div>
        <div class="sdn-storage-meta-row">
          <span>{storageView.standardsSynced}</span>
          <!-- No sync-freshness surface exists yet — the green "confirmed" styling only applies once freshnessKnown is real. -->
          <span class:sdn-storage-fresh={storageView.freshnessKnown}>{storageView.freshness}</span>
        </div>
        {#each storageView.schemaRows as row (row.label)}
          <div class="sdn-storage-meta-row sdn-storage-meta-row--tight">
            <span>{row.label}</span>
            <span class="sdn-storage-schema">{row.value}</span>
          </div>
        {:else}
          <div class="sdn-storage-meta-row sdn-storage-meta-row--tight">
            <span>SCHEMA</span>
            <span class="sdn-storage-schema">NO SCHEMAS</span>
          </div>
        {/each}
      {:else if w.id === 'activity'}
        <div class="sdn-widget-title">ACTIVITY LOG</div>
        {#if activityRows.length}
          <!-- Real events from GET /node/activity (loop U4.2 / M2): the
               node's bounded ring, newest first. -->
          <div class="sdn-activity-list">
            {#each activityRows as row, i (i)}
              <div class="sdn-activity-row">
                <span class="sdn-activity-time">{row.time}</span>
                <span class="sdn-activity-text">{row.text}</span>
              </div>
            {/each}
          </div>
        {:else}
          <!-- Session-gated surface: anonymous/offline degrades to an empty
               list, and a fresh ring is legitimately empty too. -->
          <div class="sdn-widget-status-sub sdn-widget-status-sub--plain">NO ACTIVITY YET</div>
        {/if}
      {:else if w.id === 'credentials'}
        <!-- PROVIDER CREDENTIALS (write-only).

             The node stores these encrypted at rest and never returns a stored
             secret to any caller, so this widget can only ever show whether a
             lane is CONFIGURED — never the value. The secret input is a write
             buffer: never pre-filled, cleared on save.

             Status dot follows the console honesty rule — green only on a
             confirmed successful probe; a stored-but-unprobed credential is
             neutral (unknown), never a fabricated red. -->
        <div class="sdn-widget-title">PROVIDER CREDENTIALS</div>
        {#if credentialNotice}
          <div class="sdn-widget-status-sub sdn-widget-status-sub--plain">{credentialNotice}</div>
        {:else}
          <div class="sdn-cred-list">
            {#each credentialRows as row (row.lane.id)}
              <div class="sdn-cred-row">
                <div class="sdn-cred-head">
                  <span class="sdn-cred-dot" style={`background:${row.dotColor};box-shadow:0 0 6px ${row.dotColor};`}
                  ></span>
                  <span class="sdn-cred-label">{row.lane.label}</span>
                  <span class="sdn-cred-state">{row.stateLabel}</span>
                </div>
                <div class="sdn-cred-purpose">{row.lane.purpose}</div>
                {#if row.status.configured && row.status.usernameMasked}
                  <div class="sdn-cred-purpose">ACCOUNT {row.status.usernameMasked}</div>
                {/if}

                <div class="sdn-cred-form">
                  <input
                    class="sdn-cred-input"
                    type="text"
                    autocomplete="off"
                    spellcheck="false"
                    placeholder={row.lane.usernameLabel}
                    aria-label={`${row.lane.label} ${row.lane.usernameLabel}`}
                    bind:value={credentialUsername[row.lane.id]}
                  />
                  <!-- Never pre-filled with the stored value: the node has no
                       route that would return it. -->
                  <input
                    class="sdn-cred-input"
                    type="password"
                    autocomplete="off"
                    spellcheck="false"
                    placeholder="SECRET"
                    aria-label={`${row.lane.label} secret`}
                    bind:value={credentialSecret[row.lane.id]}
                  />
                </div>

                <div class="sdn-cred-actions">
                  <label class="sdn-cred-verify">
                    <input type="checkbox" bind:checked={credentialVerify[row.lane.id]} />
                    VERIFY ON SAVE
                  </label>
                  <button
                    class="sdn-cred-btn"
                    type="button"
                    disabled={credentialBusy === row.lane.id}
                    onclick={() => onSaveCredential(row.lane.id)}
                  >
                    {row.status.configured ? 'REPLACE' : 'SAVE'}
                  </button>
                  <button
                    class="sdn-cred-btn"
                    type="button"
                    disabled={credentialBusy === row.lane.id || !row.status.configured}
                    onclick={() => onClearCredential(row.lane.id)}
                  >
                    CLEAR
                  </button>
                </div>

                {#if credentialMessages[row.lane.id]}
                  <div class="sdn-cred-msg">{credentialMessages[row.lane.id]}</div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
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

  /* --- PROVIDER CREDENTIALS ------------------------------------------- */

  .sdn-cred-list {
    display: flex;
    flex-direction: column;
    gap: 14px;
    margin-top: 4px;
  }

  .sdn-cred-row {
    border-top: 1px solid rgba(120, 160, 180, 0.16);
    padding-top: 10px;
  }

  .sdn-cred-row:first-child {
    border-top: none;
    padding-top: 0;
  }

  .sdn-cred-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .sdn-cred-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
  }

  .sdn-cred-label {
    font-size: 12px;
    color: #eaf6f8;
    letter-spacing: 0.04em;
  }

  .sdn-cred-state {
    margin-left: auto;
    font-size: 10.5px;
    color: #7d929b;
    letter-spacing: 0.04em;
  }

  .sdn-cred-purpose {
    font-size: 10.5px;
    color: #7d929b;
    margin-top: 4px;
    letter-spacing: 0.03em;
  }

  .sdn-cred-form {
    display: flex;
    gap: 8px;
    margin-top: 8px;
  }

  .sdn-cred-input {
    flex: 1 1 0;
    min-width: 0;
    background: rgba(10, 22, 30, 0.7);
    border: 1px solid rgba(120, 160, 180, 0.28);
    border-radius: 3px;
    padding: 6px 8px;
    font: inherit;
    font-size: 11px;
    color: #eaf6f8;
    letter-spacing: 0.03em;
  }

  .sdn-cred-input:focus {
    outline: none;
    border-color: rgba(120, 190, 230, 0.6);
  }

  .sdn-cred-input::placeholder {
    color: #5c7481;
  }

  .sdn-cred-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
  }

  .sdn-cred-verify {
    display: flex;
    align-items: center;
    gap: 5px;
    margin-right: auto;
    font-size: 10px;
    color: #7d929b;
    letter-spacing: 0.04em;
    cursor: pointer;
  }

  .sdn-cred-btn {
    background: rgba(74, 166, 224, 0.08);
    border: 1px solid rgba(120, 160, 180, 0.32);
    border-radius: 3px;
    padding: 5px 12px;
    font: inherit;
    font-size: 10px;
    letter-spacing: 0.06em;
    color: #cfe4ea;
    cursor: pointer;
  }

  .sdn-cred-btn:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
  }

  .sdn-cred-btn:disabled {
    opacity: 0.4;
    cursor: default;
  }

  .sdn-cred-msg {
    margin-top: 7px;
    font-size: 10px;
    color: #7d929b;
    letter-spacing: 0.03em;
  }
</style>
