# flatsqlrt — FlatSQL-WASI engine hosted in-process (WasmEdge)

Hosts the FlatSQL streaming SQL-over-FlatBuffers engine
(`flatsql-wasi.wasm`, a WASI reactor) inside the Go daemon via the shared
`internal/wasmrt` WasmEdge wrapper, and wraps its C ABI in a Go API.

This is the engine that replaces `mattn/go-sqlite3` per
`ARCHITECTURE_FLATSQL_FIRST.md`: data stays as FlatBuffers, queries run over
the raw buffers through SQLite virtual tables, and results stream out as
aligned size-prefixed FlatBuffer frames (`QueryRawFlatBufferStream`).

## Embedded artifact provenance

`flatsql-wasi.wasm` is copied verbatim from the `flatsql` repo
(superproject submodule `repos/main-packages/flatsql`, npm `flatsql`):

- source path: `flatsql/wasm/flatsql-wasi.wasm`
- flatsql commit: `0c76d87b29fcffae453a88969418cc70884a5ecc`
- sha256: `3b28fd9cefe376c0fe10e9fb41f280ece36d50b93ab4f482208db2d27cc18cf6`

When the flatsql submodule pin moves, re-copy the artifact and update this
block (`cp ../../flatsql/wasm/flatsql-wasi.wasm internal/flatsqlrt/` from
`sdn-server/`). The embedded sha256 is asserted by `TestEmbeddedArtifact`.

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
