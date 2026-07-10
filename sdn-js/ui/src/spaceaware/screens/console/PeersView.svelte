<script lang="ts">
  /**
   * PEERS console view (loop task U3.3). Ground truth: the
   * `<!-- ============ PEERS ============ -->` block in
   * `design_handoff/sdn_console/SDN Console.dc.html` — a DIRECTORY FILTER
   * toolbar (ALL/TRUSTED/OBSERVED/PROVIDERS tabs + search + count), a
   * TRUSTED & OBSERVED PEERS table (left, span 8), and a PEER DETAIL card
   * (right, span 4). The shared `ConsoleHeader` already renders
   * "PEERS · DISCOVERY & TRUST" (`lib/console.ts`'s `CONSOLE_TITLES`/
   * `CONSOLE_SUBTITLES`), so this view starts at the DIRECTORY FILTER row.
   *
   * All data wiring lives in `../../lib/peers-data.ts` — this file only
   * renders its view-model strings verbatim, matching `NodeView.svelte`'s
   * established split (see that file's own doc comment). Every honest
   * empty/disabled state below (FEEDS "—", OWNERTRUST "—", EPM CID
   * "NOT PUBLISHED", disabled vCARD/QR, gated CONNECT) traces back to a real
   * gap documented in `peers-data.ts` — nothing here fabricates data the
   * daemon doesn't actually expose yet (see the loop task's HONESTY RULES).
   *
   * One intentional deviation from the mock's own logic (not from the loop
   * task, which explicitly calls for this): the mock's "N PEERS" count is
   * the UNFILTERED total (`this.PEERS.length`, computed once regardless of
   * `peerFilter`/`peerSearch`); this view shows the count of the currently
   * FILTERED rows instead, since a static total next to a filtered table
   * reads as a bug once real (non-fixture) data varies in size.
   *
   * Selection semantics mirror the mock exactly: the PEER DETAIL card tracks
   * a selected peer id against the FULL (unfiltered) peer list, defaulting
   * to the first peer overall, so switching DIRECTORY FILTER tabs never
   * clears an existing selection just because it scrolled out of the
   * filtered view.
   */
  import { onMount } from 'svelte';
  import {
    PEER_FILTER_TABS,
    buildPeerDetailView,
    buildPeerRowViews,
    buildPeerRows,
    canConnectPeers,
    buildConnectAddr,
    connectToPeer,
    fetchPeerDetail,
    filterPeers,
    loadPeersDashboardData,
    peerFilterTabStyle,
    peersEmptyStateLabel,
    type PeerDetail,
    type PeerFilterTab,
    type PeersDashboardData,
  } from '../../lib/peers-data';
  import type { AuthSessionState } from '../../../lib/auth/auth-store';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  let { apiClient, authState }: { apiClient: SdnApiClient; authState: AuthSessionState } = $props();

  let dashboard = $state<PeersDashboardData | null>(null);
  let loaded = $state(false);
  let activeTab = $state<PeerFilterTab>('all');
  let searchQuery = $state('');
  let selectedPeerId = $state<string | null>(null);
  let peerDetail = $state<PeerDetail | null>(null);
  let connecting = $state(false);
  let connectError = $state<string | null>(null);

  // Full (unfiltered) row set — the DIRECTORY FILTER tabs/search apply on
  // top of this for the table, but PEER DETAIL selection always resolves
  // against the full list (see the file doc comment above).
  const allRows = $derived(buildPeerRows(dashboard?.peers ?? [], dashboard?.paidPeerIds ?? new Set()));
  const filteredRows = $derived(filterPeers(searchQuery, activeTab, allRows));
  const rowViews = $derived(buildPeerRowViews(filteredRows, selectedPeerId));
  const selectedRow = $derived(allRows.find((r) => r.peerId === selectedPeerId) ?? allRows[0] ?? null);
  const selectedPeerIdResolved = $derived(selectedRow?.peerId ?? null);
  const canConnect = $derived(canConnectPeers(authState));
  const detailView = $derived(buildPeerDetailView(selectedRow, peerDetail, canConnect));
  const emptyStateLabel = $derived(peersEmptyStateLabel(loaded, allRows.length, filteredRows.length));

  onMount(() => {
    void loadPeersDashboardData(apiClient).then((data) => {
      dashboard = data;
      loaded = true;
    });
  });

  // Re-fetch GET /api/v1/peers/{peerId} whenever the resolved selection
  // changes (never on every re-render — this derived value only changes
  // when the selected peer id itself actually changes). That endpoint 401s
  // for a non-admin session; `fetchPeerDetail` swallows that into `null`,
  // and `buildPeerDetailView` falls back to the row's own `addrs`.
  $effect(() => {
    const id = selectedPeerIdResolved;
    peerDetail = null;
    connectError = null;
    if (!id) return;
    let cancelled = false;
    void fetchPeerDetail(apiClient, id).then((detail) => {
      if (!cancelled) peerDetail = detail;
    });
    return () => {
      cancelled = true;
    };
  });

  function selectPeer(peerId: string) {
    selectedPeerId = peerId;
  }

  function setTab(tab: PeerFilterTab) {
    activeTab = tab;
  }

  function handleSearchInput(event: Event) {
    searchQuery = (event.currentTarget as HTMLInputElement).value;
  }

  async function handleConnect() {
    if (!detailView?.connectEnabled || connecting || !selectedRow) return;
    const addr = peerDetail?.addrs[0] ?? selectedRow.addrs[0];
    if (!addr) return;
    connecting = true;
    connectError = null;
    const result = await connectToPeer(apiClient, buildConnectAddr(addr, selectedRow.peerId));
    connecting = false;
    if (!result.ok) {
      connectError = result.message ?? 'Connect failed.';
      return;
    }
    connectError = null;
    // Refresh the swarm list + re-fetch detail so a newly-dialed peer (and
    // its updated connection_count) shows up without a manual reload.
    void loadPeersDashboardData(apiClient).then((data) => {
      dashboard = data;
    });
    void fetchPeerDetail(apiClient, selectedRow.peerId).then((detail) => {
      peerDetail = detail;
    });
  }
