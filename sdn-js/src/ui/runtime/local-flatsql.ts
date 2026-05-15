import type { FlatSQLDatabase } from 'flatsql/wasm';

import { validateReadOnlySql, type ReadOnlySqlValidationOptions } from './read-only-sql-sandbox';
import type { RawDataRecord } from './sdn-backend';

export interface LocalFlatSqlSchema {
  standardId: string;
  tableName: string;
  fileId: string;
  schema: string;
}

export interface LocalFlatSqlStoreOptions {
  schemas: LocalFlatSqlSchema[];
  persistenceKey?: string | null;
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
}

export interface ClearLocalFlatSqlStoreOptions {
  persistenceKey: string;
  standardIds: string[];
  desktopPersistenceBaseUrl?: string | null;
  fetch?: FetchLike | null;
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

export interface FlatSqlSizePrefixedStreamInfo {
  totalRecordCount: number;
  ingestRecordCount: number;
  ingestStartOffset: number;
  allFramesHaveDirectFileIdentifier: boolean;
}

export interface LocalFlatSqlStore {
  ingestRecords(standardId: string, records: RawDataRecord[], sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): Promise<number>;
  ingestFlatBufferStream(standardId: string, streamBytes: Uint8Array, options?: LocalFlatSqlStreamIngestOptions | null): Promise<number>;
  flush(standardId?: string): Promise<void>;
  recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[]): Promise<void>;
  listPinLedgerEntries(query?: LocalFlatSqlPinLedgerQuery): Promise<LocalFlatSqlPinLedgerEntry[]>;
  query(sql: string, standardId?: string, options?: LocalFlatSqlQueryOptions): LocalFlatSqlQueryResult | Promise<LocalFlatSqlQueryResult>;
  getStats(options?: LocalFlatSqlStatsOptions): LocalFlatSqlStandardStats[] | Promise<LocalFlatSqlStandardStats[]>;
  destroy(): void;
}

interface StandardDatabaseState {
  schema: LocalFlatSqlSchema;
  db: FlatSQLDatabase;
  ingestedKeys: Set<string>;
  cachedBytes: number;
  dirty: boolean;
}

type FetchLike = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;

interface LocalFlatSqlPersistenceStore {
  readonly available: boolean;
  readBytes(key: string): Promise<Uint8Array | null>;
  writeBytes(key: string, bytes: Uint8Array): Promise<void>;
  readJson(key: string): Promise<unknown>;
  writeJson(key: string, value: unknown): Promise<void>;
  deleteKey(key: string): Promise<void>;
}

const LOCAL_FLATSQL_DB_NAME = 'sdn-local-flatsql';
const LOCAL_FLATSQL_STORE_NAME = 'datastores';

export async function createLocalFlatSqlStore(options: LocalFlatSqlStoreOptions): Promise<LocalFlatSqlStore> {
  const { initFlatSQL } = await import('flatsql/wasm');
  const flatsql = await initFlatSQL({ skipIntegrityCheck: true });
  const states = new Map<string, StandardDatabaseState>();
  const persistenceStore = createLocalFlatSqlPersistenceStore(options);

  for (const schema of options.schemas) {
    const standardId = normalizeStandardId(schema.standardId);
    const db = flatsql.createDatabase(stripFlatBufferComments(schema.schema), `sdn-${standardId.toLowerCase()}`);
    db.registerFileId(schema.fileId, schema.tableName);
    const persisted = options.persistenceKey
      ? await readPersistedFlatSqlBytes(persistenceStore, persistedStandardKey(options.persistenceKey, standardId))
      : null;
    if (persisted && persisted.byteLength > 0) {
      db.loadAndRebuild(persisted);
    }
    const ingestedKeys = options.persistenceKey
      ? await readPersistedRecordKeys(persistenceStore, persistedRecordKey(options.persistenceKey, standardId))
      : new Set<string>();
    const persistedRecordCount = Number(db.getStats().find((entry) => entry.tableName === schema.tableName)?.recordCount ?? 0);
    const repairedIngestedKeys = ingestedKeys.size > persistedRecordCount
      ? new Set<string>()
      : ingestedKeys;
    states.set(standardId, {
      schema: { ...schema, standardId },
      db,
      ingestedKeys: repairedIngestedKeys,
      cachedBytes: persisted?.byteLength ?? 0,
      dirty: repairedIngestedKeys !== ingestedKeys,
    });
  }

  const pinLedger = options.persistenceKey
    ? await readPersistedPinLedgerEntries(persistenceStore, persistedPinLedgerKey(options.persistenceKey))
    : new Map<string, LocalFlatSqlPinLedgerEntry>();
  return new WasmLocalFlatSqlStore(states, options.persistenceKey ?? null, pinLedger, persistenceStore);
}

