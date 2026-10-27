/** SDN record/query adapter over the unified binary FlatSQL engine. */
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
  createDefaultLocalFlatSqlPersistenceStore,
  createLocalFlatSqlStore,
  iterateFlatSqlSizePrefixedStream,
  LOCAL_FLATSQL_DEFAULT_SOURCE,
  stripSdnFlatBufferSizePrefix,
  type LocalFlatSqlEngineOptions,
  type LocalFlatSqlIngestRecord,
  type LocalFlatSqlPersistenceStore,
  type LocalFlatSqlPinLedgerEntry,
  type LocalFlatSqlPinLedgerQuery,
  type LocalFlatSqlSchema,
  type LocalFlatSqlStore,
} from './local-flatsql';

export type EngineQueryParam = null | boolean | number | bigint | string | Uint8Array;

async function sha256Hex(data: Uint8Array): Promise<string> {
  const digest = await sha256(new Uint8Array(data));
  return Array.from(digest)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

export interface FlatSQLEngineRecordStoreOptions {
  schemas?: LocalFlatSqlSchema[];
  engine?: LocalFlatSqlEngineOptions;
  persistenceKey?: string | null;
  desktopPersistenceBaseUrl?: string | null;
  fetch?: ((input: string | URL | Request, init?: RequestInit) => Promise<Response>) | null;
  persistenceStore?: LocalFlatSqlPersistenceStore | null;
  defaultSource?: string;
  queryProfiles?: EngineEpochQueryProfilesConfig | null;
  nowSeconds?: (() => number) | null;
  nowMs?: () => number;
  hashHex?: (data: Uint8Array) => Promise<string>;
  flushOnWrite?: boolean;
}

interface RegisteredStandard {
  standardId: string;
  fileId: string;
  tableName: string;
}

interface EngineRecordStoreContext {
  standards: LocalFlatSqlStore | null;
  standardIds: Map<string, RegisteredStandard>;
  options: FlatSQLEngineRecordStoreOptions;
  syncEnvelopePersistence?: { store: LocalFlatSqlPersistenceStore; key: string } | null;
}

export interface EngineSyncChunkIngestOptions {
  source?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
  persist?: boolean;
}

export interface EngineSyncIngestResult {
  standardId: string;
  totalRecords: number;
  ingestedRecords: number;
  indexedEnvelopes: number;
  sources: string[];
  replayed: boolean;
}

export interface EnginePublishedShardIngestOptions {
  source?: string | null;
  providerPeerId?: string | null;
  providerPublicKey?: string | null;
  manifest?: Pick<FlatSqlSyncManifest, 'providerId' | 'sourceName' | 'batchId' | 'schema' | 'queryProfile' | 'snapshotId'> | null;
  segment?: Pick<FlatSqlSyncManifestSegment, 'rowCount' | 'chunkHash' | 'shardSha256' | 'feedHead'> | null;
  indexEnvelopes?: boolean;
  persist?: boolean;
}

export interface EngineStreamStoreOptions {
  source?: string | null;
  standardId?: string | null;
  shardCid?: string | null;
  sha256?: string | null;
  pinLedgerEntries?: LocalFlatSqlPinLedgerEntry[];
  persist?: boolean;
  indexEnvelopes?: boolean;
}

export interface EngineStreamStoreResult {
  standardId: string;
  source: string;
  frames: number;
  ingested: number;
  indexedEnvelopes: number;
  bytes: number;
  replayed: boolean;
}

export interface EngineStreamReadOptions {
  source?: string | null;
}

export class FlatSQLEngineRecordStore {
  private readonly standards: LocalFlatSqlStore | null;
  private readonly standardIds: Map<string, RegisteredStandard>;
  private readonly syncEnvelopes = new Map<string, SyncEnvelopeRow>();
  private readonly syncEnvelopePersistence: { store: LocalFlatSqlPersistenceStore; key: string } | null;
  private readonly flushOnWrite: boolean;
  private readonly hashHex: (data: Uint8Array) => Promise<string>;
  private readonly nowMs: () => number;
  private readonly defaultSource: string;
  private closed = false;

  protected constructor(context: EngineRecordStoreContext) {
    this.standards = context.standards;
    this.standardIds = context.standardIds;
    this.syncEnvelopePersistence = context.syncEnvelopePersistence ?? null;
    this.flushOnWrite = context.options.flushOnWrite ?? Boolean(context.options.persistenceKey);
    this.hashHex = context.options.hashHex ?? sha256Hex;
    this.nowMs = context.options.nowMs ?? (() => Date.now());
    this.defaultSource = context.options.defaultSource?.trim() || LOCAL_FLATSQL_DEFAULT_SOURCE;
  }

  static async open(
    this: typeof FlatSQLEngineRecordStore,
    options: FlatSQLEngineRecordStoreOptions = {},
  ): Promise<FlatSQLEngineRecordStore> {
    const schemas = options.schemas ?? [];
    const standards = await createLocalFlatSqlStore({
      schemas, engine: options.engine, persistenceKey: options.persistenceKey,
      desktopPersistenceBaseUrl: options.desktopPersistenceBaseUrl, fetch: options.fetch,
      persistenceStore: options.persistenceStore, queryProfiles: options.queryProfiles, nowSeconds: options.nowSeconds,
    });
    const standardIds = new Map<string, RegisteredStandard>();
    for (const schema of schemas) {
      const standardId = standardIdFromSchemaName(schema.standardId);
      standardIds.set(standardId, { standardId, fileId: schema.fileId, tableName: schema.tableName });
    }
    const backing = options.persistenceStore ?? createDefaultLocalFlatSqlPersistenceStore(options);
    const syncEnvelopePersistence = options.persistenceKey && backing.available
      ? { store: backing, key: `${options.persistenceKey}:sync-envelopes` } : null;
    const store = new this({ standards, standardIds, options, syncEnvelopePersistence });
    try { await store.replaySyncEnvelopes(); return store; }
    catch (error) { await standards.destroy(); throw error; }
  }

  get standardsStore(): LocalFlatSqlStore | null {
    return this.standards;
  }

  async queryEpochRawStream(standardId: string, request?: EngineEpochQueryRequest | null): Promise<Uint8Array> {
    const standards = this.requireStandards();
    if (!standards.queryEpochRawStream) {
      throw new Error('The configured standards store does not expose engine epoch raw-stream queries');
    }
    return standards.queryEpochRawStream(standardId, request ?? null);
  }

  async queryEpochFrames(
    standardId: string,
    request?: EngineEpochQueryRequest | null,
  ): Promise<Generator<Uint8Array, void, undefined>> {
    return iterateFlatSqlSizePrefixedStream(await this.queryEpochRawStream(standardId, request));
  }

  async ingestFlatBufferStream(
    standardId: string,
    streamBytes: Uint8Array,
    options?: Parameters<LocalFlatSqlStore['ingestFlatBufferStream']>[2],
  ): Promise<number> {
    return this.requireStandards().ingestFlatBufferStream(standardId, streamBytes, options);
  }

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
      ...(shardCid ? { recordKeyPrefix: `shard:${shardCid}` } : {}),
      ...(options.pinLedgerEntries?.length ? { pinLedgerEntries: options.pinLedgerEntries } : {}),
    });
    if (ingested === 0 && options.pinLedgerEntries?.length) {
      await standards.recordPinLedgerEntries(options.pinLedgerEntries, { persist: false });
    }
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

  async readStream(standardId: string, options: EngineStreamReadOptions = {}): Promise<Uint8Array> {
    const standards = this.requireStandards();
    const standard = this.requireRegisteredStandard(standardId);
    const source = options.source?.trim() || null;
    if (source) {
      const sources = await standards.listSources?.(standard.standardId);
      if (Array.isArray(sources) && !sources.includes(source)) return new Uint8Array(0);
      return this.queryRawFlatBufferStream(
        standard.standardId,
        `SELECT _data FROM ${quote(standard.tableName + '@' + source)} ORDER BY _rowid ASC`,
      );
    }
    return this.queryRawFlatBufferStream(
      standard.standardId,
      `SELECT _data FROM ${quote(standard.tableName)} ORDER BY _rowid ASC`,
    );
  }

  registeredStandards(): Array<{ standardId: string; fileId: string; tableName: string }> {
    return Array.from(this.standardIds.values()).map((entry) => ({ ...entry }));
  }

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
      return await this.ingestAnonymousSyncStream(standardId, schemaName, frames, options);
    }
    if (frames.length < refs.length) {
      throw new Error(`FlatSQL sync chunk delivered ${frames.length}/${refs.length} record frames`);
    }
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
    const timestampMs = this.nowMs();
    for (const ref of refs) {
      if (!ref.cid) continue;
      if (this.upsertSyncEnvelope({
        schema: schemaName,
        cid: ref.cid,
        peerId: ref.peerId || ref.producerPeerId || '',
        timestampMs,
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
      snapshotId: options.segment?.feedHead || options.manifest?.snapshotId || header.feedHead || null,
      head: options.segment?.feedHead || header.feedHead || null,
      highWaterMark: null,
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
      recordKeyPrefix: `shard:${cid}`,
      pinLedgerEntries: [ledgerEntry],
    });
    if (ingestedRecords === 0) {
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

  private upsertSyncEnvelope(row: SyncEnvelopeRow): boolean {
    const key = `${row.schema}|${row.cid}`;
    const isNew = !this.syncEnvelopes.has(key);
    this.syncEnvelopes.set(key, row);
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
    await this.syncEnvelopePersistence.store.writeJson(
      this.syncEnvelopePersistence.key,
      Array.from(this.syncEnvelopes.values()),
    );
  }

  async queryRawFlatBufferStream(standardId: string, sql: string, params?: EngineQueryParam[]): Promise<Uint8Array> {
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

  private async select(standard: RegisteredStandard, where: string, parameters: EngineQueryParam[] = [], limit = 100) {
    return this.requireStandards().query(
      `SELECT _cid,_peer,_ts,_data,_signature,_seq FROM ${quote(standard.tableName)} ${where}`,
      standard.standardId, { parameters, defaultLimit: limit, maxLimit: limit, maxBytes: 16 * 1024 * 1024 },
    );
  }
  private record(standard: RegisteredStandard, row: unknown[]): StoredRecord {
    return { schema: `${standard.standardId}.fbs`, cid: String(row[0]), peerId: String(row[1] ?? ''),
      timestamp: Number(row[2]) * 1000, data: row[3] as Uint8Array,
      signature: row[4] instanceof Uint8Array ? row[4] : new Uint8Array() };
  }
  async store(schema: SchemaName | string, data: Uint8Array, peerId: string, signature: Uint8Array): Promise<string> {
    this.refuseMultiFrameStream(data);
    const standard = this.requireRegisteredStandard(String(schema));
    const payload = stripSdnFlatBufferSizePrefix(data);
    if (!flatBufferMatchesFileId(payload, standard.fileId)) throw new Error('Record file identifier does not match schema');
    await this.requireStandards().ingestRecords(standard.standardId, [{ schemaName: `${standard.standardId}.fbs`, cid: '',
      peerId, timestamp: new Date(this.nowMs()).toISOString(), dataBytes: payload, signature }], { source: this.defaultSource });
    const result = await this.select(standard, 'WHERE _data = ?1', [payload], 1);
    if (result.rows.length !== 1) throw new Error('Stored record is not queryable');
    if (this.flushOnWrite) await this.flush();
    return String(result.rows[0][0]);
  }
  async get(schema: SchemaName | string, cid: string): Promise<StoredRecord | null> {
    const standard = this.requireRegisteredStandard(String(schema));
    const result = await this.select(standard, 'WHERE _cid = ?1', [cid], 1);
    return result.rows.length ? this.record(standard, result.rows[0]) : null;
  }
  async query(schema: SchemaName | string, filter?: QueryFilter): Promise<StoredRecord[]> {
    const standard = this.requireRegisteredStandard(String(schema));
    const out: StoredRecord[] = [], params: EngineQueryParam[] = [];
    const clauses: string[] = [];
    if (filter?.peerId) { params.push(filter.peerId); clauses.push(`_peer = ?${params.length}`); }
    if (filter?.since) { params.push(Math.floor(filter.since.getTime() / 1000)); clauses.push(`_ts >= ?${params.length}`); }
    const maximum = filter?.limit && filter.limit > 0 ? Math.floor(filter.limit) : Infinity;
    let offset = 0;
    while (out.length < maximum) {
      const take = Math.min(64, maximum - out.length);
      const result = await this.select(standard,
        `${clauses.length ? 'WHERE ' + clauses.join(' AND ') : ''} ORDER BY _ts DESC,_seq DESC LIMIT ${take} OFFSET ${offset}`, params, take);
      out.push(...result.rows.map(row => this.record(standard, row))); offset += result.rows.length;
      if (result.rows.length < take) break;
    }
    return out;
  }
  async sql(query: string): Promise<{ columns: string[]; rows: unknown[][]; rowCount: number }> {
    const result = await this.requireStandards().query(query);
    return { columns: result.columns, rows: result.rows, rowCount: result.rows.length };
  }
  async delete(cid: string): Promise<void> {
    for (const standard of this.standardIds.values()) {
      const result = await this.select(standard, 'WHERE _cid = ?1', [cid], 1);
      if (result.rows.length) await this.requireStandards().removeRecords(standard.standardId, [result.rows[0][3] as Uint8Array]);
    }
    for (const [key, row] of this.syncEnvelopes) if (row.cid === cid) this.syncEnvelopes.delete(key);
    if (this.flushOnWrite) await this.flush();
  }
  async count(schema?: SchemaName | string): Promise<number> {
    const selected = schema === undefined ? [...this.standardIds.values()] : [this.requireRegisteredStandard(String(schema))];
    let count = 0;
    for (const standard of selected) {
      const result = await this.requireStandards().query(`SELECT count(*) FROM ${quote(standard.tableName)}`, standard.standardId);
      count += Number(result.rows[0][0]);
    }
    return count;
  }
  async listRecords(): Promise<StoredRecord[]> {
    const rows: StoredRecord[] = [];
    for (const standard of this.standardIds.values()) rows.push(...await this.query(standard.standardId));
    return rows;
  }
  async flush(): Promise<void> { await this.requireStandards().flush(); await this.persistSyncEnvelopes(); }
  async close(): Promise<void> {
    if (this.closed) return;
    await this.flush(); await this.requireStandards().destroy(); this.closed = true;
  }
}
const quote = (name: string): string => '"' + name.replace(/"/g, '""') + '"';

interface SyncEnvelopeRow {
  schema: string;
  cid: string;
  peerId: string;
  timestampMs: number;
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
    schemaName: ref.schemaName && ref.schemaName !== 'unknown' ? ref.schemaName : schemaName,
    cid: ref.cid,
    peerId: ref.peerId || null,
    providerId: ref.providerId ?? null,
    sourceName: ref.sourceName ?? null,
    batchId: ref.batchId ?? null,
    timestamp: null,
    dataBytes: frame,
  };
}

function countFlatSqlStreamFrames(streamBytes: Uint8Array): number {
  let count = 0;
  for (const frame of iterateFlatSqlSizePrefixedStream(streamBytes)) {
    void frame;
    count += 1;
  }
  return count;
}

export function flatBufferMatchesFileId(data: Uint8Array, fileId: string): boolean {
  if (fileId.length !== 4) return false;
  if (hasFileIdAt(data, 4, fileId)) return true;
  return isSingleSizePrefixedBuffer(data) && hasFileIdAt(data, 8, fileId);
}

function isSingleSizePrefixedBuffer(data: Uint8Array): boolean {
  if (data.byteLength < 12) return false;
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  return view.getUint32(0, true) + 4 === data.byteLength;
}

export function flatBufferStreamMatchesFileId(data: Uint8Array, fileId: string): boolean {
  return countFlatBufferStreamFramesWithFileId(data, fileId) > 0;
}

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
