/**
 * FlatSQL-WASM engine store (loop D.1) — THE SDNNode store.
 *
 * Promoted from `src/ui/runtime/local-flatsql.ts` into core: per-standard
 * engine databases created from SDS FlatBuffer schemas, per-provider source
 * partitioning via `registerSource` (shadow tables `<Table>@<source>`), and
 * unified views (`<Table>` = UNION ALL over the shadow tables with a
 * `_source` column carrying the FULL shadow-table name, e.g.
 * `OMM@celestrak-gp`) — exactly mirroring the sdn-server engine layout
 * (sdn-server/internal/storage/engine_records.go). Records stored without an
 * explicit source partition under the server's default source name `local`.
 *
 * Persistence (browser durability): the engine is in-memory (OMIT_WAL), so
 * durable state lives in a key/value persistence store (IndexedDB
 * `sdn-local-flatsql`/`datastores` by default; desktop nodes can point at an
 * HTTP persistence endpoint). Per (standard, source) the ALIGNED size-
 * prefixed record stream of the shadow table is persisted
 * (`SELECT _data FROM "Table@source"`), and boot re-registers the sources,
 * rebuilds the unified views, and re-ingests each stream into its partition.
 * (Engine `exportData`/`loadAndRebuild` cannot be used once sources exist:
 * rebuild routes records by file id only, which would drop the source
 * attribution — verified against flatsql database.cpp.) Legacy single-blob
 * snapshots (pre-D.1 `exportData` bytes) are migrated into the `local`
 * source partition on first open.
 */
import type { FlatSQL, FlatSQLDatabase } from 'flatsql/wasm';

import { validateReadOnlySql, type ReadOnlySqlValidationOptions } from './read-only-sql-sandbox';

/**
 * Minimal record surface the engine store ingests. Structurally compatible
 * with the webUI backend's RawDataRecord (src/ui/runtime/sdn-backend.ts).
 */
export interface LocalFlatSqlIngestRecord {
  schemaName: string;
  cid: string;
  peerId?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
  batchId?: string | null;
  timestamp?: string | null;
  dataBytes?: Uint8Array;
}

export interface LocalFlatSqlSchema {
  standardId: string;
  tableName: string;
  fileId: string;
  schema: string;
}

/**
 * Default source partition for records stored without an explicit source —
 * mirrors sdn-server's engineDefaultSource (engine_records.go).
 */
export const LOCAL_FLATSQL_DEFAULT_SOURCE = 'local';

export interface LocalFlatSqlStoreOptions {
  schemas: LocalFlatSqlSchema[];
  persistenceKey?: string | null;
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
  /** Injectable persistence backend (tests / embedders). Defaults to IndexedDB or the desktop endpoint. */
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
}

export interface ClearLocalFlatSqlStoreOptions {
  persistenceKey: string;
  standardIds: string[];
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
}

export interface LocalFlatSqlQueryResult {
  columns: string[];
  rows: unknown[][];
  records: Array<Record<string, unknown>>;
}

export type LocalFlatSqlQueryOptions = ReadOnlySqlValidationOptions;

export interface LocalFlatSqlStandardStats {
  standardId: string;
  tableName: string;
  recordCount: number;
  cachedBytes: number;
  ingestedRecordCount: number;
  pinnedRows: number;
  pinnedBytes: number;
  snapshotId: string | null;
  head: string | null;
  highWaterMark: string | null;
  lastSyncedAt: string | null;
}

export interface LocalFlatSqlPinLedgerEntry {
  cid: string;
  standardId: string;
  schemaName: string;
  providerPeerId?: string | null;
  providerPublicKey?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
  batchId?: string | null;
  queryProfile?: string | null;
  snapshotId?: string | null;
  head?: string | null;
  highWaterMark?: string | null;
  byteHash?: string | null;
  role: string;
  rowCount?: number | null;
  byteCount?: number | null;
  ttlSeconds?: number | null;
  verificationState: string;
  materializedAt?: string | null;
  verifiedAt?: string | null;
  updatedAt?: string | null;
}

export interface LocalFlatSqlPinLedgerQuery {
  cid?: string | null;
  standardId?: string | null;
  schemaName?: string | null;
  providerPeerId?: string | null;
  providerPublicKey?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
  batchId?: string | null;
  queryProfile?: string | null;
  role?: string | null;
  verificationState?: string | null;
}

export interface LocalFlatSqlIngestOptions {
  source?: string | null;
  persist?: boolean;
  transfer?: boolean;
}

export interface LocalFlatSqlStreamIngestOptions extends LocalFlatSqlIngestOptions {
  recordKeys?: string[];
  recordKeyPrefix?: string;
  recordKeyOffset?: number;
  skipRecords?: number;
  pinLedgerEntries?: LocalFlatSqlPinLedgerEntry[];
}

export interface LocalFlatSqlStatsOptions {
  includeCachedBytes?: boolean;
}

export interface LocalFlatSqlPinLedgerWriteOptions {
  persist?: boolean;
}

export interface LocalFlatSqlClearOptions {
  persist?: boolean;
}

export interface FlatSqlSizePrefixedStreamInfo {
  totalRecordCount: number;
  ingestRecordCount: number;
  ingestStartOffset: number;
  allFramesHaveDirectFileIdentifier: boolean;
}

export interface LocalFlatSqlStore {
  ingestRecords(standardId: string, records: LocalFlatSqlIngestRecord[], sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): Promise<number>;
  ingestFlatBufferStream(standardId: string, streamBytes: Uint8Array, options?: LocalFlatSqlStreamIngestOptions | null): Promise<number>;
  /** Registered source partitions (shadow tables `<Table>@<source>`) for a standard. */
  listSources?(standardId: string): string[] | Promise<string[]>;
  clearStandard(standardId: string, options?: LocalFlatSqlClearOptions): Promise<void>;
  createStandardReplacementStore(standardId: string): Promise<LocalFlatSqlStore>;
  replaceStandardFrom(standardId: string, replacementStore: LocalFlatSqlStore, entries: LocalFlatSqlPinLedgerEntry[], options?: LocalFlatSqlPinLedgerWriteOptions): Promise<void>;
  flush(standardId?: string): Promise<void>;
  recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[], options?: LocalFlatSqlPinLedgerWriteOptions): Promise<void>;
  listPinLedgerEntries(query?: LocalFlatSqlPinLedgerQuery): Promise<LocalFlatSqlPinLedgerEntry[]>;
  query(sql: string, standardId?: string, options?: LocalFlatSqlQueryOptions): LocalFlatSqlQueryResult | Promise<LocalFlatSqlQueryResult>;
  getStats(options?: LocalFlatSqlStatsOptions): LocalFlatSqlStandardStats[] | Promise<LocalFlatSqlStandardStats[]>;
  destroy(): void;
}

