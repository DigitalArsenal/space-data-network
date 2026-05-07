<script lang="ts">
  import { onMount } from 'svelte';
  import { createBackendFromLocation } from './lib/backend-context';
  import { normalizeSdnRoute, primaryRouteFromNormalized } from './lib/routes';
  import type { NodeSummary, SdnBackendMode } from '../../src/ui/runtime/sdn-backend';

  const navItems = [
    { href: '#/node', route: '/node', label: 'Node' },
    { href: '#/peers', route: '/peers', label: 'Peers' },
    { href: '#/local-data', route: '/local-data', label: 'Local Data' },
  ] as const;

  let currentRoute = '/node';
  let backendMode: SdnBackendMode = 'desktop-local';
  let nodeSummary: NodeSummary | null = null;
  let nodeState = 'loading';

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
    return () => window.removeEventListener('hashchange', updateRouteFromLocation);
  });

  function updateRouteFromLocation(): void {
    const rawRoute = window.location.hash || window.location.pathname;
    currentRoute = normalizeSdnRoute(rawRoute);
  }

  function currentPrimaryRoute(): string {
    return primaryRouteFromNormalized(currentRoute);
  }

  function currentTitle(): string {
    return screenTitles[currentPrimaryRoute()] ?? 'Node';
  }
</script>

<div class="sdn-app">
  <div class="sdn-shell">
    <aside class="sdn-side-nav" aria-label="Primary">
      <div class="sdn-brand">
        <strong>SDN</strong>
        <span>{backendMode}</span>
      </div>
      <nav class="sdn-nav-list">
        {#each navItems as item}
          <a
            class="sdn-nav-link"
            href={item.href}
            aria-current={currentPrimaryRoute() === item.route ? 'page' : undefined}
          >
            {item.label}
          </a>
        {/each}
      </nav>
    </aside>

    <main class="sdn-main">
      <header class="sdn-top-bar">
        <h1>{currentTitle()}</h1>
        <div class="sdn-top-meta" aria-label="Runtime status">
          <span class="sdn-chip" data-state={nodeState === 'online' ? 'online' : 'degraded'}>{nodeState}</span>
          <span class="sdn-chip">{nodeSummary?.peerId ?? 'peer pending'}</span>
          <span class="sdn-chip">{nodeSummary?.agentVersion ?? backendMode}</span>
        </div>
      </header>

      <section class="sdn-content" aria-label={currentTitle()}>
        <article class="sdn-card">
          <h2>{currentTitle()}</h2>
          <p>{nodeSummary?.displayName ?? 'Space Data Network'}</p>
        </article>
      </section>
    </main>
  </div>
</div>
