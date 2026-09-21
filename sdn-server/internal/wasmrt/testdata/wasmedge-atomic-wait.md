# Isolated WasmEdge atomic-wait regression

The patch applies to WasmEdge 0.16.4, source commit
`be85c2fbba68318f103b4a766728f6946e65abf8`.

Upstream compares the shared word, registers the waiter, and sleeps under
separate lock intervals. A notification can be lost between these steps.
It also keeps waiting after a notification unless the memory value changes;
[WebAssembly atomic notification](https://webassembly.github.io/threads/core/exec/instructions.html) does not require a store.

The patch uses the same mutex for comparison, registration, notification and
condition-variable wait. A per-waiter notification predicate handles spurious
wakeups and notifications without a value change. An ordered multimap keeps
waiter iterators valid while other threads register. Cancellation still wakes
all waiters and returns the interrupted error.

`SDN_WASM_ATOMIC_REGRESSION=1 go test ./internal/wasmrt -run
TestWasiAtomicNotifyWithoutStore -count=1` fails against upstream 0.16.4 and
passes against the patched library. The fixture has a worker notify until a
waiter is present, without storing to the word. The expected wait result is 0.
Run all worker, actual-artifact, and repeated HTTP flow tests against the same
library before deployment. Runtime timeouts remain a separate defense.

This is a development dependency patch, not an upstream release or a global
runtime upgrade. Build an isolated SDK and point the development executable's
rpath at it. Keep the original SDK and rollback executable intact. The stack's
`deployment/ut-austin-core/build-wasmedge.sh` records the reproduction recipe.
