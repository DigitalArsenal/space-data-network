import {
  FLATSQL_SYNC_PROTOCOL_ID,
  requestFlatSqlSyncChunk,
  requestFlatSqlSyncDatastores,
  requestFlatSqlSyncManifest,
  requestFlatSqlWireSpeedProbe,
  type FlatSqlSyncChunk,
  type FlatSqlSyncDatastoreEntry,
  type FlatSqlSyncDatastoreList,
  type FlatSqlSyncManifest,
  type FlatSqlSyncTransport,
  type FlatSqlSyncQuery,
  type FlatSqlSyncRecordRef,
  type FlatSqlWireSpeedProbeQuery,
  type FlatSqlWireSpeedProbeResult,
} from '../../flatsql-sync';
import {
  createAvailableResult,
  createCapability,
  createCapabilityResult,
  createUnavailableResult,
  type BackendCapability,
  type BackendResult,
  type DataScanResult,
  type DataSummary,
  type LocalObjectSummary,
  type NodeAccessUser,
  type NodeAccessUserInput,
  type NodeSummary,
  type ObservedSdnPeer,
  type RawDataQuery,
  type RawDataRecord,
  type RawDataRecordBytes,
  type RawDataStreamRequest,
  type SdnBackend,
  type StorageSummary,
} from './sdn-backend';

