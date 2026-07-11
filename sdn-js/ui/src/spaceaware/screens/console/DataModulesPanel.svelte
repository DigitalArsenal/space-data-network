<script lang="ts">
  /**
   * MODULES sub-view (loop task U3.6) — the DATA view's DATA STANDARDS/
   * MODULES toggle's second panel. Ground truth: the
   * `<!-- MODULES VIEW -->` block in `design_handoff/sdn_console/SDN Console.dc.html`
   * — a panel titled "ANALYSIS & PROPAGATION MODULES" with a right caption
   * ("loaded from all connected peers · paid & open"), a 2-column card grid,
   * each card carrying a name (+ optional PAID chip), an optional category
   * pill, a "provider · vX.Y.Z"-style sub-line, a status label, and a single
   * action button (UNLOAD/LOAD/SUBSCRIBE).
   *
   * Split out of `DataView.svelte` (which owns only the toggle itself) per
   * that file's original doc-comment note that this view could get large —
   * same rationale `DataStandardsExplorer.svelte` documents for its own
   * split. All data wiring lives in `../../lib/modules-data.ts` — this file
   * only renders its view-model strings verbatim, same split as every other
   * console view (`NodeView.svelte`/`PeersView.svelte`/
   * `DataStandardsExplorer.svelte`).
   *
   * Every honest degraded state below traces back to a real gap documented
   * in `modules-data.ts`: no `kind`/category surface (category pill only
   * renders when the module's own `manifest.pluginFamily` is present, in
   * ONE neutral color — never the mock's fabricated
   * PROPAGATION/INGEST/ANALYSIS 3-color scheme), no `provider` surface
   * (sub-line falls back to the module's own id), an anonymous session
   * (`GET /api/v1/modules/runtime` 401s → real listings still render,
   * ops disabled, with an honest sign-in note), and an empty marketplace
   * catalog (today's real state on this build — an honest "NO MODULES"
   * panel, never the mock's 11-row fixture).
   */
  import { onMount } from 'svelte';
  import {
    MODULES_ANONYMOUS_NOTE,
    buildModuleCards,
    loadModulesDashboardData,
    moduleCardActionStyle,
    modulesEmptyStateLabel,
    runModuleAction,
    type ModuleCardView,
    type ModulesDashboardData,
  } from '../../lib/modules-data';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  let { apiClient }: { apiClient: SdnApiClient } = $props();

  let dashboard = $state<ModulesDashboardData | null>(null);
  let loaded = $state(false);
  let pendingModuleId = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  const cards = $derived(buildModuleCards(dashboard?.runtime?.modules ?? [], dashboard?.listings ?? []));
  const emptyLabel = $derived(modulesEmptyStateLabel(loaded, cards.length));
  const showAnonymousNote = $derived(loaded && dashboard?.runtimeUnauthorized === true);

  async function refresh() {
    const data = await loadModulesDashboardData(apiClient);
    dashboard = data;
    loaded = true;
  }

  onMount(() => {
    void refresh();
  });

  async function handleAction(card: ModuleCardView) {
    if (!card.actionKind || card.actionKind === 'subscribe') return;
    if (!card.actionEnabled || pendingModuleId) return;
    pendingModuleId = card.id;
    actionError = null;
    const result = await runModuleAction(apiClient, card.id, card.actionKind);
    pendingModuleId = null;
    if (!result.ok) {
      actionError = result.message ?? 'Action failed.';
      return;
    }
    // Re-fetch so the card reflects the daemon's new status/action set
    // rather than an optimistic guess.
    void refresh();
  }
</script>

