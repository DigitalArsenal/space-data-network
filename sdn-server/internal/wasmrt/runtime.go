// Package wasmrt provides a shared WasmEdge WASM runtime wrapper for SDN.
// It abstracts VM lifecycle, WASI setup, memory management (malloc/free),
// host module registration, and function execution.
package wasmrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

var (
	ErrNoModule    = errors.New("WASM module not loaded")
	ErrAllocFailed = errors.New("WASM memory allocation failed")
	ErrMemory      = errors.New("WASM memory access failed")

	// ErrExecutionTimeout is returned (wrapped) when a guest invocation is
	// aborted because it exceeded its configured wall-clock budget
	// (WithExecTimeout / the ctx deadline passed to ExecuteContext,
	// whichever is shorter). The in-flight execution is hard-interrupted
	// (WasmEdge_AsyncCancel) and reaped before this error is returned, so
	// the runtime is guaranteed usable for the next invocation.
	ErrExecutionTimeout = errors.New("wasm execution exceeded wall-clock timeout")

	// ErrFuelExhausted is returned (wrapped) when a guest invocation is
	// aborted by WasmEdge's instruction-cost limit (WithCostLimit) — the
	// fuel/gas mechanism. WasmEdge aborts the execution itself once the
	// cumulative cost crosses the configured budget; the VM remains usable
	// afterward.
	ErrFuelExhausted = errors.New("wasm execution exceeded instruction/cost fuel budget")
)

// IsResourceLimitExceeded reports whether err (or any error it wraps) is a
// wasmrt resource-limit breach: wall-clock timeout or fuel/cost exhaustion.
func IsResourceLimitExceeded(err error) bool {
	return errors.Is(err, ErrExecutionTimeout) || errors.Is(err, ErrFuelExhausted)
}

// classifyExecError inspects a raw WasmEdge execution error and wraps it in
// the appropriate typed sentinel when it recognizes a resource-limit breach.
// WasmEdge reports these as plain result messages ("cost limit exceeded",
// "execution interrupted") rather than distinguishable Go error types, so
// detection is by substring match against the upstream message text (see
// enum.inc CostLimitExceeded / Interrupted in the WasmEdge C API).
func classifyExecError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "cost limit exceeded"):
		return fmt.Errorf("%w: %v", ErrFuelExhausted, err)
	case strings.Contains(msg, "execution interrupted"):
		return fmt.Errorf("%w: %v", ErrExecutionTimeout, err)
	default:
		return err
	}
}

// HostFunc describes a single host function to register in a host module.
type HostFunc struct {
	Name    string
	Func    func(data interface{}, callframe *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result)
	Params  []*wasmedge.ValType
	Returns []*wasmedge.ValType
	Cost    uint
}

// Option configures a Module during creation.
type Option func(*config)

type config struct {
	maxMemoryPages    uint32
	enableWASI        bool
	wasiArgs          []string
	wasiEnvs          []string
	wasiPreopens      []string
	mallocName        string
	freeName          string
	secureDeallocName string
	hostModules       []hostModuleSpec
	dedicatedThread   bool
	registeredName    string
	linkedModules     []*Module
	namedWasm         []namedWasmSpec

	// execTimeout is the default per-Execute wall-clock budget (0 disables
	// wall-clock enforcement — the pre-B3 behavior). See WithExecTimeout.
	execTimeout time.Duration
	// costLimit is the default per-Execute WasmEdge instruction-cost budget
	// (0 disables fuel enforcement — the pre-B3 behavior). See WithCostLimit.
	costLimit uint
}

type hostModuleSpec struct {
	name  string
	funcs []HostFunc
}

type namedWasmSpec struct {
	name  string
	bytes []byte
}

func WithMaxMemoryPages(pages uint32) Option {
	return func(c *config) { c.maxMemoryPages = pages }
}

