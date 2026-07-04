# WasmEdge 0.14: nested AOT execution corrupts the suspended outer AOT frame

**Status:** worked around in-tree (loop C.5b). Upstream bug in libwasmedge
0.14.0 (Go bindings `github.com/second-state/WasmEdge-go v0.14.0`).
**RETESTED (loop C.9, 2026-07-03): FIXED in libwasmedge 0.16.4** — see
"Upstream retest" at the bottom. The workaround stays in-tree because 0.14
builds remain possible (the v0.14.0 Go binding is the newest release and
builds against both runtimes).

## Symptom

An AOT-compiled flow artifact mounted by `internal/flowrt` compiles clean and
serves fine — until a dispatch issues a `storage.flatsql_*` capability
hostcall. The hostcall itself succeeds (the store engine, its own AOT VM,
returns the correct response), but as soon as control returns to the
suspended flow VM, its very next linear-memory access traps:

```
[error] execution failed: out of bounds memory access, Code: 0x408
[error]     When executing function name: "space_data_module_runtime_dispatch_current_invocation_direct"
```

The same artifact, same bytes, same request runs correctly interpreted
(loop C.4 shipped that configuration). The trap looked artifact-specific for
a while because only the retrieval node's dispatch died — every dispatch
without a nested engine call (http-route, decision-gate, …) ran fine under
AOT.

## Root cause (empirical matrix)

A standalone driver (WasmEdge-go only, no SDN code) that replays the exact
recorded $HTQ ingress frame and hostcall response bytes against the real
123 KB flow artifact isolates the trigger precisely — it is NOT the
artifact, the payload, memory growth, bulk-memory ops, or the 2048-page
host cap:

| outer VM (flow) | nested VM executed inside host function | result |
|---|---|---|
| AOT | — (no nested execution) | OK, even with 60 MB `memory.grow` |
| AOT | interpreted engine | OK |
| interpreted | AOT engine | OK (the C.4 production config) |
| AOT | AOT engine, same OS thread | **trap on return** |
| AOT | AOT engine, different OS thread | **OK** |

libwasmedge 0.14 keeps executor state per OS thread (thread-local execution
context used by AOT-compiled code and its intrinsic/trampoline path). When
VM B executes AOT code on a thread where VM A's AOT frame is suspended
beneath a host function, B's execution clobbers that state; A's next memory
access is then bounds-checked against the wrong context and falsely traps
"out of bounds memory access". Interpreted execution does not touch the
AOT thread-local state, which is why every mixed configuration works.

## Workaround shipped

`wasmrt.WithDedicatedThread()` — the module executes every guest call on one
dedicated, `runtime.LockOSThread()`-pinned worker (channel round-trip per
call, noise next to any engine workload). `internal/flatsqlrt` always sets
it: the engine is exactly the VM that gets executed from inside other
modules' host functions (storage capability hostcalls). With the engine
pinned to its own thread, no other VM's AOT frame is ever suspended beneath
an engine execution, and flow mounts are safe to AOT-compile —
`node.MountFlows` now sets `FlowMountDeps.AOTCacheDir` (same sha256-keyed
cache as the engine, `flowmount-` prefix).

Guard test: `SDN_C4_AOT_REPRO=1 go test ./internal/flowrt/ -run
TestAOTMountRepro` mounts the real data-retrieval bundle AOT-compiled over a
seeded store and asserts the request succeeds.

Rules of thumb until upstream is fixed:

- Any VM whose exports run inside another VM's host function must use
  `WithDedicatedThread` (today: only the flatsql engine).
- Never execute a second AOT VM synchronously on the caller's thread from
  inside a WasmEdge host function.

## Upstream repro

Minimal repro without SDN: two `wasmedge.VM`s, both running AOT-compiled
modules. VM1's host function calls `vm2.Execute("malloc", 64)`. After the
host function returns, VM1 traps on its next memory access. Scratch driver
used for the bisect (drives the flow ABI + records/replays hostcall bytes):
loop C.5b, `aotprobe` — the decisive toggle is executing the nested VM on
the same vs a different OS thread.

Fix candidates upstream: save/restore the thread-local executor state around
`Executor::execute` re-entry, or make the execution context stack-scoped.
Worth re-testing on WasmEdge ≥ 0.15 before upgrading the pinned Go binding.

## Upstream retest (loop C.9, 2026-07-03)

Retested with the original `aotprobe` driver (real 123 KB bridge-mode flow
artifact, recorded $HTQ ingress + hostcall response bytes, the real
`flatsql-wasi-noeh.wasm` engine as the nested VM executed inside the
`storage.flatsql_epoch_stream` hostcall). Full matrix per runtime, v0.14.0
Go binding in both cases (it compiles unchanged against 0.16.x headers):

| outer VM | nested VM (inside hostcall) | 0.14.0 | 0.16.4 |
|---|---|---|---|
| AOT | none | OK | OK |
| AOT | interpreted engine, same thread | OK | OK |
| interpreted | AOT engine, same thread | OK | OK |
| AOT | AOT engine, same thread | **trap** (`out of bounds memory access, Code: 0x408` in `space_data_module_runtime_dispatch_current_invocation_direct`) | **OK** |
| AOT | AOT engine, different thread | OK | OK |

**libwasmedge 0.16.4 fixes this defect** (and the C.7 linked-drain trap,
`flatsql-component-linkage.md` §8.4). No upstream report needed — the fix
already shipped. Versions 0.15.x/0.16.0–0.16.3 were not tested; the in-tree
version gate (`flatsqlrt.RuntimeHasLinkedAOTFix`) therefore requires the
verified ≥ 0.16.4.

Runtime/binding compatibility found during the retest:

- `WasmEdge-go v0.14.0` (newest binding release as of 2026-07) **builds and
  passes the full sdn-server suite against libwasmedge 0.16.4** — the C API
  it uses is unchanged between 0.14 and 0.16.
- libwasmedge **0.17.0 is binding-incompatible**: it removed the by-value
  `WasmEdge_Limit` struct API (replaced by `WasmEdge_LimitContext *`,
  SOVERSION bump 0.1.0 → 0.1.1), so the binding fails to compile
  (`ast.go:187: could not determine what C.WasmEdge_Limit refers to`).
  Moving past 0.16.x waits on an upstream binding release or a fork.
- The wasm-exceptions constraint is UNCHANGED: 0.14/0.16.4/0.17.0 all reject
  EH encodings in AOT — the engine stays the no-EH build.

`WithDedicatedThread` is KEPT even on fixed runtimes: it costs one channel
round-trip per call (noise), still protects any 0.14 build, and serializes
engine execution on one thread. Flow mounts' force-interpret of LINKED
artifacts is version-gated off on ≥ 0.16.4 (flowrt/httpmount.go).

SA_ONSTACK note (measured on macOS with a genuine-trap probe, both 0.14.0
and 0.16.4 identical): during execution libwasmedge installs its own
SIGSEGV/SIGBUS/SIGFPE handlers WITH `SA_ONSTACK`, but after handling a
genuine trap it leaves the process handlers WITHOUT `SA_ONSTACK` and without
Go's handlers (sa_flags 0x43 → 0x2). Genuine wasm traps return normal Go
errors (process survives), but Go's own fault handling is degraded after the
first trap on POSIX-signal platforms (Linux; macOS Go uses Mach exception
ports for faults, so the exposure there is limited). Pre-existing on 0.14 —
not a regression, not a blocker; engine traps already poison-and-replace the
runtime.
