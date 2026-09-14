# Survey and history: sdn-stream-flatbuffers-into-flatsql

Moved out of the task file 2026-09-14 (8 KiB task budget). The task file keeps only objective, scope, acceptance and verification.


## The command (owner, 2026-09-02, verbatim)

"The flatbuffers are streamed to flatsql which persists it directly to disk, and
builds the sql index / metadata which it writes to disk; any node restart just
uses whatever is on disk, and only loads into memory what it needs to continue
consuming."

And on the layers this deletes: "it should NEVER re-ingest, that's literally the
whole point of flatsql, it's just on disk."

## The shape of the defect

Record BYTES live in host-written stream files; FlatSQL stores POINTERS
(`stream_path`, `stream_offset`, `record_length`). The database is therefore
treated as a cache to be re-derived, so every boot replays a journal. Three
copies of the same records: host-01 `control.flatsqldb` 22 GB +
`record-catalog.flatsqlmeta` 19 GB + `flatsql-streams/` 5.5 GB.

## Survey correction 2026-09-14 (read this before item 1)

Items 1 and 2 as originally written ARE ALREADY BUILT. Verified, not assumed:

- The vendored artifact imports all seven `flatsql_io_*` on module `env`
  (`wasm-objdump -j Import` on `internal/flatsqlrt/flatsql-wasi-noeh.wasm`:
  13 imports = 7 io + 6 WASI) and exports `flatsql_open_db`,
  `flatsql_is_disk_backed`, `flatsql_open_state`, `flatsql_reindex_all/_step`,
  `flatsql_flush_index`, `flatsql_flushed_offset`.
- `flatsqlrt.go:352-364` registers them UNCONDITIONALLY (`HostIOModule = "env"`,
  hostio.go:49); a runtime with no file root still registers all seven and
  returns `FLATSQL_IO_ERR_ACCESS` — fail-closed, never a silent RAM fallback.
- `flatsql_boot_state.go:774` already opens the control DB disk-backed with
  `JournalTruncate` and hard-fails if the engine reports NOT disk-backed.

So there is NO pin bump and NO flatsql rebuild on the critical path, and the
engine arena already restores from `.fsdata` at boot — the boot comment says it
outright: "OpenState verifies that index and makes the records visible in
milliseconds — no ReindexAll, no re-ingest through WASM."

## What the 6 h boot actually is (measured on this machine)

From the owner's own recorded runs (`.tmp/admin-dev-*.log`):

- warm boot **37 s**, of which `OpenState` is 25 s
- cold boot **1 h 18 m**: "replayed=28249668 sources=78 total_records=2265803"

28.2 M journal frames to yield 2.27 M records — **12x redundancy**, because every
re-ingest of an unchanged object appends another frame forever. The journal was
9.8 GB then and is 46 GB on the dev node now; host-01 19 GB, host-02 22 GB.

The journal is therefore redundant with a control DB that is ALREADY durable.
It is not load-bearing for records — it is load-bearing for six things that must
be re-homed before it can be deleted (see below).

## What lands — Part A (sdn)

1. **CAT supersede on ingest.** Kill the 12x at source. Within one producer's
   table a CAT record supersedes the previous record for the same object.
   Identity per CAT.fbs: `(CATALOG_URI, CATALOG_OBJECT_ID)` when both present,
   else `NORAD_CAT_ID`, else `OBJECT_ID`; no identity means no supersede. No
   historical CAT is stored. `extractIndexedFields` writes nothing but
   `norad_cat_id` for CAT today, so un-numbered objects have NO identity at all —
   fixing that is part of this.
2. **Delete the record-catalog journal and its replay**, and with it
   `record_catalog_journal.go`, `record_catalog_replay.go`, the boot resume
   marks and `flatsql-streams/`. Re-home its six dependents first:
   - `sdn_record_index.rowid` — the WIRE-VISIBLE datasync cursor deployed peers
     hold. The journal is what reproduces it (frames carry `Index.RowID` and
     replay re-inserts explicitly).
   - repeat-CID producer attribution (`latestUpsertAttributionByCID` scans
     frames because the SQL read source returns an unstable pick).
   - delete durability — every delete path appends its event AFTER commit; that
     event is what makes a delete survive.
   - `CompactStreams`' atomic commit unit (streams + journal committed together
     via a CRC manifest; `recoverPendingCompaction` runs before the journal opens
     at every boot).
   - read-only opens (`NewFlatSQLStoreReadOnly` re-derives from journal+streams
     into an EPHEMERAL engine).
   - `RecoverPoisonedEngine` (deletes the control DB and rebuilds from the
     journal).
3. **Keep `auxiliary.flatsqlmeta`.** It is a SECOND journal and is NOT records:
   node EPM, pin ledger, dataset shard publications incl. `feed_sequence`,
   asset-pin audit, source batch licences. Out of scope, not deleted.

## Part B (flatsql) — the actual ceiling

`loadStreamFromDisk` does `std::vector<uint8_t> stream; readWholeStream(&stream)`
(cpp/src/flatsql_state.cpp:229-235): the ENTIRE `.fsdata` is read into wasm
linear memory and every frame copied into the arena, against a 4 GiB ceiling
shared by all routed standards. THAT is why the hot window exists, and it is why
"all records live in the engine" is impossible today.

Part B pages `.fsdata` instead of slurping it, so resident memory tracks the
working set. The engine hot window can only be deleted after Part B lands;
until then it stays and `enforceEngineHotWindowLocked` keeps evicting.

## Explicitly refused

- **No checkpointing, resuming or trimming the journal.** That is optimising a
  layer this task removes. It has been tried twice; the 2026-09-14 attempt made
  the next boot resume mid-journal, collided explicit rowids
  ("UNIQUE constraint failed: sdn_record_index.rowid") and left host-02 failing
  every boot. Reverted in 575f2610. Tasks
  `sdn-record-catalog-replay-unbounded-journal` and
  `sdn-engine-nested-transaction-replay-abort` were dropped as contradictory.
- **No migration and no back-compat.** Owner 2026-09-14: all current hosts are
  wiped and take the new binary. Do not write a converter, do not read an old
  journal, do not keep a compatibility branch.
- **No second engine.** `flatsqldrv/standalone.go:33` still opens stock sqlite
  for auth.db; it may move now that FlatSQL is disk-backed, but that is its own
  task, not scope creep into this one.

## Verification

- A node with a full catalog boots and serves reads in **seconds**, cold, with
  no re-ingest and no hydration phase. State the measured number.
- No process writes record bytes outside FlatSQL: the store directory holds the
  engine's own files and nothing else.
- Kill -9 mid-ingest, reopen: identical query results and high-water mark
  (STORAGE-DURABILITY.md §6.6 scenario).
- CAT row count equals the distinct object count of the ingested edition, across
  two successive ingests of the same catalog.
- Disk: one copy of the records.
- Rollout is a fleet wipe: build locally, wipe every host's store, ship the
  binary, verify boot time and catalog completeness on each before declaring it.

## Escalation

The engine-side disk path and ABI landed natively (§3.3,
`cpp/test/disk_persistence_test.cpp`). If the wasm VFS bridge (§3.5) proves
incomplete under WasmEdge, escalate to the `flatsql` component rather than
building a host-side shim around it.
