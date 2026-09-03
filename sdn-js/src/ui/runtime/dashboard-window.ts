/**
 * Dashboard window runtime.
 *
 * The dashboard is a node-shaped client: it runs the SAME FlatSQL engine the
 * node runs and is fed by the SAME size-prefixed FlatBuffer record frames —
 * but as a WINDOW, never a mirror. For every UI state (standard, source lane,
 * epoch range) the client asks the node for exactly the rows that state
 * needs on the raw lane (`POST /api/v1/data/query` with
 * `Accept: application/vnd.sdn.flatbuffers.stream`), ingests the frames into
 * `<Table>@<source>` partitions of the local engine, runs the screen's SQL
 * locally, and drops the window when the state changes. The node never
 * projects a record to JSON for the dashboard; the local engine projects
 * every standard from its own engine DDL (`GET /api/v1/standards/<CODE>.fbs`).
 *
 * Two load shapes:
 *   - `loadPage`   — one server page (offset/limit) for the plain grid; every
 *                    page change re-ingests just that page, so paging covers
 *                    every stored record.
 *   - `loadWindow` — bounded batches (rows/bytes caps) for sort, filters,
 *                    search, charts, pivots and timelines; the footer reports
 *                    window vs stored so a capped window is never mistaken
 *                    for the whole store.
 *
 * Loads are serialized per standard and stamped with a generation: a newer
 * state supersedes an in-flight load BEFORE its ingest, so a stale state can
 * never leave a partial window behind.
 */

import {
  flatSqlSizePrefixedStreamInfo,
  type LocalFlatSqlClearOptions,
  type LocalFlatSqlQueryOptions,
  type LocalFlatSqlQueryResult,
  type LocalFlatSqlSchema,
  type LocalFlatSqlStandardStats,
  type LocalFlatSqlStatsOptions,
  type LocalFlatSqlStreamIngestOptions,
} from './local-flatsql';
import type { WorkerSchemaSyncProgress } from './local-flatsql-worker-client';
import type { RawDataQuery } from './sdn-backend';
import {
  authRawFlatbufferStreamRequest,
  joinUrl,
  rawDataQueryPayload,
  resolveFetch,
  type FetchLike,
} from './sdn-backend-adapter-utils';
import { SerialTaskQueue } from './serial-task-queue';
import { syncRowCountSummary } from './sync-progress';
import { encodeWorkerSchemaSyncProgressFlatBuffer } from './worker-sync-status-flatbuffer';

/** Window mode row cap: batches stop once the window holds this many rows. */
export const WINDOW_MAX_ROWS = 20_000;
/** Window mode batch size — one raw-lane request per batch. */
export const WINDOW_BATCH_ROWS = 5_000;
/** Window mode byte cap on the ingested frames. */
export const WINDOW_MAX_BYTES = 24 * 1024 * 1024;
/** The raw lane's own per-request cap (sdn-server rawDataMaxRawStreamLimit). */
export const RAW_LANE_MAX_LIMIT = 20_000;
/** `$DSS` SYNC_PROTOCOL value for windows loaded over the raw lane. */
export const DASHBOARD_WINDOW_SYNC_PROTOCOL = 'http:/api/v1/data/query';
/** Filter used once per standard to learn whether the node indexes epochs for it. */
export const DASHBOARD_WINDOW_RANGE_PROBE_FILTER = 'EPOCH >= 1970-01-01';
/** Source partition used when neither the node nor the state names one. */
export const DASHBOARD_WINDOW_UNKNOWN_SOURCE = 'unknown';

export interface DashboardWindowRange {
  /** Column the range applies to (`EPOCH` is the only column the node filters server-side). */
  column: string;
  /** Inclusive lower bound — ISO 8601 UTC (`YYYY-MM-DD` or RFC 3339); '' = open. */
  from: string;
  /** Inclusive upper bound — ISO 8601 UTC; '' = open. */
  to: string;
}

export interface DashboardWindowState {
  /** Standard code without `.fbs` (`OMM`, `CNP`); `.fbs` is tolerated. */
  schema: string;
  /** UI source lane (`OMM@celestrak-gp`), the bare source name, or '' for every source. */
  source?: string | null;
  range?: DashboardWindowRange | null;
}

export type DashboardWindowMode = 'page' | 'window';
export type DashboardWindowServerOrder = 'newest-first' | 'epoch-asc';