// WithExecTimeout sets the default per-Execute wall-clock budget (B3
// defensive hardening). Zero (the default when this option is omitted)
// disables wall-clock enforcement, preserving pre-B3 behavior for existing
// callers that don't opt in.
//
// When set and the module was NOT created WithDedicatedThread, breaches are
// hard-interrupted via WasmEdge's async-execute + cancel mechanism
// (WasmEdge_AsyncCancel) — the guest is stopped mid-instruction and the
// runtime remains usable for the next call. WithDedicatedThread modules
// cannot safely use the async interrupt path (see WithDedicatedThread's
// doc comment on nested-AOT thread affinity), so for those the wall-clock
// budget is enforced best-effort only; WithCostLimit is the mechanism that
// reliably bounds them and should always be set alongside this option for
// dedicated-thread modules.
func WithExecTimeout(d time.Duration) Option {
	return func(c *config) { c.execTimeout = d }
}

// WithCostLimit sets the default per-Execute WasmEdge instruction-cost
// budget (the fuel/gas mechanism; B3 defensive hardening). Zero (the
// default when this option is omitted) disables fuel enforcement,
// preserving pre-B3 behavior for existing callers that don't opt in.
//
// Enabling this turns on WasmEdge's cost-measuring statistics for the VM
// (WasmEdge_ConfigureStatisticsSetCostMeasuring) and, before every Execute
// call, raises the VM's cumulative cost limit by this budget
// (WasmEdge_StatisticsSetCostLimit). WasmEdge aborts the guest itself
// ("cost limit exceeded") once the budget is spent — this works uniformly
// for both dedicated-thread and normal modules, and is the only mechanism
// in this package that reliably bounds a hot loop with no host calls
// regardless of dispatch mode.
func WithCostLimit(limit uint) Option {
	return func(c *config) { c.costLimit = limit }
}

// ExecBudget is a per-CALL resource-budget override, carried on the Go
// context by the HOST for a single Execute/ExecuteContext call. It RAISES (or
// lowers) the module's configured default per-Execute budget for that one
// call, letting a trusted host caller — e.g. a scheduled cron invocation that
// legitimately runs for minutes — grant a larger wall-clock/fuel allowance
// than the tight interactive default WITHOUT weakening the default for every
// other call. A zero field means "use the module's configured default".
//
// This is a host-only knob: Go context values cannot be set by guest WASM, so
// a module can never self-grant a bigger budget through this path. The
// module's WithExecTimeout/WithCostLimit defaults remain the fail-closed
// baseline — a call with no ExecBudget on its context is bounded exactly as
// before. A ctx deadline still narrows the resulting wall-clock budget when
// the deadline is sooner (see effectiveTimeout), so a large scheduled budget
// never defeats an even-sooner request-scoped deadline.
//
// Note: CostLimit only bites if cost accounting was enabled at module creation
// (WithCostLimit > 0); a per-call CostLimit cannot turn fuel metering on for a
// module that was created without it.
type ExecBudget struct {
	// Timeout is the wall-clock budget for the call. 0 = use module default.
	Timeout time.Duration
	// CostLimit is the WasmEdge instruction-cost budget for the call.
	// 0 = use module default.
	CostLimit uint
}

type execBudgetKey struct{}

// WithExecBudget returns a context carrying a per-call ExecBudget override.
// Intended for host callers that know a specific call should run under a
// different budget than the module default (e.g. modulert's scheduled/cron
// seam grants a multi-minute budget; interactive callers leave it unset).
func WithExecBudget(ctx context.Context, b ExecBudget) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, execBudgetKey{}, b)
}

// execBudgetFrom extracts a per-call ExecBudget override from ctx, if the host
// attached one via WithExecBudget.
func execBudgetFrom(ctx context.Context) (ExecBudget, bool) {
	if ctx == nil {
		return ExecBudget{}, false
	}
	b, ok := ctx.Value(execBudgetKey{}).(ExecBudget)
	return b, ok
}

func WithWASI() Option {
	return func(c *config) { c.enableWASI = true }
}

