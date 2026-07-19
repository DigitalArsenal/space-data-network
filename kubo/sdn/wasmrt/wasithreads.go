package wasmrt

// wasi-threads host support.
//
// WHY THIS EXISTS
// ---------------
// Modules compiled for the wasm32-wasip1-threads target (wasi-sdk / clang
// `-pthread`) spawn threads by IMPORTING `wasi.thread-spawn` and EXPORTING
// `wasi_thread_start`, over a SHARED linear memory imported as `env.memory`.
// WasmEdge 0.14.1 implements the WebAssembly *threads proposal* (atomics +
// shared memory) but ships NO wasi-threads host module — there is no
// `wasi.thread-spawn` anywhere in WasmEdge (verified: absent from 0.14.x,
// 0.15.x, and master). So a wasi-threads module INSTANTIATES fine under the
// node's WasmEdge but TRAPS the moment the guest calls pthread_create, because
// the `wasi.thread-spawn` import is unresolved.
//
// This file supplies that missing runtime primitive as a GENERIC host function,
// exactly the way wasmtime's wasi-threads and Node's `wasi.thread-spawn` harness
// do. It is a runtime capability (like WASI itself): it knows nothing about OD,
// providers, records, or batching. It only knows the wasi-threads ABI.
//
// HOW IT WORKS (mirrors wasmtime / the WASI-threads spec)
// -------------------------------------------------------
//   - ONE shared linear-memory instance is created host-side and exported to
//     the guest as its imported `env.memory`. Every thread instance imports
//     THIS SAME memory, so all threads share one address space (the whole point
//     of pthreads). Shared memories are pre-reserved to their max — they never
//     move — so cross-thread pointers stay valid.
//   - Host modules (env+memory, wasi.thread-spawn, wasi_snapshot_preview1, and
//     any caller-supplied host funcs) are registered ONCE into a single Store.
//     After setup the Store is READ-ONLY; import resolution during instantiation
//     only reads it. This is the model the upstream WasmEdge C-API threads
//     example proves thread-safe (multiple OS threads sharing one store + one
//     imported memory).
//   - On `wasi.thread-spawn(start_arg)` the host allocates a positive, unique
//     tid, then on a FRESH, OS-locked goroutine instantiates a NEW module
//     instance from the SAME cached AST (giving that thread its own mutable
//     globals — its own `__stack_pointer`/TLS — while sharing the imported
//     memory) and invokes `wasi_thread_start(tid, start_arg)`. It returns the
//     tid to the guest.
//
// ONE shared Executor, not a per-thread executor. WasmEdge 0.14.1 keeps the
// atomic wait/notify queue (`WaiterMap`) PER-EXECUTOR: a `memory.atomic.wait`
// parked via one executor is only woken by a `memory.atomic.notify` on that
// SAME executor. So `pthread_join` (the joiner waits; the exiting child
// notifies) and every futex the guest builds on wait/notify would DEADLOCK if
// each thread had its own executor. All thread instances therefore run on the
// one shared `mainExec` — the same model the upstream WasmEdge C-API threads
// example uses (multiple OS threads sharing one executor + one imported
// memory). Concurrent use from several OS threads is safe because WasmEdge's
// per-execution state is thread-local; the only shared mutable state is the
// mutex-guarded WaiterMap and the shared Memory (built for concurrent atomics).
//
// Instantiation runs the module's start function (`__wasm_init_memory`), i.e.
// it EXECUTES wasm. It runs on the worker's OWN OS thread, never nested inside
// the spawning thread's in-flight (possibly AOT) execution — the spawning
// thread parks on a channel until the worker reports the instantiation result,
// so no two wasm executions ever overlap on one OS thread (avoids the
// libwasmedge < 0.16.4 nested-AOT executor-state defect; see runtime.go
// WithDedicatedThread).

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// The fixed wasi-threads ABI names (not module-specific — these are the spec).
const (
	wasiThreadsImportModule = "wasi"
	wasiThreadSpawnFunc     = "thread-spawn"
	wasiThreadStartExport   = "wasi_thread_start"
)