export interface DashboardWindowLoad {
  /** State key the window was loaded for (see {@link dashboardWindowStateKey}). */
  key: string;
  standardId: string;
  tableName: string;
  mode: DashboardWindowMode;
  /** Source name the node was asked for ('' = every source). */
  source: string;
  /** Rows the local engine holds for this state. */
  windowRows: number;
  /** Rows the node stores for this state (X-SDN-Total-Count). */
  storedRows: number;
  /** True when the window holds fewer rows than the node stores for the state. */
  partial: boolean;
  /** Frame bytes ingested for this load. */
  bytes: number;
  serverOrder: DashboardWindowServerOrder;
  /** True when the node applied the epoch range (sync_filter) server-side. */
  serverRange: boolean;
  /** The `sync_filter` sent to the node ('' when none). */
  syncFilter: string;
  /** Human-readable range text for status ('' when the state has no range). */
  rangeText: string;
  /** Server offset of the first row held. */
  offset: number;
  page: number | null;
  pageSize: number | null;
  /** Raw-lane requests that fed this window. */
  batches: number;
  /** UTC ISO timestamp of the load. */
  loadedAt: string;
}

export interface DashboardWindowStandard {
  standardId: string;
  tableName: string;
  fileId: string;
  /** Engine columns: declared fields + `_source`, `_rowid`, `_offset`, `_data`. */
  columns: string[];
}

export interface DashboardWindowPageOptions {
  page: number;
  limit: number;
}

export interface DashboardWindowLoadOptions {
  maxRows?: number;
  maxBytes?: number;
  batchRows?: number;
}

export interface DashboardWindowSourceRun {
  source: string;
  count: number;
}

/** The subset of the local FlatSQL store the window runtime drives. */
export interface WindowStore {
  /** Register a standard from its engine DDL (worker stores created with `schemas: []`). */
  addStandard?(schema: LocalFlatSqlSchema): void | Promise<void>;
  ingestFlatBufferStream(standardId: string, streamBytes: Uint8Array, options?: LocalFlatSqlStreamIngestOptions | null): Promise<number>;
  clearStandard(standardId: string, options?: LocalFlatSqlClearOptions): Promise<void>;
  query(sql: string, standardId?: string, options?: LocalFlatSqlQueryOptions): LocalFlatSqlQueryResult | Promise<LocalFlatSqlQueryResult>;
  getStats(options?: LocalFlatSqlStatsOptions): LocalFlatSqlStandardStats[] | Promise<LocalFlatSqlStandardStats[]>;
  listSources?(standardId: string): string[] | Promise<string[]>;
  destroy?(): void;
}

export interface DashboardWindowRuntime {
  /** Register a standard in the local engine from the node's engine DDL; cached per code. */
  ensureStandard(code: string): Promise<DashboardWindowStandard>;
  stateKey(state: DashboardWindowState): string;
  /** Drop the window and ingest exactly one server page for the state. */
  loadPage(state: DashboardWindowState, options: DashboardWindowPageOptions): Promise<DashboardWindowLoad>;
  /** Drop the window and ingest the state's rows in bounded batches. */
  loadWindow(state: DashboardWindowState, options?: DashboardWindowLoadOptions): Promise<DashboardWindowLoad>;
  /** Read-only SQL over the local window (callers pass their own caps). */
  query(sql: string, code: string, options?: LocalFlatSqlQueryOptions): Promise<LocalFlatSqlQueryResult>;
  /** Source partitions the window currently holds. */
  listSources(code: string): Promise<string[]>;
  /** Drop a standard's window. */
  clear(code: string): Promise<void>;
  currentLoad(code: string): DashboardWindowLoad | null;
  /** `$DSS` frame describing the window vs the node's store. */
  status(code: string): Uint8Array;
  /** Forget every cached standard and load; the store is left to its owner. */
  destroy(): void;
}

export interface CreateDashboardWindowOptions {
  store: WindowStore;
  fetch?: FetchLike | null;
  baseUrl?: string | null;
}

/** Raised when a newer state superseded a load before its ingest. */
export class DashboardWindowSupersededError extends Error {
  override readonly name = 'DashboardWindowSupersededError';

  constructor(standardId: string) {
    super(`window load for ${standardId} was superseded by a newer state`);
  }
}

