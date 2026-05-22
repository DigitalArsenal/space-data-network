<script lang="ts">
  import { onMount } from 'svelte';
  import AppShell from './components/AppShell.svelte';
  import { createBackendFromLocation } from './lib/backend-context';
  import {
    createNodeIdentitySessionController,
    type NodeIdentitySessionController,
    type NodeIdentitySessionState,
  } from './lib/node-identity-session';
  import { normalizeSdnRoute, primaryRouteFromNormalized } from './lib/routes';
  import LocalDataScreen from './screens/LocalDataScreen.svelte';
  import NodeScreen from './screens/NodeScreen.svelte';
  import PeersScreen from './screens/PeersScreen.svelte';
  import type { HostedEpmRecord } from '../../src/ui/runtime/identity';
  import type {
    NodeSummary,
    NodeIdentityApplyResult,
    NodeIdentitySettings,
    ObservedSdnPeer,
    SdnBackend,
    SdnBackendMode,
  } from '../../src/ui/runtime/sdn-backend';

  let currentRoute = '/node';
  let backend: SdnBackend | null = null;
  let backendMode: SdnBackendMode = 'desktop-local';
  let nodeSummary: NodeSummary | null = null;
  let nodeState = 'loading';
  let nodeProfile: Record<string, unknown> | null = null;
  let peers: ObservedSdnPeer[] = [];
  let trustedPeers: ObservedSdnPeer[] = [];
  let hostedEpms: HostedEpmRecord[] = [];
  let storageLabel = 'pending';
  let primaryRoute = '/node';
  let screenTitle = 'Node';
  let nodeIdentitySession: NodeIdentitySessionController | null = null;
  let nodeIdentityReady = false;
  let nodeIdentityLocked = true;
  let nodeIdentitySettings: NodeIdentitySettings = { ttlMs: 3600000 };
  let nodeIdentityExpiresAt: number | null = null;
  let nodeIdentityStatus = 'Locked';
  let nodeIdentityMismatch: NodeIdentityApplyResult | null = null;
  let nodeIdentityLoginPromptKey = 0;
  let logoutConfirmOpen = false;
  let dataScreenPrimed = false;

  const screenTitles: Record<string, string> = {
    '/node': 'Node',
    '/peers': 'Peers',
    '/data': 'Data',
  };

  onMount(() => {
    let mounted = true;
    backend = createBackendFromLocation(window.location);
    backendMode = backend.mode;
    nodeIdentitySession = createNodeIdentitySessionController({
      backend,
      onStateChange: applyNodeIdentityState,
    });
    nodeIdentityReady = false;
    void nodeIdentitySession.loadSettings().finally(() => {
      if (mounted) {
        nodeIdentityReady = true;
      }
    });
    updateRouteFromLocation();
    window.addEventListener('hashchange', updateRouteFromLocation);
    backend.getNodeSummary().then((result) => {
      nodeSummary = result.data;
      nodeState = result.ok && result.data?.online ? 'online' : result.capability.state;
    }).catch((error: unknown) => {
      nodeState = error instanceof Error ? error.message : 'unavailable';
    });
    backend.getNodeProfile().then((result) => {
      nodeProfile = result.data;
    }).catch(() => {
      nodeProfile = null;
    });
    backend.listObservedPeers().then((result) => {
      peers = result.data ?? [];
    }).catch(() => {
      peers = [];
    });
    backend.listTrustedPeers().then((result) => {
      trustedPeers = result.data ?? [];
    }).catch(() => {
      trustedPeers = [];
    });
    loadHostedEpms();
    backend.getStorageSummary().then((result) => {
      storageLabel = formatBytes(result.data?.usedBytes);
    }).catch(() => {
      storageLabel = 'unavailable';
    });
    return () => {
      mounted = false;
      window.removeEventListener('hashchange', updateRouteFromLocation);
      nodeIdentitySession?.destroy();
    };
  });

  $: primaryRoute = primaryRouteFromNormalized(currentRoute);
  $: screenTitle = screenTitles[primaryRoute] ?? 'Node';
  $: if (backend || primaryRoute === '/data') dataScreenPrimed = true;

  function updateRouteFromLocation(): void {
    const rawRoute = window.location.hash || window.location.pathname;
    currentRoute = normalizeSdnRoute(rawRoute);
  }

  function formatBytes(value: number | null | undefined): string {
    if (!value) return 'pending';
    if (value > 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
    if (value > 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB`;
    return `${value} B`;
  }

  async function loadHostedEpms(): Promise<void> {
    if (!backend) {
      hostedEpms = [];
      return;
    }
    try {
      const result = await backend.listHostedEpms();
      hostedEpms = result.data ?? [];
    } catch {
      hostedEpms = [];
    }
  }

  function applyNodeIdentityState(state: NodeIdentitySessionState): void {
    nodeIdentityLocked = state.locked;
    nodeIdentitySettings = state.settings;
    nodeIdentityExpiresAt = state.sessionExpiresAt;
    nodeIdentityStatus = state.status;
    nodeIdentityMismatch = state.mismatch;
    if (!state.locked) {
      void reloadNodeIdentity();
    }
  }

  async function reloadNodeIdentity(): Promise<void> {
    if (!backend) return;
    try {
      const profileResult = await backend.getNodeProfile();
      nodeProfile = profileResult.data;
    } catch {
      nodeProfile = null;
    }
    try {
      const summaryResult = await backend.getNodeSummary();
      nodeSummary = summaryResult.data;
      nodeState = summaryResult.ok && summaryResult.data?.online ? 'online' : summaryResult.capability.state;
    } catch (error) {
      nodeState = error instanceof Error ? error.message : 'unavailable';
    }
    await loadHostedEpms();
  }

  function requestLogout(): void {
    logoutConfirmOpen = true;
  }

  async function confirmLogout(): Promise<void> {
    logoutConfirmOpen = false;
    if (nodeIdentitySession) {
      await nodeIdentitySession.logout();
    }
    currentRoute = '/node';
    if (window.location.hash !== '#/node') {
      window.location.hash = '#/node';
    }
    nodeIdentityLoginPromptKey += 1;
  }

  function cancelLogout(): void {
    logoutConfirmOpen = false;
  }

  async function confirmNodeIdentityReplacement(): Promise<void> {
    await nodeIdentitySession?.confirmNodeIdentityReplacement();
  }

  function declineNodeIdentityReplacement(): void {
    nodeIdentitySession?.declineNodeIdentityReplacement();
  }

  async function saveNodeIdentitySettings(settings: NodeIdentitySettings): Promise<void> {
    await nodeIdentitySession?.saveSettings(settings);
  }
</script>

<AppShell
  activeRoute={primaryRoute}
  {backendMode}
  {nodeState}
  peerCount={peers.length}
  {storageLabel}
  title={screenTitle}
  {nodeIdentityLocked}
  {nodeIdentityExpiresAt}
  onLogoutClick={requestLogout}
>
  {#if primaryRoute === '/peers'}
    <PeersScreen {backend} {peers} {hostedEpms} />
  {:else if primaryRoute !== '/data'}
    <NodeScreen
      {backend}
      summary={nodeSummary}
      profile={nodeProfile}
      {hostedEpms}
      {nodeIdentityReady}
      {nodeIdentityLocked}
      {nodeIdentitySession}
      {nodeIdentitySettings}
      {nodeIdentityStatus}
      {nodeIdentityMismatch}
      {nodeIdentityLoginPromptKey}
      onUnlock={reloadNodeIdentity}
      onHostedEpmsReload={loadHostedEpms}
      onNodeIdentitySettingsSave={saveNodeIdentitySettings}
    />
  {/if}
  {#if dataScreenPrimed}
    <div hidden={primaryRoute !== '/data'} aria-hidden={primaryRoute !== '/data'}>
      <LocalDataScreen
        {backend}
        {peers}
        {trustedPeers}
        route={currentRoute}
      />
    </div>
  {/if}
</AppShell>

{#if logoutConfirmOpen}
  <div class="sdn-modal-backdrop" role="presentation">
    <dialog class="sdn-modal" open aria-label="Confirm logout">
      <h2>Log out</h2>
      <p>Are you sure you want to log out?</p>
      <div class="sdn-toolbar sdn-section-toolbar">
        <button class="sdn-button" type="button" on:click={confirmLogout}>Logout</button>
        <button class="sdn-button sdn-button-muted" type="button" on:click={cancelLogout}>Cancel</button>
      </div>
    </dialog>
  </div>
{/if}

{#if nodeIdentityMismatch}
  <div class="sdn-modal-backdrop" role="presentation">
    <dialog class="sdn-modal" open aria-label="Confirm node identity replacement">
      <h2>Replace node keys</h2>
      <p>The selected wallet does not match the public keys in the current node EPM.</p>
      <div class="sdn-key-compare">
        <div>
          <span>Current</span>
          <code>{String(nodeIdentityMismatch.current?.peer_id ?? nodeIdentityMismatch.current?.peerId ?? 'unknown')}</code>
        </div>
        <div>
          <span>Wallet</span>
          <code>{String(nodeIdentityMismatch.proposed?.peer_id ?? nodeIdentityMismatch.proposed?.peerId ?? 'unknown')}</code>
        </div>
      </div>
      <div class="sdn-toolbar sdn-section-toolbar">
        <button class="sdn-button" type="button" on:click={confirmNodeIdentityReplacement}>Replace and sign EPM</button>
        <button class="sdn-button sdn-button-muted" type="button" on:click={declineNodeIdentityReplacement}>Cancel</button>
      </div>
    </dialog>
  </div>
{/if}
