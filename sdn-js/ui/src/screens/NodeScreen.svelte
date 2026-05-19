<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { NodeIdentityApplyResult, NodeIdentitySettings, NodeSummary, SdnBackend } from '../../../src/ui/runtime/sdn-backend';
  import IdentityPanel from '../components/IdentityPanel.svelte';
  import NodeIdentityGate from '../components/NodeIdentityGate.svelte';
  import type { NodeIdentitySessionController } from '../lib/node-identity-session';

  export let backend: SdnBackend | null = null;
  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let hostedEpms: HostedEpmRecord[] = [];
  export let onHostedEpmsReload: () => void | Promise<void> = () => {};
  export let nodeIdentityReady = false;
  export let nodeIdentityLocked = true;
  export let nodeIdentitySession: NodeIdentitySessionController | null = null;
  export let nodeIdentitySettings: NodeIdentitySettings = { ttlMs: 3600000 };
  export let nodeIdentityStatus = 'Locked';
  export let nodeIdentityMismatch: NodeIdentityApplyResult | null = null;
  export let nodeIdentityLoginPromptKey = 0;
  export let onUnlock: () => void | Promise<void> = () => {};
  export let onNodeIdentitySettingsSave: (settings: NodeIdentitySettings) => void | Promise<void> = () => {};
</script>

{#if !nodeIdentityReady}
  <div class="sdn-node-identity-gate" aria-hidden="true"></div>
{:else if nodeIdentityLocked}
  <NodeIdentityGate
    controller={nodeIdentitySession}
    status={nodeIdentityStatus}
    mismatch={nodeIdentityMismatch}
    loginPromptKey={nodeIdentityLoginPromptKey}
    {onUnlock}
  />
{:else}
  <IdentityPanel
    {backend}
    {summary}
    {profile}
    {hostedEpms}
    {nodeIdentityLocked}
    {nodeIdentitySettings}
    onReload={onHostedEpmsReload}
    onNodeIdentitySettingsSave={onNodeIdentitySettingsSave}
  />
{/if}
