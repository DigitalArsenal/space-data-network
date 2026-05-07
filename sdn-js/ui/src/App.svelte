<script lang="ts">
  import { onMount } from 'svelte';
  import AppShell from './components/AppShell.svelte';
  import { createBackendFromLocation } from './lib/backend-context';
  import { normalizeSdnRoute, primaryRouteFromNormalized } from './lib/routes';
  import LocalDataScreen from './screens/LocalDataScreen.svelte';
  import NodeScreen from './screens/NodeScreen.svelte';
  import PeersScreen from './screens/PeersScreen.svelte';
  import type {
    BackendCapability,
    LocalObjectSummary,
    NodeSummary,
    ObservedSdnPeer,
    SdnBackendMode,
    StorageSummary,
  } from '../../src/ui/runtime/sdn-backend';

  let currentRoute = '/node';
  let backendMode: SdnBackendMode = 'desktop-local';
  let nodeSummary: NodeSummary | null = null;
  let nodeState = 'loading';
  let nodeProfile: Record<string, unknown> | null = null;
  let capabilities: BackendCapability[] = [];
  let peers: ObservedSdnPeer[] = [];
  let storage: StorageSummary | null = null;
  let objects: LocalObjectSummary[] = [];
  let walletState = 'pending';
  let storageLabel = 'pending';
  let primaryRoute = '/node';
  let screenTitle = 'Node';

  const screenTitles: Record<string, string> = {
    '/node': 'Node',
    '/peers': 'Peers',
    '/local-data': 'Local Data',
    '/advanced': 'Advanced',
    '/claim-core': 'Claim Core',
  };

  onMount(() => {
    const backend = createBackendFromLocation(window.location);
    backendMode = backend.mode;
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
      walletState = result.ok ? 'claimed' : result.capability.state;
    }).catch(() => {
      walletState = 'unavailable';
    });
    backend.getCapabilities().then((result) => {
      capabilities = result;
    }).catch(() => {
      capabilities = [];
    });
    backend.listObservedPeers().then((result) => {
      peers = result.data ?? [];
    }).catch(() => {
      peers = [];
    });
    backend.getStorageSummary().then((result) => {
      storage = result.data;
      storageLabel = formatBytes(result.data?.usedBytes);
    }).catch(() => {
      storageLabel = 'unavailable';
    });
    backend.listObjects().then((result) => {
      objects = result.data ?? [];
    }).catch(() => {
      objects = [];
    });
    return () => window.removeEventListener('hashchange', updateRouteFromLocation);
  });

  function updateRouteFromLocation(): void {
    const rawRoute = window.location.hash || window.location.pathname;
    currentRoute = normalizeSdnRoute(rawRoute);
  }

  $: primaryRoute = primaryRouteFromNormalized(currentRoute);
  $: screenTitle = screenTitles[primaryRoute] ?? 'Node';

  function formatBytes(value: number | null | undefined): string {
    if (!value) return 'pending';
    if (value > 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
    if (value > 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB`;
    return `${value} B`;
  }
</script>

<AppShell
  activeRoute={primaryRoute}
  {backendMode}
  {nodeState}
  peerCount={peers.length}
  {walletState}
  {storageLabel}
  title={screenTitle}
>
  {#if primaryRoute === '/peers'}
    <PeersScreen {peers} />
  {:else if primaryRoute === '/local-data'}
    <LocalDataScreen {storage} {objects} />
  {:else}
    <NodeScreen summary={nodeSummary} profile={nodeProfile} {capabilities} />
  {/if}
</AppShell>
