package wasmrt

// Doorbells and completion pollers (design §5.4, A24, A29).
//
// DOORBELL. Each writer thread owns a {seq u32, sleeping u32} pair in shared
// memory. The guest loads seq, scans for work, and when idle sets sleeping,
// re-checks seq, and waits on seq with expected = the value it loaded, so an
// increment that lands at any point makes the wait return at once: no wakeup
// is ever lost, notify or not. The host rings by adding to seq and asks for a
// notify only when the thread says it is sleeping. Notifies are coalesced and
// issued by ONE doorbell thread per instance (a C pthread, so waking it is one
// futex wake), which invokes the guest's tiny wake export
// (memory.atomic.notify) on the instance's own executor, where the waiters
// are registered. It never runs inside a host function.
//
// COMPLETION POLLER. Waiters for an ack word (a u64 the guest advances
// monotonically) are completed by one goroutine per instance that reads the
// words every interval while any waiter exists, and parks otherwise. No
// import, no guest call.

/*
#cgo CFLAGS: -std=gnu11 -O2
#include "doorbell_native.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// ErrClosed is returned by a doorbell or poller that has been closed.
var ErrClosed = errors.New("wasmrt: closed")

// exportedFunction finds a function exported by the module's main instance.
func (m *Module) exportedFunction(name string) *wasmedge.Function {
	var inst *wasmedge.Module
	if m.registeredName != "" {
		inst = m.vm.GetRegisteredModule(m.registeredName)
	} else {
		inst = m.vm.GetActiveModule()
	}
	if inst == nil {
		return nil
	}
	return inst.FindFunction(name)
}

// DoorbellStats counts ring outcomes.
type DoorbellStats struct {
	Rings     int64 // Ring calls
	Requested int64 // rings that found the thread sleeping and asked for a notify
	Signals   int64 // requests that had to wake the parked doorbell thread
	Notifies  int64 // wake-export invocations issued
	Errors    int64 // wake-export invocations that failed
}

// Doorbell rings a module's service threads (see the package note above).
// The doorbell thread itself is C (doorbell_native.c).
type Doorbell struct {
	mem   *SharedMemory
	db    *C.sdn_doorbell
	base  uint32
	count int
	once  sync.Once
	mu    sync.RWMutex // Ring/NotifyAll (readers) against Close (writer)
	done  bool
	rings atomic.Int64
}

// StartDoorbell serves count doorbells at base ({seq, sleeping} u32 pairs)
// through the module's wakeExport(addr i32, n i32) export.
func (m *Module) StartDoorbell(mem *SharedMemory, wakeExport string, base uint32, count int) (*Doorbell, error) {
	if m == nil || m.vm == nil {
		return nil, ErrNoModule
	}
	fn := m.exportedFunction(wakeExport)
	if fn == nil {
		return nil, fmt.Errorf("wasmrt: module does not export %q", wakeExport)
	}
	for i := 0; i < count; i++ {
		if err := mem.Check(base+uint32(8*i), 8); err != nil {
			return nil, err
		}
	}
	exec, err := innerHandle(m.vm.GetExecutor(), "WasmEdge_ExecutorContext")
	if err != nil {
		return nil, err
	}
	wake, err := innerHandle(fn, "WasmEdge_FunctionInstanceContext")
	if err != nil {
		return nil, err
	}
	db := C.sdn_doorbell_start(exec, wake, C.uint32_t(base), C.uint32_t(count))
	if db == nil {
		return nil, errors.New("wasmrt: cannot start the doorbell thread")
	}
	return &Doorbell{mem: mem, db: db, base: base, count: count}, nil
}

func (d *Doorbell) seqAddr(i int) uint32 { return d.base + uint32(8*i) }

// Ring advances doorbell i's sequence and, if its thread is sleeping, asks the
// doorbell thread to notify it. Never blocks on the guest.
func (d *Doorbell) Ring(i int) {
	if i < 0 || i >= d.count {
		return
	}
	d.rings.Add(1)
	d.mem.Add32(d.seqAddr(i), 1)
	if d.mem.Load32(d.seqAddr(i)+4) == 0 {
		return
	}
	d.mu.RLock()
	if !d.done {
		C.sdn_doorbell_request(d.db, C.uint32_t(i))
	}
	d.mu.RUnlock()
}

// Sleeping reports whether doorbell i's thread says it is waiting.
func (d *Doorbell) Sleeping(i int) bool { return d.mem.Load32(d.seqAddr(i)+4) != 0 }

// Seq returns doorbell i's current sequence value.
func (d *Doorbell) Seq(i int) uint32 { return d.mem.Load32(d.seqAddr(i)) }

// NotifyAll wakes every waiter on addr now, on the calling thread (used when
// stopping an instance; never call it from inside a host function). Returns
// ErrClosed after Close.
func (d *Doorbell) NotifyAll(addr uint32) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.done {
		return ErrClosed
	}
	if C.sdn_doorbell_notify_now(d.db, C.uint32_t(addr), C.int32_t(1<<30)) != 0 {
		return errors.New("wasmrt: wake export failed")
	}
	return nil
}

// Stats snapshots the doorbell's counters.
func (d *Doorbell) Stats() DoorbellStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	st := DoorbellStats{Rings: d.rings.Load()}
	if d.done {
		return st
	}
	var req, sig, not, errs C.uint64_t
	C.sdn_doorbell_stats(d.db, &req, &sig, &not, &errs)
	st.Requested, st.Signals, st.Notifies, st.Errors = int64(req), int64(sig), int64(not), int64(errs)
	return st
}

// Close stops the doorbell thread and waits for it.
func (d *Doorbell) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.mu.Lock()
		d.done = true
		d.mu.Unlock()
		C.sdn_doorbell_stop(d.db)
	})
}

// CompletionPoller completes waiters on monotonically increasing u64 words.
type CompletionPoller struct {
	mem      *SharedMemory
	interval time.Duration

	mu      sync.Mutex
	waiters map[*ackWaiter]struct{}
	kick    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once

	sweeps atomic.Int64
}

type ackWaiter struct {
	off    uint32
	target uint64
	done   chan struct{}
}

// StartCompletionPoller polls every interval while waiters exist.
func StartCompletionPoller(mem *SharedMemory, interval time.Duration) *CompletionPoller {
	if interval <= 0 {
		interval = 250 * time.Microsecond
	}
	p := &CompletionPoller{mem: mem, interval: interval, waiters: make(map[*ackWaiter]struct{}),
		kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

// Wait returns once the u64 at off is at least target.
func (p *CompletionPoller) Wait(ctx context.Context, off uint32, target uint64) error {
	if err := p.mem.Check(off, 8); err != nil {
		return err
	}
	if p.mem.Load64(off) >= target {
		return nil
	}
	w := &ackWaiter{off: off, target: target, done: make(chan struct{})}
	p.mu.Lock()
	select {
	case <-p.stop:
		p.mu.Unlock()
		return ErrClosed
	default:
	}
	p.waiters[w] = struct{}{}
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiters, w)
		p.mu.Unlock()
		return ctx.Err()
	case <-p.stop:
		return ErrClosed
	}
}

// Sweeps counts polling passes (a parked poller does none).
func (p *CompletionPoller) Sweeps() int64 { return p.sweeps.Load() }

func (p *CompletionPoller) run() {
	defer close(p.done)
	tick := time.NewTimer(p.interval)
	tick.Stop()
	defer tick.Stop()
	for {
		p.mu.Lock()
		n := len(p.waiters)
		p.mu.Unlock()
		if n == 0 {
			select {
			case <-p.stop:
				return
			case <-p.kick:
				continue
			}
		}
		p.sweeps.Add(1)
		p.mu.Lock()
		for w := range p.waiters {
			if p.mem.Load64(w.off) >= w.target {
				close(w.done)
				delete(p.waiters, w)
			}
		}
		p.mu.Unlock()
		tick.Reset(p.interval)
		select {
		case <-p.stop:
			return
		case <-tick.C:
		}
	}
}

// Close stops the poller; pending waiters get ErrClosed.
func (p *CompletionPoller) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.mu.Lock()
		close(p.stop)
		p.mu.Unlock()
	})
	<-p.done
}
