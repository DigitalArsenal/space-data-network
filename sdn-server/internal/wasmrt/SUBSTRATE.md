# Partition-store substrate on WasmEdge (T5)

The host side of a threaded FlatSQL partition-store instance: design
`docs/architecture/flatsql-partition-store.md` (stack repo) §5.1, §5.4, §15,
§18 T5 and amendments A21–A24, A29–A31. The engine (T1/T4) and the router
(T6) build on it. Code: `wasmrt/{wasi_threads,service_threads,doorbell,
doorbell_native.c,substrate,cgo_wasmedge}.go`,
`flatsqlrt/{hostio_native.c,hostio_native.go,psinstance.go,aot.go}`.

## Engine contract

An instance loads `flatsql-ps-threads.wasm` only as AOT code compiled with
THREADS and Interruptible (A30), after proving it runs native: the
interpreter's instruction counter must not move across `_initialize`.

Exports the host calls: `_initialize`, `malloc`, `free`,
`flatsql_ps_init(role, cfg_ptr, cfg_len) -> 0`, `flatsql_ps_layout(out_ptr) ->
bytes`, `flatsql_ps_start() -> threads started`, `flatsql_ps_wake(addr, n)`
(= `memory.atomic.notify`), `flatsql_ps_stop(deadline_ms f64) -> threads
left`, `wasi_thread_start`. Control calls run under an explicit budget
(default 10 s); one that traps or outruns it fences the instance.

**Substrate layout v1** (`flatsql_ps_layout`, 128 bytes, u32 LE):

| off | field | meaning |
|---|---|---|
| 0 | magic | `0x314C5350` ("PSL1") |
| 4 / 8 / 12 | version / size / flags | 1 / 128 / 0 |
| 16 | stop_word | u32; the host stores 1 to stop |
| 20 / 24 | heartbeat_base / count | u32 per service thread |
| 28 / 32 | doorbell_base / count | `{seq u32, sleeping u32}` per writer |
| 36 / 40 | ack_base / count | u64 per ack word, monotonic |
| 44 | mem_gen | u32, bumped after `memory.grow` |
| 48 | canary_base | u32 per service thread (A30) |
| 52 | live_threads | u32 |
| 56–127 | reserved | 0 |

Guest obligations: every service loop increments its heartbeat and checks the
stop word (long vtab/merge loops every 4 K entries); a leaving thread stores
`0xFFFFFFFF` in its heartbeat; an idle writer sets `sleeping`, re-reads `seq`
and waits on `seq` with the value it read; every address is naturally aligned
and below the initial memory size.

