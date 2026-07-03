# WasmEdge 0.14: nested AOT execution corrupts the suspended outer AOT frame

**Status:** worked around in-tree (loop C.5b). Upstream bug in libwasmedge
0.14.0 (Go bindings `github.com/second-state/WasmEdge-go v0.14.0`).

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