interface StandardDatabaseState {
  schema: LocalFlatSqlSchema;
  db: FlatSQLDatabase;
  ingestedKeys: Set<string>;
  /** Source partitions registered on this database (shadow tables `<Table>@<source>`). */
  sources: Set<string>;
  /** Sources whose streams are currently persisted (stale keys get deleted on flush). */
  persistedSources: string[];
  cachedBytes: number;
  dirty: boolean;
}

type FetchLike = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;

export interface LocalFlatSqlPersistenceStore {
  readonly available: boolean;
  readBytes(key: string): Promise<Uint8Array | null>;
  writeBytes(key: string, bytes: Uint8Array): Promise<void>;
  readJson(key: string): Promise<unknown>;
  writeJson(key: string, value: unknown): Promise<void>;
  deleteKey(key: string): Promise<void>;
}

/** In-memory persistence store (tests / ephemeral nodes). */
export class MemoryFlatSqlPersistenceStore implements LocalFlatSqlPersistenceStore {
  readonly available = true;
  private readonly entries = new Map<string, unknown>();

  async readBytes(key: string): Promise<Uint8Array | null> {
    const value = this.entries.get(key);
    return value instanceof Uint8Array ? new Uint8Array(value) : null;
  }

  async writeBytes(key: string, bytes: Uint8Array): Promise<void> {
    this.entries.set(key, new Uint8Array(bytes));
  }

  async readJson(key: string): Promise<unknown> {
    const value = this.entries.get(key);
    return value instanceof Uint8Array || value === undefined ? null : JSON.parse(JSON.stringify(value));
  }

  async writeJson(key: string, value: unknown): Promise<void> {
    this.entries.set(key, JSON.parse(JSON.stringify(value)));
  }

  async deleteKey(key: string): Promise<void> {
    this.entries.delete(key);
  }
}

const LOCAL_FLATSQL_DB_NAME = 'sdn-local-flatsql';
const LOCAL_FLATSQL_STORE_NAME = 'datastores';

// One engine per JS context. flatsql's initFlatSQL() rebinds its
// module-level wasm instance on EVERY call, which silently invalidates all
// database handles created by earlier calls — so every store in this
// process MUST share a single initialization.
let sharedFlatSqlPromise: Promise<FlatSQL> | null = null;

/** Shared FlatSQL-WASM engine instance for this JS context. */
export async function getSharedFlatSql(): Promise<FlatSQL> {
  if (!sharedFlatSqlPromise) {
    sharedFlatSqlPromise = import('flatsql/wasm').then(({ initFlatSQL }) =>
      initFlatSQL({ skipIntegrityCheck: true }));
  }
  return sharedFlatSqlPromise;
}

export async function createLocalFlatSqlStore(options: LocalFlatSqlStoreOptions): Promise<LocalFlatSqlStore> {
  const flatsql = await getSharedFlatSql();
  const states = new Map<string, StandardDatabaseState>();
  const persistenceStore = createLocalFlatSqlPersistenceStore(options);

  for (const schema of options.schemas) {
    const standardId = normalizeStandardId(schema.standardId);
    const db = flatsql.createDatabase(stripFlatBufferComments(schema.schema), `sdn-${standardId.toLowerCase()}`);
    db.registerFileId(schema.fileId, schema.tableName);

    // Restore source partitions (server-mirroring layout): re-register every
    // persisted source, rebuild the unified views, then re-ingest each
    // source's aligned stream into its shadow table.
    const sources = new Set<string>();
    let cachedBytes = 0;
    let migratedLegacySnapshot = false;
    if (options.persistenceKey) {
      const persistedSources = normalizePersistedSourceNames(
        await persistenceStore.readJson(persistedSourcesKey(options.persistenceKey, standardId)),
      );
      for (const source of persistedSources) {
        db.registerSource(source);
        sources.add(source);
      }
      if (sources.size > 0) db.createUnifiedViews();
      for (const source of persistedSources) {
        const stream = await persistenceStore.readBytes(persistedSourceStreamKey(options.persistenceKey, standardId, source));
        if (stream && stream.byteLength > 0) {
          db.ingest(stream, source);
          cachedBytes += stream.byteLength;
        }
      }

      // Legacy pre-D.1 snapshot (single exportData blob, no source
      // attribution): migrate its records into the default `local` source.
      // Only consulted when NO new-format source list exists — once the
      // per-source layout has been written, a leftover legacy blob (e.g.
      // crash before its delete) must not double-ingest.
      const legacy = sources.size === 0
        ? await readPersistedFlatSqlBytes(persistenceStore, persistedStandardKey(options.persistenceKey, standardId))
        : null;
      if (legacy && legacy.byteLength > 0) {
        if (!sources.has(LOCAL_FLATSQL_DEFAULT_SOURCE)) {
          db.registerSource(LOCAL_FLATSQL_DEFAULT_SOURCE);
          sources.add(LOCAL_FLATSQL_DEFAULT_SOURCE);
          db.createUnifiedViews();
        }
        db.ingest(legacy, LOCAL_FLATSQL_DEFAULT_SOURCE);
        cachedBytes += legacy.byteLength;
        migratedLegacySnapshot = true;
      }
    }

    const ingestedKeys = options.persistenceKey
      ? await readPersistedRecordKeys(persistenceStore, persistedRecordKey(options.persistenceKey, standardId))
      : new Set<string>();
    const persistedRecordCount = recordCountForDatabase(db, schema.tableName);
    const repairedIngestedKeys = ingestedKeys.size > persistedRecordCount
      ? new Set<string>()
      : ingestedKeys;
    states.set(standardId, {
      schema: { ...schema, standardId },
      db,
      ingestedKeys: repairedIngestedKeys,
      sources,
      persistedSources: Array.from(sources),
      cachedBytes,
      dirty: repairedIngestedKeys !== ingestedKeys || migratedLegacySnapshot,
    });
  }

  const pinLedger = options.persistenceKey
    ? await readPersistedPinLedgerEntries(persistenceStore, persistedPinLedgerKey(options.persistenceKey))
    : new Map<string, LocalFlatSqlPinLedgerEntry>();
  return new WasmLocalFlatSqlStore(states, options.persistenceKey ?? null, pinLedger, persistenceStore, flatsql);
}

