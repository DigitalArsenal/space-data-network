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
 * Persistence (browser durability): THE ENGINE ITSELF IS DISK-BACKED. Every
 * standard's database is opened at a PATH (`openDatabase`) inside this store's
 * namespace on the shared seven-import I/O router, so the engine writes its own
 * arena (`<path>.fsdata`) and index (`<path>`) through the node's persistence
 * store — IndexedDB `sdn-local-flatsql`/`datastores` by default, or a desktop
 * HTTP persistence endpoint. Boot is `hydrate -> openDatabase -> registerFileId
 * -> openState`, and flush is `flushIndex() -> await io.flushFor(path)`: exactly the
 * sequence the Go host runs through `internal/flatsqlrt/hostio.go`. One engine,
 * one durability story.
 *
 * SOURCE PARTITIONS ARE DURABLE TOO (flatsql >= 1.4.5). `_flatsql_sources` and
 * `_flatsql_source_ranges` live in the index file, so `openState` restores every
 * `<Table>@<source>` shadow table, re-binds the vtab modules and re-creates the
 * unified views with NOTHING re-registered. That is what retired the old
 * snapshot-export path (per-(standard, source) `SELECT _data FROM
 * "Table@source"` blobs re-ingested at boot): it cost a whole-dataset rewrite
 * per flush and a full re-ingest per boot, and it existed only because
 * flatsql <= 1.4.4 restored base tables alone.
 *
 * Two legacy layouts are still READ, once, and deleted after they are folded
 * into the engine's own state: the per-source streams above, and the pre-D.1
 * single `exportData` blob (migrated into the default `local` partition).
 * Neither is ever written again.
 */
import type { FlatSQL, FlatSQLDatabase, QueryParam } from 'flatsql/wasm';

import {
  resolveEngineEpochQuery,
  DEFAULT_ENGINE_EPOCH_SPECS,
  type EngineEpochProfileSpec,
  type EngineEpochQueryProfilesConfig,
  type EngineEpochQueryRequest,
} from './epoch-query-sql';
import { validateReadOnlySql, type ReadOnlySqlValidationOptions } from './read-only-sql-sandbox';
import {
  FlatSqlIoRouter,
  createFlatSqlIoStoreBackend,
  hydrateFlatSqlDatabase,
  restoreFlatSqlState,
  flatSqlDurablePaths,
  type DurableFlatSqlDatabase,
} from './flatsql-io-store';

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
  /**
   * Engine epoch profile columns for this standard (loop D.2). Overrides /
   * supplements DEFAULT_ENGINE_EPOCH_SPECS so CAT/MPE/SPW can opt into the
   * engine-native epoch profiles through configuration, not code.
   */
  epochSpec?: {
    partitionColumn?: string;
    epochColumn?: string;
  } | null;
}

/**
 * Default source partition for records stored without an explicit source —
 * mirrors sdn-server's engineDefaultSource (engine_records.go).
 */
export const LOCAL_FLATSQL_DEFAULT_SOURCE = 'local';

/**
 * How the shared engine instance is initialised (dashboard window model,
 * owner ruling 2026-09-03). The FIRST store opened in a JS context decides:
 * with `wasmPath` the engine is fetched from that directory with integrity
 * REQUIRED; without it, the Node/vitest lane loads the wasm beside flatsql's
 * own index.js with the integrity check skipped, exactly as before.
 */
export interface LocalFlatSqlEngineOptions {
  /**
   * Absolute URL of the directory serving `flatsql.wasm` and `integrity.json`
   * (the node serves both same-origin under `/sdn-js`). flatsql fetches
   * `${wasmPath}/integrity.json`, then `${wasmPath}/flatsql.wasm`, verifies
   * the SHA-384 and fails closed on a mismatch or a missing manifest. MUST be
   * absolute: inside a blob: worker a relative URL has nothing to resolve
   * against.
   */
  wasmPath?: string | null;
  /**
   * SHA-384 provider for the integrity check (base64 digest or raw digest
   * bytes). Defaults to WebCrypto (`crypto.subtle.digest('SHA-384')`) when
   * the context has it; functions do not survive structured clone, so the
   * worker lane always relies on that default.
   */
  computeSHA384?: (data: ArrayBuffer) => Promise<Uint8Array | string> | Uint8Array | string;
}

