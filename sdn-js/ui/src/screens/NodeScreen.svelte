<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { NodeSummary, SdnBackend } from '../../../src/ui/runtime/sdn-backend';
  import AdvancedDrawer from '../components/AdvancedDrawer.svelte';
  import IdentityPanel from '../components/IdentityPanel.svelte';

  export let backend: SdnBackend | null = null;
  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let hostedEpms: HostedEpmRecord[] = [];
  export let onHostedEpmsReload: () => void | Promise<void> = () => {};
  export let advancedOpen = false;
</script>

<IdentityPanel
  {backend}
  {summary}
  {profile}
  {hostedEpms}
  onReload={onHostedEpmsReload}
/>

<button class="sdn-button" type="button" on:click={() => advancedOpen = !advancedOpen}>Advanced</button>
<AdvancedDrawer open={advancedOpen} title="Kubo Diagnostics">
  <p>Low-level Kubo and IPFS controls stay behind this drawer.</p>
</AdvancedDrawer>
