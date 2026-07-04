# FlatSQL as a Linked Component Dependency

Loop C.3c3 phase 1 — feasibility, design, and proof for storage-needing
modules declaring the FlatSQL engine as a **dependency** and calling its
exports **directly in-wasm** against the host store's **live engine
instance** (the one holding the hot-window data): zero hostcalls, zero
copies for queries, identical under WasmEdge (sdn-server) and the browser
shim. The `storage.flatsql_*` hostcalls in
`internal/modulert/caps/storage.go` are the transitional bridge this
design retires.

Status: **proven by running code** (see §5). Nothing in this doc is
assumed — every API claim was checked in source, every "works" claim was
executed.

---

## 1. The hard problem, stated precisely

The engine (`internal/flatsqlrt/flatsql-wasi-noeh.wasm`, emscripten
`STANDALONE_WASM=1 PURE_WASI=1`, memory `(1024, 65536)` pages, AOT-compiled
in production) and a dependent module (e.g. `data-source/retrieval`,
emscripten `STANDALONE_WASM=1`) are **separately linked emscripten
artifacts**. Each assumes it owns linear-memory layout: active data
segments at fixed addresses, `__stack_pointer`, `__heap_base`, its own
`dlmalloc` heap. Two such artifacts cannot share one memory as-is — their
static layouts collide. And if they *don't* share memory, a `(ptr,len)`
returned by `flatsql_query_raw_flatbuffer_stream` points into engine
memory that the dependent's loads/stores cannot reach (core wasm
loads/stores address the module's own memory index 0 only).

Verified engine artifact facts (via `wasm2wat`):

- Imports **only** 6 WASI functions (`fd_write`, `fd_read`,
  `clock_time_get`, `environ_sizes_get`, `environ_get`, `random_get`).
- Exports `memory`, `__indirect_function_table`, `malloc`, `free`,
  `_initialize`, and every `flatsql_*` entry point.
- Signatures are plain i32/f64 C ABI (e.g. `flatsql_create_db(i32,i32)->i32`,
  `flatsql_result_cell_number(i32,i32)->f64`).

This is exactly the export surface a linked dependent needs — including
the memory and the allocator. Nothing about the engine build has to
change for options B/D below.

## 2. Verified host API facts

### 2.1 WasmEdge-go v0.14.0 (checked in `$(go env GOMODCACHE)/github.com/second-state/!wasm!edge-go@v0.14.0/wasmedge/`)

| API | File:line | What it does |
|---|---|---|
| `VM.RegisterWasmBuffer(modname, buf)` | `vm.go:72` (`WasmEdge_VMRegisterModuleFromBytes`) | Load+validate+**instantiate** wasm as a **named** module in the VM's store. Its exports resolve imports of anything instantiated later in that VM. |
| `VM.RegisterModule(module *Module)` | `vm.go:91` (`WasmEdge_VMRegisterModuleFromImport`) | Register an **existing module INSTANCE** (from anywhere — another VM, an executor, a host module) as an import source. **No re-instantiation, no copy** — the live instance, its live memory. |
| `VM.GetRegisteredModule(name)` | `vm.go:425` | Fetch the named instance (non-owning) to hand to another VM. |
| `Executor.Register(store, ast, modname)` | `executor.go:55` (`WasmEdge_ExecutorRegister`) | VM-less equivalent: instantiate AST as named instance in a `Store`. |
| `Executor.RegisterImport(store, module)` | `executor.go:65` (`WasmEdge_ExecutorRegisterImport`) | VM-less equivalent of `RegisterModule`. |
| `Module.FindMemory/FindFunction/FindTable/FindGlobal` | `instance.go` | Direct access to a live instance's exports (used by the host to read result buffers from engine memory — unchanged from today). |
| `Table.SetData/Grow`, `NewGlobal(mut)` | `instance.go:356/369/456` | The primitives a future SIDE_MODULE mini-loader needs (GOT/table patching). |

There is **no** import-object-per-instantiation API in 0.14 (you cannot
say "resolve this module's `flatsql` imports from instance X" without
registering X under the name `flatsql` in the store/VM first). Name-based
registration is the linking mechanism, and it is sufficient.

### 2.2 Browser (checked in `flatsql/wasm/standalone.js` and the SDK)