export interface Libp2pFlatSqlSyncClient {
  readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk>;
  listFlatSqlSyncDatastores?(query: Pick<FlatSqlSyncQuery, 'targetPeerId' | 'candidateAddrs'>): Promise<FlatSqlSyncDatastoreList>;
  openFlatSqlSyncManifest?(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest>;
  measureWireSpeed?(query: FlatSqlWireSpeedProbeQuery): Promise<FlatSqlWireSpeedProbeResult>;
  stop?(): Promise<void> | void;
}

export interface Libp2pFlatSqlSyncBackendOptions {
  targetPeerId: string;
  candidateAddrs: string[];
  datastoreKey?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
  displayName?: string | null;
  publicKey?: string | null;
  schemas?: string[];
  syncClient?: Libp2pFlatSqlSyncClient;
  nodeFactory?: () => Promise<Libp2pFlatSqlSyncClient>;
}

const DEFAULT_SUMMARY_SCHEMAS = ['CAT.fbs', 'EPM.fbs', 'MPE.fbs', 'OMM.fbs', 'PNM.fbs', 'SPW.fbs'];

export function createLibp2pFlatSqlSyncBackend(options: Libp2pFlatSqlSyncBackendOptions): SdnBackend {
  const targetPeerId = options.targetPeerId.trim();
  const candidateAddrs = Array.from(new Set(options.candidateAddrs.map((addr) => addr.trim()).filter(Boolean)));
  const providerId = optionalText(options.providerId);
  const sourceName = optionalText(options.sourceName);
  const datastoreKey = optionalText(options.datastoreKey);
  const displayName = optionalText(options.displayName) ?? providerId ?? targetPeerId;
  const summarySchemas = normalizeSummarySchemas(options.schemas);
  let clientPromise: Promise<Libp2pFlatSqlSyncClient> | null = options.syncClient ? Promise.resolve(options.syncClient) : null;

  async function ensureClient(): Promise<Libp2pFlatSqlSyncClient> {
    if (!targetPeerId) throw new Error('remote peer ID is not configured');
    if (candidateAddrs.length === 0) throw new Error('remote libp2p sync address is not configured');
    if (!clientPromise) clientPromise = options.nodeFactory ? options.nodeFactory() : createDefaultLibp2pFlatSqlSyncClient(candidateAddrs);
    return clientPromise;
  }

  async function requestChunk(query: RawDataQuery & {
    op?: FlatSqlSyncQuery['op'];
    scanHash?: string;
    chunkHash?: string;
    nextCursor?: string;
    totalCount?: number;
    highWaterMark?: string;
    records?: RawDataRecord[];
  }): Promise<FlatSqlSyncChunk> {
    const client = await ensureClient();
    return client.readFlatSqlSyncChunk({
      targetPeerId,
      candidateAddrs,
      op: query.op,
      schema: query.schema,
      datastoreKey: query.datastoreKey ?? datastoreKey ?? undefined,
      providerId: query.providerId ?? providerId ?? undefined,
      sourceName: query.sourceName ?? sourceName ?? undefined,
      batchId: query.batchId,
      peerId: query.peerId,
      cursor: query.cursor,
      snapshotId: query.snapshotId,
      head: query.head,
      nextCursor: query.nextCursor,
      totalCount: query.totalCount,
      highWaterMark: query.highWaterMark,
      scanHash: query.scanHash,
      chunkHash: query.chunkHash,
      queryProfile: query.queryProfile,
      syncFilter: query.syncFilter,
      limit: query.limit,
      offset: query.offset,
      records: query.records?.map(rawRecordToFlatSqlRef),
    });
  }

  async function scanRawData(query: RawDataQuery): Promise<BackendResult<DataScanResult>> {
    try {
      const chunk = await requestChunk({ ...query, op: 'scan' });
      return createAvailableResult('scanRawData', dataScanResultFromChunk(chunk));
    } catch (error) {
      return createUnavailableResult('scanRawData', formatSyncError(error));
    }
  }

  async function streamRawData(request: RawDataStreamRequest): Promise<BackendResult<RawDataRecord[]>> {
    try {
      const chunk = await requestChunk({
        schema: request.schema,
        datastoreKey: request.datastoreKey,
        op: 'read_chunk',
        scanHash: request.scanHash,
        chunkHash: request.chunkHash,
        snapshotId: request.snapshotId,
        head: request.head,
        cursor: request.cursor,
        nextCursor: request.nextCursor,
        totalCount: request.totalCount,
        highWaterMark: request.highWaterMark,
        queryProfile: request.queryProfile,
        syncFilter: request.syncFilter,
        records: request.records,
      });
      return createAvailableResult('streamRawData', rawRecordsWithDataFromFlatSqlChunk(chunk, request.records));
    } catch (error) {
      return createUnavailableResult('streamRawData', formatSyncError(error));
    }
  }

  return {
    mode: 'remote-sdn',
    connect: getNodeSummary,
    async getCapabilities(): Promise<BackendCapability[]> {
      return [
        createCapability(
          'flatSqlSync',
          targetPeerId && candidateAddrs.length > 0 ? 'available' : 'unavailable',
          targetPeerId && candidateAddrs.length > 0 ? undefined : 'remote peer ID and libp2p sync address are required',
        ),
        createCapability('transport:libp2p', 'available'),
        createCapability('transport:http', 'unavailable', 'configured remote data does not support HTTP fallback'),
        createCapability('transport:ssh', 'unavailable', 'configured remote data does not support SSH fallback'),
      ];
    },
    getNodeSummary,
    async getHealth() {
      try {
        await requestChunk({ schema: summarySchemas[0] ?? 'OMM.fbs', op: 'scan', limit: 1, offset: 0 });
        return createAvailableResult('getHealth', { healthy: true, details: { protocol: FLATSQL_SYNC_PROTOCOL_ID, peerId: targetPeerId } });
      } catch (error) {
        return createUnavailableResult('getHealth', formatSyncError(error));
      }
    },
    async getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('getNodeProfile', 'remote-only', 'configured remote node profile is advertised through its EPM and peer metadata');
    },
    async saveNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('saveNodeProfile', 'local-only', 'configured remote node profile edits are not available from the data explorer');
    },
    async listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listWalletsAndEpms', 'local-only', 'wallet and EPM management must run on a local node', []);
    },
    async beginClaimEpm(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('beginClaimEpm', 'local-only', 'EPM claim must run on a local node');
    },
    async exportCore(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('exportCore', 'local-only', 'Core export must run on a local node');
    },
    async importCore(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('importCore', 'local-only', 'Core import must run on a local node');
    },
    async listNodeAccessUsers(): Promise<BackendResult<NodeAccessUser[]>> {
      return createCapabilityResult('listNodeAccessUsers', 'permission-required', 'node access users require an authenticated server session', []);
    },
    async saveNodeAccessUser(_user: NodeAccessUserInput): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('saveNodeAccessUser', 'permission-required', 'node access changes require an authenticated server session');
    },
    async revokeNodeAdmin(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('revokeNodeAdmin', 'permission-required', 'node access changes require an authenticated server session');
    },
    async deleteNodeAccessUser(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('deleteNodeAccessUser', 'permission-required', 'node access changes require an authenticated server session');
    },
    async listHostedEpms() {
      return createCapabilityResult('listHostedEpms', 'remote-only', 'configured remote EPM listing is not part of FlatSQL sync', []);
    },
    async saveHostedEpm(record) {
      return createCapabilityResult('saveHostedEpm', 'local-only', `editing hosted EPM ${record.id} must run on a local node`);
    },
    async importHostedEpm(input) {
      return createCapabilityResult('importHostedEpm', 'local-only', `importing hosted EPM ${input.name} must run on a local node`);
    },
    async deleteHostedEpm(id) {
      return createCapabilityResult('deleteHostedEpm', 'local-only', `deleting hosted EPM ${id} must run on a local node`);
    },
    async downloadHostedEpm(id, format) {
      return createCapabilityResult('downloadHostedEpm', 'remote-only', `download for ${id}.${format} is not part of FlatSQL sync`);
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      return createAvailableResult('listObservedPeers', [{
        id: targetPeerId,
        name: displayName,
        addrs: candidateAddrs,
        trustLevel: 'configured',
        agentVersion: 'sdn-configured-node',
        protocols: [FLATSQL_SYNC_PROTOCOL_ID],
      }]);
    },
    async listTrustedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      return this.listObservedPeers();
    },
    async searchDirectory(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('searchDirectory', 'remote-only', 'directory search is not part of FlatSQL sync', []);
    },
    async connectPeer(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('connectPeer', 'remote-only', 'configured remote connection uses its advertised libp2p sync address');
    },
    async searchListings(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('searchListings', 'remote-only', 'marketplace listings are not part of FlatSQL sync', []);
    },
    async listOwnedItems(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listOwnedItems', 'permission-required', 'owned item lookup requires an authenticated session', []);
    },
    async requestGrant(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('requestGrant', 'permission-required', 'grant request requires an authenticated session');
    },
    async installModule(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('installModule', 'local-only', 'module installation must run on a local node');
    },
    async subscribeDataFeed(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('subscribeDataFeed', 'local-only', 'FlatSQL sync subscriptions are managed locally');
    },
    async getStorageSummary(): Promise<BackendResult<StorageSummary>> {
      return createCapabilityResult('getStorageSummary', 'local-only', 'remote storage details are not part of FlatSQL sync');
    },
    async listObjects(): Promise<BackendResult<LocalObjectSummary[]>> {
      return createCapabilityResult('listObjects', 'local-only', 'object inventory is local to the desktop FlatSQL store', []);
    },
    async inspectObject(): Promise<BackendResult<LocalObjectSummary | Record<string, unknown>>> {
      return createCapabilityResult('inspectObject', 'local-only', 'object inspection is local to the desktop FlatSQL store');
    },
    async getDataSummary(): Promise<BackendResult<DataSummary>> {
      try {
        const datastoreSummary = await dataSummaryFromDatastores().catch(() => null);
        if (datastoreSummary) return createAvailableResult('getDataSummary', datastoreSummary);
        const chunks: FlatSqlSyncChunk[] = [];
        let firstError: unknown = null;
        for (const schema of summarySchemas) {
          try {
            chunks.push(await requestChunk({
              schema,
              ...(datastoreKey ? { datastoreKey } : {}),
              op: 'scan',
              limit: 1,
              offset: 0,
            }));
          } catch (error) {
            firstError ??= error;
          }
        }
        if (chunks.length === 0) {
          return createUnavailableResult('getDataSummary', formatSyncError(firstError ?? 'remote FlatSQL sync summary unavailable'));
        }
        const schemas = chunks
          .map((chunk) => ({
            schemaName: chunk.header.schema,
            count: chunk.header.totalCount,
            totalBytes: estimateTotalBytes(chunk.header.results, chunk.header.totalCount),
          }))
          .filter((schema) => schema.count > 0)
          .sort((left, right) => right.count - left.count || left.schemaName.localeCompare(right.schemaName));
        const totalRecords = schemas.reduce((sum, schema) => sum + schema.count, 0);
        const totalBytes = schemas.reduce((sum, schema) => sum + schema.totalBytes, 0);
        return createAvailableResult('getDataSummary', {
          totalRecords,
          totalBytes,
          schemas,
          sources: schemas.map((schema) => ({
            schemaName: schema.schemaName,
            providerId: providerId ?? targetPeerId,
            ...(datastoreKey ? { datastoreKey } : {}),
            sourceName: sourceName ?? '',
            batchId: '',
            producerPeerId: targetPeerId,
            producerPublicKey: optionalText(options.publicKey) ?? '',
            count: schema.count,
            totalBytes: schema.totalBytes,
          })),
        });
      } catch (error) {
        return createUnavailableResult('getDataSummary', formatSyncError(error));
      }
    },
    scanRawData,
    streamRawData,
    async queryRawData(query: RawDataQuery): Promise<BackendResult<RawDataRecord[]>> {
      try {
        const chunk = await requestChunk({ ...query, op: 'read_chunk' });
        return createAvailableResult('queryRawData', rawRecordsWithDataFromFlatSqlChunk(chunk));
      } catch (error) {
        return createUnavailableResult('queryRawData', formatSyncError(error));
      }
    },
    async readRawDataRecord(_schemaName: string, cid: string): Promise<BackendResult<RawDataRecordBytes>> {
      return createCapabilityResult('readRawDataRecord', 'local-only', `record ${cid} bytes are available after scan-bound FlatSQL streaming`);
    },
    async pinObject(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('pinObject', 'local-only', 'pinning is managed by the local FlatSQL store');
    },
    async unpinObject(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('unpinObject', 'local-only', 'pinning is managed by the local FlatSQL store');
    },
    async listRulesets(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listRulesets', 'local-only', 'retention rulesets are managed by the local FlatSQL store', []);
    },
    async saveRuleset(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('saveRuleset', 'local-only', 'retention rulesets are managed by the local FlatSQL store');
    },
    async runSqlQuery(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('runSqlQuery', 'local-only', 'SQL queries run against the local FlatSQL store after sync', []);
    },
    async getKuboStatus(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('getKuboStatus', 'local-only', 'Kubo status is only available for local desktop nodes');
    },
    async listFiles(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listFiles', 'local-only', 'MFS file browsing is only available for local desktop nodes', []);
    },
    async resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>> {
      return createCapabilityResult('resolveCid', 'local-only', `CID ${cid} resolution is local to the desktop gateway`);
    },
    async readGatewayUrl(path: string): Promise<BackendResult<{ path: string; gatewayUrl: string }>> {
      return createCapabilityResult('readGatewayUrl', 'local-only', `gateway read for ${path} is local to the desktop gateway`);
    },
  };

  async function getNodeSummary(): Promise<BackendResult<NodeSummary>> {
    if (!targetPeerId || candidateAddrs.length === 0) {
      return createUnavailableResult('getNodeSummary', 'remote peer ID and libp2p sync address are required');
    }
    return createAvailableResult('getNodeSummary', {
      displayName,
      peerId: targetPeerId,
      agentVersion: 'sdn-configured-node',
      online: true,
      runtime: 'remote-sdn',
    });
  }

  async function dataSummaryFromDatastores(): Promise<DataSummary | null> {
    const client = await ensureClient();
    if (!client.listFlatSqlSyncDatastores) return null;
    const datastores = await client.listFlatSqlSyncDatastores({ targetPeerId, candidateAddrs });
    if (datastores.results.length === 0) return null;

    const sources: DataSummary['sources'] = [];
    const schemasByName = new Map<string, { schemaName: string; count: number; totalBytes: number }>();
    for (const entry of datastores.results) {
      const schemaName = entry.identity.schemaName;
      if (!schemaName || schemaName === 'unknown') continue;
      const chunk = await requestChunk({
        schema: schemaName,
        datastoreKey: entry.key,
        op: 'scan',
        limit: 1,
        offset: 0,
      });
      const count = Math.max(0, chunk.header.totalCount);
      const totalBytes = estimateTotalBytes(chunk.header.results, count);
      if (count <= 0) continue;
      sources.push(dataSourceSummaryFromDatastore(entry, count, totalBytes));
      const current = schemasByName.get(schemaName) ?? { schemaName, count: 0, totalBytes: 0 };
      current.count += count;
      current.totalBytes += totalBytes;
      schemasByName.set(schemaName, current);
    }
    if (sources.length === 0) return null;
    const schemas = Array.from(schemasByName.values())
      .sort((left, right) => right.count - left.count || left.schemaName.localeCompare(right.schemaName));
    return {
      totalRecords: schemas.reduce((sum, schema) => sum + schema.count, 0),
      totalBytes: schemas.reduce((sum, schema) => sum + schema.totalBytes, 0),
      schemas,
      sources: sources.sort((left, right) => right.count - left.count || left.schemaName.localeCompare(right.schemaName)),
    };
  }
}

