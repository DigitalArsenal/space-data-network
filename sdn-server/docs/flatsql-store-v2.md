# FlatSQL Store v2 — design (loop B.1)

Status: accepted design for replacing `internal/storage`'s go-sqlite3 layer
with the in-process FlatSQL-WASM engine (`internal/flatsqlrt`). Companion
prototype: `internal/flatsqlrt/storev2_prototype_test.go`. Authority:
`ARCHITECTURE_FLATSQL_FIRST.md` (superproject) + loop doc GROUND TRUTH.

## 1. Shape

One `flatsqlrt.Runtime` (AOT-cached) per datastore, holding ONE FlatSQL
database whose schema string concatenates:

- every SDS record table the node serves (OMM, CAT, MPE, SPW, EPM, PNM, …),
  each `flatsql_register_file_id`-routed by its 4-byte identifier, and
  partitioned per provider/source via `flatsql_register_source`
  (`OMM@celestrak-gp`, …) with unified `UNION ALL` views (`_source` column
  carries the shadow-table name);
- the **control tables** (plain SQLite tables created through
  `flatsql_query` DDL — proven in `TestControlTableDDLThroughEngine`):
  `sdn_record_index`, `sdn_record_source_tags`, `sdn_record_source_summary`,
  `sdn_metadata`, `sdn_directory`, `sdn_local_epms`, `sdn_log_index`, and
  the publication-bookkeeping tables.

One SQLite context for everything means cursor queries can join control
rows against record vtabs, exactly like today's `sdn.db`.

Column extraction for query predicates (epoch, norad, entity) comes from the
record vtabs natively — `sdn_record_index` keeps only what the sync/cursor
and point-read paths need: `(rowid, schema_name, cid, table_ref, engine_seq,
stream_path, stream_offset, record_length, peer_id, timestamp,
signature_hex)`.

## 2. Durability (per A.4 decisions)

- **Payloads**: the existing append-only `flatsql-streams/<table>.flatsql`
  files, byte-unchanged (size-prefixed FlatBuffer frames).
- **Metadata**: a new append-only **journal** per datastore
  (`flatsql-streams/journal.sdnj`): one compact frame per ingested record
  carrying `(seq u64, schema_id, cid, stream_path_ref, stream_offset u64,
  record_length u32, peer_id, timestamp, source-tag tuple, signature)` —
  i.e. today's `sdn_record_index` + `sdn_record_source_tags` rows as a flat
  log. CRC per frame; torn tails truncated at boot.
- **Boot**: replay the journal in `seq` order; for each frame, feed the
  payload bytes (read from the stream file) to
  `flatsql_ingest_one_with_source` and `INSERT` the control row **with the
  explicit rowid = seq**. MEASURED (A.4): 493K records rebuild in ~145 ms;
  ~1 s at 3M. No snapshots needed.
- The engine is thereafter a pure cache: any crash loses nothing that the
  journal + streams cannot reproduce.

## 3. The datasync cursor (the deployed-peer contract)

Wire cursor stays `(AfterRowID, MaxRowID, SnapshotID)`
(`datasync.EncodeRawRecordCursor`), and the store keeps satisfying
`RawRecordQuery{UseRowIDCursor, AfterRowID, MaxRowID}` + `RawRecordHead`:

- **rowid ≡ journal seq**: a strictly-monotonic uint64 assigned at ingest,
  durable in the journal, reproduced identically at every boot via explicit
  `INSERT ... rowid = seq`. Paging = `WHERE rowid > :after AND rowid <=
  :max ORDER BY rowid LIMIT :n` over `sdn_record_index`; head =
  `MAX(rowid)`.
