import {
  createLocalFlatSqlStore,
  type LocalFlatSqlIngestOptions,
  type LocalFlatSqlQueryResult,
  type LocalFlatSqlStandardStats,
  type LocalFlatSqlStatsOptions,
  type LocalFlatSqlStore,
  type LocalFlatSqlStoreOptions,
} from './local-flatsql';
import type { DataScanResult, DataSummary, RawDataQuery, RawDataRecord } from './sdn-backend';

export interface WorkerFlatSqlSyncBackendConfig {
  targetPeerId: string;
  candidateAddrs: string[];
  providerId?: string | null;
  sourceName?: string | null;
  displayName?: string | null;
  publicKey?: string | null;
  gatewayUrl?: string | null;
}

export interface WorkerSchemaSyncProgress {
  status: 'idle' | 'syncing' | 'synced' | 'capped' | 'error';
  syncedRows: number;
  totalRows: number;
  localRows: number;
  cachedBytes: number;
  pinnedBytes: number;
  providerPeerId: string | null;
  providerPublicKey: string | null;
  snapshotId: string | null;
  head: string | null;
  cursor: string | null;
  nextCursor: string | null;
  highWaterMark: string | null;
  queryProfile: string | null;
  chunkHash: string | null;
  syncProtocol: string | null;
  verifiedChunks: string[];
  lastSyncedAt: string | null;
  error: string | null;
}

export interface WorkerSchemaSyncRequest {
  standardId: string;
  schema: string;
  backendConfig: WorkerFlatSqlSyncBackendConfig;
  initialProgress: WorkerSchemaSyncProgress;
  totalRows: number;
  capBytes: number;
  pageSize: number;
  persistRecordInterval: number;
  source: string | null;
}

export interface WorkerSchemaSyncUpdate {
  progress: WorkerSchemaSyncProgress;
  stats: LocalFlatSqlStandardStats[];
}

export interface WorkerRemotePageRequest {
  standardId: string;
  query: RawDataQuery;
  backendConfig: WorkerFlatSqlSyncBackendConfig;
  source: string | null;
}

export interface WorkerRemotePageResult {
  scan: DataScanResult | null;
  records: RawDataRecord[];
  stats: LocalFlatSqlStandardStats[];
}

export interface WorkerLocalFlatSqlStore extends LocalFlatSqlStore {
  getRemoteDataSummary(backendConfig: WorkerFlatSqlSyncBackendConfig): Promise<DataSummary | null>;
  queryRemotePage(request: WorkerRemotePageRequest): Promise<WorkerRemotePageResult>;
  syncSchema(request: WorkerSchemaSyncRequest, onProgress?: (update: WorkerSchemaSyncUpdate) => void): Promise<WorkerSchemaSyncUpdate>;
}

type WorkerRequest =
  | { id: number; type: 'init'; options: LocalFlatSqlStoreOptions }
  | { id: number; type: 'ingestRecords'; standardId: string; records: RawDataRecord[]; sourceOrOptions?: string | LocalFlatSqlIngestOptions | null }
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

interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
}

export async function createWorkerLocalFlatSqlStore(options: LocalFlatSqlStoreOptions): Promise<WorkerLocalFlatSqlStore> {
  if (typeof Worker === 'undefined') return await createLocalFlatSqlStore(options) as WorkerLocalFlatSqlStore;

  const worker = new Worker(new URL('./local-flatsql.worker.ts', import.meta.url), { type: 'module' });
  const store = new WorkerLocalFlatSqlStoreClient(worker);
  await store.init(options);
  return store;
}

class WorkerLocalFlatSqlStoreClient implements WorkerLocalFlatSqlStore {
  private nextId = 1;
  private readonly pending = new Map<number, PendingRequest>();
  private readonly syncProgressHandlers = new Map<number, (update: WorkerSchemaSyncUpdate) => void>();
  private statsCache: LocalFlatSqlStandardStats[] = [];
  private destroyed = false;

  constructor(private readonly worker: Worker) {
    this.worker.onmessage = (event: MessageEvent<WorkerResponse>) => {
      this.handleResponse(event.data);
    };
    this.worker.onerror = (event) => {
      this.rejectAll(new Error(event.message || 'FlatSQL worker failed'));
    };
    this.worker.onmessageerror = () => {
      this.rejectAll(new Error('FlatSQL worker message failed'));
    };
  }

  init(options: LocalFlatSqlStoreOptions): Promise<void> {
    return this.request<void>({ id: 0, type: 'init', options });
  }

