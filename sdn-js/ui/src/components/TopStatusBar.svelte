<script lang="ts">
  import type { SdnBackendMode } from '../../../src/ui/runtime/sdn-backend';
  import StatusChip from './StatusChip.svelte';

  export let title = 'Node';
  export let backendMode: SdnBackendMode = 'desktop-local';
  export let nodeState = 'loading';
  export let peerCount: number | null = null;
  export let storageLabel = 'pending';
  export let nodeIdentityLocked = true;
  export let nodeIdentityExpiresAt: number | null = null;
  export let onLogoutClick: () => void = () => {};

  $: identityLabel = nodeIdentityLocked ? 'locked' : expiresAtLabel(nodeIdentityExpiresAt);

  function logoutClick(): void {
    onLogoutClick();
  }

  function expiresAtLabel(value: number | null): string {
    if (!value) return 'unlocked';
    const minutes = Math.max(1, Math.ceil((value - Date.now()) / 60000));
    return `${minutes}m`;
  }
</script>

<header class="sdn-top-bar">
  <div>
    <h1>{title}</h1>
    <p>{backendMode}</p>
  </div>
  <div class="sdn-top-meta" aria-label="Runtime">
    <StatusChip label="Node" value={nodeState} tone={nodeState === 'online' ? 'online' : 'warning'} />
    <StatusChip label="Identity" value={identityLabel} tone={nodeIdentityLocked ? 'warning' : 'online'} />
    <StatusChip label="Peers" value={peerCount ?? 0} />
    <StatusChip label="Storage" value={storageLabel} />
    {#if !nodeIdentityLocked}
      <button class="sdn-button sdn-button-compact sdn-button-muted" type="button" on:click={logoutClick}>Logout</button>
    {/if}
  </div>
</header>
