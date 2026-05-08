<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { ObservedSdnPeer, SdnBackend } from '../../../src/ui/runtime/sdn-backend';
  import DirectorySearchPanel from '../components/DirectorySearchPanel.svelte';
  import MetricCard from '../components/cards/MetricCard.svelte';

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let hostedEpms: HostedEpmRecord[] = [];
  let query = '';
  let peerActionState = 'Peer EPM actions use public EPM and public vCard downloads.';

  $: filteredPeers = peers.filter((peer) => {
    const needle = query.toLowerCase();
    return !needle || peer.name.toLowerCase().includes(needle) || peer.id.toLowerCase().includes(needle);
  });

  function getPeerEpm(peer: ObservedSdnPeer): HostedEpmRecord | null {
    return hostedEpms.find((record) => record.peerId === peer.id || record.id === peer.id) ?? null;
  }

  async function downloadHostedEpm(peer: ObservedSdnPeer, format: 'epm' | 'vcard'): Promise<void> {
    if (!backend) {
      peerActionState = 'Backend unavailable; peer public EPM download is disabled.';
      return;
    }
    const epm = getPeerEpm(peer);
    const id = epm?.id ?? peer.id;
    peerActionState = `Preparing public ${format === 'vcard' ? 'vCard' : 'EPM'} for ${peer.name || peer.id}...`;
    try {
      const result = await backend.downloadHostedEpm(id, format);
      if (!result.ok || !result.data) {
        peerActionState = result.capability.reason ?? 'Public peer EPM download is unavailable.';
        return;
      }
      triggerDownload(result.data.url, result.data.filename);
      peerActionState = `Public ${format === 'vcard' ? 'vCard' : 'EPM'} download started.`;
    } catch (error) {
      peerActionState = error instanceof Error ? error.message : String(error);
    }
  }

  function epmStatus(peer: ObservedSdnPeer): string {
    const epm = getPeerEpm(peer);
    if (epm?.epmCid) return epm.epmCid;
    if (epm) return epm.kind === 'node-self' ? 'node self EPM' : 'hosted EPM';
    return 'lookup available';
  }

  function triggerDownload(url: string, filename: string): void {
    const link = document.createElement('a');
    link.href = url;
    link.download = filename;
    link.rel = 'noreferrer';
    document.body.appendChild(link);
    link.click();
    link.remove();
  }
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
        <tr><th>Name</th><th>Peer ID</th><th>EPM</th><th>Trust</th><th>Agent</th><th>Actions</th></tr>
      </thead>
      <tbody>
        {#each filteredPeers as peer}
          <tr>
            <td>{peer.name}</td>
            <td><code>{peer.id}</code></td>
            <td>{epmStatus(peer)}</td>
            <td>{peer.trustLevel}</td>
            <td>{peer.agentVersion ?? 'unknown'}</td>
            <td>
              <div class="sdn-actions-nowrap">
                <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(peer, 'epm')}>EPM</button>
                <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(peer, 'vcard')}>vCard</button>
              </div>
            </td>
          </tr>
        {:else}
          <tr><td colspan="6">No SDN peers loaded.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="sdn-status-line">{peerActionState}</p>
</article>

<DirectorySearchPanel {backend} />

<section class="sdn-panel-grid">
  <article class="sdn-card sdn-glass"><h2>Marketplace</h2><p>Data feeds, modules, and schemas appear here as backend endpoints graduate from degraded capability state.</p></article>
  <article class="sdn-card sdn-glass"><h2>Modules</h2><p>Install and configure analysis modules from verified providers.</p></article>
  <article class="sdn-card sdn-glass"><h2>Mission Builder</h2><p>Combine peers, feeds, modules, and retention rules into a repeatable mission loadout.</p></article>
</section>
