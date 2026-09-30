package storage

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrent pages at one type state share one count.
func TestF2CountCacheSharesOneCount(t *testing.T) {
	var c f2CountCache
	var loads atomic.Int32
	release := make(chan struct{})
	load := func() (int64, error) {
		loads.Add(1)
		<-release
		return 42, nil
	}
	var wg sync.WaitGroup
	got := make([]int64, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, err := c.count("k", "m1", load)
			if err != nil {
				t.Error(err)
			}
			got[i] = n
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d counts ran for 8 concurrent pages, want 1", n)
	}
	for i, n := range got {
		if n != 42 {
			t.Fatalf("page %d got %d", i, n)
		}
	}
}

// A type that moved waits f2CountWait for its recount, then answers its
// recent count while the one recount runs; the recount's answer is kept for
// the new state.
func TestF2CountCacheAnswersWhileItRecounts(t *testing.T) {
	var c f2CountCache
	if n, err := c.count("k", "m1", func() (int64, error) { return 10, nil }); err != nil || n != 10 {
		t.Fatalf("first count: %d, %v", n, err)
	}
	release := make(chan struct{})
	var loads atomic.Int32
	recount := func() (int64, error) {
		loads.Add(1)
		<-release
		return 11, nil
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		if n, err := c.count("k", "m2", recount); err != nil || n != 10 {
			t.Fatalf("during the recount: %d, %v; want the last count 10", n, err)
		}
		if d := time.Since(start); d < f2CountWait || d > f2CountWait+time.Second {
			t.Fatalf("answered after %v during the recount, want about %v", d, f2CountWait)
		}
	}
	close(release)
	c.wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d recounts, want 1", n)
	}
	if n, err := c.count("k", "m2", func() (int64, error) { t.Fatal("recounted a kept state"); return 0, nil }); err != nil || n != 11 {
		t.Fatalf("after the recount: %d, %v", n, err)
	}
}

// A recount that answers within f2CountWait is the page's answer (a page read
// after a write counts it); a count older than f2CountStaleFor does not answer
// for a moved type: the page waits for the recount.
func TestF2CountCacheWaitsPastTheStaleBound(t *testing.T) {
	var c f2CountCache
	if _, err := c.count("k", "m1", func() (int64, error) { return 10, nil }); err != nil {
		t.Fatal(err)
	}
	if n, err := c.count("k", "m2", func() (int64, error) { return 11, nil }); err != nil || n != 11 {
		t.Fatalf("a quick recount: %d, %v; want 11", n, err)
	}
	c.mu.Lock()
	c.entries["k"].at = time.Now().Add(-2 * f2CountStaleFor)
	c.mu.Unlock()
	slow := func() (int64, error) { time.Sleep(2 * f2CountWait); return 12, nil }
	if n, err := c.count("k", "m3", slow); err != nil || n != 12 {
		t.Fatalf("an old count for a moved type: %d, %v; want the recount 12", n, err)
	}
}

// A failed count is returned to its waiters and kept for no state.
func TestF2CountCacheKeepsNoFailedCount(t *testing.T) {
	var c f2CountCache
	boom := errors.New("lane gone")
	if _, err := c.count("k", "m1", func() (int64, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v, want %v", err, boom)
	}
	if n, err := c.count("k", "m1", func() (int64, error) { return 7, nil }); err != nil || n != 7 {
		t.Fatalf("after a failed count: %d, %v", n, err)
	}
}

// The cache holds f2CountCacheKeys keys and evicts the least recently used:
// a count read on every page outlives a burst of one-off keys.
func TestF2CountCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	var c f2CountCache
	one := func() (int64, error) { return 1, nil }
	if _, err := c.count("hot", "m", one); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3*f2CountCacheKeys; i++ {
		if _, err := c.count(fmt.Sprintf("once-%d", i), "m", one); err != nil {
			t.Fatal(err)
		}
		if _, err := c.count("hot", "m", func() (int64, error) { return 0, errors.New("the hot count was evicted") }); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > f2CountCacheKeys {
		t.Fatalf("%d keys held, want at most %d", len(c.entries), f2CountCacheKeys)
	}
}
