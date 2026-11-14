package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// The format-4 write path that stops answering. On 2026-11-14 a node's
// engine (flatsql 3.12.1, head 70026dcb) trapped, was reopened, and then
// stopped answering writes for good while reads went on; the in-process
// reproducer (TestFormat4ConcurrentFlowLoad) showed a SUPERSEDE unanswered
// for 4m33s after a reopen, the engine's writer looping without a trap, so
// nothing fenced it. DropNextWriteForTest makes the next write request one
// the engine never sees: the same wait, on demand.

// f4HangEngines records the engines a store opens and the failures they
// report, with the write-path bounds of opt applied to each.
type f4HangEngines struct {
	mu       sync.Mutex
	engines  []*format4.Engine
	failures []error
}

func (h *f4HangEngines) last(t *testing.T) *format4.Engine {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.engines) == 0 {
		t.Fatal("no engine opened")
	}
	return h.engines[len(h.engines)-1]
}

func (h *f4HangEngines) counts() (opened, failed int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.engines), len(h.failures)
}

func (h *f4HangEngines) failure(i int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.failures) {
		return nil
	}
	return h.failures[i]
}

// onHangEngines wraps the test opener (inside onFormat4Engine).
func onHangEngines(t *testing.T, writeHang, deadline time.Duration) *f4HangEngines {
	h := &f4HangEngines{}
	inner := format4OpenEngine
	format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
		opt.WriteHang, opt.RequestDeadline = writeHang, deadline
		failure := opt.OnFailure
		opt.OnFailure = func(err error) {
			h.mu.Lock()
			h.failures = append(h.failures, err)
			h.mu.Unlock()
			if failure != nil {
				failure(err)
			}
		}
		api, err := inner(ctx, opt)
		if e, ok := api.(*format4.Engine); ok && err == nil {
			h.mu.Lock()
			h.engines = append(h.engines, e)
			h.mu.Unlock()
		}
		return api, err
	}
	t.Cleanup(func() { format4OpenEngine = inner })
	return h
}

// A supersede the engine never answers, on the first engine and again on
// the engine reopened after it: each time the write-hang bound fences the
// engine, the store reopens it through its trap path, and the next writes
// are answered.
func TestFormat4WriteHangFencesAndReopens(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		const bound = 2 * time.Second // above any healthy test write on a loaded machine
		h := onHangEngines(t, bound, -1)
		s := openFormat4ForTest(t, t.TempDir())
		defer s.Close()
		sc := newF2Script()
		write := func(i int) error {
			_, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[i:i+1], "peer-a", nil,
				SourceTags{ProviderID: "p", SourceName: "s", BatchID: fmt.Sprintf("b%d", i)})
			return err
		}
		if err := write(0); err != nil {
			t.Fatalf("first write: %v", err)
		}
		for round := 1; round <= 2; round++ {
			h.last(t).DropNextWriteForTest()
			start := time.Now()
			_, err := s.ReconcileSourceBatch("OMM.fbs", "p", "s", fmt.Sprintf("b%d", round-1), true)
			took := time.Since(start)
			if err == nil {
				t.Fatalf("round %d: the unanswered supersede returned no error", round)
			}
			if took < bound || took > bound+10*time.Second {
				t.Fatalf("round %d: the unanswered supersede returned after %s (bound %s): %v", round, took, bound, err)
			}
			// The store reopens the engine (format4Daemon.onFailure).
			deadline := time.Now().Add(60 * time.Second)
			for {
				opened, _ := h.counts()
				if opened == round+1 && !s.f4.reopening.Load() {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("round %d: not reopened within 60 s (engines opened %d)", round, opened)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if f := h.failure(round - 1); !errors.Is(f, format4.ErrWriteHang) {
				t.Fatalf("round %d: OnFailure got %v, want format4.ErrWriteHang", round, f)
			}
			// Writes are answered again, by the reopened engine.
			for i := 0; i < 3; i++ {
				if err := write(10*round + i); err != nil {
					t.Fatalf("round %d: write %d after the reopen: %v", round, i, err)
				}
			}
			if _, err := s.ReconcileSourceBatch("OMM.fbs", "p", "s", fmt.Sprintf("b%d", 10*round+2), true); err != nil {
				t.Fatalf("round %d: supersede after the reopen: %v", round, err)
			}
		}
		if opened, failed := h.counts(); opened != 3 || failed != 2 {
			t.Fatalf("engines opened %d, failures %d; want 3 and 2", opened, failed)
		}
	})
}

// With the hang fence off, the request deadline still bounds a write the
// engine never answers although the store calls with its lifetime context;
// the engine is not fenced and answers the next write.
func TestFormat4RequestDeadlineBoundsStoreWrites(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		const deadline = 2 * time.Second
		h := onHangEngines(t, -1, deadline)
		s := openFormat4ForTest(t, t.TempDir())
		defer s.Close()
		sc := newF2Script()
		tags := SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b0"}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:1], "peer-a", nil, tags); err != nil {
			t.Fatalf("first write: %v", err)
		}
		h.last(t).DropNextWriteForTest()
		start := time.Now()
		_, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[1:2], "peer-a", nil, tags)
		took := time.Since(start)
		if !errors.Is(err, format4.ErrUnanswered) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unanswered write: err = %v, want format4.ErrUnanswered", err)
		}
		if took < deadline || took > deadline+10*time.Second {
			t.Fatalf("unanswered write returned after %s (deadline %s)", took, deadline)
		}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[2:3], "peer-a", nil, tags); err != nil {
			t.Fatalf("write after the deadline: %v", err)
		}
		if opened, failed := h.counts(); opened != 1 || failed != 0 {
			t.Fatalf("engines opened %d, failures %d; want 1 and 0", opened, failed)
		}
	})
}
