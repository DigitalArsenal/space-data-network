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
- flatsql commit: `8e86fee` (v1.2.0 — sandboxed public query `flatsql_query_sandboxed`, gateway loop G.5)
- sha256: `1c53398ae6dc76ec806a3e4724461c6ccc82b6fe8861c6a1a42efcfa4b4c7f64`

Why no-exceptions (loop A.3/A.3b findings, measured): WasmEdge's AOT
compiler (0.14–0.17) cannot parse wasm-exceptions (exnref) modules, and its
interpreter runs the engine ~100x slower than native (nearest-epoch over
145K rows: 38.7 s interpreted vs 0.59 s AOT; ingest 78K vs 4.09M rec/s).
Wasmtime runs exnref natively but its C API/Go bindings do not expose the
exceptions proposal yet. The no-EH build is export-identical and
byte-parity-verified against the browser artifact (parity_test.go).

Error semantics (since flatsql A.3c no-throw refactor): user-triggerable
failures — bad SQL, param-count mismatch, unknown template, duplicate
source, bad schema — are pre-validated/latched in the engine WITHOUT
throwing, return clean errors, and do NOT poison the runtime. Only a
genuine trap (untouched internal throw path, OOM, unreachable) sets
`Runtime.Poisoned()`; a poisoned runtime must be discarded and recreated.

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