export async function clearLocalFlatSqlStore(options: ClearLocalFlatSqlStoreOptions): Promise<void> {
  const persistenceKey = options.persistenceKey.trim();
  const standardIds = Array.from(new Set(options.standardIds.map(normalizeStandardId)));
  const persistenceStore = createLocalFlatSqlPersistenceStore(options);
  if (!persistenceKey || standardIds.length === 0 || !persistenceStore.available) return;
  for (const standardId of standardIds) {
    const persistedSources = normalizePersistedSourceNames(
      await persistenceStore.readJson(persistedSourcesKey(persistenceKey, standardId)),
    );
    for (const source of persistedSources) {
      await persistenceStore.deleteKey(persistedSourceStreamKey(persistenceKey, standardId, source));
    }
    await persistenceStore.deleteKey(persistedSourcesKey(persistenceKey, standardId));
    await persistenceStore.deleteKey(persistedStandardKey(persistenceKey, standardId));
    await persistenceStore.deleteKey(persistedRecordKey(persistenceKey, standardId));
  }
  const entries = normalizePersistedPinLedgerEntries(await persistenceStore.readJson(persistedPinLedgerKey(persistenceKey)))
    .filter((entry) => !standardIds.includes(normalizeStandardId(entry.standardId || entry.schemaName)));
  if (entries.length > 0) {
    await persistenceStore.writeJson(persistedPinLedgerKey(persistenceKey), entries);
  } else {
    await persistenceStore.deleteKey(persistedPinLedgerKey(persistenceKey));
  }
}

export function stripSdnFlatBufferSizePrefix(bytes: Uint8Array): Uint8Array {
  if (bytes.byteLength < 12) return bytes;
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const littleEndianLength = view.getUint32(0, true);
  const bigEndianLength = view.getUint32(0, false);
  const payloadLength = bytes.byteLength - 4;
  if (
    (littleEndianLength === payloadLength || bigEndianLength === payloadLength) &&
    hasFlatBufferFileIdentifier(bytes, 8)
  ) {
    return bytes.subarray(4);
  }
  return bytes;
}

class WasmLocalFlatSqlStore implements LocalFlatSqlStore {
  constructor(
    private readonly states: Map<string, StandardDatabaseState>,
    private readonly persistenceKey: string | null,
    private readonly pinLedger: Map<string, LocalFlatSqlPinLedgerEntry>,
    private readonly persistenceStore: LocalFlatSqlPersistenceStore,
    private readonly flatsql: FlatSQL,
  ) {}

  async ingestRecords(
    standardId: string,
    records: LocalFlatSqlIngestRecord[],
    sourceOrOptions?: string | LocalFlatSqlIngestOptions | null,
  ): Promise<number> {
    const options = normalizeIngestOptions(sourceOrOptions);
    const state = this.stateForStandard(standardId);
    const nextRecords = records.filter((record) => !state.ingestedKeys.has(recordIngestKey(record)));
    const recordsWithBytes = nextRecords.filter((record) => record.dataBytes instanceof Uint8Array);
    const buffers = recordsWithBytes.map((record) => stripSdnFlatBufferSizePrefix(record.dataBytes as Uint8Array));
    if (buffers.length === 0) return 0;

    const source = this.ensureSource(state, options.source);
    const beforeRecordCount = recordCountForState(state);
    state.db.ingestBuffers(buffers, source);
    const count = Math.max(0, recordCountForState(state) - beforeRecordCount);
    for (const record of recordsWithBytes.slice(0, count)) state.ingestedKeys.add(recordIngestKey(record));
    state.dirty = true;
    if (options.persist !== false) await this.persistStandard(state);
    return count;
  }

  async ingestFlatBufferStream(
    standardId: string,
    streamBytes: Uint8Array,
    options: LocalFlatSqlStreamIngestOptions | null = null,
  ): Promise<number> {
    const state = this.stateForStandard(standardId);
    const streamInfo = flatSqlSizePrefixedStreamInfo(streamBytes, options?.skipRecords ?? 0);
    if (streamInfo.ingestRecordCount === 0) return 0;

    const keyOffset = Math.max(0, options?.recordKeyOffset ?? options?.skipRecords ?? 0);
    const directRecordKeys = directFlatSqlStreamRecordKeys(
      standardId,
      streamInfo.ingestRecordCount,
      keyOffset,
      options,
    );
    const trustOrderedPublishedOffsets = isOrderedPublishedShardIngest(options);
    if (
      directRecordKeys &&
      streamInfo.allFramesHaveDirectFileIdentifier &&
      (
        trustOrderedPublishedOffsets ||
        directRecordKeys.every((key) => !state.ingestedKeys.has(key))
      )
    ) {
      const directStreamBytes = streamInfo.ingestStartOffset === 0
        ? streamBytes
        : streamBytes.slice(streamInfo.ingestStartOffset);
      const directSource = this.ensureSource(state, options?.source);
      const beforeRecordCount = recordCountForState(state);
      state.db.ingest(directStreamBytes, directSource);
      const directIngestCount = Math.max(0, recordCountForState(state) - beforeRecordCount);
      for (const key of directRecordKeys.slice(0, Math.max(0, directIngestCount))) {
        state.ingestedKeys.add(key);
      }
      state.dirty = true;
      if (directIngestCount > 0 && options?.pinLedgerEntries?.length) {
        await this.recordPinLedgerEntries(options.pinLedgerEntries);
      }
      if (options?.persist !== false) await this.persistStandard(state);
      return directIngestCount;
    }

    const streamRecords = decodeFlatSqlSizePrefixedStream(streamBytes, options?.skipRecords ?? 0);
    const candidates = streamRecords.map((buffer, index) => ({
      buffer,
      key: flatSqlStreamRecordKey(standardId, buffer, index + keyOffset, options),
    }));
    const nextRecords = trustOrderedPublishedOffsets
      ? candidates
      : filterNewFlatSqlRecordCandidates(candidates, state.ingestedKeys);
    if (nextRecords.length === 0) return 0;

    const source = this.ensureSource(state, options?.source);
    const beforeRecordCount = recordCountForState(state);
    state.db.ingestBuffers(nextRecords.map((entry) => stripSdnFlatBufferSizePrefix(entry.buffer)), source);
    const ingestedCount = Math.max(0, recordCountForState(state) - beforeRecordCount);
    for (const entry of nextRecords.slice(0, ingestedCount)) state.ingestedKeys.add(entry.key);
    state.dirty = true;
    if (ingestedCount > 0 && options?.pinLedgerEntries?.length) {
      await this.recordPinLedgerEntries(options.pinLedgerEntries);
    }
    if (options?.persist !== false) await this.persistStandard(state);
    return ingestedCount;
  }

