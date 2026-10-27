/** SDN's consumer adapter: external SDS conversion, one binary FlatSQL store.
 * Record durability belongs to FlatSQL (Node filesystem / browser OPFS).
 * The host's key-value backend stores only publication/pin metadata.
 */
import { FlatSQL, type FlatSQLOptions } from 'flatsql';
import { encodeFlatSqlQuery, decodeFlatSqlResult, type FlatSqlQueryParam as QueryParam } from 'space-data-module-sdk/runtime-host';
import { FlatcRunner } from 'flatc-wasm/runner';
import { resolveEngineEpochQuery, DEFAULT_ENGINE_EPOCH_SPECS, type EngineEpochProfileSpec,
  type EngineEpochQueryProfilesConfig, type EngineEpochQueryRequest } from './epoch-query-sql';
import { validateReadOnlySql, type ReadOnlySqlValidationOptions } from './read-only-sql-sandbox';
import { sha384Digest } from './crypto/sha384';
import { verifyNodeSignedModule } from './module-artifact';
export interface LocalFlatSqlIngestRecord {
  schemaName: string;
  cid: string;
  peerId?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
  batchId?: string | null;
  timestamp?: string | null;
  dataBytes?: Uint8Array;
  signature?: Uint8Array;
}

export interface LocalFlatSqlSchema {
  standardId: string;
  tableName: string;
  fileId: string;
  bfbs?: Uint8Array;
  schema?: string;
  epochSpec?: {
    partitionColumn?: string;
    epochColumn?: string;
  } | null;
}

export const LOCAL_FLATSQL_DEFAULT_SOURCE = 'local';

export interface LocalFlatSqlEngineOptions extends FlatSQLOptions {
  wasmPath?: string | null;
  publisherKey?: string | null;
  computeSHA384?: (data: ArrayBuffer) => Promise<Uint8Array | string> | Uint8Array | string;
}

export interface LocalFlatSqlStoreOptions {
  schemas: LocalFlatSqlSchema[];
  engine?: LocalFlatSqlEngineOptions | null;
  persistenceKey?: string | null;
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
  queryProfiles?: EngineEpochQueryProfilesConfig | null;
  nowSeconds?: (() => number) | null;
}

export interface ClearLocalFlatSqlStoreOptions {
  persistenceKey: string;
  standardIds: string[];
  engine?: LocalFlatSqlEngineOptions | null;
  schemas?: Pick<LocalFlatSqlSchema, 'standardId' | 'tableName'>[];
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
}

export interface LocalFlatSqlQueryResult {
  columns: string[];
  rows: unknown[][];
  records: Array<Record<string, unknown>>;
}

export interface LocalFlatSqlQueryOptions extends ReadOnlySqlValidationOptions { parameters?: QueryParam[]; }

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
  addStandard(schema: LocalFlatSqlSchema): Promise<void>;
  ingestRecords(standardId: string, records: LocalFlatSqlIngestRecord[], sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): Promise<number>;
  ingestFlatBufferStream(standardId: string, streamBytes: Uint8Array, options?: LocalFlatSqlStreamIngestOptions | null): Promise<number>;
  listSources?(standardId: string): string[] | Promise<string[]>;
  clearStandard(standardId: string, options?: LocalFlatSqlClearOptions): Promise<void>;
  createStandardReplacementStore(standardId: string): Promise<LocalFlatSqlStore>;
  replaceStandardFrom(standardId: string, replacementStore: LocalFlatSqlStore, entries: LocalFlatSqlPinLedgerEntry[], options?: LocalFlatSqlPinLedgerWriteOptions): Promise<void>;
  flush(standardId?: string): Promise<void>;
  recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[], options?: LocalFlatSqlPinLedgerWriteOptions): Promise<void>;
  listPinLedgerEntries(query?: LocalFlatSqlPinLedgerQuery): Promise<LocalFlatSqlPinLedgerEntry[]>;
  query(sql: string, standardId?: string, options?: LocalFlatSqlQueryOptions): LocalFlatSqlQueryResult | Promise<LocalFlatSqlQueryResult>;
  queryEpochRawStream?(standardId: string, request?: EngineEpochQueryRequest | null): Uint8Array | Promise<Uint8Array>;
  queryRawFlatBufferStream?(standardId: string, sql: string, params?: QueryParam[]): Uint8Array | Promise<Uint8Array>;
  getStats(options?: LocalFlatSqlStatsOptions): LocalFlatSqlStandardStats[] | Promise<LocalFlatSqlStandardStats[]>;
  removeRecords(standardId: string, records: Uint8Array[]): Promise<void>;
  destroy(): Promise<void>;
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


