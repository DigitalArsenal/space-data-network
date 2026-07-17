// Package flowrt is the SDN flow runtime: a Go host that drives a compiled
// FLOW WASM artifact — a finite-state machine of module invocations — through
// its runtime ABI. It is ported onto the kubo base as Phase 2c of the kubo
// rebase. "A module is the degenerate flow": a flow whose nodes invoke MODULES
// (sdn/modulert, already ported) runs on this core WITHOUT any storage/data
// plane, because the run path (runtime.go) binds only the FSM export ABI
// (space_data_module_runtime_*) and the WasmEdge host — it never imports
// flatsqlrt/engine_link/storage.
//
// # Ported here (storage-free flow CORE), building/green
//
//   - abi.go        — the flow runtime ABI: export names + descriptor
//     structs (frame/invocation/dispatch/dependency/node+ingress state) and
//     their little-endian (de)serializers.
//   - runtime.go    — FlowRuntime: loads a flow-wasm, caches descriptor
//     counts + per-node static dispatch identities, and Drains its FSM
//     (GetReadyNodeIndex -> BeginInvocation -> dispatch [host handler or
//     in-wasm linked-direct/drain_linked] -> ApplyInvocationResult ->
//     CompleteInvocation). This is the core run path; it does NOT require the
//     deferred data layer (linkedSection is an optional, nil-by-default hook).
//   - store.go      — on-disk installed-flow store (runtime.wasm/flow.plg,
//     the flow graph as a $PLG FlatBuffer).
//   - hostfuncs.go  — the "sdn"/"env" host modules (clock/random/log + the
//     C++ exception stubs).
//   - handlers.go   — Handler/HandlerMap resolution + the frame/result types.
//   - flowplugin.go — FlowPlugin: adapts a FlowRuntime to the plugins.Plugin /
//     CronProvider / UIProvider interfaces (timer -> cron, http -> route).
//   - manager.go    — FlowManager: install/load/register flows as plugins. Its
//     only external-config dependency, config.FlowsConfig, was ported minimal
//     as sdn/flowconfig.FlowsConfig (see below). Its plugins.Manager dependency
//     is satisfied by the minimal Manager added in sdn/plugins/manager_min.go.
//   - api.go        — the /api/v1/flows REST management surface (list / deploy
//     / get / delete / start / stop / status / capabilities).
//
// # config.FlowsConfig trim (sdn/flowconfig)
//
// manager.go's only sdn-server internal/config dependency is the type
// config.FlowsConfig. Rather than port the whole internal/config package, only
// FlowsConfig is copied — into sdn/flowconfig — and only the four fields
// FlowManager actually reads (Enabled, StoragePath, MaxFlows, MaxMemoryPages).
// The EditorEnabled/EditorPath fields and the Mounts []FlowMount / Services
// []FlowService slices were left behind: they feed only the DEFERRED serving
// files and the discarded editor, and porting them would drag in the
// FlowMount/FlowService nested types. See sdn/flowconfig/config.go.
//
// # DEFERRED — do NOT port until the Phase 3 data plane lands
//
//   - httpmount.go   — HTTP serving of mounted flows (config/flatsqlrt/httpabi).
//     Owns the MountedFlow type + RegisterFlowMounts/LoadMountedFlow.
//   - cronmount.go   — cron/timer serving of flow services (config/flatsqlrt).
//   - engine_link.go — the FlatSQL engine link; the data plane belongs to
//     Phase 3. runtime.go's SetLinkedSection hook is where it re-attaches.
//   - capabilities/  — flow capability host services.
//   - editor/        — a UI tool = discarded cruft; not ported.
//
// Deferring these does NOT block running a flow: the flow-run test in this
// package (flow_run_test.go) drives a REAL compiled flow bundle's FSM directly
// through FlowRuntime, with the sdn/modulert hostcall bridge as the only extra
// host import — no storage, no engine link.
package flowrt
