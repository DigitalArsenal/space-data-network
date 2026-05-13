import {
  createLocalFlatSqlStore,
  type LocalFlatSqlPinLedgerEntry,
  type LocalFlatSqlPinLedgerQuery,
  type LocalFlatSqlQueryOptions,
  type LocalFlatSqlStandardStats,
  type LocalFlatSqlStatsOptions,
  type LocalFlatSqlStore,
  type LocalFlatSqlStoreOptions,
  type LocalFlatSqlStreamIngestOptions,
} from './local-flatsql';
import { TimeoutError, withTimeout } from './async-timeout';
import { Libp2pFlatSqlSyncBackendCache } from './libp2p-sync-backend-cache';
import {
  dataScanResultFromChunk,
  flatSqlRecordKeys,
  rawRecordsWithDataFromFlatSqlChunk,
} from './sdn-backend-libp2p-sync';
import {
  fetchCidBytesFromGateway,
  timedFlatBufferStreamFromPublishedFlatSqlSegment,
} from './published-flatbuffer-shard';
import { retryRemoteSyncOperation } from './remote-sync-retry';
import { SerialTaskQueue } from './serial-task-queue';
import { syncRowCountSummary } from './sync-progress';
import { DEFAULT_WIRE_SPEED_TARGET, measuredWireSpeedUtilization, meetsWireSpeedTarget } from './sync-throughput';
import { connectIpfsArtifactPeers, connectIpfsArtifactProviders } from './ipfs-artifact-peers';
import type { DataSummary, RawDataRecord, SdnBackend } from './sdn-backend';
import type { FlatSqlSyncManifest, FlatSqlSyncManifestSegment } from '../../flatsql-sync';
import type {
  WorkerFlatSqlSyncBackendConfig,
  WorkerRemotePageRequest,
  WorkerRemotePageResult,
  WorkerSchemaSyncProgress,
  WorkerSchemaSyncRequest,
} from './local-flatsql-worker-client';

type WorkerRequest =
  | { id: number; type: 'init'; options: LocalFlatSqlStoreOptions }
  | { id: number; type: 'ingestRecords'; standardId: string; records: RawDataRecord[]; sourceOrOptions?: unknown }
  | { id: number; type: 'ingestFlatBufferStream'; standardId: string; streamBytes: Uint8Array; options?: LocalFlatSqlStreamIngestOptions | null }
  | { id: number; type: 'flush'; standardId?: string }
  | { id: number; type: 'query'; sql: string; standardId?: string; options?: LocalFlatSqlQueryOptions }
  | { id: number; type: 'getStats'; options?: LocalFlatSqlStatsOptions }
  | { id: number; type: 'recordPinLedgerEntries'; entries: LocalFlatSqlPinLedgerEntry[] }
  | { id: number; type: 'listPinLedgerEntries'; query?: LocalFlatSqlPinLedgerQuery }
  | { id: number; type: 'getRemoteDataSummary'; backendConfig: WorkerFlatSqlSyncBackendConfig }
  | { id: number; type: 'queryRemotePage'; request: WorkerRemotePageRequest }
  | { id: number; type: 'syncSchema'; request: WorkerSchemaSyncRequest }
  | { id: number; type: 'destroy' };

type WorkerResponse =
  | { id: number; ok: true; data?: unknown; stats?: LocalFlatSqlStandardStats[] }
  | { id: number; ok: false; error: string }
  | { id: number; type: 'syncProgress'; progress: WorkerSchemaSyncProgress; stats: LocalFlatSqlStandardStats[] };

type FlatSqlWorkerGlobal = {
  onmessage: ((event: MessageEvent<WorkerRequest>) => void) | null;
  postMessage(response: WorkerResponse, transferables?: Transferable[]): void;
};

const workerGlobal = self as unknown as FlatSqlWorkerGlobal;
const syncBackendCache = new Libp2pFlatSqlSyncBackendCache();
const schemaSyncQueue = new SerialTaskQueue();
const REMOTE_SYNC_OPERATION_TIMEOUT_MS = 60_000;
const REMOTE_SYNC_RETRY_ATTEMPTS = 4;
const REMOTE_SYNC_RETRY_DELAY_MS = 500;
const PUBLISHED_MANIFEST_SYNC_CHUNK_SIZE = 50_000;
const PUBLISHED_SHARD_FETCH_CONCURRENCY = 24;
const PUBLISHED_SHARD_PROVIDER_DISCOVERY_CID_LIMIT = 16;
const WIRE_SPEED_PROBE_BYTES = 64 * 1024 * 1024;
let store: LocalFlatSqlStore | null = null;

workerGlobal.onmessage = (event: MessageEvent<WorkerRequest>) => {
  void handleRequest(event.data);
};