// WithWASIThreads enables the wasi-threads runtime primitive for this module:
// the host resolves the guest's `wasi.thread-spawn` import and its shared
// `env.memory` import, and actually spawns OS threads that each run
// `wasi_thread_start` on their own instance over the one shared memory.
//
// It implies WASI (a wasi-threads module always imports wasi_snapshot_preview1)
// and dedicated-thread execution (the main guest call, which spawns the
// workers, runs on one locked OS thread). It is mutually exclusive with the
// VM-linkage options (WithRegisteredName / WithLinkedModuleFrom / WithNamedWasm),
// which drive the high-level VM path; a wasi-threads module is built on the
// low-level Loader/Store/Executor API so the host can share one Memory instance
// across per-thread instances.
func WithWASIThreads() Option {
	return func(c *config) {
		c.wasiThreads = true
		c.enableWASI = true
		c.dedicatedThread = true
	}
}

// threadsBackend is the low-level (VM-free) execution backend for a
// wasi-threads module. A Module has exactly one of {vm, threads}.
type threadsBackend struct {
	conf   *wasmedge.Configure
	loader *wasmedge.Loader
	ast    *wasmedge.AST
	store  *wasmedge.Store

	// sharedMem is the single shared linear memory, owned by the env host
	// module, imported by every instance as `env.memory`. Held here (non-owning)
	// for host<->guest memory access.
	sharedMem *wasmedge.Memory

	// Host module instances registered into the store (owned; released on
	// teardown). envMod owns sharedMem.
	hostMods []*wasmedge.Module
	wasiMod  *wasmedge.Module

	// Main-thread execution context.
	mainExec  *wasmedge.Executor
	mainStats *wasmedge.Statistics
	mainInst  *wasmedge.Module

	// tid allocation: positive, unique, monotonic (1, 2, 3, ...).
	nextTID int32

	// Observability (proof + benchmarking): counts and the distinct kernel
	// threads workers ran on. Guarded by obsMu.
	obsMu     sync.Mutex
	spawns    int
	liveNow   int
	peakLive  int
	osTIDs    []int64
	firstErr  error
	workersWG sync.WaitGroup

	// instMu serializes instantiation into the shared store (defensive: keeps
	// at most one Executor mutating instance-local allocation at a time).
	instMu sync.Mutex
}