export interface LocalFlatSqlStoreOptions {
  schemas: LocalFlatSqlSchema[];
  /** Engine initialisation; honoured by the first store opened in this JS context. */
  engine?: LocalFlatSqlEngineOptions | null;
  persistenceKey?: string | null;
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
  /** Injectable persistence backend (tests / embedders). Defaults to IndexedDB or the desktop endpoint. */
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
  /**
   * Per-standard default query profiles (loop D.2) — the SAME shape the
   * retrieval module reads through plugin.getConfig, keyed by schema name
   * (`OMM.fbs`) or standard id (`OMM`). Request fields override these;
   * whatever is still unset falls back to the compiled defaults
   * (`nearest`, epoch = now, limit 50000).
   */
  queryProfiles?: EngineEpochQueryProfilesConfig | null;
  /** Clock used when a query epoch is neither requested nor configured (tests). */
  nowSeconds?: (() => number) | null;
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
  /**
   * Open one more standard on this store, on demand (dashboard window model:
   * any of the node's standards, opened when its screen is first shown).
   * Idempotent per standard id; the open sequence is byte-identical to the
   * constructor's.
   */
  addStandard(schema: LocalFlatSqlSchema): Promise<void>;
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
  /**
   * PRIMARY query path (loop D.2): run an engine-native epoch profile
   * (`nearest` / `as_of` / `forward` — the server's retrieval profiles,
   * SQL byte-identical to sdn-server engine_records.go) over the unified
   * per-standard view and return the ALIGNED size-prefixed FlatBuffer frame
   * stream — the wire format, zero-copy out of the engine.
   */
  queryEpochRawStream?(standardId: string, request?: EngineEpochQueryRequest | null): Uint8Array | Promise<Uint8Array>;
  /**
   * Generic aligned-raw-stream query (server mirror of
   * FlatSQLStore.QueryRawStream): read-only SQL whose result cells are all
   * BLOBs (`SELECT _data FROM ...`), executed verbatim with positional
   * params. Returns the aligned size-prefixed frame stream.
   */
  queryRawFlatBufferStream?(standardId: string, sql: string, params?: QueryParam[]): Uint8Array | Promise<Uint8Array>;
  getStats(options?: LocalFlatSqlStatsOptions): LocalFlatSqlStandardStats[] | Promise<LocalFlatSqlStandardStats[]>;
  destroy(): void;
}