async function handleRequest(request: WorkerRequest): Promise<void> {
  try {
    switch (request.type) {
      case 'init':
        store?.destroy();
        await syncBackendCache.destroy();
        store = await createLocalFlatSqlStore(request.options);
        postSuccess(request.id, undefined, await stats({ includeCachedBytes: false }));
        return;
      case 'ingestRecords': {
        const currentStore = requireStore();
        const ingested = await currentStore.ingestRecords(
          request.standardId,
          request.records,
          request.sourceOrOptions as Parameters<LocalFlatSqlStore['ingestRecords']>[2],
        );
        postSuccess(request.id, ingested, await stats({ includeCachedBytes: false }));
        return;
      }
      case 'ingestFlatBufferStream': {
        const currentStore = requireStore();
        const ingested = await currentStore.ingestFlatBufferStream(
          request.standardId,
          request.streamBytes,
          request.options,
        );
        postSuccess(request.id, ingested, await stats({ includeCachedBytes: false }));
        return;
      }
      case 'flush': {
        const currentStore = requireStore();
        await currentStore.flush(request.standardId);
        postSuccess(request.id, undefined, await stats({ includeCachedBytes: true }));
        return;
      }
      case 'query': {
        const result = await requireStore().query(request.sql, request.standardId, request.options);
        postSuccess(request.id, result);
        return;
      }
      case 'getStats':
        postSuccess(request.id, await stats(request.options));
        return;
      case 'recordPinLedgerEntries':
        await requireStore().recordPinLedgerEntries(request.entries);
        postSuccess(request.id, undefined, await stats({ includeCachedBytes: true }));
        return;
      case 'listPinLedgerEntries':
        postSuccess(request.id, await requireStore().listPinLedgerEntries(request.query));
        return;
      case 'getRemoteDataSummary':
        postSuccess(request.id, await getRemoteDataSummary(request.backendConfig));
        return;
      case 'queryRemotePage':
        postRemotePageSuccess(request.id, await queryRemotePage(request.request));
        return;
      case 'syncSchema':
        postSuccess(request.id, await schemaSyncQueue.enqueue(() => syncSchemaInWorker(request.id, request.request)));
        return;
      case 'destroy':
        store?.destroy();
        store = null;
        await syncBackendCache.destroy();
        postSuccess(request.id);
        return;
      default:
        throw new Error('unsupported FlatSQL worker request');
    }
  } catch (error) {
    postFailure(request.id, error instanceof Error ? error.message : String(error));
  }
}

function requireStore(): LocalFlatSqlStore {
  if (!store) throw new Error('FlatSQL worker store is not initialized');
  return store;
}

async function stats(options?: LocalFlatSqlStatsOptions): Promise<LocalFlatSqlStandardStats[]> {
  return await requireStore().getStats(options);
}

async function getRemoteDataSummary(backendConfig: WorkerFlatSqlSyncBackendConfig): Promise<DataSummary | null> {
  const summary = await withRemoteBackendOperation(backendConfig, 'Remote data summary', (backend) => backend.getDataSummary());
  if (!summary.ok) throw new Error(summary.capability.reason ?? 'Remote data summary failed');
  return summary.data;
}

async function queryRemotePage(request: WorkerRemotePageRequest): Promise<WorkerRemotePageResult> {
  const currentStore = requireStore();
  const chunk = await withRemoteSyncChunkOperation(
    request.backendConfig,
    'Remote page chunk',
    {
      targetPeerId: '',
      ...request.query,
      op: 'read_chunk',
    },
  );
  const scan = dataScanResultFromChunk(chunk);
  const records = rawRecordsWithDataFromFlatSqlChunk(chunk, scan.results);
  if (chunk.recordStream.byteLength > 0 && scan.results.length > 0) {
    await currentStore.ingestFlatBufferStream(request.standardId, chunk.recordStream, {
      source: request.source,
      persist: true,
      recordKeys: flatSqlRecordKeys(scan.results),
    });
  }
  return {
    scan: scan ?? null,
    records,
    stats: await currentStore.getStats({ includeCachedBytes: true }),
  };
}

