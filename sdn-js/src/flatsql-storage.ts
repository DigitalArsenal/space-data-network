/**
 * FlatSQL store-of-record for the browser node (WS6.5).
 *
 * Replaces the IndexedDB SDNStorage as the node's record store: records live
 * in the space-data-module-sdk FlatSQL runtime store (pure-JS FlatSQL rows
 * keyed (schema, rowId) with the full record envelope as the row payload),
 * and durability comes from snapshots persisted through a pluggable
 * SnapshotPersistence — on Helia the snapshot bytes are added as a unixfs
 * block and the root CID is remembered in a small ref store (localStorage),
 * making Helia the store of record across page reloads.
 *
 * The public surface mirrors SDNStorage (store/get/query/delete/count/close)
 * and satisfies the ModuleHostRecordStore contract, so it drops into
 * SDNNode.init and createModuleHostCapabilityAdapters unchanged.
 */

import { createFlatSqlRuntimeStore } from 'space-data-module-sdk/runtime-host';
import { sha256 } from './crypto/hd-wallet';
import type { SchemaName } from './schemas';
import type { StoredRecord, QueryFilter } from './storage';

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

export interface FlatSQLStorageOptions {
  persistence?: SnapshotPersistence;
  /** Persist a snapshot after every mutation (default true when persistence given). */
  flushOnWrite?: boolean;
  hashHex?: (data: Uint8Array) => Promise<string>;
  nowMs?: () => number;
}

interface RowRef {
  record: StoredRecord;
}

export class FlatSQLStorage {
  private readonly rows = createFlatSqlRuntimeStore();
  private readonly byCid = new Map<string, RowRef>();
  private readonly persistence?: SnapshotPersistence;
  private readonly flushOnWrite: boolean;
  private readonly hashHex: (data: Uint8Array) => Promise<string>;
  private readonly nowMs: () => number;

  private constructor(options: FlatSQLStorageOptions) {
    this.persistence = options.persistence;
    this.flushOnWrite = options.flushOnWrite ?? Boolean(options.persistence);
    this.hashHex = options.hashHex ?? sha256Hex;
    this.nowMs = options.nowMs ?? (() => Date.now());
  }

  static async open(options: FlatSQLStorageOptions = {}): Promise<FlatSQLStorage> {
    const storage = new FlatSQLStorage(options);
    if (options.persistence) {
      const snapshot = await options.persistence.load();
      if (snapshot && snapshot.length > 0) {
        for (const record of decodeSnapshot(snapshot)) {
          storage.append(record);
        }
      }
    }
    return storage;
  }

  private append(record: StoredRecord): void {
    this.rows.appendRow({ schemaFileId: record.schema, payload: record });
    this.byCid.set(record.cid, { record });
  }

  async store(
    schema: SchemaName | string,
    data: Uint8Array,
    peerId: string,
    signature: Uint8Array,
  ): Promise<string> {
    const cid = await this.hashHex(data);
    if (this.byCid.has(cid)) return cid; // content-addressed dedupe
    this.append({
      cid,
      schema: String(schema),
      peerId,
      timestamp: this.nowMs(),
      data: new Uint8Array(data),
      signature: new Uint8Array(signature ?? new Uint8Array(0)),
    });
    if (this.flushOnWrite) await this.flush();
    return cid;
  }

  async get(schema: SchemaName | string, cid: string): Promise<StoredRecord | null> {
    const entry = this.byCid.get(cid);
    if (!entry || entry.record.schema !== String(schema)) return null;
    return { ...entry.record };
  }

  async query(
    schema: SchemaName | string,
    filter?: QueryFilter,
  ): Promise<StoredRecord[]> {
    // Row identity lives in FlatSQL; envelopes ride along as row payloads.
    let records = this.rows
      .listRows(String(schema))
      .map((row: { payload: unknown }) => row.payload as StoredRecord)
      .filter(Boolean)
      .filter((record: StoredRecord) => this.byCid.has(record.cid));
    if (filter?.peerId) {
      records = records.filter((r: StoredRecord) => r.peerId === filter.peerId);
    }
    if (filter?.since) {
      const sinceMs = filter.since.getTime();
      records = records.filter((r: StoredRecord) => r.timestamp >= sinceMs);
    }
    records.sort((a: StoredRecord, b: StoredRecord) => b.timestamp - a.timestamp);
    if (filter?.limit && records.length > filter.limit) {
      records = records.slice(0, filter.limit);
    }
    return records.map((r: StoredRecord) => ({ ...r }));
  }

  /** Raw SQL over the FlatSQL row table (schemaFileId/rowId provenance). */
  sql(query: string): { columns: string[]; rows: unknown[][]; rowCount: number } {
    return this.rows.query(query);
  }

  async delete(cid: string): Promise<void> {
    // FlatSQL rows are append-only; deletion is a tombstone on the cid index
    // (excluded from queries and from the next snapshot).
    this.byCid.delete(cid);
    if (this.flushOnWrite) await this.flush();
  }

  async count(schema?: SchemaName | string): Promise<number> {
    if (schema !== undefined) {
      return (await this.query(schema)).length;
    }
    return this.byCid.size;
  }

  listRecords(): StoredRecord[] {
    return [...this.byCid.values()].map((entry) => ({ ...entry.record }));
  }

  async flush(): Promise<void> {
    if (!this.persistence) return;
    await this.persistence.save(encodeSnapshot(this.listRecords()));
  }

  async close(): Promise<void> {
    await this.flush();
  }
}
