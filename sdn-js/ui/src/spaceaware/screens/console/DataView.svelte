<script lang="ts">
  /**
   * DATA console view (loop task U3.5 — spacedatastandards.org standards +
   * schema explorer). Ground truth: the
   * `<!-- ============ DATA ============ -->` block in
   * `design_handoff/sdn_console/SDN Console.dc.html` — a banner
   * ("spacedatastandards.org" wordmark + package version chip + standards
   * count + FLATSQL STORE sync status + a DATA STANDARDS/MODULES toggle)
   * above either the STANDARDS explorer (`DataStandardsExplorer.svelte`) or
   * a MODULES placeholder panel. The shared `ConsoleHeader` already renders
   * "DATA · STANDARDS WORKBENCH", so this view starts at the banner.
   *
   * All data wiring lives in `../../lib/standards-data.ts`
   * (channels+stats+node/info fetch/join/sort) and `../../lib/standards-fbs.ts`
   * (the vendored `.fbs` corpus + parser) — this file renders their
   * view-model strings verbatim, same split as `NodeView.svelte`/
   * `PeersView.svelte`. Every honest empty/degraded state below (0-row
   * standards, the "no vendored schema" EXPLORER/GENERATE fallback, the
   * degraded SCHEMA SYNC banner label) traces back to a real gap documented
   * in those lib files — nothing here fabricates data the daemon/vendored
   * package doesn't actually provide.
   *
   * MODULES (loading/unloading analysis & propagation modules from
   * connected peers) and the STANDARDS detail's own DATA tab (a local
   * FlatSQL query workbench) are BOTH explicitly out of this task's scope
   * (loop task U3.6) — both render the same honest
   * `ConsolePlaceholder.svelte`-style copy instead of the mock's fabricated
   * module list / query output fixtures. The MODULES toggle itself still
   * has to render pixel-true (loop task spec), so it's a real, working
   * toggle — only its content is a placeholder.
   */
  import { onMount } from 'svelte';
  import DataStandardsExplorer from './DataStandardsExplorer.svelte';
  import {
    DATA_VIEW_TOGGLES,
    MODULES_PLACEHOLDER_COPY,
    MODULES_PLACEHOLDER_TITLE,
    buildSchemaSyncBannerView,
    dataViewToggleStyle,
    formatSdsPackageVersionChip,
    formatStandardsCountCaption,
    joinStandardsWithStats,
    loadStandardsDashboardData,
    sortStandardEntries,
    type DataViewToggle,
    type StandardsDashboardData,
  } from '../../lib/standards-data';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  let { apiClient }: { apiClient: SdnApiClient } = $props();

  let dashboard = $state<StandardsDashboardData | null>(null);
  let loaded = $state(false);
  let dataView = $state<DataViewToggle>('standards');
  let selectedCode = $state<string | null>(null);

  const entries = $derived(
    sortStandardEntries(joinStandardsWithStats(dashboard?.channels ?? [], dashboard?.stats ?? null)),
  );
  const bannerView = $derived(buildSchemaSyncBannerView(dashboard !== null && dashboard.stats !== null));
  const sdsPackageVersion = $derived(dashboard?.nodeInfo?.standardsVersion ?? null);
  const sdsVersionChip = $derived(formatSdsPackageVersionChip(sdsPackageVersion));
  const standardsCountCaption = $derived(formatStandardsCountCaption(entries.length));

  onMount(() => {
    void loadStandardsDashboardData(apiClient).then((data) => {
      dashboard = data;
      loaded = true;
    });
  });

  function selectStandard(code: string) {
    selectedCode = code;
  }

  function setDataView(view: DataViewToggle) {
    dataView = view;
  }
</script>

<div class="sdn-data-root">
  <section class="sdn-data-banner">
    <div class="sdn-data-wordmark-row">
      <span class="sdn-data-wordmark">spacedatastandards<span class="sdn-data-wordmark-accent">.org</span></span>
      <span class="sdn-data-version-chip">{sdsVersionChip}</span>
    </div>
    <span class="sdn-data-count-caption">{standardsCountCaption}</span>
    <span class="sdn-data-banner-spacer"></span>
    <div class="sdn-data-sync-status">
      <span class="sdn-data-sync-dot" style={`background:${bannerView.dotColor};box-shadow:0 0 6px ${bannerView.dotColor};`}
      ></span>
      {bannerView.label}
    </div>
    <div class="sdn-data-view-toggle">
      {#each DATA_VIEW_TOGGLES as toggle (toggle.id)}
        {@const style = dataViewToggleStyle(toggle.id, dataView)}
        <button
          type="button"
          class="sdn-data-view-toggle-btn"
          style={`background:${style.background};color:${style.color};`}
          title={`Switch to ${toggle.label}`}
          onclick={() => setDataView(toggle.id)}
        >
          {toggle.label}
        </button>
      {/each}
    </div>
  </section>

  {#if dataView === 'standards'}
    <DataStandardsExplorer {entries} {selectedCode} onSelect={selectStandard} {loaded} {sdsPackageVersion} />
  {:else}
    <section class="sdn-data-modules-placeholder">
      <div class="sdn-data-placeholder-title">{MODULES_PLACEHOLDER_TITLE}</div>
      <p class="sdn-data-placeholder-copy">{MODULES_PLACEHOLDER_COPY}</p>
    </section>
  {/if}
</div>

<style>
  .sdn-data-root {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .sdn-data-banner {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 13px 16px;
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }

  .sdn-data-wordmark-row {
    display: flex;
    align-items: baseline;
    gap: 9px;
  }

  .sdn-data-wordmark {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 18px;
    letter-spacing: 0.02em;
    color: #eaf6f8;
  }

  .sdn-data-wordmark-accent {
    color: #35c9d8;
  }

  .sdn-data-version-chip {
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #9fd4f5;
    border: 1px solid rgba(120, 190, 230, 0.45);
    padding: 2px 8px;
  }

  .sdn-data-count-caption {
    font-size: 11px;
    letter-spacing: 0.04em;
    color: #6f8693;
  }

  .sdn-data-banner-spacer {
    flex: 1;
  }

  .sdn-data-sync-status {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 11px;
    color: #7d929b;
  }

  .sdn-data-sync-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
  }

  .sdn-data-view-toggle {
    display: flex;
    border: 1px solid rgba(90, 150, 180, 0.3);
  }

  .sdn-data-view-toggle-btn {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 11.5px;
    letter-spacing: 0.1em;
    border: 0;
    cursor: pointer;
    padding: 6px 14px;
    transition:
      background 0.14s,
      color 0.14s;
  }

  .sdn-data-modules-placeholder {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 15px 16px;
  }

  .sdn-data-placeholder-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 13px;
    letter-spacing: 0.08em;
    color: #9fb3bc;
    margin-bottom: 8px;
  }

  .sdn-data-placeholder-copy {
    margin: 0;
    font-size: 11px;
    line-height: 1.5;
    color: #5d7681;
  }
</style>
