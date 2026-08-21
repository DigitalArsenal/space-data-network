package api

import (
	"sync"
	"time"
)

// A PUBLIC READ MUST NEVER WAIT ON THE WRITER.
//
// The record store is single-writer: a full CelesTrak ingest holds the write
// lock for tens of minutes on a small host, and every reader that takes the
// read lock queues behind it. Measured on host-01 mid-ingest, 2026-07-28:
// /api/v1/id answered in 63 ms while /api/v1/stats and /api/v1/data/index did
// not answer AT ALL — both were still open after 30 s and were cut off by the
// client, not by the server. A status surface that stops answering exactly
// when the node is busiest is worse than useless: "is it working?" is asked
// PRECISELY then.
//
// The same defect was already fixed twice by hand — the $APPS feed's PNM and
// producer refreshes (apps.go pnmRefreshBudget / remoteProducerBudget) and the
// cached /ws/status frame. Those fixes each spawned a goroutine, waited on a
// timer, and DISCARDED the late result, so the next request paid the full cost
// again and a permanently-busy store meant a permanently-degraded surface.
// boundedReader is that pattern done once, properly, for every surface that
// reads the store on an anonymous hot path:
//
//   - the caller waits at most `budget`;
//   - a slow load is abandoned by the CALLER, never cancelled — its result is
//     kept as the new last-known-good, so the wait is paid once, not per
//     request;
//   - single-flight per key: a flood of readers against a blocked store starts
//     ONE load, not one per request (the old pattern's worst failure — it
//     added load to the thing it was waiting on);
//   - a value younger than `minRefresh` is served with no load at all, which
//     is what keeps the refresh off the hot path;
//   - when the budget expires the last-known-good answer is served, marked
//     stale and stamped with the moment it was true.
//
// This is pure connector policy: it decides WHEN to stop waiting on storage,
// never what the answer means.

// boundedEntry is one cached answer and its in-flight load.
type boundedEntry struct {
	mu sync.Mutex
	// val/at/have are the last-known-good answer. A failed load never
	// destroys them: a transient store error must not blank a surface.
	val  interface{}
	at   time.Time
	have bool
	// done is non-nil exactly while a load is in flight; it is closed when
	// that load finishes. It is what makes this single-flight.
	done chan struct{}
	// used is the last time any reader touched this key, for eviction.
	used time.Time
}

// boundedReader is a keyed, single-flight, last-known-good cache.
//
// The zero value is NOT usable; construct with newBoundedReader.
type boundedReader struct {
	mu      sync.Mutex
	entries map[string]*boundedEntry
	// maxKeys bounds the map so a parameterized surface (the record index
	// takes schema/provider/source/batch/norad/page/limit) cannot be turned
	// into unbounded memory growth by an anonymous caller.
	maxKeys int
	// now supplies the timestamps that decide staleness and stamp answers.
	// nil = time.Now. It is injectable so tests decide staleness on LOGICAL
	// time — how much newer one answer is than the last — never on how long
	// the wall clock took between two requests. A loaded box must not decide
	// the gate: measured 2026-08-14, TestStatsReportsItsOwnStaleness passed in
	// 1.3s alone and FAILED inside the parallel package run at load ~18, when
	// the 2s min-refresh window lapsed while the scheduler starved the test
	// goroutine. See gauntlet-go-host-tier-tests-fail-under-machine-load.
	nowFn func() time.Time
}

// now is the reader's clock. Nil-safe so the nil-cache degrade path keeps
// working when an assembled-by-lit handler has no reader at all.
func (b *boundedReader) now() time.Time {
	if b != nil && b.nowFn != nil {
		return b.nowFn()
	}
	return time.Now()
}

// boundedReaderDefaultKeys is the key ceiling for a parameterized surface. The
// record index is a UI drill-down: a handful of schemas times a handful of
// pages is the real working set, and anything past that is either a crawler or
// an attack — both of which get correct answers, just not free memory.
const boundedReaderDefaultKeys = 256

func newBoundedReader(maxKeys int) *boundedReader {
	if maxKeys < 1 {
		maxKeys = boundedReaderDefaultKeys
	}
	return &boundedReader{entries: make(map[string]*boundedEntry), maxKeys: maxKeys}
}

// entryFor returns the entry for key, creating it if needed and evicting the
// least-recently-used entry when the map is full.
func (b *boundedReader) entryFor(key string) *boundedEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	if e, ok := b.entries[key]; ok {
		e.used = b.now()
		return e
	}

	if len(b.entries) >= b.maxKeys {
		var oldestKey string
		var oldest time.Time
		for k, e := range b.entries {
			// Never evict an entry with a load in flight: its goroutine
			// would then write into an entry nobody can read.
			if e.done != nil {
				continue
			}
			if oldestKey == "" || e.used.Before(oldest) {
				oldestKey, oldest = k, e.used
			}
		}
		if oldestKey != "" {
			delete(b.entries, oldestKey)
		}
	}

	e := &boundedEntry{used: b.now()}
	b.entries[key] = e
	return e
}

// boundedResult is what a bounded read could say within its budget.
type boundedResult struct {
	// Value is the answer, or nil when this surface has never once been read
	// successfully (a cold node whose store was busy from the first request).
	Value interface{}
	// OK reports whether Value holds anything at all.
	OK bool
	// Fresh reports whether Value was produced by THIS request's load. When
	// false the value is last-known-good and AsOf says how old it is.
	Fresh bool
	// AsOf is when Value was true. Zero when !OK.
	AsOf time.Time
}