func WithWASIArgs(args []string, envs []string, preopens []string) Option {
	return func(c *config) {
		c.enableWASI = true
		c.wasiArgs = args
		c.wasiEnvs = envs
		c.wasiPreopens = preopens
	}
}

func WithMallocName(name string) Option {
	return func(c *config) { c.mallocName = name }
}

func WithFreeName(name string) Option {
	return func(c *config) { c.freeName = name }
}

func WithSecureDealloc(name string) Option {
	return func(c *config) { c.secureDeallocName = name }
}

func WithHostModule(name string, funcs []HostFunc) Option {
	return func(c *config) {
		c.hostModules = append(c.hostModules, hostModuleSpec{name: name, funcs: funcs})
	}
}

// WithDedicatedThread executes every guest call of this module on one
// dedicated, locked OS thread. REQUIRED for any AOT-compiled module whose
// exports are invoked from inside ANOTHER module's host function (nested
// execution) on libwasmedge < 0.16.4: 0.14 keeps per-thread executor state
// that a nested AOT execution clobbers — when control returns to the
// suspended outer AOT frame on the same thread, its next linear-memory
// access falsely traps "out of bounds memory access" (see
// docs/wasmedge-aot-nested-execution.md, flowrt TestAOTMountRepro). Loop
// C.9 proved the defect FIXED in libwasmedge 0.16.4 (standalone matrix
// green), but the option is KEPT unconditionally: it also serializes this
// module's executions, still protects 0.14 builds, and costs one channel
// round-trip per call — noise next to any engine query.
func WithDedicatedThread() Option {
	return func(c *config) { c.dedicatedThread = true }
}

// WithRegisteredName instantiates the main module as a NAMED module in the
// VM's store (VM.RegisterWasmBuffer) instead of the anonymous active module.
// A named live instance can be shared into other VMs via
// WithLinkedModuleFrom — the direct-linkage mechanism (an anonymous active
// module cannot be registered as an import source; see
// docs/flatsql-component-linkage.md §4.1). Execute/memory access route
// through the registered instance transparently.
func WithRegisteredName(name string) Option {
	return func(c *config) { c.registeredName = name }
}

// WithLinkedModuleFrom registers another Module's LIVE named instance into
// this VM before instantiation (WasmEdge_VMRegisterModuleFromImport — no
// re-instantiation, no copy), so this module's imports of that name resolve
// against the live instance: direct in-wasm calls into its exports and
// memory. The source module must have been created WithRegisteredName. The
// source instance MUST outlive this module (loop C.7: mounts release
// dependent flow instances before the store retires a replaced engine).
func WithLinkedModuleFrom(src *Module) Option {
	return func(c *config) { c.linkedModules = append(c.linkedModules, src) }
}

// WithNamedWasm loads+instantiates additional wasm bytes as a named module
// in the same VM before the main module (e.g. the flatsql_link shim, whose
// imports resolve against previously registered linked modules).
func WithNamedWasm(name string, bytes []byte) Option {
	return func(c *config) { c.namedWasm = append(c.namedWasm, namedWasmSpec{name: name, bytes: bytes}) }
}

// Module wraps a WasmEdge VM with convenience methods for memory management
// and function execution.
type Module struct {
	conf     *wasmedge.Configure
	vm       *wasmedge.VM
	hostMods []*wasmedge.Module
	mu       sync.Mutex

	// registeredName is non-empty when the main module lives in the VM store
	// as a NAMED instance (WithRegisteredName) — required for cross-VM
	// instance linking.
	registeredName string

	mallocName        string
	freeName          string
	secureDeallocName string

	// Dedicated execution thread (WithDedicatedThread): all vm.Execute
	// calls are served by one locked OS thread.
	execCh          chan *execRequest
	execWG          sync.WaitGroup
	dedicatedThread bool

	// Resource limits (B3 defensive hardening). Zero values disable the
	// corresponding enforcement — see WithExecTimeout / WithCostLimit.
	execTimeout time.Duration
	costLimit   uint
}