**Host I/O flags and status** added to `flatsql_io.h`'s set (T1 must define
the same values): `CREATE_PARENTS 0x0100` (mkdir -p, fsync each new
directory's parent, fsync the parent of a newly created file),
`UNLINK_IF_UNUSED 0x0200` (`BUSY` while any instance of the store holds the
path), `OPEN_DEFERRED 0x0400` (a synchronous open here), `FLATSQL_IO_ERR_BUSY
-7`. Any other flag bit is refused. After a revoke, a call parks until the
instance is stopped and then returns `FLATSQL_IO_ERR_ACCESS`.

## The C host I/O module (`flatsqlrt/hostio_native.c`)

The seven `env.flatsql_io_*` are C WasmEdge host functions added to the
instance's own `env` module: no Go code, cgo callback or WasmEdge-go
host-function lock per call. Virtual handles `(slot << 8 | gen)`; a
per-instance fd budget (RLIMIT split statically, A29) with CLOCK eviction and
reopen; the read/write/sync path takes no lock (slot pins and atomics); the
instance table lock is taken only to decide an open, close, reopen or
eviction, never across a syscall; the store-wide path registry lock only on
open, close and unlink. Both are pthread mutexes, instrumented for hold time
and for the instance classes that took them. Sync is `fdatasync` on Linux and
`F_FULLFSYNC` on darwin (directories too). Confinement: `openat2
(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` on Linux, an `O_NOFOLLOW` component
walk elsewhere. BULK threads get nice +10 (QoS utility on darwin).

**Revocation (A23):** set `revoked`; `dup2` a closed-pipe sentinel over every
fd (the fd number stays allocated, so a replacement never receives it and a
late call from the revoked instance hits the pipe); drain only in-flight
mutating calls; release the path registrations. Syncs are not drained.

## WasmEdge 0.16.4 patches

Carried as heredocs in `scripts/build-static-wasmedge.sh` (so the CI prefix
cache key and the Dockerfile's static layer change with them); byte-identical
copies in `testdata/` feed the tests (`TestStaticBuildCarriesTheRuntimePatches`).

- `01-atomic-wait`: compare, register and sleep under one mutex; a notify
  wakes a waiter without a store (`testdata/wasmedge-atomic-wait.md`).
- `02-stop-token`: a stop is sticky for every invocation on the executor and
  is read, never consumed, by the interpreter and by Interruptible AOT code;
  the first invocation on an idle executor clears a stale token. Upstream's
  `exchange(0)` stopped one thread per cancel, and made every AOT block an RMW
  on one cache line: four Interruptible spinners ran 2.8x the interpreter
  unpatched against 17x patched (darwin/arm64).

`spacedatanetwork substrate-selftest [--require-patched]` measures them in the
running binary (notify without store, one stop ending every thread,
Interruptible AOT that really runs native) and round-trips the C host I/O
module. A31: it is a start-up metric (logged when the first instance opens)
and a release gate, not a format-2 refusal.

## Host rollout (coordinator ops)

A31 removed the per-host library install: the patched runtime is inside the
static binary.

1. Build the fleet binary from the landed commit:
   `docker buildx build --platform linux/amd64 -f deployment/docker/Dockerfile -t sdn-build:<sha> --target builder --load .`
   The `wasmedge-static` stage rebuilds (its input script changed); its log
   shows `WasmEdge patch applied: 01-atomic-wait.patch` and `02-stop-token.patch`.
2. `node deployment/release/extract-release-binary.mjs --image sdn-build:<sha> --container-path /out/spacedatanetwork --out ./buildout-spacedatanetwork --arch amd64 --replace`
3. Before publishing, on a linux/amd64 machine (or `docker run --rm --platform linux/amd64 -v "$PWD":/b debian:bookworm-slim /b/buildout-spacedatanetwork substrate-selftest --require-patched`):
   exit 0 with `"patched": true` and `"native_host_io": true`.
4. `node deployment/release/publish-fleet-update.mjs --binary ./buildout-spacedatanetwork --source-commit <shortsha>`; host-01 and host-02 upgrade in place.
5. On each host, as the service user, run the installed binary's
   `substrate-selftest --require-patched` and keep the JSON as the rollout
   receipt. No AOT prewarm is needed for this change: the runtime still
   reports 0.16.4, so the legacy engine's cached artifacts keep their key, and
   code compiled without Interruptible has no stop-token checks to differ.
6. Rollback is the update lane's previous slot (five are kept).

## Measured (2026-09-27)

| Check | Mac Studio (darwin/arm64, 28 CPU) | Linux arm64 VM (Docker Desktop, `--cpus 4`) |
|---|---|---|
| #1 4 threads vs 1, same work | 0.252 | 0.252 |
| #3 guest pread / pwrite p99 over raw syscall (8 x 1M, 4 KiB) | +1.0 / +9.2 us (darwin raw pthreads write faster than any other thread, Go's included) | +0.51 / +0.51 us |
| #3 10,000 files through the LRU at RLIMIT 256 | 0 errors | 0 errors |
| #4 / A23 bytes after revoke, RLIMIT 64 + 1,000-file replacement | 0 / 0 foreign | 0 / 0 foreign |
| A23 revoke with a 30 s fsync stuck | 18 us | 65 us |
| #6 trap in one of three instances | 0 cancellations outside | same |
| #7 idle wakeups (4 writers) | 74/s | 72/s |
| #7 / A24 ring->ack, 20k transitions | p50 11 us, p99 65 us, 0 timeout-with-work | p50 38 us, p99 129 us, 0 |
| A21 one stop, 16 Interruptible AOT spinners | 0.5 ms | 0.5 ms |
| A21 hung thread fenced | 10.8 s | 11.8 s |
| #5 LazyFS power loss, durable head | n/a | 0 violations; negative control 100% caught |

Latency and scaling thresholds are asserted under `SDN_PS_ACCEPTANCE=1` (the
named machine); CI reports them.

## Deviations from the design (with evidence)

- **Control calls** run as cancellable async invocations serialized by a Go
  mutex, not on a dedicated exec thread (§3, §5.4). A dedicated thread's
  synchronous invoke cannot be interrupted; with the stop-token patch and
  Interruptible AOT a cancel ends the call and every service thread, which is
  what a hung control call must do (A21, A30).
- **The doorbell thread is C** (a pthread on a condition variable), still one
  per instance (A29). A Go goroutine locked to its OS thread costs two thread
  wake-ups per ring (an idle M, then its own M): ring->ack p50 867 us, p99
  1.96 ms and 28 waits ended by timeout in the Linux VM, against 38 us, 129 us
  and 0 in C.
- **CLOCK instead of exact LRU** for fd eviction, so the hot path stays
  lock-free (one relaxed store per call).
- **No new status for revoked calls**: they park, then return
  `FLATSQL_IO_ERR_ACCESS` (§5.4) rather than a new STOPPED code (A23).
- **#5 directory half**: LazyFS forwards mkdir and create to the backing file
  system (`lazyfs/src/lazyfs.cpp` `lfs_mkdir`, `lfs_create`) and loses only
  unsynced data, so its trials cannot drop an un-fsynced directory entry. The
  data half ran (above); the directory half needs dm-log-writes replay on a
  Linux host (`scripts/lazyfs-dir-durability.sh` notes the procedure).
- **Threaded AOT cache key** adds `-intr-<runtime tag>`; the engine prefix
  (`fsqlps`) must not start with `flatsql-`, which the legacy engine prunes.