  async ingestRecords(
    standardId: string,
    records: RawDataRecord[],
    sourceOrOptions?: string | LocalFlatSqlIngestOptions | null,
  ): Promise<number> {
    const transfer = typeof sourceOrOptions === 'object' && sourceOrOptions?.transfer === true;
    const prepared = prepareRecordsForWorker(records, transfer);
    const requestSource = stripTransferOption(sourceOrOptions);
    return await this.request<number>(
      {
        id: 0,
        type: 'ingestRecords',
        standardId,
        records: prepared.records,
        sourceOrOptions: requestSource,
      },
      prepared.transferables,
    );
  }

  async flush(standardId?: string): Promise<void> {
    await this.request<void>({ id: 0, type: 'flush', standardId });
  }

  query(sql: string, standardId?: string): Promise<LocalFlatSqlQueryResult> {
    return this.request<LocalFlatSqlQueryResult>({ id: 0, type: 'query', sql, standardId });
  }

  async getStats(options?: LocalFlatSqlStatsOptions): Promise<LocalFlatSqlStandardStats[]> {
    const stats = await this.request<LocalFlatSqlStandardStats[]>({ id: 0, type: 'getStats', options });
    this.statsCache = stats;
    return stats;
  }

  getRemoteDataSummary(backendConfig: WorkerFlatSqlSyncBackendConfig): Promise<DataSummary | null> {
    return this.request<DataSummary | null>({ id: 0, type: 'getRemoteDataSummary', backendConfig });
  }

  queryRemotePage(request: WorkerRemotePageRequest): Promise<WorkerRemotePageResult> {
    return this.request<WorkerRemotePageResult>({ id: 0, type: 'queryRemotePage', request });
  }

  syncSchema(request: WorkerSchemaSyncRequest, onProgress?: (update: WorkerSchemaSyncUpdate) => void): Promise<WorkerSchemaSyncUpdate> {
    const id = this.nextId;
    this.nextId += 1;
    if (onProgress) this.syncProgressHandlers.set(id, onProgress);
    return new Promise<WorkerSchemaSyncUpdate>((resolve, reject) => {
      this.pending.set(id, {
        resolve: (value) => resolve(value as WorkerSchemaSyncUpdate),
        reject,
      });
      this.worker.postMessage({ id, type: 'syncSchema', request } satisfies WorkerRequest);
    }).finally(() => {
      this.syncProgressHandlers.delete(id);
    });
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.rejectAll(new Error('FlatSQL worker store was destroyed'));
    this.worker.postMessage({ id: 0, type: 'destroy' } satisfies WorkerRequest);
    this.worker.terminate();
  }

  private request<T>(request: WorkerRequest, transferables: Transferable[] = []): Promise<T> {
    if (this.destroyed) return Promise.reject(new Error('FlatSQL worker store was destroyed'));
    const id = this.nextId;
    this.nextId += 1;
    const message = { ...request, id } as WorkerRequest;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, {
        resolve: (value) => resolve(value as T),
        reject,
      });
      this.worker.postMessage(message, transferables);
    });
  }

  private handleResponse(response: WorkerResponse): void {
    if ('type' in response && response.type === 'syncProgress') {
      this.statsCache = response.stats;
      this.syncProgressHandlers.get(response.id)?.({ progress: response.progress, stats: response.stats });
      return;
    }
    const pending = this.pending.get(response.id);
    if (!pending) return;
    this.pending.delete(response.id);
    if (!('ok' in response)) return;
    if (!response.ok) {
      pending.reject(new Error(response.error));
      return;
    }
    if (response.stats) this.statsCache = response.stats;
    pending.resolve(response.data ?? response.stats ?? this.statsCache);
  }

  private rejectAll(error: Error): void {
    for (const pending of this.pending.values()) pending.reject(error);
    this.pending.clear();
  }
}

function prepareRecordsForWorker(records: RawDataRecord[], transfer: boolean): { records: RawDataRecord[]; transferables: Transferable[] } {
  const transferables: Transferable[] = [];
  const preparedRecords = records.map((record) => {
    if (!(record.dataBytes instanceof Uint8Array)) return record;
    const bytes = record.dataBytes;
    if (transfer && bytes.byteOffset === 0 && bytes.byteLength === bytes.buffer.byteLength && bytes.buffer instanceof ArrayBuffer) {
      transferables.push(bytes.buffer);
      return record;
    }
    const dataBytes = bytes.slice();
    if (transfer) transferables.push(dataBytes.buffer);
    return { ...record, dataBytes };
  });
  return { records: preparedRecords, transferables };
}

function stripTransferOption(sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): string | LocalFlatSqlIngestOptions | null | undefined {
  if (!sourceOrOptions || typeof sourceOrOptions !== 'object') return sourceOrOptions;
  const rest = { ...sourceOrOptions };
  delete rest.transfer;
  return rest;
}
