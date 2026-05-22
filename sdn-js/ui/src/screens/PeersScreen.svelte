<script lang="ts">
  import { onMount } from 'svelte';
  import {
    DEFAULT_OWNERTRUST,
    PGP_OWNERTRUST_LEVELS,
    isTrustedDirectoryOwnertrust,
    loadDataDirectoryState,
    persistDataDirectoryState,
    subscriptionKey,
    upsertDataFeedSubscription,
    updatePeerOwnertrust,
    type DataDirectoryState,
    type PgpOwnertrust,
  } from '../../../src/ui/runtime/data-directory';
  import {
    buildPeerDataFeeds,
    dataSummaryListingsForSource,
    type PeerDataFeed,
  } from '../../../src/ui/runtime/peer-data-feeds';
  import {
    createDefaultLibp2pFlatSqlSyncClient,
    createLibp2pFlatSqlSyncBackend,
  } from '../../../src/ui/runtime/sdn-backend-libp2p-sync';
  import {
    hostedEpmRecordFromDirectoryRecord,
    peerDisplayName,
    peerEmail as peerIdentityEmail,
    peerEpmCid,
    peerEpmJson,
    peerHostedEpmRecord,
    peerPhone as peerIdentityPhone,
  } from '../../../src/ui/runtime/peer-identity';
  import {
    createVCardQrPayload as createVCardQrPayloadLocal,
    identityPublicKeyValue,
  } from '../../../src/ui/runtime/identity-vcard';
  import type { HostedEpmRecord } from '../../../src/ui/runtime/identity';
  import type { ObservedSdnPeer, SdnBackend } from '../../../src/ui/runtime/sdn-backend';
  import DirectorySearchPanel from '../components/DirectorySearchPanel.svelte';

  type PeerSortColumn = 'name' | 'peerId' | 'trust' | 'ip' | 'agent';
  type SortDirection = 'asc' | 'desc';
  type PeerView = 'home' | 'observed' | 'feeds' | 'peer-detail';

  type IdentityRuntimeModule = {
    createVCardQrPayload: (input: Record<string, unknown> | HostedEpmRecord) => string;
  };

  type QrCodeModule = {
    toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
  };

  interface ConfiguredSdnNode {
    id: string;
    name: string;
    addrs: string[];
    trust_level?: string;
    trustLevel?: string;
    metadata?: Record<string, unknown>;
  }

  interface DataSourceOption {
    id: string;
    label: string;
    detail: string;
    peerId: string;
    publicKey: string | null;
    syncAddrs: string[];
    searchText: string;
  }

  interface StorefrontModule {
    id: string;
    name: string;
    peerId: string;
    providerName: string;
    version: string;
    status: string;
  }

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let hostedEpms: HostedEpmRecord[] = [];

  const identityRuntimeModules = import.meta.glob('../../../src/ui/runtime/identity.ts');
  const PUBLIC_DIRECTORY_BASE_URL = 'https://sdn.spaceaware.io';
  const DEFAULT_SUBSCRIPTION_STORAGE_CAP = 1;
  const DEFAULT_SUBSCRIPTION_STORAGE_UNIT = 'GB';

  let peerView: PeerView = 'home';
  let query = '';
  let sortColumn: PeerSortColumn = 'name';
  let sortDirection: SortDirection = 'asc';
  let selectedPeerId = '';
  let peerQrDataUrl = '';
  let peerQrState = '';
  let peerQrKey = '';
  let configuredDataSources: ConfiguredSdnNode[] = [];
  let dataDirectoryState: DataDirectoryState = loadDataDirectoryState();
  let storefrontListings: Array<Record<string, unknown>> = [];
  let directoryPeerEpms: HostedEpmRecord[] = [];
  let directoryPeerEpmsKey = '';
  let feedStatus = '';
  let directoryStatus = '';
  let identityRuntimePromise: Promise<IdentityRuntimeModule> | null = null;
  let qrCodeModulePromise: Promise<QrCodeModule> | null = null;

  $: dataSourceOptions = buildDataSourceOptions(configuredDataSources, peers);
  $: peerDataFeeds = buildPeerDataFeeds(dataSourceOptions, storefrontListings, dataDirectoryState);
  $: storefrontModules = buildStorefrontModules(storefrontListings);
  $: peerIdentityVersion = directoryPeerEpms.map((record) => `${record.peerId}:${record.epmCid ?? ''}:${record.updatedAt ?? ''}`).join('|');
  $: trustedPeers = peers.filter((peer) => isTrustedDirectoryOwnertrust(ownertrustForPeer(peer.id)));
  $: filteredPeers = filterPeersForQuery(peers, peerIdentityVersion);
  $: visiblePeers = sortPeers(filteredPeers, sortColumn, sortDirection);
  $: selectedPeer = selectedPeerId ? peers.find((peer) => peer.id === selectedPeerId) ?? null : null;
  $: selectedPeerFeeds = selectedPeer ? peerDataFeeds.filter((feed) => feed.peerId === selectedPeer.id) : [];
  $: selectedPeerModules = selectedPeer ? storefrontModules.filter((module) => module.peerId === selectedPeer.id) : [];
  $: selectedPeerSummary = selectedPeerSummaryFor(selectedPeer, peerIdentityVersion);
  $: void loadDirectoryPeerEpmsForPeers(peers, backend);
  $: void renderPeerQr(selectedPeer, peerIdentityVersion);

  onMount(() => {
    dataDirectoryState = loadDataDirectoryState();
    void loadPeerStorefrontSources();
  });

  async function loadPeerStorefrontSources(): Promise<void> {
    await loadConfiguredDataSources();
    await loadStorefrontListings();
  }

  async function loadConfiguredDataSources(): Promise<void> {
    if (typeof fetch !== 'function') {
      configuredDataSources = [];
      return;
    }
    try {
      const response = await fetch('/api/local/sdn-nodes', {
        headers: { accept: 'application/json' },
      });
      configuredDataSources = response.ok ? normalizeConfiguredDataSources(await response.json()) : [];
    } catch {
      configuredDataSources = [];
    }
  }

  async function loadStorefrontListings(): Promise<void> {
    const listings: Array<Record<string, unknown>> = [];
    let status = '';
    if (!backend) {
      storefrontListings = await loadConfiguredDataFeedListings(buildDataSourceOptions(configuredDataSources, peers));
      return;
    }
    try {
      const result = await backend.searchListings('');
      listings.push(...(result.data ?? []));
      status = result.ok ? '' : result.capability.reason ?? '';
    } catch (error) {
      status = errorMessage(error);
    }
    const summaryListings = await loadConfiguredDataFeedListings(buildDataSourceOptions(configuredDataSources, peers));
    storefrontListings = [...listings, ...summaryListings];
    directoryStatus = storefrontListings.length > 0 ? '' : status;
  }

  async function loadConfiguredDataFeedListings(sources: DataSourceOption[]): Promise<Array<Record<string, unknown>>> {
    const listings: Array<Record<string, unknown>> = [];
    for (const source of sources) {
      if (!source.syncAddrs.length) continue;
      let client: Awaited<ReturnType<typeof createDefaultLibp2pFlatSqlSyncClient>> | null = null;
      try {
        client = await createDefaultLibp2pFlatSqlSyncClient(source.syncAddrs);
        const syncBackend = createLibp2pFlatSqlSyncBackend({
          targetPeerId: source.peerId,
          candidateAddrs: source.syncAddrs,
          providerId: configuredProviderIdFromSource(source),
          displayName: source.label,
          publicKey: source.publicKey,
          syncClient: client,
        });
        const result = await syncBackend.getDataSummary();
        if (result.data) listings.push(...dataSummaryListingsForSource(source, result.data));
      } catch {
        // Discovery stays on libp2p; unavailable peers simply do not publish feed rows yet.
      } finally {
        await client?.stop?.();
      }
    }
    return listings;
  }

  function setPeerView(view: PeerView): void {
    peerView = view;
    if (view !== 'peer-detail') selectedPeerId = '';
    feedStatus = '';
  }

  function showPeerDetail(peer: ObservedSdnPeer): void {
    selectedPeerId = peer.id;
    peerView = 'peer-detail';
  }

  function getPeerEpm(peer: ObservedSdnPeer): HostedEpmRecord | null {
    return hostedEpms.find((record) => record.peerId === peer.id || record.id === peer.id)
      ?? directoryPeerEpms.find((record) => record.peerId === peer.id || record.id === peer.id)
      ?? null;
  }

  async function loadDirectoryPeerEpmsForPeers(observedPeers: ObservedSdnPeer[], activeBackend: SdnBackend | null): Promise<void> {
    const peerIds = Array.from(new Set(observedPeers.map((peer) => peer.id).filter(Boolean))).sort();
    const key = `${activeBackend?.mode ?? 'none'}:${peerIds.join(',')}`;
    if (directoryPeerEpmsKey === key) return;
    directoryPeerEpmsKey = key;
    if (peerIds.length === 0) {
      directoryPeerEpms = [];
      return;
    }

    const records = (await Promise.all(observedPeers.map(async (peer) => {
      const directoryRecords: Array<Record<string, unknown>> = [];
      if (activeBackend) {
        try {
          const result = await activeBackend.searchDirectory(peer.id);
          directoryRecords.push(...directoryRecordsFromPayload(result.data));
        } catch {
          // Public directory fallback below keeps peer identity discovery non-blocking.
        }
      }
      directoryRecords.push(...await loadPublicDirectoryPeerRecords(peer.id));
      return directoryRecords
        .map(hostedEpmRecordFromDirectoryRecord)
        .filter((record): record is HostedEpmRecord => record !== null && record.peerId === peer.id);
    }))).flat();

    if (directoryPeerEpmsKey === key) {
      directoryPeerEpms = dedupePeerEpmRecords(records);
    }
  }

  async function loadPublicDirectoryPeerRecords(peerId: string): Promise<Array<Record<string, unknown>>> {
    if (typeof fetch !== 'function') return [];
    try {
      const response = await fetch(`${PUBLIC_DIRECTORY_BASE_URL}/api/directory/nodes?q=${encodeURIComponent(peerId)}`, {
        headers: { accept: 'application/json' },
      });
      return response.ok ? directoryRecordsFromPayload(await response.json()) : [];
    } catch {
      return [];
    }
  }

  function ownertrustForPeer(peerId: string): PgpOwnertrust {
    return dataDirectoryState.peerTrust[peerId] ?? DEFAULT_OWNERTRUST;
  }

  function handleOwnertrustChange(peer: ObservedSdnPeer, event: Event): void {
    const value = (event.currentTarget as HTMLSelectElement).value as PgpOwnertrust;
    dataDirectoryState = updatePeerOwnertrust(dataDirectoryState, peer.id, value);
    persistDataDirectoryState(dataDirectoryState);
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
      ownertrustForPeer(peer.id),
      peerIp(peer),
      peer.agentVersion ?? '',
      peerEmail(peer),
      peerPhone(peer),
      peerEpmCid(peer, getPeerEpm(peer)) ?? '',
    ].some((value) => value.toLowerCase().includes(needle));
  }

  function filterPeersForQuery(items: ObservedSdnPeer[], _identityVersion: string): ObservedSdnPeer[] {
    return items.filter(peerMatchesQuery);
  }

  function selectedPeerSummaryFor(peer: ObservedSdnPeer | null, _identityVersion: string): Array<{ label: string; value: string }> {
    return peer ? peerEpmSummary(peer) : [];
  }

  function sortPeers(items: ObservedSdnPeer[], column: PeerSortColumn, direction: SortDirection): ObservedSdnPeer[] {
    const multiplier = direction === 'asc' ? 1 : -1;
    return [...items].sort((left, right) => peerSortValue(left, column).localeCompare(peerSortValue(right, column)) * multiplier);
  }

  function peerSortValue(peer: ObservedSdnPeer, column: PeerSortColumn): string {
    if (column === 'name') return displayNameForPeer(peer);
    if (column === 'peerId') return peer.id;
    if (column === 'trust') return ownertrustForPeer(peer.id);
    if (column === 'ip') return peerIp(peer);
    return peer.agentVersion ?? '';
  }

  function handlePeerRowKeydown(event: KeyboardEvent, peer: ObservedSdnPeer): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    showPeerDetail(peer);
  }

  function subscribeToDataFeed(feed: PeerDataFeed): void {
    dataDirectoryState = upsertDataFeedSubscription(dataDirectoryState, {
      dataSourceId: feed.dataSourceId,
      peerId: feed.peerId,
      datastoreKey: feed.datastoreKey,
      standardId: feed.standardId,
      providerName: feed.providerName,
      providerId: feed.providerId,
      providerPublicKey: feed.providerPublicKey,
      sourceName: feed.sourceName,
      remoteRows: feed.remoteRows,
      storageCap: DEFAULT_SUBSCRIPTION_STORAGE_CAP,
      storageUnit: DEFAULT_SUBSCRIPTION_STORAGE_UNIT,
      syncFilter: '',
    });
    persistDataDirectoryState(dataDirectoryState);
    feedStatus = `Subscribed ${feed.providerName} ${feed.standardId}; ownertrust is ${ownertrustForPeer(feed.peerId)}.`;
  }

  function isFeedSubscribed(feed: PeerDataFeed): boolean {
    const key = subscriptionKey(feed.dataSourceId, feed.standardId, feed.datastoreKey);
    return dataDirectoryState.subscriptions.some((subscription) => subscription.id === key);
  }

  function buildStorefrontModules(listings: Array<Record<string, unknown>>): StorefrontModule[] {
    return listings
      .filter((listing) => listingKind(listing) === 'module')
      .map((listing): StorefrontModule | null => {
        const peerId = listingPeerId(listing);
        if (!peerId) return null;
        return {
          id: stringValue(listing.id) ?? stringValue(listing.pluginId) ?? `${peerId}:module`,
          name: stringValue(listing.name) ?? stringValue(listing.title) ?? stringValue(listing.pluginId) ?? 'Module',
          peerId,
          providerName: listingProviderName(listing, peerId),
          version: stringValue(listing.version) ?? '',
          status: stringValue(listing.status) ?? '',
        };
      })
      .filter((module): module is StorefrontModule => module !== null);
  }

  function buildDataSourceOptions(configuredNodes: ConfiguredSdnNode[], observedPeers: ObservedSdnPeer[]): DataSourceOption[] {
    const observedNames = new Map(observedPeers.map((peer) => [peer.id, peer.name]));
    const options: DataSourceOption[] = [];
    for (const node of configuredNodes) {
      const peerId = configuredNodePeerId(node);
      const syncAddrs = configuredNodeSyncAddrs(node, peerId);
      if (!peerId || syncAddrs.length === 0) continue;
      const publicKey = configuredNodePublicKey(node) ?? peerId;
      const label = configuredNodeLabel(node, observedNames, peerId);
      const detail = [node.id, configuredNodeHostName(node)].filter(Boolean).join(' / ');
      options.push({
        id: `configured:${node.id}`,
        label,
        detail,
        peerId,
        publicKey,
        syncAddrs,
        searchText: [label, detail, publicKey, peerId, node.trustLevel, node.trust_level, syncAddrs.join(' ')].filter(Boolean).join(' ').toLowerCase(),
      });
    }
    return dedupeDataSourceOptions(options);
  }

  function displayNameForPeer(peer: ObservedSdnPeer): string {
    return peerDisplayName(peer, getPeerEpm(peer));
  }

  function peerEmail(peer: ObservedSdnPeer): string {
    return peerIdentityEmail(peer, getPeerEpm(peer));
  }

  function peerPhone(peer: ObservedSdnPeer): string {
    return peerIdentityPhone(peer, getPeerEpm(peer));
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
    const epmJson = peerEpmJson(peer, epm);
    return [
      { label: 'Display name', value: displayNameForPeer(peer) },
      { label: 'Email', value: peerEmail(peer) },
      { label: 'Phone', value: peerPhone(peer) },
      { label: 'PeerID', value: peer.id },
      { label: 'EPM CID', value: peerEpmCid(peer, epm) ?? '' },
      { label: 'Public key', value: publicKeyValue(epmJson) ?? '' },
      { label: 'Signing public key', value: identityPublicKeyValue(epmJson, 'signing') ?? '' },
      { label: 'Encryption public key', value: identityPublicKeyValue(epmJson, 'encryption') ?? '' },
    ];
  }

  async function renderPeerQr(peer: ObservedSdnPeer | null, identityVersion = ''): Promise<void> {
    const epm = peer ? getPeerEpm(peer) : null;
    const key = peer ? JSON.stringify([peer.id, epm?.id, epm?.updatedAt, peerEpmCid(peer, epm), identityVersion, peerEpmJson(peer, epm)]) : '';
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
    return peerHostedEpmRecord(peer, getPeerEpm(peer));
  }

  function publicKeyValue(epm: Record<string, unknown>): string | undefined {
    return identityPublicKeyValue(epm);
  }

  function normalizeConfiguredDataSources(payload: unknown): ConfiguredSdnNode[] {
    const records = recordsFromPayloadKey(payload, 'nodes');
    return records.map((record): ConfiguredSdnNode | null => {
      const id = readRecordString(record, 'id', 'peer_id', 'peerId');
      if (!id) return null;
      const addrs = Array.isArray(record.addrs) ? record.addrs.filter((entry): entry is string => typeof entry === 'string') : [];
      return {
        id,
        name: readRecordString(record, 'name', 'display_name', 'displayName', 'dn') ?? id,
        addrs,
        trust_level: readRecordString(record, 'trust_level', 'trustLevel') ?? undefined,
        metadata: isRecord(record.metadata) ? record.metadata : {},
      };
    }).filter((record): record is ConfiguredSdnNode => record !== null);
  }

  function configuredNodeSyncAddrs(node: ConfiguredSdnNode, peerId: string | null): string[] {
    if (!peerId) return [];
    return node.addrs
      .map((addr) => addr.trim())
      .filter((addr) => addr.includes('/p2p/') && addr.endsWith(`/p2p/${peerId}`) && /\/tcp\/\d+\/wss?\//.test(addr));
  }

  function configuredNodeHostName(node: ConfiguredSdnNode): string {
    return readRecordString(node.metadata ?? {}, 'host_name', 'hostName') ?? '';
  }

  function configuredNodePeerId(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'peer_id', 'peerId')
      ?? node.addrs.map((addr) => addr.split('/p2p/')[1]).find((value): value is string => Boolean(value))
      ?? null;
  }

  function configuredNodePublicKey(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'public_key', 'publicKey', 'signing_public_key', 'signingPublicKey');
  }

  function configuredNodeLabel(node: ConfiguredSdnNode, observedNames: Map<string, string>, peerId: string | null): string {
    if (node.name && node.name !== node.id && node.name !== peerId) return node.name;
    const observedPeerName = peerId ? observedNames.get(peerId) : null;
    if (observedPeerName && observedPeerName !== peerId) return observedPeerName;
    const observedNodeName = observedNames.get(node.id);
    if (observedNodeName && observedNodeName !== node.id) return observedNodeName;
    return node.name ?? peerId ?? node.id;
  }

  function configuredProviderIdFromSource(source: DataSourceOption): string | null {
    return source.id.startsWith('configured:') ? source.id.slice('configured:'.length) : source.id;
  }

  function dedupeDataSourceOptions(options: DataSourceOption[]): DataSourceOption[] {
    const seen = new Set<string>();
    return options.filter((source) => {
      if (seen.has(source.id)) return false;
      seen.add(source.id);
      return true;
    });
  }

  function listingKind(listing: Record<string, unknown>): 'data' | 'module' {
    const kind = stringValue(listing.listingKind) ?? stringValue(listing.kind) ?? stringValue(listing.type);
    return kind === 'data' || kind === 'data_stream' || kind === 'dataset' ? 'data' : 'module';
  }

  function listingPeerId(listing: Record<string, unknown>): string | null {
    return stringValue(listing.peerId)
      ?? stringValue(listing.providerPeerId)
      ?? stringValue(listing.providerId)
      ?? stringValue(listing.authorPeerId)
      ?? null;
  }

  function listingProviderName(listing: Record<string, unknown>, fallback: string): string {
    return stringValue(listing.providerName)
      ?? stringValue(listing.authorName)
      ?? stringValue(listing.sellerName)
      ?? stringValue(listing.sourceName)
      ?? fallback;
  }

  function recordsFromPayloadKey(payload: unknown, key: string): Array<Record<string, unknown>> {
    if (Array.isArray(payload)) return payload.filter(isRecord);
    if (!isRecord(payload)) return [];
    const value = payload[key];
    if (Array.isArray(value)) return value.filter(isRecord);
    return [];
  }

  function directoryRecordsFromPayload(payload: unknown): Array<Record<string, unknown>> {
    if (Array.isArray(payload)) return payload.filter(isRecord);
    if (!isRecord(payload)) return [];
    return [
      ...recordsFromPayloadKey(payload, 'results'),
      ...recordsFromPayloadKey(payload, 'nodes'),
      ...recordsFromPayloadKey(payload, 'users'),
    ];
  }

  function dedupePeerEpmRecords(records: HostedEpmRecord[]): HostedEpmRecord[] {
    const byPeer = new Map<string, HostedEpmRecord>();
    for (const record of records) {
      const key = record.peerId || record.id;
      const current = byPeer.get(key);
      if (!current || (!current.epmCid && record.epmCid)) {
        byPeer.set(key, record);
      }
    }
    return [...byPeer.values()];
  }

  function readRecordString(record: Record<string, unknown>, ...keys: string[]): string | null {
    for (const key of keys) {
      const value = record[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
    }
    return null;
  }

  function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function stringValue(value: unknown): string | undefined {
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }

  function formatNumber(value: number): string {
    return new Intl.NumberFormat('en-US').format(value);
  }

  function shorten(value: string | null | undefined, length = 30): string {
    if (!value) return '';
    if (value.length <= length) return value;
    return `${value.slice(0, Math.max(4, length - 5))}...${value.slice(-4)}`;
  }

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

{#if peerView === 'home'}
  <section class="sdn-storefront-home" aria-label="Peers storefront">
    <div class="sdn-storefront-directory">
      <h2>Directory Search</h2>
      <DirectorySearchPanel {backend} />
    </div>

    <div class="sdn-storefront-stats" aria-label="Peer storefront summary">
      <button class="sdn-storefront-stat" type="button" on:click={() => setPeerView('observed')}>
        <span>Observed Peers</span>
        <strong>{formatNumber(peers.length)}</strong>
      </button>
      <button class="sdn-storefront-stat" type="button" on:click={() => setPeerView('feeds')}>
        <span>Data Feeds</span>
        <strong>{formatNumber(peerDataFeeds.length)}</strong>
      </button>
    </div>
  </section>
{:else if peerView === 'observed'}
  <section class="sdn-peer-workspace" aria-label="Observed peer directory">
    <nav class="sdn-breadcrumbs" aria-label="Peer breadcrumbs">
      <button type="button" on:click={() => setPeerView('home')}>Peers</button>
      <span>/ Directory</span>
    </nav>

    <article class="sdn-card">
      <div class="sdn-card-head">
        <div>
          <h2>Trusted And Observed Peers</h2>
          <p>{formatNumber(trustedPeers.length)} trusted / {formatNumber(peers.length)} observed</p>
        </div>
        <input class="sdn-input" bind:value={query} placeholder="Search peers" aria-label="Search peers" />
      </div>

      <div class="sdn-trust-key" aria-label="Trust key">
        <strong>Trust key</strong>
        <span>unknown: not evaluated</span>
        <span>never: explicitly not trusted</span>
        <span>marginal: limited data source trust</span>
        <span>full: fully trusted</span>
        <span>ultimate: local ultimate ownertrust</span>
      </div>

      <div class="sdn-table-wrap">
        <table class="sdn-table">
          <thead>
            <tr>
              <th><button type="button" on:click={() => setSort('name')}>{sortablePeerHeader('name', 'Name')}</button></th>
              <th><button type="button" on:click={() => setSort('peerId')}>{sortablePeerHeader('peerId', 'PeerID')}</button></th>
              <th><button type="button" on:click={() => setSort('trust')}>{sortablePeerHeader('trust', 'Ownertrust')}</button></th>
              <th><button type="button" on:click={() => setSort('ip')}>{sortablePeerHeader('ip', 'IP')}</button></th>
              <th><button type="button" on:click={() => setSort('agent')}>{sortablePeerHeader('agent', 'Agent')}</button></th>
            </tr>
          </thead>
          <tbody>
            {#each visiblePeers as peer}
              <tr
                role="button"
                tabindex="0"
                on:click={() => showPeerDetail(peer)}
                on:keydown={(event) => handlePeerRowKeydown(event, peer)}
              >
                <td>{displayNameForPeer(peer)}</td>
                <td><code>{peer.id}</code></td>
                <td>
                  <select
                    class="sdn-input sdn-select sdn-ownertrust-select"
                    aria-label={`${displayNameForPeer(peer)} ownertrust`}
                    value={ownertrustForPeer(peer.id)}
                    on:click|stopPropagation
                    on:keydown|stopPropagation
                    on:change={(event) => handleOwnertrustChange(peer, event)}
                  >
                    {#each PGP_OWNERTRUST_LEVELS as level}
                      <option value={level}>{level}</option>
                    {/each}
                  </select>
                </td>
                <td>{peerIp(peer)}</td>
                <td>{peer.agentVersion ?? ''}</td>
              </tr>
            {:else}
              <tr><td colspan="5">No SDN peers loaded.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </article>
  </section>
{:else if peerView === 'feeds'}
  <section class="sdn-peer-workspace" aria-label="Data feeds">
    <nav class="sdn-breadcrumbs" aria-label="Peer breadcrumbs">
      <button type="button" on:click={() => setPeerView('home')}>Peers</button>
      <span>/ Data Feeds</span>
    </nav>

    <article class="sdn-card">
      <div class="sdn-card-head">
        <div>
          <h2>Data Feeds</h2>
          <p>{formatNumber(dataDirectoryState.subscriptions.length)} subscriptions</p>
        </div>
      </div>

      <div class="sdn-table-wrap">
        <table class="sdn-table sdn-feed-table">
          <thead>
            <tr>
              <th>Provider</th>
              <th>Schema</th>
              <th>Remote rows</th>
              <th>Ownertrust</th>
              <th>Subscribe</th>
            </tr>
          </thead>
          <tbody>
            {#each peerDataFeeds as feed}
              <tr>
                <td>
                  <strong>{feed.providerName}</strong>
                  <small>{shorten(feed.providerPublicKey ?? feed.peerId, 42)}</small>
                </td>
                <td>{feed.standardId}</td>
                <td>{formatNumber(feed.remoteRows)}</td>
                <td>{ownertrustForPeer(feed.peerId)}</td>
                <td>
                  <button
                    class="sdn-button sdn-button-muted sdn-button-compact"
                    type="button"
                    disabled={isFeedSubscribed(feed)}
                    on:click={() => subscribeToDataFeed(feed)}
                  >
                    {isFeedSubscribed(feed) ? 'Subscribed' : 'Subscribe'}
                  </button>
                </td>
              </tr>
            {:else}
              <tr><td colspan="5">No published feeds discovered from configured peers.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>

      {#if feedStatus}
        <p class="sdn-empty-inline" role="status">{feedStatus}</p>
      {:else if directoryStatus}
        <p class="sdn-empty-inline" role="status">{directoryStatus}</p>
      {/if}
    </article>
  </section>
{:else if selectedPeer}
  <section class="sdn-peer-workspace" aria-label="Peer details">
    <nav class="sdn-breadcrumbs" aria-label="Peer breadcrumbs">
      <button type="button" on:click={() => setPeerView('observed')}>Directory</button>
      <span>/ {displayNameForPeer(selectedPeer)}</span>
    </nav>

    <article class="sdn-card">
      <div class="sdn-card-head">
        <div>
          <h2>{displayNameForPeer(selectedPeer)}</h2>
          <p>{selectedPeer.id}</p>
        </div>
        <select
          class="sdn-input sdn-select sdn-ownertrust-select"
          aria-label={`${displayNameForPeer(selectedPeer)} ownertrust`}
          value={ownertrustForPeer(selectedPeer.id)}
          on:change={(event) => handleOwnertrustChange(selectedPeer, event)}
        >
          {#each PGP_OWNERTRUST_LEVELS as level}
            <option value={level}>{level}</option>
          {/each}
        </select>
      </div>

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
            {#each selectedPeerSummary as field}
              <div>
                <dt>{field.label}</dt>
                <dd>{field.value}</dd>
              </div>
            {/each}
          </dl>
        </div>
      </section>
    </article>

    <article class="sdn-card">
      <div class="sdn-card-head">
        <h2>Available Data</h2>
      </div>
      <div class="sdn-table-wrap">
        <table class="sdn-table sdn-feed-table">
          <thead>
            <tr>
              <th>Schema</th>
              <th>Remote rows</th>
              <th>Source</th>
              <th>Subscribe</th>
            </tr>
          </thead>
          <tbody>
            {#each selectedPeerFeeds as feed}
              <tr>
                <td>{feed.standardId}</td>
                <td>{formatNumber(feed.remoteRows)}</td>
                <td>{feed.providerName}</td>
                <td>
                  <button
                    class="sdn-button sdn-button-muted sdn-button-compact"
                    type="button"
                    disabled={isFeedSubscribed(feed)}
                    on:click={() => subscribeToDataFeed(feed)}
                  >
                    {isFeedSubscribed(feed) ? 'Subscribed' : 'Subscribe'}
                  </button>
                </td>
              </tr>
            {:else}
              <tr><td colspan="4">No data feeds published for this peer.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if feedStatus}
        <p class="sdn-empty-inline" role="status">{feedStatus}</p>
      {/if}
    </article>

    <article class="sdn-card">
      <div class="sdn-card-head">
        <h2>Available Modules</h2>
      </div>
      <div class="sdn-table-wrap">
        <table class="sdn-table">
          <thead>
            <tr>
              <th>Module</th>
              <th>Version</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {#each selectedPeerModules as module}
              <tr>
                <td>{module.name}</td>
                <td>{module.version}</td>
                <td>{module.status}</td>
              </tr>
            {:else}
              <tr><td colspan="3">No modules published for this peer.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </article>
  </section>
{/if}