async function syncSchemaInWorker(id: number, request: WorkerSchemaSyncRequest): Promise<{ progress: WorkerSchemaSyncProgress; stats: LocalFlatSqlStandardStats[] }> {
  const currentStore = requireStore();
  let currentStats = await currentStore.getStats({ includeCachedBytes: false });
  let offset = localRowsForStandard(currentStats, request.standardId);
  let localRows = offset;
  let cachedBytes = cachedBytesForStandard(currentStats, request.standardId);
  let totalRows = Math.max(request.totalRows, localRows);
  let recordsSincePersist = 0;
  const canResume = localRows > 0;
  let nextCursor = canResume ? request.initialProgress.nextCursor ?? '' : '';
  let snapshotId = canResume ? request.initialProgress.snapshotId ?? '' : '';
  let head = canResume ? request.initialProgress.head ?? '' : '';
  let highWaterMark = canResume ? request.initialProgress.highWaterMark ?? '' : '';
  let queryProfile = normalizeSyncQueryProfile(request.queryProfile ?? (canResume ? request.initialProgress.queryProfile : null));
  const syncFilter = request.syncFilter?.trim() || undefined;
  let verifiedChunks = canResume ? request.initialProgress.verifiedChunks.slice(-256) : [];
  let snapshotVerifiedForSession = false;
  let downloadedBytes = 0;
  let manifestDiscoveryMs = 0;
  let networkTransferMs = 0;
  let verificationMs = 0;
  let flatSqlMaterializationMs = 0;
  const manifestStartedAt = Date.now();
  const publishedManifest = await openPublishedManifest(request.backendConfig, request.schema, syncFilter, queryProfile).catch(() => null);
  manifestDiscoveryMs = Math.max(0, Date.now() - manifestStartedAt);
  const measuredWireSpeedBytesPerSecond = await measuredWireSpeedBaselineBytesPerSecond(request.backendConfig);
  const syncStartedAtMs = Date.now();

  const providerPeerId = request.backendConfig.targetPeerId || null;
  const providerPublicKey = request.backendConfig.publicKey || request.backendConfig.targetPeerId || null;
  let progress = progressFor(request.initialProgress, {
    status: 'syncing',
    syncedRows: localRows,
    totalRows,
    localRows,
    cachedBytes,
    pinnedBytes: cachedBytes,
    ...downloadProgressPatch(syncStartedAtMs, downloadedBytes, measuredWireSpeedBytesPerSecond),
    manifestDiscoveryMs,
    networkTransferMs,
    verificationMs,
    flatSqlMaterializationMs,
    providerPeerId,
    providerPublicKey,
    snapshotId: snapshotId || null,
    head: head || null,
    highWaterMark: highWaterMark || null,
    nextCursor: nextCursor || null,
    queryProfile,
    syncFilter: syncFilter ?? null,
    verifiedChunks,
    error: null,
  });
  postProgress(id, progress, currentStats);

  try {
    if (publishedManifest && publishedManifest.segments.length > 0) {
      return await syncPublishedSegments({
        id,
        request,
        currentStore,
        manifest: publishedManifest.manifest,
        segments: publishedManifest.segments,
        currentStats,
        progress,
        localRows,
        totalRows,
        cachedBytes,
        verifiedChunks,
        downloadedBytes,
        manifestDiscoveryMs,
        networkTransferMs,
        verificationMs,
        flatSqlMaterializationMs,
        syncStartedAtMs,
        measuredWireSpeedBytesPerSecond,
        providerPeerId,
        providerPublicKey,
      });
    }

    while (localRows < totalRows || totalRows === 0) {
      if (cachedBytes >= request.capBytes && localRows < totalRows) {
        await currentStore.flush(request.standardId);
        currentStats = await currentStore.getStats({ includeCachedBytes: true });
        progress = progressFor(progress, {
          status: 'capped',
          syncedRows: localRows,
          totalRows,
          localRows,
          cachedBytes,
          pinnedBytes: cachedBytes,
          ...downloadProgressPatch(syncStartedAtMs, downloadedBytes, measuredWireSpeedBytesPerSecond),
          manifestDiscoveryMs,
          networkTransferMs,
          verificationMs,
          flatSqlMaterializationMs,
          providerPeerId,
          providerPublicKey,
          snapshotId: snapshotId || null,
          head: head || null,
          nextCursor: nextCursor || null,
          queryProfile,
          syncFilter: syncFilter ?? null,
          verifiedChunks,
          error: `Storage cap reached at ${request.capBytes} bytes`,
        });
        postProgress(id, progress, currentStats);
        return { progress, stats: currentStats };
      }

      let networkStartedAt = Date.now();
      let chunk = await withRemoteSyncChunkOperation(
        request.backendConfig,
        'Remote sync chunk',
        {
          targetPeerId: '',
          schema: request.schema,
          op: 'read_chunk',
          limit: request.pageSize,
          ...(syncFilter ? { syncFilter } : {}),
          ...(nextCursor
            ? {
                cursor: nextCursor,
                snapshotId: snapshotId || undefined,
                head: head || undefined,
                ...(snapshotVerifiedForSession
                  ? {
                      totalCount: totalRows,
                      highWaterMark: highWaterMark || undefined,
                    }
                  : {}),
            queryProfile,
          }
            : { offset, queryProfile }),
        },
      );
      networkTransferMs += Math.max(0, Date.now() - networkStartedAt);

      let scan = dataScanResultFromChunk(chunk);
      downloadedBytes += chunk.recordStream.byteLength;

      if (nextCursor && head && scan.head && scan.head !== head) {
        nextCursor = '';
        offset = localRows;
        snapshotVerifiedForSession = false;
        networkStartedAt = Date.now();
        chunk = await withRemoteSyncChunkOperation(
          request.backendConfig,
          'Remote sync snapshot refresh',
          {
            targetPeerId: '',
            schema: request.schema,
            op: 'read_chunk',
            limit: request.pageSize,
            offset,
            queryProfile,
            ...(syncFilter ? { syncFilter } : {}),
          },
        );
        networkTransferMs += Math.max(0, Date.now() - networkStartedAt);
        scan = dataScanResultFromChunk(chunk);
        downloadedBytes += chunk.recordStream.byteLength;
      }

      snapshotId = scan.snapshotId || snapshotId;
      head = scan.head || head;
      highWaterMark = scan.highWaterMark || highWaterMark;
      snapshotVerifiedForSession = true;
      queryProfile = scan.queryProfile || queryProfile;
      totalRows = Math.max(totalRows, scan.totalCount ?? 0, offset + scan.results.length);
      if (scan.results.length === 0) break;

      let materializationStartedAt = Date.now();
      const ingestedRows = await currentStore.ingestFlatBufferStream(request.standardId, chunk.recordStream, {
        source: request.source,
        persist: false,
        recordKeys: flatSqlRecordKeys(scan.results),
      });
      flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
      recordsSincePersist += ingestedRows;
      const chunkHash = scan.chunkHash || scan.scanHash;
      if (chunkHash && !verifiedChunks.includes(chunkHash)) {
        verifiedChunks = [...verifiedChunks, chunkHash].slice(-256);
      }
      if (recordsSincePersist >= request.persistRecordInterval || offset + scan.results.length >= totalRows) {
        materializationStartedAt = Date.now();
        await currentStore.flush(request.standardId);
        flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
        recordsSincePersist = 0;
        currentStats = await currentStore.getStats({ includeCachedBytes: true });
      } else {
        currentStats = await currentStore.getStats({ includeCachedBytes: false });
      }
      localRows = localRowsForStandard(currentStats, request.standardId);
      cachedBytes = cachedBytesForStandard(currentStats, request.standardId);
      offset += scan.results.length;
      nextCursor = scan.nextCursor ?? '';

      progress = progressFor(progress, {
        status: localRows >= totalRows ? 'synced' : 'syncing',
        syncedRows: localRows,
        totalRows,
        localRows,
        cachedBytes,
        pinnedBytes: cachedBytes,
        ...downloadProgressPatch(syncStartedAtMs, downloadedBytes, measuredWireSpeedBytesPerSecond),
        manifestDiscoveryMs,
        networkTransferMs,
        verificationMs,
        flatSqlMaterializationMs,
        providerPeerId,
        providerPublicKey,
        snapshotId: snapshotId || null,
        head: head || null,
        cursor: scan.cursor || null,
        nextCursor: nextCursor || null,
        highWaterMark: highWaterMark || null,
        queryProfile,
        chunkHash: chunkHash || null,
        syncProtocol: scan.syncProtocol || null,
        syncFilter: syncFilter ?? null,
        verifiedChunks,
        lastSyncedAt: new Date().toISOString(),
        error: null,
      });
      postProgress(id, progress, currentStats);

      if (offset >= totalRows || scan.results.length === 0) break;
    }

    const materializationStartedAt = Date.now();
    await currentStore.flush(request.standardId);
    flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
    currentStats = await currentStore.getStats({ includeCachedBytes: true });
    localRows = localRowsForStandard(currentStats, request.standardId);
    cachedBytes = cachedBytesForStandard(currentStats, request.standardId);
    progress = progressFor(progress, {
      status: localRows >= totalRows ? 'synced' : 'idle',
      syncedRows: localRows,
      totalRows,
      localRows,
      cachedBytes,
      pinnedBytes: cachedBytes,
      ...downloadProgressPatch(syncStartedAtMs, downloadedBytes, measuredWireSpeedBytesPerSecond),
      manifestDiscoveryMs,
      networkTransferMs,
      verificationMs,
      flatSqlMaterializationMs,
      providerPeerId,
      providerPublicKey,
      snapshotId: snapshotId || null,
      head: head || null,
      nextCursor: nextCursor || null,
      highWaterMark: highWaterMark || null,
      queryProfile,
      syncFilter: syncFilter ?? null,
      verifiedChunks,
      lastSyncedAt: new Date().toISOString(),
      error: null,
    });
    postProgress(id, progress, currentStats);
    return { progress, stats: currentStats };
  } catch (error) {
    await currentStore.flush(request.standardId).catch(() => undefined);
    currentStats = await Promise.resolve(currentStore.getStats({ includeCachedBytes: true })).catch(() => currentStats);
    progress = progressFor(progress, {
      status: 'error',
      error: error instanceof Error ? error.message : 'Schema sync failed',
      lastSyncedAt: new Date().toISOString(),
    });
    postProgress(id, progress, currentStats);
    return { progress, stats: currentStats };
  }
}

