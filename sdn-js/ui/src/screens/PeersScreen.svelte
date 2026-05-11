<script lang="ts">
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { ObservedSdnPeer, SdnBackend } from '../../../src/ui/runtime/sdn-backend';
  import DirectorySearchPanel from '../components/DirectorySearchPanel.svelte';
  import MetricCard from '../components/cards/MetricCard.svelte';

  type PeerSortColumn = 'name' | 'peerId' | 'trust' | 'ip' | 'agent';
  type SortDirection = 'asc' | 'desc';

  type IdentityRuntimeModule = {
    createVCardQrPayload: (input: Record<string, unknown> | HostedEpmRecord) => string;
  };

  type QrCodeModule = {
    toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
  };

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let hostedEpms: HostedEpmRecord[] = [];

  const identityRuntimeModules = import.meta.glob('../../../src/ui/runtime/identity.ts');
  let query = '';
  let sortColumn: PeerSortColumn = 'name';
  let sortDirection: SortDirection = 'asc';
  let expandedPeerId = '';
  let peerQrDataUrl = '';
  let peerQrState = '';
  let peerQrKey = '';
  let identityRuntimePromise: Promise<IdentityRuntimeModule> | null = null;
  let qrCodeModulePromise: Promise<QrCodeModule> | null = null;

  $: filteredPeers = peers.filter(peerMatchesQuery);
  $: visiblePeers = sortPeers(filteredPeers, sortColumn, sortDirection);
  $: expandedPeer = expandedPeerId ? peers.find((peer) => peer.id === expandedPeerId) ?? null : null;
  $: void renderPeerQr(expandedPeer);

  function getPeerEpm(peer: ObservedSdnPeer): HostedEpmRecord | null {
    return hostedEpms.find((record) => record.peerId === peer.id || record.id === peer.id) ?? null;
  }

  function setSort(column: PeerSortColumn): void {
    if (sortColumn === column) {
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
      return;
    }
    sortColumn = column;
    sortDirection = 'asc';
  }

  function sortablePeerHeader(column: PeerSortColumn, label: string): string {
    if (sortColumn !== column) return label;
    return `${label} ${sortDirection.toUpperCase()}`;
  }

  function peerMatchesQuery(peer: ObservedSdnPeer): boolean {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;
    return [
      displayNameForPeer(peer),
      peer.id,
      peer.trustLevel,
      peerIp(peer),
      peer.agentVersion ?? '',
      peerEmail(peer),
      peerPhone(peer),
      getPeerEpm(peer)?.epmCid ?? '',
    ].some((value) => value.toLowerCase().includes(needle));
  }

  function sortPeers(items: ObservedSdnPeer[], column: PeerSortColumn, direction: SortDirection): ObservedSdnPeer[] {
    const multiplier = direction === 'asc' ? 1 : -1;
    return [...items].sort((left, right) => peerSortValue(left, column).localeCompare(peerSortValue(right, column)) * multiplier);
  }

  function peerSortValue(peer: ObservedSdnPeer, column: PeerSortColumn): string {
    if (column === 'name') return displayNameForPeer(peer);
    if (column === 'peerId') return peer.id;
    if (column === 'trust') return peer.trustLevel;
    if (column === 'ip') return peerIp(peer);
    return peer.agentVersion ?? '';
  }

  function togglePeer(peer: ObservedSdnPeer): void {
    expandedPeerId = expandedPeerId === peer.id ? '' : peer.id;
  }

  function handlePeerRowKeydown(event: KeyboardEvent, peer: ObservedSdnPeer): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    togglePeer(peer);
  }

  function displayNameForPeer(peer: ObservedSdnPeer): string {
    const epm = getPeerEpm(peer)?.epmJson ?? {};
    return stringValue(epm.dn)
      ?? stringValue(epm.DN)
      ?? stringValue(epm.displayName)
      ?? stringValue(epm.legal_name)
      ?? stringValue(epm.name)
      ?? stringValue(peer.name)
      ?? peer.id;
  }

  function peerEmail(peer: ObservedSdnPeer): string {
    const epm = getPeerEpm(peer)?.epmJson ?? {};
    return stringValue(epm.email) ?? stringValue(epm.EMAIL) ?? '';
  }

  function peerPhone(peer: ObservedSdnPeer): string {
    const epm = getPeerEpm(peer)?.epmJson ?? {};
    return stringValue(epm.telephone) ?? stringValue(epm.phone) ?? stringValue(epm.TELEPHONE) ?? '';
  }

  function peerIp(peer: ObservedSdnPeer): string {
    for (const addr of peer.addrs ?? []) {
      const ip = addr.match(/\/ip[46]\/([^/]+)/)?.[1];
      if (ip) return ip;
      const dns = addr.match(/\/dns(?:4|6)?\/([^/]+)/)?.[1];
      if (dns) return dns;
    }
    return '';
  }

  function peerEpmSummary(peer: ObservedSdnPeer): Array<{ label: string; value: string }> {
    const epm = getPeerEpm(peer);
    const epmJson = epm?.epmJson ?? {};
    return [
      { label: 'Display name', value: displayNameForPeer(peer) },
      { label: 'Email', value: peerEmail(peer) },
      { label: 'Phone', value: peerPhone(peer) },
      { label: 'PeerID', value: peer.id },
      { label: 'EPM CID', value: epm?.epmCid ?? stringValue(epmJson.epm_cid) ?? stringValue(epmJson.epmCid) ?? '' },
      { label: 'Public key', value: publicKeyValue(epmJson) ?? '' },
      { label: 'Signing public key', value: stringValue(epmJson.signing_public_key) ?? stringValue(epmJson.signingPublicKey) ?? '' },
      { label: 'Encryption public key', value: stringValue(epmJson.encryption_public_key) ?? stringValue(epmJson.encryptionPublicKey) ?? '' },
    ];
  }

  async function renderPeerQr(peer: ObservedSdnPeer | null): Promise<void> {
    const key = peer ? JSON.stringify([peer.id, getPeerEpm(peer)?.id, getPeerEpm(peer)?.updatedAt, getPeerEpm(peer)?.epmCid]) : '';
    if (key === peerQrKey) return;
    peerQrKey = key;
    peerQrDataUrl = '';
    if (!peer) {
      peerQrState = '';
      return;
    }
    peerQrState = 'Rendering QR...';
    try {
      const [payload, qrCode] = await Promise.all([
        createVCardQrPayloadFromRuntime(toHostedRecord(peer)),
        loadQrCodeModule(),
      ]);
      const dataUrl = await qrCode.toDataURL(payload, {
        color: { dark: '#f5f5f7', light: '#00000000' },
        errorCorrectionLevel: 'M',
        margin: 1,
        width: 220,
      });
      if (peerQrKey === key) {
        peerQrDataUrl = dataUrl;
        peerQrState = '';
      }
    } catch (error) {
      if (peerQrKey === key) {
        peerQrState = `QR unavailable: ${errorMessage(error)}`;
      }
    }
  }

  async function loadQrCodeModule(): Promise<QrCodeModule> {
    qrCodeModulePromise ??= importQrCodeModule();
    return qrCodeModulePromise;
  }

  async function importQrCodeModule(): Promise<QrCodeModule> {
    // @ts-expect-error qrcode does not ship TypeScript declarations in this package.
    const module = await import('qrcode');
    return (module.default ?? module) as QrCodeModule;
  }

  async function createVCardQrPayloadFromRuntime(record: HostedEpmRecord): Promise<string> {
    try {
      const runtime = await loadIdentityRuntime();
      return runtime.createVCardQrPayload(record);
    } catch {
      return createVCardQrPayloadLocal(record);
    }
  }

  async function loadIdentityRuntime(): Promise<IdentityRuntimeModule> {
    identityRuntimePromise ??= loadIdentityRuntimeModule();
    return identityRuntimePromise;
  }

  async function loadIdentityRuntimeModule(): Promise<IdentityRuntimeModule> {
    const load = identityRuntimeModules['../../../src/ui/runtime/identity.ts'];
    if (!load) return { createVCardQrPayload: createVCardQrPayloadLocal };
    const module = await load();
    const runtime = module as IdentityRuntimeModule;
    return {
      createVCardQrPayload: runtime.createVCardQrPayload ?? createVCardQrPayloadLocal,
    };
  }

  function toHostedRecord(peer: ObservedSdnPeer): HostedEpmRecord {
    const epm = getPeerEpm(peer);
    if (epm) return epm;
    return {
      id: peer.id,
      kind: 'hosted',
      label: displayNameForPeer(peer),
      peerId: peer.id,
      epmJson: {
        dn: displayNameForPeer(peer),
        peer_id: peer.id,
        agent_version: peer.agentVersion ?? '',
      },
    };
  }

  function createVCardQrPayloadLocal(input: Record<string, unknown> | HostedEpmRecord): string {
    const record = isHostedEpmRecord(input) ? input : {
      id: stringValue(input.id) ?? stringValue(input.peer_id) ?? 'peer',
      kind: 'hosted' as const,
      label: stringValue(input.dn) ?? stringValue(input.name) ?? 'Peer',
      peerId: stringValue(input.peer_id) ?? '',
      epmJson: input,
    };
    const epm = record.epmJson;
    const publicKey = publicKeyValue(epm);
    const lines = ['BEGIN:VCARD', 'VERSION:3.0'];
    addVCardLine(lines, 'FN', stringValue(epm.dn) ?? stringValue(epm.DN) ?? record.label);
    addVCardLine(lines, 'TEL', stringValue(epm.telephone) ?? stringValue(epm.phone));
    addVCardLine(lines, 'X-SDN-PEER-ID', record.peerId);
    addVCardLine(lines, 'X-SDN-EPM-CID', record.epmCid ?? stringValue(epm.epm_cid) ?? stringValue(epm.epmCid));
    addVCardLine(lines, 'EMAIL;TYPE=INTERNET', publicKeyEmailAddress(publicKey));
    addVCardLine(lines, 'X-SDN-PUBLIC-KEY', publicKey);
    lines.push('END:VCARD');
    return lines.join('\r\n');
  }

  function addVCardLine(lines: string[], key: string, value: string | undefined): void {
    if (value?.trim()) lines.push(`${key}:${value.replace(/\r?\n/g, ' ')}`);
  }

  function publicKeyValue(epm: Record<string, unknown>): string | undefined {
    return stringValue(epm.public_key)
      ?? stringValue(epm.PUBLIC_KEY)
      ?? stringValue(epm.publicKey)
      ?? stringValue(epm.signing_public_key)
      ?? stringValue(epm.signingPublicKey)
      ?? stringValue(epm.signing_pubkey_hex)
      ?? stringValue(epm.encryption_public_key)
      ?? stringValue(epm.encryptionPublicKey)
      ?? stringValue(epm.encryption_pubkey_hex);
  }

  function publicKeyEmailAddress(publicKey: string | undefined): string | undefined {
    const localPart = publicKey?.trim().replace(/\s+/g, '').replace(/[^A-Za-z0-9._%+-]/g, '');
    return localPart ? `${localPart}@spacedatanetwork.org` : undefined;
  }

  function isHostedEpmRecord(value: Record<string, unknown> | HostedEpmRecord): value is HostedEpmRecord {
    return typeof value.id === 'string'
      && (value.kind === 'node-self' || value.kind === 'hosted')
      && typeof value.label === 'string'
      && typeof value.peerId === 'string'
      && typeof value.epmJson === 'object'
      && value.epmJson !== null;
  }

  function stringValue(value: unknown): string | undefined {
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
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
        <tr>
          <th><button type="button" on:click={() => setSort('name')}>{sortablePeerHeader('name', 'Name')}</button></th>
          <th><button type="button" on:click={() => setSort('peerId')}>{sortablePeerHeader('peerId', 'PeerID')}</button></th>
          <th><button type="button" on:click={() => setSort('trust')}>{sortablePeerHeader('trust', 'Trust')}</button></th>
          <th><button type="button" on:click={() => setSort('ip')}>{sortablePeerHeader('ip', 'IP')}</button></th>
          <th><button type="button" on:click={() => setSort('agent')}>{sortablePeerHeader('agent', 'Agent')}</button></th>
        </tr>
      </thead>
      <tbody>
        {#each visiblePeers as peer}
          <tr
            class:active={expandedPeerId === peer.id}
            role="button"
            tabindex="0"
            aria-expanded={expandedPeerId === peer.id}
            on:click={() => togglePeer(peer)}
            on:keydown={(event) => handlePeerRowKeydown(event, peer)}
          >
            <td>{displayNameForPeer(peer)}</td>
            <td><code>{peer.id}</code></td>
            <td>{peer.trustLevel}</td>
            <td>{peerIp(peer)}</td>
            <td>{peer.agentVersion ?? ''}</td>
          </tr>
          {#if expandedPeerId === peer.id}
            <tr class="sdn-peer-expanded-row">
              <td colspan="5">
                <section class="sdn-peer-expanded">
                  <div class="sdn-qr-frame" aria-label="Public peer vCard QR">
                    {#if peerQrDataUrl}
                      <img src={peerQrDataUrl} alt="Public vCard QR code" />
                    {/if}
                  </div>
                  <div>
                    <h3>EPM Fields</h3>
                    {#if peerQrState}
                      <p class="sdn-status-line">{peerQrState}</p>
                    {/if}
                    <dl class="sdn-profile-details sdn-peer-fields">
                      {#each peerEpmSummary(peer) as field}
                        <div>
                          <dt>{field.label}</dt>
                          <dd>{field.value}</dd>
                        </div>
                      {/each}
                    </dl>
                  </div>
                </section>
              </td>
            </tr>
          {/if}
        {:else}
          <tr><td colspan="5">No SDN peers loaded.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</article>

<DirectorySearchPanel {backend} />

<section class="sdn-panel-grid">
  <article class="sdn-card sdn-glass"><h2>Marketplace</h2><p>Data feeds, modules, and schemas appear here as backend endpoints graduate from degraded capability state.</p></article>
  <article class="sdn-card sdn-glass"><h2>Modules</h2><p>Install and configure analysis modules from verified providers.</p></article>
  <article class="sdn-card sdn-glass"><h2>Mission Builder</h2><p>Combine peers, feeds, modules, and retention rules into a repeatable mission loadout.</p></article>
</section>