  async clearStandard(standardId: string, options: LocalFlatSqlClearOptions = {}): Promise<void> {
    const state = this.stateForStandard(standardId);
    const previouslyPersistedSources = state.persistedSources;
    state.db.destroy();
    state.db = this.createDatabaseForSchema(state.schema);
    state.ingestedKeys.clear();
    state.sources = new Set<string>();
    state.persistedSources = [];
    state.cachedBytes = 0;
    state.dirty = false;
    this.deletePinLedgerEntriesForStandard(state.schema.standardId);
    if (options.persist !== false) {
      await this.deletePersistedStandard(state.schema.standardId, previouslyPersistedSources);
    }
  }

  async createStandardReplacementStore(standardId: string): Promise<LocalFlatSqlStore> {
    const state = this.stateForStandard(standardId);
    const replacementState: StandardDatabaseState = {
      schema: { ...state.schema },
      db: this.createDatabaseForSchema(state.schema),
      ingestedKeys: new Set<string>(),
      sources: new Set<string>(),
      persistedSources: [],
      cachedBytes: 0,
      dirty: false,
    };
    return new WasmLocalFlatSqlStore(
      new Map([[state.schema.standardId, replacementState]]),
      null,
      new Map<string, LocalFlatSqlPinLedgerEntry>(),
      this.persistenceStore,
      this.flatsql,
    );
  }

  async replaceStandardFrom(
    standardId: string,
    replacementStore: LocalFlatSqlStore,
    entries: LocalFlatSqlPinLedgerEntry[],
    options: LocalFlatSqlPinLedgerWriteOptions = {},
  ): Promise<void> {
    if (!(replacementStore instanceof WasmLocalFlatSqlStore)) {
      throw new Error('FlatSQL replacement store must use the local WASM implementation');
    }
    const activeState = this.stateForStandard(standardId);
    const replacementState = replacementStore.stateForStandard(standardId);
    activeState.db.destroy();
    activeState.db = replacementState.db;
    activeState.ingestedKeys = new Set(replacementState.ingestedKeys);
    activeState.sources = new Set(replacementState.sources);
    activeState.cachedBytes = replacementState.db.exportData().byteLength;
    activeState.dirty = false;
    replacementState.db = replacementStore.createDatabaseForSchema(replacementState.schema);
    replacementState.ingestedKeys.clear();
    replacementState.sources = new Set<string>();
    replacementState.persistedSources = [];
    replacementState.cachedBytes = 0;
    replacementState.dirty = false;
    this.deletePinLedgerEntriesForStandard(activeState.schema.standardId);
    await this.recordPinLedgerEntries(entries, { persist: false });
    if (options.persist !== false) {
      await this.persistStandard(activeState);
      await this.persistPinLedger();
    }
  }

  async flush(standardId?: string): Promise<void> {
    if (standardId) {
      await this.persistStandard(this.stateForStandard(standardId));
      await this.persistPinLedger();
      return;
    }
    for (const state of this.states.values()) {
      await this.persistStandard(state);
    }
    await this.persistPinLedger();
  }

