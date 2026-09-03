/**
 * The STORE-ONLY FlatSQL worker — the dashboard's engine (window model, owner
 * ruling 2026-09-03: "the same machinery as a node, storage aside").
 *
 * Same engine store (`createLocalFlatSqlStore`), same request/response wire
 * shapes and the same `postSuccess` / `postFailure` / `postRawStreamSuccess`
 * helpers as `local-flatsql.worker.ts`, minus every sync lane. This module
 * imports NOTHING from the libp2p sync stack (no backend cache, no published
 * shard fetchers) and performs no HTTP of its own: every byte the engine
 * ingests is handed over by the main thread as an aligned size-prefixed
 * FlatBuffer stream, and every answer travels back as engine rows or an
 * aligned frame stream. Sync-lane requests (`syncSchema`, `queryRemotePage`,
 * `getRemoteDataSummary`) are refused explicitly, never silently dropped.
 *
 * Bundled by the dashboard as `sdn-node-data-worker?worker&inline` (an iife
 * blob: worker), which is why the engine must be initialised with an ABSOLUTE
 * `engine.wasmPath` — a blob: URL cannot resolve `./flatsql.wasm`.
 */
import {
  createLocalFlatSqlStore,
  type LocalFlatSqlClearOptions,
  type LocalFlatSqlIngestOptions,
  type LocalFlatSqlIngestRecord,
  type LocalFlatSqlPinLedgerEntry,
  type LocalFlatSqlPinLedgerQuery,
  type LocalFlatSqlQueryOptions,
  type LocalFlatSqlSchema,
  type LocalFlatSqlStandardStats,
  type LocalFlatSqlStatsOptions,
  type LocalFlatSqlStore,
  type LocalFlatSqlStoreOptions,
  type LocalFlatSqlStreamIngestOptions,
} from './local-flatsql';
import type { EngineEpochQueryRequest } from '../../epoch-query-sql';
import type { QueryParam } from 'flatsql/wasm';

type WorkerRequest =
  | { id: number; type: 'init'; options: LocalFlatSqlStoreOptions }
  | { id: number; type: 'addStandard'; schema: LocalFlatSqlSchema }
  | { id: number; type: 'ingestRecords'; standardId: string; records: LocalFlatSqlIngestRecord[]; sourceOrOptions?: string | LocalFlatSqlIngestOptions | null }
  | { id: number; type: 'ingestFlatBufferStream'; standardId: string; streamBytes: Uint8Array; options?: LocalFlatSqlStreamIngestOptions | null }
  | { id: number; type: 'clearStandard'; standardId: string; options?: LocalFlatSqlClearOptions }
  | { id: number; type: 'flush'; standardId?: string }
  | { id: number; type: 'query'; sql: string; standardId?: string; options?: LocalFlatSqlQueryOptions }
  | { id: number; type: 'listSources'; standardId: string }
  | { id: number; type: 'queryEpochRawStream'; standardId: string; request?: EngineEpochQueryRequest | null }
  | { id: number; type: 'queryRawFlatBufferStream'; standardId: string; sql: string; params?: QueryParam[] }
  | { id: number; type: 'getStats'; options?: LocalFlatSqlStatsOptions }
  | { id: number; type: 'recordPinLedgerEntries'; entries: LocalFlatSqlPinLedgerEntry[] }
  | { id: number; type: 'listPinLedgerEntries'; query?: LocalFlatSqlPinLedgerQuery }
  | { id: number; type: 'getRemoteDataSummary' | 'queryRemotePage' | 'syncSchema' }
  | { id: number; type: 'destroy' };

type WorkerResponse =
  | { id: number; ok: true; data?: unknown; stats?: LocalFlatSqlStandardStats[] }
  | { id: number; ok: false; error: string };

type FlatSqlWorkerGlobal = {
  onmessage: ((event: MessageEvent<WorkerRequest>) => void) | null;
  postMessage(response: WorkerResponse, transferables?: Transferable[]): void;
};

const SYNC_LANE_UNAVAILABLE = 'not available in the store-only FlatSQL worker';

const workerGlobal = self as unknown as FlatSqlWorkerGlobal;
let store: LocalFlatSqlStore | null = null;

workerGlobal.onmessage = (event: MessageEvent<WorkerRequest>) => {
  void handleRequest(event.data);
};