// newThreadsModule builds a Module backed by the wasi-threads low-level path.
func newThreadsModule(wasmBytes []byte, cfg *config) (*Module, error) {
	conf := wasmedge.NewConfigure()
	conf.AddConfig(wasmedge.THREADS)
	conf.AddConfig(wasmedge.EXCEPTION_HANDLING)

	tb := &threadsBackend{conf: conf}

	fail := func(err error) (*Module, error) {
		tb.release()
		return nil, err
	}

	tb.loader = wasmedge.NewLoaderWithConfig(conf)
	if tb.loader == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create loader"))
	}
	ast, err := tb.loader.LoadBuffer(wasmBytes)
	if err != nil {
		return fail(fmt.Errorf("wasi-threads: load wasm: %w", err))
	}
	tb.ast = ast

	// The low-level Executor requires a validated AST (the VM path validates
	// implicitly). Validate once; the AST is reused for every thread instance.
	validator := wasmedge.NewValidatorWithConfig(conf)
	if validator == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create validator"))
	}
	if err := validator.Validate(ast); err != nil {
		validator.Release()
		return fail(fmt.Errorf("wasi-threads: validate wasm: %w", err))
	}
	validator.Release()

	// Discover the shared-memory import (module+field+limits) from the AST so
	// the host memory we create matches the guest's declared import exactly.
	memModName, memFieldName, memMin, memMax, memShared, ok := findMemoryImport(ast)
	if !ok {
		return fail(fmt.Errorf("wasi-threads: module declares no imported memory (needs env.memory; build with -Wl,--import-memory,--shared-memory)"))
	}
	if !memShared {
		return fail(fmt.Errorf("wasi-threads: imported memory %s.%s is not shared (build with -Wl,--shared-memory)", memModName, memFieldName))
	}
	// Allow the shared memory to reach its declared max.
	if memMax > 0 {
		conf.SetMaxMemoryPage(uint(memMax))
	}

	// Create the single shared memory instance.
	memLimit := wasmedge.NewLimitSharedWithMax(uint(memMin), uint(memMax))
	if memLimit == nil {
		return fail(fmt.Errorf("wasi-threads: invalid shared memory limits min=%d max=%d", memMin, memMax))
	}
	memType := wasmedge.NewMemoryType(memLimit)
	if memType == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create memory type"))
	}
	sharedMem := wasmedge.NewMemory(memType)
	memType.Release()
	if sharedMem == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create shared memory instance"))
	}

	// Build the caller's host modules; merge the shared memory into the module
	// named by the memory import (typically "env"). Create it if absent.
	var memHostMod *wasmedge.Module
	for _, spec := range cfg.hostModules {
		hm := newHostModule(spec)
		if hm == nil {
			sharedMem.Release()
			return fail(fmt.Errorf("wasi-threads: failed to create host module %q", spec.name))
		}
		tb.hostMods = append(tb.hostMods, hm)
		if spec.name == memModName {
			memHostMod = hm
		}
	}
	if memHostMod == nil {
		memHostMod = wasmedge.NewModule(memModName)
		if memHostMod == nil {
			sharedMem.Release()
			return fail(fmt.Errorf("wasi-threads: failed to create %q host module", memModName))
		}
		tb.hostMods = append(tb.hostMods, memHostMod)
	}
	memHostMod.AddMemory(memFieldName, sharedMem) // ownership -> memHostMod
	// Re-fetch a non-owning handle for host<->guest memory access.
	tb.sharedMem = memHostMod.FindMemory(memFieldName)
	if tb.sharedMem == nil {
		return fail(fmt.Errorf("wasi-threads: shared memory vanished after AddMemory"))
	}

	// The wasi.thread-spawn host module.
	spawnMod := wasmedge.NewModule(wasiThreadsImportModule)
	if spawnMod == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create %q host module", wasiThreadsImportModule))
	}
	tb.hostMods = append(tb.hostMods, spawnMod)
	{
		i32 := wasmedge.NewValTypeI32()
		ft := wasmedge.NewFunctionType([]*wasmedge.ValType{wasmedge.NewValTypeI32()}, []*wasmedge.ValType{i32})
		spawnFn := wasmedge.NewFunction(ft, tb.threadSpawnHost, nil, 0)
		ft.Release()
		spawnMod.AddFunction(wasiThreadSpawnFunc, spawnFn)
	}

	// The WASI (wasi_snapshot_preview1) host module.
	tb.wasiMod = wasmedge.NewWasiModule(cfg.wasiArgs, cfg.wasiEnvs, cfg.wasiPreopens)
	if tb.wasiMod == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create WASI module"))
	}

	// One shared store; register every host module ONCE. After this the store
	// is read-only for the lifetime of the module.
	tb.store = wasmedge.NewStore()
	if tb.store == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create store"))
	}
	setupExec := wasmedge.NewExecutorWithConfig(conf)
	if setupExec == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create setup executor"))
	}
	defer setupExec.Release()
	if err := setupExec.RegisterImport(tb.store, tb.wasiMod); err != nil {
		return fail(fmt.Errorf("wasi-threads: register WASI: %w", err))
	}
	for _, hm := range tb.hostMods {
		if err := setupExec.RegisterImport(tb.store, hm); err != nil {
			return fail(fmt.Errorf("wasi-threads: register host module %q: %w", hm.GetName(), err))
		}
	}

	// The main instance + its dedicated executor (optionally with statistics
	// for cost-budget enforcement, mirroring the VM path).
	if cfg.costLimit > 0 {
		// Turn on instruction-cost accounting so applyCostBudget's
		// SetCostLimit actually bounds the main guest execution (mirrors the
		// VM path in NewModule). Worker threads run their own executors and are
		// not individually fuel-metered — see the package note on budgeting.
		conf.SetStatisticsCostMeasuring(true)
		tb.mainStats = wasmedge.NewStatistics()
		tb.mainExec = wasmedge.NewExecutorWithConfigAndStatistics(conf, tb.mainStats)
	} else {
		tb.mainExec = wasmedge.NewExecutorWithConfig(conf)
	}
	if tb.mainExec == nil {
		return fail(fmt.Errorf("wasi-threads: failed to create main executor"))
	}
	mainInst, err := tb.mainExec.Instantiate(tb.store, tb.ast)
	if err != nil {
		return fail(fmt.Errorf("wasi-threads: instantiate main: %w", err))
	}
	tb.mainInst = mainInst

	m := &Module{
		mallocName:        cfg.mallocName,
		freeName:          cfg.freeName,
		secureDeallocName: cfg.secureDeallocName,
		dedicatedThread:   cfg.dedicatedThread,
		execTimeout:       cfg.execTimeout,
		costLimit:         cfg.costLimit,
		threads:           tb,
	}
	if cfg.dedicatedThread {
		m.startExecThread()
	}
	return m, nil
}

