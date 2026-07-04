/**
 * FlatSQL-WASM engine record store (loop D.1) — THE SDNNode store.
 *
 * ONE store: the same FlatSQL-WASM engine that runs inside sdn-server hosts
 * the browser node's records. Layout mirrors the server
 * (sdn-server/internal/storage):
 *
 *  - Record envelopes are indexed in a plain SQL control table named
 *    `sdn_record_index` created through engine DDL (server: flatsql.go) —
 *    schema_name / cid / source_timestamp columns keep the server's naming.
 *    Payload bytes are NOT stored in SQLite rows (server keeps them in
 *    append-only stream files); here they live in an in-memory envelope map
 *    whose durable substrate is the snapshot journal below.
 *  - SDS FlatBuffer records are mirrored into per-standard engine databases
 *    (src/local-flatsql.ts): per-provider `registerSource` shadow tables
 *    (`OMM@<source>`) + unified views whose `_source` column carries the
 *    full shadow-table name — identical to engine_records.go. Records
 *    stored through the node surface default to the server's `local`
 *    source partition.
 *  - Durability: the engine is in-memory; boot = replay. The envelope
 *    journal (same size-prefixed codec the pre-D.1 FlatSQLStorage snapshots
 *    used, so existing snapshots keep loading) persists through a pluggable
 *    SnapshotPersistence — IndexedDB by default when a persistenceKey is
 *    given, or Helia/memory when injected. Per-standard record streams
 *    persist per (standard, source) via the engine store.
 *
 * The public surface satisfies both `NodeRecordStorage` (SDNNode) and the
 * `ModuleHostRecordStore` contract (module-host-adapters.ts), so module
 * hosting drops in unchanged.
 */

import { sha256 } from './crypto/hd-wallet';
import type { EngineEpochQueryProfilesConfig, EngineEpochQueryRequest } from './epoch-query-sql';
import type { SchemaName } from './schemas';
import type { StoredRecord, QueryFilter } from './storage';
import {
  createDefaultLocalFlatSqlPersistenceStore,
  createLocalFlatSqlStore,
  getSharedFlatSql,
  iterateFlatSqlSizePrefixedStream,
  LOCAL_FLATSQL_DEFAULT_SOURCE,
  type LocalFlatSqlPersistenceStore,
  type LocalFlatSqlSchema,
  type LocalFlatSqlStore,
} from './local-flatsql';

/** Positional query parameter accepted by the engine (flatsql/wasm QueryParam). */
export type EngineQueryParam = null | boolean | number | string | Uint8Array;

// ---------------------------------------------------------------------------
// Snapshot persistence (journal) — codec + backends preserved from the
// pre-D.1 FlatSQLStorage so existing persisted snapshots load unchanged.
// ---------------------------------------------------------------------------

export interface SnapshotPersistence {
  load(): Promise<Uint8Array | null>;
  save(bytes: Uint8Array): Promise<void>;
}

/** In-memory persistence (tests / ephemeral nodes). */
export class MemorySnapshotPersistence implements SnapshotPersistence {
  private snapshot: Uint8Array | null = null;
  async load(): Promise<Uint8Array | null> {
    return this.snapshot ? new Uint8Array(this.snapshot) : null;
  }
  async save(bytes: Uint8Array): Promise<void> {
    this.snapshot = new Uint8Array(bytes);
  }
}

/** Minimal Helia surface the persistence needs (helia + @helia/unixfs). */
export interface HeliaLike {
  addBytes(bytes: Uint8Array): Promise<{ toString(): string }>;
  catBytes(cid: string): Promise<Uint8Array>;
}

/**
 * Helia-backed persistence: snapshot bytes become a unixfs block; the root
 * CID is remembered in a Storage-like ref store (localStorage in browsers).
 */
export class HeliaSnapshotPersistence implements SnapshotPersistence {
  constructor(
    private readonly helia: HeliaLike,
    private readonly refStore: Pick<Storage, 'getItem' | 'setItem'>,
    private readonly refKey = 'sdn.flatsql-store.root-cid',
  ) {}

  rootCid(): string | null {
    return this.refStore.getItem(this.refKey);
  }

  async load(): Promise<Uint8Array | null> {
    const cid = this.refStore.getItem(this.refKey);
    if (!cid) return null;
    return this.helia.catBytes(cid);
  }