function dataSourceSummaryFromDatastore(
  entry: FlatSqlSyncDatastoreEntry,
  count: number,
  totalBytes: number,
): DataSummary['sources'][number] {
  return {
    datastoreKey: entry.key,
    schemaName: entry.identity.schemaName,
    providerId: entry.identity.providerId ?? '',
    sourceName: entry.identity.sourceName ?? '',
    batchId: entry.identity.batchHead ?? '',
    producerPeerId: entry.identity.sourcePeerId ?? '',
    producerPublicKey: entry.identity.sourcePublicKey ?? '',
    count,
    totalBytes,
  };
}

export async function createDefaultLibp2pFlatSqlSyncClient(candidateAddrs: string[]): Promise<Libp2pFlatSqlSyncClient> {
  const normalizedAddrs = normalizeCandidateAddrs(candidateAddrs);
  const [{ createLibp2p }, { noise }, { yamux }, { multiaddr }, { peerIdFromString }] = await Promise.all([
    import('libp2p'),
    import('@chainsafe/libp2p-noise'),
    import('@chainsafe/libp2p-yamux'),
    import('@multiformats/multiaddr'),
    import('@libp2p/peer-id'),
  ]);
  const transports: any[] = [];
  const services: Record<string, any> = {};
  const includeAllTransports = normalizedAddrs.length === 0;
  if (includeAllTransports || normalizedAddrs.some((addr) => addr.includes('/ws') || addr.includes('/wss'))) {
    const [{ webSockets }, { all: wsFilters }] = await Promise.all([
      import('@libp2p/websockets'),
      import('@libp2p/websockets/filters'),
    ]);
    transports.push(webSockets({ filter: wsFilters }));
  }
  if (includeAllTransports || normalizedAddrs.some((addr) => addr.includes('/webtransport'))) {
    const { webTransport } = await import('@libp2p/webtransport');
    transports.push(webTransport());
  }
  if (includeAllTransports || normalizedAddrs.some((addr) => addr.includes('/webrtc') || addr.includes('/p2p-circuit'))) {
    const [{ webRTC }, { circuitRelayTransport }, { identify }] = await Promise.all([
      import('@libp2p/webrtc'),
      import('@libp2p/circuit-relay-v2'),
      import('@libp2p/identify'),
    ]);
    transports.push(webRTC(), circuitRelayTransport({ discoverRelays: 0 }));
    services.identify = identify();
  }
  const libp2p = await createLibp2p({
    transports,
    connectionEncryption: [noise()],
    streamMuxers: [yamux()],
    peerDiscovery: [],
    services,
  });
  await libp2p.start();

  const transport: FlatSqlSyncTransport = {
    async dialProtocol(targetPeerId, protocolId, payload, requestCandidateAddrs) {
      const addrs = normalizeCandidateAddrs(requestCandidateAddrs?.length ? requestCandidateAddrs : normalizedAddrs);
      if (addrs.length === 0) {
        const stream = await libp2p.dialProtocol(peerIdFromString(targetPeerId), protocolId);
        return exchangeFlatSqlSyncStream(stream, payload);
      }

      let lastError: unknown = null;
      for (const addr of addrs) {
        try {
          const stream = await libp2p.dialProtocol(multiaddr(normalizeDialTarget(addr, targetPeerId)), protocolId);
          return await exchangeFlatSqlSyncStream(stream, payload);
        } catch (error) {
          lastError = error;
        }
      }
      throw new Error(`failed to dial ${protocolId} for ${targetPeerId}: ${formatSyncError(lastError)}`);
    },
  };

  return {
    readFlatSqlSyncChunk(query) {
      return requestFlatSqlSyncChunk(transport, query);
    },
    listFlatSqlSyncDatastores(query) {
      return requestFlatSqlSyncDatastores(transport, query);
    },
    openFlatSqlSyncManifest(query) {
      return requestFlatSqlSyncManifest(transport, query);
    },
    measureWireSpeed(query) {
      return requestFlatSqlWireSpeedProbe(transport, query);
    },
    async stop() {
      await libp2p.stop();
    },
  };
}