export function isDashboardWindowSuperseded(error: unknown): error is DashboardWindowSupersededError {
  return error instanceof DashboardWindowSupersededError
    || (error instanceof Error && error.name === 'DashboardWindowSupersededError');
}

interface StandardWindowState {
  standard: Promise<DashboardWindowStandard> | null;
  queue: SerialTaskQueue;
  generation: number;
  load: DashboardWindowLoad | null;
  rangeProbe: Promise<boolean> | null;
}

interface RawLanePage {
  bytes: Uint8Array;
  recordCount: number;
  totalCount: number | null;
  runs: DashboardWindowSourceRun[] | null;
}

interface NormalizedRange {
  column: string;
  from: string;
  to: string;
}

const COLUMN_QUERY_OPTIONS: LocalFlatSqlQueryOptions = { maxLimit: 1, maxBytes: 64_000, timeoutMs: 5_000 };

export function createDashboardWindow(options: CreateDashboardWindowOptions): DashboardWindowRuntime {
  return new DashboardWindow(options);
}

/** Standard code as the engine keys it: trimmed, upper-cased, without `.fbs`. */
export function normalizeStandardCode(code: string): string {
  const trimmed = (code ?? '').trim();
  const withoutExtension = trimmed.replace(/\.fbs$/i, '');
  const normalized = withoutExtension.trim().toUpperCase();
  if (!normalized) throw new Error('a standard code is required');
  return normalized;
}

/**
 * Source name the node is asked for: the part of a UI lane (`OMM@celestrak-gp`)
 * after the table prefix; a bare name is used as-is; '' = every source.
 */
export function sourceNameForState(state: DashboardWindowState, code = normalizeStandardCode(state.schema)): string {
  const lane = (state.source ?? '').trim();
  if (!lane) return '';
  if (lane.toUpperCase().startsWith(`${code}@`)) return lane.slice(code.length + 1).trim();
  const at = lane.indexOf('@');
  return at >= 0 ? lane.slice(at + 1).trim() : lane;
}

/** `${CODE}|${source}|${range.column}|${range.from}|${range.to}` — a change means clear + reload. */
export function dashboardWindowStateKey(state: DashboardWindowState): string {
  const code = normalizeStandardCode(state.schema);
  const range = normalizeRange(state.range);
  return [code, (state.source ?? '').trim(), range?.column ?? '', range?.from ?? '', range?.to ?? ''].join('|');
}

/** Parse `X-SDN-Source-Runs` (`<pathEscaped source or ->:<count>` pairs, comma-joined, frame order). */
export function parseSourceRunsHeader(value: string | null | undefined): DashboardWindowSourceRun[] | null {
  const text = (value ?? '').trim();
  if (!text) return null;
  const runs: DashboardWindowSourceRun[] = [];
  for (const part of text.split(',')) {
    const item = part.trim();
    if (!item) continue;
    const separator = item.lastIndexOf(':');
    if (separator < 0) return null;
    const count = Number(item.slice(separator + 1));
    if (!Number.isInteger(count) || count < 0) return null;
    const escaped = item.slice(0, separator);
    runs.push({ source: escaped === '-' ? '' : decodePathSegment(escaped), count });
  }
  return runs;
}

/** Frame start offsets of an aligned u32-LE size-prefixed stream, plus the end offset. */
export function sizePrefixedFrameBoundaries(bytes: Uint8Array): number[] {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const bounds = [0];
  let offset = 0;
  while (offset < bytes.byteLength) {
    if (bytes.byteLength - offset < 4) throw new Error(`Invalid FlatSQL size-prefixed stream: truncated frame header at offset ${offset}`);
    const length = view.getUint32(offset, true);
    offset += 4 + length;
    if (offset > bytes.byteLength) throw new Error(`Invalid FlatSQL size-prefixed stream: truncated frame at index ${bounds.length - 1}`);
    bounds.push(offset);
  }
  return bounds;
}

/**
 * `$DSS` progress for a window: localRows = window rows, totalRows = rows the
 * node stores for the state, missingRows = the gap. `synced` when the state
 * is completely held (a page is complete by definition), `capped` when the
 * window mode caps cut it short, `idle` when nothing is loaded.
 */
