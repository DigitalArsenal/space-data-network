package flatsqlrt

// The T5 spike (design §18 T5 #1, §21 "evidence still missing"): parallel AOT
// guest threads, concurrent control calls on several instances, and a
// shared-memory base that does not move on memory.grow.

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// T5 #1: 4 AOT threads doing CPU-bound work finish in <= 0.3x the one-thread
// wall time (acceptance machine: a 4-core Linux box; this reports the machine
// it ran on).
func TestParallelAOTThreadsScale(t *testing.T) {
	requirePatched(t)
	if runtime.NumCPU() < 4 {
		t.Skipf("needs 4 CPUs, have %d", runtime.NumCPU())
	}
	p := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 0}, probeOpts{control: time.Minute})
	defer p.Stop()
	// Calibrate so one thread runs ~1 s.
	iters := int64(1 << 22)
	for {
		start := time.Now()
		if _, err := p.Control("probe_cpu", iters); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) > 250*time.Millisecond || iters > 1<<34 {
			iters = int64(float64(iters) * float64(time.Second) / float64(time.Since(start)))
			break
		}
		iters *= 4
	}
	iters -= iters % 4
	best := func(f func() error) time.Duration {
		var min time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			if err := f(); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); min == 0 || d < min {
				min = d
			}
		}
		return min
	}
	one := best(func() error { _, err := p.Control("probe_cpu", iters); return err })
	four := best(func() error {
		v, err := p.Control("probe_parallel", int32(4), iters/4)
		if err == nil && v[0].(int64) < 0 {
			err = fmt.Errorf("probe_parallel returned %d", v[0])
		}
		return err
	})
	ratio := float64(four) / float64(one)
	t.Logf("%s/%s, %d CPUs: 1 thread %s, 4 threads %s for the same work: ratio %.3f (acceptance <= 0.3)",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), one, four, ratio)
	if ratio > 0.3 && perfGate() {
		t.Fatalf("4 AOT threads took %.3fx the one-thread time", ratio)
	}
}

// The shared-memory base the host reads through must not move when the guest
// grows its memory (design §5.4 completion pollers).
func TestSharedMemoryBaseStableAcrossGrow(t *testing.T) {
	requirePatched(t)
	p := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 2}, probeOpts{})
	defer p.Stop()
	before, err := p.Module().SharedMemory()
	if err != nil {
		t.Fatal(err)
	}
	pages := before.Pages()
	gen0, _ := p.Load32(p.Layout().MemGen)
	for i := 0; i < 8; i++ {
		v, err := p.Control("probe_grow", int32(64))
		if err != nil || wasmrt.ToInt32(v[0]) < 0 {
			t.Fatalf("grow: %v %v", v, err)
		}
	}
	after, err := p.Module().SharedMemory()
	if err != nil {
		t.Fatal(err)
	}
	if after.Pages() != pages+8*64 {
		t.Fatalf("pages %d -> %d", pages, after.Pages())
	}
	if !before.BaseStable() || before.Base() != after.Base() {
		t.Fatalf("memory base moved: %#x -> %#x", before.Base(), after.Base())
	}
	if gen, _ := p.Load32(p.Layout().MemGen); gen != gen0+8 {
		t.Fatalf("generation word %d -> %d", gen0, gen)
	}
	// A word in the grown region: the guest writes it, the host reads it
	// through the old base.
	addr := uint32(pages*65536 + 4096)
	if _, err := p.Control("probe_store", int32(addr), int32(0x5DAB1E)); err != nil {
		t.Fatal(err)
	}
	if got := after.Load32(addr); got != 0x5DAB1E {
		t.Fatalf("grown-region word %#x", got)
	}
	// The doorbell still works after the grow.
	if err := p.Ring(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.WaitAck(ctx, 0, 1); err != nil {
		t.Fatal(err)
	}
}

// T5 #1 (second half): a writer and two reader probe instances, each with
// service threads, take concurrent control calls, doorbells and acks.
// Acceptance: 30 min with 0 traps (SDN_PS_SOAK=30m); default 5 s.
func TestConcurrentControlCallsOnThreeInstances(t *testing.T) {
	requirePatched(t)
	dur := envDuration(t, "SDN_PS_SOAK", 5*time.Second)
	st, _ := newTestStore(t)
	roles := []PSRole{PSRoleWriter, PSRoleReader, PSRoleReader}
	insts := make([]*PSInstance, len(roles))
	for i, role := range roles {
		insts[i] = openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 3}, probeOpts{role: role, store: st, stale: 10 * time.Second})
	}
	defer func() {
		for _, p := range insts {
			p.Stop()
		}
	}()
	var calls, rings, acks atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	deadline := time.Now().Add(dur)
	for i, p := range insts {
		// Two goroutines of control calls per instance.
		for c := 0; c < 2; c++ {
			wg.Add(1)
			go func(p *PSInstance, c int) {
				defer wg.Done()
				for time.Now().Before(deadline) {
					var err error
					if c == 0 {
						_, err = p.Control("probe_cpu", int64(20000))
					} else {
						_, err = p.Control("flatsql_ps_pump", float64(100))
					}
					if err != nil {
						errs <- err
						return
					}
					calls.Add(1)
				}
			}(p, c)
		}
		// One goroutine of doorbell rings and acks per writer thread.
		for w := 0; w < 3; w++ {
			wg.Add(1)
			go func(i int, p *PSInstance, w int) {
				defer wg.Done()
				for n := uint64(1); time.Now().Before(deadline); n++ {
					if err := p.Ring(w); err != nil {
						errs <- err
						return
					}
					rings.Add(1)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					// pump may have acked past n already; the ack only rises.
					err := p.WaitAck(ctx, w, n)
					cancel()
					if err != nil {
						errs <- fmt.Errorf("instance %d writer %d ring %d: %w", i, w, n, err)
						return
					}
					acks.Add(1)
				}
			}(i, p, w)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i, p := range insts {
		s := p.Stats()
		if s.Poisoned || p.Failure() != nil || s.Threads.Live != 3 {
			t.Errorf("instance %d after %s: %+v failure=%v", i, dur, s, p.Failure())
		}
	}
	t.Logf("%s: %d control calls, %d rings, %d acks across 3 instances, 0 traps", dur, calls.Load(), rings.Load(), acks.Load())
}
