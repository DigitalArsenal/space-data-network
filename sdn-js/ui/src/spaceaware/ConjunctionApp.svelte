<script lang="ts">
  /**
   * Standalone CONJUNCTION-only ship root (loop SDN_SPACEAWARE_UI_LOOP.md
   * Phase C, task C1). Mounts the reused `ConjunctionView` at `/` with only
   * the chrome it needs — a header status strip — and nothing else: no
   * console rail, no routes to descoped screens, no login. The full
   * SpaceAware app (`SpaceAwareApp.svelte`) stays committed and dormant; this
   * is a separate, thinner entry whose single-file build bundles ONLY the
   * conjunction code + fonts. The isolated typed public wallet client is
   * bundled for Login/Account, but wallet-origin credential, signing, and
   * hd-wallet wasm implementation remain outside this document.
   *
   * Data sources are all anonymous-safe (`/api/v1/peers`, `/api/v1/channels`,
   * `/api/v1/stats`, plus `/api/v1/data/health` for the header chip), so the
   * `SdnApiClient` here is instantiated directly — no `authStore`, no wallet
   * unlock, none of the login machinery that pulls the hd-wallet wasm into the
   * full-app bundle.
   *
   * The header reuses the ported console header's exact styling helpers
   * (`consoleHealthChipStyle`/`hexToRgba`/`CONSOLE_TITLES`/etc. from
   * `lib/console.ts`, already in this bundle via `conjunction-data.ts` →
   * `node-data.ts`) so the CONJUNCTION title/subtitle and the health chip
   * render pixel-identical to how they looked under the console shell in
   * U3.9 — minus the rail-linked "TRUSTED PEERS" chip (that navigated to the
   * descoped PEERS view) and minus the dynamic IDENTITY chip (this ship never
   * authenticates; it shows a fixed honest PUBLIC · ANONYMOUS chip instead).
   */
  import { onMount } from 'svelte';
  import PublicWalletPresenter from '../lib/PublicWalletPresenter.svelte';
  import { getSdnWalletClient } from '../lib/auth/wallet-client';
  import ConjunctionView from './screens/console/ConjunctionView.svelte';
  import { SdnApiClient } from '../lib/auth/sdn-api-client';
  import {
    CONSOLE_SUBTITLES,
    CONSOLE_TITLES,
    consoleHealthChipState,
    consoleHealthChipStyle,
    consoleTitleAccent,
    hexToRgba,
    type ConsoleHealthChipState,
  } from './lib/console';
  import { parseHealthResponse } from './lib/login';
  import {
    classifyConjunctionAppNav,
    conjunctionAppSessionChip,
    conjunctionAppShows3dLink,
  } from './lib/conjunction-app';

  const HEALTH_REFRESH_MS = 30_000;

  const apiClient = new SdnApiClient();
  const walletClient = getSdnWalletClient(document);

  let healthState = $state<ConsoleHealthChipState>('OFFLINE');
  const healthChip = $derived(consoleHealthChipStyle(healthState));
  const sessionChip = conjunctionAppSessionChip();

  async function refreshHealth() {
    try {
      const result = await apiClient.requestJson<unknown>('/data/health');
      healthState = consoleHealthChipState(parseHealthResponse(result.data));
    } catch {
      healthState = consoleHealthChipState('ALERT');
    }
  }

  /**
   * Navigation for the reused ConjunctionView. Its only `navigate()` caller is
   * the "OPEN IN 3D" affordance, which targets the descoped `/orbital` view.
   * In this ship that button is HIDDEN (`show3dLink={false}` below, C3
   * disposition), so `navigate()` is not reached for a descoped target in
   * practice; it remains a defensive documented no-op for descoped targets
   * (see `classifyConjunctionAppNav`) should any future caller appear.
   */
  function navigate(path: string) {
    if (classifyConjunctionAppNav(path) === 'internal') {
      // The only in-app surface is the conjunction view already showing.
      return;
    }
    // Descoped full-app route (e.g. /orbital) — not bundled in this ship.
  }

  onMount(() => {
    void refreshHealth();
    const healthInterval = setInterval(() => void refreshHealth(), HEALTH_REFRESH_MS);
    return () => clearInterval(healthInterval);
  });
</script>

<div class="conj-app-root" data-screen-label="Conjunction Screening">
  <header class="conj-app-header">
    <div class="conj-app-header-title-block">
      <div class="conj-app-header-kicker">SPACE OPERATIONS NETWORK CONSOLE</div>
      <div class="conj-app-header-title-row">
        <span class="conj-app-header-title">{CONSOLE_TITLES.conjunction}</span>
        <span class="conj-app-header-subtitle" style={`color:${consoleTitleAccent('conjunction')};`}
          >{CONSOLE_SUBTITLES.conjunction}</span
        >
      </div>
    </div>

    <div class="conj-app-header-chips">
      <PublicWalletPresenter client={walletClient} />

      <span
        class="conj-app-chip"
        title={`Node health: ${healthChip.label}`}
        style={`border-color:${hexToRgba(healthChip.color, 0.4)};background:${hexToRgba(healthChip.color, 0.06)};color:${healthChip.color};`}
      >
        <span class="conj-app-chip-dot" style={`background:${healthChip.color};box-shadow:0 0 7px ${healthChip.color};`}
        ></span>{healthChip.label}
      </span>

      <span
        class="conj-app-chip"
        title="Public conjunction screening — no operator session"
        style={`border-color:${hexToRgba(sessionChip.color, 0.4)};background:${hexToRgba(sessionChip.color, 0.06)};color:${sessionChip.color};`}
      >
        <span
          class="conj-app-chip-dot"
          style={`background:${sessionChip.color};box-shadow:0 0 7px ${sessionChip.color};`}
        ></span>{sessionChip.label}
      </span>
    </div>
  </header>

  <div class="conj-app-content">
    <ConjunctionView {apiClient} {navigate} show3dLink={conjunctionAppShows3dLink()} />
  </div>
</div>

<style>
  .conj-app-root {
    position: fixed;
    inset: 0;
    background: radial-gradient(circle at 50% -8%, #0a1722, #04060a 55%);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    -webkit-font-smoothing: antialiased;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .conj-app-header {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 18px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.18);
    background: rgba(10, 15, 21, 0.92);
    padding: 14px 24px;
  }

  .conj-app-header-kicker {
    font-size: 11.5px;
    letter-spacing: 0.22em;
    color: #5d7681;
    margin-bottom: 5px;
  }

  .conj-app-header-title-row {
    display: flex;
    align-items: baseline;
    gap: 13px;
  }

  .conj-app-header-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 29px;
    letter-spacing: 0.1em;
    color: #eaf6f8;
  }

  .conj-app-header-subtitle {
    font-size: 11.5px;
    letter-spacing: 0.16em;
  }

  .conj-app-header-chips {
    display: flex;
    gap: 8px;
  }

  .conj-app-chip {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    border: 1px solid rgba(90, 150, 180, 0.3);
    background: transparent;
    padding: 5px 11px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
    letter-spacing: 0.08em;
    color: #9fb3bc;
  }

  .conj-app-chip-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
  }

  .conj-app-content {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 18px 24px;
  }
</style>