export function dashboardWindowProgress(load: DashboardWindowLoad | null): WorkerSchemaSyncProgress {
  const idle: WorkerSchemaSyncProgress = {
    status: 'idle',
    syncedRows: 0,
    totalRows: 0,
    localRows: 0,
    pinnedRows: 0,
    missingRows: 0,
    cachedBytes: 0,
    pinnedBytes: 0,
    downloadedBytes: 0,
    downloadSpeedBytesPerSecond: 0,
    measuredWireSpeedBytesPerSecond: 0,
    wireSpeedUtilization: null,
    wireSpeedTarget: 0,
    wireSpeedTargetMet: null,
    manifestDiscoveryMs: 0,
    networkTransferMs: 0,
    verificationMs: 0,
    flatSqlMaterializationMs: 0,
    providerPeerId: null,
    providerPublicKey: null,
    snapshotId: null,
    head: null,
    cursor: null,
    nextCursor: null,
    highWaterMark: null,
    queryProfile: null,
    chunkHash: null,
    syncProtocol: DASHBOARD_WINDOW_SYNC_PROTOCOL,
    syncFilter: null,
    verifiedChunks: [],
    lastSyncedAt: null,
    error: null,
  };
  if (!load) return idle;
  const summary = syncRowCountSummary({
    localRows: load.windowRows,
    syncedRows: load.windowRows,
    remoteRows: load.storedRows,
    totalRows: load.storedRows,
    pinnedRows: 0,
  });
  return {
    ...idle,
    status: load.partial ? 'capped' : 'synced',
    syncedRows: summary.syncedRows,
    totalRows: summary.totalRows,
    localRows: load.windowRows,
    pinnedRows: summary.pinnedRows,
    missingRows: summary.missingRows,
    cachedBytes: load.bytes,
    downloadedBytes: load.bytes,
    cursor: load.key,
    queryProfile: load.mode,
    syncFilter: load.rangeText || null,
    lastSyncedAt: load.loadedAt,
  };
}

/** Encode {@link dashboardWindowProgress} as `$DSS` bytes. */
export function encodeDashboardWindowStatus(load: DashboardWindowLoad | null): Uint8Array {
  return encodeWorkerSchemaSyncProgressFlatBuffer(dashboardWindowProgress(load));
}

class DashboardWindow implements DashboardWindowRuntime {
  private readonly store: WindowStore;
  private readonly fetch: FetchLike;
  private readonly baseUrl: string;
  private readonly states = new Map<string, StandardWindowState>();

  constructor(options: CreateDashboardWindowOptions) {
    this.store = options.store;
    this.fetch = resolveFetch(options.fetch ?? undefined);
    this.baseUrl = (options.baseUrl ?? '').trim();
  }

  ensureStandard(code: string): Promise<DashboardWindowStandard> {
    const standardId = normalizeStandardCode(code);
    const state = this.stateFor(standardId);
    if (!state.standard) {
      const pending: Promise<DashboardWindowStandard> = this.registerStandard(standardId).catch((error: unknown) => {
        // A failed registration is retried on the next call, never cached.
        if (state.standard === pending) state.standard = null;
        throw error;
      });
      state.standard = pending;
    }
    return state.standard;
  }

  stateKey(state: DashboardWindowState): string {
    return dashboardWindowStateKey(state);
  }

  loadPage(state: DashboardWindowState, options: DashboardWindowPageOptions): Promise<DashboardWindowLoad> {
    const standardId = normalizeStandardCode(state.schema);
    const window = this.stateFor(standardId);
    const generation = ++window.generation;
    const page = Math.max(1, Math.floor(Number(options.page) || 1));
    const limit = clampInteger(options.limit, 1, RAW_LANE_MAX_LIMIT, 100);
    const offset = (page - 1) * limit;
    return window.queue.enqueue(async () => {
      this.assertCurrent(window, generation, standardId);
      const standard = await this.ensureStandard(standardId);
      this.assertCurrent(window, generation, standardId);
      const key = dashboardWindowStateKey(state);
      const source = sourceNameForState(state, standardId);

      await this.dropWindow(window, standardId);
      const fetched = await this.fetchRawPage({
        schema: `${standardId}.fbs`,
        ...(source ? { sourceName: source } : {}),
        limit,
        offset,
      });
      this.assertCurrent(window, generation, standardId);
      const bytes = fetched.bytes.byteLength;
      await this.ingestRuns(standardId, fetched, key, offset, source);
      const windowRows = await this.windowRows(standardId);
      // Without X-SDN-Total-Count the node stores at least what it just paged.
      const storedRows = fetched.totalCount ?? Math.max(windowRows, offset + fetched.recordCount);
      const load: DashboardWindowLoad = {
        key,
        standardId,
        tableName: standard.tableName,
        mode: 'page',
        source,
        windowRows,
        storedRows,
        partial: false,
        bytes,
        serverOrder: 'newest-first',
        serverRange: false,
        syncFilter: '',
        rangeText: '',
        offset,
        page,
        pageSize: limit,
        batches: 1,
        loadedAt: new Date().toISOString(),
      };
      window.load = load;
      return { ...load };
    });
  }

