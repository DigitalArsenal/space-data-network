# wasmrt testdata — wasi-threads proof fixture

`wasithreads_fixture.wasm` is the artifact that proves kubo's wasmrt wasi-threads
host (see `../wasithreads.go`) actually SPAWNS and RUNS more than one OS thread
under WasmEdge (compiling != running).

## What it is

A generic pthreads program (`wasithreads_fixture.c`) — ZERO OD/SDN knowledge —
compiled for `wasm32-wasip1-threads` with an **imported shared memory**. Its
import/export shape mirrors a real wasi-threads module:

| kind   | name                                                        |
|--------|-------------------------------------------------------------|
| import | `env.memory` (shared, min=2, max=16384 pages)               |
| import | `wasi.thread-spawn` `(param i32) -> i32`                    |
| import | `wasi_snapshot_preview1.{clock_time_get,proc_exit,sched_yield}` |
| export | `wasi_thread_start` `(param i32 i32)`                       |
| export | `run(nthreads i32) -> i32`, `get_marker`, `get_peak`, `get_joined`, `memory`, `_initialize` |

`run(n)` spawns `n` real pthreads; each writes a distinct marker into shared
memory and busy-works while recording the peak number of workers running
simultaneously; `main` joins them all. `get_peak() == n` is an in-guest witness
of true parallelism; `get_marker(i)` distinctness proves per-thread stacks.

## Rebuild (hermetic, in Docker — never on prod)

    ./build-wasithreads-fixture.sh          # SDKARCH=arm64 default; x86_64 for amd64 hosts

Pinned inputs: wasi-sdk 24.0, clang `--target=wasm32-wasip1-threads`,
`-Wl,--import-memory,--shared-memory,--max-memory=1073741824`.

sha256(wasithreads_fixture.wasm) = e65f215d5bacd2ab3f8976605ec1bff32c1654edab62beeff38a792f610bb779
