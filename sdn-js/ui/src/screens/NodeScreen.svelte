<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { BackendCapability, NodeSummary, SdnBackend } from '../../../src/ui/runtime/sdn-backend';
  import AdvancedDrawer from '../components/AdvancedDrawer.svelte';
  import IdentityPanel from '../components/IdentityPanel.svelte';
  import MetricCard from '../components/cards/MetricCard.svelte';

  export let backend: SdnBackend | null = null;
  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let capabilities: BackendCapability[] = [];
  export let hostedEpms: HostedEpmRecord[] = [];
  export let onHostedEpmsReload: () => void | Promise<void> = () => {};
  export let advancedOpen = false;

  $: claimed = Boolean(summary?.peerId || profile?.peer_id || profile?.PeerID);
  $: runtime = summary?.runtime ?? 'desktop-local';
  $: nodeSelfEpms = hostedEpms.filter((record) => record.kind === 'node-self').length;
  $: hostedOnlyEpms = hostedEpms.filter((record) => record.kind === 'hosted').length;
</script>

<div class="sdn-grid sdn-grid-2">
  <MetricCard title="Runtime Mode" value={runtime} detail="Active backend adapter" tone="special" />
  <MetricCard title="Identity" value={claimed ? 'claimed' : 'pending'} detail={summary?.peerId ?? 'No peer identity loaded'} tone={claimed ? 'online' : 'warning'} />
</div>

<section class="sdn-panel-grid">
  <article class="sdn-card sdn-glass">
    <h2>Node Identity</h2>
    <dl class="sdn-details">
      <div><dt>Name</dt><dd>{summary?.displayName ?? String(profile?.dn ?? 'Space Data Network')}</dd></div>
      <div><dt>Peer ID</dt><dd>{summary?.peerId ?? String(profile?.peer_id ?? 'pending')}</dd></div>
      <div><dt>Agent</dt><dd>{summary?.agentVersion ?? String(profile?.agent_version ?? 'pending')}</dd></div>
    </dl>
  </article>

  <article class="sdn-card sdn-glass">
    <h2>EPM Scope</h2>
    <dl class="sdn-details">
      <div><dt>Node self</dt><dd>{nodeSelfEpms}</dd></div>
      <div><dt>Hosted</dt><dd>{hostedOnlyEpms}</dd></div>
      <div><dt>Public sharing</dt><dd>Public EPM and public keys only</dd></div>
    </dl>
  </article>

  <article class="sdn-card sdn-glass">
    <h2>Identity Inspector</h2>
    <pre>{JSON.stringify(profile ?? {}, null, 2)}</pre>
  </article>

  <article class="sdn-card sdn-glass">
    <h2>Access & Roles</h2>
    <div class="sdn-capability-list">
      {#each capabilities.slice(0, 8) as capability}
        <span class="sdn-chip" data-state={capability.state === 'available' ? 'online' : 'warning'}>
          {capability.id}: {capability.state}
        </span>
      {/each}
    </div>
  </article>
</section>

<IdentityPanel
  {backend}
  {summary}
  {profile}
  {capabilities}
  {hostedEpms}
  onReload={onHostedEpmsReload}
/>

<button class="sdn-button" type="button" on:click={() => advancedOpen = !advancedOpen}>Advanced</button>
<AdvancedDrawer open={advancedOpen} title="Kubo Diagnostics">
  <p>Low-level Kubo and IPFS controls stay behind this drawer.</p>
</AdvancedDrawer>