async function handleRequest(request: WorkerRequest): Promise<void> {
  const id = request.id;
  try {
    switch (request.type) {
      case 'init':
        store?.destroy();
        store = await createLocalFlatSqlStore(request.options);
        postSuccess(id, undefined, await stats({ includeCachedBytes: false }));
        return;
      case 'addStandard':
        await requireStore().addStandard(request.schema);
        postSuccess(id, undefined, await stats({ includeCachedBytes: false }));
        return;
      case 'ingestRecords': {
        const ingested = await requireStore().ingestRecords(request.standardId, request.records, request.sourceOrOptions);
        postSuccess(id, ingested, await stats({ includeCachedBytes: false }));
        return;
      }
      case 'ingestFlatBufferStream': {
        const ingested = await requireStore().ingestFlatBufferStream(request.standardId, request.streamBytes, request.options);
        postSuccess(id, ingested, await stats({ includeCachedBytes: false }));
        return;
      }
      case 'clearStandard':
        await requireStore().clearStandard(request.standardId, request.options);
        postSuccess(id, undefined, await stats({ includeCachedBytes: false }));
        return;
      case 'flush':
        await requireStore().flush(request.standardId);
        postSuccess(id, undefined, await stats({ includeCachedBytes: true }));
        return;
      case 'query':
        postSuccess(id, await requireStore().query(request.sql, request.standardId, request.options));
        return;
      case 'listSources': {
        const currentStore = requireStore();
        if (!currentStore.listSources) {
          throw new Error('The FlatSQL worker store does not expose source-partition listing');
        }
        postSuccess(id, await currentStore.listSources(request.standardId));
        return;
      }
      case 'queryEpochRawStream': {
        const currentStore = requireStore();
        if (!currentStore.queryEpochRawStream) {
          throw new Error('The FlatSQL worker store does not expose engine epoch raw-stream queries');
        }
        postRawStreamSuccess(id, await currentStore.queryEpochRawStream(request.standardId, request.request ?? null));
        return;
      }
      case 'queryRawFlatBufferStream': {
        const currentStore = requireStore();
        if (!currentStore.queryRawFlatBufferStream) {
          throw new Error('The FlatSQL worker store does not expose raw FlatBuffer stream queries');
        }
        postRawStreamSuccess(id, await currentStore.queryRawFlatBufferStream(request.standardId, request.sql, request.params ?? []));
        return;
      }
      case 'getStats':
        postSuccess(id, await stats(request.options));
        return;
      case 'recordPinLedgerEntries':
        await requireStore().recordPinLedgerEntries(request.entries);
        postSuccess(id, undefined, await stats({ includeCachedBytes: true }));
        return;
      case 'listPinLedgerEntries':
        postSuccess(id, await requireStore().listPinLedgerEntries(request.query));
        return;
      case 'getRemoteDataSummary':
      case 'queryRemotePage':
      case 'syncSchema':
        postFailure(id, `${request.type} is ${SYNC_LANE_UNAVAILABLE}`);
        return;
      case 'destroy':
        store?.destroy();
        store = null;
        postSuccess(id);
        return;
      default:
        postFailure(id, `unsupported request is ${SYNC_LANE_UNAVAILABLE}`);
        return;
    }
  } catch (error) {
    postFailure(id, error instanceof Error ? error.message : String(error));
  }
}

function requireStore(): LocalFlatSqlStore {
  if (!store) throw new Error('FlatSQL worker store is not initialized');
  return store;
}

async function stats(options?: LocalFlatSqlStatsOptions): Promise<LocalFlatSqlStandardStats[]> {
  return await requireStore().getStats(options);
}

/**
 * Post an aligned raw FlatBuffer stream back to the client, transferring the
 * buffer when the bytes own it outright. Streams that are views into a
 * larger buffer (or into SharedArrayBuffer WASM memory under COI) are copied
 * first — transferring the underlying buffer would detach engine memory.
 */
function postRawStreamSuccess(id: number, streamBytes: Uint8Array): void {
  const ownsBuffer = streamBytes.byteOffset === 0
    && streamBytes.byteLength === streamBytes.buffer.byteLength
    && streamBytes.buffer instanceof ArrayBuffer;
  const bytes = ownsBuffer ? streamBytes : streamBytes.slice();
  postSuccess(id, bytes, undefined, [bytes.buffer as ArrayBuffer]);
}

function postSuccess(id: number, data?: unknown, stats?: LocalFlatSqlStandardStats[], transferables: Transferable[] = []): void {
  const response: WorkerResponse = stats ? { id, ok: true, data, stats } : { id, ok: true, data };
  workerGlobal.postMessage(response, transferables);
}

function postFailure(id: number, error: string): void {
  const response: WorkerResponse = { id, ok: false, error };
  workerGlobal.postMessage(response);
}