  async recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[], options: LocalFlatSqlPinLedgerWriteOptions = {}): Promise<void> {
    for (const candidate of entries) {
      const entry = normalizePinLedgerEntry(candidate);
      if (!entry.cid || !entry.standardId || !entry.schemaName || !entry.role || !entry.verificationState) continue;
      this.pinLedger.set(pinLedgerEntryKey(entry), entry);
    }
    if (options.persist !== false) await this.persistPinLedger();
  }

  async listPinLedgerEntries(query: LocalFlatSqlPinLedgerQuery = {}): Promise<LocalFlatSqlPinLedgerEntry[]> {
    const normalizedQuery = normalizePinLedgerQuery(query);
    return Array.from(this.pinLedger.values())
      .filter((entry) => pinLedgerEntryMatches(entry, normalizedQuery))
      .sort((left, right) => {
        const byTime = (right.updatedAt ?? '').localeCompare(left.updatedAt ?? '');
        if (byTime !== 0) return byTime;
        return left.cid.localeCompare(right.cid);
      })
      .map((entry) => ({ ...entry }));
  }

  listSources(standardId: string): string[] {
    return Array.from(this.stateForStandard(standardId).sources).sort();
  }

  query(sql: string, standardId?: string, options: LocalFlatSqlQueryOptions = {}): LocalFlatSqlQueryResult {
    const validation = validateReadOnlySql(sql, options);
    if (!validation.ok) {
      throw new Error(`FlatSQL local queries must be read-only SELECT or WITH SELECT statements: ${validation.diagnostics.join(' ')}`);
    }
    const state = standardId ? this.stateForStandard(standardId) : this.firstState();
    const startedAt = nowMs();
    const result = state.db.query(validation.sql);
    const queryResult = {
      columns: result.columns,
      rows: result.rows,
      records: result.rows.map((row) => rowToRecord(result.columns, row)),
    };
    enforceQueryRuntimeLimits(queryResult, validation.limits?.maxBytes ?? 64_000, validation.limits?.timeoutMs ?? 5_000, nowMs() - startedAt);
    return queryResult;
  }

  getStats(options: LocalFlatSqlStatsOptions = {}): LocalFlatSqlStandardStats[] {
    return Array.from(this.states.values()).map((state) => {
      const pinStats = this.pinStatsForStandard(state.schema.standardId);
      return {
        standardId: state.schema.standardId,
        tableName: state.schema.tableName,
        recordCount: recordCountForState(state),
        cachedBytes: this.cachedBytesForState(state, options.includeCachedBytes !== false),
        ingestedRecordCount: state.ingestedKeys.size,
        ...pinStats,
      };
    });
  }

  destroy(): void {
    for (const state of this.states.values()) {
      state.db.destroy();
    }
    this.states.clear();
  }

  private firstState(): StandardDatabaseState {
    const first = this.states.values().next().value as StandardDatabaseState | undefined;
    if (!first) throw new Error('No FlatSQL standards are registered');
    return first;
  }

  private stateForStandard(standardId: string): StandardDatabaseState {
    const normalized = normalizeStandardId(standardId);
    const state = this.states.get(normalized);
    if (!state) throw new Error(`No FlatSQL schema registered for ${normalized}`);
    return state;
  }

  /**
   * Register a source partition on first use and rebuild the unified views
   * (mirrors sdn-server ensureEngineSource, engine_records.go). Every ingest
   * is source-routed; records without an explicit source land in the
   * server's default `local` partition.
   */
  private ensureSource(state: StandardDatabaseState, source?: string | null): string {
    const normalized = normalizeSourceName(source);
    if (!state.sources.has(normalized)) {
      state.db.registerSource(normalized);
      state.sources.add(normalized);
      state.db.createUnifiedViews();
      state.dirty = true;
    }
    return normalized;
  }

  private createDatabaseForSchema(schema: LocalFlatSqlSchema): FlatSQLDatabase {
    const db = this.flatsql.createDatabase(stripFlatBufferComments(schema.schema), `sdn-${schema.standardId.toLowerCase()}`);
    db.registerFileId(schema.fileId, schema.tableName);
    return db;
  }

  private async persistStandard(state: StandardDatabaseState): Promise<void> {
    // Per-(standard, source) aligned streams are the durable representation:
    // the raw record bytes of each shadow table, exactly as the engine would
    // stream them (size-prefixed frames), re-ingestible with their source at
    // the next open. A single exportData blob cannot be used here — engine
    // rebuild routes records by file id only and would lose the partitions.
    const sources = Array.from(state.sources).sort();
    const streams = new Map<string, Uint8Array>();
    let totalBytes = 0;
    for (const source of sources) {
      const bytes = state.db.queryRawFlatBufferStream(
        `SELECT _data FROM "${state.schema.tableName}@${source}" ORDER BY _rowid ASC`,
      );
      streams.set(source, bytes);
      totalBytes += bytes.byteLength;
    }
    state.cachedBytes = totalBytes;
    state.dirty = false;
    if (!this.persistenceKey) return;

    for (const [source, bytes] of streams) {
      await writePersistedFlatSqlBytes(
        this.persistenceStore,
        persistedSourceStreamKey(this.persistenceKey, state.schema.standardId, source),
        bytes,
      );
    }
    await this.persistenceStore.writeJson(persistedSourcesKey(this.persistenceKey, state.schema.standardId), sources);
    // Drop streams for sources that no longer exist plus any legacy blob.
    for (const source of state.persistedSources) {
      if (!state.sources.has(source)) {
        await this.persistenceStore.deleteKey(persistedSourceStreamKey(this.persistenceKey, state.schema.standardId, source));
      }
    }
    await this.persistenceStore.deleteKey(persistedStandardKey(this.persistenceKey, state.schema.standardId));
    state.persistedSources = sources;
    await writePersistedRecordKeys(this.persistenceStore, persistedRecordKey(this.persistenceKey, state.schema.standardId), state.ingestedKeys);
  }

  private async persistPinLedger(): Promise<void> {
    if (!this.persistenceKey) return;
    if (this.pinLedger.size === 0) {
      await this.persistenceStore.deleteKey(persistedPinLedgerKey(this.persistenceKey));
      return;
    }
    await writePersistedPinLedgerEntries(this.persistenceStore, persistedPinLedgerKey(this.persistenceKey), Array.from(this.pinLedger.values()));
  }

  private async deletePersistedStandard(standardId: string, persistedSources: string[] = []): Promise<void> {
    if (!this.persistenceKey) return;
    for (const source of persistedSources) {
      await this.persistenceStore.deleteKey(persistedSourceStreamKey(this.persistenceKey, standardId, source));
    }
    await this.persistenceStore.deleteKey(persistedSourcesKey(this.persistenceKey, standardId));
    await this.persistenceStore.deleteKey(persistedStandardKey(this.persistenceKey, standardId));
    await this.persistenceStore.deleteKey(persistedRecordKey(this.persistenceKey, standardId));
    await this.persistPinLedger();
  }

  private deletePinLedgerEntriesForStandard(standardId: string): void {
    const normalizedStandardId = normalizeStandardId(standardId);
    for (const [key, entry] of this.pinLedger.entries()) {
      if (normalizeStandardId(entry.standardId || entry.schemaName) === normalizedStandardId) {
        this.pinLedger.delete(key);
      }
    }
  }

  private cachedBytesForState(state: StandardDatabaseState, includeCachedBytes: boolean): number {
    if (!includeCachedBytes) return state.cachedBytes;
    state.cachedBytes = state.db.exportData().byteLength;
    return state.cachedBytes;
  }

  private pinStatsForStandard(standardId: string): Pick<LocalFlatSqlStandardStats, 'pinnedRows' | 'pinnedBytes' | 'snapshotId' | 'head' | 'highWaterMark' | 'lastSyncedAt'> {
    const normalizedStandardId = normalizeStandardId(standardId);
    let pinnedRows = 0;
    let pinnedBytes = 0;
    let latest: LocalFlatSqlPinLedgerEntry | null = null;
    for (const entry of this.pinLedger.values()) {
      if (normalizeStandardId(entry.standardId || entry.schemaName) !== normalizedStandardId) continue;
      if ((entry.role ?? '').trim() !== 'shard') continue;
      if ((entry.verificationState ?? '').trim() !== 'verified') continue;
      if (!entry.materializedAt?.trim()) continue;
      pinnedRows += Math.max(0, entry.rowCount ?? 0);
      pinnedBytes += Math.max(0, entry.byteCount ?? 0);
      const entryTime = entry.materializedAt || entry.verifiedAt || entry.updatedAt || '';
      const latestTime = latest?.materializedAt || latest?.verifiedAt || latest?.updatedAt || '';
      if (!latest || entryTime.localeCompare(latestTime) > 0) latest = entry;
    }
    return {
      pinnedRows,
      pinnedBytes,
      snapshotId: latest?.snapshotId || null,
      head: latest?.head || null,
      highWaterMark: latest?.highWaterMark || null,
      lastSyncedAt: latest?.materializedAt || latest?.verifiedAt || latest?.updatedAt || null,
    };
  }
}

export function isReadOnlyFlatSqlQuery(sql: string): boolean {
  return validateReadOnlySql(sql).ok;
}

function recordCountForState(state: StandardDatabaseState): number {
  return recordCountForDatabase(state.db, state.schema.tableName);
}

/**
 * Total records for a standard = base table + every `<Table>@<source>`
 * shadow table (source-routed ingest lands in the shadow tables only; the
 * base name becomes the unified view once sources exist).
 */
function recordCountForDatabase(db: FlatSQLDatabase, tableName: string): number {
  const shadowPrefix = `${tableName}@`;
  return db.getStats().reduce((total, entry) => (
    entry.tableName === tableName || entry.tableName.startsWith(shadowPrefix)
      ? total + Number(entry.recordCount ?? 0)
      : total
  ), 0);
}

function normalizeSourceName(source?: string | null): string {
  const trimmedSource = typeof source === 'string' ? source.trim() : '';
  return trimmedSource || LOCAL_FLATSQL_DEFAULT_SOURCE;
}

function normalizePersistedSourceNames(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return Array.from(new Set(
    value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0)
      .map((entry) => entry.trim()),
  )).sort();
}