// newHostModule builds a WasmEdge host module instance from a spec (shared with
// the VM path's registration logic, but returns the instance instead of
// registering it into a VM).
func newHostModule(spec hostModuleSpec) *wasmedge.Module {
	hm := wasmedge.NewModule(spec.name)
	if hm == nil {
		return nil
	}
	for _, hf := range spec.funcs {
		ft := wasmedge.NewFunctionType(hf.Params, hf.Returns)
		fn := wasmedge.NewFunction(ft, hf.Func, nil, hf.Cost)
		ft.Release()
		hm.AddFunction(hf.Name, fn)
	}
	return hm
}

// threadSpawnHost implements the `wasi.thread-spawn` import. It is the ONLY
// module-agnostic thing this file does: allocate a tid, run
// wasi_thread_start(tid, start_arg) on a fresh OS thread over the shared memory,
// return the tid (>0) or a negative errno.
func (tb *threadsBackend) threadSpawnHost(_ interface{}, _ *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
	startArg := params[0].(int32)
	tid := atomic.AddInt32(&tb.nextTID, 1) // 1, 2, 3, ...

	ready := make(chan error, 1)
	tb.workersWG.Add(1)
	go func() {
		defer tb.workersWG.Done()
		// Lock this worker to one OS thread for the whole thread body: WasmEdge's
		// per-execution state is thread-local, so a worker must not migrate OS
		// threads mid-flight, and nothing else may run wasm on this OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// CRITICAL: every thread instance runs on the ONE shared executor
		// (tb.mainExec), never a per-thread executor. WasmEdge 0.14.1 keeps the
		// atomic wait/notify queue (`WaiterMap`) PER-EXECUTOR — a waiter parked
		// via one executor is only woken by a notify on that SAME executor. A
		// per-thread executor would put pthread_join's `memory.atomic.wait` and
		// the exiting child's `memory.atomic.notify` on different queues, so the
		// join would deadlock. One shared executor keeps them on one queue.
		// Concurrent use from several OS threads is safe here because WasmEdge's
		// execution state is thread-local and no two wasm executions ever nest on
		// one OS thread (the spawner parks on `ready` during instantiation).
		tb.instMu.Lock()
		inst, err := tb.mainExec.Instantiate(tb.store, tb.ast)
		tb.instMu.Unlock()
		if err != nil {
			ready <- err
			return
		}
		defer inst.Release()

		startFn := inst.FindFunction(wasiThreadStartExport)
		if startFn == nil {
			ready <- fmt.Errorf("export %q not found", wasiThreadStartExport)
			return
		}

		// Instantiation succeeded — the guest's pthread_create can now proceed.
		ready <- nil

		tb.enterWorker()
		_, invErr := tb.mainExec.Invoke(startFn, tid, startArg)
		tb.leaveWorker(invErr)
	}()

	if err := <-ready; err != nil {
		// wasi-threads: negative return => spawn failed (errno). wasi-libc maps
		// this to a pthread_create failure the guest can handle.
		return []interface{}{int32(-1)}, wasmedge.Result_Success
	}
	return []interface{}{tid}, wasmedge.Result_Success
}

func (tb *threadsBackend) enterWorker() {
	ktid := osThreadID()
	tb.obsMu.Lock()
	tb.spawns++
	tb.liveNow++
	if tb.liveNow > tb.peakLive {
		tb.peakLive = tb.liveNow
	}
	tb.osTIDs = append(tb.osTIDs, ktid)
	tb.obsMu.Unlock()
}

func (tb *threadsBackend) leaveWorker(err error) {
	tb.obsMu.Lock()
	tb.liveNow--
	if err != nil && tb.firstErr == nil {
		tb.firstErr = err
	}
	tb.obsMu.Unlock()
}

