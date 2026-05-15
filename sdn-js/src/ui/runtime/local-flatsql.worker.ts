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
  timedFlatBufferStreamFromPublishedFlatSqlSegment,
} from './published-flatbuffer-shard';
import {
  assertPublishedSegmentMaterializedRowCount,
  completedPublishedRowsForSegments,
  completedPublishedSegmentCids,
  pendingPublishedSegmentItems,
} from './published-segment-sync';
import { retryRemoteSyncOperation } from './remote-sync-retry';
import { SerialTaskQueue } from './serial-task-queue';
import { syncRowCountSummary } from './sync-progress';
import { DEFAULT_WIRE_SPEED_TARGET, measuredWireSpeedUtilization, meetsWireSpeedTarget } from './sync-throughput';
import { encodeWorkerSchemaSyncProgressFlatBuffer } from './worker-sync-status-flatbuffer';
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
  | { id: number; type: 'syncProgress'; progressBytes: Uint8Array; stats: LocalFlatSqlStandardStats[] };

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
const PUBLISHED_SHARD_FETCH_CONCURRENCY = 32;
const PUBLISHED_SHARD_FETCH_ATTEMPTS = 3;
const PUBLISHED_SHARD_FETCH_RETRY_DELAY_MS = 750;
const PUBLISHED_SHARD_FETCH_TIMEOUT_MS = 120_000;
const PUBLISHED_SHARD_RANGE_BYTES = 16 * 1024 * 1024;
const PUBLISHED_SHARD_RANGE_CONCURRENCY = 1;
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
        postSyncSuccess(request.id, await schemaSyncQueue.enqueue(() => syncSchemaInWorker(request.id, request.request)));
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

  const providerPeerId = request.backendConfig.targetPeerId || null;
  const providerPublicKey = request.backendConfig.publicKey || request.backendConfig.targetPeerId || null;
  let progress = progressFor(request.initialProgress, {
    status: 'syncing',
    syncedRows: localRows,
    totalRows,
    localRows,
    cachedBytes,
    pinnedBytes: cachedBytes,
    ...downloadProgressPatch(downloadedBytes, networkTransferMs, measuredWireSpeedBytesPerSecond),
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
      await syncBackendCache.releaseControlClient(request.backendConfig);
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
          ...downloadProgressPatch(downloadedBytes, networkTransferMs, measuredWireSpeedBytesPerSecond),
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
        ...downloadProgressPatch(downloadedBytes, networkTransferMs, measuredWireSpeedBytesPerSecond),
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
      ...downloadProgressPatch(downloadedBytes, networkTransferMs, measuredWireSpeedBytesPerSecond),
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
  const manifestTotalRows = options.segments.reduce((sum, segment) => sum + Math.max(0, segment.rowCount), 0);
  totalRows = manifestTotalRows > 0 ? manifestTotalRows : Math.max(localRows, manifestTotalRows);
  const completedEntries = await options.currentStore.listPinLedgerEntries({
    standardId: options.request.standardId,
    providerPeerId: options.providerPeerId,
    providerPublicKey: options.providerPublicKey,
    providerId: options.manifest.providerId ?? options.request.backendConfig.providerId ?? null,
    sourceName: options.manifest.sourceName ?? options.request.backendConfig.sourceName ?? null,
    batchId: options.manifest.batchId ?? null,
    queryProfile: options.manifest.queryProfile || 'dataset-publication-offset-v1',
    role: 'shard',
    verificationState: 'verified',
  });
  const completedSegmentCids = completedPublishedSegmentCids(completedEntries);
  let completedRows = completedPublishedRowsForSegments(options.segments, completedSegmentCids);
  progress = progressFor(progress, {
    syncedRows: completedRows,
    localRows: completedRows,
    pinnedRows: completedRows,
    totalRows,
  });
  postProgress(options.id, progress, currentStats);

  try {
    for await (const fetched of fetchPublishedSegmentsInOrder(options.segments, options.request, completedSegmentCids)) {
      const { segment, streamBytes } = fetched;
      networkTransferMs += fetched.networkTransferMs;
      downloadedBytes += streamBytes.byteLength;
      verificationMs += fetched.verificationMs;
      if (cachedBytes >= options.request.capBytes && completedRows < totalRows) {
        const materializationStartedAt = Date.now();
        await options.currentStore.flush(options.request.standardId);
        flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
        currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
        progress = progressFor(progress, {
          status: 'capped',
          syncedRows: completedRows,
          totalRows,
          localRows: completedRows,
          cachedBytes,
          pinnedBytes: cachedBytes,
          pinnedRows: completedRows,
          ...downloadProgressPatch(downloadedBytes, networkTransferMs, options.measuredWireSpeedBytesPerSecond),
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

      let materializationStartedAt = Date.now();
      const ingestedRows = await options.currentStore.ingestFlatBufferStream(options.request.standardId, streamBytes, {
        source: options.request.source,
        persist: false,
        skipRecords: fetched.skipRecords,
        recordKeyPrefix: `published:${segment.cid}`,
        recordKeyOffset: fetched.cumulativeRows + fetched.skipRecords,
      });
      flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
      assertPublishedSegmentMaterializedRowCount({
        cid: segment.cid,
        standardId: options.request.standardId,
        expectedRows: segment.rowCount,
        materializedRows: ingestedRows,
      });
      const chunkHash = segment.chunkHash || segment.cid;
      if (chunkHash && !verifiedChunks.includes(chunkHash)) {
        verifiedChunks = [...verifiedChunks, chunkHash].slice(-256);
      }
      materializationStartedAt = Date.now();
      await options.currentStore.flush(options.request.standardId);
      flatSqlMaterializationMs += Math.max(0, Date.now() - materializationStartedAt);
      await options.currentStore.recordPinLedgerEntries([pinLedgerEntryForPublishedSegment({
        request: options.request,
        manifest: options.manifest,
        segment,
        streamBytes,
        providerPeerId: options.providerPeerId,
        providerPublicKey: options.providerPublicKey,
      })]);
      if (segment.cid) completedSegmentCids.add(segment.cid);
      completedRows = completedPublishedRowsForSegments(options.segments, completedSegmentCids);
      currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
      localRows = localRowsForStandard(currentStats, options.request.standardId);
      cachedBytes = cachedBytesForStandard(currentStats, options.request.standardId);
      progress = progressFor(progress, {
        status: completedRows >= totalRows ? 'synced' : 'syncing',
        syncedRows: completedRows,
        totalRows,
        localRows: completedRows,
        cachedBytes,
        pinnedBytes: cachedBytes,
        pinnedRows: completedRows,
        ...downloadProgressPatch(downloadedBytes, networkTransferMs, options.measuredWireSpeedBytesPerSecond),
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
    completedRows = completedPublishedRowsForSegments(options.segments, completedSegmentCids);
    progress = progressFor(progress, {
      status: completedRows >= totalRows ? 'synced' : 'idle',
      syncedRows: completedRows,
      totalRows,
      localRows: completedRows,
      cachedBytes,
      pinnedBytes: cachedBytes,
      pinnedRows: completedRows,
      ...downloadProgressPatch(downloadedBytes, networkTransferMs, options.measuredWireSpeedBytesPerSecond),
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
    materializedAt: now,
    verifiedAt: now,
    updatedAt: now,
  };
}

async function* fetchPublishedSegmentsInOrder(
  segments: FlatSqlSyncManifestSegment[],
  request: WorkerSchemaSyncRequest,
  completedSegmentCids: ReadonlySet<string>,
): AsyncGenerator<{
  segment: FlatSqlSyncManifestSegment;
  streamBytes: Uint8Array;
  cumulativeRows: number;
  segmentEnd: number;
  skipRecords: 0;
  networkTransferMs: number;
  verificationMs: number;
}> {
  type PublishedSegmentFetchSuccess = {
    ok: true;
    segment: FlatSqlSyncManifestSegment;
    streamBytes: Uint8Array;
    cumulativeRows: number;
    segmentEnd: number;
    skipRecords: 0;
    networkTransferMs: number;
    verificationMs: number;
  };
  type PublishedSegmentFetchFailure = {
    ok: false;
    segment: FlatSqlSyncManifestSegment;
    cumulativeRows: number;
    segmentEnd: number;
    skipRecords: 0;
    error: unknown;
  };
  type PublishedSegmentFetchResult = PublishedSegmentFetchSuccess | PublishedSegmentFetchFailure;
  const syncItems = pendingPublishedSegmentItems(segments, completedSegmentCids);

  const inFlight = new Map<number, Promise<PublishedSegmentFetchResult>>();
  let nextToSchedule = 0;
  const schedule = (): void => {
    while (nextToSchedule < syncItems.length && inFlight.size < PUBLISHED_SHARD_FETCH_CONCURRENCY) {
      const index = nextToSchedule;
      nextToSchedule += 1;
      const item = syncItems[index];
      if (!item) continue;
      inFlight.set(index, fetchPublishedSegment({
        request,
        item,
      }));
    }
  };

  for (let index = 0; index < syncItems.length; index += 1) {
    schedule();
    const next = inFlight.get(index);
    if (!next) throw new Error('published shard prefetch queue lost ordering');
    const fetched = await next;
    inFlight.delete(index);
    schedule();
    if (!fetched.ok) {
      throw new Error(`published shard fetch failed for ${fetched.segment.cid ?? `segment ${index}`}: ${formatWorkerError(fetched.error)}`);
    }
    yield fetched;
  }
}

async function fetchPublishedSegment(options: {
  request: WorkerSchemaSyncRequest;
  item: { segment: FlatSqlSyncManifestSegment; cumulativeRows: number; segmentEnd: number; skipRecords: 0 };
}): Promise<
  {
    ok: true;
    segment: FlatSqlSyncManifestSegment;
    streamBytes: Uint8Array;
    cumulativeRows: number;
    segmentEnd: number;
    skipRecords: 0;
    networkTransferMs: number;
    verificationMs: number;
  }
  | {
    ok: false;
    segment: FlatSqlSyncManifestSegment;
    cumulativeRows: number;
    segmentEnd: number;
    skipRecords: 0;
    error: unknown;
  }
> {
  const { item } = options;
  const cid = item.segment.cid;
  if (!cid) {
    return {
      ok: false,
      ...item,
      error: new Error('published shard segment is missing CID'),
    };
  }
  try {
    const result = await timedFlatBufferStreamFromPublishedFlatSqlSegment({
      schema: options.request.schema,
      providerPeerId: options.request.backendConfig.targetPeerId,
      cid,
      shardSha256: item.segment.shardSha256,
      fetchAttempts: PUBLISHED_SHARD_FETCH_ATTEMPTS,
      retryDelayMs: PUBLISHED_SHARD_FETCH_RETRY_DELAY_MS,
      fetchCidBytes: (targetCid) => fetchPublishedShardBytesViaLibp2pRanges({
        request: options.request,
        segment: item.segment,
        cid: targetCid,
      }),
    });
    return {
      ok: true,
      ...item,
      streamBytes: result.streamBytes,
      networkTransferMs: result.networkTransferMs,
      verificationMs: result.verificationMs,
    };
  } catch (error) {
    return {
      ok: false,
      ...item,
      error,
    };
  }
}

async function fetchPublishedShardBytesViaLibp2pRanges(options: {
  request: WorkerSchemaSyncRequest;
  segment: FlatSqlSyncManifestSegment;
  cid: string;
}): Promise<Uint8Array> {
  const backendConfigs = publishedShardBackendConfigsFor(options.request.backendConfig);
  const byteCount = Math.max(0, Math.floor(options.segment.byteCount));
  if (byteCount <= 0) {
    const shard = await withRemotePublishedShardOperation(backendConfigs, `Published shard ${options.cid}`, {
      targetPeerId: '',
      schema: options.request.schema,
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: options.cid,
    }, 0);
    return shard.streamBytes;
  }

  const ranges: Array<{ index: number; offset: number; length: number }> = [];
  for (let offset = 0; offset < byteCount; offset += PUBLISHED_SHARD_RANGE_BYTES) {
    ranges.push({
      index: ranges.length,
      offset,
      length: Math.min(PUBLISHED_SHARD_RANGE_BYTES, byteCount - offset),
    });
  }

  const chunks = new Array<Uint8Array>(ranges.length);
  let nextRange = 0;
  async function rangeWorker(): Promise<void> {
    while (nextRange < ranges.length) {
      const range = ranges[nextRange];
      nextRange += 1;
      if (!range) return;
      const shard = await withRemotePublishedShardOperation(backendConfigs, `Published shard ${options.cid} range ${range.offset}`, {
        targetPeerId: '',
        schema: options.request.schema,
        op: 'read_published_shard',
        queryProfile: 'dataset-publication-offset-v1',
        cid: options.cid,
        byteOffset: range.offset,
        byteLength: range.length,
      }, publishedShardPreferredSourceIndex(options.segment, range.index));
      const headerOffset = shard.header.byteOffset ?? range.offset;
      const headerLength = shard.header.byteLength ?? shard.header.byteCount;
      if (headerOffset !== range.offset || headerLength !== range.length || shard.streamBytes.byteLength !== range.length) {
        throw new Error(`published shard ${options.cid} returned range ${headerOffset}+${headerLength}/${range.offset}+${range.length}`);
      }
      chunks[range.index] = shard.streamBytes;
    }
  }

  const workers = Array.from(
    { length: Math.max(1, Math.min(PUBLISHED_SHARD_RANGE_CONCURRENCY, ranges.length)) },
    () => rangeWorker(),
  );
  await Promise.all(workers);
  return concatUint8Arrays(chunks);
}

function publishedShardPreferredSourceIndex(segment: FlatSqlSyncManifestSegment, rangeIndex: number): number {
  const segmentIndex = Number.isFinite(segment.index) ? Math.max(0, Math.floor(segment.index)) : 0;
  return segmentIndex + Math.max(0, Math.floor(rangeIndex));
}

function concatUint8Arrays(chunks: Uint8Array[]): Uint8Array {
  const length = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function formatWorkerError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
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
  downloadedBytes: number,
  networkTransferMs: number,
  measuredWireSpeedBytesPerSecond: number,
): Pick<WorkerSchemaSyncProgress, 'downloadedBytes' | 'downloadSpeedBytesPerSecond' | 'measuredWireSpeedBytesPerSecond' | 'wireSpeedUtilization' | 'wireSpeedTarget' | 'wireSpeedTargetMet'> {
  const networkSeconds = Math.max(0, Math.floor(networkTransferMs)) / 1000;
  const downloadSpeedBytesPerSecond = networkSeconds > 0
    ? Math.max(0, Math.floor(downloadedBytes / networkSeconds))
    : 0;
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
  return Math.max(stat?.recordCount ?? 0, 0);
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

async function withRemotePublishedShardOperation(
  backendConfigs: WorkerFlatSqlSyncBackendConfig[],
  label: string,
  query: Parameters<Libp2pFlatSqlSyncBackendCache['readFlatSqlPublishedShard']>[1],
  preferredSourceIndex: number,
) {
  return await retryRemoteSyncOperation({
    label,
    attempts: PUBLISHED_SHARD_FETCH_ATTEMPTS,
    retryDelayMs: PUBLISHED_SHARD_FETCH_RETRY_DELAY_MS,
    reset: () => undefined,
    run: () => withTimeout(
      syncBackendCache.readFlatSqlPublishedShardFromSources(backendConfigs, query, preferredSourceIndex),
      PUBLISHED_SHARD_FETCH_TIMEOUT_MS,
      `${label} timed out after ${PUBLISHED_SHARD_FETCH_TIMEOUT_MS / 1000}s`,
    ),
  });
}

function publishedShardBackendConfigsFor(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
): WorkerFlatSqlSyncBackendConfig[] {
  const sources = [
    ...(backendConfig.publishedShardSources ?? []).map(stripPublishedShardSources),
    stripPublishedShardSources(backendConfig),
  ];
  const deduped: WorkerFlatSqlSyncBackendConfig[] = [];
  const seen = new Set<string>();
  for (const source of sources) {
    const targetPeerId = source.targetPeerId?.trim();
    const candidateAddrs = Array.from(new Set((source.candidateAddrs ?? []).map((addr) => addr.trim()).filter(Boolean))).sort();
    if (!targetPeerId || candidateAddrs.length === 0) continue;
    const normalized = {
      ...source,
      targetPeerId,
      candidateAddrs,
    };
    const key = JSON.stringify({
      targetPeerId,
      candidateAddrs,
      datastoreKey: normalized.datastoreKey ?? null,
      providerId: normalized.providerId ?? null,
      sourceName: normalized.sourceName ?? null,
    });
    if (seen.has(key)) continue;
    seen.add(key);
    deduped.push(normalized);
  }
  return deduped.length > 0 ? deduped : [stripPublishedShardSources(backendConfig)];
}

function stripPublishedShardSources(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
): WorkerFlatSqlSyncBackendConfig {
  const {
    publishedShardSources: _publishedShardSources,
    ...rest
  } = backendConfig;
  return rest;
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

function postSyncSuccess(
  id: number,
  result: { progress: WorkerSchemaSyncProgress; stats: LocalFlatSqlStandardStats[] },
): void {
  const progressBytes = encodeWorkerSchemaSyncProgressFlatBuffer(result.progress);
  const response: WorkerResponse = {
    id,
    ok: true,
    data: {
      progressBytes,
      stats: result.stats,
    },
    stats: result.stats,
  };
  workerGlobal.postMessage(response, [progressBytes.buffer]);
}

function postProgress(id: number, progress: WorkerSchemaSyncProgress, stats: LocalFlatSqlStandardStats[]): void {
  const progressBytes = encodeWorkerSchemaSyncProgressFlatBuffer(progress);
  const response: WorkerResponse = { id, type: 'syncProgress', progressBytes, stats };
  workerGlobal.postMessage(response, [progressBytes.buffer]);
}
