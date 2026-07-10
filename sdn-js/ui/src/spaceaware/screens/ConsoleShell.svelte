<script lang="ts">
  /**
   * SDN Console shell (loop task U3.1) — the layout engine every console
   * view (NODE/PEERS/GROUPS/DATA/CHANNELS/CONJUNCTION) sits inside. Ground
   * truth: `SDN Console.dc.html`'s `<!-- COLLAPSIBLE RAIL -->` +
   * `<!-- MAIN -->` + `<!-- QR overlay -->` blocks and its README.md.
   *
   * Only the NODE view (`screens/console/NodeView.svelte`) is a real port
   * right now; the other five render `ConsolePlaceholder.svelte` inside
   * this same shell (rail/header/chips all real, view body pending its own
   * loop task) — see the loop task's scope note.
   *
   * Deep-link compatibility: the `.dc.html` prototype reads `?route=` /
   * `?group=` once on mount (`componentDidMount`) and sets its OWN internal
   * state — it never had real sub-routes. This app already has real
   * History-API paths (`/console/{view}`, `router.ts`), so on mount here we
   * map any `?route=` query param onto the equivalent path and `navigate()`
   * to it once; `?group=` is captured but not consumed yet (the GROUPS view
   * that would use it is a later loop task) so the scheme keeps working
   * once that view lands.
   */
  import { onMount } from 'svelte';
  import ConsoleRail from './console/ConsoleRail.svelte';
  import ConsoleHeader from './console/ConsoleHeader.svelte';
  import ConsolePlaceholder from './console/ConsolePlaceholder.svelte';
  import NodeView from './console/NodeView.svelte';
  import QrOverlay from './console/QrOverlay.svelte';
  import { consoleHealthChipState, resolveConsoleDeepLinkPath, type ConsoleHealthChipState } from '../lib/console';
  import { parseHealthResponse } from '../lib/login';
  import type { ConsoleView, SpaceAwareRoute } from '../router';
  import type { AuthSessionState } from '../../lib/auth/auth-store';
  import type { SdnApiClient } from '../../lib/auth/sdn-api-client';

  let {
    route,
    navigate,
    authState,
    apiClient,
  }: {
    route: SpaceAwareRoute;
    navigate: (path: string) => void;
    authState: AuthSessionState;
    apiClient: SdnApiClient;
  } = $props();

  const HEALTH_REFRESH_MS = 30_000;

  let healthState = $state<ConsoleHealthChipState>('OFFLINE');
  let qrOpen = $state(false);

  // ConsoleShell only ever mounts for `route.screen === 'console'`
  // (SpaceAwareApp.svelte's branch) — router.ts's `matchSpaceAwareRoute`
  // guarantees `route.sub` is a `ConsoleView` (never a `Bmc2Mode`) in that
  // case, but `SpaceAwareRoute.sub`'s type is shared across screens, hence
  // the cast.
  const activeView = $derived((route.sub ?? 'node') as ConsoleView);

  async function refreshHealth() {
    try {
      const result = await apiClient.requestJson<unknown>('/data/health');
      healthState = consoleHealthChipState(parseHealthResponse(result.data));
    } catch {
      healthState = consoleHealthChipState('ALERT');
    }
  }

  function closeQr() {
    qrOpen = false;
  }

  function openQr() {
    qrOpen = true;
  }

  onMount(() => {
    try {
      const target = resolveConsoleDeepLinkPath(window.location.search, activeView);
      if (target) navigate(target);
    } catch {
      // Malformed query string — nothing to redirect to.
    }

    void refreshHealth();
    const healthInterval = setInterval(() => void refreshHealth(), HEALTH_REFRESH_MS);
    return () => clearInterval(healthInterval);
  });
</script>

<div class="sdn-console-root" data-screen-label="SDN Console">
  <ConsoleRail {activeView} {navigate} />

  <main class="sdn-console-main">
    <ConsoleHeader view={activeView} {healthState} sessionStatus={authState.status} {navigate} />

    <div class="sdn-console-content">
      {#if activeView === 'node'}
        <NodeView onOpenQr={openQr} {apiClient} {healthState} />
      {:else}
        <ConsolePlaceholder view={activeView} />
      {/if}
    </div>
  </main>

  <QrOverlay open={qrOpen} onClose={closeQr} />
</div>

<style>
  .sdn-console-root {
    position: fixed;
    inset: 0;
    background: radial-gradient(circle at 50% -8%, #0a1722, #04060a 55%);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
  }

  .sdn-console-main {
    position: absolute;
    left: 66px;
    right: 0;
    top: 0;
    bottom: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .sdn-console-content {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 18px 24px;
  }
</style>
