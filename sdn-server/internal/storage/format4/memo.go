package format4

// memo.go — the engine's counter answers, kept between writes.
//
// SUMMARY (types, partitions, lanes) and HEAD answer from the engine's
// counters, but each answer is a mailbox round trip (50-250 us; GATES-r2
// R10/R20), where formats 1 and 2 answer the same reads from host memory in
// microseconds. Between two writes these answers cannot change: a write's
// ack follows its commit and the visibility publish (C-4), and only writes
// move the counters. So the Engine keeps the last answer per request and
// forgets them all when a write starts and again when it ends:
//
//   - a write is every ClassWrite request (PUT, SUPERSEDE, DELETE,
//     QUOTA_GC, REBUILD), a type registration, Activate and SetQuota;
//   - an answer is kept only when no write ran while it was read;
//   - a write whose request was abandoned (its caller left; the engine
//     still finishes it) turns keeping off for the Engine's life;
//   - what moves without a write is not kept by generation: full-text
//     state (FTS, a HEAD with a search) is never kept; with a quota set the
//     engine deletes on its own once a second, so an answer is then kept for
//     at most memoQuotaAge; disk sizes move with checkpoints and are kept for
//     memoDiskAge (format 2's DiskUsageBytes lags by its sweep the same way).
//
// Engine.State hands out the same generation as a stamp, so a caller can
// keep what it derives from these answers (the daemon's totals and
// summaries) under the same rule.

import (
	"sync"
	"time"
)

const (
	// memoKeys bounds the answers kept (HEAD keys are per filter).
	memoKeys = 512
	// memoQuotaAge is how long an answer is kept while a quota is set: the
	// engine's quota pass runs once a second.
	memoQuotaAge = time.Second
	// memoDiskAge is how long the disk sizes are kept.
	memoDiskAge = time.Second
)

type memoEntry struct {
	gen uint64
	at  time.Time
	v   any
}

// memo is the Engine's kept answers.
type memo struct {
	mu      sync.Mutex
	gen     uint64 // moves when a write starts and when it ends
	writing int    // writes running
	off     bool   // an abandoned write may still commit: keep nothing
	quota   bool   // a quota is set: the engine deletes on its own
	entries map[string]memoEntry
}

// begin and end bracket a write.
func (m *memo) begin() {
	m.mu.Lock()
	m.writing++
	m.gen++
	m.mu.Unlock()
}

func (m *memo) end(abandoned bool) {
	m.mu.Lock()
	m.writing--
	m.gen++
	if abandoned {
		m.off, m.entries = true, nil
	}
	m.mu.Unlock()
}

// setQuota records whether the engine runs its own quota pass.
func (m *memo) setQuota(on bool) {
	m.mu.Lock()
	m.quota = on
	m.gen++
	m.mu.Unlock()
}

// state is the counter state's stamp: it moves when a write starts or ends
// and, with a quota set, every memoQuotaAge. ok is false while a write runs
// or once keeping is off: nothing read now may be kept.
func (m *memo) state() (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stamp := m.gen
	if m.quota {
		stamp = m.gen<<32 | uint64(time.Now().UnixNano()/int64(memoQuotaAge))&(1<<32-1)
	}
	return stamp, !m.off && m.writing == 0
}

// get returns key's kept answer; else the generation a fresh answer is read
// at (pass it to put). byGen false keeps the answer for maxAge whatever the
// writes did.
func (m *memo) get(key string, byGen bool, maxAge time.Duration) (any, uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if byGen && m.quota && (maxAge == 0 || maxAge > memoQuotaAge) {
		maxAge = memoQuotaAge
	}
	if e, ok := m.entries[key]; ok && (!byGen || (e.gen == m.gen && m.writing == 0)) &&
		(maxAge == 0 || time.Since(e.at) < maxAge) {
		return e.v, 0, true
	}
	return nil, m.gen, false
}

// put keeps v, read at gen, unless a write ran since (byGen).
func (m *memo) put(key string, byGen bool, gen uint64, at time.Time, v any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.off || (byGen && (m.writing != 0 || m.gen != gen)) {
		return
	}
	if m.entries == nil || len(m.entries) >= memoKeys {
		m.entries = make(map[string]memoEntry)
	}
	m.entries[key] = memoEntry{gen: gen, at: at, v: v}
}

// kept answers key from the memo, or from load, keeping load's answer
// (never an error). A slice answer is shared: callers clone it.
func kept[T any](m *memo, key string, byGen bool, maxAge time.Duration, load func() (T, error)) (T, error) {
	v, gen, ok := m.get(key, byGen, maxAge)
	if ok {
		return v.(T), nil
	}
	at := time.Now()
	fresh, err := load()
	if err == nil {
		m.put(key, byGen, gen, at, fresh)
	}
	return fresh, err
}