  loadWindow(state: DashboardWindowState, options: DashboardWindowLoadOptions = {}): Promise<DashboardWindowLoad> {
    const standardId = normalizeStandardCode(state.schema);
    const window = this.stateFor(standardId);
    const generation = ++window.generation;
    const maxRows = clampInteger(options.maxRows, 1, Number.MAX_SAFE_INTEGER, WINDOW_MAX_ROWS);
    const maxBytes = clampInteger(options.maxBytes, 1, Number.MAX_SAFE_INTEGER, WINDOW_MAX_BYTES);
    const batchRows = clampInteger(options.batchRows, 1, RAW_LANE_MAX_LIMIT, WINDOW_BATCH_ROWS);
    return window.queue.enqueue(async () => {
      this.assertCurrent(window, generation, standardId);
      const standard = await this.ensureStandard(standardId);
      this.assertCurrent(window, generation, standardId);
      const key = dashboardWindowStateKey(state);
      const source = sourceNameForState(state, standardId);
      const range = normalizeRange(state.range);
      const serverRange = range ? await this.serverRangeSupported(window, standardId, range) : false;
      this.assertCurrent(window, generation, standardId);
      const syncFilter = serverRange && range ? serverRangeFilter(range) : '';

      await this.dropWindow(window, standardId);
      let offset = 0;
      let received = 0;
      let bytes = 0;
      let batches = 0;
      let storedRows: number | null = null;
      for (;;) {
        const limit = Math.min(batchRows, maxRows - received);
        if (limit <= 0) break;
        const fetched = await this.fetchRawPage({
          schema: `${standardId}.fbs`,
          ...(source ? { sourceName: source } : {}),
          ...(syncFilter ? { syncFilter } : {}),
          limit,
          offset,
        });
        this.assertCurrent(window, generation, standardId);
        batches += 1;
        const batchBytes = fetched.bytes.byteLength;
        await this.ingestRuns(standardId, fetched, key, offset, source);
        received += fetched.recordCount;
        bytes += batchBytes;
        offset += fetched.recordCount;
        if (fetched.totalCount != null) storedRows = fetched.totalCount;
        if (fetched.recordCount < limit) break;
        if (received >= maxRows || bytes >= maxBytes) break;
      }
      const windowRows = await this.windowRows(standardId);
      const stored = storedRows ?? Math.max(windowRows, received);
      const load: DashboardWindowLoad = {
        key,
        standardId,
        tableName: standard.tableName,
        mode: 'window',
        source,
        windowRows,
        storedRows: stored,
        partial: windowRows < stored,
        bytes,
        serverOrder: serverRange ? 'epoch-asc' : 'newest-first',
        serverRange,
        syncFilter,
        rangeText: range ? rangeText(range) : '',
        offset: 0,
        page: null,
        pageSize: null,
        batches,
        loadedAt: new Date().toISOString(),
      };
      window.load = load;
      return { ...load };
    });
  }

  async query(sql: string, code: string, options?: LocalFlatSqlQueryOptions): Promise<LocalFlatSqlQueryResult> {
    return await this.store.query(sql, normalizeStandardCode(code), options);
  }

  async listSources(code: string): Promise<string[]> {
    const standardId = normalizeStandardCode(code);
    if (typeof this.store.listSources !== 'function') return [];
    return [...(await this.store.listSources(standardId))];
  }

  clear(code: string): Promise<void> {
    const standardId = normalizeStandardCode(code);
    const window = this.stateFor(standardId);
    window.generation += 1;
    return window.queue.enqueue(async () => {
      window.load = null;
      if (!window.standard) return;
      try {
        await window.standard;
      } catch {
        return;
      }
      await this.store.clearStandard(standardId, { persist: false });
    });
  }

  currentLoad(code: string): DashboardWindowLoad | null {
    const load = this.states.get(normalizeStandardCode(code))?.load ?? null;
    return load ? { ...load } : null;
  }

