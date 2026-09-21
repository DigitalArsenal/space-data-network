package wasmrt

import (
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
	g := &wasiThreadGroup{owner: m, ast: ast, next: 1, workers: make(map[int32]*wasiWorker), failure: make(chan struct{})}
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
	fail := func() ([]interface{}, wasmedge.Result) { return []interface{}{int32(-1)}, wasmedge.Result_Success }
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.owner.Poisoned() || len(g.workers) >= 32 || g.next >= 1<<29 {
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
	go func() {
		_, err := async.GetResult()
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
func (g *wasiThreadGroup) stop() bool {
	g.mu.Lock()
	g.closed = true
	pending := make([]*wasiWorker, 0, len(g.workers))
	for _, w := range g.workers {
		w.async.Cancel()
		pending = append(pending, w)
	}
	g.mu.Unlock()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for _, w := range pending {
		select {
		case <-w.done:
		case <-deadline.C:
			return false
		}
	}
	return true
}
