# flatsqlrt — FlatSQL-WASI engine hosted in-process (WasmEdge)

Hosts the FlatSQL streaming SQL-over-FlatBuffers engine
(`flatsql-wasi.wasm`, a WASI reactor) inside the Go daemon via the shared
`internal/wasmrt` WasmEdge wrapper, and wraps its C ABI in a Go API.

This is the engine that replaces `mattn/go-sqlite3` per
`ARCHITECTURE_FLATSQL_FIRST.md`: data stays as FlatBuffers, queries run over
the raw buffers through SQLite virtual tables, and results stream out as
aligned size-prefixed FlatBuffer frames (`QueryRawFlatBufferStream`).

## Embedded artifact provenance

`flatsql-wasi-noeh.wasm` is the **no-exceptions** WASI build (CMake target
`flatsql_wasi_noeh`, `-fignore-exceptions`), the PUBLISHED release's file, byte
for byte (published-deps law):

- npm package: `flatsql@3.4.0` (`https://registry.npmjs.org/flatsql/-/flatsql-3.4.0.tgz`),
  published by flatsql's `npm-publish.yml` from tag `v3.4.0` with provenance
- gitHead: `14a307555c21d7c28342d664d13cb3f04a8a74b9`
- sha256: `8f11fd49ee2e6961b9c1c22dd891d5a6645d0faf481f815408c0f1f5d3a385ab`
  (the package's `wasm/integrity.json`; `TestEmbeddedArtifact`)
- 2,154,784 bytes; emscripten/emsdk 4.0.23, FlatBuffers 8af3053e
- what it adds over the previous embed: record-arena compaction at runtime
  (`flatsql_compact_arena`, `flatsql_arena_stat`, `arena.go`; flatsql
  docs/STORAGE-DURABILITY.md §6.4.2), a partition's vtab showing only its own
  rows, idempotent index inserts, and everything flatsql released since 2.0.3.
  WAL (below) is in the release itself since 3.4.0.
- previous: `flatsql@2.0.3` plus the WAL commit `51471e7` (local build), sha256
  `19ba179354064a3e9e448548ff974f045649e469e2ede00836383df88ca1c3bc`. On a copy
  of a host-02-sized store the two answer identically: every partition's rows
  and bytes, the ledger, the partition map and the unified views
  (`TestEngineUpgradeAnswersIdenticallyOnHost02Fixture`).

A store opened by this engine that compacted its arena writes engine state
`format_version` 2 (sequence runs); the previous engine answers -2 on it and
re-derives its index from the stream, renumbering the rows, and the warm-boot
residency reconcile re-mirrors the window (by design; not measured). Rolling
back after a compaction costs a re-derivation and a window refill, not data.

### journal_mode=WAL

This artifact can open a database in WAL. The stock build cannot: SQLite omits
WAL wherever the VFS supplies no shared memory, and this VFS left
`xShmMap/xShmLock/xShmBarrier/xShmUnmap` null. The wal-index only has to be
SHARED when several processes attach, though, and SQLite's own unix VFS backs
it with heap memory under an exclusive lock ("we do not really need shared
memory ... simulated with heap memory", `unixOpenSharedMemory`). FlatSQL is
opened by exactly one writer — the one-daemon-per-box law — so the same
shortcut applies and the four methods are now implemented on the heap.

Measured on this machine, same inserts and commit cadence, journal mode the
only difference: TRUNCATE 1002 rows/s at 49.9 ms per commit, WAL 5301 rows/s
at 9.4 ms — 5.3x. End to end through the SDN store, a 64-record write window
goes from 233 to 1440 rec/s (6.2x) because TRUNCATE fsyncs the rollback
journal on every commit.

SAFE ONLY BECAUSE THERE IS ONE ENGINE CONNECTION. The heap wal-index is
per-connection, so two SQLite connections onto one database would each hold
their own and corrupt it. `flatsqldrv`'s pool is eight stateless proxies onto
the ONE engine SQLite context; that is the invariant this depends on.

This version enables SQLite FTS5 and schema-directed `flatsql_record_text`
extraction, including schemas registered after ingestion or behind unified
source views. SDN indexes searchable values without converting its record
store: result pages still contain the original size-prefixed FlatBuffers.
The published no-exceptions artifact matches the locally verified build byte
for byte; `sdn-js` pins the same package version for browser queries.

### The seven `env` imports are a HARD GATE

From v1.4.0 the artifact imports `flatsql_io_open / read / write / truncate /
sync / size / close` on module **`env`** UNCONDITIONALLY. A host that does not
register them **cannot instantiate the module** — there is no degraded mode.
`hostio.go` is that registration, and the artifact bump and the host wiring must
always land in the same commit.

The measured import surface of this artifact is 6 WASI + 7 `flatsql_io`; the
WASI six are unchanged from the pre-VFS build (`clock_time_get`, `fd_write`,
`fd_read`, `environ_sizes_get`, `environ_get`, `random_get`) — FlatSQL uses no
WASI file descriptors at all, so **preopens are not part of this contract**.

A runtime created WITHOUT `WithFileIORoot` still registers all seven, but every
one refuses (`refusingHostFuncs`). That is deliberate and fail-closed: the
defect this lane exists to remove was I/O that *looked* durable. An ephemeral
engine's disk-backed open fails outright instead of succeeding against RAM.

`kubo/sdn/flatsqlrt` is a separate lane and deliberately stays on the pre-VFS
artifact — old artifact plus old host is self-consistent; bumping it without the
same `env` wiring is what would break it.

### DEPLOY REQUIREMENT — re-run `prewarm-aot` with this bump

The AOT cache key is `flatsql-<sha256[:8]>-we<libwasmedge>.aot.wasm`, keyed on
the ENGINE BYTES. Changing the artifact invalidates every host's cache, and the
daemon **never compiles at startup** by design — so a host that ships this
binary without re-running `spacedatanetwork prewarm-aot` as the daemon's user
silently falls back to the INTERPRETER, which is ~100x slower for query
workloads.

That failure is especially nasty for this particular change: the whole point is
a faster boot, and an interpreted engine would make the boot SLOWER while every
log line still said the warm path was taken. The boot line
`FlatSQL engine mode: ...` is what to check — it must say AOT.

Why no-exceptions (loop A.3/A.3b findings, measured): WasmEdge's AOT
compiler (0.14–0.17) cannot parse wasm-exceptions (exnref) modules, and its
interpreter runs the engine ~100x slower than native (nearest-epoch over
145K rows: 38.7 s interpreted vs 0.59 s AOT; ingest 78K vs 4.09M rec/s).
Wasmtime runs exnref natively but its C API/Go bindings do not expose the
exceptions proposal yet. The no-EH build is export-identical and
byte-parity-verified against the browser artifact (parity_test.go).

Error semantics: EVERY host-reachable query failure is a value, never a
trap. Two layers get there:

- pre-validation (flatsql A.3c): bad SQL, param-count mismatch, unknown
  template, duplicate source, bad schema are latched before execution;
- exception-free EXECUTION (flatsql no-eh query-error latch, graph task
  `mod-flatsql-query-params-unreachable-trap`): SQL errors raised while the
  statement RUNS — constraint violations, busy/locked-after-retries, bind
  and IO errors — return through `executeNoThrow`/`queryNoThrow` instead of
  `throw`.

The second layer is not cosmetic. This artifact is compiled
`-fignore-exceptions`, so a `throw` on this build is not an exception, it
is `unreachable`: the guest aborts and the whole engine instance is
poisoned. Before the latch, an ordinary UNIQUE-constraint violation inside
one bound INSERT aborted host-01's record-catalog hydration on every boot
(`flatsql_query_params`, `calling stack:3351, 3351, 3351, 325, 192, 574,
3351`), so its 1.34M-frame catalog never finished hydrating. The contract is
asserted directly by `sql_error_no_trap_test.go` across every query entry
the C ABI exposes.

Only a genuine trap (a remaining internal throw path, OOM, unreachable)
sets `Runtime.Poisoned()`; a poisoned runtime must be discarded and
recreated.

Production daemons should pass `WithPrecompiledAOTCache(dir)`: the portable
module is AOT-compiled by an explicit release/prewarm step and loaded from
the sha256-keyed cache afterwards. `WithAOTCache(dir)` is only for tests and
maintenance tools that intentionally compile on cache miss.

When the flatsql dependency moves, copy its published no-exceptions artifact
and update this block. Verify the package integrity and artifact SHA before
copying `node_modules/flatsql/wasm/flatsql-wasi-noeh.wasm` here.
The embedded sha256 is asserted by `TestEmbeddedArtifact`.

## The partition-store engine (store format 2)

`flatsql-ps-threads.wasm` is the partition-store engine of store format 2
(stack design `docs/architecture/flatsql-partition-store.md`, T6), embedded
by `psartifact.go` and run by `psinstance.go` (`PSABIEngine`) as separate
writer and reader instances. It is the PUBLISHED release's file, byte for
byte (published-deps law, design A34):

- npm package: `flatsql@3.5.1` (`https://registry.npmjs.org/flatsql/-/flatsql-3.5.1.tgz`),
  published by flatsql's `npm-publish.yml` from tag `v3.5.1` with provenance
- gitHead: `965b16b1d48cf53257879ceac41dfadd2e39b359`
- sha256: `3a215da45a53f3a7016257429c392720e728caf850d3a1213dc501bfb5844359`
  (the package's `wasm/integrity.json`; `TestEmbeddedPSThreadsArtifact`)
- 2,485,719 bytes; `wasm32-wasip1-threads`, wasi-sdk 30
- 3.5.1 creates a fresh store in a crash-safe order (A5: the registry files,
  MIGRATED, then STORE, whose one torn state the engine finishes from MIGRATED;
  `format2/store_crash_test.go`) and keeps per-partition bookkeeping
  O(1)/O(log S) per commit (flatsql PARTITION-STORE.md §40, B4, M3). 3.5.0
  added the query-gap plans the format-2 reads use (§39) and records stored
  with their own size prefix; `flatsql_ps_stats` returns 36 entries since
  3.3.0 (`Writer.Stats`)
- previous: `flatsql@3.5.0`, sha256 `787adbcccf52a9e9fe2767d6679b6f2d94a4ead41985bf2a7bb0855f821abc72`

It loads only as a THREADS + Interruptible AOT artifact (design A30: no
interpreter fallback) under the prefix `fsqlps`. `spacedatanetwork
prewarm-aot` compiles it on every host (a failure fails the command only when
`SDN_STORE_FORMAT=2` is set); a format-2 daemon never compiles on the service
path. Bumping the artifact therefore needs a `prewarm-aot` run, exactly like
the legacy engine below.

## ABI conventions (mirrors `flatsql/wasm/standalone.js`)

- WASI reactor: instantiate with WASI + the exception-handling proposal
  (the module uses Wasm exnref), then call `_initialize` before anything.
- Strings are NUL-terminated C strings in guest memory; buffers are
  ptr+len; `size_t` = i32 (wasm32).
- Query params cross as a TLV blob `[u8 tag][u32le len][payload]`,
  tags: 0=null 1=bool 2=int64 3=float64 4=string 5=bytes.
- Raw stream results: `flatsql_query_raw_flatbuffer_stream` requires every
  selected cell to be a BLOB (use the hidden `_data` column) and exposes the
  concatenated `[u32le length][bytes]` frames via
  `flatsql_response_artifact_data/_size`.
- Errors: any 0/false return → `flatsql_get_error` (C string).
- The engine is in-memory (SQLITE_OMIT_WAL); durability is app-driven via
  `flatsql_export_data` / `flatsql_load_and_rebuild`.