<section class="sdn-modules-panel">
  <div class="sdn-modules-header">
    <span class="sdn-modules-kicker">ANALYSIS &amp; PROPAGATION MODULES</span>
    <span class="sdn-modules-caption">loaded from all connected peers · paid &amp; open</span>
  </div>

  {#if showAnonymousNote}
    <div class="sdn-modules-anonymous-note">{MODULES_ANONYMOUS_NOTE}</div>
  {/if}

  {#if emptyLabel}
    <div class="sdn-modules-empty">{emptyLabel}</div>
  {:else}
    <div class="sdn-modules-grid">
      {#each cards as card (card.id)}
        {@const actionStyle = moduleCardActionStyle(card.actionKind)}
        <div class="sdn-modules-card">
          <div class="sdn-modules-card-main">
            <div class="sdn-modules-card-name-row">
              <span class="sdn-modules-card-name">{card.name}</span>
              {#if card.paid}
                <span class="sdn-modules-paid-chip">PAID</span>
              {/if}
            </div>
            <div class="sdn-modules-card-meta-row">
              {#if card.categoryLabel}
                <span class="sdn-modules-category-pill">{card.categoryLabel.toUpperCase()}</span>
              {/if}
              <span class="sdn-modules-provider-line">{card.providerLine}</span>
            </div>
          </div>
          <div class="sdn-modules-card-side">
            <span class="sdn-modules-status-label" style={`color:${card.statusColor};`}>{card.statusLabel}</span>
            {#if card.actionKind === 'subscribe'}
              <a
                class="sdn-modules-action-btn"
                style={`background:${actionStyle.background};border-color:${actionStyle.border};color:${actionStyle.color};`}
                href={card.subscribeHref}
                title={card.actionTooltip}
              >
                {card.actionLabel}
              </a>
            {:else}
              <button
                type="button"
                class="sdn-modules-action-btn"
                style={`background:${actionStyle.background};border-color:${actionStyle.border};color:${actionStyle.color};`}
                disabled={!card.actionEnabled || pendingModuleId === card.id}
                title={card.actionTooltip}
                onclick={() => handleAction(card)}
              >
                {pendingModuleId === card.id ? '···' : card.actionLabel}
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if actionError}
    <div class="sdn-modules-action-error">{actionError}</div>
  {/if}
</section>

<style>
  .sdn-modules-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 15px 16px;
  }

  .sdn-modules-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 14px;
    flex-wrap: wrap;
    gap: 6px;
  }

  .sdn-modules-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }

  .sdn-modules-caption {
    font-size: 10px;
    letter-spacing: 0.1em;
    color: #5d7681;
  }

  .sdn-modules-anonymous-note {
    font-size: 11px;
    line-height: 1.4;
    color: #ffb24d;
    border: 1px solid rgba(255, 178, 77, 0.35);
    background: rgba(255, 178, 77, 0.06);
    padding: 8px 10px;
    margin-bottom: 14px;
  }

  .sdn-modules-empty {
    padding: 24px 4px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
    text-align: center;
  }

  .sdn-modules-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }

  .sdn-modules-card {
    display: flex;
    align-items: center;
    gap: 12px;
    background: rgba(255, 255, 255, 0.015);
    border: 1px solid rgba(90, 150, 180, 0.2);
    padding: 12px 14px;
  }

  .sdn-modules-card-main {
    flex: 1;
    min-width: 0;
  }

  .sdn-modules-card-name-row {
    display: flex;
    align-items: center;
    gap: 7px;
    flex-wrap: wrap;
  }

  .sdn-modules-card-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 15.5px;
    color: #eaf6f8;
  }

  .sdn-modules-paid-chip {
    font-size: 9px;
    letter-spacing: 0.08em;
    color: #35c9d8;
    border: 1px solid rgba(53, 201, 216, 0.45);
    padding: 1px 5px;
    flex: none;
  }

  .sdn-modules-card-meta-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 5px;
    flex-wrap: wrap;
  }

  .sdn-modules-category-pill {
    font-size: 9.5px;
    letter-spacing: 0.1em;
    color: #9fd4f5;
    background: rgba(74, 166, 224, 0.1);
    padding: 1px 6px;
    flex: none;
  }

  .sdn-modules-provider-line {
    font-size: 11px;
    color: #6f8693;
    overflow-wrap: anywhere;
  }

  .sdn-modules-card-side {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 7px;
    flex: none;
  }

  .sdn-modules-status-label {
    font-size: 11px;
    letter-spacing: 0.06em;
  }

  .sdn-modules-action-btn {
    display: inline-block;
    border: 1px solid;
    padding: 5px 13px;
    font-size: 11px;
    letter-spacing: 0.08em;
    cursor: pointer;
    text-decoration: none;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-modules-action-btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-modules-action-error {
    margin-top: 12px;
    font-size: 10.5px;
    line-height: 1.4;
    color: #ff8d8d;
  }
</style>
