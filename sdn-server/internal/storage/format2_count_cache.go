package storage

// format2_count_cache.go — the record index page's window counts on store
// format 2. A count the lane counters cannot prove walks every posting of its
// tag: seconds for host-02's MPE and OMM sources, and every commit to the type
// moves the state the count was read at. So:
//
//   - one count per key runs at a time, and concurrent pages share it (six
//     identical pages after one commit each ran their own 2.6 s count, 16 s
//     in all on the one bulk lane, and unrelated reads queued behind them);
//   - a key whose type moved waits f2CountWait for its recount, then answers
//     its last count, when that count is recent (f2CountStaleFor), while the
//     recount finishes in the background: a page waits at most f2CountWait on
//     a count it already has an answer for. A small type's recount answers
//     within the wait, so a page read after a write counts it; a count that
//     walks millions of postings under a running ingest lags it by one
//     recount (the page's rows never lag);
//   - it holds at most f2CountCacheKeys keys and evicts the least recently
//     used, so the expensive counts outlive a burst of one-off keys.

import (
	"sync"
	"time"
)

const (
	// f2CountCacheKeys bounds the keys the cache holds.
	f2CountCacheKeys = 256
	// f2CountWait is how long a page waits for its recount before it answers
	// the last count.
	f2CountWait = 250 * time.Millisecond
	// f2CountStaleFor is how old a count may be and still answer while its
	// recount runs. Past it the page waits for the recount.
	f2CountStaleFor = time.Minute
)

type f2CountCache struct {
	mu      sync.Mutex
	entries map[string]*f2CountEntry
	tick    uint64
	// recounts are the counts running; Close waits for them after it
	// cancels their context.
	recounts sync.WaitGroup
}

type f2CountEntry struct {
	mark    string
	n       int64
	at      time.Time // when the count answered (zero: none yet)
	used    uint64
	pending *f2CountRun
}

// f2CountRun is one count in flight; its waiters read n and err once done
// is closed.
type f2CountRun struct {
	done chan struct{}
	n    int64
	err  error
}

// count answers key's count at mark: the kept count when it was read at
// mark; else load's answer (one load per key at a time, however many callers
// wait on it), or, when load takes longer than f2CountWait, the kept count if
// it is younger than f2CountStaleFor, while load finishes in the background.
func (c *f2CountCache) count(key, mark string, load func() (int64, error)) (int64, error) {
	now := time.Now()
	c.mu.Lock()
	e := c.entryLocked(key)
	if !e.at.IsZero() && e.mark == mark {
		n := e.n
		c.mu.Unlock()
		return n, nil
	}
	run := e.pending
	if run == nil {
		run = &f2CountRun{done: make(chan struct{})}
		e.pending = run
		c.recounts.Add(1)
		go func() {
			defer c.recounts.Done()
			run.n, run.err = load()
			c.mu.Lock()
			if run.err == nil {
				e.mark, e.n, e.at = mark, run.n, time.Now()
			}
			e.pending = nil
			c.mu.Unlock()
			close(run.done)
		}()
	}
	if e.at.IsZero() || now.Sub(e.at) >= f2CountStaleFor {
		c.mu.Unlock()
		<-run.done
		return run.n, run.err
	}
	last := e.n
	c.mu.Unlock()
	t := time.NewTimer(f2CountWait)
	defer t.Stop()
	select {
	case <-run.done:
		return run.n, run.err
	case <-t.C:
		return last, nil
	}
}

// entryLocked returns key's entry, marked used, making room for it.
func (c *f2CountCache) entryLocked(key string) *f2CountEntry {
	c.tick++
	if e := c.entries[key]; e != nil {
		e.used = c.tick
		return e
	}
	if c.entries == nil {
		c.entries = map[string]*f2CountEntry{}
	}
	if len(c.entries) >= f2CountCacheKeys {
		var oldest string
		var at uint64
		for k, e := range c.entries {
			if e.pending == nil && (oldest == "" || e.used < at) {
				oldest, at = k, e.used
			}
		}
		if oldest != "" {
			delete(c.entries, oldest)
		}
	}
	e := &f2CountEntry{used: c.tick}
	c.entries[key] = e
	return e
}

// wait waits for the counts running (their context is cancelled first).
func (c *f2CountCache) wait() { c.recounts.Wait() }
