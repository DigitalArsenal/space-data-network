import type { FlatSQLDatabase } from 'flatsql/wasm';

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
}

export interface ClearLocalFlatSqlStoreOptions {
  persistenceKey: string;
  standardIds: string[];
}

export interface LocalFlatSqlQueryResult {
  columns: string[];
  rows: unknown[][];
  records: Array<Record<string, unknown>>;
}

export interface LocalFlatSqlStandardStats {
  standardId: string;
  tableName: string;
  recordCount: number;
  cachedBytes: number;
  ingestedRecordCount: number;
}

export interface LocalFlatSqlIngestOptions {
  source?: string | null;
  persist?: boolean;
  transfer?: boolean;
}

export interface LocalFlatSqlStatsOptions {
  includeCachedBytes?: boolean;
}

export interface LocalFlatSqlStore {
  ingestRecords(standardId: string, records: RawDataRecord[], sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): Promise<number>;
  flush(standardId?: string): Promise<void>;
  query(sql: string, standardId?: string): LocalFlatSqlQueryResult | Promise<LocalFlatSqlQueryResult>;
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

const LOCAL_FLATSQL_DB_NAME = 'sdn-local-flatsql';
const LOCAL_FLATSQL_STORE_NAME = 'datastores';

export async function createLocalFlatSqlStore(options: LocalFlatSqlStoreOptions): Promise<LocalFlatSqlStore> {
  const { initFlatSQL } = await import('flatsql/wasm');
  const flatsql = await initFlatSQL({ skipIntegrityCheck: true });
  const states = new Map<string, StandardDatabaseState>();

  for (const schema of options.schemas) {
    const standardId = normalizeStandardId(schema.standardId);
    const db = flatsql.createDatabase(stripFlatBufferComments(schema.schema), `sdn-${standardId.toLowerCase()}`);
    db.registerFileId(schema.fileId, schema.tableName);
    const persisted = options.persistenceKey
      ? await readPersistedFlatSqlBytes(persistedStandardKey(options.persistenceKey, standardId))
      : null;
    if (persisted && persisted.byteLength > 0) {
      db.loadAndRebuild(persisted);
    }
    const ingestedKeys = options.persistenceKey
      ? await readPersistedRecordKeys(persistedRecordKey(options.persistenceKey, standardId))
      : new Set<string>();
    states.set(standardId, {
      schema: { ...schema, standardId },
      db,
      ingestedKeys,
      cachedBytes: persisted?.byteLength ?? 0,
      dirty: false,
    });
  }

  return new WasmLocalFlatSqlStore(states, options.persistenceKey ?? null);
}

export async function clearLocalFlatSqlStore(options: ClearLocalFlatSqlStoreOptions): Promise<void> {
  const persistenceKey = options.persistenceKey.trim();
  const standardIds = Array.from(new Set(options.standardIds.map(normalizeStandardId)));
  if (!persistenceKey || standardIds.length === 0 || !hasIndexedDb()) return;
  const db = await openLocalFlatSqlDb();
  await new Promise<void>((resolve) => {
    const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readwrite');
    const store = transaction.objectStore(LOCAL_FLATSQL_STORE_NAME);
    for (const standardId of standardIds) {
      store.delete(persistedStandardKey(persistenceKey, standardId));
      store.delete(persistedRecordKey(persistenceKey, standardId));
    }
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

    const count = state.db.ingestBuffers(buffers);
    for (const record of recordsWithBytes) state.ingestedKeys.add(recordIngestKey(record));
    state.dirty = true;
    if (options.persist !== false) await this.persistStandard(state);
    return count;
  }

  async flush(standardId?: string): Promise<void> {
    if (standardId) {
      await this.persistStandard(this.stateForStandard(standardId));
      return;
    }
    for (const state of this.states.values()) {
      await this.persistStandard(state);
    }
  }

  query(sql: string, standardId?: string): LocalFlatSqlQueryResult {
    if (!isReadOnlyFlatSqlQuery(sql)) {
      throw new Error('FlatSQL local queries must be read-only SELECT or WITH SELECT statements');
    }
    const state = standardId ? this.stateForStandard(standardId) : this.firstState();
    const result = state.db.query(sql);
    return {
      columns: result.columns,
      rows: result.rows,
      records: result.rows.map((row) => rowToRecord(result.columns, row)),
    };
  }

  getStats(options: LocalFlatSqlStatsOptions = {}): LocalFlatSqlStandardStats[] {
    return Array.from(this.states.values()).map((state) => {
      const dbStats = state.db.getStats().find((entry) => entry.tableName === state.schema.tableName);
      return {
        standardId: state.schema.standardId,
        tableName: state.schema.tableName,
        recordCount: Number(dbStats?.recordCount ?? 0),
        cachedBytes: this.cachedBytesForState(state, options.includeCachedBytes !== false),
        ingestedRecordCount: state.ingestedKeys.size,
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
    await writePersistedFlatSqlBytes(persistedStandardKey(this.persistenceKey, state.schema.standardId), bytes);
    await writePersistedRecordKeys(persistedRecordKey(this.persistenceKey, state.schema.standardId), state.ingestedKeys);
  }

  private cachedBytesForState(state: StandardDatabaseState, includeCachedBytes: boolean): number {
    if (!includeCachedBytes) return state.cachedBytes;
    state.cachedBytes = state.db.exportData().byteLength;
    return state.cachedBytes;
  }
}

export function isReadOnlyFlatSqlQuery(sql: string): boolean {
  const normalized = stripSqlComments(sql).trim();
  if (!normalized) return false;
  if (hasMultipleStatements(normalized)) return false;
  if (!/^(select|with)\b/i.test(normalized)) return false;
  return !/\b(attach|alter|create|delete|detach|drop|insert|pragma|reindex|replace|truncate|update|vacuum)\b/i.test(normalized);
}

function stripSqlComments(sql: string): string {
  return sql
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .split('\n')
    .map((line) => line.replace(/--.*$/, ''))
    .join('\n');
}

function stripFlatBufferComments(schema: string): string {
  return schema
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .split('\n')
    .map((line) => line.replace(/\/\/.*$/, ''))
    .join('\n');
}

function hasMultipleStatements(sql: string): boolean {
  const trimmed = sql.trim();
  if (!trimmed.includes(';')) return false;
  return !/;\s*$/.test(trimmed);
}

function normalizeIngestOptions(sourceOrOptions?: string | LocalFlatSqlIngestOptions | null): LocalFlatSqlIngestOptions {
  if (!sourceOrOptions) return {};
  if (typeof sourceOrOptions === 'string') return { source: sourceOrOptions };
  return sourceOrOptions;
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

async function readPersistedFlatSqlBytes(key: string): Promise<Uint8Array | null> {
  if (!hasIndexedDb()) return null;
  const db = await openLocalFlatSqlDb();
  return new Promise((resolve) => {
    const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readonly');
    const request = transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).get(key);
    request.onerror = () => resolve(null);
    request.onsuccess = () => {
      const value = request.result;
      if (value instanceof ArrayBuffer) {
        resolve(new Uint8Array(value));
        return;
      }
      if (value instanceof Uint8Array) {
        resolve(value);
        return;
      }
      resolve(null);
    };
    transaction.oncomplete = () => db.close();
    transaction.onerror = () => {
      db.close();
      resolve(null);
    };
  });
}

async function writePersistedFlatSqlBytes(key: string, bytes: Uint8Array): Promise<void> {
  if (!hasIndexedDb()) return;
  const db = await openLocalFlatSqlDb();
  await new Promise<void>((resolve) => {
    const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readwrite');
    transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).put(bytes.slice().buffer, key);
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

async function readPersistedRecordKeys(key: string): Promise<Set<string>> {
  if (!hasIndexedDb()) return new Set();
  const db = await openLocalFlatSqlDb();
  return new Promise((resolve) => {
    const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readonly');
    const request = transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).get(key);
    request.onerror = () => resolve(new Set());
    request.onsuccess = () => {
      const value = request.result;
      resolve(new Set(Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string') : []));
    };
    transaction.oncomplete = () => db.close();
    transaction.onerror = () => {
      db.close();
      resolve(new Set());
    };
  });
}

async function writePersistedRecordKeys(key: string, keys: Set<string>): Promise<void> {
  if (!hasIndexedDb()) return;
  const db = await openLocalFlatSqlDb();
  await new Promise<void>((resolve) => {
    const transaction = db.transaction(LOCAL_FLATSQL_STORE_NAME, 'readwrite');
    transaction.objectStore(LOCAL_FLATSQL_STORE_NAME).put(Array.from(keys), key);
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