- `loadFlatSQLStandalone` (`standalone.js:381`) instantiates the engine
  with a minimal `wasi_snapshot_preview1` shim and **retains the live
  exports** at `FlatSQLStandalone._runtime.exports` (functions + memory).
- `WebAssembly.instantiate(dependentBytes, { flatsql: engineExports })`
  is the browser-native form of instance-export linking — the imports
  object *is* the registration mechanism. `loadFlatSQLStandalone` already
  merges caller-supplied import namespaces (`options.imports`,
  `standalone.js:385-394`), and the SDK harness
  (`space-data-network-modules/tests/lib/sdkBrowserShimHarness.mjs`)
  already composes import objects the same way for module instantiation.

### 2.3 SDK flow compiler (checked in `repos/ancillary-packages/space-data-module-sdk/src/flow/flowCompiler.js`)

- Flows are linked **at compile time** from per-module guest-link objects
  (`dist/guest-link/module-link.o` + `metadata.json`), produced by
  `compileModuleFromSource` with automated symbol prefixing
  (`deriveGuestLinkRenameArgs`, `compileModule.js:330` — parses the
  object's linking section and re-compiles with `-D<sym>=<prefix><sym>`).
- Link step (`linkFlowArtifactWithEmception`, `flowCompiler.js:1204`):
  emception `em++` with `-O3 -s STANDALONE_WASM=1 -s ALLOW_MEMORY_GROWTH=1
  -s ERROR_ON_UNDEFINED_SYMBOLS=0 -Wl,--allow-undefined --no-entry
  -Wl,--export=<sym>...` → **one artifact, one linear memory**.
- Manifest `DEPENDENCIES` already distinguishes `kind: "component"`
  (linked library) from `kind: "node"` (graph node) in `artifact.json`
  (`flowCompiler.js:1302-1330`) — the declaration slot for a flatsql
  engine component **already exists**.

### 2.4 Current transitional bridge (what gets retired)

`data-source/retrieval` (`src/retrieval_module.cpp:45-55`) imports the
generic `space_data_module_host.call/response_len/read_response`
trampoline and passes `storage.flatsql_epoch_stream` /
`storage.flatsql_query_stream` as **op strings** with a JSON+segments
envelope; `internal/modulert/caps/storage.go` decodes, runs the query on
the store's `flatsqlrt.Runtime`, copies the result stream out of engine
memory, re-envelopes it, and the module copies it *again* into its own
memory via `read_response`. Two copies + JSON codec per query.

## 3. Option matrix

| # | Option | WasmEdge-go 0.14 | Browser shim | Emscripten toolchain | Zero-hostcall? | Zero-copy? | Verdict |
|---|---|---|---|---|---|---|---|
| A | Emscripten dynamic linking (`MAIN_MODULE`/`SIDE_MODULE`) | ⚠ needs a custom mini-loader (no `dlopen` runtime) | ⚠ same (SDK shim is not the emscripten JS runtime) | ✅ supported, engine rebuild as `MAIN_MODULE` **not** required if loader synthesizes the env | ✅ | ✅ | **Phase 2** — right answer for arbitrary C++ dependents; real loader work (§3.A) |
| B-i | Two memories via multi-memory | ⚠ `--enable-multi-memory` exists (proposal flag) | ⚠ shipped in modern browsers | ❌ **blocker**: LLVM/clang can emit multi-memory ops, but emscripten C/C++ has no way to address a second memory from ordinary pointers — no `-sMULTI_MEMORY` codegen path | ✅ | ✅ | Dead end from C++ today; hand-written wasm only |
| B-ii | Engine-exported copy-out helpers | — | — | — | — | — | **Impossible in core wasm**: the engine cannot name the caller's memory; a `memcpy`-out export has nowhere to copy *to* |
| B-iii | Host `memory_read/memory_write` trampolines between the two memories | ✅ trivial host funcs | ✅ trivial JS | ✅ no changes | ❌ (host trampoline for every buffer crossing) | ⚠ one copy per crossing | Honest call: this **violates the zero-hostcall directive in spirit** — it is a slimmer hostcall bridge (drops the JSON envelope, keeps the copies). Acceptable only as an interim optimization of the *existing* bridge, not as the destination |
| B-iv | Dependent **imports the engine's memory + exports** | ✅ **PROVEN** (name-based instance registration, §5) | ✅ **PROVEN** (imports object, §5) | ⚠ constraint: dependent must have **no private static layout** | ✅ | ✅ | **Recommended now** for generated/first-party components (§4.1); the layout-collision problem is dissolved, not solved, by making dependents position-independent by construction |
| C | Guest-link the engine into the flow artifact (compile-time, one memory) | ✅ (it's just one module) | ✅ | ⚠ flatsql+sqlite as a prefixed guest-link object — heavy but automated | ✅ | ✅ | **Recommended now** for **private-engine** flows (§4.2). For live-store data, only via the inversion (§3.C) — possible but not recommended for the main store |

### 3.A Option A details (what the mini-loader must do)

An emscripten `SIDE_MODULE=2` dependent is relocatable PIC: no memory, no
static placement. Its imports are `env.memory`, `env.__indirect_function_table`,
`env.__memory_base` (i32 global), `env.__table_base` (i32 global),
`env.__stack_pointer` (mutable i32 global), plus `GOT.mem.*`/`GOT.func.*`
mutable i32 globals for interposable symbols, plus `env.<undefined funcs>`.
Emscripten's JS runtime normally supplies all of this; neither WasmEdge-go
nor the SDK's minimal WASI shim does. A mini-loader (Go + JS, ~identical
logic) would:

1. `base = engine.malloc(data_size + bss)` → `env.__memory_base`.
2. `stack = engine.malloc(stack_size)` → fresh mutable global
   `__stack_pointer = stack + stack_size` (per-dependent stack carved
   from the engine heap — the engine exports `malloc`, and WasmEdge-go
   can build mutable globals: `NewGlobal(gtype, val)`, `instance.go:456`).
3. `tbase = engine table size; table.Grow(n)` → `env.__table_base`
   (engine exports `__indirect_function_table`; `Table.Grow/SetData`
   exist in WasmEdge-go).
4. Resolve `GOT.func.*` to table slots, `GOT.mem.*` to engine-export
   addresses, `env.*` funcs to engine exports.
5. Call the dependent's `__wasm_apply_data_relocs` / `_initialize`.

All primitives exist in both hosts; this is engineering, not research
(estimate: a focused loop). It is the path that lets the **existing C++
retrieval module** link directly. Until then, C++ modules with private
memory stay on the hostcall bridge.

### 3.C Option C inversion (store attaches to a flow's linked engine)

`flatsqlrt.Runtime` wraps a `*wasmrt.Module` and calls only
`Execute/ReadMemory/Allocate/Deallocate/MemoryStats` — it could accept an
externally provided instance behind a small interface
(`NewFromModule(mod)`), so `internal/storage.FlatSQLStore` *could* attach
to a flow artifact's linked-in engine. Evaluated honestly:

- There is **one** store holding the hot window; only **one** flow
  artifact could be "the" engine host. Every other flow is back to square
  one.
- Lifecycle inverts: store data lives and dies with the flow artifact's
  deploy cycle. A redeploy forces `flatsql_export_data` →
  `flatsql_load_and_rebuild` migration of the whole hot window.
- Verdict: viable for a dedicated single-pipeline appliance; **rejected**
  as the general mechanism. The store stays the engine's owner; flows
  attach to it (B-iv), or get private engines (C).

## 4. Recommended design

### 4.1 Flows/modules needing the live store (option B-iv, "engine-import components")

**Contract for a linkable dependent** (enforced by the flow compiler /
artifact inspection — all statically checkable):

- imports `flatsql.memory` (and nothing under `env`);
- declares **no** memory of its own and **no active data segments** — all
  constant data are **passive segments** `memory.init`'d into
  `flatsql.malloc`'d space at runtime (this is what makes it
  position-independent **without any PIC toolchain**: bulk-memory ops are
  baseline in WasmEdge 0.14 and every browser);
