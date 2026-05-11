import {
  createLocalFlatSqlStore,
  type LocalFlatSqlStandardStats,
  type LocalFlatSqlStatsOptions,
  type LocalFlatSqlStore,
  type LocalFlatSqlStoreOptions,
} from './local-flatsql';
import { TimeoutError, withTimeout } from './async-timeout';
import { Libp2pFlatSqlSyncBackendCache } from './libp2p-sync-backend-cache';
import type { DataSummary, RawDataRecord } from './sdn-backend';
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
const REMOTE_SYNC_OPERATION_TIMEOUT_MS = 60_000;
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
        postSuccess(request.id, await syncSchemaInWorker(request.id, request.request));
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
  const summary = await withRemoteSyncTimeout(
    syncBackendCache.backendFor(backendConfig).getDataSummary(),
    'Remote data summary',
  );
  if (!summary.ok) throw new Error(summary.capability.reason ?? 'Remote data summary failed');
  return summary.data;
}

async function queryRemotePage(request: WorkerRemotePageRequest): Promise<WorkerRemotePageResult> {
  const currentStore = requireStore();
  const backend = syncBackendCache.backendFor(request.backendConfig);
  const scanResult = await withRemoteSyncTimeout(backend.scanRawData(request.query), 'Remote page scan');
  if (!scanResult.ok) throw new Error(scanResult.capability.reason ?? 'Remote scan failed');
  const scan = scanResult.data;
  let records: RawDataRecord[] = [];
  if (scan?.results.length) {
    const streamResult = await withRemoteSyncTimeout(
      backend.streamRawData({
        schema: scan.schema,
        scanHash: scan.scanHash,
        chunkHash: scan.chunkHash || scan.scanHash,
        snapshotId: scan.snapshotId,
        head: scan.head,
        cursor: scan.cursor,
        nextCursor: scan.nextCursor,
        totalCount: scan.totalCount,
        highWaterMark: scan.highWaterMark,
        queryProfile: scan.queryProfile,
        records: scan.results,
      }),
      'Remote page stream',
    );
    if (!streamResult.ok) throw new Error(streamResult.capability.reason ?? 'Remote FlatBuffer stream failed');
    records = streamResult.data ?? [];
    await currentStore.ingestRecords(request.standardId, records, {
      source: request.source,
      persist: true,
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
  const backend = syncBackendCache.backendFor(request.backendConfig);
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
  let queryProfile = canResume ? request.initialProgress.queryProfile ?? 'ordered-offset-v1' : 'ordered-offset-v1';
  let verifiedChunks = canResume ? request.initialProgress.verifiedChunks.slice(-256) : [];

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
    nextCursor: nextCursor || null,
    queryProfile,
    verifiedChunks,
    error: null,
  });
  postProgress(id, progress, currentStats);

  try {
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

      let scanResult = await withRemoteSyncTimeout(
        backend.scanRawData({
          schema: request.schema,
          limit: request.pageSize,
          ...(nextCursor
            ? {
                cursor: nextCursor,
                snapshotId: snapshotId || undefined,
                head: head || undefined,
                queryProfile,
              }
            : { offset }),
        }),
        'Remote sync scan',
      );
      if (!scanResult.ok) throw new Error(scanResult.capability.reason ?? 'Remote scan failed');

      let scan = scanResult.data;
      if (!scan) throw new Error('Remote scan returned no data');

      if (nextCursor && head && scan.head && scan.head !== head) {
        nextCursor = '';
        offset = localRows;
        scanResult = await withRemoteSyncTimeout(
          backend.scanRawData({
            schema: request.schema,
            limit: request.pageSize,
            offset,
            queryProfile,
          }),
          'Remote sync snapshot refresh',
        );
        if (!scanResult.ok) throw new Error(scanResult.capability.reason ?? 'Remote scan failed after snapshot refresh');
        scan = scanResult.data;
        if (!scan) throw new Error('Remote scan returned no data after snapshot refresh');
      }

      snapshotId = scan.snapshotId || snapshotId;
      head = scan.head || head;
      queryProfile = scan.queryProfile || queryProfile;
      totalRows = Math.max(totalRows, scan.totalCount ?? 0, offset + scan.results.length);
      if (scan.results.length === 0) break;

      const streamResult = await withRemoteSyncTimeout(
        backend.streamRawData({
          schema: scan.schema,
          scanHash: scan.scanHash,
          chunkHash: scan.chunkHash || scan.scanHash,
          snapshotId: scan.snapshotId,
          head: scan.head,
          cursor: scan.cursor,
          nextCursor: scan.nextCursor,
          totalCount: scan.totalCount,
          highWaterMark: scan.highWaterMark,
          queryProfile: scan.queryProfile || queryProfile,
          records: scan.results,
        }),
        'Remote sync stream',
      );
      if (!streamResult.ok) throw new Error(streamResult.capability.reason ?? 'Remote FlatBuffer stream failed');

      const records = streamResult.data ?? [];
      const ingestedRows = await currentStore.ingestRecords(request.standardId, records, {
        source: request.source,
        persist: false,
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
        highWaterMark: scan.highWaterMark || null,
        queryProfile,
        chunkHash: chunkHash || null,
        syncProtocol: scan.syncProtocol || null,
        verifiedChunks,
        lastSyncedAt: new Date().toISOString(),
        error: null,
      });
      postProgress(id, progress, currentStats);

      if (!nextCursor || offset >= totalRows || records.length === 0) break;
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

function progressFor(current: WorkerSchemaSyncProgress, patch: Partial<WorkerSchemaSyncProgress>): WorkerSchemaSyncProgress {
  return {
    ...current,
    ...patch,
    syncedRows: Math.max(patch.syncedRows ?? current.syncedRows, patch.localRows ?? current.localRows),
  };
}

function localRowsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
  return stats.find((entry) => entry.standardId === standardId)?.recordCount ?? 0;
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
