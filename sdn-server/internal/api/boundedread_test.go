package api

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The property under test is the one the live defect violated: a read whose
// loader is blocked behind the ingest writer must still ANSWER, from what it
// last knew, inside its budget.

func TestBoundedReaderAnswersWithinBudgetWhenLoadBlocks(t *testing.T) {
	b := newBoundedReader(8)

	// Prime a last-known-good value with a fast load.
	first := b.read("k", time.Second, 0, func() (interface{}, error) { return "good", nil })
	if !first.OK || !first.Fresh || first.Value.(string) != "good" {
		t.Fatalf("priming read: got %+v, want fresh OK value", first)
	}

	// Now the store blocks, as it does mid-ingest.
	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	second := b.read("k", 100*time.Millisecond, 0, func() (interface{}, error) {
		<-release
		return "newer", nil
	})
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("blocked read took %s; a public read must not wait on the writer", elapsed)
	}
	if !second.OK {
		t.Fatal("blocked read returned no value; last-known-good was available")
	}
	if second.Fresh {
		t.Fatal("blocked read reported Fresh; it served a cached value")
	}
	if got := second.Value.(string); got != "good" {
		t.Fatalf("blocked read value = %q, want the last-known-good %q", got, "good")
	}
	if second.AsOf.IsZero() {
		t.Fatal("stale answer carried no as-of timestamp; a stale value must say when it was true")
	}
}

func TestBoundedReaderReportsNothingKnownWhenColdAndBlocked(t *testing.T) {
	b := newBoundedReader(8)
	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	res := b.read("cold", 80*time.Millisecond, 0, func() (interface{}, error) {
		<-release
		return "never seen", nil
	})
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("cold blocked read did not respect its budget")
	}
	if res.OK {
		t.Fatalf("cold blocked read claimed a value: %+v", res)
	}
}

// An abandoned load is abandoned by the CALLER only. Its result must be kept,
// or a permanently-busy store means a permanently-degraded surface — the exact
// failure of the hand-rolled goroutine+timer pattern this replaces.
func TestBoundedReaderKeepsLateResult(t *testing.T) {
	b := newBoundedReader(8)
	release := make(chan struct{})

	res := b.read("k", 50*time.Millisecond, 0, func() (interface{}, error) {
		<-release
		return "late", nil
	})
	if res.OK {
		t.Fatal("expected the first read to give up with nothing known")
	}

	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := b.read("k", 50*time.Millisecond, time.Minute, func() (interface{}, error) {
			t.Error("loader ran again; the late result should have satisfied this read")
			return nil, nil
		})
		if got.OK && got.Value.(string) == "late" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("late load result was discarded instead of cached")
}

// Single flight: a poll storm against a blocked store must not multiply the
// load on the very thing it is waiting for.
func TestBoundedReaderIsSingleFlight(t *testing.T) {
	b := newBoundedReader(8)
	release := make(chan struct{})
	var loads int64

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.read("k", 50*time.Millisecond, 0, func() (interface{}, error) {
				atomic.AddInt64(&loads, 1)
				<-release
				return "v", nil
			})
		}()
	}
	wg.Wait()
	close(release)

	if n := atomic.LoadInt64(&loads); n != 1 {
		t.Fatalf("blocked store saw %d concurrent loads, want exactly 1", n)
	}
}

// A young value is served with no load at all — that is what keeps the refresh
// off the hot path.
func TestBoundedReaderMinRefreshSuppressesLoad(t *testing.T) {
	b := newBoundedReader(8)
	var loads int64
	loader := func() (interface{}, error) {
		atomic.AddInt64(&loads, 1)
		return "v", nil
	}

	for i := 0; i < 10; i++ {
		res := b.read("k", time.Second, time.Minute, loader)
		if !res.OK {
			t.Fatalf("read %d returned nothing", i)
		}
	}
	if n := atomic.LoadInt64(&loads); n != 1 {
		t.Fatalf("loader ran %d times under a 1m min-refresh, want 1", n)
	}
}

// A transient store error must never blank a surface that already had an answer.
func TestBoundedReaderKeepsValueAcrossLoadError(t *testing.T) {
	b := newBoundedReader(8)
	if res := b.read("k", time.Second, 0, func() (interface{}, error) { return "good", nil }); !res.OK {
		t.Fatal("priming read failed")
	}
	res := b.read("k", time.Second, 0, func() (interface{}, error) { return nil, errors.New("store busy") })
	if !res.OK || res.Value.(string) != "good" {
		t.Fatalf("after a failed load: %+v, want the previous good value retained", res)
	}
}