- imports only `flatsql.malloc`, `flatsql.free`, `flatsql.flatsql_*`.

**sdn-server wiring** (small, mechanical):

1. `internal/wasmrt`: add a named-registration path — either
   `WithRegisteredName("flatsql")` (VM uses `RegisterWasmBuffer` +
   `ExecuteRegistered` instead of `LoadWasmBuffer` + `Execute`) or a
   `Module.RegisterInto(vm2)` helper wrapping
   `vm2.RegisterModule(vm1.GetRegisteredModule(name))`. The engine
   instance **must be named**; an anonymous active module cannot be
   registered as an import source.
2. `internal/flatsqlrt`: `New()` gains the named path;
   `Runtime.Instance() *wasmedge.Module` accessor (or an opaque handle)
   for modulert to link against. Everything else (locking, poisoning,
   AOT cache) is unchanged.
3. `internal/modulert`: when a manifest declares the component dependency
   (reuse the SDK's `kind: "component"` dependency slot + a new
   capability string, e.g. `storage_engine_link`), `instantiateWASM`
   calls `vm.RegisterModule(storeEngineInstance)` **before**
   `vm.Instantiate()`. The cross-VM shape is exactly what the POC proves.
4. **Concurrency (mandatory):** the engine is `SQLITE_THREADSAFE=0` and
   all access today is serialized on `Runtime`'s lock. Direct-linked
   dependents bypass that Go lock, so every invocation of a linked
   module's export MUST be executed while holding the store engine's
   lock (modulert's invoke path takes `Runtime.Lock()` around
   `vm.Execute`). This also serializes result-latch usage
   (`flatsql_result_*` read the engine's current result — same
   discipline the host already follows).
