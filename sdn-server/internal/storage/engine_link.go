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
	"fmt"

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
	if wasHydrated {
		if err := s.rebuildEngineRecordsLocked(); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: hot-window hydration: %w", err)
		}
		if err := s.checkpointEngineLocked(); err != nil {
			log.Warnf("FlatSQL engine recovery: record state not flushed (the next boot rebuilds the tail): %v", err)
		}
	}

	log.Infof("FlatSQL engine rebuilt after poisoning (epoch %d)", s.engineEpoch)
	return s.engineEpoch, nil
}
