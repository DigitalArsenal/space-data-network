import {
  FLATSQL_SYNC_PROTOCOL_ID,
  requestFlatSqlPublishedShard,
  requestFlatSqlPublishedShardBatch,
  requestFlatSqlSyncChunk,
  requestFlatSqlSyncDatastores,
  requestFlatSqlSyncManifest,
  requestFlatSqlWireSpeedProbe,
  type FlatSqlSyncChunk,
  type FlatSqlSyncDatastoreEntry,
  type FlatSqlSyncDatastoreList,
  type FlatSqlSyncManifest,
  type FlatSqlPublishedShard,
  type FlatSqlPublishedShardBatch,
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
  type NodeIdentityApplyResult,
  type NodeIdentitySettings,
  type NodeSummary,
  type ObservedSdnPeer,
  type RawDataQuery,
  type RawDataRecord,
  type RawDataRecordBytes,
  type RawDataStreamRequest,
  type SdnBackend,
  type StorageSummary,
} from './sdn-backend';
import { createUnavailableChannelBackend } from './channel-backend';
import { flatSqlSourceNameForSchema } from './data-source-routing';

export interface Libp2pFlatSqlSyncClient {
  readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk>;
  listFlatSqlSyncDatastores?(query: Pick<FlatSqlSyncQuery, 'targetPeerId' | 'candidateAddrs'>): Promise<FlatSqlSyncDatastoreList>;
  openFlatSqlSyncManifest?(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest>;
  readFlatSqlPublishedShard?(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard>;
  readFlatSqlPublishedShardBatch?(query: FlatSqlSyncQuery & { cids: string[] }): Promise<FlatSqlPublishedShardBatch>;
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

export interface Libp2pFlatSqlSyncClientOptions {
  requestTimeoutMs?: number;
}

const DEFAULT_SUMMARY_SCHEMAS = ['CAT.fbs', 'EPM.fbs', 'MPE.fbs', 'OMM.fbs', 'PNM.fbs', 'SPW.fbs'];
export const DEFAULT_LIBP2P_FLATSQL_SYNC_REQUEST_TIMEOUT_MS = 60_000;
export const LIBP2P_FLATSQL_SYNC_YAMUX_OPTIONS = {
  initialStreamWindowSize: 16 * 1024 * 1024,
  maxStreamWindowSize: 128 * 1024 * 1024,
  maxMessageSize: 1024 * 1024,
};
export const LIBP2P_FLATSQL_SYNC_MAX_OUTBOUND_STREAMS = 512;

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
    const request: FlatSqlSyncQuery = {
      targetPeerId,
      candidateAddrs,
      op: query.op,
      schema: query.schema,
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
    };
    const queryHasDatastoreKey = Object.prototype.hasOwnProperty.call(query, 'datastoreKey') && query.datastoreKey !== undefined;
    const queryHasProviderId = Object.prototype.hasOwnProperty.call(query, 'providerId') && query.providerId !== undefined;
    const queryHasSourceName = Object.prototype.hasOwnProperty.call(query, 'sourceName') && query.sourceName !== undefined;
    const resolvedDatastoreKey = queryHasDatastoreKey ? optionalText(query.datastoreKey) : datastoreKey;
    const resolvedProviderId = queryHasProviderId ? optionalText(query.providerId) : providerId;
    const requestedSourceName = queryHasSourceName ? optionalText(query.sourceName) : sourceName;
    const resolvedSourceName = flatSqlSourceNameForSchema({
      schemaName: query.schema,
      providerId: resolvedProviderId,
      sourceName: requestedSourceName,
    });
    if (resolvedDatastoreKey) request.datastoreKey = resolvedDatastoreKey;
    if (resolvedProviderId) request.providerId = resolvedProviderId;
    if (resolvedSourceName) request.sourceName = resolvedSourceName;
    return client.readFlatSqlSyncChunk(request);
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
    channels: createUnavailableChannelBackend('channel operations require the HTTP channel API and verified grants'),
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
    async getNodeIdentitySettings(): Promise<BackendResult<NodeIdentitySettings>> {
      return createCapabilityResult('getNodeIdentitySettings', 'local-only', 'node identity unlock settings are local to the desktop app', { ttlMs: 3600000 });
    },
    async saveNodeIdentitySettings(settings: NodeIdentitySettings): Promise<BackendResult<NodeIdentitySettings>> {
      return createCapabilityResult('saveNodeIdentitySettings', 'local-only', 'node identity unlock settings are local to the desktop app', settings);
    },
    async selectFlatbufferStorageLocation(): Promise<BackendResult<{ canceled: boolean; path: string | null }>> {
      return createCapabilityResult('selectFlatbufferStorageLocation', 'local-only', 'FlatBuffer storage locations are selected on the local desktop app', {
        canceled: true,
        path: null,
      });
    },
    async applyWalletNodeIdentity(): Promise<BackendResult<NodeIdentityApplyResult>> {
      return createCapabilityResult('applyWalletNodeIdentity', 'local-only', 'wallet-backed node identity changes must run on the local desktop node');
    },
    async logoutNodeIdentity(): Promise<BackendResult<Record<string, unknown>>> {
      return createAvailableResult('logoutNodeIdentity', { ok: true });
    },
    async getWalletStorage() {
      return createCapabilityResult('getWalletStorage', 'local-only', 'wallet storage is local to the desktop app', {
        entries: {},
        encryptedAtRest: false,
      });
    },
    async saveWalletStorage(entries: Record<string, string | null>) {
      return createCapabilityResult('saveWalletStorage', 'local-only', 'wallet storage is local to the desktop app', {
        entries: Object.fromEntries(
          Object.entries(entries).filter((entry): entry is [string, string] => typeof entry[1] === 'string'),
        ),
        encryptedAtRest: false,
      });
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
        const datastoreSchemas = new Set(datastoreSummary?.schemas.map((schema) => schema.schemaName) ?? []);
        const schemaScanSummary = await dataSummaryFromSchemaScans(datastoreSchemas).catch((error) => {
          if (datastoreSummary) return null;
          throw error;
        });
        const mergedSummary = mergeDataSummaries(datastoreSummary, schemaScanSummary);
        if (!mergedSummary) return createUnavailableResult('getDataSummary', 'remote FlatSQL sync summary unavailable');
        return createAvailableResult('getDataSummary', mergedSummary);
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

  async function dataSummaryFromSchemaScans(excludedSchemas: ReadonlySet<string> = new Set()): Promise<DataSummary | null> {
    const chunks: FlatSqlSyncChunk[] = [];
    let firstError: unknown = null;
    for (const schema of summarySchemas) {
      if (excludedSchemas.has(schema)) continue;
      const schemaSourceName = flatSqlSourceNameForSchema({ schemaName: schema, providerId, sourceName });
      try {
        chunks.push(await requestChunk({
          schema,
          ...(datastoreKey ? { datastoreKey } : {}),
          ...(schemaSourceName ? { sourceName: schemaSourceName } : { sourceName: '' }),
          op: 'scan',
          limit: 1,
          offset: 0,
        }));
      } catch (error) {
        firstError ??= error;
      }
    }
    if (chunks.length === 0) {
      if (firstError) throw firstError;
      return null;
    }
    const schemas = chunks
      .map((chunk) => ({
        schemaName: chunk.header.schema,
        count: chunk.header.totalCount,
        totalBytes: estimateTotalBytes(chunk.header.results, chunk.header.totalCount),
      }))
      .filter((schema) => schema.count > 0)
      .sort((left, right) => right.count - left.count || left.schemaName.localeCompare(right.schemaName));
    if (schemas.length === 0) return null;
    return {
      totalRecords: schemas.reduce((sum, schema) => sum + schema.count, 0),
      totalBytes: schemas.reduce((sum, schema) => sum + schema.totalBytes, 0),
      schemas,
      sources: schemas.map((schema) => ({
        schemaName: schema.schemaName,
        providerId: providerId ?? targetPeerId,
        ...(datastoreKey ? { datastoreKey } : {}),
        sourceName: flatSqlSourceNameForSchema({ schemaName: schema.schemaName, providerId, sourceName }) ?? '',
        batchId: '',
        producerPeerId: targetPeerId,
        producerPublicKey: optionalText(options.publicKey) ?? '',
        count: schema.count,
        totalBytes: schema.totalBytes,
      })),
    };
  }
}

function mergeDataSummaries(...summaries: Array<DataSummary | null>): DataSummary | null {
  const activeSummaries = summaries.filter((summary): summary is DataSummary => Boolean(summary));
  if (activeSummaries.length === 0) return null;
  const sources = activeSummaries.flatMap((summary) => summary.sources);
  const schemasByName = new Map<string, { schemaName: string; count: number; totalBytes: number }>();
  for (const summary of activeSummaries) {
    for (const schema of summary.schemas) {
      const current = schemasByName.get(schema.schemaName) ?? { schemaName: schema.schemaName, count: 0, totalBytes: 0 };
      current.count += schema.count;
      current.totalBytes += schema.totalBytes;
      schemasByName.set(schema.schemaName, current);
    }
  }
  const schemas = Array.from(schemasByName.values())
    .filter((schema) => schema.count > 0)
    .sort((left, right) => right.count - left.count || left.schemaName.localeCompare(right.schemaName));
  return {
    totalRecords: schemas.reduce((sum, schema) => sum + schema.count, 0),
    totalBytes: schemas.reduce((sum, schema) => sum + schema.totalBytes, 0),
    schemas,
    sources: sources.sort((left, right) => right.count - left.count || left.schemaName.localeCompare(right.schemaName)),
  };
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

export async function createDefaultLibp2pFlatSqlSyncClient(
  candidateAddrs: string[],
  options: Libp2pFlatSqlSyncClientOptions = {},
): Promise<Libp2pFlatSqlSyncClient> {
  const normalizedAddrs = normalizeCandidateAddrs(candidateAddrs);
  const requestTimeoutMs = normalizeTimeoutMs(options.requestTimeoutMs);
  const [{ createLibp2p }, { noise }, { yamux }, { multiaddr }, { peerIdFromString }] = await Promise.all([
    import('libp2p'),
    import('@chainsafe/libp2p-noise'),
    import('@chainsafe/libp2p-yamux'),
    import('@multiformats/multiaddr'),
    import('@libp2p/peer-id'),
  ]);
  const transports: any[] = [];
  const services: Record<string, any> = {};
  const transportSelection = selectLibp2pFlatSqlSyncTransports(normalizedAddrs);
  if (transportSelection.tcp && isNodeRuntime()) {
    const { tcp } = await loadNodeTcpTransport();
    transports.push(tcp());
  }
  if (transportSelection.webSockets) {
    const [{ webSockets }, { all: wsFilters }] = await Promise.all([
      import('@libp2p/websockets'),
      import('@libp2p/websockets/filters'),
    ]);
    transports.push(webSockets({ filter: wsFilters }));
  }
  if (transportSelection.webTransport) {
    const { webTransport } = await import('@libp2p/webtransport');
    transports.push(webTransport());
  }
  if (transportSelection.webRtcRelay || transportSelection.webRtcDirect) {
    const [{ webRTC, webRTCDirect }, { circuitRelayTransport }, { identify }] = await Promise.all([
      import('@spacedatanetwork/libp2p-webrtc-v1'),
      import('@libp2p/circuit-relay-v2'),
      import('@libp2p/identify'),
    ]);
    if (transportSelection.webRtcRelay) {
      transports.push(webRTC(), circuitRelayTransport({ discoverRelays: 0 }));
    }
    if (transportSelection.webRtcDirect) {
      transports.push(webRTCDirect());
    }
    services.identify = identify();
  }
  const libp2p = await createLibp2p({
    transports,
    connectionEncryption: [noise()],
    streamMuxers: [yamux(LIBP2P_FLATSQL_SYNC_YAMUX_OPTIONS)],
    peerDiscovery: [],
    services,
  });
  await libp2p.start();

  const transport: FlatSqlSyncTransport = {
    async dialProtocol(targetPeerId, protocolId, payload, requestCandidateAddrs) {
      const addrs = orderLibp2pFlatSqlSyncDialAddrs(
        normalizeCandidateAddrs(requestCandidateAddrs?.length ? requestCandidateAddrs : normalizedAddrs)
          .filter((addr) => isLibp2pFlatSqlSyncAddrDialable(addr, transportSelection)),
      );
      if (addrs.length === 0) {
        const stream = await withAbortableTimeout(
          `FlatSQL sync dial ${protocolId}`,
          requestTimeoutMs,
          (signal) => libp2p.dialProtocol(peerIdFromString(targetPeerId), protocolId, {
            signal,
            maxOutboundStreams: LIBP2P_FLATSQL_SYNC_MAX_OUTBOUND_STREAMS,
          }),
        );
        return exchangeFlatSqlSyncStream(stream, payload, {
          timeoutMs: requestTimeoutMs,
          label: `FlatSQL sync exchange ${protocolId}`,
        });
      }

      let lastError: unknown = null;
      for (const addr of addrs) {
        try {
          const stream = await withAbortableTimeout(
            `FlatSQL sync dial ${protocolId}`,
            requestTimeoutMs,
            (signal) => libp2p.dialProtocol(multiaddr(normalizeDialTarget(addr, targetPeerId)), protocolId, {
              signal,
              maxOutboundStreams: LIBP2P_FLATSQL_SYNC_MAX_OUTBOUND_STREAMS,
            }),
          );
          return await exchangeFlatSqlSyncStream(stream, payload, {
            timeoutMs: requestTimeoutMs,
            label: `FlatSQL sync exchange ${protocolId}`,
          });
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
    readFlatSqlPublishedShard(query) {
      return requestFlatSqlPublishedShard(transport, query);
    },
    readFlatSqlPublishedShardBatch(query) {
      return requestFlatSqlPublishedShardBatch(transport, query);
    },
    measureWireSpeed(query) {
      return requestFlatSqlWireSpeedProbe(transport, query);
    },
    async stop() {
      await libp2p.stop();
    },
  };
}

export interface Libp2pFlatSqlSyncTransportSelection {
  tcp: boolean;
  webSockets: boolean;
  webTransport: boolean;
  webRtcRelay: boolean;
  webRtcDirect: boolean;
}

export function selectLibp2pFlatSqlSyncTransports(candidateAddrs: string[]): Libp2pFlatSqlSyncTransportSelection {
  const addrs = normalizeCandidateAddrs(candidateAddrs);
  const includeAll = addrs.length === 0;
  return {
    tcp: includeAll || addrs.some((addr) => addr.includes('/tcp/') && !addr.includes('/ws') && !addr.includes('/wss') && !addr.includes('/webtransport')),
    webSockets: includeAll || addrs.some((addr) => addr.includes('/ws') || addr.includes('/wss')),
    webTransport: includeAll || addrs.some((addr) => addr.includes('/webtransport')),
    webRtcRelay: includeAll || addrs.some((addr) => (addr.includes('/webrtc') && !addr.includes('/webrtc-direct')) || addr.includes('/p2p-circuit')),
    webRtcDirect: includeAll || addrs.some((addr) => addr.includes('/webrtc-direct')),
  };
}

export function isLibp2pFlatSqlSyncAddrDialable(addr: string, selection: Libp2pFlatSqlSyncTransportSelection): boolean {
  if (addr.includes('/webtransport')) return selection.webTransport;
  if (addr.includes('/webrtc-direct')) return selection.webRtcDirect && runtimeSupportsWebRtcDirect();
  if (addr.includes('/webrtc') || addr.includes('/p2p-circuit')) return selection.webRtcRelay && runtimeSupportsWebRtcRelay();
  if (addr.includes('/ws') || addr.includes('/wss')) return selection.webSockets;
  if (addr.includes('/tcp/')) return selection.tcp && isNodeRuntime();
  return true;
}

export function orderLibp2pFlatSqlSyncDialAddrs(addrs: string[]): string[] {
  const nodeRuntime = isNodeRuntime();
  return [...addrs].sort((left, right) => dialAddrPriority(left, nodeRuntime) - dialAddrPriority(right, nodeRuntime));
}

export interface ExchangeFlatSqlSyncStreamOptions {
  timeoutMs?: number;
  label?: string;
}

export async function exchangeFlatSqlSyncStream(
  stream: unknown,
  payloadBytes: Uint8Array,
  options: ExchangeFlatSqlSyncStreamOptions = {},
): Promise<Uint8Array> {
  const libp2pStream = stream as {
    sink(source: AsyncIterable<Uint8Array>): Promise<void>;
    source: AsyncIterable<unknown>;
    close(): Promise<void>;
  };
  const timeoutMs = normalizeTimeoutMs(options.timeoutMs);
  return withAbortableTimeout(
    options.label ?? 'FlatSQL sync exchange',
    timeoutMs,
    () => performFlatSqlSyncStreamExchange(libp2pStream, payloadBytes),
    () => libp2pStream.close().catch(() => undefined),
  );
}

async function performFlatSqlSyncStreamExchange(
  libp2pStream: {
    sink(source: AsyncIterable<Uint8Array>): Promise<void>;
    source: AsyncIterable<unknown>;
    close(): Promise<void>;
  },
  payloadBytes: Uint8Array,
): Promise<Uint8Array> {
  const sinkDone = libp2pStream.sink((async function* source() {
    yield cloneStreamBytes(payloadBytes);
  })());
  let responseError: unknown;
  try {
    const chunks: Uint8Array[] = [];
    try {
      for await (const chunk of libp2pStream.source) {
        chunks.push(streamChunkBytes(chunk));
      }
    } catch (error) {
      responseError = error;
    }
    if (responseError) throw responseError;
    await sinkDone;
    return concatStreamBytes(chunks);
  } finally {
    await libp2pStream.close().catch(() => undefined);
    if (responseError) await sinkDone.catch(() => undefined);
  }
}

async function withAbortableTimeout<T>(
  label: string,
  timeoutMs: number,
  operation: (signal: AbortSignal) => Promise<T>,
  onTimeout?: () => void | Promise<void>,
): Promise<T> {
  if (timeoutMs <= 0) return operation(new AbortController().signal);
  const controller = new AbortController();
  let timeout: ReturnType<typeof setTimeout> | null = null;
  let timedOut = false;
  const timeoutMessage = `${label} timed out after ${timeoutMs} ms`;
  const operationPromise = operation(controller.signal);
  const timeoutPromise = new Promise<never>((_, reject) => {
    timeout = setTimeout(() => {
      timedOut = true;
      controller.abort(new Error(timeoutMessage));
      void Promise.resolve(onTimeout?.()).catch(() => undefined);
      reject(new Error(timeoutMessage));
    }, timeoutMs);
  });

  try {
    return await Promise.race([operationPromise, timeoutPromise]);
  } finally {
    if (timeout) clearTimeout(timeout);
    if (timedOut) operationPromise.catch(() => undefined);
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

function streamChunkBytes(chunk: unknown): Uint8Array {
  if (chunk instanceof Uint8Array) return chunk;
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
  if (ArrayBuffer.isView(chunk)) {
    return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
  }
  if (chunk && typeof chunk === 'object' && 'subarray' in chunk && typeof chunk.subarray === 'function') {
    const view = (chunk.subarray as () => unknown).call(chunk);
    if (view instanceof Uint8Array) return view;
    if (view instanceof ArrayBuffer) return new Uint8Array(view);
    if (ArrayBuffer.isView(view)) return new Uint8Array(view.buffer, view.byteOffset, view.byteLength);
    return new Uint8Array(view as ArrayLike<number>);
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

function normalizeTimeoutMs(timeoutMs: number | null | undefined): number {
  if (typeof timeoutMs !== 'number' || !Number.isFinite(timeoutMs)) return DEFAULT_LIBP2P_FLATSQL_SYNC_REQUEST_TIMEOUT_MS;
  return Math.max(0, Math.floor(timeoutMs));
}

function isNodeRuntime(): boolean {
  return typeof process !== 'undefined' && Boolean(process.versions?.node) && typeof window === 'undefined';
}

function runtimeSupportsWebRtcDirect(): boolean {
  return !isNodeRuntime() && typeof globalThis.RTCPeerConnection !== 'undefined';
}

function runtimeSupportsWebRtcRelay(): boolean {
  return !isNodeRuntime() && typeof globalThis.RTCPeerConnection !== 'undefined';
}

function dialAddrPriority(addr: string, nodeRuntime: boolean): number {
  if (nodeRuntime && addr.includes('/tcp/') && !addr.includes('/ws') && !addr.includes('/wss') && !addr.includes('/webtransport')) return 0;
  if (!nodeRuntime && addr.includes('/webtransport')) return 0;
  if (!nodeRuntime && addr.includes('/webrtc-direct')) return 1;
  if (addr.includes('/ws') || addr.includes('/wss')) return nodeRuntime ? 1 : 2;
  if (!nodeRuntime && (addr.includes('/webrtc') || addr.includes('/p2p-circuit'))) return 3;
  return 4;
}

async function loadNodeTcpTransport(): Promise<{ tcp: () => any }> {
  const dynamicImport = new Function('specifier', 'return import(specifier)') as (specifier: string) => Promise<{ tcp: () => any }>;
  return dynamicImport('@libp2p/tcp');
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