export async function clearLocalFlatSqlStore(options: ClearLocalFlatSqlStoreOptions): Promise<void> {
  const persistenceKey = options.persistenceKey.trim();
  const standardIds = Array.from(new Set(options.standardIds.map(normalizeStandardId)));
  const persistenceStore = createLocalFlatSqlPersistenceStore(options);
  if (!persistenceKey || standardIds.length === 0 || !persistenceStore.available) return;
  for (const standardId of standardIds) {
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
  ) {}

  async ingestRecords(
    standardId: string,
    records: RawDataRecord[],
    sourceOrOptions?: string | LocalFlatSqlIngestOptions | null,
  ): Promise<number> {
    const options = normalizeIngestOptions(sourceOrOptions);
    const state = this.stateForStandard(standardId);
    const nextRecords = records.filter((record) => !state.ingestedKeys.has(recordIngestKey(record)));
    const recordsWithBytes = nextRecords.filter((record) => record.dataBytes instanceof Uint8Array);
    const buffers = recordsWithBytes.map((record) => stripSdnFlatBufferSizePrefix(record.dataBytes as Uint8Array));
    if (buffers.length === 0) return 0;

    const beforeRecordCount = recordCountForState(state);
    state.db.ingestBuffers(buffers);
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
      const beforeRecordCount = recordCountForState(state);
      state.db.ingest(directStreamBytes);
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
      : candidates.filter((entry) => !state.ingestedKeys.has(entry.key));
    if (nextRecords.length === 0) return 0;

    const beforeRecordCount = recordCountForState(state);
    state.db.ingestBuffers(nextRecords.map((entry) => stripSdnFlatBufferSizePrefix(entry.buffer)));
    const ingestedCount = Math.max(0, recordCountForState(state) - beforeRecordCount);
    for (const entry of nextRecords.slice(0, ingestedCount)) state.ingestedKeys.add(entry.key);
    state.dirty = true;
    if (ingestedCount > 0 && options?.pinLedgerEntries?.length) {
      await this.recordPinLedgerEntries(options.pinLedgerEntries);
    }
    if (options?.persist !== false) await this.persistStandard(state);
    return ingestedCount;
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

  async recordPinLedgerEntries(entries: LocalFlatSqlPinLedgerEntry[]): Promise<void> {
    for (const candidate of entries) {
      const entry = normalizePinLedgerEntry(candidate);
      if (!entry.cid || !entry.standardId || !entry.schemaName || !entry.role || !entry.verificationState) continue;
      this.pinLedger.set(pinLedgerEntryKey(entry), entry);
    }
    await this.persistPinLedger();
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
      const dbStats = state.db.getStats().find((entry) => entry.tableName === state.schema.tableName);
      const pinStats = this.pinStatsForStandard(state.schema.standardId);
      return {
        standardId: state.schema.standardId,
        tableName: state.schema.tableName,
        recordCount: Number(dbStats?.recordCount ?? 0),
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

  private async persistStandard(state: StandardDatabaseState): Promise<void> {
    const bytes = state.db.exportData();
    state.cachedBytes = bytes.byteLength;
    state.dirty = false;
    if (!this.persistenceKey) return;
    await writePersistedFlatSqlBytes(this.persistenceStore, persistedStandardKey(this.persistenceKey, state.schema.standardId), bytes);
    await writePersistedRecordKeys(this.persistenceStore, persistedRecordKey(this.persistenceKey, state.schema.standardId), state.ingestedKeys);
  }

  private async persistPinLedger(): Promise<void> {
    if (!this.persistenceKey) return;
    await writePersistedPinLedgerEntries(this.persistenceStore, persistedPinLedgerKey(this.persistenceKey), Array.from(this.pinLedger.values()));
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
  return Number(state.db.getStats().find((entry) => entry.tableName === state.schema.tableName)?.recordCount ?? 0);
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

function recordIngestKey(record: RawDataRecord): string {
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

function createLocalFlatSqlPersistenceStore(options: Pick<LocalFlatSqlStoreOptions, 'desktopPersistenceBaseUrl' | 'fetch'>): LocalFlatSqlPersistenceStore {
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