async function syncPublishedSegments(options: {
  id: number;
  request: WorkerSchemaSyncRequest;
  currentStore: LocalFlatSqlStore;
  manifest: FlatSqlSyncManifest;
  segments: FlatSqlSyncManifestSegment[];
  currentStats: LocalFlatSqlStandardStats[];
  progress: WorkerSchemaSyncProgress;
  localRows: number;
  totalRows: number;
  cachedBytes: number;
  verifiedChunks: string[];
  downloadedBytes: number;
  manifestDiscoveryMs: number;
  networkTransferMs: number;
  verificationMs: number;
  flatSqlMaterializationMs: number;
  syncStartedAtMs: number;
  measuredWireSpeedBytesPerSecond: number;
  providerPeerId: string | null;
  providerPublicKey: string | null;
}): Promise<{ progress: WorkerSchemaSyncProgress; stats: LocalFlatSqlStandardStats[] }> {
  let {
    currentStats,
    progress,
    localRows,
    totalRows,
    cachedBytes,
    verifiedChunks,
  } = options;
  let downloadedBytes = options.downloadedBytes;
  let networkTransferMs = options.networkTransferMs;
  let verificationMs = options.verificationMs;
  let flatSqlMaterializationMs = options.flatSqlMaterializationMs;
  const gatewayUrl = options.request.backendConfig.gatewayUrl?.trim();
  if (!gatewayUrl) throw new Error('local IPFS gateway URL is required for published shard sync');
  const manifestTotalRows = options.segments.reduce((sum, segment) => sum + Math.max(0, segment.rowCount), 0);
  totalRows = Math.max(totalRows, manifestTotalRows, options.request.totalRows);

  try {
    await connectIpfsArtifactPeers({
      ipfsApiUrl: options.request.backendConfig.ipfsApiUrl,
      artifactPeerAddrs: options.request.backendConfig.artifactPeerAddrs,
    });
    void connectIpfsArtifactProviders({
      ipfsApiUrl: options.request.backendConfig.ipfsApiUrl,
      cids: options.segments
        .map((segment) => segment.cid)
        .filter((cid): cid is string => Boolean(cid))
        .slice(0, PUBLISHED_SHARD_PROVIDER_DISCOVERY_CID_LIMIT),
    }).catch(() => undefined);
    let recordsSincePersist = 0;
    for await (const fetched of fetchPublishedSegmentsInOrder(options.segments, options.request, gatewayUrl, localRows)) {
      const { segment, streamBytes, cumulativeRows, segmentEnd } = fetched;
      networkTransferMs += fetched.networkTransferMs;
      verificationMs += fetched.verificationMs;
      if (cachedBytes >= options.request.capBytes && localRows < totalRows) {
        const materializationStartedAt = Date.now();
        await options.currentStore.flush(options.request.standardId);
        flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
        currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
        progress = progressFor(progress, {
          status: 'capped',
          syncedRows: localRows,
          totalRows,
          localRows,
          cachedBytes,
          pinnedBytes: cachedBytes,
          ...downloadProgressPatch(options.syncStartedAtMs, downloadedBytes, options.measuredWireSpeedBytesPerSecond),
          manifestDiscoveryMs: options.manifestDiscoveryMs,
          networkTransferMs,
          verificationMs,
          flatSqlMaterializationMs,
          providerPeerId: options.providerPeerId,
          providerPublicKey: options.providerPublicKey,
          cursor: segment.cursor || null,
          nextCursor: segment.nextCursor || null,
          syncFilter: options.request.syncFilter?.trim() || null,
          chunkHash: segment.chunkHash || segment.cid,
          verifiedChunks,
          error: `Storage cap reached at ${options.request.capBytes} bytes`,
        });
        postProgress(options.id, progress, currentStats);
        return { progress, stats: currentStats };
      }

      downloadedBytes += streamBytes.byteLength;
      const resumeRecordOffset = Math.max(0, localRows - cumulativeRows);
      let materializationStartedAt = Date.now();
      const ingestedRows = await options.currentStore.ingestFlatBufferStream(options.request.standardId, streamBytes, {
        source: options.request.source,
        persist: false,
        skipRecords: resumeRecordOffset,
        recordKeyPrefix: `published:${segment.cid}`,
        recordKeyOffset: resumeRecordOffset,
        pinLedgerEntries: [pinLedgerEntryForPublishedSegment({
          request: options.request,
          manifest: options.manifest,
          segment,
          streamBytes,
          providerPeerId: options.providerPeerId,
          providerPublicKey: options.providerPublicKey,
        })],
      });
      flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
      recordsSincePersist += ingestedRows;
      localRows += ingestedRows;
      const chunkHash = segment.chunkHash || segment.cid;
      if (chunkHash && !verifiedChunks.includes(chunkHash)) {
        verifiedChunks = [...verifiedChunks, chunkHash].slice(-256);
      }
      if (recordsSincePersist >= options.request.persistRecordInterval || segmentEnd >= totalRows) {
        materializationStartedAt = Date.now();
        await options.currentStore.flush(options.request.standardId);
        flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
        recordsSincePersist = 0;
        currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
        localRows = localRowsForStandard(currentStats, options.request.standardId);
        cachedBytes = cachedBytesForStandard(currentStats, options.request.standardId);
      }
      progress = progressFor(progress, {
        status: localRows >= totalRows ? 'synced' : 'syncing',
        syncedRows: localRows,
        totalRows,
        localRows,
        cachedBytes,
        pinnedBytes: cachedBytes,
        ...downloadProgressPatch(options.syncStartedAtMs, downloadedBytes, options.measuredWireSpeedBytesPerSecond),
        manifestDiscoveryMs: options.manifestDiscoveryMs,
        networkTransferMs,
        verificationMs,
        flatSqlMaterializationMs,
        providerPeerId: options.providerPeerId,
        providerPublicKey: options.providerPublicKey,
        cursor: segment.cursor || null,
        nextCursor: segment.nextCursor || null,
        queryProfile: 'dataset-publication-offset-v1',
        chunkHash,
        syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
        syncFilter: options.request.syncFilter?.trim() || null,
        verifiedChunks,
        lastSyncedAt: new Date().toISOString(),
        error: null,
      });
      postProgress(options.id, progress, currentStats);
    }

    const materializationStartedAt = Date.now();
    await options.currentStore.flush(options.request.standardId);
    flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
    currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
    localRows = localRowsForStandard(currentStats, options.request.standardId);
    cachedBytes = cachedBytesForStandard(currentStats, options.request.standardId);
    progress = progressFor(progress, {
      status: localRows >= totalRows ? 'synced' : 'idle',
      syncedRows: localRows,
      totalRows,
      localRows,
      cachedBytes,
      pinnedBytes: cachedBytes,
      ...downloadProgressPatch(options.syncStartedAtMs, downloadedBytes, options.measuredWireSpeedBytesPerSecond),
      manifestDiscoveryMs: options.manifestDiscoveryMs,
      networkTransferMs,
      verificationMs,
      flatSqlMaterializationMs,
      providerPeerId: options.providerPeerId,
      providerPublicKey: options.providerPublicKey,
      queryProfile: 'dataset-publication-offset-v1',
      syncFilter: options.request.syncFilter?.trim() || null,
      verifiedChunks,
      lastSyncedAt: new Date().toISOString(),
      error: null,
    });
    postProgress(options.id, progress, currentStats);
    return { progress, stats: currentStats };
  } catch (error) {
    await options.currentStore.flush(options.request.standardId).catch(() => undefined);
    currentStats = await Promise.resolve(options.currentStore.getStats({ includeCachedBytes: true })).catch(() => currentStats);
    progress = progressFor(progress, {
      status: 'error',
      error: error instanceof Error ? error.message : 'Published shard sync failed',
      lastSyncedAt: new Date().toISOString(),
    });
    postProgress(options.id, progress, currentStats);
    return { progress, stats: currentStats };
  }
}

