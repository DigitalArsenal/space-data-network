/**
 * Bridge the FlatSQL seven-import host I/O contract onto sdn-js's EXISTING
 * persistence stores.
 *
 * FlatSQL 1.4.0 registers its own sqlite3_vfs over seven offset-addressed
 * imports (`flatsql_io_open/read/write/truncate/sync/size/close`). The engine
 * is one binary with no runtime detection in it; the difference between a
 * WasmEdge preopen and a browser IndexedDB store lives entirely here, in the
 * host shim. See flatsql docs/STORAGE-DURABILITY.md §6.
 *
 * `LocalFlatSqlPersistenceStore` is a FLAT key -> bytes store: no path_open,
 * no seek, no partial write. flatsql's `createChunkedStoreBackend` gives it
 * pread/pwrite semantics by addressing fixed-size page groups
 * (`<prefix><path>#<group>`, plus a `#meta` length record), so a flush costs
 * O(dirty chunks) rather than O(file) and the browser reads and writes the same
 * byte ranges the POSIX lane does. Not "the browser rewrites everything" — the
 * same algorithm over a different transport.
 *
 * THE ONE ASYMMETRY, stated plainly. IndexedDB and the desktop HTTP store are
 * asynchronous; a wasm import cannot await. So durability is an explicit
 * awaited step at the JS boundary:
 *
 *     await io.hydrate(path);      // BEFORE opening the database
 *     ...engine runs fully synchronously...
 *     db.flushIndex();             // engine appends + records the mark
 *     await io.flush();            // bytes become durable HERE
 *
 * Query results are identical to the native lane in every case; only the moment
 * bytes land differs, and it is awaited rather than assumed.
 *
 * IMPORTANT — one engine per JS context. flatsql's initFlatSQL() rebinds its
 * module-level wasm instance on every call, so the I/O backend must be
 * installed ONCE at shared-init time. That is why this module exports a ROUTER:
 * several stores can coexist behind one wasm instance by owning disjoint path
 * prefixes.
 */

import {
  createChunkedStoreBackend,
  createMemoryBackend,
} from 'flatsql/io';

import type { LocalFlatSqlPersistenceStore } from './local-flatsql.js';

/** Status codes the seven imports may return. Values, never throws. */
export const FLATSQL_IO_ERR_NOENT = -2;
export const FLATSQL_IO_ERR_ACCESS = -3;
export const FLATSQL_IO_ERR_BADHANDLE = -6;

/** Engine state codes (flatsql_open_state and friends). */
export const FLATSQL_STATE_OK = 0;
export const FLATSQL_STATE_ABSENT = -1;
export const FLATSQL_STATE_VERSION_MISMATCH = -2;
export const FLATSQL_STATE_CORRUPT = -3;
export const FLATSQL_STATE_TORN = -4;
export const FLATSQL_STATE_NO_FILESYSTEM = -5;

export function describeFlatSqlStateCode(code: number): string {
  switch (code) {
    case FLATSQL_STATE_ABSENT: return 'no persisted state';
    case FLATSQL_STATE_VERSION_MISMATCH: return 'format or schema changed';
    case FLATSQL_STATE_CORRUPT: return 'verification failed';
    case FLATSQL_STATE_TORN: return 'torn pair (stream shorter than the recorded mark)';
    case FLATSQL_STATE_NO_FILESYSTEM: return 'no filesystem (ephemeral engine)';
    default: return code >= 0 ? `${code} records` : `unknown code ${code}`;
  }
}

/** The synchronous backend shape flatsql's loaders expect. */
export interface FlatSqlIoBackend {
  open(path: string, flags: number): number;
  read(handle: number, dst: Uint8Array, offset: number): number;
  write(handle: number, src: Uint8Array, offset: number): number;
  truncate(handle: number, size: number): number;
  sync(handle: number): number;
  size(handle: number): number;
  close(handle: number): number;
  hydrate(path: string): Promise<unknown>;
  flush(): Promise<void>;
  drop(path: string): Promise<void>;
}