async function exchangeFlatSqlSyncStream(stream: unknown, payloadBytes: Uint8Array): Promise<Uint8Array> {
  const libp2pStream = stream as {
    sink(source: AsyncIterable<Uint8Array>): Promise<void>;
    source: AsyncIterable<unknown>;
    close(): Promise<void>;
  };
  try {
    await libp2pStream.sink((async function* source() {
      yield cloneStreamBytes(payloadBytes);
    })());
    const chunks: Uint8Array[] = [];
    for await (const chunk of libp2pStream.source) {
      chunks.push(cloneStreamBytes(chunk));
    }
    return concatStreamBytes(chunks);
  } finally {
    await libp2pStream.close().catch(() => undefined);
  }
}

function cloneStreamBytes(chunk: unknown): Uint8Array {
  if (chunk instanceof Uint8Array) return new Uint8Array(chunk);
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk.slice(0));
  if (ArrayBuffer.isView(chunk)) {
    return new Uint8Array(chunk.buffer.slice(chunk.byteOffset, chunk.byteOffset + chunk.byteLength));
  }
  if (chunk && typeof chunk === 'object' && 'subarray' in chunk && typeof chunk.subarray === 'function') {
    return new Uint8Array(chunk.subarray());
  }
  throw new Error('stream chunk must be Uint8Array-compatible bytes');
}