// execRequest carries one guest call to the dedicated execution thread.
type execRequest struct {
	ctx    context.Context
	name   string
	params []interface{}
	done   chan execResult
}

type execResult struct {
	values []interface{}
	err    error
}

// startExecThread spawns the dedicated, OS-locked execution worker.
func (m *Module) startExecThread() {
	m.execCh = make(chan *execRequest)
	m.execWG.Add(1)
	go func() {
		defer m.execWG.Done()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		for req := range m.execCh {
			values, err := m.executeDirect(req.ctx, req.name, req.params...)
			req.done <- execResult{values: values, err: err}
		}
	}()
}

// runSync invokes an export synchronously on the calling goroutine, routing
// through the named registered instance when the module was created
// WithRegisteredName.
func (m *Module) runSync(name string, params ...interface{}) ([]interface{}, error) {
	if m.registeredName != "" {
		return m.vm.ExecuteRegistered(m.registeredName, name, params...)
	}
	return m.vm.Execute(name, params...)
}

// HasFunction reports whether the instantiated module exports a function with
// the given name.
func (m *Module) HasFunction(name string) bool {
	if m == nil || m.vm == nil {
		return false
	}
	names, _ := m.vm.GetFunctionList()
	for _, exported := range names {
		if exported == name {
			return true
		}
	}
	return false
}

// runAsyncWithTimeout invokes an export via WasmEdge's async-execute path
// and hard-interrupts it (WasmEdge_AsyncCancel) if it has not completed
// within timeout. On breach the in-flight execution is canceled and reaped
// (via Async.GetResult, which blocks until the cancellation has actually
// taken effect) before returning, so the VM is guaranteed idle — and safe
// for the next Execute call — by the time this function returns.
func (m *Module) runAsyncWithTimeout(timeout time.Duration, name string, params ...interface{}) ([]interface{}, error) {
	var async *wasmedge.Async
	if m.registeredName != "" {
		async = m.vm.AsyncExecuteRegistered(m.registeredName, name, params...)
	} else {
		async = m.vm.AsyncExecute(name, params...)
	}
	if async == nil {
		return nil, fmt.Errorf("failed to start async execution of %q", name)
	}
	defer async.Release()

	ms := timeout.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	if async.WaitFor(int(ms)) {
		values, err := async.GetResult()
		return values, classifyExecError(err)
	}

	// Hard interrupt: cancel, then reap so the cancellation has fully taken
	// effect (and any WasmEdge-internal execution thread has been joined)
	// before we hand control back to the caller.
	async.Cancel()
	_, _ = async.GetResult()
	return nil, fmt.Errorf("%w: %q exceeded %s", ErrExecutionTimeout, name, timeout)
}

// applyCostBudget raises the VM's cumulative WasmEdge cost limit by cost
// above whatever has already been spent, giving the upcoming Execute call a
// fresh fuel budget. WasmEdge's cost counter is cumulative for the VM's
// lifetime (the Go bindings do not expose a per-call reset), so "per
// invocation" fuel is implemented by ratcheting the limit up each call rather
// than resetting the counter. cost is the module default (m.costLimit) unless
// a per-call ExecBudget override raised it (see executeDirect).
func (m *Module) applyCostBudget(cost uint) {
	stats := m.vm.GetStatistics()
	if stats == nil {
		return
	}
	stats.SetCostLimit(stats.GetTotalCost() + cost)
}

// effectiveTimeout resolves the wall-clock budget for one Execute call from a
// base budget (the module's WithExecTimeout default, or a per-call ExecBudget
// override chosen by the host), narrowed to ctx's deadline when ctx has one
// and it is sooner. Zero means "no wall-clock enforcement". The ctx-deadline
// narrowing is preserved for both the default and the override path, so an
// even-sooner request-scoped deadline always wins.
func (m *Module) effectiveTimeout(ctx context.Context, base time.Duration) time.Duration {
	timeout := base
	if dl, ok := ctx.Deadline(); ok {
		remain := time.Until(dl)
		if remain <= 0 {
			remain = time.Millisecond
		}
		if timeout <= 0 || remain < timeout {
			timeout = remain
		}
	}
	return timeout
}

