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

export interface FlatSQLStorageOptions {
  persistence?: SnapshotPersistence;
  /** Persist a snapshot after every mutation (default true when persistence given). */
  flushOnWrite?: boolean;
  hashHex?: (data: Uint8Array) => Promise<string>;
  nowMs?: () => number;
}

export class FlatSQLStorage extends FlatSQLEngineRecordStore {
  static async open(options: FlatSQLStorageOptions = {}): Promise<FlatSQLStorage> {
    return await super.open(options) as FlatSQLStorage;
  }
}