const BATCH_BYTES = 4 * 1024 * 1024;
function quoteIdentifier(value: string): string { return '"' + value.replace(/"/g, '""') + '"'; }
function rootForKey(key: string): string { return `sdn-flatsql/${encodeURIComponent(key)}`; }
function persistedPinLedgerKey(key: string): string { return `${key}:pin-ledger`; }
function normalizeSourceName(source?: string | null): string { return source?.trim() || LOCAL_FLATSQL_DEFAULT_SOURCE; }
function normalizeIngestOptions(value?: string | LocalFlatSqlIngestOptions | null): LocalFlatSqlIngestOptions {
  return typeof value === 'string' ? { source: value } : value ?? {};
}
export function stripSdnFlatBufferSizePrefix(bytes: Uint8Array): Uint8Array {
  if (bytes.length >= 12 && new DataView(bytes.buffer, bytes.byteOffset, 4).getUint32(0, true) === bytes.length - 4 && hasFlatBufferFileIdentifier(bytes, 8)) return bytes.subarray(4);
  return bytes;
}
function frame(payload: Uint8Array): Uint8Array {
  const bytes = new Uint8Array(payload.length + 4);
  new DataView(bytes.buffer).setUint32(0, payload.length, true); bytes.set(payload, 4); return bytes;
}
function joinFrames(records: Uint8Array[]): Uint8Array {
  const bytes = new Uint8Array(records.reduce((n, r) => n + r.length + 4, 0));
  const view = new DataView(bytes.buffer); let at = 0;
  for (const record of records) { view.setUint32(at, record.length, true); bytes.set(record, at + 4); at += record.length + 4; }
  return bytes;
}
async function engineOptions(options: LocalFlatSqlStoreOptions): Promise<FlatSQLOptions> {
  const { wasmPath, publisherKey, computeSHA384: _digest, ...engine } = options.engine ?? {};
  if (options.persistenceKey && !engine.root) engine.root = rootForKey(options.persistenceKey);
  if (!options.persistenceKey && !engine.root) engine.backend = 'memory';
  if (wasmPath) {
    if (!publisherKey) throw new Error('The node reported no publisher key for its engine.');
    const base = wasmPath.replace(/\/+$/, '');
    const response = await (options.fetch ?? fetch)(`${base}/flatsql.wasm`, { credentials: 'same-origin' });
    if (!response.ok) throw new Error(`Engine download failed (${response.status})`);
    const verified = verifyNodeSignedModule(new Uint8Array(await response.arrayBuffer()), [publisherKey]).module;
    const digest = sha384Digest(verified);
    engine.wasmBytes = verified;
    engine.verify = bytes => { const actual = sha384Digest(bytes); if (!actual.every((b, i) => b === digest[i])) throw new Error('Engine bytes changed after signature verification'); };
    engine.workerUrl = `${base}/runtime/browser-guest.mjs`;
    engine.threadWorkerUrl = `${base}/runtime/wasiThreadBrowserWorker.mjs`;
    engine.ioWorkerUrl = `${base}/runtime/opfsIoWorker.mjs`;
  }
  return engine;
}
let compilerPromise: Promise<FlatcRunner> | undefined;
async function binarySchema(schema: LocalFlatSqlSchema): Promise<Uint8Array> {
  if (schema.bfbs) return schema.bfbs;
  if (!schema.schema) throw new Error(`Binary schema required for ${schema.standardId}`);
  const compiler = await (compilerPromise ??= FlatcRunner.init());
  compiler.mountFile('/schema/main.fbs', schema.schema); compiler.mountFile('/out/.keep', '');
  const result = compiler.runCommand(['--binary', '--schema', '-o', '/out', '/schema/main.fbs']);
  if (result.code) throw new Error(`Schema compilation failed: ${result.stderr}`);
  const bytes = compiler.readFile('/out/main.bfbs');
  if (!(bytes instanceof Uint8Array)) throw new Error('Compiler did not produce a binary schema');
  return bytes.slice();
}
export async function createLocalFlatSqlStore(options: LocalFlatSqlStoreOptions): Promise<LocalFlatSqlStore> {
  const persistence = createLocalFlatSqlPersistenceStore(options);
  const db = await FlatSQL.open(await engineOptions(options));
  try {
    const pins = options.persistenceKey ? await readPersistedPinLedgerEntries(persistence, persistedPinLedgerKey(options.persistenceKey)) : new Map<string, LocalFlatSqlPinLedgerEntry>();
    const store = new WasmLocalFlatSqlStore(db, options, persistence, pins);
    for (const schema of options.schemas) await store.addStandard(schema);
    return store;
  } catch (error) { await db.close(); throw error; }
}
export async function clearLocalFlatSqlStore(options: ClearLocalFlatSqlStoreOptions): Promise<void> {
  const ids = new Set(options.standardIds.map(normalizeStandardId));
  const names = new Map(options.schemas?.map(schema => [normalizeStandardId(schema.standardId), schema.tableName]));
  const db = await FlatSQL.open(await engineOptions({ ...options, schemas: [] }));
  try {
    const surface = decodeFlatSqlResult(await db.surface());
    const tables = new Set(surface.rows.filter(row => row[2] === '').map(row => row[0]));
    for (const id of ids) {
      const name = names.get(id) ?? id;
      if (!tables.has(name)) continue;
      for (;;) {
        const { rows } = decodeFlatSqlResult(await db.execute(encodeFlatSqlQuery(`SELECT _data FROM ${quoteIdentifier(name)} LIMIT 64`)));
        if (!rows.length) break;
        for (const [payload] of rows) await db.remove(name, frame(payload as Uint8Array));
      }
    }
  } finally { await db.close(); }
  const persistence = createLocalFlatSqlPersistenceStore(options), key = persistedPinLedgerKey(options.persistenceKey);
  const remaining = [...(await readPersistedPinLedgerEntries(persistence, key)).values()].filter(entry => !ids.has(entry.standardId));
  if (remaining.length) await writePersistedPinLedgerEntries(persistence, key, remaining);
  else await persistence.deleteKey(key);
}
class WasmLocalFlatSqlStore implements LocalFlatSqlStore {
  private readonly schemas = new Map<string, LocalFlatSqlSchema>();
  private readonly opening = new Map<string, Promise<void>>();
  constructor(private readonly db: FlatSQL, private readonly options: LocalFlatSqlStoreOptions,
    private readonly persistenceStore: LocalFlatSqlPersistenceStore,
    private readonly pinLedger: Map<string, LocalFlatSqlPinLedgerEntry>) {}
  async addStandard(schema: LocalFlatSqlSchema): Promise<void> {
    const id = normalizeStandardId(schema.standardId);
    if (this.schemas.has(id)) return;
    if (this.opening.has(id)) return this.opening.get(id);
    const pending = (async () => {
      const bfbs = await binarySchema(schema);
      await this.db.registerSchema({ name: schema.tableName, fileIdentifier: schema.fileId, bfbs, fullText: true });
      this.schemas.set(id, { ...schema, standardId: id, bfbs });
    })().finally(() => this.opening.delete(id));
    this.opening.set(id, pending); return pending;
  }
  private schema(id: string): LocalFlatSqlSchema {
    const schema = this.schemas.get(normalizeStandardId(id));
    if (!schema) throw new Error(`No FlatSQL schema registered for ${id}`);
    return schema;
  }
  private async rows(sql: string, parameters: QueryParam[] = []) {
    return decodeFlatSqlResult(await this.db.execute(encodeFlatSqlQuery(sql, parameters)));
  }
  async ingestRecords(id: string, records: LocalFlatSqlIngestRecord[], sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): Promise<number> {
    const schema = this.schema(id), options = normalizeIngestOptions(sourceOrOptions); let count = 0;
    for (const record of records) {
      if (!record.dataBytes) continue;
      const payload = stripSdnFlatBufferSizePrefix(record.dataBytes);
      const timestamp = record.timestamp ? Math.floor(Date.parse(record.timestamp) / 1000) : undefined;
      if (timestamp !== undefined && !Number.isFinite(timestamp)) throw new Error('Invalid record timestamp');
      const receipt = decodeFlatSqlResult(await this.db.ingest(schema.tableName, frame(payload), {
        source: normalizeSourceName(options.source ?? record.sourceName), provider: record.providerId ?? undefined,
        producer: record.peerId ?? undefined, batch: record.batchId ?? undefined, timestamp,
        signatures: record.signature ? [record.signature] : undefined,
      }));
      if (receipt.rows.some(row => row[1] === 0)) throw new Error('FlatSQL rejected an input record');
      count += receipt.rows.filter(row => row[1] === 1).length;
    }
    return count;
  }
  async ingestFlatBufferStream(id: string, stream: Uint8Array, options: LocalFlatSqlStreamIngestOptions | null = null): Promise<number> {
    const schema = this.schema(id), info = flatSqlSizePrefixedStreamInfo(stream, options?.skipRecords ?? 0);
    if (!info.allFramesHaveDirectFileIdentifier) throw new Error('FlatBuffer file identifier missing');
    let batch: Uint8Array[] = [], size = 0, count = 0;
    const flush = async () => {
      if (!batch.length) return;
      const receipt = decodeFlatSqlResult(await this.db.ingest(schema.tableName, joinFrames(batch), { source: normalizeSourceName(options?.source) }));
      if (receipt.rows.some(row => row[1] === 0)) throw new Error('FlatSQL rejected an input record');
      count += receipt.rows.filter(row => row[1] === 1).length;
      batch = []; size = 0;
    };
    for (const payload of iterateFlatSqlSizePrefixedStream(stream.subarray(info.ingestStartOffset))) {
      if (payload.length + 4 > BATCH_BYTES) throw new Error('Record exceeds the SDN ingest request limit');
      if (size + payload.length + 4 > BATCH_BYTES) await flush();
      batch.push(payload); size += payload.length + 4;
    }
    await flush();
    if (options?.pinLedgerEntries?.length) await this.recordPinLedgerEntries(options.pinLedgerEntries, options);
    return count;
  }
  async removeRecords(id: string, records: Uint8Array[]): Promise<void> {
    const schema = this.schema(id);
    for (const payload of records) await this.db.remove(schema.tableName, frame(stripSdnFlatBufferSizePrefix(payload)));
  }
  async clearStandard(id: string, options: LocalFlatSqlClearOptions = {}): Promise<void> {
    const schema = this.schema(id);
    for (;;) {
      const { rows } = await this.rows(`SELECT _data FROM ${quoteIdentifier(schema.tableName)} LIMIT 64`);
      if (!rows.length) break;
      await this.removeRecords(id, rows.map(row => row[0] as Uint8Array));
    }
    for (const [key, entry] of this.pinLedger) if (entry.standardId === schema.standardId) this.pinLedger.delete(key);
    if (options.persist !== false) await this.flush();
  }
  async createStandardReplacementStore(id: string): Promise<LocalFlatSqlStore> {
    const engine = { ...this.options.engine, root: undefined, path: undefined, backend: 'memory' as const };
    return createLocalFlatSqlStore({ ...this.options, schemas: [this.schema(id)], persistenceKey: null, engine });
  }
  async replaceStandardFrom(id: string, replacement: LocalFlatSqlStore, entries: LocalFlatSqlPinLedgerEntry[], options: LocalFlatSqlPinLedgerWriteOptions = {}): Promise<void> {
    if (!(replacement instanceof WasmLocalFlatSqlStore)) throw new Error('Replacement must be a FlatSQL store');
    if (replacement === this) throw new Error('Replacement must be a separate staging store');
    const schema = replacement.schema(id), target = this.schema(id);
    if (schema.fileId !== target.fileId || schema.bfbs!.length !== target.bfbs!.length || !schema.bfbs!.every((byte, i) => byte === target.bfbs![i]))
      throw new Error('Replacement schema must match the destination');
    const pins = entries.map(normalizePinLedgerEntry);
    // The staging store is fully ingested before the durable store changes.
    await this.clearStandard(id, { persist: false });
    let after: QueryParam = 0;
    for (;;) {
      const result = await replacement.rows(`SELECT _rowid, _cid, _data FROM ${quoteIdentifier(schema.tableName)} WHERE _rowid > ?1 ORDER BY _rowid LIMIT 64`, [after]);
      if (!result.rows.length) break;
      for (const [seq, cid, data] of result.rows) {
        const copies = await replacement.rows('SELECT provider,source,batch,peer,ts,sig FROM flatsql_provenance(?1,?2) WHERE seq=?3', [schema.tableName, cid as string, seq as number | bigint]);
        if (!copies.rows.length) throw new Error('Staged record has no provenance');
        await this.ingestRecords(id, copies.rows.map(([provider, source, batch, peer, timestamp, signature]) => ({
          schemaName: id, cid: cid as string, dataBytes: data as Uint8Array, signature: signature as Uint8Array | undefined,
          providerId: provider as string | null, sourceName: source as string | null, batchId: batch as string | null,
          peerId: peer as string, timestamp: new Date(Number(timestamp) * 1000).toISOString(),
        })));
      }
      after = result.rows.at(-1)![0] as number | bigint;
    }
    await this.recordPinLedgerEntries(pins, options);
  }
  async recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[], options: LocalFlatSqlPinLedgerWriteOptions = {}): Promise<void> {
    for (const candidate of entries) { const entry = normalizePinLedgerEntry(candidate); this.pinLedger.set(pinLedgerEntryKey(entry), entry); }
    if (options.persist !== false) await this.flush();
  }
  async listPinLedgerEntries(query: LocalFlatSqlPinLedgerQuery = {}): Promise<LocalFlatSqlPinLedgerEntry[]> {
    const normalized = normalizePinLedgerQuery(query);
    return [...this.pinLedger.values()].filter(entry => pinLedgerEntryMatches(entry, normalized))
      .sort((a, b) => (b.updatedAt ?? '').localeCompare(a.updatedAt ?? '') || a.cid.localeCompare(b.cid)).map(entry => ({ ...entry }));
  }
  async listSources(id: string): Promise<string[]> {
    const schema = this.schema(id);
    const surface = decodeFlatSqlResult(await this.db.surface());
    const sources = new Set(surface.rows.filter(row => row[2] && row[0] === schema.tableName + '@' + row[2]).map(row => String(row[2])));
    const local = await this.rows(`SELECT _rowid FROM ${quoteIdentifier(schema.tableName + '@local')} LIMIT 1`);
    if (local.rows.length) sources.add('local');
    return [...sources].sort();
  }
  async query(sql: string, id?: string, options: LocalFlatSqlQueryOptions = {}): Promise<LocalFlatSqlQueryResult> {
    if (id) this.schema(id);
    const validation = validateReadOnlySql(sql, options);
    if (!validation.ok) throw new Error(`FlatSQL queries must be read-only: ${validation.diagnostics.join(' ')}`);
    const result = decodeFlatSqlResult(await this.db.execute(encodeFlatSqlQuery(validation.sql, options.parameters), {
      sandbox: true, maxRows: validation.limits!.maxRows, maxResultBytes: validation.limits!.maxBytes, timeoutMs: validation.limits!.timeoutMs,
    }));
    return { ...result, records: result.rows.map(row => rowToRecord(result.columns, row)) };
  }
  async queryEpochRawStream(id: string, request: EngineEpochQueryRequest | null = null): Promise<Uint8Array> {
    const schema = this.schema(id), profiles = this.options.queryProfiles;
    const query = resolveEngineEpochQuery({ spec: engineEpochSpecForSchema(schema), request,
      defaults: profiles?.[`${schema.standardId}.fbs`] ?? profiles?.[schema.standardId] ?? null, nowSeconds: this.options.nowSeconds });
    return this.queryRawFlatBufferStream(id, query.sql, query.params);
  }
  async queryRawFlatBufferStream(id: string, sql: string, parameters: QueryParam[] = []): Promise<Uint8Array> {
    this.schema(id);
    const validation = validateReadOnlySql(sql);
    if (!validation.ok) throw new Error(`FlatSQL queries must be read-only: ${validation.diagnostics.join(' ')}`);
    return this.db.execute(encodeFlatSqlQuery(sql, parameters), { records: true, sandbox: true });
  }
  async getStats(options: LocalFlatSqlStatsOptions = {}): Promise<LocalFlatSqlStandardStats[]> {
    const stats: LocalFlatSqlStandardStats[] = [];
    for (const schema of this.schemas.values()) {
      const { rows } = await this.rows(`SELECT count(*), ${options.includeCachedBytes === false ? '0' : 'coalesce(sum(length(_data)),0)'} FROM ${quoteIdentifier(schema.tableName)}`);
      stats.push({ standardId: schema.standardId, tableName: schema.tableName, recordCount: Number(rows[0][0]),
        cachedBytes: Number(rows[0][1]), ingestedRecordCount: Number(rows[0][0]), ...this.pinStatsForStandard(schema.standardId) });
    }
    return stats;
  }
  async flush(_id?: string): Promise<void> {
    if (this.options.persistenceKey) await writePersistedPinLedgerEntries(this.persistenceStore, persistedPinLedgerKey(this.options.persistenceKey), [...this.pinLedger.values()]);
  }
  async destroy(): Promise<void> { await this.flush(); await this.db.close(); }
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
export function isReadOnlyFlatSqlQuery(sql: string): boolean { return validateReadOnlySql(sql).ok; }
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
    if (length < 8 || nextOffset > streamBytes.byteLength) {
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
    if (length < 8 || nextOffset > streamBytes.byteLength) {
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
    if (length < 8 || nextOffset > streamBytes.byteLength) {
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
