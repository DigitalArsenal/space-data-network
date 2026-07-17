package flowrt

// pool.go — FlowPool runs N resident instances of the SAME baked runtime.wasm so a
// flow drains many objects IN PARALLEL. A single FlowRuntime.Drain is serialized
// (it holds rt.mu for the whole drain), and one flow instance's ingress + 64 MiB
// guest heap can't hold a whole constellation at once — so parallelism comes from a
// POOL of independent instances, each with its own linear memory, exactly mirroring
// sdnruns.ReactorFitter (sdn/sdnruns/fit.go:372) + Runner.fitObjects
// (sdn/sdnruns/runner.go:266). Each Run() acquires a free instance behind a buffered
// free-channel semaphore, resets it, fires a trigger, drains, and releases it.
//
// This is a scheduling capability primitive (like the host cron/HTTP primitives),
// NOT flow business logic — the per-object flow ($PLG) is the unit of work; the pool
// just runs Concurrency() copies of it at once.

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// FlowPool is a fixed-size pool of resident FlowRuntime instances over one baked
// runtime.wasm. Safe for concurrent use by up to Concurrency() goroutines.
type FlowPool struct {
	wasm     []byte
	maxPages uint32
	n        int

	mu     sync.Mutex
	loaded bool
	free   chan *FlowRuntime // buffered to n: semaphore + free list
	all    []*FlowRuntime
}

// NewFlowPool builds a pool over wasm (a baked flow runtime.wasm). n is the number
// of resident instances (concurrent drains); n <= 0 selects runtime.NumCPU().
// Instances are created lazily on the first Run.
func NewFlowPool(wasm []byte, n int, maxMemoryPages uint32) *FlowPool {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	if n < 1 {
		n = 1
	}
	return &FlowPool{wasm: wasm, n: n, maxPages: maxMemoryPages}
}

// Concurrency reports the resident-instance pool size.
func (p *FlowPool) Concurrency() int { return p.n }

// ensureLoaded instantiates the n resident FlowRuntime instances once.
func (p *FlowPool) ensureLoaded() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return nil
	}
	if len(p.wasm) == 0 {
		return fmt.Errorf("flowrt: FlowPool has no runtime.wasm")
	}
	free := make(chan *FlowRuntime, p.n)
	all := make([]*FlowRuntime, 0, p.n)
	for i := 0; i < p.n; i++ {
		rt, err := NewFlowRuntime(p.wasm, p.maxPages)
		if err != nil {
			return fmt.Errorf("flowrt: FlowPool instance %d: %w", i, err)
		}
		all = append(all, rt)
		free <- rt
	}
	p.free = free
	p.all = all
	p.loaded = true
	return nil
}

// Run acquires a free instance, resets its state, fires triggerIndex, and drains it,
// returning the DrainResult. It blocks until an instance is free or ctx is done.
func (p *FlowPool) Run(ctx context.Context, triggerIndex uint32, handlers HandlerMap, opts DrainOptions) (*DrainResult, error) {
	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}
	var rt *FlowRuntime
	select {
	case rt = <-p.free:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { p.free <- rt }()

	rt.ResetState()
	rt.EnqueueTrigger(triggerIndex)
	return rt.Drain(ctx, handlers, opts)
}

// Instances exposes the resident runtimes (read-only inspection, e.g. tests).
func (p *FlowPool) Instances() []*FlowRuntime {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*FlowRuntime, len(p.all))
	copy(out, p.all)
	return out
}
