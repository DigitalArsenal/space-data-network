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
import type {
  FlatSqlPublishedShard,
  FlatSqlSyncChunk,
  FlatSqlSyncManifest,
  FlatSqlSyncManifestSegment,
  FlatSqlSyncRecordRef,
} from './flatsql-sync';
import type { SchemaName } from './schemas';
import type { StoredRecord, QueryFilter } from './storage';
import {
  configureEngineDatabaseSession,
  createDefaultLocalFlatSqlPersistenceStore,
  createLocalFlatSqlStore,
  getSharedFlatSql,
  iterateFlatSqlSizePrefixedStream,
  LOCAL_FLATSQL_DEFAULT_SOURCE,
  type LocalFlatSqlIngestRecord,
  type LocalFlatSqlPersistenceStore,
  type LocalFlatSqlPinLedgerEntry,
  type LocalFlatSqlPinLedgerQuery,
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
  /**
   * SDS standards mirrored into per-standard engine databases (server
   * layout). THIS OPTION DECIDES DURABILITY: with no schemas there is no
   * per-standard disk-backed lane at all and everything falls to the
   * whole-blob envelope journal (see `envelopeJournalOnly`).
   */
  schemas?: LocalFlatSqlSchema[];
  /** Key namespacing persisted engine state (streams, journal) in IndexedDB. */
  persistenceKey?: string | null;
  /**
   * Acknowledge whole-blob envelope-journal persistence: REQUIRED when a
   * `persistenceKey` is given without `schemas`, ignored otherwise.
   *
   * A `persistenceKey` looks like a request for durable storage, and until
   * 2.0.18 a schemas-less key silently downgraded to ONE `<key>:record-envelopes`
   * blob rewritten in full on every flush and re-ingested in full at boot —
   * measured at 10,544,950 B for a live catalogue cache. That downgrade is
   * now refused rather than silent: pass `schemas` for the disk-backed
   * per-source lane, or pass this flag to say the journal is what you meant.
   */
  envelopeJournalOnly?: boolean;
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

/**
 * A standard registered on this store: the routing key (`OMM`), the 4-byte
 * FlatBuffer file identifier payloads are ROUTED BY, and the engine table its
 * frames land in.
 */
interface RegisteredStandard {
  standardId: string;
  fileId: string;
  tableName: string;
}

interface EngineRecordStoreContext {
  controlDb: ControlDatabase;
  standards: LocalFlatSqlStore | null;
  standardIds: Map<string, RegisteredStandard>;
  options: FlatSQLEngineRecordStoreOptions;
  /** Compact durable index for datasync-fed envelope rows (loop D.4). */
  syncEnvelopePersistence?: { store: LocalFlatSqlPersistenceStore; key: string } | null;
}

// ---------------------------------------------------------------------------
// Datasync-fed ingest (loop D.4): published-shard / flatsql-sync streams
// materialize into THE engine store with pin-ledger provenance intact.
// ---------------------------------------------------------------------------

/** Provenance fallbacks for sync-chunk ingest (per-record refs win). */
export interface EngineSyncChunkIngestOptions {
  /** Explicit source partition override (wins over all provenance). */
  source?: string | null;
  /** Fallback provider when a record ref carries no provider tag. */
  providerId?: string | null;
  /** Fallback source name when a record ref carries no source tag. */
  sourceName?: string | null;
  /** Persist engine streams + envelope index after ingest (default true). */
  persist?: boolean;
}

export interface EngineSyncIngestResult {
  standardId: string;
  /** Frames delivered by the peer (before dedupe). */
  totalRecords: number;
  /** New engine rows materialized (dedupe makes replay 0). */
  ingestedRecords: number;
  /** Envelope index rows written for the first time. */
  indexedEnvelopes: number;
  /** Source partitions touched (server layout: `registerSource` per provider). */
  sources: string[];
  /** True when the pin ledger proved this delivery was already materialized. */
  replayed: boolean;
}

export interface EnginePublishedShardIngestOptions {
  /** Explicit source partition override (wins over header/manifest provenance). */
  source?: string | null;
  /** libp2p peer the shard was fetched from (pin-ledger provenance). */
  providerPeerId?: string | null;
  providerPublicKey?: string | null;
  /** Manifest the shard was announced in (provenance fallback: provider/source/batch/head). */
  manifest?: Pick<FlatSqlSyncManifest, 'providerId' | 'sourceName' | 'batchId' | 'schema' | 'queryProfile' | 'snapshotId' | 'head' | 'highWaterMark'> | null;
  /** Manifest segment for this shard (feed head / chunk-hash fallback). */
  segment?: Pick<FlatSqlSyncManifestSegment, 'rowCount' | 'chunkHash' | 'shardSha256' | 'feedHead'> | null;
  /** Write per-record envelope index rows (cid = sha256, the server computeCID). Default true. */
  indexEnvelopes?: boolean;
  /** Persist engine streams + pin ledger + envelope index after ingest (default true). */
  persist?: boolean;
}

/**
 * Admit options for `storeStream` — the shard-shaped admit point. The
 * standard is NOT one of them: it is routed from the frames' file identifier.
 */
export interface EngineStreamStoreOptions {
  /** Source partition the shard lands in (default: the store's defaultSource). */
  source?: string | null;
  /**
   * ASSERTION only — the standard the caller believes this shard is. Routing
   * is by file identifier; a mismatch is refused, never overridden.
   */
  standardId?: string | null;
  /** Publication content id; keys the frames so re-admitting the shard is a no-op. */
  shardCid?: string | null;
  /** Expected sha256 of the stream bytes, verified before ingest when given. */
  sha256?: string | null;
  /** Provenance rows for the pin ledger (provider / source / batch attribution). */
  pinLedgerEntries?: LocalFlatSqlPinLedgerEntry[];
  /** Flush the lane after ingest (default true). */
  persist?: boolean;
  /**
   * Write a per-FRAME envelope index row. DEFAULT FALSE: a shard's durable
   * substrate is the lane, so envelope rows would be a second durable copy of
   * bytes the lane already holds (measured: 21,104,976 B for one 10.5 MB
   * shard). Opt in only when the frames must also be reachable through the
   * record envelope surface (`get`/`query`/`count`).
   */
  indexEnvelopes?: boolean;
}

export interface EngineStreamStoreResult {
  /** Standard the frames' file identifier routed to. */
  standardId: string;
  source: string;
  /** Frames in the delivered stream. */
  frames: number;
  /** Frames newly materialized in the lane (replay makes this 0). */
  ingested: number;
  /** Envelope index rows written (0 unless `indexEnvelopes`). */
  indexedEnvelopes: number;
  bytes: number;
  /** True when the ingested-keys ledger proved this shard was already resident. */
  replayed: boolean;
}

export interface EngineStreamReadOptions {
  /**
   * Read ONE source partition in ingest order — required for byte-identical
   * round trips. Omitted, the read spans every partition (a union).
   */
  source?: string | null;
}

export class FlatSQLEngineRecordStore {
  private readonly controlDb: ControlDatabase;
  private readonly standards: LocalFlatSqlStore | null;
  private readonly standardIds: Map<string, RegisteredStandard>;
  private readonly byCid = new Map<string, StoredRecord>();
  /** Datasync-fed envelope index rows (schema|cid → row), payloads live in the engine partitions. */
  private readonly syncEnvelopes = new Map<string, SyncEnvelopeRow>();
  private readonly syncEnvelopePersistence: { store: LocalFlatSqlPersistenceStore; key: string } | null;
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
    this.syncEnvelopePersistence = context.syncEnvelopePersistence ?? null;
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
    configureEngineDatabaseSession(controlDb);
    controlDb.query(CONTROL_TABLE_DDL);
    controlDb.query(CONTROL_INDEX_DDL);

    const schemas = options.schemas ?? [];
    if (options.persistenceKey && schemas.length === 0 && options.envelopeJournalOnly !== true) {
      controlDb.destroy();
      throw new Error(
        `FlatSQL record store: persistenceKey ${JSON.stringify(options.persistenceKey)} was given without \`schemas\`, ` +
        'so there is NO per-standard disk-backed lane and every byte would fall to one whole-blob ' +
        `\`${options.persistenceKey}:record-envelopes\` snapshot, rewritten in full on every flush and re-ingested in ` +
        'full at boot. That silent downgrade is refused. Pass `schemas` (the disk-backed per-source lane, and the ' +
        'only way to reach `storeStream`/`readStream`), or pass `envelopeJournalOnly: true` to declare that the ' +
        'whole-blob journal is what you meant.',
      );
    }
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
    const standardIds = new Map<string, RegisteredStandard>();
    for (const schema of schemas) {
      const standardId = schema.standardId.trim().split('.')[0]?.toUpperCase() ?? '';
      if (standardId) standardIds.set(standardId, { standardId, fileId: schema.fileId, tableName: schema.tableName });
    }

    const resolvedOptions: FlatSQLEngineRecordStoreOptions = { ...options };
    let syncEnvelopePersistence: { store: LocalFlatSqlPersistenceStore; key: string } | null = null;
    if (options.persistenceKey) {
      const backing = options.persistenceStore
        ?? createDefaultLocalFlatSqlPersistenceStore({
          desktopPersistenceBaseUrl: options.desktopPersistenceBaseUrl,
          fetch: options.fetch,
        });
      if (backing.available) {
        if (!resolvedOptions.persistence) {
          resolvedOptions.persistence = new PersistenceStoreSnapshotPersistence(
            backing,
            `${options.persistenceKey}:record-envelopes`,
          );
        }
        syncEnvelopePersistence = {
          store: backing,
          key: `${options.persistenceKey}:sync-envelopes`,
        };
      }
    }

    const store = new this({
      controlDb,
      standards,
      standardIds,
      options: resolvedOptions,
      syncEnvelopePersistence,
    });
    await store.replayJournal();
    await store.replaySyncEnvelopes();
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
    return requireSyncStream(standards.queryEpochRawStream(standardId, request ?? null));
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
   * Ingest an aligned size-prefixed FlatBuffer record stream straight into
   * the per-standard engine database (loop D.3: HTTP bulk-stream bodies feed
   * through verbatim — no base64, no JSON re-encode, no per-record
   * round-trips). Delegates to the engine store's stream ingest (dedupe via
   * the ingested-keys ledger; per-(standard,source) persistence).
   */
  async ingestFlatBufferStream(
    standardId: string,
    streamBytes: Uint8Array,
    options?: Parameters<LocalFlatSqlStore['ingestFlatBufferStream']>[2],
  ): Promise<number> {
    return this.requireStandards().ingestFlatBufferStream(standardId, streamBytes, options);
  }

  // -------------------------------------------------------------------------
  // THE STREAM SURFACE (2.0.18) — shard-shaped admit point.
  //
  // `store()` admits ONE record and its provenance envelope (peer id,
  // signature, cid); the envelope journal is that record's durable substrate
  // and the engine vtab is a query cache over it. That contract is unchanged.
  //
  // A published SDS SHARD is a different payload class: already durable,
  // already content-addressed by the publication that named it, carrying no
  // per-frame peer id or signature. It is admitted HERE, into the disk-backed
  // per-source lane, and the lane is its durable substrate — no envelope row,
  // no second copy. That is how the two lanes stay ONE copy each instead of
  // the 2x measured when a shard was forced through `store()`.
  //
  // Round-trip identity is the guarantee that makes this safe for a
  // content-addressed consumer: `readStream` returns the bytes `storeStream`
  // was given, byte for byte, across teardown and reopen.
  // -------------------------------------------------------------------------

  /**
   * Admit an aligned size-prefixed SDS shard into the disk-backed
   * per-source standards lane. The standard is resolved from the FRAMES'
   * FlatBuffer file identifier — header-only routing, never a caller-supplied
   * name (`options.standardId` is an ASSERTION, refused on mismatch).
   *
   * Requires `schemas` (there is no lane without them) and refuses a payload
   * that is not a size-prefixed stream of a registered standard.
   */
  async storeStream(
    streamBytes: Uint8Array,
    options: EngineStreamStoreOptions = {},
  ): Promise<EngineStreamStoreResult> {
    const standards = this.requireStandards();
    const routed = this.routeStreamByFileId(streamBytes);
    const expected = options.standardId?.trim().split('.')[0]?.toUpperCase();
    if (expected && expected !== routed.standard.standardId) {
      throw new Error(
        `FlatSQL stream store: payload frames carry file identifier ${JSON.stringify(routed.standard.fileId)} ` +
        `(standard ${routed.standard.standardId}), but the caller asserted ${JSON.stringify(expected)}. ` +
        'Streams route by file identifier; the assertion is refused rather than overridden.',
      );
    }
    const standardId = routed.standard.standardId;

    if (options.sha256) {
      const actual = await this.hashHex(streamBytes);
      if (actual !== options.sha256.trim().toLowerCase()) {
        throw new Error(
          `FlatSQL stream store: ${standardId} shard sha256 mismatch — expected ${options.sha256}, got ${actual}`,
        );
      }
    }

    const source = options.source?.trim() || this.defaultSource;
    const shardCid = options.shardCid?.trim() || null;
    const ingested = await standards.ingestFlatBufferStream(standardId, streamBytes, {
      source,
      persist: false,
      // Idempotent replay: the shard cid keys every frame, so re-admitting the
      // same shard is a no-op instead of a duplicate ingest.
      ...(shardCid ? { recordKeyPrefix: `shard:${shardCid}` } : {}),
      ...(options.pinLedgerEntries?.length ? { pinLedgerEntries: options.pinLedgerEntries } : {}),
    });
    if (ingested === 0 && options.pinLedgerEntries?.length) {
      await standards.recordPinLedgerEntries(options.pinLedgerEntries, { persist: false });
    }

    // Envelope rows are OPT-IN for a shard (default false): the lane is the
    // durable substrate for this payload class, and indexing 32k frames costs
    // a sha256 each plus a second durable copy of the same bytes.
    let indexedEnvelopes = 0;
    if (options.indexEnvelopes === true) {
      const timestampMs = this.nowMs();
      const schemaName = `${standardId}.fbs`;
      for (const frame of iterateFlatSqlSizePrefixedStream(streamBytes)) {
        const recordCid = await this.hashHex(frame);
        if (this.upsertSyncEnvelope({ schema: schemaName, cid: recordCid, peerId: '', timestampMs })) {
          indexedEnvelopes += 1;
        }
      }
    }

    if (options.persist !== false) {
      await standards.flush(standardId);
      if (indexedEnvelopes > 0) await this.persistSyncEnvelopes();
    }

    return {
      standardId,
      source,
      frames: routed.frames,
      ingested,
      indexedEnvelopes,
      bytes: streamBytes.byteLength,
      replayed: ingested === 0 && routed.frames > 0,
    };
  }

  /**
   * Read a standard's lane back as ONE aligned size-prefixed stream — the
   * paired read for `storeStream`, and the wire format itself.
   *
   * With a `source` this reads that per-source shadow table in `_rowid`
   * order, which is exactly the order the frames were ingested in, so a shard
   * admitted through `storeStream` comes back BYTE-IDENTICAL (verified at
   * 32,141 frames / 10,544,168 B, across teardown + reopen). Without a
   * `source` it reads the unified view across every partition, which is a
   * union — use a source when byte identity is what you need.
   *
   * Returns an empty stream when the standard has no such partition yet.
   */
  readStream(standardId: string, options: EngineStreamReadOptions = {}): Uint8Array {
    const standards = this.requireStandards();
    const standard = this.requireRegisteredStandard(standardId);
    const source = options.source?.trim() || null;
    if (source) {
      const sources = standards.listSources?.(standard.standardId);
      if (Array.isArray(sources) && !sources.includes(source)) return new Uint8Array(0);
      return this.queryRawFlatBufferStream(
        standard.standardId,
        `SELECT _data FROM "${standard.tableName}@${source}" ORDER BY _rowid ASC`,
      );
    }
    return this.queryRawFlatBufferStream(
      standard.standardId,
      `SELECT _data FROM "${standard.tableName}" ORDER BY _rowid ASC`,
    );
  }

  /** Standards registered on this store (routing key, file id, engine table). */
  registeredStandards(): Array<{ standardId: string; fileId: string; tableName: string }> {
    return Array.from(this.standardIds.values()).map((entry) => ({ ...entry }));
  }

  /** Resolve a size-prefixed stream to its standard by FILE IDENTIFIER alone. */
  private routeStreamByFileId(streamBytes: Uint8Array): { standard: RegisteredStandard; frames: number } {
    for (const standard of this.standardIds.values()) {
      const frames = countFlatBufferStreamFramesWithFileId(streamBytes, standard.fileId);
      if (frames > 0) return { standard, frames };
    }
    const registered = Array.from(this.standardIds.values())
      .map((entry) => `${entry.standardId} (${entry.fileId})`)
      .join(', ') || '<none>';
    throw new Error(
      'FlatSQL stream store: payload is not an aligned size-prefixed stream whose frames all carry a registered ' +
      `standard's file identifier. Registered: ${registered}. A single record goes through \`store()\`; ` +
      'a shard must be the wire format (u32 length + frame, repeated, tiling the buffer exactly).',
    );
  }

  /**
   * `store()` is the RECORD admit point. A multi-frame SDS shard handed to it
   * used to be accepted, enveloped whole, and mirrored through a one-record
   * ingest that produced zero queryable rows while doubling storage. The
   * detection fix alone would make that silent (envelope only, no mirror);
   * refusing makes it loud and names the surface that is actually wanted.
   *
   * Only genuine shards are refused: the payload must tile the buffer exactly
   * into 2+ frames that ALL carry a registered standard's file identifier.
   */
  private refuseMultiFrameStream(data: Uint8Array): void {
    for (const standard of this.standardIds.values()) {
      const frames = countFlatBufferStreamFramesWithFileId(data, standard.fileId);
      if (frames > 1) {
        throw new Error(
          `FlatSQL record store: store() admits ONE record, but this payload is an aligned size-prefixed ` +
          `${standard.standardId} STREAM of ${frames} frames (${data.byteLength} B). Use ` +
          '`storeStream(bytes, { source, shardCid })`, which admits it into the disk-backed per-source lane ' +
          'and reads back byte-identical through `readStream(standardId, { source })`.',
        );
      }
    }
  }

  private requireRegisteredStandard(standardId: string): RegisteredStandard {
    const key = String(standardId).trim().split('.')[0]?.toUpperCase() ?? '';
    const standard = this.standardIds.get(key);
    if (!standard) {
      const registered = Array.from(this.standardIds.keys()).join(', ') || '<none>';
      throw new Error(`FlatSQL record store: standard ${JSON.stringify(standardId)} is not registered (have: ${registered})`);
    }
    return standard;
  }

  /**
   * Datasync-fed ingest (loop D.4): materialize a flatsql-sync `read_chunk`
   * response into the engine store with its TRUE provenance. Every record
   * ref's provider/source/batch tags route the record into its per-provider
   * source partition (`registerSource` shadow tables — the server layout),
   * NOT into the node's default `local` partition; the envelope index
   * (`sdn_record_index` mirror) gains a row per ref (cid / peer /
   * source_timestamp), with the payload bytes living in the engine
   * partition exactly like the server keeps them in stream files.
   *
   * Idempotent under sync re-delivery: dedupe keys are the D.1
   * ingested-keys ledger keys (`schema|cid|provider|source|batch|timestamp`
   * — identical to the webUI sync worker's `flatSqlRecordKeys`), and
   * envelope rows upsert by (schema, cid).
   */
  async ingestSyncChunk(
    chunk: FlatSqlSyncChunk,
    options: EngineSyncChunkIngestOptions = {},
  ): Promise<EngineSyncIngestResult> {
    const standards = this.requireStandards();
    const schemaName = normalizeSyncSchemaName(chunk.header.schema);
    const standardId = standardIdFromSchemaName(schemaName);
    const refs = chunk.header.results;
    const frames = chunk.records;

    if (refs.length === 0) {
      // Anonymous stream (no refs): materialize under the fallback
      // provenance and index envelopes by content hash (server computeCID).
      return await this.ingestAnonymousSyncStream(standardId, schemaName, frames, options);
    }
    if (frames.length < refs.length) {
      throw new Error(`FlatSQL sync chunk delivered ${frames.length}/${refs.length} record frames`);
    }

    // Group records by their resolved source partition, preserving order.
    const groups = new Map<string, LocalFlatSqlIngestRecord[]>();
    for (let index = 0; index < refs.length; index += 1) {
      const ref = refs[index];
      const source = resolveSyncSourceName(ref, options, this.defaultSource);
      const group = groups.get(source) ?? [];
      group.push(syncIngestRecordFromRef(schemaName, ref, frames[index]));
      groups.set(source, group);
    }

    let ingestedRecords = 0;
    for (const [source, group] of groups) {
      ingestedRecords += await standards.ingestRecords(standardId, group, { source, persist: false });
    }

    let indexedEnvelopes = 0;
    for (const ref of refs) {
      if (!ref.cid) continue;
      if (this.upsertSyncEnvelope({
        schema: schemaName,
        cid: ref.cid,
        peerId: ref.peerId || ref.producerPeerId || '',
        timestampMs: parseSyncTimestampMs(ref.timestamp, this.nowMs),
      })) {
        indexedEnvelopes += 1;
      }
    }

    if (options.persist !== false) {
      await standards.flush(standardId);
      await this.persistSyncEnvelopes();
    }
    return {
      standardId,
      totalRecords: frames.length,
      ingestedRecords,
      indexedEnvelopes,
      sources: [...groups.keys()],
      replayed: ingestedRecords === 0 && frames.length > 0,
    };
  }

  /**
   * Datasync-fed ingest (loop D.4): materialize a published FlatSQL shard
   * (`read_published_shard` response, or a manifest segment fetched over
   * IPFS) into the engine store. The shard's provider/source/batch
   * provenance routes into a per-provider engine source partition, the pin
   * ledger records the shard exactly like the webUI sync worker does
   * (role `shard`, verified, byteHash/head/rowCount), and — by default —
   * every record frame is indexed as an envelope row whose cid is the
   * sha256 of the frame (byte-identical to the server's computeCID).
   *
   * Idempotent under re-delivery: a verified pin-ledger entry for the same
   * (cid, standard, provider/source/batch) short-circuits to a no-op, and
   * the ingested-keys ledger (`shard:<cid>` keys) catches replays even when
   * the ledger entry is missing.
   */
  async ingestPublishedShard(
    shard: FlatSqlPublishedShard,
    options: EnginePublishedShardIngestOptions = {},
  ): Promise<EngineSyncIngestResult> {
    const standards = this.requireStandards();
    const header = shard.header;
    const cid = header.cid?.trim();
    if (!cid) throw new Error('published shard cid is required');
    const schemaName = normalizeSyncSchemaName(header.schema || options.manifest?.schema || '');
    const standardId = standardIdFromSchemaName(schemaName);

    const providerId = header.providerId ?? options.manifest?.providerId ?? null;
    const sourceName = header.sourceName ?? options.manifest?.sourceName ?? null;
    const batchId = header.batchId ?? options.manifest?.batchId ?? null;
    const source = options.source?.trim()
      || sourceName?.trim()
      || providerId?.trim()
      || this.defaultSource;

    const frameCount = countFlatSqlStreamFrames(shard.streamBytes);
    const expectedRows = Math.max(0, Math.floor(header.rowCount || options.segment?.rowCount || 0));
    if (expectedRows > 0 && frameCount !== expectedRows) {
      throw new Error(`published shard ${cid} carries ${frameCount}/${expectedRows} record frames`);
    }

    // Pin-ledger idempotence: a verified materialized entry for this shard
    // under the same provenance makes re-delivery a no-op.
    const existing = await standards.listPinLedgerEntries({
      cid,
      standardId,
      providerId,
      sourceName,
      batchId,
      role: 'shard',
      verificationState: 'verified',
    });
    if (existing.some((entry) => Boolean(entry.materializedAt))) {
      return {
        standardId,
        totalRecords: frameCount,
        ingestedRecords: 0,
        indexedEnvelopes: 0,
        sources: [source],
        replayed: true,
      };
    }

    const expectedSha = header.shardSha256?.trim() || options.segment?.shardSha256?.trim();
    if (expectedSha) {
      const actualSha = await this.hashHex(shard.streamBytes);
      if (actualSha !== expectedSha) {
        throw new Error(`published shard SHA-256 mismatch for ${cid}`);
      }
    }

    const now = new Date(this.nowMs()).toISOString();
    const ledgerEntry: LocalFlatSqlPinLedgerEntry = {
      cid,
      standardId,
      schemaName,
      providerPeerId: options.providerPeerId ?? null,
      providerPublicKey: options.providerPublicKey ?? null,
      providerId,
      sourceName,
      batchId,
      queryProfile: header.queryProfile || options.manifest?.queryProfile || 'dataset-publication-offset-v1',
      snapshotId: options.segment?.feedHead || options.manifest?.snapshotId || header.head || null,
      head: options.segment?.feedHead || header.head || options.manifest?.head || null,
      highWaterMark: options.manifest?.highWaterMark || null,
      byteHash: expectedSha || options.segment?.chunkHash || null,
      role: 'shard',
      rowCount: expectedRows > 0 ? expectedRows : frameCount,
      byteCount: shard.streamBytes.byteLength,
      verificationState: 'verified',
      materializedAt: now,
      verifiedAt: now,
      updatedAt: now,
    };

    const ingestedRecords = await standards.ingestFlatBufferStream(standardId, shard.streamBytes, {
      source,
      persist: false,
      // NOT the worker's trust-ordered `published:` prefix: the record-key
      // ledger must keep replays idempotent even without the pin ledger.
      recordKeyPrefix: `shard:${cid}`,
      pinLedgerEntries: [ledgerEntry],
    });
    if (ingestedRecords === 0) {
      // Replay caught by the ingested-keys ledger — still record provenance
      // so the pin ledger converges.
      await standards.recordPinLedgerEntries([ledgerEntry], { persist: false });
    }

    let indexedEnvelopes = 0;
    if (options.indexEnvelopes !== false) {
      const timestampMs = this.nowMs();
      for (const frame of iterateFlatSqlSizePrefixedStream(shard.streamBytes)) {
        const recordCid = await this.hashHex(frame);
        if (this.upsertSyncEnvelope({
          schema: schemaName,
          cid: recordCid,
          peerId: options.providerPeerId ?? '',
          timestampMs,
        })) {
          indexedEnvelopes += 1;
        }
      }
    }

    if (options.persist !== false) {
      await standards.flush(standardId);
      await this.persistSyncEnvelopes();
    }
    return {
      standardId,
      totalRecords: frameCount,
      ingestedRecords,
      indexedEnvelopes,
      sources: [source],
      replayed: ingestedRecords === 0 && frameCount > 0,
    };
  }

  /** Pin-ledger provenance surface (provider/source/batch attribution queries). */
  async listPinLedgerEntries(query?: LocalFlatSqlPinLedgerQuery): Promise<LocalFlatSqlPinLedgerEntry[]> {
    return this.requireStandards().listPinLedgerEntries(query);
  }

  async recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[]): Promise<void> {
    return this.requireStandards().recordPinLedgerEntries(entries);
  }

  private async ingestAnonymousSyncStream(
    standardId: string,
    schemaName: string,
    frames: Uint8Array[],
    options: EngineSyncChunkIngestOptions,
  ): Promise<EngineSyncIngestResult> {
    const standards = this.requireStandards();
    const source = options.source?.trim()
      || options.sourceName?.trim()
      || options.providerId?.trim()
      || this.defaultSource;
    const records: LocalFlatSqlIngestRecord[] = [];
    for (const frame of frames) {
      records.push({
        schemaName,
        cid: await this.hashHex(frame),
        providerId: options.providerId ?? null,
        sourceName: options.sourceName ?? null,
        dataBytes: frame,
      });
    }
    const ingestedRecords = await standards.ingestRecords(standardId, records, { source, persist: false });
    let indexedEnvelopes = 0;
    for (const record of records) {
      if (this.upsertSyncEnvelope({
        schema: schemaName,
        cid: record.cid,
        peerId: '',
        timestampMs: this.nowMs(),
      })) {
        indexedEnvelopes += 1;
      }
    }
    if (options.persist !== false) {
      await standards.flush(standardId);
      await this.persistSyncEnvelopes();
    }
    return {
      standardId,
      totalRecords: frames.length,
      ingestedRecords,
      indexedEnvelopes,
      sources: frames.length > 0 ? [source] : [],
      replayed: ingestedRecords === 0 && frames.length > 0,
    };
  }

  /**
   * Upsert a datasync-fed envelope index row. Server mirror: SQLite holds
   * index/provenance rows only — the payload stays in the engine partition
   * (the server's append-only stream files). Returns true on first insert.
   */
  private upsertSyncEnvelope(row: SyncEnvelopeRow): boolean {
    const key = `${row.schema}|${row.cid}`;
    const isNew = !this.syncEnvelopes.has(key) && !this.byCid.has(row.cid);
    this.syncEnvelopes.set(key, row);
    this.controlDb.query(
      'INSERT OR REPLACE INTO sdn_record_index (schema_name, cid, peer_id, source_timestamp, created_at) VALUES (?1, ?2, ?3, ?4, ?5)',
      [row.schema, row.cid, row.peerId, row.timestampMs, Math.floor(this.nowMs() / 1000)],
    );
    return isNew;
  }

  private async replaySyncEnvelopes(): Promise<void> {
    if (!this.syncEnvelopePersistence) return;
    const value = await this.syncEnvelopePersistence.store.readJson(this.syncEnvelopePersistence.key);
    if (!Array.isArray(value)) return;
    for (const candidate of value) {
      if (!candidate || typeof candidate !== 'object') continue;
      const row = candidate as Partial<SyncEnvelopeRow>;
      if (typeof row.schema !== 'string' || typeof row.cid !== 'string' || !row.schema || !row.cid) continue;
      this.upsertSyncEnvelope({
        schema: row.schema,
        cid: row.cid,
        peerId: typeof row.peerId === 'string' ? row.peerId : '',
        timestampMs: typeof row.timestampMs === 'number' && Number.isFinite(row.timestampMs)
          ? row.timestampMs
          : this.nowMs(),
      });
    }
  }

  private async persistSyncEnvelopes(): Promise<void> {
    if (!this.syncEnvelopePersistence) return;
    if (this.syncEnvelopes.size === 0) return;
    await this.syncEnvelopePersistence.store.writeJson(
      this.syncEnvelopePersistence.key,
      Array.from(this.syncEnvelopes.values()),
    );
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
    return requireSyncStream(standards.queryRawFlatBufferStream(standardId, sql, params));
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
    this.refuseMultiFrameStream(data);
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
    for (const [key, row] of this.syncEnvelopes) {
      if (row.cid === cid) this.syncEnvelopes.delete(key);
    }
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
    await this.persistSyncEnvelopes();
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

// ---------------------------------------------------------------------------
// Datasync ingest helpers (loop D.4)
// ---------------------------------------------------------------------------

interface SyncEnvelopeRow {
  schema: string;
  cid: string;
  peerId: string;
  timestampMs: number;
}

/**
 * The engine record store always wraps the in-process (synchronous) engine
 * store; the async raw-stream variants exist only on worker-proxied stores.
 * No silent adaptation: a Promise here means a mis-wired store.
 */
function requireSyncStream(stream: Uint8Array | Promise<Uint8Array>): Uint8Array {
  if (stream instanceof Uint8Array) return stream;
  throw new Error(
    'FlatSQLEngineRecordStore requires a synchronous standards store; worker-proxied stores expose the async raw-stream API directly',
  );
}

function normalizeSyncSchemaName(schema: string): string {
  const trimmedSchema = schema.trim();
  if (!trimmedSchema) throw new Error('FlatSQL sync schema is required');
  return trimmedSchema;
}

function standardIdFromSchemaName(schemaName: string): string {
  const standardId = schemaName.trim().split('.')[0]?.toUpperCase() ?? '';
  if (!standardId) throw new Error('FlatSQL sync schema is required');
  return standardId;
}

/**
 * Resolve a sync record's engine source partition. TRUE provenance first:
 * an explicit caller override wins, then the record ref's own source tag,
 * then the query-level fallbacks, then the record's provider id — the
 * server's default `local` partition is the last resort only.
 */
function resolveSyncSourceName(
  ref: FlatSqlSyncRecordRef,
  options: EngineSyncChunkIngestOptions,
  defaultSource: string,
): string {
  return options.source?.trim()
    || ref.sourceName?.trim()
    || options.sourceName?.trim()
    || ref.providerId?.trim()
    || options.providerId?.trim()
    || defaultSource;
}

function syncIngestRecordFromRef(
  schemaName: string,
  ref: FlatSqlSyncRecordRef,
  frame: Uint8Array,
): LocalFlatSqlIngestRecord {
  return {
    // Dedupe key = schema|cid|provider|source|batch|timestamp — identical
    // to the webUI worker's flatSqlRecordKeys, so records deduplicate
    // across every sync path that feeds the same engine store.
    schemaName: ref.schemaName && ref.schemaName !== 'unknown' ? ref.schemaName : schemaName,
    cid: ref.cid,
    peerId: ref.peerId || null,
    providerId: ref.providerId ?? null,
    sourceName: ref.sourceName ?? null,
    batchId: ref.batchId ?? null,
    timestamp: ref.timestamp ?? null,
    dataBytes: frame,
  };
}

function parseSyncTimestampMs(timestamp: string | undefined, nowMs: () => number): number {
  if (timestamp) {
    const parsed = Date.parse(timestamp);
    if (Number.isFinite(parsed)) return parsed;
  }
  return nowMs();
}

/** Frame count of an aligned little-endian size-prefixed record stream. */
function countFlatSqlStreamFrames(streamBytes: Uint8Array): number {
  let count = 0;
  for (const frame of iterateFlatSqlSizePrefixedStream(streamBytes)) {
    void frame;
    count += 1;
  }
  return count;
}

/**
 * True when the payload is ONE FlatBuffer RECORD carrying the given 4-byte
 * file identifier, either bare (identifier at bytes 4-7) or size-prefixed
 * (identifier at bytes 8-11).
 *
 * The size-prefixed arm requires the u32 length prefix to account for the
 * WHOLE buffer — the server's rule verbatim (sdn-server/internal/storage/
 * engine_records.go:137 `engineRecordPayload`: `uint32(data[:4])+4 ==
 * len(data) && SizePrefixedOMMBufferHasIdentifier(data)`). Without that
 * length equality an aligned size-prefixed STREAM of N frames matches as a
 * single record — its first frame's u32 prefix puts the file identifier at
 * bytes 8-11 — and a 10.5 MB shard gets handed to a one-record ingest,
 * which is the false positive measured in 2.0.17 (0 rows, doubled
 * IndexedDB, +342 ms). sdn-js was MORE permissive than the Go host it
 * mirrors; this closes that divergence. Multi-frame streams are classified
 * by `flatBufferStreamMatchesFileId`.
 */
export function flatBufferMatchesFileId(data: Uint8Array, fileId: string): boolean {
  if (fileId.length !== 4) return false;
  if (hasFileIdAt(data, 4, fileId)) return true;
  return isSingleSizePrefixedBuffer(data) && hasFileIdAt(data, 8, fileId);
}

/** True when the u32 length prefix accounts for the whole buffer (ONE record). */
function isSingleSizePrefixedBuffer(data: Uint8Array): boolean {
  if (data.byteLength < 12) return false;
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  return view.getUint32(0, true) + 4 === data.byteLength;
}

/**
 * True when the payload is an ALIGNED SIZE-PREFIXED STREAM whose frames all
 * carry the given file identifier — the SDS shard shape (the wire format,
 * and what `queryRawFlatBufferStream` returns).
 *
 * Strict by construction: the frame lengths must tile the buffer exactly and
 * EVERY frame must carry the identifier, so opaque record bytes can never be
 * mistaken for a shard. A one-frame stream satisfies this and also satisfies
 * `flatBufferMatchesFileId` — that overlap is deliberate: a single record is
 * legitimately admissible through either lane.
 */
export function flatBufferStreamMatchesFileId(data: Uint8Array, fileId: string): boolean {
  return countFlatBufferStreamFramesWithFileId(data, fileId) > 0;
}

/**
 * Frame count of an aligned size-prefixed stream whose frames ALL carry
 * `fileId`; 0 when the buffer is not such a stream. Never throws — this is a
 * classifier, run on untrusted bytes.
 */
export function countFlatBufferStreamFramesWithFileId(data: Uint8Array, fileId: string): number {
  if (fileId.length !== 4 || data.byteLength < 12) return 0;
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  let offset = 0;
  let frames = 0;
  while (offset < data.byteLength) {
    if (data.byteLength - offset < 4) return 0;
    const length = view.getUint32(offset, true);
    offset += 4;
    const nextOffset = offset + length;
    // Guard against a bogus u32 overflowing past the buffer (or wrapping).
    if (length < 8 || nextOffset > data.byteLength || nextOffset < offset) return 0;
    if (!hasFileIdAt(data, offset + 4, fileId)) return 0;
    offset = nextOffset;
    frames += 1;
  }
  return frames;
}

function hasFileIdAt(data: Uint8Array, offset: number, fileId: string): boolean {
  if (data.byteLength < offset + 4) return false;
  for (let i = 0; i < 4; i++) {
    if (data[offset + i] !== fileId.charCodeAt(i)) return false;
  }
  return true;
}