async function openPublishedManifest(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
  schema: string,
  syncFilter?: string,
  queryProfile = 'dataset-publication-offset-v1',
): Promise<{ manifest: FlatSqlSyncManifest; segments: FlatSqlSyncManifestSegment[] } | null> {
  if (!backendConfig.gatewayUrl?.trim()) return null;
  if (syncFilter?.trim()) return null;
  if (queryProfile !== 'dataset-publication-offset-v1') return null;
  const manifest = await withRemoteSyncManifestOperation(backendConfig, 'Remote published shard manifest', {
    targetPeerId: '',
    schema,
    op: 'open_manifest',
    queryProfile,
    limit: PUBLISHED_MANIFEST_SYNC_CHUNK_SIZE,
  });
  return {
    manifest,
    segments: manifest.segments.filter((segment) => Boolean(segment.cid)),
  };
}

function normalizeSyncQueryProfile(value: unknown): string {
  return value === 'ordered-offset-v1' || value === 'dataset-publication-offset-v1'
    ? value
    : 'dataset-publication-offset-v1';
}

function pinLedgerEntryForPublishedSegment(options: {
  request: WorkerSchemaSyncRequest;
  manifest: FlatSqlSyncManifest;
  segment: FlatSqlSyncManifestSegment;
  streamBytes: Uint8Array;
  providerPeerId: string | null;
  providerPublicKey: string | null;
}): LocalFlatSqlPinLedgerEntry {
  const now = new Date().toISOString();
  return {
    cid: options.segment.cid ?? '',
    standardId: options.request.standardId,
    schemaName: options.manifest.schema || options.request.schema,
    providerPeerId: options.providerPeerId,
    providerPublicKey: options.providerPublicKey,
    providerId: options.manifest.providerId ?? options.request.backendConfig.providerId ?? null,
    sourceName: options.manifest.sourceName ?? options.request.backendConfig.sourceName ?? null,
    batchId: options.manifest.batchId ?? null,
    queryProfile: options.manifest.queryProfile || 'dataset-publication-offset-v1',
    snapshotId: options.segment.feedHead || options.manifest.snapshotId || options.manifest.head || null,
    head: options.segment.feedHead || options.manifest.head || null,
    highWaterMark: options.manifest.highWaterMark || null,
    byteHash: options.segment.shardSha256 || options.segment.chunkHash || null,
    role: 'shard',
    rowCount: options.segment.rowCount,
    byteCount: options.streamBytes.byteLength,
    verificationState: 'verified',
    verifiedAt: now,
    updatedAt: now,
  };
}

