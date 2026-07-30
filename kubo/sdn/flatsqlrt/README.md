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
`flatsql_wasi_noeh`, `-fignore-exceptions`) copied verbatim from the
`flatsql` repo (superproject submodule `repos/main-packages/flatsql`):

- source path: `flatsql/wasm/flatsql-wasi-noeh.wasm`
- flatsql commit: `b26ed45` (no-eh query-error latch — a SQL error is a value, never `unreachable`)
- sha256: `4d17fc5f4936305a005bfc5c63c550a58e448412900299ac7d6adc63ba0137e9`

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

When the flatsql submodule pin moves, rebuild + re-copy the artifact and
update this block (`cmake --build build-wasm --target flatsql_wasi_noeh`
in `flatsql/cpp`, then copy `flatsql/wasm/flatsql-wasi-noeh.wasm` here).
The embedded sha256 is asserted by `TestEmbeddedArtifact`.

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
