package storage

// Direct engine linkage surface (loop C.7): flow mounts with engineLinkage
// "flatsql" register the store's LIVE engine instance into their VMs and
// call flatsql_* exports in-wasm. The store stays the engine's owner; this
// file exposes exactly what a mount needs:
//
//   EngineRuntime         the live runtime + database (register + lock +
//                          body-ref harvest handles)
//   EngineEpoch           monotonic engine identity — bumped every time the
//                          store REPLACES its engine; mounts re-instantiate
//                          dependent flow instances when it moves
//   RecoverPoisonedEngine replace a trapped engine in place: fresh runtime
//                          over the SAME control database, hot window
//                          reconciled from the residency ledger. The old
//                          runtime is RETIRED, not closed — dependent VMs
//                          may still hold borrowed references to its
//                          instance; retired engines are released at store
//                          Close (bounded by poison rarity; a poisoned
//                          engine previously meant a dead daemon).

import (
	"context"
	"fmt"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// EngineRuntime returns the store's current engine runtime and database for
// direct linkage. Callers must treat them as valid only for the current
// EngineEpoch.
func (s *FlatSQLStore) EngineRuntime() (*flatsqlrt.Runtime, *flatsqlrt.Database) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.engine, s.engineDB
}

// EngineEpoch reports the engine replacement counter (starts at 1 for the
// boot engine).
func (s *FlatSQLStore) EngineEpoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.engineEpoch
}

// RecoverPoisonedEngine replaces the engine if (and only if) it is poisoned:
// a new runtime opens the SAME control database — SQLite's rollback journal
// discards whatever the trapped engine had in flight — and the hot window is
// brought current from the residency ledger exactly as a boot does. Holds
// the store write lock for the duration. Idempotent and cheap when the
// engine is healthy. Returns the (possibly bumped) epoch.
func (s *FlatSQLStore) RecoverPoisonedEngine() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.engine == nil {
		return 0, fmt.Errorf("store is closed")
	}
	if !s.engine.Poisoned() {
		return s.engineEpoch, nil
	}

	// Readers that arrive from here on are answered ErrEngineRebuilding at the
	// gate (readGate) instead of queueing on s.mu for the whole rebuild.
	s.engineRebuilding.Store(true)
	defer s.engineRebuilding.Store(false)
	if s.ps != nil {
		// Format 2: only the control instance is replaced; the partition
		// store's instances are their own poison domains (§15).
		return s.recoverFormat2ControlInstanceLocked()
	}

	log.Warnf("FlatSQL engine poisoned — reopening the control database on a replacement engine (epoch %d)", s.engineEpoch)

	// ONE WRITER PER FILE, ALWAYS. A poisoned runtime is RETIRED, not closed:
	// linked flow VMs may still hold borrowed references to its named
	// instance. But a retired engine that still owns open file descriptors on
	// the control database would be a SECOND WRITER against the file the
	// replacement is about to open. Closing only the host FILE LAYER severs
	// that without touching the wasm instance those VMs still reference: any
	// further I/O the dead engine attempts gets ioErrBadHandle, which is
	// precisely what a poisoned engine should get.
	s.engine.FileIO().CloseAll()

	engine, engineDB, mark, plan, err := openControlEngine(s.basePath, s.controlDBPath)
	if err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: %w", err)
	}
	if err := registerEngineFileIDs(engineDB, plan.Excluded); err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: register file identifiers: %w", err)
	}
	if err := dropExcludedStandardViews(engineDB, plan.Excluded); err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: clear leftover views: %w", err)
	}
	db := flatsqldrv.Open(engineDB)
	if mib := resolveEnginePageCacheMiB(); mib > 0 {
		_, _ = db.Exec(fmt.Sprintf("PRAGMA cache_size = -%d", mib*1024))
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: foreign keys: %w", err)
	}

	// Swap. The OLD runtime is retired, NOT closed: linked flow VMs may
	// still hold borrowed references to its named instance until their
	// mounts observe the epoch bump and re-instantiate.
	oldDB := s.db
	s.retiredEngines = append(s.retiredEngines, s.engine)
	s.db = db
	s.engine = engine
	s.engineDB = engineDB
	s.engineSources = plan.registeredSources()
	s.engineResident = map[string]int64{}
	s.engineSchemaLoaded = map[string]bool{}
	s.engineExcluded = plan.Excluded
	s.engineStateWarm = plan.EngineState.Warm
	s.engineStateRecords = plan.EngineState.Records
	s.engineMarkRowID.Store(mark.EngineRowID)
	s.engineUnflushed.Store(0)
	s.engineEpoch++
	if oldDB != nil {
		_ = oldDB.Close()
	}

	if err := s.initTables(); err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: init tables: %w", err)
	}
	s.settleEngineResidencyAtOpen()
	// EVERY ROUTED BASE NAME MUST RESOLVE AFTER RECOVERY TOO: a store whose
	// tables hold no records for a standard registers nothing for it, and
	// `SELECT _data FROM IRM` would then answer "no such table" — the answer
	// no caller can tell from a real failure.
	if err := s.finishEngineSourceSetup(plan); err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: engine source setup: %w", err)
	}
	wasHydrated := s.engineHotHydrated.Load()
	s.engineHotHydrated.Store(false)
	switch {
	case !wasHydrated:
	case plan.EngineState.Warm:
		// The arena reopened: reconcile it and ingest the tail, here.
		if err := s.rebuildEngineRecordsLocked(); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: hot-window hydration: %w", err)
		}
		if err := s.checkpointEngineLocked(); err != nil {
			log.Warnf("FlatSQL engine recovery: record state not flushed (the next boot rebuilds the tail): %v", err)
		}
	default:
		// A COLD window is refilled in the background, one bounded page per
		// lock hold, exactly as a boot does — NOT under this lock. Refilling
		// it here held the store write lock through the whole rebuild on
		// host-02 (2026-09-27): no write landed, and an update shutdown could
		// not drain and left the store open. Readers of a standard not yet
		// refilled are gated per standard (EngineSchemaReady); Close cancels
		// the refill between pages.
		s.startRecoveryHydration()
	}

	log.Infof("FlatSQL engine rebuilt after poisoning (epoch %d)", s.engineEpoch)
	return s.engineEpoch, nil
}

// recoveryHydration refills a cold hot window after an engine recovery, in
// the background, cancellable by Close.
type recoveryHydration struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

// startRecoveryHydration runs HydrateEngineHotWindowContext in the background.
// Called with s.mu held; the refill takes the lock per page once it is
// released.
func (s *FlatSQLStore) startRecoveryHydration() {
	h := &s.recoveryHydrate
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if h.ctx == nil {
		h.ctx, h.cancel = context.WithCancel(context.Background())
	}
	ctx := h.ctx
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		n, err := s.HydrateEngineHotWindowContext(ctx)
		if err != nil {
			log.Errorf("FlatSQL engine hot-window refill after recovery failed after %d records: %v", n, err)
			return
		}
		if ctx.Err() == nil {
			log.Infof("FlatSQL engine hot-window refill after recovery complete: %d records", n)
		}
	}()
}

// stopRecoveryHydration cancels a refill in progress and waits for it to
// leave. Call it WITHOUT the store lock: the refill may be waiting for it.
func (s *FlatSQLStore) stopRecoveryHydration() {
	h := &s.recoveryHydrate
	h.mu.Lock()
	h.closed = true
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Unlock()
	h.wg.Wait()
}
