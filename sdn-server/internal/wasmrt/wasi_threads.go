package wasmrt

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// WASI preview1 threads: a fresh instance per worker, one shared linear memory,
// and one executor. WasmEdge 0.16.4 keeps atomic waiters on the executor, so a
// separate executor per worker breaks atomic.wait/notify. No physics lives here.
// https://github.com/WebAssembly/wasi-threads
type wasiThreadGroup struct {
	owner       *Module
	ast         *wasmedge.AST
	mu          sync.Mutex
	closed      bool
	failure     chan struct{}
	failureOnce sync.Once
	next        int32
	workers     map[int32]*wasiWorker
	// maxWorkers caps concurrently running workers (WithMaxThreads).
	maxWorkers int
	// service marks a partition-store instance (WithServiceThreads).
	service bool
	// spawned and refused count spawn outcomes for the instance's lifetime.
	spawned, refused int64
	// interrupting is set once the host stops the group on purpose, so the
	// "execution interrupted" its workers then return is not read as a trap.
	interrupting bool
}
type wasiWorker struct {
	instance *wasmedge.Module
	async    *wasmedge.Async
	done     chan struct{}
}

func (m *Module) prepareWasiThreads(ast *wasmedge.AST, cfg *config) (*wasmedge.Memory, error) {
	required := false
	var memoryType *wasmedge.MemoryType
	for _, i := range ast.ListImports() {
		if i.GetModuleName() == "wasi" && i.GetExternalName() == "thread-spawn" {
			required = true
		}
		if i.GetExternalType() == wasmedge.ExternType_Memory {
			if i.GetModuleName() != "env" || i.GetExternalName() != "memory" || memoryType != nil {
				return nil, fmt.Errorf("unsupported WASM imported memory: expected one env.memory")
			}
			memoryType = i.GetExternalValue().(*wasmedge.MemoryType)
		}
	}
	if !required && memoryType == nil {
		return nil, nil
	}
	if memoryType == nil {
		return nil, fmt.Errorf("WASI threads require imported shared memory")
	}
	limit := memoryType.GetLimit()
	if !limit.IsShared() || !limit.HasMax() {
		return nil, fmt.Errorf("WASI threads require bounded shared memory")
	}
	maximum := limit.GetMax()
	if cfg.maxMemoryPages > 0 && maximum > uint(cfg.maxMemoryPages) {
		maximum = uint(cfg.maxMemoryPages)
	}
	if maximum < limit.GetMin() {
		return nil, fmt.Errorf("shared memory exceeds operator budget")
	}
	bounded := wasmedge.NewMemoryType(wasmedge.NewLimitSharedWithMax(limit.GetMin(), maximum))
	memory := wasmedge.NewMemory(bounded)
	bounded.Release()
	if memory == nil {
		return nil, fmt.Errorf("could not allocate shared memory")
	}
	if !required {
		return memory, nil
	}
	maxWorkers := cfg.maxThreads
	if maxWorkers == 0 {
		maxWorkers = DefaultMaxThreads
	}
	g := &wasiThreadGroup{owner: m, ast: ast, next: 1, workers: make(map[int32]*wasiWorker),
		failure: make(chan struct{}), maxWorkers: maxWorkers, service: cfg.serviceThreads}
	m.threads = g
	host := wasmedge.NewModule("wasi")
	ft := wasmedge.NewFunctionType([]*wasmedge.ValType{wasmedge.NewValTypeI32()}, []*wasmedge.ValType{wasmedge.NewValTypeI32()})
	fn := wasmedge.NewFunction(ft, g.spawn, nil, 0)
	ft.Release()
	host.AddFunction("thread-spawn", fn)
	if err := m.vm.RegisterModule(host); err != nil {
		host.Release()
		memory.Release()
		return nil, err
	}
	m.hostMods = append(m.hostMods, host)
	return memory, nil
}

func (g *wasiThreadGroup) spawn(_ interface{}, frame *wasmedge.CallingFrame, args []interface{}) ([]interface{}, wasmedge.Result) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fail := func() ([]interface{}, wasmedge.Result) {
		g.refused++
		return []interface{}{int32(-1)}, wasmedge.Result_Success
	}
	if g.closed || g.owner.Poisoned() || len(g.workers) >= g.maxWorkers || g.next >= 1<<29 {
		return fail()
	}
	executor := frame.GetExecutor()
	instance, err := executor.Instantiate(g.owner.vm.GetStore(), g.ast)
	if err != nil {
		return fail()
	}
	entry := instance.FindFunction("wasi_thread_start")
	if entry == nil {
		instance.Release()
		return fail()
	}
	tid := g.next
	g.next++
	async := executor.AsyncInvoke(entry, tid, args[0].(int32))
	if async == nil {
		instance.Release()
		return fail()
	}
	w := &wasiWorker{instance: instance, async: async, done: make(chan struct{})}
	g.workers[tid] = w
	g.spawned++
	go func() {
		_, err := async.GetResult()
		g.mu.Lock()
		interrupting := g.interrupting
		g.mu.Unlock()
		if err != nil && interrupting && errors.Is(classifyExecError(err), ErrExecutionTimeout) {
			// Our own stop reached this worker: an expected exit, not a trap.
			err = nil
		}
		if err != nil {
			g.owner.MarkPoisoned(fmt.Errorf("WASI worker %d: %w", tid, err))
			g.failureOnce.Do(func() { close(g.failure) })
			g.mu.Lock()
			g.closed = true
			for otherID, other := range g.workers {
				if otherID != tid {
					other.async.Cancel()
				}
			}
			g.mu.Unlock()
		}
		g.mu.Lock()
		async.Release()
		instance.Release()
		delete(g.workers, tid)
		close(w.done)
		g.mu.Unlock()
	}()
	return []interface{}{tid}, wasmedge.Result_Success
}