export interface FlatSqlIoStoreOptions {
  /** Page-group size. 64 KiB by default: 16 SQLite pages per store value. */
  chunkBytes?: number;
  /** Key namespace inside the store. */
  prefix?: string;
  /** Paths already known to exist (skips a probe round trip on boot). */
  knownPaths?: string[];
}

/**
 * Back the seven imports with one sdn-js persistence store.
 *
 * `MemoryFlatSqlPersistenceStore` is a legitimate backend here, not a stub: it
 * survives engine teardown within a process and is exactly what the ephemeral
 * lane should do. What it cannot do is survive the process, and the test matrix
 * asserts that documented behaviour rather than skipping it.
 */
export function createFlatSqlIoStoreBackend(
  store: LocalFlatSqlPersistenceStore,
  options: FlatSqlIoStoreOptions = {},
): FlatSqlIoBackend {
  if (!store.available) {
    // Fail closed and loudly. A backend that silently answers from RAM is the
    // exact defect this layer exists to close.
    return createMemoryBackend({ chunkBytes: options.chunkBytes }) as FlatSqlIoBackend;
  }
  return createChunkedStoreBackend(store, {
    chunkBytes: options.chunkBytes ?? 64 * 1024,
    prefix: options.prefix ?? 'flatsql-io/',
    knownPaths: options.knownPaths ?? [],
  }) as FlatSqlIoBackend;
}

interface RouterEntry {
  prefix: string;
  backend: FlatSqlIoBackend;
}

/**
 * Dispatch the seven imports to several backends by path prefix.
 *
 * There is exactly one wasm instance per JS context, so there is exactly one
 * import object. A router is how more than one store gets to be durable at the
 * same time without a second engine — which "use FlatSQL only" forbids anyway.
 */
export class FlatSqlIoRouter implements FlatSqlIoBackend {
  private readonly entries: RouterEntry[] = [];
  private readonly fallback: FlatSqlIoBackend;
  private readonly handles = new Map<number, { backend: FlatSqlIoBackend; inner: number }>();
  private nextHandle = 1;

  constructor(fallback?: FlatSqlIoBackend) {
    this.fallback = fallback ?? (createMemoryBackend() as FlatSqlIoBackend);
  }

  /** Longest-prefix wins, so a specific mount beats a general one. */
  register(prefix: string, backend: FlatSqlIoBackend): this {
    this.entries.push({ prefix, backend });
    this.entries.sort((a, b) => b.prefix.length - a.prefix.length);
    return this;
  }

  backendFor(path: string): FlatSqlIoBackend {
    for (const entry of this.entries) {
      if (path.startsWith(entry.prefix)) return entry.backend;
    }
    return this.fallback;
  }

  open(path: string, flags: number): number {
    const backend = this.backendFor(path);
    const inner = backend.open(path, flags);
    // PROBE/UNLINK return a status, not a handle: pass it straight through.
    if (inner < 0 || (flags & 0x00c0) !== 0) return inner;
    const handle = this.nextHandle++;
    this.handles.set(handle, { backend, inner });
    return handle;
  }

  private entry(handle: number) {
    return this.handles.get(handle) ?? null;
  }

  read(handle: number, dst: Uint8Array, offset: number): number {
    const entry = this.entry(handle);
    return entry ? entry.backend.read(entry.inner, dst, offset) : FLATSQL_IO_ERR_BADHANDLE;
  }

  write(handle: number, src: Uint8Array, offset: number): number {
    const entry = this.entry(handle);
    return entry ? entry.backend.write(entry.inner, src, offset) : FLATSQL_IO_ERR_BADHANDLE;
  }

  truncate(handle: number, size: number): number {
    const entry = this.entry(handle);
    return entry ? entry.backend.truncate(entry.inner, size) : FLATSQL_IO_ERR_BADHANDLE;
  }

  sync(handle: number): number {
    const entry = this.entry(handle);
    return entry ? entry.backend.sync(entry.inner) : FLATSQL_IO_ERR_BADHANDLE;
  }

  size(handle: number): number {
    const entry = this.entry(handle);
    return entry ? entry.backend.size(entry.inner) : FLATSQL_IO_ERR_BADHANDLE;
  }