// executeDirect invokes an export on the calling goroutine (or, when called
// from the dedicated-thread worker, on that locked OS thread), applying the
// module's configured resource limits.
//
// Fuel (WithCostLimit) is enforced uniformly regardless of dispatch mode —
// WasmEdge aborts the guest itself once the budget is spent, which works
// whether the call is synchronous or async and whether or not it runs on a
// dedicated thread.
//
// Wall-clock (WithExecTimeout) is hard-enforced via the async-execute +
// cancel path ONLY for non-dedicated-thread modules. WithDedicatedThread
// modules keep the plain synchronous call to preserve the nested-AOT
// thread-affinity invariant that option exists for (see its doc comment);
// for those, WithCostLimit is the mechanism that reliably bounds a runaway
// guest, and the wall-clock budget is best-effort only (a ctx.Err() check
// up front, no mid-flight interrupt). This is a known, documented residual
// gap — see WithExecTimeout's doc comment.
func (m *Module) executeDirect(ctx context.Context, name string, params ...interface{}) ([]interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Per-call budget: start from the module's configured defaults, then let a
	// host-attached ExecBudget override RAISE (or lower) them for this one
	// call. Guest WASM cannot set a Go context value, so this is never a
	// module self-grant — the host caller decides (e.g. modulert grants a
	// multi-minute budget on the scheduled/cron seam, while interactive
	// invokes leave it unset and keep the tight default). The ctx deadline
	// still narrows the resulting wall-clock budget below.
	costLimit := m.costLimit
	base := m.execTimeout
	if b, ok := execBudgetFrom(ctx); ok {
		if b.CostLimit > 0 {
			costLimit = b.CostLimit
		}
		if b.Timeout > 0 {
			base = b.Timeout
		}
	}

	if costLimit > 0 {
		m.applyCostBudget(costLimit)
	}

	timeout := m.effectiveTimeout(ctx, base)
	if timeout > 0 && !m.dedicatedThread {
		return m.runAsyncWithTimeout(timeout, name, params...)
	}

	values, err := m.runSync(name, params...)
	return values, classifyExecError(err)
}

// exec routes a guest call through the dedicated thread when configured,
// falling back to a direct call otherwise.
func (m *Module) exec(ctx context.Context, name string, params ...interface{}) ([]interface{}, error) {
	if m.execCh == nil {
		return m.executeDirect(ctx, name, params...)
	}
	req := &execRequest{ctx: ctx, name: name, params: params, done: make(chan execResult, 1)}
	m.execCh <- req
	res := <-req.done
	return res.values, res.err
}

// MemoryStats describes the module's current default linear memory size.
type MemoryStats struct {
	Pages    uint64
	Bytes    uint64
	MaxPages uint64
	MaxBytes uint64
}

