<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { NodeSummary } from '../../../src/ui/runtime/sdn-backend';
  import { shortPeerId } from '../../../src/ui/runtime/peer-identity';

  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let hostedEpms: HostedEpmRecord[] = [];

  $: nodeName = summary?.displayName ?? publicString(profile, 'dn') ?? 'Space Data Network';
  $: nodePeerId = summary?.peerId ?? publicString(profile, 'peer_id') ?? publicString(profile, 'PeerID') ?? '';
  $: agentVersion = summary?.agentVersion ?? publicString(profile, 'agent_version') ?? '';

  function publicString(record: Record<string, unknown> | null, key: string): string | null {
    const value = record?.[key];
    return typeof value === 'string' && value.trim() ? value.trim() : null;
  }
</script>

<section class="sdn-panel sdn-readable-panel" aria-labelledby="node-identity-heading">
  <header class="sdn-section-header">
    <div>
      <p class="sdn-eyebrow">Read-only node identity</p>
      <h2 id="node-identity-heading">{nodeName}</h2>
    </div>
  </header>

  <dl class="sdn-profile-details">
    <div>
      <dt>Peer ID</dt>
      <dd><code title={nodePeerId || 'Not published'}>{nodePeerId ? shortPeerId(nodePeerId) : 'Not published'}</code></dd>
    </div>
    <div>
      <dt>Agent</dt>
      <dd>{agentVersion || 'Not reported'}</dd>
    </div>
    <div>
      <dt>Node status</dt>
      <dd>{summary?.online ? 'Online' : 'Unavailable'}</dd>
    </div>
  </dl>
</section>

<section class="sdn-panel sdn-readable-panel" aria-labelledby="hosted-epm-heading">
  <header class="sdn-section-header">
    <div>
      <p class="sdn-eyebrow">Hosted EPM status</p>
      <h2 id="hosted-epm-heading">Published identities</h2>
    </div>
    <span class="sdn-chip">{hostedEpms.length}</span>
  </header>

  {#if hostedEpms.length === 0}
    <p class="sdn-status-line">No hosted EPM records reported by this node.</p>
  {:else}
    <ul class="sdn-identity-list" aria-label="Hosted EPM records">
      {#each hostedEpms as record (record.id)}
        <li class="sdn-identity-row">
          <span class="sdn-identity-row-copy">
            <strong>{record.label || record.id}</strong>
            <span title={record.peerId || 'Not published'}>{record.peerId ? shortPeerId(record.peerId) : 'Peer ID not published'}</span>
            {#if record.epmCid}
              <small title={record.epmCid}>EPM {shortPeerId(record.epmCid)}</small>
            {:else}
              <small>EPM CID not published</small>
            {/if}
          </span>
        </li>
      {/each}
    </ul>
  {/if}
</section>
