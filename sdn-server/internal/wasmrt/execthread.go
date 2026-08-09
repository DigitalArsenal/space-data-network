package wasmrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// GuestCaller is the guest-facing surface a caller needs to drive one unit of
// work against a module: invoke exports, move bytes in and out of linear
// memory, and manage guest allocations.
//
// It exists so a caller can be written ONCE and run either
//   - from an arbitrary goroutine, against *Module (every guest call pays a
//     dedicated-thread handoff), or
//   - inside Module.RunOnExecThread, against *ExecScope (the whole unit of
//     work pays ONE handoff, total).
//
// Both *Module and *ExecScope implement it, so the switch is a parameter, not
// a rewrite.
type GuestCaller interface {
	Execute(name string, params ...interface{}) ([]interface{}, error)
	Allocate(data []byte) (uint32, error)
	AllocateSize(size uint32) (uint32, error)
	AllocateString(s string) (uint32, error)
	Deallocate(ptr uint32)
	SecureDeallocate(ptr, size uint32)
	ReadMemory(offset, length uint32) ([]byte, error)
	WriteMemory(offset uint32, data []byte) error
	ReadCString(ptr, maxLen uint32) (string, error)
	MemoryStats() (MemoryStats, error)
}

var (
	_ GuestCaller = (*Module)(nil)
	_ GuestCaller = (*ExecScope)(nil)
)

// execBatchDisabled turns RunOnExecThread back into per-call dispatch.
// SDN_WASMRT_EXEC_BATCH=0 (or "off"/"false") restores the pre-batch behaviour
// exactly. Read once: this must not be re-evaluated per call on the hot path.
var execBatchDisabled = func() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SDN_WASMRT_EXEC_BATCH"))) {
	case "0", "off", "false", "no":
		return true
	default:
		return false
	}
}()

// ExecBatchEnabled reports whether guest-call batching is active, so a status
// surface can state which dispatch mode produced a measurement instead of
// leaving a reader to guess.
func ExecBatchEnabled() bool { return !execBatchDisabled }

// ExecScope is a GuestCaller that is ALREADY on the module's dedicated
// execution thread. Its Execute invokes the guest synchronously in place —
// no channel send, no goroutine wakeup, no scheduler round trip — because the
// goroutine running it IS the dedicated worker.
//
// A scope is valid only for the duration of the RunOnExecThread callback that
// produced it. Using one afterwards executes the guest from the wrong thread
// and breaks the nested-AOT thread-affinity invariant WithDedicatedThread
// exists to hold, so callers must not retain it.
type ExecScope struct {
	m    *Module
	ctx  context.Context
	live atomic.Bool
}