// NewModule creates a WasmEdge VM, optionally enables WASI and host modules,
// loads the WASM bytes, and instantiates the module.
func NewModule(wasmBytes []byte, opts ...Option) (*Module, error) {
	cfg := &config{
		mallocName: "malloc",
		freeName:   "free",
	}
	for _, o := range opts {
		o(cfg)
	}

	conf := wasmedge.NewConfigure()
	conf.AddConfig(wasmedge.THREADS)
	conf.AddConfig(wasmedge.EXCEPTION_HANDLING)
	if cfg.enableWASI {
		conf.AddConfig(wasmedge.WASI)
	}
	if cfg.maxMemoryPages > 0 {
		conf.SetMaxMemoryPage(uint(cfg.maxMemoryPages))
	}
	if cfg.costLimit > 0 {
		// Enables WasmEdge's instruction-cost accounting so
		// Statistics.SetCostLimit (applyCostBudget) actually aborts guest
		// execution once the fuel budget is spent (B3 defensive hardening).
		conf.SetStatisticsCostMeasuring(true)
	}

	vm := wasmedge.NewVMWithConfig(conf)
	if vm == nil {
		conf.Release()
		return nil, errors.New("failed to create WasmEdge VM")
	}

	m := &Module{
		conf:              conf,
		vm:                vm,
		registeredName:    cfg.registeredName,
		mallocName:        cfg.mallocName,
		freeName:          cfg.freeName,
		secureDeallocName: cfg.secureDeallocName,
		dedicatedThread:   cfg.dedicatedThread,
		execTimeout:       cfg.execTimeout,
		costLimit:         cfg.costLimit,
	}

	// Initialize WASI module if enabled.
	// When WASI is in the Configure, the VM auto-registers a WASI module.
	// We just need to call InitWasi() on it to set args/env/preopens.
	if cfg.enableWASI {
		wasiMod := vm.GetImportModule(wasmedge.WASI)
		if wasiMod == nil {
			m.Release()
			return nil, errors.New("failed to get WASI import module from VM")
		}
		args := cfg.wasiArgs
		if args == nil {
			args = []string{}
		}
		envs := cfg.wasiEnvs
		if envs == nil {
			envs = []string{}
		}
		preopens := cfg.wasiPreopens
		if preopens == nil {
			preopens = []string{}
		}
		wasiMod.InitWasi(args, envs, preopens)
	}

	// Register host modules
	for _, spec := range cfg.hostModules {
		hostMod := wasmedge.NewModule(spec.name)
		if hostMod == nil {
			m.Release()
			return nil, fmt.Errorf("failed to create host module %q", spec.name)
		}
		for _, hf := range spec.funcs {
			ft := wasmedge.NewFunctionType(hf.Params, hf.Returns)
			fn := wasmedge.NewFunction(ft, hf.Func, nil, hf.Cost)
			ft.Release()
			hostMod.AddFunction(hf.Name, fn)
		}
		err := vm.RegisterModule(hostMod)
		if err != nil {
			hostMod.Release()
			m.Release()
			return nil, fmt.Errorf("failed to register host module %q: %w", spec.name, err)
		}
		m.hostMods = append(m.hostMods, hostMod)
	}

	// Direct-linkage import sources (loop C.7): live instances borrowed from
	// other VMs first (e.g. the store's FlatSQL engine under its registered
	// name), then named wasm modules instantiated in THIS VM (e.g. the
	// flatsql_link shim, whose own imports resolve against the instances
	// registered above). Both must precede the main module's instantiation.
	for _, src := range cfg.linkedModules {
		inst := src.RegisteredInstance()
		if inst == nil {
			m.Release()
			return nil, errors.New("linked module has no registered named instance (create it WithRegisteredName)")
		}
		if err := vm.RegisterModule(inst); err != nil {
			m.Release()
			return nil, fmt.Errorf("failed to register linked module instance %q: %w", src.RegisteredName(), err)
		}
	}
	for _, spec := range cfg.namedWasm {
		if err := vm.RegisterWasmBuffer(spec.name, spec.bytes); err != nil {
			m.Release()
			return nil, fmt.Errorf("failed to register named wasm %q: %w", spec.name, err)
		}
	}

	// Load, validate, and instantiate the WASM module (named when a
	// registered name was requested — required to share the live instance
	// into other VMs).
	if cfg.registeredName != "" {
		if err := vm.RegisterWasmBuffer(cfg.registeredName, wasmBytes); err != nil {
			m.Release()
			return nil, fmt.Errorf("failed to register WASM as %q: %w", cfg.registeredName, err)
		}
	} else {
		if err := vm.LoadWasmBuffer(wasmBytes); err != nil {
			m.Release()
			return nil, fmt.Errorf("failed to load WASM: %w", err)
		}
		if err := vm.Validate(); err != nil {
			m.Release()
			return nil, fmt.Errorf("failed to validate WASM: %w", err)
		}
		if err := vm.Instantiate(); err != nil {
			m.Release()
			return nil, fmt.Errorf("failed to instantiate WASM: %w", err)
		}
	}

	if cfg.dedicatedThread {
		m.startExecThread()
	}

	return m, nil
}