function stripFlatBufferComments(schema: string): string {
  return schema
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .split('\n')
    .map((line) => line.replace(/\/\/.*$/, ''))
    .join('\n');
}

function normalizeIngestOptions(sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): LocalFlatSqlIngestOptions {
  if (!sourceOrOptions) return {};
  if (typeof sourceOrOptions === 'string') return { source: sourceOrOptions };
  return sourceOrOptions;
}

function enforceQueryRuntimeLimits(result: LocalFlatSqlQueryResult, maxBytes: number, timeoutMs: number, elapsedMs: number): void {
  if (elapsedMs > timeoutMs) {
    throw new Error(`FlatSQL local query exceeded time limit of ${timeoutMs} ms.`);
  }
  const resultBytes = estimateQueryResultBytes(result);
  if (resultBytes > maxBytes) {
    throw new Error(`FlatSQL local query exceeded byte limit of ${maxBytes} bytes.`);
  }
}

function estimateQueryResultBytes(result: LocalFlatSqlQueryResult): number {
  let total = 0;
  for (const row of result.records) {
    for (const value of Object.values(row)) {
      total += estimateValueBytes(value);
    }
  }
  return total;
}

function estimateValueBytes(value: unknown): number {
  if (value == null) return 0;
  if (typeof value === 'number') return 8;
  if (typeof value === 'boolean') return 1;
  if (typeof value === 'string') return value.length;
  if (value instanceof Uint8Array || value instanceof ArrayBuffer) return value.byteLength;
  return JSON.stringify(value)?.length ?? String(value).length;
}

function nowMs(): number {
  return typeof performance !== 'undefined' && typeof performance.now === 'function'
    ? performance.now()
    : Date.now();
}

