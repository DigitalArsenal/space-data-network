/**
 * FlatSQLStorage (loop D.1) — thin delegate over the FlatSQL-WASM engine
 * record store.
 *
 * Pre-D.1 this class was a pure-JS row index (space-data-module-sdk runtime
 * store) with a JSON snapshot codec — NOT the WASM engine. The engine store
 * (src/engine-record-store.ts) is now THE SDNNode store; this class remains
 * only as a compatibility alias because it has been part of the published
 * package surface (index.ts) since WS6.5. Public behavior is preserved:
 * store/get/query/delete/count/listRecords/flush/close, content-addressed
 * dedupe, and the SnapshotPersistence journal (same codec, so existing
 * Memory/Helia/IndexedDB snapshots keep loading). The `sql()` escape hatch
 * now queries the engine's `sdn_record_index` control table (server-mirrored
 * layout) instead of the retired SDK `RuntimeHostRow` table.
 */

import {
  FlatSQLEngineRecordStore,
  type FlatSQLEngineRecordStoreOptions,
  type SnapshotPersistence,
} from './engine-record-store';

export type {
  SnapshotPersistence,
  HeliaLike,
} from './engine-record-store';
export {
  MemorySnapshotPersistence,
  HeliaSnapshotPersistence,
} from './engine-record-store';

/**
 * Open options for `FlatSQLStorage`.
 *
 * Until 2.0.18 this interface declared only `persistence`, `flushOnWrite`,
 * `hashHex` and `nowMs`, while `open()` forwarded everything to the engine
 * store — so `persistenceKey` and `schemas`, the two options that DECIDE
 * whether the disk-backed per-source lane exists at all, were accepted at
 * runtime and invisible to the compiler. Callers in other repos passed
 * `persistenceKey` alone, got no lane and no error, and cached whole-blob.
 * The interface now declares the full engine surface, so the option that
 * decides durability is the one the compiler can see.
 */
export interface FlatSQLStorageOptions extends FlatSQLEngineRecordStoreOptions {
  persistence?: SnapshotPersistence;
  /** Persist a snapshot after every mutation (default true when persistence given). */
  flushOnWrite?: boolean;
  hashHex?: (data: Uint8Array) => Promise<string>;
  nowMs?: () => number;
}

/**
 * Compatibility alias over the engine record store, kept because it has been
 * part of the published surface since WS6.5. It inherits the whole store,
 * including the 2.0.18 stream surface (`storeStream` / `readStream`) — the
 * shard-shaped admit point a browser catalogue cache wants, as opposed to
 * `store()`, which admits ONE record plus its provenance envelope.
 */
export class FlatSQLStorage extends FlatSQLEngineRecordStore {
  static async open(options: FlatSQLStorageOptions = {}): Promise<FlatSQLStorage> {
    return await super.open(options) as FlatSQLStorage;
  }
}