  async save(bytes: Uint8Array): Promise<void> {
    const cid = await this.helia.addBytes(bytes);
    this.refStore.setItem(this.refKey, cid.toString());
  }
}

/**
 * SnapshotPersistence over a LocalFlatSqlPersistenceStore key — the default
 * journal backend (IndexedDB `sdn-local-flatsql`/`datastores`) when the
 * store opens with a persistenceKey and no explicit persistence.
 */
export class PersistenceStoreSnapshotPersistence implements SnapshotPersistence {
  constructor(
    private readonly store: LocalFlatSqlPersistenceStore,
    private readonly key: string,
  ) {}

  async load(): Promise<Uint8Array | null> {
    return this.store.readBytes(this.key);
  }

  async save(bytes: Uint8Array): Promise<void> {
    await this.store.writeBytes(this.key, bytes);
  }
}

// --- snapshot codec: [u32 count] then per record
//     [u32 metaLen][meta JSON][u32 dataLen][data][u32 sigLen][signature] ---

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

function encodeSnapshot(records: StoredRecord[]): Uint8Array {
  const parts: Uint8Array[] = [];
  let total = 4;
  for (const record of records) {
    const meta = textEncoder.encode(
      JSON.stringify({
        cid: record.cid,
        schema: record.schema,
        peerId: record.peerId,
        timestamp: record.timestamp,
      }),
    );
    parts.push(meta, record.data, record.signature);
    total += 12 + meta.length + record.data.length + record.signature.length;
  }
  const bytes = new Uint8Array(total);
  const view = new DataView(bytes.buffer);
  view.setUint32(0, records.length, true);
  let offset = 4;
  let part = 0;
  for (let i = 0; i < records.length; i++) {
    for (let j = 0; j < 3; j++) {
      const chunk = parts[part++];
      view.setUint32(offset, chunk.length, true);
      offset += 4;
      bytes.set(chunk, offset);
      offset += chunk.length;
    }
  }
  return bytes;
}

function decodeSnapshot(bytes: Uint8Array): StoredRecord[] {
  if (bytes.length < 4) return [];
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const count = view.getUint32(0, true);
  const records: StoredRecord[] = [];
  let offset = 4;
  const readChunk = (): Uint8Array => {
    const length = view.getUint32(offset, true);
    offset += 4;
    const chunk = bytes.subarray(offset, offset + length);
    offset += length;
    return chunk;
  };
  for (let i = 0; i < count && offset < bytes.length; i++) {
    const meta = JSON.parse(textDecoder.decode(readChunk())) as Omit<
      StoredRecord,
      'data' | 'signature'
    >;
    const data = new Uint8Array(readChunk());
    const signature = new Uint8Array(readChunk());
    records.push({ ...meta, data, signature });
  }
  return records;
}