// read serves key within budget.
//
// load is called on its own goroutine and is never cancelled — abandoning a
// store read does not make the store any less busy, and the result is worth
// keeping for the next caller. minRefresh suppresses the load entirely while
// the cached value is younger than it.
func (b *boundedReader) read(key string, budget, minRefresh time.Duration, load func() (interface{}, error)) boundedResult {
	// A handler assembled as a struct literal (tests do this) has no cache.
	// Degrade to a direct load rather than panicking: correctness first, and
	// the production constructors always supply one.
	if b == nil {
		val, err := load()
		if err != nil {
			return boundedResult{}
		}
		return boundedResult{Value: val, OK: true, Fresh: true, AsOf: b.now()}
	}

	e := b.entryFor(key)

	e.mu.Lock()
	// Young enough: answer from cache and do not touch the store at all.
	if e.have && minRefresh > 0 && b.now().Sub(e.at) < minRefresh {
		res := boundedResult{Value: e.val, OK: true, Fresh: false, AsOf: e.at}
		e.mu.Unlock()
		return res
	}
	if e.done == nil {
		e.done = make(chan struct{})
		go b.loadInto(e, load)
	}
	wait := e.done
	cached := boundedResult{Value: e.val, OK: e.have, Fresh: false, AsOf: e.at}
	e.mu.Unlock()

	if budget <= 0 {
		return cached
	}

	timer := time.NewTimer(budget)
	defer timer.Stop()

	select {
	case <-wait:
		e.mu.Lock()
		res := boundedResult{Value: e.val, OK: e.have, Fresh: e.have, AsOf: e.at}
		e.mu.Unlock()
		return res
	case <-timer.C:
		// The store is busy. Answer with what was last true rather than not
		// answering; the in-flight load keeps running and the NEXT caller
		// gets its result for free.
		return cached
	}
}

// loadInto runs one load and publishes its result, then releases the
// single-flight gate. A load error leaves the previous value standing.
func (b *boundedReader) loadInto(e *boundedEntry, load func() (interface{}, error)) {
	val, err := load()

	e.mu.Lock()
	if err == nil {
		e.val, e.at, e.have = val, b.now(), true
	}
	done := e.done
	e.done = nil
	e.mu.Unlock()

	if err != nil {
		log.Debugf("bounded read: load failed, keeping last-known-good: %v", err)
	}
	close(done)
}

// boundedRequest is one keyed load in a readAll batch.
type boundedRequest struct {
	Key  string
	Load func() (interface{}, error)
}

// readAll serves several keys under ONE shared budget.
//
// An endpoint that makes N store reads must not cost N budgets. Measured live
// on host-01 mid-ingest, 2026-07-28: /api/v1/stats makes two store reads, and
// serving them one after the other spent 1.55 s where the whole answer was
// available in 0.79 s. The budget belongs to the RESPONSE, not to each query
// inside it — the reads are independent, so they wait together.
func (b *boundedReader) readAll(budget, minRefresh time.Duration, reqs ...boundedRequest) map[string]boundedResult {
	out := make(map[string]boundedResult, len(reqs))
	if b == nil {
		for _, req := range reqs {
			out[req.Key] = b.read(req.Key, budget, minRefresh, req.Load)
		}
		return out
	}

	type inflight struct {
		key   string
		e     *boundedEntry
		wait  chan struct{}
		start boundedResult
	}
	var pending []inflight

	for _, req := range reqs {
		e := b.entryFor(req.Key)
		e.mu.Lock()
		if e.have && minRefresh > 0 && b.now().Sub(e.at) < minRefresh {
			out[req.Key] = boundedResult{Value: e.val, OK: true, Fresh: false, AsOf: e.at}
			e.mu.Unlock()
			continue
		}
		if e.done == nil {
			e.done = make(chan struct{})
			go b.loadInto(e, req.Load)
		}
		wait := e.done
		start := boundedResult{Value: e.val, OK: e.have, Fresh: false, AsOf: e.at}
		e.mu.Unlock()
		pending = append(pending, inflight{key: req.Key, e: e, wait: wait, start: start})
	}

	if len(pending) == 0 {
		return out
	}

	// Relay each in-flight completion onto one channel so a single timer can
	// bound them all. Each relay ends when its load ends; none outlive it.
	finished := make(chan int, len(pending))
	for i, p := range pending {
		go func(i int, ch <-chan struct{}) {
			<-ch
			finished <- i
		}(i, p.wait)
	}

	settled := make([]bool, len(pending))
	remaining := len(pending)
	timer := time.NewTimer(budget)
	defer timer.Stop()

wait:
	for remaining > 0 {
		select {
		case i := <-finished:
			p := pending[i]
			p.e.mu.Lock()
			out[p.key] = boundedResult{Value: p.e.val, OK: p.e.have, Fresh: p.e.have, AsOf: p.e.at}
			p.e.mu.Unlock()
			settled[i] = true
			remaining--
		case <-timer.C:
			break wait
		}
	}

	// Whatever did not finish in time answers from what it last knew.
	for i, p := range pending {
		if !settled[i] {
			out[p.key] = p.start
		}
	}
	return out
}

// storeReadBudget is how long ANY anonymous surface waits on the record store
// before answering from what it last knew. Deliberately the same 750 ms the
// $APPS feed already proved sufficient: an unloaded store answers these
// queries in single-digit milliseconds, so 750 ms is only ever spent when the
// writer holds the lock — and in that case no amount of extra waiting helps,
// because the writer holds it for minutes.
const storeReadBudget = 750 * time.Millisecond

// storeReadMinRefresh is how stale an answer may get before a reader triggers
// a refresh. These are aggregate/paged views of a store that changes in
// minutes-long ingest batches; two seconds of staleness is invisible, and it
// collapses a poll storm into one load.
const storeReadMinRefresh = 2 * time.Second