async function* fetchPublishedSegmentsInOrder(
  segments: FlatSqlSyncManifestSegment[],
  request: WorkerSchemaSyncRequest,
  gatewayUrl: string,
  localRows: number,
): AsyncGenerator<{
  segment: FlatSqlSyncManifestSegment;
  streamBytes: Uint8Array;
  cumulativeRows: number;
  segmentEnd: number;
  networkTransferMs: number;
  verificationMs: number;
}> {
  const syncItems: Array<{ segment: FlatSqlSyncManifestSegment; cumulativeRows: number; segmentEnd: number }> = [];
  let cumulativeRows = 0;
  for (const segment of segments) {
    const segmentRows = Math.max(0, segment.rowCount);
    const segmentEnd = cumulativeRows + segmentRows;
    if (segment.cid && segmentEnd > localRows) {
      syncItems.push({ segment, cumulativeRows, segmentEnd });
    }
    cumulativeRows = segmentEnd;
  }

  const inFlight = new Map<number, Promise<{
    segment: FlatSqlSyncManifestSegment;
    streamBytes: Uint8Array;
    cumulativeRows: number;
    segmentEnd: number;
    networkTransferMs: number;
    verificationMs: number;
  }>>();
  let nextToSchedule = 0;
  const schedule = (): void => {
    while (nextToSchedule < syncItems.length && inFlight.size < PUBLISHED_SHARD_FETCH_CONCURRENCY) {
      const index = nextToSchedule;
      nextToSchedule += 1;
      const item = syncItems[index];
      if (!item) continue;
      inFlight.set(index, timedFlatBufferStreamFromPublishedFlatSqlSegment({
        schema: request.schema,
        providerPeerId: request.backendConfig.targetPeerId,
        cid: item.segment.cid as string,
        shardSha256: item.segment.shardSha256,
        fetchCidBytes: (cid) => fetchCidBytesFromGateway(gatewayUrl, cid),
      }).then((result) => ({
        ...item,
        streamBytes: result.streamBytes,
        networkTransferMs: result.networkTransferMs,
        verificationMs: result.verificationMs,
      })));
    }
  };

  for (let index = 0; index < syncItems.length; index += 1) {
    schedule();
    const next = inFlight.get(index);
    if (!next) throw new Error('published shard prefetch queue lost ordering');
    const fetched = await next;
    inFlight.delete(index);
    schedule();
    yield fetched;
  }
}

