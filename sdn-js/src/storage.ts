/**
 * Record-store data shapes shared by the FlatSQL-WASM engine record store
 * (`engine-record-store.ts` — THE SDNNode store) and its consumers.
 *
 * The legacy IndexedDB `SDNStorage` backend that used to live here was
 * removed in loop D.5: the engine store is the only record store, and it
 * already persists durably (IndexedDB source-stream snapshots in browsers,
 * HTTP persistence on desktop, memory for tests).
 */

export interface StoredRecord {
  cid: string;
  schema: string;
  peerId: string;
  timestamp: number;
  data: Uint8Array;
  signature: Uint8Array;
}

export interface QueryFilter {
  peerId?: string;
  since?: Date;
  limit?: number;
}