function concatStreamBytes(chunks: Uint8Array[]): Uint8Array {
  const length = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function normalizeDialTarget(addr: string, targetPeerId: string): string {
  const trimmed = addr.trim();
  if (!trimmed) return trimmed;
  if (trimmed.includes('/p2p-circuit')) {
    return trimmed.includes(`/p2p/${targetPeerId}`) ? trimmed : `${trimmed}/p2p/${targetPeerId}`;
  }
  if (trimmed.includes(`/p2p/${targetPeerId}`)) return trimmed;
  if (trimmed.includes('/p2p/')) return `${trimmed}/p2p-circuit/p2p/${targetPeerId}`;
  return `${trimmed}/p2p/${targetPeerId}`;
}

function normalizeCandidateAddrs(addrs: string[]): string[] {
  return Array.from(new Set(addrs.map((addr) => addr.trim()).filter(Boolean)));
}

export function dataScanResultFromChunk(chunk: FlatSqlSyncChunk): DataScanResult {
  return {
    schema: chunk.header.schema,
    totalCount: chunk.header.totalCount,
    count: chunk.header.count,
    limit: chunk.header.limit,
    offset: chunk.header.offset,
    cursor: chunk.header.cursor,
    nextCursor: chunk.header.nextCursor,
    snapshotId: chunk.header.snapshotId,
    head: chunk.header.head,
    highWaterMark: chunk.header.highWaterMark,
    scanHash: chunk.header.scanHash,
    chunkHash: chunk.header.chunkHash,
    queryProfile: chunk.header.queryProfile,
    syncProtocol: chunk.header.syncProtocol,
    maxChunkSize: chunk.header.maxChunkSize,
    transports: chunk.header.transports,
    results: chunk.header.results.map(rawRecordFromFlatSqlRef),
  };
}

export function rawRecordsWithDataFromFlatSqlChunk(chunk: FlatSqlSyncChunk, fallbackRefs: RawDataRecord[] = []): RawDataRecord[] {
  const refs = chunk.header.results.length > 0 ? chunk.header.results.map(rawRecordFromFlatSqlRef) : fallbackRefs;
  if (refs.length > 0 && chunk.records.length < refs.length) {
    throw new Error(`remote FlatSQL sync returned ${chunk.records.length}/${refs.length} FlatBuffer frames`);
  }
  return refs.map((record, index) => ({
    ...record,
    ...(chunk.records[index] ? { dataBytes: chunk.records[index] } : {}),
  }));
}

export function flatSqlRecordKeys(records: RawDataRecord[]): string[] {
  return records.map((record) => [
    record.schemaName,
    record.cid,
    record.providerId ?? '',
    record.sourceName ?? '',
    record.batchId ?? '',
    record.timestamp ?? '',
  ].join('|'));
}

function rawRecordFromFlatSqlRef(record: FlatSqlSyncRecordRef): RawDataRecord {
  return {
    schemaName: record.schemaName,
    cid: record.cid,
    peerId: record.peerId,
    providerId: record.providerId,
    sourceName: record.sourceName,
    batchId: record.batchId,
    producerPeerId: record.producerPeerId,
    producerPublicKey: record.producerPublicKey,
    timestamp: record.timestamp,
    sizeBytes: record.sizeBytes,
  };
}

function rawRecordToFlatSqlRef(record: RawDataRecord): FlatSqlSyncRecordRef {
  return {
    schemaName: record.schemaName,
    cid: record.cid,
    peerId: record.peerId,
    providerId: record.providerId,
    sourceName: record.sourceName,
    batchId: record.batchId,
    timestamp: record.timestamp,
    sizeBytes: record.sizeBytes,
  };
}

function estimateTotalBytes(records: FlatSqlSyncRecordRef[], totalCount: number): number {
  if (records.length === 0 || totalCount <= 0) return 0;
  const averageBytes = records.reduce((sum, record) => sum + record.sizeBytes, 0) / records.length;
  return Math.round(averageBytes * totalCount);
}

function normalizeSummarySchemas(schemas: string[] | undefined): string[] {
  const candidates = schemas?.length ? schemas : DEFAULT_SUMMARY_SCHEMAS;
  return Array.from(new Set(candidates.map((schema) => schema.trim()).filter(Boolean)));
}

function optionalText(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  return trimmed ? trimmed : null;
}

function formatSyncError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