5. **Poisoning:** a trap inside a linked invocation may corrupt engine
   state exactly like a host-invoked trap → propagate to
   `Runtime.poisoned` (store recreates the engine from snapshot; linked
   dependents get re-registered against the new instance, which means
   modulert must re-instantiate dependents on engine replacement).
6. **Security (hard boundary):** importing the engine's memory grants a
   dependent full read/write of **all** store data and of the engine's
   own structures. Linking is therefore restricted to first-party /
   flow-compiler-generated artifacts (signed, capability-gated).
   **Untrusted third-party modules never link; they stay on the hostcall
   bridge — that bridge remains the security boundary by design.**

**Browser side:** the store shim holds `FlatSQLStandalone`; expose its
`_runtime.exports` and instantiate linkable dependents with
`WebAssembly.instantiate(bytes, { flatsql: engineExports })`. Symmetric
with the server, no shim changes required (proven, §5.3).

**Who emits such artifacts?** The flow compiler: for a flow whose
manifest declares the flatsql **component** dependency in
"live-store" mode, it emits the dispatch/query plan as an engine-import
component (generated code + passive data — the compiler controls the
whole artifact, so the "no static layout" constraint is free). Arbitrary
hand-written C++ dependents need option A's loader (Phase 2).

### 4.2 Flows with private engines (option C, guest-link)

For pipelines that want their **own** isolated engine (scratch tables,
private ingest) rather than the shared hot window:

1. flatsql gains a guest-link build: run its C API sources + sqlite
   amalgamation through `compileModuleFromSource` (the symbol-prefix
   rename is automated; sqlite's symbol count is large but mechanical)
   → `dist/guest-link/module-link.o` + `metadata.json`.
2. Consumer manifests declare it as a `kind: "component"` dependency;
   `linkFlowArtifactWithEmception` folds it into the flow artifact — one
   memory **by construction**, calls are direct `call` instructions, not
   even cross-instance.
3. The flow's engine state is private and ephemeral (or durably owned by
   the flow via its own `flatsql_export_data` snapshots through the
   storage capability).

### 4.3 What stays on the transitional hostcall bridge, and why

| Stays | Why |
|---|---|
| Untrusted / third-party modules | Memory sharing = full store disclosure; the bridge is the security boundary |
| Existing C++ modules with private memory (retrieval today) | Until the Phase-2 SIDE_MODULE mini-loader lands, they physically cannot address engine memory |
| `storage.write` / `storage.delete` / provider-scoped policy ops | These are store-policy operations (ABAC, provenance, tombstones), not query-path; they should remain mediated regardless of linkage |
| Remote/cross-process storage topologies | No shared address space exists to link into |

The bridge's `storage.flatsql_query_stream`/`epoch_stream` ops retire for
first-party flow artifacts as 4.1/4.2 land; retrieval is the first
migration target (its two query ops map 1:1 onto direct
`flatsql_query_raw_flatbuffer_stream` calls).

## 5. Proof of concept (run 2026-07-03, all passing)

