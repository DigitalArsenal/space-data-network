<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { NodeSummary } from '../../../src/ui/runtime/sdn-backend';
  import IdentityPanel from '../components/IdentityPanel.svelte';

  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let hostedEpms: HostedEpmRecord[] = [];
</script>

<IdentityPanel {summary} {profile} {hostedEpms} />