// NewModuleFromFile loads WASM from a file path.
func NewModuleFromFile(path string, opts ...Option) (*Module, error) {
	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read WASM file: %w", err)
	}
	return NewModule(wasmBytes, opts...)
}

// Execute calls a WASM exported function by name, subject to the module's
// configured default resource limits (WithExecTimeout / WithCostLimit).
func (m *Module) Execute(name string, params ...interface{}) ([]interface{}, error) {
	return m.exec(context.Background(), name, params...)
}

// ExecuteContext calls a WASM exported function by name, narrowing the
// module's configured wall-clock budget to ctx's deadline when ctx has one
// and it is sooner (B3: per-invocation timeout threaded from the caller).
// An already-canceled/expired ctx fails fast without starting the guest
// call. When the host attached a per-call ExecBudget to ctx (WithExecBudget),
// that budget replaces the module's wall-clock/fuel defaults for this call
// (still subject to the ctx-deadline narrowing above); otherwise fuel
// (WithCostLimit) enforcement is unaffected by ctx.
func (m *Module) ExecuteContext(ctx context.Context, name string, params ...interface{}) ([]interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return m.exec(ctx, name, params...)
}

// memory returns the default linear memory ("memory") from the active (or
// named registered) module.
func (m *Module) memory() (*wasmedge.Memory, error) {
	var mod *wasmedge.Module
	if m.registeredName != "" {
		mod = m.vm.GetRegisteredModule(m.registeredName)
	} else {
		mod = m.vm.GetActiveModule()
	}
	if mod == nil {
		return nil, ErrNoModule
	}
	mem := mod.FindMemory("memory")
	if mem == nil {
		return nil, fmt.Errorf("%w: no 'memory' export found", ErrMemory)
	}
	return mem, nil
}

// RegisteredName reports the name the main module was registered under
// (empty for anonymous active modules).
func (m *Module) RegisteredName() string { return m.registeredName }

// RegisteredInstance returns the live named module instance (nil unless the
// module was created WithRegisteredName). The instance is owned by this VM —
// borrowers must not outlive it.
func (m *Module) RegisteredInstance() *wasmedge.Module {
	if m == nil || m.vm == nil || m.registeredName == "" {
		return nil
	}
	return m.vm.GetRegisteredModule(m.registeredName)
}

// MemoryStats returns the current and configured maximum linear memory sizes.
func (m *Module) MemoryStats() (MemoryStats, error) {
	if m == nil || m.vm == nil {
		return MemoryStats{}, ErrNoModule
	}
	mem, err := m.memory()
	if err != nil {
		return MemoryStats{}, err
	}
	pages := uint64(mem.GetPageSize())
	maxPages := uint64(0)
	if m.conf != nil {
		maxPages = uint64(m.conf.GetMaxMemoryPage())
	}
	const wasmPageSize = uint64(65536)
	return MemoryStats{
		Pages:    pages,
		Bytes:    pages * wasmPageSize,
		MaxPages: maxPages,
		MaxBytes: maxPages * wasmPageSize,
	}, nil
}

// ReadMemory copies bytes from WASM linear memory at [offset, offset+length).
func (m *Module) ReadMemory(offset, length uint32) ([]byte, error) {
	mem, err := m.memory()
	if err != nil {
		return nil, err
	}
	data, err := mem.GetData(uint(offset), uint(length))
	if err != nil {
		return nil, fmt.Errorf("%w: GetData at %d len %d: %v", ErrMemory, offset, length, err)
	}
	// Copy out — the returned slice may point into WASM linear memory.
	result := make([]byte, length)
	copy(result, data)
	return result, nil
}

