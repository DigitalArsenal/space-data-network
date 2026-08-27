package storage

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// THE SLOW-STATEMENT INSTRUMENT COULD NOT SEE THIS CLASS, AND THAT IS WHY IT
// RAN FOR MONTHS.
//
// flatsqlrt.accountQuery measures a STATEMENT: how long a caller waited for the
// engine lock and how long it held it. Measured live on host-01 over a
// 26-minute window of 70 tile requests, it logged 158 slow statements, ALL of
// them with `waited 0s`, and NONE of them a guest tile statement — while those
// same requests took 3.3 s to 90 s end to end. The engine lock was never
// contended, because everything is already serialized one level up: on
// FlatSQLStore.s.mu.
//
// Go's sync.RWMutex is write-preferring. A sustained writer stream (continuous
// p2p materialization plus hot-window eviction) therefore starves readers
// indefinitely, and not one nanosecond of that wait appears in any instrument
// the node had. `/tiles/meta` — ONE query against an EMPTY relation — had a
// median of 1.2 s and a maximum of 90 s.
//
// So the store lock gets its own account. It is deliberately the same shape as
// accountQuery (wait + hold, one warning line naming the site) because the two
// numbers are read together: a request that is slow with `waited 0s` on the
// engine and seconds of wait HERE has been diagnosed by those two lines alone.

// storeLockSlowThreshold is the hold/wait threshold for a warning. Same
// default as the engine's statement threshold so the two accounts are directly
// comparable; override with SDN_FLATSQL_SLOW_LOCK_MS.
var storeLockSlowThreshold = func() time.Duration {
	if raw := os.Getenv("SDN_FLATSQL_SLOW_LOCK_MS"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 250 * time.Millisecond
}()

// storeLockStats is the cumulative account. Cheap enough to be always on: four
// atomic adds per acquisition.
type storeLockStats struct {
	writeAcquires atomic.Int64
	writeWaitNs   atomic.Int64
	writeHeldNs   atomic.Int64
	readAcquires  atomic.Int64
	readWaitNs    atomic.Int64
	readHeldNs    atomic.Int64
	slow          atomic.Int64
}

// StoreLockAccount is the operator-facing snapshot.
type StoreLockAccount struct {
	WriteAcquires int64
	WriteWait     time.Duration
	WriteHeld     time.Duration
	ReadAcquires  int64
	ReadWait      time.Duration
	ReadHeld      time.Duration
	Slow          int64
}

// StoreLockStats reports the store-lock account. This is the number that was
// missing: readers starving behind a write-preferring RWMutex show up here as
// ReadWait, and nowhere else.
func (s *FlatSQLStore) StoreLockStats() StoreLockAccount {
	if s == nil {
		return StoreLockAccount{}
	}
	return StoreLockAccount{
		WriteAcquires: s.lockStats.writeAcquires.Load(),
		WriteWait:     time.Duration(s.lockStats.writeWaitNs.Load()),
		WriteHeld:     time.Duration(s.lockStats.writeHeldNs.Load()),
		ReadAcquires:  s.lockStats.readAcquires.Load(),
		ReadWait:      time.Duration(s.lockStats.readWaitNs.Load()),
		ReadHeld:      time.Duration(s.lockStats.readHeldNs.Load()),
		Slow:          s.lockStats.slow.Load(),
	}
}

// lockWrite takes s.mu for WRITING, named by site, and returns the release.
// The site is the holder's name, because "who held the store lock for 26.6
// seconds" is the only question worth asking when a reader starves.
func (s *FlatSQLStore) lockWrite(site string) func() {
	queued := time.Now()
	s.mu.Lock()
	waited := time.Since(queued)
	held := time.Now()
	s.lockStats.writeAcquires.Add(1)
	s.lockStats.writeWaitNs.Add(int64(waited))
	return func() {
		holdFor := time.Since(held)
		s.lockStats.writeHeldNs.Add(int64(holdFor))
		s.mu.Unlock()
		s.accountStoreLock("write", site, waited, holdFor)
	}
}

// lockRead takes s.mu for READING, named by site, and returns the release.
func (s *FlatSQLStore) lockRead(site string) func() {
	queued := time.Now()
	s.mu.RLock()
	waited := time.Since(queued)
	held := time.Now()
	s.lockStats.readAcquires.Add(1)
	s.lockStats.readWaitNs.Add(int64(waited))
	return func() {
		holdFor := time.Since(held)
		s.lockStats.readHeldNs.Add(int64(holdFor))
		s.mu.RUnlock()
		s.accountStoreLock("read", site, waited, holdFor)
	}
}

// accountStoreLock logs one line when either axis crossed the threshold. A
// READER that waited is the starvation symptom; a WRITER that held is its
// cause, and both name their site so the pair reads as one story.
func (s *FlatSQLStore) accountStoreLock(mode, site string, waited, held time.Duration) {
	if storeLockSlowThreshold <= 0 || (waited < storeLockSlowThreshold && held < storeLockSlowThreshold) {
		return
	}
	s.lockStats.slow.Add(1)
	log.Warnf("FlatSQL slow lock: %s %q held %s, waited %s for the STORE lock (s.mu) — this is OUTSIDE the engine, so the engine's slow-statement account cannot see it; Go's RWMutex is write-preferring, so a writer that holds here starves every concurrent reader for the same time.",
		mode, site, held.Round(time.Millisecond), waited.Round(time.Millisecond))
}