// Hashing goes through the wasm wallet (native/WASM crypto runtime
// boundary — browser WebCrypto is banned in production source).
async function sha256Hex(data: Uint8Array): Promise<string> {
  const digest = await sha256(new Uint8Array(data));
  return Array.from(digest)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

// ---------------------------------------------------------------------------
// Control-table layout (server mirror: sdn-server/internal/storage/flatsql.go)
// ---------------------------------------------------------------------------

/**
 * Minimal FlatBuffer schema for the control database: the engine requires a
 * schema to create a database, but the control DB only hosts plain SQL
 * tables created through DDL (exactly like the server's control tables live
 * beside the record vtabs in one engine).
 */
const CONTROL_DB_SCHEMA = `
  table SDNControl { ID:uint; }
  root_type SDNControl;
  file_identifier "$SDN";
`;

const CONTROL_TABLE_DDL = `CREATE TABLE IF NOT EXISTS sdn_record_index (
  schema_name TEXT NOT NULL,
  cid TEXT NOT NULL,
  peer_id TEXT,
  source_timestamp INTEGER NOT NULL,
  created_at INTEGER,
  PRIMARY KEY (schema_name, cid)
)`;

const CONTROL_INDEX_DDL = `CREATE INDEX IF NOT EXISTS idx_sdn_record_index_lookup
ON sdn_record_index (schema_name, source_timestamp DESC)`;

/** Engine database surface the record store drives (subset of flatsql/wasm). */
interface ControlDatabase {
  query(sql: string, params?: Array<null | boolean | number | string | Uint8Array>): {
    columns: string[];
    rows: unknown[][];
  };
  destroy(): void;
}

export interface FlatSQLEngineRecordStoreOptions {
  /** Envelope journal backend. Defaults to IndexedDB when persistenceKey is set. */
  persistence?: SnapshotPersistence;
  /** Persist the journal after every mutation (default true when a journal exists). */
  flushOnWrite?: boolean;
  hashHex?: (data: Uint8Array) => Promise<string>;
  nowMs?: () => number;
  /** SDS standards mirrored into per-standard engine databases (server layout). */
  schemas?: LocalFlatSqlSchema[];
  /** Key namespacing persisted engine state (streams, journal) in IndexedDB. */
  persistenceKey?: string | null;
  desktopPersistenceBaseUrl?: string | null;
  fetch?: ((input: string | URL | Request, init?: RequestInit) => Promise<Response>) | null;
  /** Injectable key/value persistence backend (tests / embedders). */
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
  /** Source partition for records stored through the node surface (default `local`). */
  defaultSource?: string;
  /**
   * Per-standard default query profiles (loop D.2) — retrieval-module
   * config shape, keyed by schema name (`OMM.fbs`) or standard id (`OMM`).
   */
  queryProfiles?: EngineEpochQueryProfilesConfig | null;
  /** Clock for defaulted query epochs (tests). */
  nowSeconds?: (() => number) | null;
}

interface EngineRecordStoreContext {
  controlDb: ControlDatabase;
  standards: LocalFlatSqlStore | null;
  standardIds: Map<string, { standardId: string; fileId: string }>;
  options: FlatSQLEngineRecordStoreOptions;
}

export class FlatSQLEngineRecordStore {
  private readonly controlDb: ControlDatabase;
  private readonly standards: LocalFlatSqlStore | null;
  private readonly standardIds: Map<string, { standardId: string; fileId: string }>;
  private readonly byCid = new Map<string, StoredRecord>();
  private readonly persistence?: SnapshotPersistence;
  private readonly flushOnWrite: boolean;
  private readonly hashHex: (data: Uint8Array) => Promise<string>;
  private readonly nowMs: () => number;
  private readonly defaultSource: string;
  private closed = false;

  protected constructor(context: EngineRecordStoreContext) {
    this.controlDb = context.controlDb;
    this.standards = context.standards;
    this.standardIds = context.standardIds;
    this.persistence = context.options.persistence;
    this.flushOnWrite = context.options.flushOnWrite ?? Boolean(context.options.persistence);
    this.hashHex = context.options.hashHex ?? sha256Hex;
    this.nowMs = context.options.nowMs ?? (() => Date.now());
    this.defaultSource = context.options.defaultSource?.trim() || LOCAL_FLATSQL_DEFAULT_SOURCE;
  }

  static async open(
    this: typeof FlatSQLEngineRecordStore,
    options: FlatSQLEngineRecordStoreOptions = {},
  ): Promise<FlatSQLEngineRecordStore> {
    const flatsql = await getSharedFlatSql();
    const controlDb = flatsql.createDatabase(CONTROL_DB_SCHEMA, 'sdn-control') as unknown as ControlDatabase;
    controlDb.query(CONTROL_TABLE_DDL);
    controlDb.query(CONTROL_INDEX_DDL);

    const schemas = options.schemas ?? [];
    const standards = schemas.length > 0
      ? await createLocalFlatSqlStore({
        schemas,
        persistenceKey: options.persistenceKey,
        desktopPersistenceBaseUrl: options.desktopPersistenceBaseUrl,
        fetch: options.fetch,
        persistenceStore: options.persistenceStore,
        queryProfiles: options.queryProfiles,
        nowSeconds: options.nowSeconds,
      })
      : null;
    const standardIds = new Map<string, { standardId: string; fileId: string }>();
    for (const schema of schemas) {
      const standardId = schema.standardId.trim().split('.')[0]?.toUpperCase() ?? '';
      if (standardId) standardIds.set(standardId, { standardId, fileId: schema.fileId });
    }

    const resolvedOptions: FlatSQLEngineRecordStoreOptions = { ...options };
    if (!resolvedOptions.persistence && options.persistenceKey) {
      const backing = options.persistenceStore
        ?? createDefaultLocalFlatSqlPersistenceStore({
          desktopPersistenceBaseUrl: options.desktopPersistenceBaseUrl,
          fetch: options.fetch,
        });
      if (backing.available) {
        resolvedOptions.persistence = new PersistenceStoreSnapshotPersistence(
          backing,
          `${options.persistenceKey}:record-envelopes`,
        );
      }
    }

    const store = new this({
      controlDb,
      standards,
      standardIds,
      options: resolvedOptions,
    });
    await store.replayJournal();
    return store;
  }

  /** Per-standard engine store (per-source shadow tables + unified views). */
  get standardsStore(): LocalFlatSqlStore | null {
    return this.standards;
  }

  /**
   * PRIMARY public query API (loop D.2): run an engine-native epoch profile
   * (`nearest` — the default — / `as_of` / `forward`; the server's retrieval
   * profiles, SQL byte-identical to sdn-server engine_records.go, params
   * bound in the same positional order) over the unified per-standard view
   * and return the ALIGNED size-prefixed FlatBuffer frame stream — the wire
   * format, byte-identical to the Go host for the same store contents.
   *
   * Defaults resolve request > per-standard `queryProfiles` config >
   * compiled fallback (`nearest`, epoch = now, limit 50000) — the same
   * precedence as the retrieval module.
   */
  queryEpochRawStream(standardId: string, request?: EngineEpochQueryRequest | null): Uint8Array {
    const standards = this.requireStandards();
    if (!standards.queryEpochRawStream) {
      throw new Error('The configured standards store does not expose engine epoch raw-stream queries');
    }
    return standards.queryEpochRawStream(standardId, request ?? null);
  }

  /**
   * Decoded-record convenience over `queryEpochRawStream`: an iterator of
   * per-record FlatBuffer frames that are zero-copy subarray VIEWS into the
   * aligned stream.
   */
  queryEpochFrames(
    standardId: string,
    request?: EngineEpochQueryRequest | null,
  ): Generator<Uint8Array, void, undefined> {
    return iterateFlatSqlSizePrefixedStream(this.queryEpochRawStream(standardId, request));
  }

  /**
   * Generic aligned-raw-stream query (server mirror of
   * FlatSQLStore.QueryRawStream): read-only SQL whose result cells are all
   * BLOBs, run verbatim with positional params against the standard's
   * engine database.
   */
  queryRawFlatBufferStream(standardId: string, sql: string, params?: EngineQueryParam[]): Uint8Array {
    const standards = this.requireStandards();
    if (!standards.queryRawFlatBufferStream) {
      throw new Error('The configured standards store does not expose raw FlatBuffer stream queries');
    }
    return standards.queryRawFlatBufferStream(standardId, sql, params);
  }

  private requireStandards(): LocalFlatSqlStore {
    if (!this.standards) {
      throw new Error('No per-standard engine databases configured — open the store with `schemas`');
    }
    return this.standards;
  }

  private async replayJournal(): Promise<void> {
    if (!this.persistence) return;
    const snapshot = await this.persistence.load();
    if (!snapshot || snapshot.length === 0) return;
    for (const record of decodeSnapshot(snapshot)) {
      this.insertEnvelope(record);
      await this.mirrorIntoStandard(record);
    }
  }

  private insertEnvelope(record: StoredRecord): void {
    this.byCid.set(record.cid, record);
    this.controlDb.query(
      'INSERT OR REPLACE INTO sdn_record_index (schema_name, cid, peer_id, source_timestamp, created_at) VALUES (?1, ?2, ?3, ?4, ?5)',
      [record.schema, record.cid, record.peerId, record.timestamp, Math.floor(this.nowMs() / 1000)],
    );
  }

  /**
   * Mirror SDS FlatBuffer payloads into the per-standard engine database
   * (server behavior: engine vtab is a query cache over the durable
   * envelope; per-record failures are logged and skipped). Idempotent across
   * journal replays via the engine store's ingested-keys ledger.
   */
  private async mirrorIntoStandard(record: StoredRecord): Promise<void> {
    if (!this.standards) return;
    const standardId = record.schema.trim().split('.')[0]?.toUpperCase() ?? '';
    const standard = this.standardIds.get(standardId);
    if (!standard || !flatBufferMatchesFileId(record.data, standard.fileId)) return;
    try {
      await this.standards.ingestRecords(standardId, [{
        schemaName: `${standardId}.fbs`,
        cid: record.cid,
        peerId: record.peerId,
        timestamp: new Date(record.timestamp).toISOString(),
        dataBytes: record.data,
      }], { source: this.defaultSource });
    } catch (error) {
      console.warn(`FlatSQL engine store: mirror ${standardId} record ${record.cid} failed:`, error);
    }
  }

  async store(
    schema: SchemaName | string,
    data: Uint8Array,
    peerId: string,
    signature: Uint8Array,
  ): Promise<string> {
    const cid = await this.hashHex(data);
    if (this.byCid.has(cid)) return cid; // content-addressed dedupe
    const record: StoredRecord = {
      cid,
      schema: String(schema),
      peerId,
      timestamp: this.nowMs(),
      data: new Uint8Array(data),
      signature: new Uint8Array(signature ?? new Uint8Array(0)),
    };
    this.insertEnvelope(record);
    await this.mirrorIntoStandard(record);
    if (this.flushOnWrite) await this.flush();
    return cid;
  }

  async get(schema: SchemaName | string, cid: string): Promise<StoredRecord | null> {
    const record = this.byCid.get(cid);
    if (!record || record.schema !== String(schema)) return null;
    return { ...record };
  }

  async query(
    schema: SchemaName | string,
    filter?: QueryFilter,
  ): Promise<StoredRecord[]> {
    const clauses = ['schema_name = ?1'];
    const params: Array<string | number> = [String(schema)];
    if (filter?.peerId) {
      params.push(filter.peerId);
      clauses.push(`peer_id = ?${params.length}`);
    }
    if (filter?.since) {
      params.push(filter.since.getTime());
      clauses.push(`source_timestamp >= ?${params.length}`);
    }
    let sql = `SELECT cid FROM sdn_record_index WHERE ${clauses.join(' AND ')} ORDER BY source_timestamp DESC, rowid DESC`;
    if (filter?.limit && filter.limit > 0) {
      params.push(Math.floor(filter.limit));
      sql += ` LIMIT ?${params.length}`;
    }
    const result = this.controlDb.query(sql, params);
    const records: StoredRecord[] = [];
    for (const row of result.rows) {
      const record = this.byCid.get(String(row[0]));
      if (record) records.push({ ...record });
    }
    return records;
  }

  /** Raw SQL over the engine's control table(s) (schema/cid provenance). */
  sql(query: string): { columns: string[]; rows: unknown[][]; rowCount: number } {
    const result = this.controlDb.query(query);
    return { columns: result.columns, rows: result.rows, rowCount: result.rows.length };
  }

  async delete(cid: string): Promise<void> {
    this.controlDb.query('DELETE FROM sdn_record_index WHERE cid = ?1', [cid]);
    this.byCid.delete(cid);
    if (this.flushOnWrite) await this.flush();
  }

  async count(schema?: SchemaName | string): Promise<number> {
    const result = schema !== undefined
      ? this.controlDb.query('SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = ?1', [String(schema)])
      : this.controlDb.query('SELECT COUNT(*) FROM sdn_record_index');
    return Number(result.rows[0]?.[0] ?? 0);
  }

  listRecords(): StoredRecord[] {
    return [...this.byCid.values()].map((record) => ({ ...record }));
  }

  async flush(): Promise<void> {
    if (!this.persistence) return;
    await this.persistence.save(encodeSnapshot(this.listRecords()));
  }

  async close(): Promise<void> {
    if (this.closed) return;
    await this.flush();
    this.closed = true;
    try {
      this.standards?.destroy();
      this.controlDb.destroy();
    } catch {
      // Engine teardown is best-effort; the durable journal is already safe.
    }
  }
}

/**
 * True when the payload is a FlatBuffer carrying the given 4-byte file
 * identifier, either bare (identifier at bytes 4-7) or size-prefixed
 * (identifier at bytes 8-11) — the same acceptance the server's
 * engineRecordPayload applies.
 */
export function flatBufferMatchesFileId(data: Uint8Array, fileId: string): boolean {
  if (fileId.length !== 4) return false;
  return hasFileIdAt(data, 4, fileId) || hasFileIdAt(data, 8, fileId);
}

function hasFileIdAt(data: Uint8Array, offset: number, fileId: string): boolean {
  if (data.byteLength < offset + 4) return false;
  for (let i = 0; i < 4; i++) {
    if (data[offset + i] !== fileId.charCodeAt(i)) return false;
  }
  return true;
}
