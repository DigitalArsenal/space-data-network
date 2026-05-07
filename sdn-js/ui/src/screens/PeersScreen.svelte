<script lang="ts">
  import type { ObservedSdnPeer } from '../../../src/ui/runtime/sdn-backend';
  import MetricCard from '../components/cards/MetricCard.svelte';

  export let peers: ObservedSdnPeer[] = [];
  let query = '';

  $: filteredPeers = peers.filter((peer) => {
    const needle = query.toLowerCase();
    return !needle || peer.name.toLowerCase().includes(needle) || peer.id.toLowerCase().includes(needle);
  });
</script>

<div class="sdn-grid sdn-grid-3">
  <MetricCard title="Observed Peers" value={peers.length} detail="SDN identify records only" tone="online" />
  <MetricCard title="Data Feeds" value="degraded" detail="Marketplace feed adapter pending" tone="warning" />
  <MetricCard title="Mission Loadout" value="draft" detail="Assemble providers, schemas, and modules" tone="special" />
</div>

<article class="sdn-card">
  <div class="sdn-card-head">
    <h2>Trusted And Observed Peers</h2>
    <input class="sdn-input" bind:value={query} placeholder="Search peers" aria-label="Search peers" />
  </div>
  <div class="sdn-table-wrap">
    <table class="sdn-table">
      <thead>
        <tr><th>Name</th><th>Peer ID</th><th>Trust</th><th>Agent</th></tr>
      </thead>
      <tbody>
        {#each filteredPeers as peer}
          <tr>
            <td>{peer.name}</td>
            <td><code>{peer.id}</code></td>
            <td>{peer.trustLevel}</td>
            <td>{peer.agentVersion ?? 'unknown'}</td>
          </tr>
        {:else}
          <tr><td colspan="4">No SDN peers loaded.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</article>

<section class="sdn-panel-grid">
  <article class="sdn-card"><h2>Marketplace</h2><p>Data feeds, modules, and schemas appear here as backend endpoints graduate from degraded capability state.</p></article>
  <article class="sdn-card"><h2>Modules</h2><p>Install and configure analysis modules from verified providers.</p></article>
  <article class="sdn-card"><h2>Mission Builder</h2><p>Combine peers, feeds, modules, and retention rules into a repeatable mission loadout.</p></article>
</section>