function progressFor(current: WorkerSchemaSyncProgress, patch: Partial<WorkerSchemaSyncProgress>): WorkerSchemaSyncProgress {
  const next = {
    ...current,
    ...patch,
    syncedRows: Math.max(patch.syncedRows ?? current.syncedRows, patch.localRows ?? current.localRows),
  };
  const rowCounts = syncRowCountSummary({
    localRows: next.localRows,
    syncedRows: next.syncedRows,
    pinnedRows: next.pinnedRows,
    remoteRows: next.totalRows,
    totalRows: next.totalRows,
  });
  return {
    ...next,
    syncedRows: rowCounts.syncedRows,
    pinnedRows: rowCounts.pinnedRows,
    missingRows: rowCounts.missingRows,
    totalRows: rowCounts.totalRows,
  };
}

function downloadProgressPatch(
  startedAtMs: number,
  downloadedBytes: number,
  measuredWireSpeedBytesPerSecond: number,
): Pick<WorkerSchemaSyncProgress, 'downloadedBytes' | 'downloadSpeedBytesPerSecond' | 'measuredWireSpeedBytesPerSecond' | 'wireSpeedUtilization' | 'wireSpeedTarget' | 'wireSpeedTargetMet'> {
  const elapsedSeconds = Math.max(0.001, (Date.now() - startedAtMs) / 1000);
  const downloadSpeedBytesPerSecond = Math.max(0, Math.floor(downloadedBytes / elapsedSeconds));
  const wireSpeedUtilization = measuredWireSpeedUtilization(downloadSpeedBytesPerSecond, measuredWireSpeedBytesPerSecond);
  return {
    downloadedBytes,
    downloadSpeedBytesPerSecond,
    measuredWireSpeedBytesPerSecond,
    wireSpeedUtilization,
    wireSpeedTarget: DEFAULT_WIRE_SPEED_TARGET,
    wireSpeedTargetMet: wireSpeedUtilization == null
      ? null
      : meetsWireSpeedTarget(downloadSpeedBytesPerSecond, measuredWireSpeedBytesPerSecond),
  };
}