// WHY THIS PRIMITIVE EXISTS — measured, host-01, 2026-08-09.
//
// The FlatSQL engine module is created WithDedicatedThread, so EVERY guest
// call is a cross-OS-thread handoff: an unbuffered send to a
// runtime.LockOSThread()ed worker and a reply on a second channel. That
// comment used to say the handoff "costs one channel round-trip per call —
// noise next to any engine query". That claim is now REFUTED by measurement,
// and this file is the correction.
//
// A store read is not one guest call. flatsqlrt's Database.Query allocates the
// SQL (malloc), executes it, then reads the result back one export call at a
// time: column count, row count, EVERY column name, and TWO calls per result
// CELL (type, then value). A 30-row x 15-column control table is therefore
// ~920 guest calls — and ~920 pairs of goroutine wakeups on a 2-vCPU box whose
// load average sits at 3. At roughly a millisecond per round trip under that
// contention, one indexed read of a 30-row table costs ~0.9 s, flat, at idle
// and under load alike, against a 6 ms HTTP+TLS floor on the same box in the
// same second.
//
// The rows were never the work and the SQL was never the work. The DISPATCH
// was the work.
//
// RunOnExecThread removes it without weakening anything the dedicated thread
// protects: the guest still executes only ever on that one locked OS thread,
// so the nested-AOT affinity invariant holds byte for byte. What changes is
// that a whole STATEMENT is one unit of work sent to that thread, instead of
// one thousand.
//
// The wall-clock budget applies to the batch as a whole, which is a TIGHTENING:
// before, each of the ~920 calls carried its own five-minute budget, so a
// pathological statement could hold the engine for far longer than any single
// call's bound suggested. Now the statement is the bounded thing.
//
// fn runs on the worker goroutine. It MUST use the GuestCaller it is handed for
// every guest call: calling back into Module.Execute from inside fn would send
// on execCh from the very goroutine that is supposed to receive from it, and
// that self-deadlocks until the wall-clock budget abandons the thread.
//
// A module without a dedicated thread has no handoff to save, so fn runs inline
// on the caller's goroutine and every guest call behaves exactly as before.
func (m *Module) RunOnExecThread(ctx context.Context, fn func(GuestCaller) error) error {
	if m == nil || m.vm == nil {
		return ErrNoModule
	}
	if fn == nil {
		return nil
	}
	// Same fail-fast ordering as exec(): before any lock, before any handoff,
	// so a caller unwinding from an earlier trap is refused instantly rather
	// than queued behind a call that will never return.
	if m.poisoned.Load() {
		return m.poisonedErr()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// A/B AND ROLLBACK, at ONE binary and ONE engine pin. Handing fn the
	// *Module makes every guest call take the old per-call handoff again, so
	// the before/after can be measured on the SAME process on the SAME box —
	// the only comparison the evidence-admissibility law admits — and an
	// operator who suspects this change can revert its behaviour with a unit
	// file line instead of a rollback binary.
	if execBatchDisabled {
		return fn(m)
	}

	scope := &ExecScope{m: m, ctx: ctx}
	scope.live.Store(true)
	defer scope.live.Store(false)

	if m.execCh == nil {
		return runBatchBody(m, scope, fn)
	}

	base := m.execTimeout
	if b, ok := execBudgetFrom(ctx); ok && b.Timeout > 0 {
		base = b.Timeout
	}
	timeout := m.effectiveTimeout(ctx, base)

	req := &execRequest{
		ctx:   ctx,
		name:  "(batch)",
		batch: func() error { return runBatchBody(m, scope, fn) },
		done:  make(chan execResult, 1),
	}

	var fired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		fired = timer.C
	}

	m.statBatches.Add(1)
	m.statDispatch.Add(1)

	// Bounded on BOTH halves, exactly as exec(): an unbuffered send to a worker
	// that is already wedged in cgo would otherwise hang this caller forever.
	select {
	case m.execCh <- req:
	case <-fired:
		return m.abandonExecThread("(batch)", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case res := <-req.done:
		return res.err
	case <-fired:
		return m.abandonExecThread("(batch)", timeout)
	case <-ctx.Done():
		return m.abandonExecThreadCause("(batch)", ctx.Err())
	}
}

// runBatchBody runs the caller's unit of work, converting a panic into a
// poisoned module and a returned error.
//
// The batch body is HOST Go code (result materialization, cell decoding) that
// runs on the dedicated worker goroutine. An unrecovered panic there kills the
// worker without ever sending on `done`, so the caller would block until the
// five-minute wall-clock budget abandoned the thread — turning a Go bug into a
// five-minute engine stall. Recovering converts it into an immediate, typed
// failure, and poisons the instance because a body that panicked mid-readout
// may have left the engine's result cursor in an unknown state.
func runBatchBody(m *Module, scope *ExecScope, fn func(GuestCaller) error) (err error) {
	m.inBatch.Store(true)
	defer m.inBatch.Store(false)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wasmrt: panic in exec-thread batch: %v\n%s", r, debug.Stack())
			m.MarkPoisoned(err)
		}
	}()
	return fn(scope)
}

// ErrBatchReentry is returned when a guest call is dispatched through the
// module while one of its batches is running.
//
// This is the ONE way RunOnExecThread can be misused into a wedge: the batch
// body runs ON the dedicated worker goroutine, so a call to Module.Execute from
// inside it sends on execCh from the very goroutine that receives from it. That
// send cannot complete, and with no bound it would hang until the five-minute
// wall-clock budget abandoned the thread and poisoned the engine — a Go typo
// costing a five-minute engine stall and a wholesale instance replacement.
//
// Refusing instead makes it a typed error at the call site, in microseconds,
// with the export named. It cannot fire for a lock-respecting caller: every
// batch is taken under the module lock that all other guest callers hold, so no
// other goroutine can be dispatching while inBatch is set. If this error is ever
// seen, the fix is to use the GuestCaller the batch was handed.
var ErrBatchReentry = errors.New("wasm guest call dispatched while this module's exec-thread batch is running (use the batch's GuestCaller)")

// Execute invokes an export synchronously on the thread this scope belongs to.
func (s *ExecScope) Execute(name string, params ...interface{}) ([]interface{}, error) {
	m := s.m
	if !s.live.Load() {
		return nil, fmt.Errorf("wasmrt: ExecScope used after its RunOnExecThread callback returned (export %q)", name)
	}
	if m.poisoned.Load() {
		return nil, m.poisonedErr()
	}
	values, err := m.executeDirect(s.ctx, name, params...)
	if poisonsModule(err) {
		m.MarkPoisoned(fmt.Errorf("trap in %q: %w", name, err))
	}
	return values, err
}

// The memory surface does not dispatch: WasmEdge linear-memory access is a
// plain read/write against the instance, not a guest invocation, so these are
// identical to the module's and are here only so one GuestCaller covers a whole
// unit of work.
func (s *ExecScope) ReadMemory(offset, length uint32) ([]byte, error) {
	return s.m.ReadMemory(offset, length)
}
func (s *ExecScope) WriteMemory(offset uint32, data []byte) error {
	return s.m.WriteMemory(offset, data)
}
func (s *ExecScope) MemoryStats() (MemoryStats, error) { return s.m.MemoryStats() }
func (s *ExecScope) ReadCString(ptr, maxLen uint32) (string, error) {
	return readCStringVia(s, ptr, maxLen)
}

