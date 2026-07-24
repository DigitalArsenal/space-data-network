<script lang="ts">
  import type { SdnBackendMode } from '../../../src/ui/runtime/sdn-backend';
  import SideNav from './SideNav.svelte';
  import TopStatusBar from './TopStatusBar.svelte';

  export let activeRoute = '/node';
  export let backendMode: SdnBackendMode = 'desktop-local';
  export let nodeState = 'loading';
  export let peerCount: number | null = null;
  export let storageLabel = 'pending';
  export let title = 'Node';
</script>

<div class="sdn-app">
  <div class="sdn-shell">
    <SideNav {activeRoute} />
    <main class="sdn-main">
      <TopStatusBar
        {title}
        {backendMode}
        {nodeState}
        {peerCount}
        {storageLabel}
      />
      <section class="sdn-content" aria-label={title}>
        <slot />
      </section>
    </main>
  </div>
</div>
