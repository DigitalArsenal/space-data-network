<script lang="ts">
  import type { SdnBackendMode } from '../../../src/ui/runtime/sdn-backend';
  import StatusChip from './StatusChip.svelte';

  export let title = 'Node';
  export let backendMode: SdnBackendMode = 'desktop-local';
  export let nodeState = 'loading';
  export let peerCount: number | null = null;
  export let storageLabel = 'pending';
</script>

<header class="sdn-top-bar">
  <div>
    <h1>{title}</h1>
    <p>{backendMode}</p>
  </div>
  <div class="sdn-top-meta" aria-label="Runtime">
    <StatusChip label="Node" value={nodeState} tone={nodeState === 'online' ? 'online' : 'warning'} />
    <StatusChip label="Peers" value={peerCount ?? 0} />
    <StatusChip label="Storage" value={storageLabel} />
  </div>
</header>