// Allocation DOES dispatch (malloc/free are guest exports), so these route
// through this scope's in-place Execute rather than the module's handoff.
func (s *ExecScope) Allocate(data []byte) (uint32, error)      { return allocateVia(s, data) }
func (s *ExecScope) AllocateSize(size uint32) (uint32, error)  { return allocateSizeVia(s, s.m.mallocName, size) }
func (s *ExecScope) AllocateString(str string) (uint32, error) { return allocateStringVia(s, str) }
func (s *ExecScope) Deallocate(ptr uint32)                     { s.Execute(s.m.freeName, int32(ptr)) }
func (s *ExecScope) SecureDeallocate(ptr, size uint32) {
	if s.m.secureDeallocName != "" {
		s.Execute(s.m.secureDeallocName, int32(ptr), int32(size))
		return
	}
	s.Deallocate(ptr)
}

// Shared implementations so *Module and *ExecScope cannot drift apart.

func allocateVia(g GuestCaller, data []byte) (uint32, error) {
	ptr, err := g.AllocateSize(uint32(len(data)))
	if err != nil {
		return 0, err
	}
	if err := g.WriteMemory(ptr, data); err != nil {
		g.Deallocate(ptr)
		return 0, err
	}
	return ptr, nil
}

func allocateSizeVia(g GuestCaller, mallocName string, size uint32) (uint32, error) {
	results, err := g.Execute(mallocName, int32(size))
	if err != nil {
		return 0, fmt.Errorf("malloc(%d) failed: %w", size, err)
	}
	ptr := toUint32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("%w: malloc(%d) returned 0", ErrAllocFailed, size)
	}
	return ptr, nil
}

func allocateStringVia(g GuestCaller, s string) (uint32, error) {
	return g.Allocate(append([]byte(s), 0))
}

func readCStringVia(g GuestCaller, ptr, maxLen uint32) (string, error) {
	data, err := g.ReadMemory(ptr, maxLen)
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

// DispatchStats is the module's cumulative guest-call account.
//
// THE INSTRUMENT THAT NAMES THE LANE. `perf` on host-01 showed ~87,000
// rt_sigaction/s — two per guest execution — IDENTICAL at idle and under five
// concurrent API probes, i.e. ~43,500 guest calls per second of purely
// BACKGROUND work on a 2-vCPU box. Nothing in the daemon could say which export
// those calls were, so the lane could not be named, only inferred. These
// counters name it: per-export counts plus how many of them cost a thread
// handoff and how many rode inside a batch.
type DispatchStats struct {
	// Calls is every guest invocation, batched or not.
	Calls int64
	// Dispatches is how many of those cost a cross-OS-thread handoff. With
	// RunOnExecThread this is one per STATEMENT rather than one per call, so
	// Calls/Dispatches is the amortization actually achieved at runtime.
	Dispatches int64
	// Batches counts RunOnExecThread units of work.
	Batches int64
	// PerExport is the call count for each export, descending.
	PerExport []ExportCount
}

// ExportCount is one export's cumulative invocation count.
type ExportCount struct {
	Name  string
	Count int64
}

// countCall records one guest invocation. One sync.Map load plus one atomic
// add — tens of nanoseconds against a dispatch that measured ~1 ms, so it is
// always on: an instrument that has to be enabled is an instrument that is off
// on the box where the defect is.
func (m *Module) countCall(name string) {
	m.statCalls.Add(1)
	if v, ok := m.callCounts.Load(name); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	v, _ := m.callCounts.LoadOrStore(name, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

// DispatchStats returns the cumulative guest-call account (see DispatchStats).
func (m *Module) DispatchStats() DispatchStats {
	if m == nil {
		return DispatchStats{}
	}
	out := DispatchStats{
		Calls:      m.statCalls.Load(),
		Dispatches: m.statDispatch.Load(),
		Batches:    m.statBatches.Load(),
	}
	m.callCounts.Range(func(k, v interface{}) bool {
		out.PerExport = append(out.PerExport, ExportCount{Name: k.(string), Count: v.(*atomic.Int64).Load()})
		return true
	})
	sort.Slice(out.PerExport, func(i, j int) bool {
		if out.PerExport[i].Count != out.PerExport[j].Count {
			return out.PerExport[i].Count > out.PerExport[j].Count
		}
		return out.PerExport[i].Name < out.PerExport[j].Name
	})
	return out
}

// Top renders the n busiest exports as "name=count" pairs — the one-line form
// a log or status surface wants.
func (d DispatchStats) Top(n int) string {
	var b strings.Builder
	for i, e := range d.PerExport {
		if i >= n {
			break
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%d", e.Name, e.Count)
	}
	return b.String()
}