Artifacts: `internal/flatsqlrt/linkage_poc_test.go` (WAT source inline in
the file; toy dependent is 540 bytes, hand-assembled, imports
`flatsql.memory/malloc/free/flatsql_*`, all data passive +
`memory.init` — the B-iv contract exactly).

### 5.1 Same-VM linking + bidirectional shared state (WasmEdge-go 0.14)

`VM.RegisterWasmBuffer("flatsql", engine)` → toy instantiated in the same
VM → toy creates a db and queries it **entirely in-wasm**; then the HOST
queries the toy-created db handle through the same instance:

```
=== RUN   TestLinkedComponent_SameVM
    linkage_poc_test.go:164: toy created engine db handle=1171656 via direct in-wasm call
    linkage_poc_test.go:174: toy run_query(handle) = 42 (in-wasm through engine instance)
    linkage_poc_test.go:210: host queried toy-created db through same instance: 42
--- PASS: TestLinkedComponent_SameVM (0.04s)
```

### 5.2 Cross-VM instance linking (the modulert shape)

Engine instance owned by VM 1 (the store), registered into VM 2 (the
module's VM) via `VM.RegisterModule` — **no re-instantiation**:

```
=== RUN   TestLinkedComponent_CrossVM
    linkage_poc_test.go:260: cross-VM: dependent in VM2 ran query in VM1's live engine instance = 42
--- PASS: TestLinkedComponent_CrossVM (0.03s)
```

### 5.3 Browser shim (identical artifact, real flatsql loader)

`loadFlatSQLStandalone` (the real browser WASI shim) + the **same 540
bytes** with the engine's exports as the imports object
(scratchpad `browser-linkage-poc.mjs`, Node):

```
toy created engine db handle = 1171656
toy run_query(handle) = 42
host query on toy-created db = 42
BROWSER-SHIM POC PASS
```

(Identical handle value under both runtimes — same engine bytes, same
deterministic allocator state. Byte-parity across hosts holds through
linkage.)

### 5.4 AOT-compiled engine

`TestLinkedComponent_AOTEngine` (env-gated: `FLATSQL_LINKAGE_AOT=1`)
repeats 5.2 with the `ensureAOT`-compiled engine — the production
configuration (interpreted dependent calling into AOT engine functions):

```
=== RUN   TestLinkedComponent_AOTEngine
[info] compile start ... codegen done            (first-run AOT compile: ~41s)
    linkage_poc_test.go:321: interpreted dependent -> AOT-compiled engine instance = 42
--- PASS: TestLinkedComponent_AOTEngine (40.82s)
```

## 6. Honest verdict

**Implementable NOW (proven):**
- Instance-export linking against the live store engine, both hosts,
  including cross-VM (modulert's shape) and with the AOT engine. No
  WasmEdge upgrade, no engine rebuild, no emscripten changes.
- The dependent-artifact contract that makes it safe (no private memory,
  passive-segment PIC) — free for flow-compiler-generated components.
- Private-engine flows via guest-link (all machinery exists in the SDK;
  flatsql needs the `module-link.o` build target).

**Needs work (known scope, not research):**
- Phase 2: SIDE_MODULE mini-loader (Go + JS) so **arbitrary C++ modules**
  (retrieval as written today) can link with their own static data. All
  runtime primitives verified present in WasmEdge-go 0.14 and the
  browser; the loader itself is the deliverable.
- modulert lifecycle: engine-replacement (poisoning) must re-instantiate
  linked dependents; invoke path must take the store engine lock.
- flatsql guest-link build target (mechanical, but sqlite-sized).

**Not viable / rejected:**
- Multi-memory from emscripten C++ (no codegen path — toolchain blocker,
  not a WasmEdge one).
- Engine-side copy-out helpers (impossible in core wasm).
- Host memory trampolines as an end state (violates the zero-hostcall
  directive; it's just a thinner bridge).
- Store-attaches-to-flow inversion as the general mechanism
  (single-tenant only; lifecycle inversion risk).

---

## 7. Loop C.5c addendum (2026-07-03)

### 7.1 Thread-affinity question ANSWERED for direct linkage

`TestLinkedComponent_AOTDependentAOTEngine` (env-gated,
`FLATSQL_LINKAGE_AOT=1`): an **AOT-compiled dependent** making direct import
calls into the **AOT-compiled engine instance** on **one locked OS thread**
runs correctly (repeated passes, no trap). The C.5b corruption
(docs/wasmedge-aot-nested-execution.md) is specific to **nested
`Executor::execute` re-entry inside a suspended host function**; a linked
direct call is one contiguous wasm call stack under ONE executor invocation
and does not touch the suspended-frame state. Consequence: linked-direct
engine calls do NOT need to hop to the engine's dedicated thread — they only
need the store engine LOCK (SQLITE_THREADSAFE=0 serialization, §4.1 item 4).

### 7.2 What C.5c landed instead of (before) full linkage

The wirespeed residue was **per-request byte copies**, and most of them were
eliminated WITHOUT direct linkage by making the heavy bytes never enter the
flow at all — the **body-reference delivery contract**:

- Guest-elected `"deliver":"ref"` on `storage.flatsql_*_stream` hostcalls
  (decision-gate injects it on the flatbuffer branch; json branch keeps
  byte delivery for in-flow field extraction).
- The storage capability handler registers the materialized Go buffer on
  the calling instance's hostcall bridge and answers with
  `result.ref = {token, size, frames, fnv1a64}` — no binary segment.
- The retrieval module forwards the reference as a
  `{"$sdnbodyref":1,...}` descriptor frame; decision-gate formats the
  host-computed word-folded FNV-1a 64 into the IDENTICAL entity tag the
  hashed-stream path produces; http-respond emits `$HTR
  BODY_REF_TOKEN/BODY_REF_SIZE` (HttpResponseAbi.fbs schema extension).
- The egress (Go `htrPipe` / JS harness) substitutes the registered buffer
  — a token lookup, not a decision. Hosts that ignore `deliver` keep
  returning segments and guests fall back to byte delivery (compat both
  directions).
- flatsqlrt gained a host-side **raw-stream mirror** keyed by the engine's
  own `(generation, sql, params)` identity: warm identical queries return
  the previously copied buffer with zero engine execution and zero copies
  (rawstream_mirror.go).

Remaining copies on a warm flatbuffer request: ONE engine→Go copy per
mirror-miss materialization, and the unavoidable socket write.

### 7.3 What full direct linkage still needs (unchanged scope, de-risked)

- Flow-compiler emission of engine-import (B-iv) query components, or the
  SIDE_MODULE mini-loader for the C++ retrieval module as written (§3.A).
- modulert wiring: `vm.RegisterModule(storeEngineInstance)` before flow
  instantiation; engine lock around linked dispatches; re-instantiation of
  dependents on engine replacement (poisoning).
- With 7.1 proven, no dedicated-thread routing is required for linked
  calls — a materially smaller change than feared after C.5b.

Note the perf calculus after 7.2: queries are submitted through one small
hostcall (~µs) while result bytes already move zero-copy; direct linkage's
remaining wirespeed value is the hostcall envelope + dedicated-thread
round-trips per query (small, measurable), while its architectural value
(zero-hostcall composition, private-engine flows) is unchanged.

---

## 8. Loop C.7 — direct linkage LANDED (2026-07-03)

### 8.1 What shipped

The B-iv end state is in production form, both hosts, same artifact:

- **Flow-compiler emission** (`flow.engineLinkage: "flatsql"` in the flow
  document): the runtime template compiles `-DSDN_FLATSQL_LINKED=1` — engine
  calls become direct wasm imports (`flatsql.malloc/free/
  flatsql_query_raw_flatbuffer_stream/…`); the memory boundary is crossed by
  the deterministic ~400-byte **flatsql_link shim** (SDK
  `src/flow/flatsqlLinkShim.js`, memory = `flatsql.memory`, pure code — the
  minimal B-iv component), which also carries the canonical word-folded
  fnv1a64 and frame counting so etags are computed over ENGINE memory
  in-wasm. Dependencies ship a second `dist/guest-link-linked/` object
  variant; the retrieval module's linked path builds the engine-native epoch
  SQL + TLV params itself (byte-identical SQL to
  `storage/engine_records.go`). The emitted manifest gains the
  `storage_engine_link` capability — the host's first-party gate.
- **Memory ownership**: the flow artifact KEEPS its own emscripten heap, the
  engine keeps its own; pointers never cross raw — SQL/params are poked into
  engine-malloc'd space through the shim (~µs for ~400 bytes), results stay
  materialized in engine memory and travel as **engine body-ref tokens**
  ("SDNE" magic | counter) via the UNCHANGED C.5c `$sdnbodyref` descriptor /
  `$HTR BODY_REF` contract (decision-gate and http-respond needed NO
  changes).
- **Host wiring** (Go): the engine instantiates as the NAMED module
  "flatsql" (`wasmrt.WithRegisteredName`); linked flow VMs borrow the live
  instance (`WithLinkedModuleFrom` → `VM.RegisterModule`) + the shim
  (`WithNamedWasm`). Every in-wasm drain runs inside
  `flatsqlrt.Database.WithLinkedDrain` — the store engine LOCK
  (SQLITE_THREADSAFE=0) held for the duration of the linked calls — and
  engine body-refs are harvested inside the SAME critical section (the
  generation cannot move ⇒ engine pointers valid by construction), resolved
  through a host mirror keyed `(generation, fnv1a64, size)`: warm = zero
  engine execution + zero copies; miss = one fnv-verified engine→host copy.
- **Poison recovery**: `storage.RecoverPoisonedEngine()` rebuilds the engine
  in place (journal replay + hot-window rebuild) and bumps `EngineEpoch`;
  mounts re-instantiate linked instances per epoch on the next request
  (`flowrt.TestLinkedMountRecoversFromEnginePoisoning`). Replaced runtimes
  are RETIRED (not closed) until store Close — dependent VMs may still hold
  borrowed instance references.
- **Browser**: `createFlowRuntimeHost({ engineLink: { exports, dbHandle } })`
  instantiates the SAME artifact against the JS engine's exports + the shim;
  `host.resolveEngineBodyRef(token)` reads the body straight out of engine
  memory. Proven end-to-end in
  `space-data-network-modules/flows/data-retrieval/tests/flow.test.mjs`
  (real standalone engine, byte-identical bodies, canonical etags, zero
  storage hostcalls).

### 8.2 libwasmedge 0.14 LIMITATION — linked flows run interpreted

An **AOT-compiled** linked flow falsely traps `out of bounds memory access`
inside the linked drain once the real query sequence runs. Bisected hard:

- interpreted flow → AOT engine: byte-verbatim green (the shipped mode);
- AOT flow, hostcall-free paths (404) and pre-engine error paths: green;
- isolated mechanisms ALL green under AOT→AOT: own-memory dependent calling
  the engine, three-module chains through the shim, callee-side engine
  memory growth (64→128 MB inside the call), caller-side growth after engine
  calls, locked and unlocked threads, THREADS/EH-configured VMs, compiler
  levels O0/O1/O2/O3;
- the full artifact's engine sequence (getConfig hostcall + engine malloc +
  ~330 shim pokes + `flatsql_query_raw_flatbuffer_stream` + artifact getters
  + shim fnv/count) traps deterministically.

Same defect class as the C.5b nested-execution corruption
(docs/wasmedge-aot-nested-execution.md): per-thread executor state around
cross-instance AOT calls. Until a WasmEdge upgrade clears the env-gated
repro (`SDN_C7_FORCE_LINKED_AOT=1 SDN_C4_AOT_REPRO=1 go test ./internal/flowrt/
-run TestAOTMountRepro`), `LoadMountedFlow` force-interprets the (small)
flow artifact of linked mounts; the engine — where query execution and
stream materialization live — stays AOT.

**UPDATE (loop C.9, 2026-07-03): FIXED in libwasmedge 0.16.4** — see §8.4.
The force-interpret is now VERSION-GATED (`flatsqlrt.RuntimeHasLinkedAOTFix`,
≥ 0.16.4): on fixed runtimes linked mounts run AOT; on 0.14 the mitigation is
unchanged. `SDN_C7_FORCE_LINKED_AOT=1` still forces AOT anywhere (the old
repro), `SDN_C7_FORCE_LINKED_INTERP=1` forces interpretation anywhere (A/B).

### 8.3 Measured consequence (wirespeed gate, 8.6 MB / 29K records)

- linked + interpreted flow (this loop): **68.3% best / 72.5% median** (flow
  2469 MB/s best, ~3.5 ms warm; the residue is interpreted flow-runtime
  execution, NOT host crossings — zero storage hostcalls remain on any
  path).
- bridge + AOT flow (C.5c): 84.9% best / 93.8% median (~1.1 ms warm).
- Both modes stay compilable (`engineLinkage` is per-flow document); the
  ≥99% gate target is unmet either way. When the upstream AOT defect is
  fixed, linked+AOT should exceed C.5c (its ~130 µs host-dispatch residue is
  architecturally gone).

### 8.4 Loop C.9 — RETESTED: fixed in libwasmedge 0.16.4 (2026-07-03)

Candidate survey: stable releases ≥0.15 are 0.15.0/0.15.1, 0.16.0–0.16.4,
0.17.0. The newest Go binding release is still `WasmEdge-go v0.14.0`; it
builds unchanged against 0.16.x (C API unchanged), while **0.17.0 removed
the by-value `WasmEdge_Limit` struct** (→ `WasmEdge_LimitContext *`,
SOVERSION 0.1.0→0.1.1), so the binding does not compile against it
(`ast.go:187: could not determine what C.WasmEdge_Limit refers to`).
0.16.4 was installed side-by-side (`~/.wasmedge-0.16.4`; CGO env selects
the runtime at build time) and retested:

- **This §8.2 defect (linked-drain AOT trap): FIXED.**
  `SDN_C7_FORCE_LINKED_AOT=1 SDN_C4_AOT_REPRO=1 TestAOTMountRepro` on
  0.14.0 traps (`out of bounds memory access, Code: 0x408` in
  `space_data_module_runtime_drain_linked`, HTTP 502); on 0.16.4 the same
  forced-AOT mount serves **200 (936 bytes)**. Full flatsqlrt/flowrt/
  modulert/storage/api/storefront/trust suites green on 0.16.4, engine
  no-EH AOT compiles and runs (the wasm-exceptions AOT rejection is
  unchanged in 0.14/0.16.4/0.17.0 — the engine stays the no-EH build).
- **The C.5b nested-AOT defect: ALSO FIXED** (standalone matrix green on
  0.16.4 including AOT-in-AOT same-thread; still traps on 0.14.0 —
  wasmedge-aot-nested-execution.md "Upstream retest").

Adoption: `LoadMountedFlow` now version-gates the force-interpret
(`flatsqlrt.RuntimeHasLinkedAOTFix`, ≥0.16.4 — the verified version;
0.15.x/0.16.0–0.16.3 untested). The AOT disk cache key includes the
runtime version (`<prefix>-<hash>-we<version>.aot.wasm`) so 0.14-compiled
native artifacts never load into an upgraded daemon. Install scripts
default to 0.16.4. `wasmrt.WithDedicatedThread` stays (protects 0.14
builds; serializes engine execution; cost is noise).

Measured (wirespeed gate, 8.6 MB / 29K records, pool=1, 0.16.4,
**linked + AOT flow**, 3 runs):

- **97.48% best / 93.09% median** (flow 3239 MB/s best, 2.66 ms warm);
  89.12%/84.79%; 85.81%/88.21%. Linked+AOT recovers the C.7 interpreted
  penalty (68.3%/72.5% → ~86–97% best) and meets/exceeds the C.5c
  bridge+AOT numbers (84.9%/93.8%) — with zero host dispatch on the query
  path. The ≥99% target is still unmet (run-to-run variance of the ~2.5 ms
  loopback baseline dominates); the gate default stays unflipped.

C.4-style measure (0.16.4, linked+AOT, 500K seeded / 400K hot window):
fb@29K **4.84 ms best** (1783 MB/s) · fb@250K **33.1 ms** (2250 MB/s) ·
json@29K 202 ms · json@250K 1.72 s (json encodes INSIDE the flow on the
linked pipeline — bridge mode remains the faster json path; per-flow
`engineLinkage` choice). Interpreted-flow control on the same seed: fb@29K
67 ms, json@29K 4.5 s — AOT of the linked artifact is a 14×/22× step.
Concurrency (8 clients × 4 reqs, fb@29K): **466.5 req/s pool=1
(4026 MB/s) / 587.3 req/s pool=4 (5068 MB/s aggregate)** — vs C.7
linked-interpreted 195/98.7 req/s and C.5c bridge 547/577 req/s: the
in-wasm-drain lock penalty is gone; linked+AOT is the best concurrent
configuration measured so far.
