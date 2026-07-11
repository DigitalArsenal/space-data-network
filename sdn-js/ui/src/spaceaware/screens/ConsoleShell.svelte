<script lang="ts">
  /**
   * SDN Console shell (loop task U3.1) — the layout engine every console
   * view (NODE/PEERS/GROUPS/DATA/CHANNELS/CONJUNCTION) sits inside. Ground
   * truth: `SDN Console.dc.html`'s `<!-- COLLAPSIBLE RAIL -->` +
   * `<!-- MAIN -->` + `<!-- QR overlay -->` blocks and its README.md.
   *
   * NODE (`screens/console/NodeView.svelte`, loop U3.1/U3.2), PEERS
   * (`screens/console/PeersView.svelte`, loop U3.3), GROUPS
   * (`screens/console/GroupsView.svelte`, loop U3.8, client-local per
   * decision D5), DATA (`screens/console/DataView.svelte`, loop U3.5), and
   * CHANNELS (`screens/console/ChannelsView.svelte`, loop U3.7) are real
   * ports now; the remaining view (CONJUNCTION) renders
   * `ConsolePlaceholder.svelte` inside this same shell (rail/header/chips
   * all real, view body pending its own loop task) — see the loop task's
   * scope note.
   *
   * Deep-link compatibility: the `.dc.html` prototype reads `?route=` /
   * `?group=` once on mount (`componentDidMount`) and sets its OWN internal
   * state — it never had real sub-routes. This app already has real
   * History-API paths (`/console/{view}`, `router.ts`), so on mount here we
   * map any `?route=` query param onto the equivalent path and `navigate()`
   * to it once; `?group=` is captured here but consumed by
   * `GroupsView.svelte` itself (it re-parses `window.location.search` on
   * its own mount — see that file's doc comment), since a `?group=`-only
   * deep link (no `?route=`) never triggers this component's own
   * `navigate()` call.
   */
  import { onMount } from 'svelte';
  import ConsoleRail from './console/ConsoleRail.svelte';
  import ConsoleHeader from './console/ConsoleHeader.svelte';
  import ConsolePlaceholder from './console/ConsolePlaceholder.svelte';
  import NodeView from './console/NodeView.svelte';
  import PeersView from './console/PeersView.svelte';
  import GroupsView from './console/GroupsView.svelte';
  import DataView from './console/DataView.svelte';
  import ChannelsView from './console/ChannelsView.svelte';
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
      {:else if activeView === 'peers'}
        <PeersView {apiClient} {authState} />
      {:else if activeView === 'groups'}
        <GroupsView {navigate} />
      {:else if activeView === 'data'}
        <DataView {apiClient} />
      {:else if activeView === 'channels'}
        <ChannelsView {apiClient} {authState} />
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