// WriteMemory writes data into WASM linear memory at offset.
func (m *Module) WriteMemory(offset uint32, data []byte) error {
	mem, err := m.memory()
	if err != nil {
		return err
	}
	if err := mem.SetData(data, uint(offset), uint(len(data))); err != nil {
		return fmt.Errorf("%w: SetData at %d len %d: %v", ErrMemory, offset, len(data), err)
	}
	return nil
}

// Allocate calls the module's malloc, writes data into the allocated region,
// and returns the pointer. Caller must Deallocate when done.
func (m *Module) Allocate(data []byte) (uint32, error) {
	ptr, err := m.AllocateSize(uint32(len(data)))
	if err != nil {
		return 0, err
	}
	if err := m.WriteMemory(ptr, data); err != nil {
		m.Deallocate(ptr)
		return 0, err
	}
	return ptr, nil
}

// AllocateSize calls the module's malloc for the given size, returning the pointer.
func (m *Module) AllocateSize(size uint32) (uint32, error) {
	results, err := m.exec(context.Background(), m.mallocName, int32(size))
	if err != nil {
		return 0, fmt.Errorf("malloc(%d) failed: %w", size, err)
	}
	ptr := toUint32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("%w: malloc(%d) returned 0", ErrAllocFailed, size)
	}
	return ptr, nil
}

// Deallocate calls the module's free.
func (m *Module) Deallocate(ptr uint32) {
	m.exec(context.Background(), m.freeName, int32(ptr))
}

// SecureDeallocate wipes memory then frees. Falls back to plain Deallocate
// if no secure dealloc function was configured.
func (m *Module) SecureDeallocate(ptr, size uint32) {
	if m.secureDeallocName != "" {
		m.exec(context.Background(), m.secureDeallocName, int32(ptr), int32(size))
	} else {
		m.Deallocate(ptr)
	}
}

// AllocateString allocates a null-terminated C string in WASM memory.
func (m *Module) AllocateString(s string) (uint32, error) {
	data := append([]byte(s), 0)
	return m.Allocate(data)
}

// ReadCString reads a null-terminated string from WASM memory.
func (m *Module) ReadCString(ptr, maxLen uint32) (string, error) {
	data, err := m.ReadMemory(ptr, maxLen)
	if err != nil {
		return "", err
	}
	for i, b := range data {
		if b == 0 {
			return string(data[:i]), nil
		}
	}
	return string(data), nil
}

// Release frees all WasmEdge resources.
func (m *Module) Release() {
	if m.execCh != nil {
		close(m.execCh)
		m.execWG.Wait()
		m.execCh = nil
	}
	if m.vm != nil {
		m.vm.Release()
		m.vm = nil
	}
	for _, hm := range m.hostMods {
		if hm != nil {
			hm.Release()
		}
	}
	m.hostMods = nil
	if m.conf != nil {
		m.conf.Release()
		m.conf = nil
	}
}

// Lock acquires the module mutex for thread-safe access.
func (m *Module) Lock() { m.mu.Lock() }

// Unlock releases the module mutex.
func (m *Module) Unlock() { m.mu.Unlock() }

// VM returns the underlying WasmEdge VM (for advanced use cases).
func (m *Module) VM() *wasmedge.VM { return m.vm }

// toUint32 converts a WasmEdge return value (interface{}) to uint32.
func toUint32(v interface{}) uint32 {
	switch val := v.(type) {
	case int32:
		return uint32(val)
	case uint32:
		return val
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}

// ToInt32 converts a WasmEdge return value to int32.
func ToInt32(v interface{}) int32 {
	switch val := v.(type) {
	case int32:
		return val
	case uint32:
		return int32(val)
	case int64:
		return int32(val)
	case uint64:
		return int32(val)
	default:
		return 0
	}
}