- **GC / hot-window eviction never renumbers**: evicting old epochs from
  the engine removes control rows + vtab records but seqs are never reused;
  stream compaction rewrites offsets in a new journal generation while
  preserving seqs. A cursor pointing at evicted rows simply pages past them
  (same behavior as today's GC-resilient stream cache).
- **Migration (B.7)**: the legacy importer replays `sdn.db` in
  `sdn_record_index.rowid` order assigning `seq = legacy rowid`, so
  deployed peers' cursors remain valid byte-for-byte. Where that ever
  fails, `SnapshotID` mismatch already forces a clean resync.

## 4. Write path

```
StoreWithSourceTags(schema, data, peer, sig, tags):
  1. append size-prefixed data → stream file        (durable payload)
  2. seq = nextSeq++; append journal frame          (durable metadata)
  3. engineSeq = IngestOneWithSource(data, source)  (vtab row, indexed)
  4. INSERT control rows (rowid = seq, engine_seq = engineSeq, tags…)
```
Steps 1–2 are the commit point; 3–4 are engine-cache updates (recoverable
by replay). Batch variants amortize one lock acquisition. The vtab
`_rowid` equals the value returned by `flatsql_ingest_one*` (proven in the
prototype), so payload fetch by control row is
`SELECT _data FROM "<Table>@<source>" WHERE _rowid = :engine_seq` — or, for
point reads that don't need the engine at all, a direct `ReadAt` on the
stream file (today's hydrate path, unchanged).

## 5. Query paths

- Cursor/sync reads: control-table SQL (above) → refs; payload frames read
  from stream files (`WriteRawRecordFrames` unchanged).
- Epoch profiles (B.3): registered query templates over the bounded
  per-provider vtabs (nearest/as_of/forward/day/window/coverage), raw
  aligned streams out via `flatsql_query_raw_flatbuffer_stream`.
- Point read `Get(schema,cid)`: control-table lookup → stream `ReadAt`.
- `Query*`/`Count*`/`Stats`/`DataSummary`: SQL over control tables + vtabs.

## 6. Retention (per A.4 capacity ceiling — ENFORCED in loop C.4)

Resident hot window bounded per schema (measured raw ceiling ≈1.5M $OMM:
3M ≈ 2.2 GB and the next arena doubling traps; the vtabs share the 4 GiB
engine with the control tables at ~4 control rows/record — B.7 — so the
shipped DEFAULT is 400K records, configurable via
`storage.engine_hot_window` / `storage.WithEngineHotWindow`). Enforcement
(engine_records.go):

- **Boot**: the rebuild loads at most the window (newest records by global
  index rowid).
- **Ingest**: after mirroring a committed batch, the oldest resident engine
  records beyond the window are TOMBSTONED (`flatsql_mark_deleted` keyed by
  the unified view's `_source` + `_rowid`) — queries stop returning them
  immediately; arena bytes are reclaimed by the next boot rebuild.

History remains in streams+journal (eviction never touches control rows,
stream files, or datasync cursor rowids — TestEngineHotWindowEviction);
epoch queries beyond the hot window are served by transient engines over
time-partitioned segments (wired after the hot path).

## 7. Migration & compatibility gates

- `internal/storage` public surface preserved (B.2); callers unchanged.
- Wire formats (datasync frames, shard/PNM) byte-stable; parity harness
  (A.2) guards engine result bytes against the browser host.
- Sibling stores: storefront + trust move onto the store API (B.5); auth,
  admin, audit, license, peers migrate in B.6; `mattn/go-sqlite3` deleted
  in B.8.

## 8. Single-writer liveness lock + ingest topology (loop C.6b)

The store is SINGLE-WRITER by construction: one in-process engine owns the
control journal and the stream appenders. Two independent processes on one
basePath (the pre-C.6b celestrak.eth production shape: daemon +
`spacedatanetwork-ingest.service`) would interleave `control.sdnj` frames
and stream appends — journal corruption.

- **Lock**: every `NewFlatSQLStore` takes a non-blocking EXCLUSIVE OS lock
  (`flock` on Unix, `LockFileEx` on Windows) on `<basePath>/store.lock`
  BEFORE touching any store file (`internal/storage/storelock.go`). A
  second writer open fails immediately with `storage.ErrStoreLocked` and an
  actionable message (holder pid/host/time from advisory JSON metadata in
  the lock file).
- **Stale leases**: none exist. The lock is kernel-owned and tied to the
  open file description, so process death — including `kill -9` — releases
  it automatically; the next open succeeds with no takeover protocol. The
  metadata is never used to decide liveness. The lock file is not unlinked
  on release (unlink would race a contender holding the old inode).
- **Ingest topology**: source-sync workers run IN the daemon (config
  `ingest.enabled`, `internal/node/ingest.go` →
  `ingest.NewRunnerWithStore`) against the daemon's own store handle, so
  hot-window enforcement, datasync cursor rowids, and engine-mirror
  generation invalidation apply to ingested records exactly as to datasync
  writes (TestInDaemonIngestSharedStoreEndToEnd). The standalone
  `spacedatanetwork ingest` verb remains for offline stores and fails with
  the lock error against a daemon-held path
  (TestNewRunnerFailsCleanlyWhenDaemonHoldsStore); cross-process lock
  semantics are proven by subprocess tests (storelock_test.go).