interface StandardDatabaseState {
  schema: LocalFlatSqlSchema;
  db: FlatSQLDatabase & DurableFlatSqlDatabase;
  /** Durable database path in the I/O router namespace; null = ephemeral engine. */
  dbPath: string | null;
  ingestedKeys: Set<string>;
  /** Source partitions on this database (shadow tables `<Table>@<source>`), restored by openState. */
  sources: Set<string>;
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

/**
 * The PRODUCTION I/O router (loop: sdn-js-flatsql-io-shim-not-wired-in-prod).
 *
 * There is exactly one wasm instance per JS context, therefore exactly one
 * import object, therefore the seven `flatsql_io_*` imports must be installed
 * ONCE at shared-init time — before any database exists. Until this was wired,
 * `initFlatSQL` was called with no `io:` at all, so the browser lane had NO
 * filesystem: `openDatabase(path)` reported FLATSQL_STATE_NO_FILESYSTEM and
 * every "durable" path was a name with nothing behind it, while the Go host
 * reached real files through `internal/flatsqlrt/hostio.go`. Same engine, two
 * different durability stories — the divergence the disk-backed law exists to
 * forbid.
 *
 * Stores register their own persistence backend under a disjoint path prefix
 * (registerFlatSqlIoStore), so several stores share the one instance.
 */
let sharedFlatSqlIoRouter: FlatSqlIoRouter | null = null;
const registeredIoPrefixes = new Set<string>();

/** The process-wide FlatSQL I/O router, created on first use. */
export function getSharedFlatSqlIoRouter(): FlatSqlIoRouter {
  if (!sharedFlatSqlIoRouter) sharedFlatSqlIoRouter = new FlatSqlIoRouter();
  return sharedFlatSqlIoRouter;
}

/** Path prefix owned by one persistence key inside the router namespace. */
export function flatSqlIoPrefixForKey(persistenceKey: string): string {
  return `sdn-flatsql/${persistenceKey}/`;
}

/**
 * Mount a persistence store as a durable FlatSQL backend.
 *
 * Idempotent per prefix: the router keeps entries forever (there is one wasm
 * instance for the life of the context), so re-registering the same prefix
 * would shadow rather than replace.
 */
export function registerFlatSqlIoStore(
  persistenceKey: string,
  store: LocalFlatSqlPersistenceStore,
): string {
  const prefix = flatSqlIoPrefixForKey(persistenceKey);
  if (!registeredIoPrefixes.has(prefix)) {
    getSharedFlatSqlIoRouter().register(prefix, createFlatSqlIoStoreBackend(store, {
      prefix: `${prefix}keys/`,
    }));
    registeredIoPrefixes.add(prefix);
  }
  return prefix;
}

/**
 * Durable database path for one standard inside a persistence key's namespace.
 * The engine owns `<path>` (index), `<path>.fsdata` (arena) and
 * `<path>-journal`; nothing else in the store addresses those keys.
 */
export function flatSqlDatabasePathForKey(persistenceKey: string, standardId: string): string {
  return `${flatSqlIoPrefixForKey(persistenceKey)}${normalizeStandardId(standardId).toLowerCase()}.db`;
}

/**
 * SQLite journal mode for the browser lane: 2 = TRUNCATE. WAL (1) needs
 * xShmMap shared memory that neither wasm lane provides (flatsql
 * docs/STORAGE-DURABILITY.md §3.5), which is precisely why the Go host runs
 * the same value.
 */
const FLATSQL_JOURNAL_TRUNCATE = 2;

type EngineDatabase = FlatSQLDatabase & DurableFlatSqlDatabase;

/**
 * THE boot sequence for one standard, in one place because every caller must
 * perform it identically (mirrors sdn-server flatsqlrt open):
 *
 *   hydrate every durable file -> openDatabase(path) -> registerFileId ->
 *   openState (restores base tables AND source partitions AND unified views)
 *   -> session PRAGMAs
 *
 * `registerFileId` before `openState` is a contract, not a preference: replay
 * routes frames by file identifier, and a database that has not been told its
 * mapping restores nothing.
 */
async function openDurableStandardDatabase(
  flatsql: FlatSQL,
  io: FlatSqlIoRouter,
  schema: LocalFlatSqlSchema,
  dbPath: string,
): Promise<{ db: EngineDatabase; restoredRecords: number; rederived: boolean }> {
  await hydrateFlatSqlDatabase(io, dbPath);
  const db = (flatsql as unknown as { openDatabase(schema: string, name: string, path: string, journalMode: number): EngineDatabase })
    .openDatabase(
      stripFlatBufferComments(schema.schema),
      `sdn-${normalizeStandardId(schema.standardId).toLowerCase()}`,
      dbPath,
      FLATSQL_JOURNAL_TRUNCATE,
    );
  db.registerFileId(schema.fileId, schema.tableName);
  const restored = restoreFlatSqlState(db);
  configureEngineDatabaseSession(db);
  return { db, restoredRecords: Math.max(0, restored.restored), rederived: restored.rederived };
}

/**
 * Shared FlatSQL-WASM engine instance for this JS context, disk-capable.
 *
 * The FIRST call decides how the engine is loaded (there is exactly one
 * instance per context, so later callers inherit that decision):
 *   - `engine.wasmPath` set: `initFlatSQL({ io, wasmPath, computeSHA384,
 *     requireIntegrity: true })` — flatsql fetches `${wasmPath}/integrity.json`
 *     and `${wasmPath}/flatsql.wasm`, verifies the SHA-384 and FAILS CLOSED
 *     (the browser lane: the node serves both files same-origin).
 *   - otherwise `initFlatSQL({ skipIntegrityCheck: true, io })` — the
 *     Node/vitest lane, unchanged.
 * A failed initialisation leaves no handles behind, so it is forgotten and the
 * next call may try again.
 */
export async function getSharedFlatSql(engine?: LocalFlatSqlEngineOptions | null): Promise<FlatSQL> {
  if (!sharedFlatSqlPromise) {
    const io = getSharedFlatSqlIoRouter();
    const wasmPath = normalizeEngineWasmPath(engine?.wasmPath);
    const computeSHA384 = engine?.computeSHA384 ?? (wasmPath ? webCryptoSha384Provider() : undefined);
    // flatsql >= 1.4.5 declares `io` on InitOptions, so this is a typed call:
    // the seven imports are bound from the router at instantiation, once.
    const pending = import('flatsql/wasm').then(({ initFlatSQL }) => (wasmPath
      ? initFlatSQL({ io, wasmPath, computeSHA384, requireIntegrity: true })
      : initFlatSQL({ skipIntegrityCheck: true, io })));
    sharedFlatSqlPromise = pending;
    pending.catch(() => {
      if (sharedFlatSqlPromise === pending) sharedFlatSqlPromise = null;
    });
  }
  return sharedFlatSqlPromise;
}

/** Trimmed, trailing-slash-free engine directory URL, or null for the default lane. */
function normalizeEngineWasmPath(value: string | null | undefined): string | null {
  const trimmed = typeof value === 'string' ? value.trim().replace(/\/+$/, '') : '';
  return trimmed || null;
}

/** WebCrypto SHA-384 for flatsql's integrity check; undefined where the context has no `crypto.subtle`. */
function webCryptoSha384Provider(): LocalFlatSqlEngineOptions['computeSHA384'] | undefined {
  const subtle = (globalThis as { crypto?: { subtle?: SubtleCrypto } }).crypto?.subtle;
  if (!subtle || typeof subtle.digest !== 'function') return undefined;
  return async (data: ArrayBuffer) => new Uint8Array(await subtle.digest('SHA-384', data));
}

/**
 * Engine database session setup (loop D.6). The browser/Node wasm build has
 * no filesystem: any query whose sorter/window spills a temp b-tree to
 * "disk" (e.g. the epoch-nearest profile over a catalog-scale partition,
 * ~29K objects) dies with `SQL execution error: disk I/O error` unless
 * SQLite keeps temp storage in memory. The engine is in-memory anyway
 * (OMIT_WAL) — the server hosts run the identical data fine, so this only
 * aligns the JS host with them. MUST run AFTER registerFileId: executing
 * any SQL first finalizes the schema before the record vtab exists.
 */
export function configureEngineDatabaseSession(db: Pick<FlatSQLDatabase, 'query'>): void {
  db.query('PRAGMA temp_store=MEMORY');
}

export async function createLocalFlatSqlStore(options: LocalFlatSqlStoreOptions): Promise<LocalFlatSqlStore> {
  const persistenceStore = createLocalFlatSqlPersistenceStore(options);
  // Mount this store's persistence backend on the shared I/O router BEFORE the
  // engine is initialized: the seven imports are bound once, at instantiation,
  // and a backend registered afterwards would never be reachable by a database
  // opened in this context.
  if (options.persistenceKey && persistenceStore.available) {
    registerFlatSqlIoStore(options.persistenceKey, persistenceStore);
  }
  const flatsql = await getSharedFlatSql(options.engine ?? null);
  const io = getSharedFlatSqlIoRouter();
  const persistenceKey = options.persistenceKey ?? null;
  const states = new Map<string, StandardDatabaseState>();

  for (const schema of options.schemas) {
    const state = await openStandardState(flatsql, io, persistenceStore, persistenceKey, schema);
    states.set(state.schema.standardId, state);
  }

  const pinLedger = options.persistenceKey
    ? await readPersistedPinLedgerEntries(persistenceStore, persistedPinLedgerKey(options.persistenceKey))
    : new Map<string, LocalFlatSqlPinLedgerEntry>();
  return new WasmLocalFlatSqlStore(
    states,
    options.persistenceKey ?? null,
    pinLedger,
    persistenceStore,
    flatsql,
    io,
    options.queryProfiles ?? null,
    options.nowSeconds ?? null,
  );
}

/**
 * THE per-standard open sequence, in one place because the constructor loop
 * and `addStandard` must perform it identically: durable open (or the
 * ephemeral engine), the one-time legacy-snapshot fold, the persisted
 * ingest-key ledger and its repair against the engine's own record count.
 */
async function openStandardState(
  flatsql: FlatSQL,
  io: FlatSqlIoRouter,
  persistenceStore: LocalFlatSqlPersistenceStore,
  persistenceKey: string | null,
  schema: LocalFlatSqlSchema,
): Promise<StandardDatabaseState> {
  const standardId = normalizeStandardId(schema.standardId);
  const durable = Boolean(persistenceKey) && persistenceStore.available;
  const dbPath = durable ? flatSqlDatabasePathForKey(persistenceKey as string, standardId) : null;

  // DURABLE OPEN. The engine restores its own base tables, source partitions
  // and unified views from the index file — nothing is re-registered and
  // nothing is re-ingested. An ephemeral store (no persistence key, or a
  // store that reports itself unavailable) still gets the in-memory engine.
  const opened = dbPath
    ? await openDurableStandardDatabase(flatsql, io, { ...schema, standardId }, dbPath)
    : null;
  const db = opened ? opened.db : createEphemeralStandardDatabase(flatsql, { ...schema, standardId });

  const sources = new Set<string>(opened ? db.listSources() : []);
  let migrated = false;
  if (persistenceKey) {
    // ONE-TIME migration off the retired snapshot-export layout. Read only
    // when the engine's own state restored nothing: once the disk-backed
    // state carries the records, a leftover blob (e.g. a crash before its
    // delete) must never double-ingest.
    const engineRestoredNothing = !opened || opened.restoredRecords === 0;
    migrated = engineRestoredNothing
      ? await migrateLegacySnapshots(db, persistenceStore, persistenceKey, standardId, sources)
      : false;
    if (!engineRestoredNothing) {
      await deleteLegacySnapshotKeys(persistenceStore, persistenceKey, standardId);
    }
    if (migrated && opened && dbPath) {
      // Fold the migrated records into the engine's durable state
      // immediately, so the legacy keys are never needed again.
      opened.db.flushIndex();
      await io.flushFor(dbPath);
    }
  }

  const ingestedKeys = persistenceKey
    ? await readPersistedRecordKeys(persistenceStore, persistedRecordKey(persistenceKey, standardId))
    : new Set<string>();
  const persistedRecordCount = recordCountForDatabase(db, schema.tableName);
  const repairedIngestedKeys = ingestedKeys.size > persistedRecordCount
    ? new Set<string>()
    : ingestedKeys;
  return {
    schema: { ...schema, standardId },
    db,
    dbPath,
    ingestedKeys: repairedIngestedKeys,
    sources,
    cachedBytes: cachedBytesForDatabase(db),
    dirty: repairedIngestedKeys !== ingestedKeys || migrated,
  };
}

/** In-memory engine database — no persistence key, or a store that is not available. */
function createEphemeralStandardDatabase(flatsql: FlatSQL, schema: LocalFlatSqlSchema): EngineDatabase {
  const db = flatsql.createDatabase(
    stripFlatBufferComments(schema.schema),
    `sdn-${normalizeStandardId(schema.standardId).toLowerCase()}`,
  ) as EngineDatabase;
  db.registerFileId(schema.fileId, schema.tableName);
  configureEngineDatabaseSession(db);
  return db;
}

/**
 * Fold the two RETIRED persistence layouts into the live database, once:
 *   1. per-(standard, source) aligned streams (`<key>:<std>:src:<source>`)
 *   2. the pre-D.1 single `exportData` blob (`<key>:<std>`) -> `local`
 * Returns true when anything was ingested. The keys are deleted either way —
 * nothing writes them again.
 */
async function migrateLegacySnapshots(
  db: EngineDatabase,
  persistenceStore: LocalFlatSqlPersistenceStore,
  persistenceKey: string,
  standardId: string,
  sources: Set<string>,
): Promise<boolean> {
  const ensureSourceOn = (source: string) => {
    if (sources.has(source)) return;
    db.registerSource(source);
    sources.add(source);
    db.createUnifiedViews();
  };

  const legacySources = normalizePersistedSourceNames(
    await persistenceStore.readJson(persistedSourcesKey(persistenceKey, standardId)),
  );
  let ingested = false;
  for (const source of legacySources) {
    const stream = await persistenceStore.readBytes(persistedSourceStreamKey(persistenceKey, standardId, source));
    if (stream && stream.byteLength > 0) {
      ensureSourceOn(source);
      db.ingest(stream, source);
      ingested = true;
    }
  }

  if (legacySources.length === 0) {
    const legacy = await readPersistedFlatSqlBytes(persistenceStore, persistedStandardKey(persistenceKey, standardId));
    if (legacy && legacy.byteLength > 0) {
      ensureSourceOn(LOCAL_FLATSQL_DEFAULT_SOURCE);
      db.ingest(legacy, LOCAL_FLATSQL_DEFAULT_SOURCE);
      ingested = true;
    }
  }

  await deleteLegacySnapshotKeys(persistenceStore, persistenceKey, standardId, legacySources);
  return ingested;
}

async function deleteLegacySnapshotKeys(
  persistenceStore: LocalFlatSqlPersistenceStore,
  persistenceKey: string,
  standardId: string,
  knownSources?: string[],
): Promise<void> {
  const sources = knownSources ?? normalizePersistedSourceNames(
    await persistenceStore.readJson(persistedSourcesKey(persistenceKey, standardId)),
  );
  for (const source of sources) {
    await persistenceStore.deleteKey(persistedSourceStreamKey(persistenceKey, standardId, source));
  }
  await persistenceStore.deleteKey(persistedSourcesKey(persistenceKey, standardId));
  await persistenceStore.deleteKey(persistedStandardKey(persistenceKey, standardId));
}

export async function clearLocalFlatSqlStore(options: ClearLocalFlatSqlStoreOptions): Promise<void> {
  const persistenceKey = options.persistenceKey.trim();
  const standardIds = Array.from(new Set(options.standardIds.map(normalizeStandardId)));
  const persistenceStore = createLocalFlatSqlPersistenceStore(options);
  if (!persistenceKey || standardIds.length === 0 || !persistenceStore.available) return;
  // Clearing a disk-backed store means DELETING THE ENGINE'S FILES, not just
  // the side JSON. Mount on the shared router first (idempotent) so the drop
  // goes through the SAME backend instance any live database is using — a
  // second backend would delete the keys and leave a stale chunk cache behind.
  registerFlatSqlIoStore(persistenceKey, persistenceStore);
  const io = getSharedFlatSqlIoRouter();
  for (const standardId of standardIds) {
    for (const path of flatSqlDurablePaths(flatSqlDatabasePathForKey(persistenceKey, standardId))) {
      await io.hydrate(path);
      await io.drop(path);
    }
    // Retired snapshot-export layout: still deleted, never written.
    await deleteLegacySnapshotKeys(persistenceStore, persistenceKey, standardId);
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
    private readonly io: FlatSqlIoRouter,
    private readonly queryProfiles: EngineEpochQueryProfilesConfig | null = null,
    private readonly nowSeconds: (() => number) | null = null,
  ) {}

  /** Standards whose open sequence is in flight, so a concurrent add shares it instead of opening twice. */
  private readonly openingStandards = new Map<string, Promise<void>>();

  async addStandard(schema: LocalFlatSqlSchema): Promise<void> {
    const standardId = normalizeStandardId(schema.standardId);
    if (this.states.has(standardId)) return;
    const inFlight = this.openingStandards.get(standardId);
    if (inFlight) return inFlight;
    const opening = openStandardState(this.flatsql, this.io, this.persistenceStore, this.persistenceKey, schema)
      .then((state) => {
        this.states.set(standardId, state);
      })
      .finally(() => {
        this.openingStandards.delete(standardId);
      });
    this.openingStandards.set(standardId, opening);
    return opening;
  }

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
    const candidates = streamRecords.map((buffer, index) => flatSqlStreamRecordCandidate(standardId, buffer, index + keyOffset, options));
    const nextRecords = trustOrderedPublishedOffsets
      ? candidates
      : filterNewFlatSqlRecordCandidates(candidates, state.ingestedKeys);
    if (nextRecords.length === 0) return 0;

    const source = this.ensureSource(state, options?.source);
    const beforeRecordCount = recordCountForState(state);
    if (
      nextRecords.length === candidates.length &&
      streamInfo.allFramesHaveDirectFileIdentifier &&
      (options?.skipRecords ?? 0) === 0
    ) {
      // No frame was filtered and the stream is already the engine wire
      // format — ingest the ORIGINAL aligned bytes in ONE engine call
      // (loop D.6: avoids re-buffering the whole stream through
      // ingestBuffers' rebuilt copy; byte-identical input to the engine).
      state.db.ingest(streamBytes, source);
    } else {
      state.db.ingestBuffers(nextRecords.map((entry) => stripSdnFlatBufferSizePrefix(entry.buffer)), source);
    }
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
    await this.resetStandardDatabase(state);
    state.ingestedKeys.clear();
    state.cachedBytes = 0;
    state.dirty = false;
    this.deletePinLedgerEntriesForStandard(state.schema.standardId);
    if (options.persist !== false) {
      await this.deletePersistedStandard(state.schema.standardId);
    }
  }

