import {
  createLocalFlatSqlStore,
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
  flatBufferStreamFromPublishedFlatSqlSegment,
} from './published-flatbuffer-shard';
import { retryRemoteSyncOperation } from './remote-sync-retry';
import { SerialTaskQueue } from './serial-task-queue';
import type { DataSummary, RawDataRecord, SdnBackend } from './sdn-backend';
import type { FlatSqlSyncManifestSegment } from '../../flatsql-sync';
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
  | { id: number; type: 'query'; sql: string; standardId?: string }
  | { id: number; type: 'getStats'; options?: LocalFlatSqlStatsOptions }
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
        const result = await requireStore().query(request.sql, request.standardId);
        postSuccess(request.id, result);
        return;
      }
      case 'getStats':
        postSuccess(request.id, await stats(request.options));
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
  let queryProfile = canResume ? request.initialProgress.queryProfile ?? 'ordered-offset-v1' : 'ordered-offset-v1';
  let verifiedChunks = canResume ? request.initialProgress.verifiedChunks.slice(-256) : [];
  let snapshotVerifiedForSession = false;
  const publishedSegments = await openPublishedManifestSegments(request.backendConfig, request.schema, request.pageSize).catch(() => []);

  const providerPeerId = request.backendConfig.targetPeerId || null;
  const providerPublicKey = request.backendConfig.publicKey || request.backendConfig.targetPeerId || null;
  let progress = progressFor(request.initialProgress, {
    status: 'syncing',
    syncedRows: localRows,
    totalRows,
    localRows,
    cachedBytes,
    pinnedBytes: cachedBytes,
    providerPeerId,
    providerPublicKey,
    snapshotId: snapshotId || null,
    head: head || null,
    highWaterMark: highWaterMark || null,
    nextCursor: nextCursor || null,
    queryProfile,
    verifiedChunks,
    error: null,
  });
  postProgress(id, progress, currentStats);

  try {
    if (publishedSegments.length > 0) {
      return await syncPublishedSegments({
        id,
        request,
        currentStore,
        segments: publishedSegments,
        currentStats,
        progress,
        localRows,
        totalRows,
        cachedBytes,
        verifiedChunks,
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
          providerPeerId,
          providerPublicKey,
          snapshotId: snapshotId || null,
          head: head || null,
          nextCursor: nextCursor || null,
          queryProfile,
          verifiedChunks,
          error: `Storage cap reached at ${request.capBytes} bytes`,
        });
        postProgress(id, progress, currentStats);
        return { progress, stats: currentStats };
      }

      let chunk = await withRemoteSyncChunkOperation(
        request.backendConfig,
        'Remote sync chunk',
        {
          targetPeerId: '',
          schema: request.schema,
          op: 'read_chunk',
          limit: request.pageSize,
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
            : { offset }),
        },
      );

      let scan = dataScanResultFromChunk(chunk);

      if (nextCursor && head && scan.head && scan.head !== head) {
        nextCursor = '';
        offset = localRows;
        snapshotVerifiedForSession = false;
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
          },
        );
        scan = dataScanResultFromChunk(chunk);
      }

      snapshotId = scan.snapshotId || snapshotId;
      head = scan.head || head;
      highWaterMark = scan.highWaterMark || highWaterMark;
      snapshotVerifiedForSession = true;
      queryProfile = scan.queryProfile || queryProfile;
      totalRows = Math.max(totalRows, scan.totalCount ?? 0, offset + scan.results.length);
      if (scan.results.length === 0) break;

      const ingestedRows = await currentStore.ingestFlatBufferStream(request.standardId, chunk.recordStream, {
        source: request.source,
        persist: false,
        recordKeys: flatSqlRecordKeys(scan.results),
      });
      recordsSincePersist += ingestedRows;
      const chunkHash = scan.chunkHash || scan.scanHash;
      if (chunkHash && !verifiedChunks.includes(chunkHash)) {
        verifiedChunks = [...verifiedChunks, chunkHash].slice(-256);
      }
      if (recordsSincePersist >= request.persistRecordInterval || offset + scan.results.length >= totalRows) {
        await currentStore.flush(request.standardId);
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
        verifiedChunks,
        lastSyncedAt: new Date().toISOString(),
        error: null,
      });
      postProgress(id, progress, currentStats);

      if (!nextCursor || offset >= totalRows || scan.results.length === 0) break;
    }

    await currentStore.flush(request.standardId);
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
      providerPeerId,
      providerPublicKey,
      snapshotId: snapshotId || null,
      head: head || null,
      nextCursor: nextCursor || null,
      highWaterMark: highWaterMark || null,
      queryProfile,
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
  segments: FlatSqlSyncManifestSegment[];
  currentStats: LocalFlatSqlStandardStats[];
  progress: WorkerSchemaSyncProgress;
  localRows: number;
  totalRows: number;
  cachedBytes: number;
  verifiedChunks: string[];
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
  const gatewayUrl = options.request.backendConfig.gatewayUrl?.trim();
  if (!gatewayUrl) throw new Error('local IPFS gateway URL is required for published shard sync');
  const manifestTotalRows = options.segments.reduce((sum, segment) => sum + Math.max(0, segment.rowCount), 0);
  totalRows = Math.max(totalRows, manifestTotalRows, options.request.totalRows);
  let cumulativeRows = 0;

  try {
    for (const segment of options.segments) {
      const segmentRows = Math.max(0, segment.rowCount);
      const segmentEnd = cumulativeRows + segmentRows;
      if (segmentEnd <= localRows) {
        cumulativeRows = segmentEnd;
        continue;
      }
      if (!segment.cid) {
        cumulativeRows = segmentEnd;
        continue;
      }
      if (cachedBytes >= options.request.capBytes && localRows < totalRows) {
        await options.currentStore.flush(options.request.standardId);
        currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
        progress = progressFor(progress, {
          status: 'capped',
          syncedRows: localRows,
          totalRows,
          localRows,
          cachedBytes,
          pinnedBytes: cachedBytes,
          providerPeerId: options.providerPeerId,
          providerPublicKey: options.providerPublicKey,
          cursor: segment.cursor || null,
          nextCursor: segment.nextCursor || null,
          chunkHash: segment.chunkHash || segment.cid,
          verifiedChunks,
          error: `Storage cap reached at ${options.request.capBytes} bytes`,
        });
        postProgress(options.id, progress, currentStats);
        return { progress, stats: currentStats };
      }

      const streamBytes = await flatBufferStreamFromPublishedFlatSqlSegment({
        schema: options.request.schema,
        providerPeerId: options.request.backendConfig.targetPeerId,
        cid: segment.cid,
        shardSha256: segment.shardSha256,
        fetchCidBytes: (cid) => fetchCidBytesFromGateway(gatewayUrl, cid),
      });
      const resumeRecordOffset = Math.max(0, localRows - cumulativeRows);
      await options.currentStore.ingestFlatBufferStream(options.request.standardId, streamBytes, {
        source: options.request.source,
        persist: false,
        skipRecords: resumeRecordOffset,
        recordKeyPrefix: `published:${segment.cid}`,
        recordKeyOffset: resumeRecordOffset,
      });
      await options.currentStore.flush(options.request.standardId);
      currentStats = await options.currentStore.getStats({ includeCachedBytes: true });
      localRows = localRowsForStandard(currentStats, options.request.standardId);
      cachedBytes = cachedBytesForStandard(currentStats, options.request.standardId);
      const chunkHash = segment.chunkHash || segment.cid;
      if (chunkHash && !verifiedChunks.includes(chunkHash)) {
        verifiedChunks = [...verifiedChunks, chunkHash].slice(-256);
      }
      progress = progressFor(progress, {
        status: localRows >= totalRows ? 'synced' : 'syncing',
        syncedRows: localRows,
        totalRows,
        localRows,
        cachedBytes,
        pinnedBytes: cachedBytes,
        providerPeerId: options.providerPeerId,
        providerPublicKey: options.providerPublicKey,
        cursor: segment.cursor || null,
        nextCursor: segment.nextCursor || null,
        queryProfile: 'dataset-publication-offset-v1',
        chunkHash,
        syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
        verifiedChunks,
        lastSyncedAt: new Date().toISOString(),
        error: null,
      });
      postProgress(options.id, progress, currentStats);
      cumulativeRows = segmentEnd;
    }

    await options.currentStore.flush(options.request.standardId);
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
      providerPeerId: options.providerPeerId,
      providerPublicKey: options.providerPublicKey,
      queryProfile: 'dataset-publication-offset-v1',
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

async function openPublishedManifestSegments(
  backendConfig: WorkerFlatSqlSyncBackendConfig,
  schema: string,
  pageSize: number,
): Promise<FlatSqlSyncManifestSegment[]> {
  if (!backendConfig.gatewayUrl?.trim()) return [];
  const manifest = await withRemoteSyncManifestOperation(backendConfig, 'Remote published shard manifest', {
    targetPeerId: '',
    schema,
    op: 'open_manifest',
    queryProfile: 'dataset-publication-offset-v1',
    limit: pageSize,
  });
  return manifest.segments.filter((segment) => Boolean(segment.cid));
}

function progressFor(current: WorkerSchemaSyncProgress, patch: Partial<WorkerSchemaSyncProgress>): WorkerSchemaSyncProgress {
  return {
    ...current,
    ...patch,
    syncedRows: Math.max(patch.syncedRows ?? current.syncedRows, patch.localRows ?? current.localRows),
  };
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