function normalizedPositiveNumber(value: unknown): number {
  const numeric = Number(value);
  return Number.isFinite(numeric) && numeric > 0 ? Math.floor(numeric) : 0;
}

function localRowsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
  const stat = stats.find((entry) => entry.standardId === standardId);
  return Math.max(stat?.ingestedRecordCount ?? 0, stat?.recordCount ?? 0);
}

function cachedBytesForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
  return stats.find((entry) => entry.standardId === standardId)?.cachedBytes ?? 0;
}

async function withRemoteSyncTimeout<T>(operation: Promise<T>, label: string): Promise<T> {
  try {
    return await withTimeout(
      operation,
      REMOTE_SYNC_OPERATION_TIMEOUT_MS,
      `${label} timed out after ${REMOTE_SYNC_OPERATION_TIMEOUT_MS / 1000}s`,
    );
  } catch (error) {
    if (error instanceof TimeoutError) {
      await syncBackendCache.destroy();
    }
    throw error;
  }
}

async function withRemoteBackendOperation<T>(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
  label: string,
  operation: (backend: SdnBackend) => Promise<T>,
): Promise<T> {
  return await retryRemoteSyncOperation({
    label,
    attempts: REMOTE_SYNC_RETRY_ATTEMPTS,
    retryDelayMs: REMOTE_SYNC_RETRY_DELAY_MS,
    reset: () => syncBackendCache.destroy(),
    run: () => withRemoteSyncTimeout(operation(syncBackendCache.backendFor(backendConfig)), label),
  });
}

async function withRemoteSyncChunkOperation(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
  label: string,
  query: Parameters<Libp2pFlatSqlSyncBackendCache['readFlatSqlSyncChunk']>[1],
) {
  return await retryRemoteSyncOperation({
    label,
    attempts: REMOTE_SYNC_RETRY_ATTEMPTS,
    retryDelayMs: REMOTE_SYNC_RETRY_DELAY_MS,
    reset: () => syncBackendCache.destroy(),
    run: () => withRemoteSyncTimeout(syncBackendCache.readFlatSqlSyncChunk(backendConfig, query), label),
  });
}

async function withRemoteSyncManifestOperation(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
  label: string,
  query: Parameters<Libp2pFlatSqlSyncBackendCache['openFlatSqlSyncManifest']>[1],
) {
  return await retryRemoteSyncOperation({
    label,
    attempts: REMOTE_SYNC_RETRY_ATTEMPTS,
    retryDelayMs: REMOTE_SYNC_RETRY_DELAY_MS,
    reset: () => syncBackendCache.destroy(),
    run: () => withRemoteSyncTimeout(syncBackendCache.openFlatSqlSyncManifest(backendConfig, query), label),
  });
}

async function measuredWireSpeedBaselineBytesPerSecond(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
): Promise<number> {
  const configured = normalizedPositiveNumber(backendConfig.measuredWireSpeedBytesPerSecond);
  if (configured > 0) return configured;
  try {
    const result = await withRemoteSyncTimeout(
      syncBackendCache.measureWireSpeed(backendConfig, { probeBytes: WIRE_SPEED_PROBE_BYTES }),
      'Remote wire-speed probe',
    );
    return normalizedPositiveNumber(result.bytesPerSecond);
  } catch {
    return 0;
  }
}

function postRemotePageSuccess(id: number, result: WorkerRemotePageResult): void {
  const prepared = prepareRecordsForTransfer(result.records);
  postSuccess(id, { ...result, records: prepared.records }, undefined, prepared.transferables);
}

function prepareRecordsForTransfer(records: RawDataRecord[]): { records: RawDataRecord[]; transferables: Transferable[] } {
  const transferables: Transferable[] = [];
  const preparedRecords = records.map((record) => {
    if (!(record.dataBytes instanceof Uint8Array)) return record;
    const bytes = record.dataBytes;
    if (!(bytes.buffer instanceof ArrayBuffer)) return record;
    const transferableBytes = bytes.byteOffset === 0 && bytes.byteLength === bytes.buffer.byteLength
      ? bytes
      : bytes.slice();
    transferables.push(transferableBytes.buffer);
    return transferableBytes === bytes ? record : { ...record, dataBytes: transferableBytes };
  });
  return { records: preparedRecords, transferables };
}

function postSuccess(id: number, data?: unknown, stats?: LocalFlatSqlStandardStats[], transferables: Transferable[] = []): void {
  const response: WorkerResponse = stats ? { id, ok: true, data, stats } : { id, ok: true, data };
  workerGlobal.postMessage(response, transferables);
}

function postFailure(id: number, error: string): void {
  const response: WorkerResponse = { id, ok: false, error };
  workerGlobal.postMessage(response);
}

function postProgress(id: number, progress: WorkerSchemaSyncProgress, stats: LocalFlatSqlStandardStats[]): void {
  const response: WorkerResponse = { id, type: 'syncProgress', progress, stats };
  workerGlobal.postMessage(response);
}
