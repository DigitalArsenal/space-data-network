<script lang="ts">
  import { onMount } from 'svelte';
  import AppShell from './components/AppShell.svelte';
  import { createBackendFromLocation } from './lib/backend-context';
  import { normalizeSdnRoute, primaryRouteFromNormalized } from './lib/routes';
  import LocalDataScreen from './screens/LocalDataScreen.svelte';
  import NodeScreen from './screens/NodeScreen.svelte';
  import PeersScreen from './screens/PeersScreen.svelte';
  import type { HostedEpmRecord } from '../../src/ui/runtime/identity';
  import type {
    NodeSummary,
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
  let hostedEpms: HostedEpmRecord[] = [];
  let storageLabel = 'pending';
  let primaryRoute = '/node';
  let screenTitle = 'Node';

  const screenTitles: Record<string, string> = {
    '/node': 'Node',
    '/peers': 'Peers',
    '/data': 'Data',
    '/advanced': 'Advanced',
  };

  onMount(() => {
    backend = createBackendFromLocation(window.location);
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
    }).catch(() => {
      nodeProfile = null;
    });
    backend.listObservedPeers().then((result) => {
      peers = result.data ?? [];
    }).catch(() => {
      peers = [];
    });
    loadHostedEpms();
    backend.getStorageSummary().then((result) => {
      storageLabel = formatBytes(result.data?.usedBytes);
    }).catch(() => {
      storageLabel = 'unavailable';
    });
    return () => window.removeEventListener('hashchange', updateRouteFromLocation);
  });

  $: primaryRoute = primaryRouteFromNormalized(currentRoute);
  $: screenTitle = screenTitles[primaryRoute] ?? 'Node';

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
</script>

<AppShell
  activeRoute={primaryRoute}
  {backendMode}
  {nodeState}
  peerCount={peers.length}
  {storageLabel}
  title={screenTitle}
>
  {#if primaryRoute === '/peers'}
    <PeersScreen {backend} {peers} {hostedEpms} />
  {:else if primaryRoute === '/data'}
    <LocalDataScreen
      {backend}
      {peers}
      route={currentRoute}
    />
  {:else}
    <NodeScreen
      {backend}
      summary={nodeSummary}
      profile={nodeProfile}
      {hostedEpms}
      onHostedEpmsReload={loadHostedEpms}
    />
  {/if}
</AppShell>