  status(code: string): Uint8Array {
    return encodeDashboardWindowStatus(this.currentLoad(code));
  }

  destroy(): void {
    for (const state of this.states.values()) {
      state.generation += 1;
      state.load = null;
      state.standard = null;
      state.rangeProbe = null;
    }
    this.states.clear();
  }

  private stateFor(standardId: string): StandardWindowState {
    let state = this.states.get(standardId);
    if (!state) {
      state = { standard: null, queue: new SerialTaskQueue(), generation: 0, load: null, rangeProbe: null };
      this.states.set(standardId, state);
    }
    return state;
  }

  private assertCurrent(state: StandardWindowState, generation: number, standardId: string): void {
    if (state.generation !== generation) throw new DashboardWindowSupersededError(standardId);
  }

  private async dropWindow(state: StandardWindowState, standardId: string): Promise<void> {
    state.load = null;
    await this.store.clearStandard(standardId, { persist: false });
  }

  private async registerStandard(standardId: string): Promise<DashboardWindowStandard> {
    const registered = await this.registeredStats(standardId);
    let tableName = registered?.tableName?.trim() || standardId;
    let fileId = `$${standardId}`;

    const url = joinUrl(this.baseUrl, `/api/v1/standards/${encodeURIComponent(standardId)}.fbs`);
    let response: Response | null;
    try {
      response = await this.fetch(url, {
        method: 'GET',
        credentials: 'same-origin',
        headers: { accept: 'text/plain', 'x-requested-with': 'sdn-ui' },
      });
    } catch (error) {
      // A standard the engine already holds keeps working when the DDL lane
      // is unreachable; an unregistered one cannot exist without it.
      if (!registered) throw error;
      response = null;
    }
    if (response && !response.ok) {
      if (!registered) throw new Error(`${url} returned HTTP ${response.status}`);
      response = null;
    }
    if (response) {
      const text = await response.text();
      const headerTable = response.headers?.get?.('x-sdn-engine-table')?.trim();
      const headerFileId = response.headers?.get?.('x-sdn-file-id')?.trim();
      if (!registered && headerTable) tableName = headerTable;
      if (headerFileId) fileId = headerFileId;
      if (!registered) {
        if (typeof this.store.addStandard !== 'function') {
          throw new Error(`standard ${standardId} is not registered in the local engine`);
        }
        if (!text.trim()) throw new Error(`${url} returned an empty engine schema for ${standardId}`);
        await this.store.addStandard({ standardId, tableName, fileId, schema: text });
      }
    }

    const columns = (await this.store.query(`SELECT * FROM ${sqlIdentifier(tableName)} LIMIT 0`, standardId, COLUMN_QUERY_OPTIONS)).columns;
    return { standardId, tableName, fileId, columns: [...columns] };
  }

  private async registeredStats(standardId: string): Promise<LocalFlatSqlStandardStats | null> {
    const stats = await this.store.getStats({ includeCachedBytes: false });
    return stats.find((entry) => normalizeStandardCode(entry.standardId) === standardId) ?? null;
  }

  private async windowRows(standardId: string): Promise<number> {
    const stats = await this.registeredStats(standardId);
    return Math.max(0, Math.floor(Number(stats?.recordCount ?? 0)));
  }

  private serverRangeSupported(state: StandardWindowState, standardId: string, range: NormalizedRange): Promise<boolean> {
    if (range.column !== 'EPOCH') return Promise.resolve(false);
    if (!state.rangeProbe) {
      const probe = this.probeServerRange(standardId).catch((error: unknown) => {
        if (state.rangeProbe === probe) state.rangeProbe = null;
        throw error;
      });
      state.rangeProbe = probe;
    }
    return state.rangeProbe;
  }

  /** Once per standard: does the node's index carry epochs for it? */
  private async probeServerRange(standardId: string): Promise<boolean> {
    const url = joinUrl(this.baseUrl, '/api/v1/data/query');
    const response = await this.fetch(url, authRawFlatbufferStreamRequest(rawDataQueryPayload({
      schema: `${standardId}.fbs`,
      limit: 1,
      offset: 0,
      syncFilter: DASHBOARD_WINDOW_RANGE_PROBE_FILTER,
    })));
    if (!response.ok) {
      // The node refused the filter for this standard: no server-side range.
      if (response.status >= 400 && response.status < 500) return false;
      throw new Error(`${url} returned HTTP ${response.status}`);
    }
    const bytes = new Uint8Array(await response.arrayBuffer());
    const total = headerInteger(response, 'x-sdn-total-count');
    if (total != null) return total > 0;
    return flatSqlSizePrefixedStreamInfo(bytes).totalRecordCount > 0;
  }