export function decodeFlatSqlSizePrefixedStream(streamBytes: Uint8Array, skipRecords = 0): Uint8Array[] {
  const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
  const records: Uint8Array[] = [];
  let offset = 0;
  let index = 0;
  while (offset < streamBytes.byteLength) {
    if (streamBytes.byteLength - offset < 4) {
      throw new Error(`Invalid FlatSQL size-prefixed stream: truncated frame header at offset ${offset}`);
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    const nextOffset = offset + length;
    if (nextOffset > streamBytes.byteLength) {
      throw new Error(`Invalid FlatSQL size-prefixed stream: truncated frame at index ${index}`);
    }
    if (index >= skipRecords) records.push(streamBytes.subarray(offset, nextOffset));
    offset = nextOffset;
    index += 1;
  }
  return records;
}

export function flatSqlSizePrefixedStreamInfo(streamBytes: Uint8Array, skipRecords = 0): FlatSqlSizePrefixedStreamInfo {
  const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
  const normalizedSkipRecords = Math.max(0, Math.floor(skipRecords));
  let offset = 0;
  let index = 0;
  let ingestStartOffset = 0;
  let sawIngestStart = normalizedSkipRecords === 0;
  let allFramesHaveDirectFileIdentifier = true;
  while (offset < streamBytes.byteLength) {
    const frameStart = offset;
    if (streamBytes.byteLength - offset < 4) {
      throw new Error(`Invalid FlatSQL size-prefixed stream: truncated frame header at offset ${offset}`);
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    const nextOffset = offset + length;
    if (nextOffset > streamBytes.byteLength) {
      throw new Error(`Invalid FlatSQL size-prefixed stream: truncated frame at index ${index}`);
    }
    if (!hasFlatBufferFileIdentifier(streamBytes, offset + 4)) {
      allFramesHaveDirectFileIdentifier = false;
    }
    if (index === normalizedSkipRecords) {
      ingestStartOffset = frameStart;
      sawIngestStart = true;
    }
    offset = nextOffset;
    index += 1;
  }
  if (!sawIngestStart) {
    ingestStartOffset = streamBytes.byteLength;
  }
  return {
    totalRecordCount: index,
    ingestRecordCount: Math.max(0, index - normalizedSkipRecords),
    ingestStartOffset,
    allFramesHaveDirectFileIdentifier,
  };
}

function directFlatSqlStreamRecordKeys(
  standardId: string,
  recordCount: number,
  keyOffset: number,
  options: LocalFlatSqlStreamIngestOptions | null,
): string[] | null {
  if (options?.recordKeys?.length) return null;
  const prefix = options?.recordKeyPrefix?.trim();
  if (!prefix) return null;
  const normalizedStandardId = normalizeStandardId(standardId);
  return Array.from({ length: recordCount }, (_, index) => `${normalizedStandardId}|${prefix}|${index + keyOffset}`);
}

function isOrderedPublishedShardIngest(options: LocalFlatSqlStreamIngestOptions | null): boolean {
  return Boolean(options?.recordKeyPrefix?.trim().startsWith('published:'));
}

function flatSqlStreamRecordKey(
  standardId: string,
  buffer: Uint8Array,
  index: number,
  options: LocalFlatSqlStreamIngestOptions | null,
): string {
  const explicitKey = options?.recordKeys?.[index - Math.max(0, options.recordKeyOffset ?? options.skipRecords ?? 0)]?.trim();
  if (explicitKey) return explicitKey;
  const prefix = options?.recordKeyPrefix?.trim();
  if (prefix) return `${normalizeStandardId(standardId)}|${prefix}|${index}`;
  return `${normalizeStandardId(standardId)}|stream|${buffer.byteLength}|${fnv1a32(buffer).toString(16).padStart(8, '0')}`;
}

function filterNewFlatSqlRecordCandidates<T extends { key: string }>(
  candidates: T[],
  ingestedKeys: ReadonlySet<string>,
): T[] {
  const seen = new Set(ingestedKeys);
  const next: T[] = [];
  for (const candidate of candidates) {
    if (seen.has(candidate.key)) continue;
    seen.add(candidate.key);
    next.push(candidate);
  }
  return next;
}

function fnv1a32(bytes: Uint8Array): number {
  let hash = 0x811c9dc5;
  for (const byte of bytes) {
    hash ^= byte;
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

function rowToRecord(columns: string[], row: unknown[]): Record<string, unknown> {
  const record: Record<string, unknown> = {};
  columns.forEach((column, index) => {
    record[column] = row[index];
  });
  return record;
}

function normalizeStandardId(value: string): string {
  const standardId = value.trim().split('.')[0]?.toUpperCase() ?? '';
  if (!standardId) throw new Error('FlatSQL standard id is required');
  return standardId;
}

function hasFlatBufferFileIdentifier(bytes: Uint8Array, offset: number): boolean {
  if (bytes.byteLength < offset + 4) return false;
  for (let index = offset; index < offset + 4; index += 1) {
    const value = bytes[index];
    if (
      value !== 36 &&
      value !== 95 &&
      !(value >= 48 && value <= 57) &&
      !(value >= 65 && value <= 90) &&
      !(value >= 97 && value <= 122)
    ) {
      return false;
    }
  }
  return true;
}

function persistedStandardKey(persistenceKey: string, standardId: string): string {
  return `${persistenceKey}:${standardId}`;
}

function persistedRecordKey(persistenceKey: string, standardId: string): string {
  return `${persistenceKey}:${standardId}:record-keys`;
}

function persistedPinLedgerKey(persistenceKey: string): string {
  return `${persistenceKey}:pin-ledger`;
}

function persistedSourcesKey(persistenceKey: string, standardId: string): string {
  return `${persistenceKey}:${standardId}:sources`;
}

function persistedSourceStreamKey(persistenceKey: string, standardId: string, source: string): string {
  return `${persistenceKey}:${standardId}:src:${source}`;
}

function recordIngestKey(record: LocalFlatSqlIngestRecord): string {
  return [
    record.schemaName,
    record.cid,
    record.providerId ?? '',
    record.sourceName ?? '',
    record.batchId ?? '',
    record.timestamp ?? '',
  ].join('|');
}

function normalizePinLedgerEntry(candidate: LocalFlatSqlPinLedgerEntry): LocalFlatSqlPinLedgerEntry {
  const standardId = normalizeStandardId(candidate.standardId || candidate.schemaName);
  const schemaName = normalizeSchemaName(candidate.schemaName || standardId);
  const updatedAt = trimmed(candidate.updatedAt) || new Date().toISOString();
  return {
    cid: trimmed(candidate.cid),
    standardId,
    schemaName,
    providerPeerId: trimmed(candidate.providerPeerId),
    providerPublicKey: trimmed(candidate.providerPublicKey),
    providerId: trimmed(candidate.providerId),
    sourceName: trimmed(candidate.sourceName),
    batchId: trimmed(candidate.batchId),
    queryProfile: trimmed(candidate.queryProfile),
    snapshotId: trimmed(candidate.snapshotId),
    head: trimmed(candidate.head),
    highWaterMark: trimmed(candidate.highWaterMark),
    byteHash: trimmed(candidate.byteHash),
    role: trimmed(candidate.role),
    rowCount: normalizedOptionalNumber(candidate.rowCount),
    byteCount: normalizedOptionalNumber(candidate.byteCount),
    ttlSeconds: normalizedOptionalNumber(candidate.ttlSeconds),
    verificationState: trimmed(candidate.verificationState),
    materializedAt: trimmed(candidate.materializedAt),
    verifiedAt: trimmed(candidate.verifiedAt),
    updatedAt,
  };
}

function normalizePinLedgerQuery(query: LocalFlatSqlPinLedgerQuery): LocalFlatSqlPinLedgerQuery {
  return {
    cid: trimmed(query.cid),
    standardId: query.standardId || query.schemaName ? normalizeStandardId(query.standardId || query.schemaName || '') : '',
    schemaName: query.schemaName ? normalizeSchemaName(query.schemaName) : '',
    providerPeerId: trimmed(query.providerPeerId),
    providerPublicKey: trimmed(query.providerPublicKey),
    providerId: trimmed(query.providerId),
    sourceName: trimmed(query.sourceName),
    batchId: trimmed(query.batchId),
    queryProfile: trimmed(query.queryProfile),
    role: trimmed(query.role),
    verificationState: trimmed(query.verificationState),
  };
}

function pinLedgerEntryMatches(entry: LocalFlatSqlPinLedgerEntry, query: LocalFlatSqlPinLedgerQuery): boolean {
  return matchesOptional(entry.cid, query.cid) &&
    matchesOptional(entry.standardId, query.standardId) &&
    matchesOptional(entry.schemaName, query.schemaName) &&
    matchesOptional(entry.providerPeerId, query.providerPeerId) &&
    matchesOptional(entry.providerPublicKey, query.providerPublicKey) &&
    matchesOptional(entry.providerId, query.providerId) &&
    matchesOptional(entry.sourceName, query.sourceName) &&
    matchesOptional(entry.batchId, query.batchId) &&
    matchesOptional(entry.queryProfile, query.queryProfile) &&
    matchesOptional(entry.role, query.role) &&
    matchesOptional(entry.verificationState, query.verificationState);
}

function pinLedgerEntryKey(entry: LocalFlatSqlPinLedgerEntry): string {
  return [
    entry.cid,
    entry.standardId,
    entry.schemaName,
    entry.providerPeerId ?? '',
    entry.providerPublicKey ?? '',
    entry.providerId ?? '',
    entry.sourceName ?? '',
    entry.batchId ?? '',
    entry.queryProfile ?? '',
    entry.role,
  ].join('|');
}

function normalizeSchemaName(value: string): string {
  const trimmedValue = value.trim();
  if (!trimmedValue) throw new Error('FlatSQL schema name is required');
  return trimmedValue.endsWith('.fbs') ? trimmedValue : `${normalizeStandardId(trimmedValue)}.fbs`;
}

function trimmed(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function normalizedOptionalNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
}

function matchesOptional(value: string | null | undefined, queryValue: string | null | undefined): boolean {
  return !queryValue || (value ?? '') === queryValue;
}

/**
 * Resolve the default persistence backend the engine store would use for the
 * given options (injected store > desktop endpoint > IndexedDB).
 */
export function createDefaultLocalFlatSqlPersistenceStore(
  options: Pick<LocalFlatSqlStoreOptions, 'desktopPersistenceBaseUrl' | 'fetch' | 'persistenceStore'> = {},
): LocalFlatSqlPersistenceStore {
  return createLocalFlatSqlPersistenceStore(options);
}

function createLocalFlatSqlPersistenceStore(options: Pick<LocalFlatSqlStoreOptions, 'desktopPersistenceBaseUrl' | 'fetch' | 'persistenceStore'>): LocalFlatSqlPersistenceStore {
  if (options.persistenceStore) return options.persistenceStore;
  const desktopBaseUrl = options.desktopPersistenceBaseUrl?.trim();
  const fetchLike = options.fetch ?? (typeof fetch !== 'undefined' ? fetch.bind(globalThis) : null);
  if (desktopBaseUrl && fetchLike) {
    return new DesktopFlatSqlPersistenceStore(desktopBaseUrl, fetchLike);
  }
  return new IndexedDbFlatSqlPersistenceStore();
}

class DesktopFlatSqlPersistenceStore implements LocalFlatSqlPersistenceStore {
  readonly available = true;
  private readonly endpoint: string;

  constructor(baseUrl: string, private readonly fetchLike: FetchLike) {
    this.endpoint = desktopFlatSqlPersistenceEndpoint(baseUrl);
  }

  async readBytes(key: string): Promise<Uint8Array | null> {
    const response = await this.fetchLike(this.urlFor(key), { method: 'GET' });
    if (response.status === 404) return null;
    if (!response.ok) return null;
    return new Uint8Array(await response.arrayBuffer());
  }

  async writeBytes(key: string, bytes: Uint8Array): Promise<void> {
    const response = await this.fetchLike(this.urlFor(key), {
      method: 'PUT',
      headers: { 'content-type': 'application/octet-stream' },
      body: bytes.slice(),
    });
    if (!response.ok) throw new Error(`FlatSQL desktop persistence write failed with HTTP ${response.status}`);
  }

  async readJson(key: string): Promise<unknown> {
    const bytes = await this.readBytes(key);
    if (!bytes) return null;
    try {
      return JSON.parse(new TextDecoder().decode(bytes));
    } catch {
      return null;
    }
  }

  async writeJson(key: string, value: unknown): Promise<void> {
    await this.writeBytes(key, new TextEncoder().encode(JSON.stringify(value)));
  }

  async deleteKey(key: string): Promise<void> {
    const response = await this.fetchLike(this.urlFor(key), { method: 'DELETE' });
    if (!response.ok && response.status !== 404) {
      throw new Error(`FlatSQL desktop persistence delete failed with HTTP ${response.status}`);
    }
  }

  private urlFor(key: string): string {
    return `${this.endpoint}/${encodeURIComponent(key)}`;
  }
}

class IndexedDbFlatSqlPersistenceStore implements LocalFlatSqlPersistenceStore {
  readonly available = hasIndexedDb();

  async readBytes(key: string): Promise<Uint8Array | null> {
    const value = await this.readValue(key);
    if (value instanceof ArrayBuffer) return new Uint8Array(value);
    if (value instanceof Uint8Array) return value;
    return null;
  }

  async writeBytes(key: string, bytes: Uint8Array): Promise<void> {
    await this.writeValue(key, bytes.slice().buffer);
  }

  async readJson(key: string): Promise<unknown> {
    const value = await this.readValue(key);
    if (value instanceof ArrayBuffer || value instanceof Uint8Array) {
      const bytes = value instanceof Uint8Array ? value : new Uint8Array(value);
      try {
        return JSON.parse(new TextDecoder().decode(bytes));
      } catch {
        return null;
      }
    }
    return value;
  }

  async writeJson(key: string, value: unknown): Promise<void> {
    await this.writeValue(key, value);
  }

  async deleteKey(key: string): Promise<void> {
    if (!this.available) return;
    const db = await openLocalFlatSqlDb();
    await new Promise<void>((resolve) => {
      const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readwrite');
      transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).delete(key);
      transaction.oncomplete = () => {
        db.close();
        resolve();
      };
      transaction.onerror = () => {
        db.close();
        resolve();
      };
    });
  }

  private async readValue(key: string): Promise<unknown> {
    if (!this.available) return null;
    const db = await openLocalFlatSqlDb();
    return await new Promise((resolve) => {
      const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readonly');
      const request = transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).get(key);
      request.onerror = () => resolve(null);
      request.onsuccess = () => resolve(request.result ?? null);
      transaction.oncomplete = () => db.close();
      transaction.onerror = () => {
        db.close();
        resolve(null);
      };
    });
  }

  private async writeValue(key: string, value: unknown): Promise<void> {
    if (!this.available) return;
    const db = await openLocalFlatSqlDb();
    await new Promise<void>((resolve) => {
      const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readwrite');
      transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).put(value, key);
      transaction.oncomplete = () => {
        db.close();
        resolve();
      };
      transaction.onerror = () => {
        db.close();
        resolve();
      };
    });
  }
}

function desktopFlatSqlPersistenceEndpoint(baseUrl: string): string {
  const trimmed = baseUrl.trim().replace(/\/+$/, '');
  return trimmed.endsWith('/api/flatsql/persistence') ? trimmed : `${trimmed}/api/flatsql/persistence`;
}

async function readPersistedFlatSqlBytes(store: LocalFlatSqlPersistenceStore, key: string): Promise<Uint8Array | null> {
  return store.readBytes(key);
}

async function writePersistedFlatSqlBytes(store: LocalFlatSqlPersistenceStore, key: string, bytes: Uint8Array): Promise<void> {
  await store.writeBytes(key, bytes);
}

async function readPersistedRecordKeys(store: LocalFlatSqlPersistenceStore, key: string): Promise<Set<string>> {
  const value = await store.readJson(key);
  return new Set(Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string') : []);
}

async function writePersistedRecordKeys(store: LocalFlatSqlPersistenceStore, key: string, keys: Set<string>): Promise<void> {
  await store.writeJson(key, Array.from(keys));
}

async function readPersistedPinLedgerEntries(store: LocalFlatSqlPersistenceStore, key: string): Promise<Map<string, LocalFlatSqlPinLedgerEntry>> {
  const entries = new Map<string, LocalFlatSqlPinLedgerEntry>();
  for (const entry of normalizePersistedPinLedgerEntries(await store.readJson(key))) {
    entries.set(pinLedgerEntryKey(entry), entry);
  }
  return entries;
}

async function writePersistedPinLedgerEntries(store: LocalFlatSqlPersistenceStore, key: string, entries: LocalFlatSqlPinLedgerEntry[]): Promise<void> {
  await store.writeJson(key, entries.map(normalizePinLedgerEntry));
}

function normalizePersistedPinLedgerEntries(value: unknown): LocalFlatSqlPinLedgerEntry[] {
  if (!Array.isArray(value)) return [];
  const entries: LocalFlatSqlPinLedgerEntry[] = [];
  for (const candidate of value) {
    if (!candidate || typeof candidate !== 'object') continue;
    try {
      entries.push(normalizePinLedgerEntry(candidate as LocalFlatSqlPinLedgerEntry));
    } catch {
      // Ignore malformed local cache metadata.
    }
  }
  return entries;
}

function openLocalFlatSqlDb(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(LOCAL_FLATSQL_DB_NAME, 1);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB open failed'));
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(LOCAL_FLATSQL_STORE_NAME)) {
        db.createObjectStore(LOCAL_FLATSQL_STORE_NAME);
      }
    };
    request.onsuccess = () => resolve(request.result);
  });
}

function hasIndexedDb(): boolean {
  return typeof indexedDB !== 'undefined';
}