// Distinct queries must not share a cache slot, or one query's rows get served
// under another query's parameters.
func TestBoundedReaderKeysAreIndependent(t *testing.T) {
	b := newBoundedReader(8)
	a := b.read("a", time.Second, 0, func() (interface{}, error) { return "A", nil })
	c := b.read("c", time.Second, 0, func() (interface{}, error) { return "C", nil })
	if a.Value.(string) != "A" || c.Value.(string) != "C" {
		t.Fatalf("keys collided: a=%v c=%v", a.Value, c.Value)
	}
}

// The key map is bounded: an anonymous parameterized surface cannot be turned
// into unbounded memory growth.
func TestBoundedReaderEvictsWhenFull(t *testing.T) {
	b := newBoundedReader(4)
	for i := 0; i < 40; i++ {
		key := string(rune('a'+i%26)) + string(rune('0'+i/26))
		b.read(key, time.Second, 0, func() (interface{}, error) { return i, nil })
	}
	b.mu.Lock()
	n := len(b.entries)
	b.mu.Unlock()
	if n > 4 {
		t.Fatalf("cache holds %d keys, want at most 4", n)
	}
}

// A nil cache (struct-literal handler) degrades to a direct load, never a panic.
func TestBoundedReaderNilIsSafe(t *testing.T) {
	var b *boundedReader
	res := b.read("k", time.Second, 0, func() (interface{}, error) { return "v", nil })
	if !res.OK || res.Value.(string) != "v" {
		t.Fatalf("nil cache read: %+v", res)
	}
}

// An endpoint that makes N store reads must cost ONE budget, not N.
func TestBoundedReaderReadAllSharesOneBudget(t *testing.T) {
	b := newBoundedReader(8)
	release := make(chan struct{})
	defer close(release)

	blocked := func() (interface{}, error) { <-release; return "x", nil }

	start := time.Now()
	res := b.readAll(150*time.Millisecond, 0,
		boundedRequest{Key: "a", Load: blocked},
		boundedRequest{Key: "b", Load: blocked},
		boundedRequest{Key: "c", Load: blocked},
	)
	elapsed := time.Since(start)

	if elapsed > 400*time.Millisecond {
		t.Fatalf("three blocked reads took %s; they must share one budget, not queue", elapsed)
	}
	if len(res) != 3 {
		t.Fatalf("readAll returned %d results, want 3", len(res))
	}
	for k, r := range res {
		if r.OK {
			t.Fatalf("key %q claimed a value it never loaded: %+v", k, r)
		}
	}
}

func TestBoundedReaderReadAllReturnsFreshValues(t *testing.T) {
	b := newBoundedReader(8)
	res := b.readAll(time.Second, 0,
		boundedRequest{Key: "a", Load: func() (interface{}, error) { return 1, nil }},
		boundedRequest{Key: "b", Load: func() (interface{}, error) { return 2, nil }},
	)
	if !res["a"].Fresh || res["a"].Value.(int) != 1 {
		t.Fatalf("a = %+v", res["a"])
	}
	if !res["b"].Fresh || res["b"].Value.(int) != 2 {
		t.Fatalf("b = %+v", res["b"])
	}
}

// A mix of one fast and one blocked read answers at the speed of the budget,
// with the fast one fresh and the slow one falling back.
func TestBoundedReaderReadAllMixesFreshAndStale(t *testing.T) {
	b := newBoundedReader(8)
	if r := b.read("slow", time.Second, 0, func() (interface{}, error) { return "old", nil }); !r.OK {
		t.Fatal("priming failed")
	}
	release := make(chan struct{})
	defer close(release)

	res := b.readAll(120*time.Millisecond, 0,
		boundedRequest{Key: "fast", Load: func() (interface{}, error) { return "new", nil }},
		boundedRequest{Key: "slow", Load: func() (interface{}, error) { <-release; return "newer", nil }},
	)
	if !res["fast"].Fresh || res["fast"].Value.(string) != "new" {
		t.Fatalf("fast = %+v, want a fresh value", res["fast"])
	}
	if res["slow"].Fresh || res["slow"].Value.(string) != "old" {
		t.Fatalf("slow = %+v, want the last-known-good value", res["slow"])
	}
}