  async createStandardReplacementStore(standardId: string): Promise<LocalFlatSqlStore> {
    const state = this.stateForStandard(standardId);
    // The staging store is deliberately EPHEMERAL: it holds a candidate
    // catalog that has not been accepted yet, so it must not touch the live
    // database's files. replaceStandardFrom moves the accepted partitions into
    // the durable database rather than swapping the handle.
    const replacementState: StandardDatabaseState = {
      schema: { ...state.schema },
      db: createEphemeralStandardDatabase(this.flatsql, state.schema),
      dbPath: null,
      ingestedKeys: new Set<string>(),
      sources: new Set<string>(),
      cachedBytes: 0,
      dirty: false,
    };
    return new WasmLocalFlatSqlStore(
      new Map([[state.schema.standardId, replacementState]]),
      null,
      new Map<string, LocalFlatSqlPinLedgerEntry>(),
      this.persistenceStore,
      this.flatsql,
      this.io,
      this.queryProfiles,
      this.nowSeconds,
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
    const replacementKeys = new Set(replacementState.ingestedKeys);

    // Empty the live database (its FILES too, when durable) and move the
    // staged partitions in as aligned streams — the same wire bytes, ingested
    // with their source. Adopting the staging handle instead would hand the
    // live standard an in-memory engine and silently un-durable the store.
    await this.resetStandardDatabase(activeState);
    for (const source of Array.from(replacementState.sources).sort()) {
      const stream = replacementState.db.queryRawFlatBufferStream(
        `SELECT _data FROM "${replacementState.schema.tableName}@${source}" ORDER BY _rowid ASC`,
      );
      if (stream.byteLength === 0) continue;
      activeState.db.registerSource(source);
      activeState.sources.add(source);
      activeState.db.createUnifiedViews();
      activeState.db.ingest(stream, source);
    }
    activeState.ingestedKeys = replacementKeys;
    activeState.cachedBytes = cachedBytesForDatabase(activeState.db);
    activeState.dirty = true;

    replacementState.db.destroy();
    replacementState.db = createEphemeralStandardDatabase(this.flatsql, replacementState.schema);
    replacementState.ingestedKeys.clear();
    replacementState.sources = new Set<string>();
    replacementState.cachedBytes = 0;
    replacementState.dirty = false;

    this.deletePinLedgerEntriesForStandard(activeState.schema.standardId);
    await this.recordPinLedgerEntries(entries, { persist: false });
    if (options.persist !== false) {
      await this.persistStandard(activeState);
      await this.persistPinLedger();
    } else {
      activeState.dirty = false;
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

  /**
   * PRIMARY query path (loop D.2): engine-native epoch profile over the
   * unified per-standard view, aligned size-prefixed FlatBuffer stream out.
   * Resolution mirrors the retrieval module: request > per-standard config
   * (`queryProfiles`) > compiled fallback (`nearest`, epoch = now, 50000).
   */
  queryEpochRawStream(standardId: string, request: EngineEpochQueryRequest | null = null): Uint8Array {
    const state = this.stateForStandard(standardId);
    const query = resolveEngineEpochQuery({
      spec: engineEpochSpecForSchema(state.schema),
      request,
      defaults: this.queryProfileDefaults(state.schema),
      nowSeconds: this.nowSeconds,
    });
    if (state.sources.size === 0) {
      // Server mirror (engine_records.go QueryEpochRawStream): with no
      // sources registered the unified view (and its _source column) does
      // not exist. Nothing ingested, nothing to return.
      return new Uint8Array(0);
    }
    return state.db.queryRawFlatBufferStream(query.sql, query.params);
  }

  /**
   * Generic aligned-raw-stream query (server mirror of
   * FlatSQLStore.QueryRawStream). The SQL must be read-only; it runs
   * VERBATIM (no limit rewriting — raw streams are the wire format and
   * limits arrive as positional params).
   */
  queryRawFlatBufferStream(standardId: string, sql: string, params: QueryParam[] = []): Uint8Array {
    const validation = validateReadOnlySql(sql);
    if (!validation.ok) {
      throw new Error(`FlatSQL raw stream queries must be read-only SELECT or WITH SELECT statements: ${validation.diagnostics.join(' ')}`);
    }
    return this.stateForStandard(standardId).db.queryRawFlatBufferStream(sql, params);
  }

  private queryProfileDefaults(schema: LocalFlatSqlSchema): EngineEpochQueryRequest | null {
    if (!this.queryProfiles) return null;
    return this.queryProfiles[`${schema.standardId}.fbs`]
      ?? this.queryProfiles[schema.standardId]
      ?? null;
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

  /**
   * Empty one standard's database. On a durable standard this DELETES THE
   * FILES and reopens at the same path, so a clear survives a reload; on an
   * ephemeral one it is a fresh in-memory database, exactly as before.
   */
  private async resetStandardDatabase(state: StandardDatabaseState): Promise<void> {
    state.db.destroy();
    state.sources = new Set<string>();
    if (!state.dbPath) {
      state.db = createEphemeralStandardDatabase(this.flatsql, state.schema);
      return;
    }
    for (const path of flatSqlDurablePaths(state.dbPath)) {
      await this.io.hydrate(path);
      await this.io.drop(path);
    }
    const opened = await openDurableStandardDatabase(this.flatsql, this.io, state.schema, state.dbPath);
    state.db = opened.db;
    state.sources = new Set(opened.db.listSources());
  }

  /**
   * Make the engine's own state durable: append the index delta inside the
   * mark's transaction, then let the async store land the dirty page groups.
   * This IS the persistence path — there is no snapshot to export, because
   * `openState` restores base tables, source partitions and unified views
   * from the file the engine just wrote.
   */
  private async persistStandard(state: StandardDatabaseState): Promise<void> {
    state.dirty = false;
    if (!this.persistenceKey) {
      state.cachedBytes = cachedBytesForDatabase(state.db);
      return;
    }
    if (state.dbPath) {
      state.db.flushIndex();
      await this.io.flushFor(state.dbPath);
      // Post-flush the mark IS the arena length, without copying it out.
      state.cachedBytes = Math.max(0, state.db.flushedOffset());
    } else {
      state.cachedBytes = cachedBytesForDatabase(state.db);
    }
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

  private async deletePersistedStandard(standardId: string): Promise<void> {
    if (!this.persistenceKey) return;
    await deleteLegacySnapshotKeys(this.persistenceStore, this.persistenceKey, standardId);
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
    state.cachedBytes = cachedBytesForDatabase(state.db);
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
 * Bytes this standard occupies = the arena, which on a durable database is
 * literally the `.fsdata` file. MEASURED: `exportData().byteLength` and
 * `flushedOffset()` agree exactly once the index is flushed (18360 == 18360 on
 * the 85-record parity corpus), and `flushedOffset()` lags by the unflushed
 * tail before it — so the mark is used only where a flush has just happened.
 */
function cachedBytesForDatabase(db: FlatSQLDatabase & DurableFlatSqlDatabase): number {
  return db.exportData().byteLength;
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

/**
 * Engine epoch profile spec for a configured standard (loop D.2): built-in
 * registry defaults (OMM) overlaid with the schema's `epochSpec` config.
 * Throws for standards without configured epoch columns.
 */
export function engineEpochSpecForSchema(schema: LocalFlatSqlSchema): EngineEpochProfileSpec {
  const defaults = DEFAULT_ENGINE_EPOCH_SPECS[normalizeStandardId(schema.standardId)];
  const partitionColumn = schema.epochSpec?.partitionColumn ?? defaults?.partitionColumn;
  const epochColumn = schema.epochSpec?.epochColumn ?? defaults?.epochColumn;
  if (!partitionColumn || !epochColumn) {
    throw new Error(
      `No engine epoch profile columns configured for ${schema.standardId} — set epochSpec {partitionColumn, epochColumn} on the schema`,
    );
  }
  return { tableName: schema.tableName, partitionColumn, epochColumn };
}

/**
 * Zero-copy frame iterator over an aligned size-prefixed FlatBuffer stream:
 * every yielded frame is a subarray VIEW into the stream bytes (no copies) —
 * the decoded-record convenience over `queryEpochRawStream` output.
 */
export function* iterateFlatSqlSizePrefixedStream(streamBytes: Uint8Array): Generator<Uint8Array, void, undefined> {
  const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
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
    yield streamBytes.subarray(offset, nextOffset);
    offset = nextOffset;
    index += 1;
  }
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

interface FlatSqlStreamRecordCandidate {
  buffer: Uint8Array;
  key: string;
  /**
   * Pre-D.6 32-bit content-key format, still honored for MEMBERSHIP so
   * ledgers persisted by older builds keep suppressing replays. Never
   * written anymore.
   */
  legacyKey?: string;
}

function flatSqlStreamRecordCandidate(
  standardId: string,
  buffer: Uint8Array,
  index: number,
  options: LocalFlatSqlStreamIngestOptions | null,
): FlatSqlStreamRecordCandidate {
  const explicitKey = options?.recordKeys?.[index - Math.max(0, options.recordKeyOffset ?? options.skipRecords ?? 0)]?.trim();
  if (explicitKey) return { buffer, key: explicitKey };
  const prefix = options?.recordKeyPrefix?.trim();
  if (prefix) return { buffer, key: `${normalizeStandardId(standardId)}|${prefix}|${index}` };
  // Content-addressed dedupe key. REAL BUG caught at catalog scale (loop
  // D.6): the old key was (byteLength, fnv1a32) — 32 bits of content hash —
  // and a 250K-record bulk stream silently dropped ~13 DISTINCT records as
  // birthday collisions. The key now carries two independent 32-bit hashes
  // (64 collision bits, one pass); the old format stays recognized for
  // membership so persisted ledgers keep deduping their replays.
  const normalizedStandardId = normalizeStandardId(standardId);
  const [h1, h2] = contentHash64(buffer);
  const h1Hex = h1.toString(16).padStart(8, '0');
  return {
    buffer,
    key: `${normalizedStandardId}|stream|${buffer.byteLength}|${h1Hex}${h2.toString(16).padStart(8, '0')}`,
    legacyKey: `${normalizedStandardId}|stream|${buffer.byteLength}|${h1Hex}`,
  };
}

function filterNewFlatSqlRecordCandidates<T extends { key: string; legacyKey?: string }>(
  candidates: T[],
  ingestedKeys: ReadonlySet<string>,
): T[] {
  // Do NOT clone ingestedKeys (it grows with the store — cloning made every
  // ingest O(total records), loop D.6); batch-local dupes get their own set.
  const batchSeen = new Set<string>();
  const next: T[] = [];
  for (const candidate of candidates) {
    if (
      ingestedKeys.has(candidate.key) ||
      batchSeen.has(candidate.key) ||
      (candidate.legacyKey !== undefined && ingestedKeys.has(candidate.legacyKey))
    ) continue;
    batchSeen.add(candidate.key);
    next.push(candidate);
  }
  return next;
}

/**
 * One-pass 64-bit content hash for stream dedupe keys: word 0 is canonical
 * FNV-1a 32 (BYTE-STABLE — it doubles as the persisted legacy key, never
 * change its math), word 1 is an independent multiply-rotate mixer.
 * Indexed loop, not for-of (~2x faster on the ingest hot path, loop D.6).
 */
function contentHash64(bytes: Uint8Array): [number, number] {
  let h1 = 0x811c9dc5;
  let h2 = 0x9e3779b9;
  for (let i = 0; i < bytes.length; i += 1) {
    const byte = bytes[i];
    h1 ^= byte;
    h1 = Math.imul(h1, 0x01000193);
    h2 = Math.imul(h2 ^ byte, 0x85ebca6b);
    h2 = (h2 << 13) | (h2 >>> 19);
  }
  return [h1 >>> 0, h2 >>> 0];
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

/**
 * READ-ONLY by design. The snapshot-export writer that used to sit beside this
 * is gone: the engine's own disk state is the durable representation, and these
 * keys are only ever read once during migration and then deleted.
 */
async function readPersistedFlatSqlBytes(store: LocalFlatSqlPersistenceStore, key: string): Promise<Uint8Array | null> {
  return store.readBytes(key);
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