// Never free shared memory beneath a worker that did not stop. The caller
// deliberately retains the poisoned VM if cancellation cannot complete.
//
// One cancel per round, not one per worker: every Async of a module shares the
// executor's single stop token, so cancelling N workers back to back set the
// same token N times and an unpatched runtime let exactly one worker consume
// it. interrupt() cancels, waits, and repeats.
func (g *wasiThreadGroup) stop() bool {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	return g.interrupt(time.Now().Add(2*time.Second)) == 0
}

// live returns the workers still running and whether any exist.
func (g *wasiThreadGroup) live() []*wasiWorker {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*wasiWorker, 0, len(g.workers))
	for _, w := range g.workers {
		out = append(out, w)
	}
	return out
}

// waitAll waits until every listed worker has left, or the deadline passes.
// Returns how many are still running.
func waitAll(workers []*wasiWorker, deadline time.Time) int {
	remaining := 0
	for _, w := range workers {
		wait := time.Until(deadline)
		if wait <= 0 {
			wait = 0
		}
		t := time.NewTimer(wait)
		select {
		case <-w.done:
		case <-t.C:
			remaining++
		}
		t.Stop()
	}
	return remaining
}

// interrupt stops every invocation on the module's executor. The patched
// runtime (build-static-wasmedge.sh, 02-stop-token) makes one cancel reach
// every thread; on an unpatched runtime a cancel reaches ONE thread, so this
// keeps cancelling, one worker at a time, until they are all gone or the
// deadline passes. Returns the workers still running.
func (g *wasiThreadGroup) interrupt(deadline time.Time) int {
	g.mu.Lock()
	g.interrupting = true
	g.mu.Unlock()
	for {
		workers := g.live()
		if len(workers) == 0 {
			return 0
		}
		workers[0].async.Cancel()
		slice := time.Now().Add(20 * time.Millisecond)
		if slice.After(deadline) {
			slice = deadline
		}
		if waitAll(workers, slice) == 0 {
			return 0
		}
		if !time.Now().Before(deadline) {
			return len(g.live())
		}
	}
}

// ThreadStats is a snapshot of a module's WASI worker threads.
type ThreadStats struct {
	Live, Max        int
	Spawned, Refused int64
	Service          bool
}

// ThreadStats reports the module's worker threads (zero when unthreaded).
func (m *Module) ThreadStats() ThreadStats {
	if m == nil || m.threads == nil {
		return ThreadStats{}
	}
	g := m.threads
	g.mu.Lock()
	defer g.mu.Unlock()
	return ThreadStats{Live: len(g.workers), Max: g.maxWorkers, Spawned: g.spawned, Refused: g.refused, Service: g.service}
}

// WaitThreads waits until the module's worker threads have all left or the
// deadline passes, and returns how many are still running. Callers stop a
// service instance cooperatively first (its stop word), then wait here, then
// InterruptThreads what did not leave.
func (m *Module) WaitThreads(deadline time.Time) int {
	if m == nil || m.threads == nil {
		return 0
	}
	return waitAll(m.threads.live(), deadline)
}

// InterruptThreads closes the module to new threads and stops every
// invocation still running on its executor (design A21), bounded by deadline.
// A guest in a compute loop stops only if it was AOT-compiled Interruptible
// or runs interpreted. Returns the threads still running; the caller must then
// treat the instance as retained (never release memory beneath them).
func (m *Module) InterruptThreads(deadline time.Time) int {
	if m == nil || m.threads == nil {
		return 0
	}
	m.threads.mu.Lock()
	m.threads.closed = true
	m.threads.mu.Unlock()
	return m.threads.interrupt(deadline)
}

// Failure is closed when any WASI worker of the module traps (nil for an
// unthreaded module). A supervisor selects on it.
func (m *Module) Failure() <-chan struct{} {
	if m == nil || m.threads == nil {
		return nil
	}
	return m.threads.failure
}