// execute runs an exported function on the main instance via the main executor.
func (tb *threadsBackend) execute(name string, params ...interface{}) ([]interface{}, error) {
	fn := tb.mainInst.FindFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("wasm function %q not found", name)
	}
	return tb.mainExec.Invoke(fn, params...)
}

func (tb *threadsBackend) hasFunction(name string) bool {
	for _, n := range tb.mainInst.ListFunction() {
		if n == name {
			return true
		}
	}
	return false
}

func (tb *threadsBackend) memory() (*wasmedge.Memory, error) {
	if tb.sharedMem == nil {
		return nil, ErrMemory
	}
	return tb.sharedMem, nil
}

func (tb *threadsBackend) maxMemoryPages() uint64 {
	if tb.conf == nil {
		return 0
	}
	return uint64(tb.conf.GetMaxMemoryPage())
}

// release tears down all wasi-threads resources, waiting for any outstanding
// worker threads to finish first (they reference the shared store/memory).
func (tb *threadsBackend) release() {
	tb.workersWG.Wait()
	if tb.mainInst != nil {
		tb.mainInst.Release()
		tb.mainInst = nil
	}
	if tb.mainExec != nil {
		tb.mainExec.Release()
		tb.mainExec = nil
	}
	if tb.mainStats != nil {
		tb.mainStats.Release()
		tb.mainStats = nil
	}
	if tb.store != nil {
		tb.store.Release()
		tb.store = nil
	}
	for _, hm := range tb.hostMods {
		if hm != nil {
			hm.Release()
		}
	}
	tb.hostMods = nil
	if tb.wasiMod != nil {
		tb.wasiMod.Release()
		tb.wasiMod = nil
	}
	if tb.ast != nil {
		tb.ast.Release()
		tb.ast = nil
	}
	if tb.loader != nil {
		tb.loader.Release()
		tb.loader = nil
	}
	if tb.conf != nil {
		tb.conf.Release()
		tb.conf = nil
	}
}

// findMemoryImport returns the module name, field name, and limits of the
// module's imported memory (the shared linear memory a wasi-threads module
// imports), or ok=false if the module imports no memory.
func findMemoryImport(ast *wasmedge.AST) (modName, fieldName string, min, max uint32, shared bool, ok bool) {
	for _, imp := range ast.ListImports() {
		if imp.GetExternalType() != wasmedge.ExternType_Memory {
			continue
		}
		mt, isMem := imp.GetExternalValue().(*wasmedge.MemoryType)
		if !isMem {
			continue
		}
		lim := mt.GetLimit()
		return imp.GetModuleName(), imp.GetExternalName(),
			uint32(lim.GetMin()), uint32(lim.GetMax()), lim.IsShared(), true
	}
	return "", "", 0, 0, false, false
}

// --- Observability accessors (generic; used by proof tests + benchmarking) ---

// ThreadSpawnCount reports how many worker OS threads this module has spawned
// via wasi.thread-spawn over its lifetime.
func (m *Module) ThreadSpawnCount() int {
	if m.threads == nil {
		return 0
	}
	m.threads.obsMu.Lock()
	defer m.threads.obsMu.Unlock()
	return m.threads.spawns
}

// PeakConcurrentThreads reports the maximum number of worker threads observed
// running simultaneously — the host-side witness of real parallelism.
func (m *Module) PeakConcurrentThreads() int {
	if m.threads == nil {
		return 0
	}
	m.threads.obsMu.Lock()
	defer m.threads.obsMu.Unlock()
	return m.threads.peakLive
}

// WorkerOSThreadIDs returns the distinct-per-spawn kernel thread ids the workers
// ran on (Linux gettid). Proof that spawned threads are real OS threads.
func (m *Module) WorkerOSThreadIDs() []int64 {
	if m.threads == nil {
		return nil
	}
	m.threads.obsMu.Lock()
	defer m.threads.obsMu.Unlock()
	out := make([]int64, len(m.threads.osTIDs))
	copy(out, m.threads.osTIDs)
	return out
}

// WorkerError returns the first error a worker thread's wasi_thread_start
// invocation reported, if any (nil = all workers ran clean).
func (m *Module) WorkerError() error {
	if m.threads == nil {
		return nil
	}
	m.threads.obsMu.Lock()
	defer m.threads.obsMu.Unlock()
	return m.threads.firstErr
}