  private async fetchRawPage(query: RawDataQuery): Promise<RawLanePage> {
    const url = joinUrl(this.baseUrl, '/api/v1/data/query');
    const response = await this.fetch(url, authRawFlatbufferStreamRequest(rawDataQueryPayload(query)));
    if (!response.ok) throw new Error(`${url} returned HTTP ${response.status}`);
    const bytes = new Uint8Array(await response.arrayBuffer());
    const info = flatSqlSizePrefixedStreamInfo(bytes);
    return {
      bytes,
      recordCount: info.totalRecordCount,
      totalCount: headerInteger(response, 'x-sdn-total-count'),
      runs: parseSourceRunsHeader(response.headers?.get?.('x-sdn-source-runs')),
    };
  }

  /**
   * Split the aligned stream at the node's source-run boundaries (zero-copy
   * views; a worker store slices only the run it transfers) and ingest each
   * run into its `<Table>@<source>` partition — the node's own ensureEngineSource.
   */
  private async ingestRuns(
    standardId: string,
    page: RawLanePage,
    key: string,
    offset: number,
    stateSource: string,
  ): Promise<number> {
    if (page.recordCount === 0) return 0;
    const bounds = sizePrefixedFrameBoundaries(page.bytes);
    const declaredRuns = page.runs?.filter((run) => run.count > 0) ?? [];
    const declaredCount = declaredRuns.reduce((total, run) => total + run.count, 0);
    const runs = declaredCount === page.recordCount && declaredRuns.length > 0
      ? declaredRuns
      : [{ source: '', count: page.recordCount }];
    let frameIndex = 0;
    let ingested = 0;
    for (let runIndex = 0; runIndex < runs.length; runIndex += 1) {
      const run = runs[runIndex];
      const start = bounds[frameIndex];
      const end = bounds[frameIndex + run.count];
      const slice = start === 0 && end === page.bytes.byteLength
        ? page.bytes
        : page.bytes.subarray(start, end);
      ingested += await this.store.ingestFlatBufferStream(standardId, slice, {
        source: run.source || stateSource || DASHBOARD_WINDOW_UNKNOWN_SOURCE,
        persist: false,
        recordKeyPrefix: `window:${key}:${offset}:${runIndex}:`,
      });
      frameIndex += run.count;
    }
    return ingested;
  }
}

function normalizeRange(range: DashboardWindowRange | null | undefined): NormalizedRange | null {
  if (!range) return null;
  const column = (range.column ?? '').trim().toUpperCase();
  const from = (range.from ?? '').trim();
  const to = (range.to ?? '').trim();
  if (!column || (!from && !to)) return null;
  return { column, from, to };
}

/** The node's sync_filter grammar: `EPOCH >= <bound> AND EPOCH <= <bound>` (ISO 8601 UTC bounds). */
function serverRangeFilter(range: NormalizedRange): string {
  const clauses: string[] = [];
  if (range.from) clauses.push(`EPOCH >= ${range.from}`);
  if (range.to) clauses.push(`EPOCH <= ${range.to}`);
  return clauses.join(' AND ');
}

function rangeText(range: NormalizedRange): string {
  const clauses: string[] = [];
  if (range.from) clauses.push(`${range.column} >= ${range.from}`);
  if (range.to) clauses.push(`${range.column} <= ${range.to}`);
  return clauses.join(' AND ');
}

function headerInteger(response: Response, name: string): number | null {
  const raw = response.headers?.get?.(name);
  if (raw == null || raw.trim() === '') return null;
  const value = Number(raw);
  return Number.isFinite(value) && value >= 0 ? Math.floor(value) : null;
}

function clampInteger(value: number | null | undefined, min: number, max: number, fallback: number): number {
  const numeric = Math.floor(Number(value));
  if (!Number.isFinite(numeric) || numeric <= 0) return fallback;
  return Math.max(min, Math.min(max, numeric));
}

function decodePathSegment(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function sqlIdentifier(name: string): string {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) ? name : `"${name.replace(/"/g, '""')}"`;
}