  close(handle: number): number {
    const entry = this.entry(handle);
    if (!entry) return FLATSQL_IO_ERR_BADHANDLE;
    this.handles.delete(handle);
    return entry.backend.close(entry.inner);
  }

  async hydrate(path: string): Promise<unknown> {
    return this.backendFor(path).hydrate(path);
  }

  /**
   * Flush EVERY mounted backend. Correct for a whole-context shutdown and
   * wrong for a single store's checkpoint: one node's ingest must not force
   * another mount's transport to run (and fail — an unavailable IndexedDB
   * mount registered by a different store would throw here). Per-store
   * checkpoints use `flushFor(path)`.
   */
  async flush(): Promise<void> {
    for (const entry of this.entries) await entry.backend.flush();
    await this.fallback.flush();
  }

  /** Flush only the backend that owns `path` — the per-store checkpoint. */
  async flushFor(path: string): Promise<void> {
    await this.backendFor(path).flush();
  }

  async drop(path: string): Promise<void> {
    await this.backendFor(path).drop(path);
  }
}

/**
 * Every file a durable FlatSQL database occupies in the backend namespace:
 * the index, the arena, and SQLite's rollback journal (journalMode TRUNCATE —
 * WAL needs xShmMap, which no wasm lane provides). The journal is listed
 * because a crash between `write` and `sync` leaves a HOT journal, and an open
 * that cannot see it silently skips the rollback it exists for.
 */
export function flatSqlDurablePaths(dbPath: string): string[] {
  return [dbPath, `${dbPath}.fsdata`, `${dbPath}-journal`];
}

/** Hydrate every file a database needs, before any synchronous engine call. */
export async function hydrateFlatSqlDatabase(
  io: Pick<FlatSqlIoBackend, 'hydrate'>,
  dbPath: string,
): Promise<void> {
  for (const path of flatSqlDurablePaths(dbPath)) {
    await io.hydrate(path);
  }
}

export interface DurableOpenResult<TDatabase> {
  db: TDatabase;
  /** Raw code from flatsql_open_state. */
  stateCode: number;
  /** Records restored (0 when a re-derivation was needed). */
  restored: number;
  /** True when the persisted index could not be trusted and was rebuilt. */
  rederived: boolean;
}

/**
 * The durable-state surface the flatsql wasm artifact exports and its published
 * `wasm/index.d.ts` still does not declare (`openDatabase`, `openState`,
 * `reindexAll`, `flushIndex`, `flushedOffset`, `isDiskBacked`, `streamPath` —
 * present on both `wasm/index.js` and `wasm/standalone.js` since 1.4.0).
 * Declared structurally here so callers type the calls they make instead of
 * casting the engine away; drop it when upstream types them.
 */
export interface DurableFlatSqlDatabase {
  openState(): number;
  reindexAll(): number;
  flushIndex(): number;
  flushedOffset(): number;
  isDiskBacked(): boolean;
}

/** `FlatSQL.openDatabase` — same gap, same reason. */
export interface DurableFlatSqlOpener<TDatabase> {
  openDatabase(schema: string, dbName: string, path: string, journalMode?: number): TDatabase;
}

type DurableDatabase = Pick<DurableFlatSqlDatabase, 'openState' | 'reindexAll' | 'isDiskBacked'>;

/**
 * The boot sequence, in one place so every caller performs it identically:
 * open state, and on ANY negative code fall back to a full re-derivation from
 * the stream. Every negative code is recoverable and the worst case is exactly
 * the behaviour hosts have today — so a failure here is a cost, never a loss.
 */
export function restoreFlatSqlState<TDatabase extends DurableDatabase>(
  db: TDatabase,
): DurableOpenResult<TDatabase> {
  const stateCode = db.openState();
  if (stateCode >= 0) {
    return { db, stateCode, restored: stateCode, rederived: false };
  }
  const rebuilt = db.reindexAll();
  return {
    db,
    stateCode,
    restored: rebuilt >= 0 ? rebuilt : 0,
    rederived: true,
  };
}
