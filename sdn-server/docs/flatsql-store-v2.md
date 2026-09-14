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
  (`OMM@provider-primary`, …) with unified `UNION ALL` views (`_source` column
  carries the shadow-table name);
- the **control tables** (plain SQLite tables created through
  `flatsql_query` DDL — proven in `TestControlTableDDLThroughEngine`):
  `sdn_record_index`, `sdn_record_source_tags`, `sdn_record_source_summary`,
  `sdn_metadata`, `sdn_directory`, `sdn_local_epms`, `sdn_log_index`, and
  the publication-bookkeeping tables.

One SQLite context for everything means cursor queries can join control
rows against record vtabs, exactly like today's `sdn.db`.

`sdn_record_index` keeps what the sync/cursor, point-read and catalog-filter
paths need: `(rowid, schema_name, cid, norad_cat_id, entity_id, object_type,
ops_status_code, epoch_unix, epoch_day, source_timestamp)`; the record bytes
and signature live on the producer table row.

## 2. Durability — the control database IS the record store

OWNER LAW 2026-09-02 (sdn-operating-model-streams-flatsql): "the flatbuffers
are streamed to flatsql which persists it directly to disk, and builds the sql
index / metadata which it writes to disk; any node restart just uses whatever
is on disk ... it should NEVER re-ingest".

- **One file.** Every record's bytes (`data BLOB` on its producer table),
  its index row (`sdn_record_index`), its provenance (`sdn_record_source_tags`,
  `sdn_record_source_summary`) and the node's auxiliary tables live in
  `control.flatsqldb`, a SQLite database written by the FlatSQL engine through
  its own VFS (`flatsql_io_*`, journal_mode=TRUNCATE, one writer). A record
  write is one transaction; a crash costs nothing that was committed.
- **Boot = open.** `NewFlatSQLStore` opens the file, replays the tail of the
  auxiliary journal (the node's own EPM, pin ledger, publications, licences —
  `auxiliary.flatsqlmeta`, the one journal the store keeps) and serves. There
  is no record journal, no stream file, no replay and no hydration phase for
  records. A file that does not open is a hard failure, never discarded
  (`errControlDatabaseUnusable`): there is no second copy to rebuild from.
- **Field-encrypted standards** are sealed BEFORE the row is written
  (`storableRecordBytes`) and opened on read; the CID and the index are always
  computed over the plaintext.
- **CAT supersedes on ingest** (`record_supersede.go`): within one producer's
  table a `$CAT` record replaces the producer's previous record for the same
  object — identity `(CATALOG_URI, CATALOG_OBJECT_ID)`, else `NORAD_CAT_ID`,
  else `OBJECT_ID`; no identity, no supersede. Two ingests of the same
  edition leave one row per object. No historical CAT is stored.

## 3. The datasync cursor (the deployed-peer contract)

Wire cursor stays `(AfterRowID, MaxRowID, SnapshotID)`
(`datasync.EncodeRawRecordCursor`), and the store keeps satisfying
`RawRecordQuery{UseRowIDCursor, AfterRowID, MaxRowID}` + `RawRecordHead`:

- **rowid = `sdn_record_index.rowid`**, durable in the control database.
  Rows are only ever inserted (SQLite allocates MAX(rowid)+1; the store never
  VACUUMs), and a repeat CID keeps its rowid, so a record's cursor position
  never moves for the life of the store. Paging = `WHERE rowid > :after AND
  rowid <= :max ORDER BY rowid LIMIT :n`; head = `MAX(rowid)`.
- **GC / supersede / hot-window eviction never renumber**: a deleted record's
  rowid is simply absent; a cursor pointing at it pages past.

## 4. Write path

```
StoreWithSourceTags(schema, data, peer, sig, tags):
  BEGIN
    supersede: delete the producer's previous row for the same $CAT object
    INSERT producer row (cid, peer_id, timestamp, data, record_length, ...)
    INSERT OR UPDATE sdn_record_index row            (the datasync cursor)
    INSERT source tags + increment the source summary
  COMMIT                                             (the commit point)
  tombstone superseded rows in the engine; mirror the new record into the
  engine vtab and record its residency (engine_residency.go)
```
Batch variants amortize one lock acquisition and one transaction per chunk.

## 5. Query paths

- Cursor/sync reads, `Get`, `Query*`, `Count*`, `DataSummary`, exports and
  shard writes: control-table SQL; the record bytes come from the row.
- Epoch profiles and the sandboxed public SQL surface: the engine vtabs
  (§6), which hold a bounded hot window of the routed standards.

## 6. The engine hot window (bounded cache, tracked by a durable ledger)

Resident records per routed schema are bounded (measured raw ceiling ≈1.5M
$OMM; the vtabs share the 4 GiB engine with the control tables, so the
shipped DEFAULT is 400K records, `storage.engine_hot_window`, and 10K for
generically routed standards, `storage.engine_generic_hot_window`). The
window exists because `flatsql_open_state` reads the whole `.fsdata` arena
into linear memory (flatsql-page-fsdata-not-slurp, Part B); it is not the
record store.

- **Residency ledger** `sdn_engine_rows (schema_name, cid, source, seq)`:
  one row per resident record, written in the SAME control transaction as
  the engine ingest (`flatsql_ingest_one_with_source` returns `seq`, which is
  the vtab's `_rowid`). It is what makes a delete or a supersede reach the
  engine at once (`MarkDeleted(<Table>@<source>, seq)`), what the eviction
  drops rows from, and what a warm boot reconciles against.
- **Persistence**: every checkpoint (30 s, and at Close) flushes the engine's
  record arena + index (`flatsql_flush_index`, into `control.flatsqldb.fsdata`
  and the engine's own tables inside the database) and then records
  `flatsql_boot.engine_rowid` — the index rowid the window has been mirrored
  through. FLUSH FIRST, MARK SECOND.
- **Warm boot** (`OpenState` succeeded): per routed schema, ledger rows whose
  `seq` is above the engine's persisted max are re-ingested (the arena never
  flushed them); engine rows the ledger does not hold are tombstoned again
  (the engine does not persist tombstones); records past the coverage mark
  that the ledger does not hold are ingested; the window bound is re-applied.
  All of it per page under the store lock, in the background.
- **Cold engine** (`.fsdata` absent or unusable — it is discarded, the
  database is kept): the ledger is cleared and each window is filled with the
  newest records from the control tables, page by page.
- **Poison recovery** (`RecoverPoisonedEngine`): a fresh runtime reopens the
  SAME database (the rollback journal discards what the trapped engine had in
  flight) and the window is brought current exactly as at boot.

## 7. Migration & compatibility gates

- `internal/storage` public surface preserved (B.2); callers unchanged.
- Wire formats (datasync frames, shard/PNM) byte-stable; parity harness
  (A.2) guards engine result bytes against the browser host.
- Sibling stores: storefront + trust move onto the store API (B.5); auth,
  admin, audit, license, peers migrate in B.6; `mattn/go-sqlite3` deleted
  in B.8.

## 8. Single-writer liveness lock + ingest topology (loop C.6b)

The store is SINGLE-WRITER by construction: one in-process engine owns the
control database and its rollback journal, and the engine's VFS carries no
cross-process locks. Two independent processes on one basePath (the pre-C.6b
legacy production shape: daemon + `spacedatanetwork-ingest.service`) would
interleave page writes — store corruption. There is no read-only open either:
a read verb that finds the store held reads through the daemon's API.

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