</script>

<div class="sdn-peers-root">
  <div class="sdn-peers-toolbar">
    <div class="sdn-peers-toolbar-left">
      <span class="sdn-peers-toolbar-kicker">DIRECTORY FILTER</span>
      <div class="sdn-peers-tabs">
        {#each PEER_FILTER_TABS as tab (tab.id)}
          {@const style = peerFilterTabStyle(tab.id, activeTab)}
          <button
            type="button"
            class="sdn-peers-tab"
            style={`color:${style.color};border-color:${style.border};background:${style.background};`}
            title={`Filter the directory to ${tab.label.toLowerCase()} peers`}
            onclick={() => setTab(tab.id)}
          >
            {tab.label}
          </button>
        {/each}
      </div>
      <span class="sdn-peers-toolbar-spacer"></span>
      <div class="sdn-peers-search">
        <span class="sdn-peers-search-glyph">⌕</span>
        <input
          class="sdn-peers-search-input"
          type="text"
          name="peers-search"
          value={searchQuery}
          oninput={handleSearchInput}
          placeholder="Search peers · name · ID ·"
          title="Search peers by name or peer ID"
        />
      </div>
    </div>
    <div class="sdn-peers-toolbar-right">
      <span class="sdn-peers-count">{filteredRows.length} PEERS</span>
    </div>
  </div>

  <section class="sdn-peers-directory">
    <div class="sdn-peers-directory-title">TRUSTED &amp; OBSERVED PEERS</div>
    <div class="sdn-peers-row-header">
      <span></span>
      <span>NAME / PEER ID</span>
      <span>TRUST</span>
      <span>FEEDS</span>
      <span>ADDRESS</span>
    </div>
    <div class="sdn-peers-rows">
      {#if emptyStateLabel}
        <div class="sdn-peers-empty">{emptyStateLabel}</div>
      {:else}
        {#each rowViews as row (row.peerId)}
          <div
            class="sdn-peers-row"
            role="button"
            tabindex="0"
            title={`View details for ${row.fullPeerId}`}
            style={`background:${row.selected ? 'rgba(74,166,224,0.1)' : 'transparent'};`}
            onclick={() => selectPeer(row.peerId)}
            onkeydown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') selectPeer(row.peerId);
            }}
          >
            <span class="sdn-peers-row-dot" style={`background:${row.trustColor};box-shadow:0 0 6px ${row.trustColor};`}></span>
            <span class="sdn-peers-row-name-cell">
              <span class="sdn-peers-row-name" class:sdn-peers-row-name--fallback={row.isFallbackName}>{row.name}</span>
              {#if row.paid}
                <span class="sdn-peers-paid-chip">PAID</span>
              {/if}
              <br />
              <span class="sdn-peers-row-fullid">{row.fullPeerId}</span>
            </span>
            <span class="sdn-peers-row-trust" style={`color:${row.trustColor};`}>{row.trustLabel}</span>
            <span class="sdn-peers-row-feeds">{row.feeds}</span>
            <span class="sdn-peers-row-address">{row.address}</span>
          </div>
        {/each}
      {/if}
    </div>
  </section>

  <section class="sdn-peers-detail">
    {#if detailView}
      <div class="sdn-peers-detail-header">
        <span class="sdn-peers-directory-title">PEER DETAIL</span>
        <span
          class="sdn-peers-detail-trust-tag"
          style={`color:${detailView.trustColor};border-color:${detailView.trustBorderColor};`}
        >
          {detailView.trustLabel}
        </span>
      </div>
      <div class="sdn-peers-detail-name" class:sdn-peers-detail-name--fallback={detailView.isFallbackName}>
        {detailView.name}
      </div>
      <div class="sdn-peers-detail-subtitle">{detailView.subtitle}</div>

      {#if detailView.paid}
        <div class="sdn-peers-paid-callout">
          <span class="sdn-peers-paid-callout-glyph">◈</span>
          <span class="sdn-peers-paid-callout-text">{detailView.paidCalloutText}</span>
        </div>
      {/if}

      <div class="sdn-peers-detail-fields">
        <div>
          <div class="sdn-peers-detail-field-label">OWNERTRUST</div>
          <div class="sdn-peers-detail-field-value" style={`color:${detailView.ownertrustColor};`}>
            {detailView.ownertrust}
          </div>
        </div>
        <div>
          <div class="sdn-peers-detail-field-label">AGENT</div>
          <div class="sdn-peers-detail-field-value">{detailView.agent}</div>
        </div>
        <div>
          <div class="sdn-peers-detail-field-label">ADDRESS</div>
          <div class="sdn-peers-detail-field-value sdn-peers-detail-field-value--wrap">{detailView.address}</div>
        </div>
        <div>
          <div class="sdn-peers-detail-field-label">DATA FEEDS</div>
          <div class="sdn-peers-detail-field-value sdn-peers-detail-field-value--feeds">{detailView.feeds}</div>
        </div>
        <div>
          <div class="sdn-peers-detail-field-label">EPM CID</div>
          <div class="sdn-peers-detail-field-value sdn-peers-detail-field-value--dense sdn-peers-detail-field-value--wrap">
            {detailView.epmCid}
          </div>
        </div>
      </div>

      <div class="sdn-peers-detail-buttons">
        <button
          type="button"
          class="sdn-peers-btn sdn-peers-btn--connect"
          disabled={!detailView.connectEnabled || connecting}
          title={detailView.connectTooltip}
          onclick={handleConnect}
        >
          {connecting ? 'CONNECTING…' : 'CONNECT'}
        </button>
        <button type="button" class="sdn-peers-btn sdn-peers-btn--ghost" disabled title={detailView.vcardTooltip}>
          vCARD
        </button>
        <button type="button" class="sdn-peers-btn sdn-peers-btn--ghost" disabled title={detailView.qrTooltip}> QR </button>
      </div>

      {#if connectError}
        <div class="sdn-peers-connect-error">{connectError}</div>
      {/if}
    {:else}
      <div class="sdn-peers-directory-title">PEER DETAIL</div>
      <div class="sdn-peers-detail-empty">NO PEER SELECTED</div>
    {/if}
  </section>
</div>

<style>
  .sdn-peers-root {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 14px;
    align-content: start;
  }

  .sdn-peers-toolbar {
    grid-column: span 12;
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 14px;
    align-items: center;
  }

  .sdn-peers-toolbar-left {
    grid-column: span 8;
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }

  .sdn-peers-toolbar-kicker {
    font-size: 10px;
    letter-spacing: 0.18em;
    color: #5a7a8a;
    flex: none;
  }

  .sdn-peers-tabs {
    display: flex;
    gap: 6px;
    flex: none;
  }

  .sdn-peers-tab {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
    letter-spacing: 0.06em;
    border: 1px solid;
    padding: 4px 11px;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-peers-toolbar-spacer {
    flex: 1;
  }

  .sdn-peers-search {
    display: flex;
    align-items: center;
    gap: 7px;
    border: 1px solid rgba(90, 150, 180, 0.3);
    background: rgba(9, 13, 18, 0.7);
    padding: 4px 10px;
    flex: none;
    width: 230px;
  }

  .sdn-peers-search-glyph {
    font-size: 12px;
    color: #5a7a8a;
    line-height: 1;
    flex: none;
  }

  .sdn-peers-search-input {
    flex: 1;
    min-width: 0;
    background: transparent;
    border: 0;
    outline: none;
    color: #eaf6f8;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
    letter-spacing: 0.02em;
  }

  .sdn-peers-search-input::placeholder {
    color: #5a7a8a;
  }

  .sdn-peers-toolbar-right {
    grid-column: span 4;
    display: flex;
    justify-content: flex-end;
  }

  .sdn-peers-count {
    font-size: 11px;
    color: #6f8693;
    letter-spacing: 0.06em;
    white-space: nowrap;
  }

  .sdn-peers-directory,
  .sdn-peers-detail {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 15px 16px;
  }

  .sdn-peers-directory {
    grid-column: span 8;
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .sdn-peers-detail {
    grid-column: span 4;
    min-width: 0;
    overflow-y: auto;
    max-height: calc(100vh - 230px);
  }

  .sdn-peers-directory-title {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 12px;
  }

  .sdn-peers-row-header {
    display: grid;
    grid-template-columns: 18px 1.7fr 0.9fr 1fr 1.2fr;
    gap: 0 12px;
    padding: 0 4px 7px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.14);
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
    flex: none;
  }

  .sdn-peers-rows {
    overflow-y: auto;
    max-height: calc(100vh - 300px);
  }

  .sdn-peers-empty {
    padding: 24px 4px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
    text-align: center;
  }

  .sdn-peers-row {
    display: grid;
    grid-template-columns: 18px 1.7fr 0.9fr 1fr 1.2fr;
    gap: 0 12px;
    align-items: center;
    padding: 11px 4px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.07);
    cursor: pointer;
  }

  .sdn-peers-row-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex: none;
  }

  .sdn-peers-row-name-cell {
    min-width: 0;
  }

  .sdn-peers-row-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 14.5px;
    color: #eaf6f8;
  }

  .sdn-peers-row-name--fallback {
    color: #cfe3ec;
    opacity: 0.85;
  }

  .sdn-peers-paid-chip {
    font-size: 9px;
    letter-spacing: 0.1em;
    color: #35c9d8;
    border: 1px solid rgba(53, 201, 216, 0.45);
    padding: 1px 5px;
    margin-left: 6px;
  }

  .sdn-peers-row-fullid {
    font-size: 10px;
    color: #6f8693;
    overflow-wrap: anywhere;
  }

  .sdn-peers-row-trust {
    font-size: 11.5px;
    letter-spacing: 0.04em;
  }

  .sdn-peers-row-feeds {
    font-size: 11.5px;
    color: #9fb3bc;
  }

  .sdn-peers-row-address {
    font-size: 11.5px;
    color: #9fb3bc;
    overflow-wrap: anywhere;
  }

  .sdn-peers-detail-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
  }

  .sdn-peers-detail-header .sdn-peers-directory-title {
    margin-bottom: 0;
  }

  .sdn-peers-detail-trust-tag {
    font-size: 10px;
    letter-spacing: 0.06em;
    border: 1px solid;
    padding: 1px 7px;
  }

  .sdn-peers-detail-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 20.5px;
    color: #eaf6f8;
    overflow-wrap: anywhere;
  }

  .sdn-peers-detail-name--fallback {
    color: #cfe3ec;
    opacity: 0.85;
  }

  .sdn-peers-detail-subtitle {
    font-size: 11.5px;
    color: #7d929b;
    margin: 4px 0 14px;
  }

  .sdn-peers-paid-callout {
    display: flex;
    align-items: center;
    gap: 8px;
    border: 1px solid rgba(53, 201, 216, 0.35);
    background: rgba(53, 201, 216, 0.06);
    padding: 8px 10px;
    margin-bottom: 14px;
  }

  .sdn-peers-paid-callout-glyph {
    font-size: 15.5px;
    color: #35c9d8;
    flex: none;
  }

  .sdn-peers-paid-callout-text {
    font-size: 11px;
    color: #9fe9f2;
    line-height: 1.4;
  }

  .sdn-peers-detail-fields {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .sdn-peers-detail-field-label {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-peers-detail-field-value {
    font-size: 13px;
    color: #cfe3ec;
    margin-top: 2px;
  }

  .sdn-peers-detail-field-value--wrap {
    overflow-wrap: anywhere;
  }

  .sdn-peers-detail-field-value--dense {
    font-size: 12.5px;
  }

  .sdn-peers-detail-field-value--feeds {
    color: #9fd4f5;
  }

  .sdn-peers-detail-buttons {
    display: flex;
    gap: 6px;
    margin-top: 16px;
  }

  .sdn-peers-btn {
    padding: 8px 0;
    font-size: 11.5px;
    letter-spacing: 0.08em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-peers-btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-peers-btn--connect {
    flex: 1.4;
    background: rgba(74, 166, 224, 0.12);
    border: 1px solid rgba(120, 190, 230, 0.5);
    color: #9fd4f5;
  }

  .sdn-peers-btn--connect:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.2);
  }

  .sdn-peers-btn--ghost {
    flex: 1;
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
  }

  .sdn-peers-btn--ghost:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.08);
  }

  .sdn-peers-connect-error {
    margin-top: 10px;
    font-size: 10.5px;
    line-height: 1.4;
    color: #ff8d8d;
  }

  .sdn-peers-detail-empty {
    font-size: 11px;
    color: #5d7681;
    letter-spacing: 0.06em;
  }
</style>
