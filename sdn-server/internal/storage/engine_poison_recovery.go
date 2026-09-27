package storage

// A poisoned engine is replaced without a restart.
//
// wasmrt poisons the engine instance when a call traps or runs past its
// per-call budget ("dedicated execution thread abandoned mid-call"), and every
// later call on that instance fails at once. RecoverPoisonedEngine replaces
// it, but only three paths ever called it: boot hot-window hydration, dataset
// shard reads, and engine-linked flow HTTP mounts. Ingest writes, quota GC,
// DataSummary, sync and every other store path just kept failing. On host-02
// (2026-09-26 09:46Z) one poisoned engine stopped CelesTrak GP and
// space-weather ingest until the 2026-09-27 16:15Z restart.
//
// The watch replaces the engine as soon as its poisoned bit is set, whoever
// tripped it. Flow services reach the store through host capabilities, so
// their next fire simply succeeds; engine-linked mounts re-instantiate on the
// epoch bump.

import (
	"sync"
	"sync/atomic"
	"time"
)

// enginePoisonWatchInterval is how often the watch looks at the engine.
// Tests shorten it before opening a store.
var enginePoisonWatchInterval = 2 * time.Second

const enginePoisonRecoveryMaxBackoff = time.Minute

type enginePoisonWatch struct {
	stop       chan struct{}
	done       chan struct{}
	running    atomic.Bool
	stopOnce   sync.Once
	recoveries atomic.Int64
}

func newEnginePoisonWatch() *enginePoisonWatch {
	return &enginePoisonWatch{stop: make(chan struct{}), done: make(chan struct{})}
}

// EngineRecoveries counts engines this store's watch has replaced.
func (s *FlatSQLStore) EngineRecoveries() int64 {
	if s == nil || s.poisonWatch == nil {
		return 0
	}
	return s.poisonWatch.recoveries.Load()
}

func (s *FlatSQLStore) startEnginePoisonWatch() {
	w := s.poisonWatch
	if w == nil || !w.running.CompareAndSwap(false, true) {
		return
	}
	go s.runEnginePoisonWatch(w, enginePoisonWatchInterval)
}

// stopEnginePoisonWatch stops and joins the watch. Call it WITHOUT the store
// lock: a recovery in progress holds it.
func (s *FlatSQLStore) stopEnginePoisonWatch() {
	w := s.poisonWatch
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stop)
		if w.running.Load() {
			<-w.done
		}
	})
}

func (s *FlatSQLStore) enginePoisoned() (poisoned, open bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engine == nil {
		return false, false
	}
	return s.engine.Poisoned(), true
}

func (s *FlatSQLStore) runEnginePoisonWatch(w *enginePoisonWatch, interval time.Duration) {
	defer close(w.done)
	wait := interval
	for {
		timer := time.NewTimer(wait)
		select {
		case <-w.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		poisoned, open := s.enginePoisoned()
		if !open {
			return
		}
		if !poisoned {
			wait = interval
			continue
		}
		start := time.Now()
		epoch, err := s.RecoverPoisonedEngine()
		if err != nil {
			// Keep trying: a node that cannot reopen its engine is down, and
			// trying again is the only thing that can bring it back.
			if wait < interval {
				wait = interval
			}
			wait *= 2
			if wait > enginePoisonRecoveryMaxBackoff {
				wait = enginePoisonRecoveryMaxBackoff
			}
			log.Errorf("FlatSQL engine is poisoned and could not be replaced (retrying in %s): %v", wait, err)
			continue
		}
		w.recoveries.Add(1)
		wait = interval
		log.Warnf("FlatSQL engine was poisoned and has been replaced without a restart (epoch %d, %s); store reads and writes resume",
			epoch, time.Since(start).Round(time.Millisecond))
	}
}
