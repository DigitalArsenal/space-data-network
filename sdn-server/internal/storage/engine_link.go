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
//   RecoverPoisonedEngine replace a trapped engine in place: fresh runtime,
//                          record-catalog replay, hot-window rebuild. The old
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
// new runtime + database, tables rebuilt from compact domain metadata when
// already hydrated, and the hot window rebuilt from stream files. Holds the
// store write lock for the duration. Idempotent and cheap when the engine is
// healthy. Returns the (possibly bumped) epoch.
func (s *FlatSQLStore) RecoverPoisonedEngine() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.engine == nil {
		return 0, fmt.Errorf("store is closed")
	}
	if !s.engine.Poisoned() {
		return s.engineEpoch, nil
	}

	log.Warnf("FlatSQL engine poisoned — rebuilding engine in place (epoch %d)", s.engineEpoch)

	// ONE WRITER PER FILE, ALWAYS.
	//
	// A poisoned runtime is RETIRED, not closed: linked flow VMs may still hold
	// borrowed references to its named instance. But a retired engine that still
	// owns open file descriptors on the control database would be a SECOND
	// WRITER against the file the replacement is about to open — the exact
	// corruption the one-daemon-per-box law exists to prevent, reproduced inside
	// one process. Closing only the host FILE LAYER severs that without touching
	// the wasm instance those VMs still reference: any further I/O the dead
	// engine attempts gets ioErrBadHandle, which is precisely what a poisoned
	// engine should get.
	//
	// The replacement then starts from a DISCARDED file rather than recovering
	// the trapped engine's half-written pages. Everything in that file is
	// derivable — this function already replays the whole catalog below — so
	// discarding is free, and trusting pages written by an engine that trapped
	// mid-transaction is not.
	if s.controlDBDurable {
		s.engine.FileIO().CloseAll()
		if err := removeControlDatabaseFiles(s.dbPath); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: discard control database: %w", err)
		}
		s.checkpointedOffset.Store(0)
	}

	opts := []flatsqlrt.Option{flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir())}
	if s.controlDBDurable {
		opts = append(opts, flatsqlrt.WithFileIORoot(s.basePath))
	}
	engine, err := flatsqlrt.New(opts...)
	if err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: start replacement engine: %w", err)
	}
	var engineDB *flatsqlrt.Database
	if s.controlDBDurable {
		engineDB, _, _, err = openControlDatabase(engine, s.dbPath)
	} else {
		engineDB, err = engine.CreateDatabase(engineRecordSchema, "sdn-control")
	}
	if err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: create database: %w", err)
	}
	if err := engineDB.RegisterFileID("$OMM", "OMM"); err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: register $OMM: %w", err)
	}
	db := flatsqldrv.Open(engineDB)
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
	s.engineSources = map[string]bool{}
	s.engineResident = map[string]int64{}
	s.engineEpoch++
	if oldDB != nil {
		_ = oldDB.Close()
	}

	if err := s.initTables(); err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: init tables: %w", err)
	}
	if s.auxiliaryMetadata != nil {
		if _, err := s.auxiliaryMetadata.Replay(s); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: replay auxiliary metadata: %w", err)
		}
	}

	catalogWasHydrated := s.recordCatalogHydrated.Load()
	hotWindowWasHydrated := s.engineHotHydrated.Load()
	s.recordCatalogHydrated.Store(false)
	s.engineHotHydrated.Store(false)
	if catalogWasHydrated && s.recordCatalog != nil {
		if _, err := s.recordCatalog.Replay(s); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: replay record catalog: %w", err)
		}
		s.recordCatalogHydrated.Store(true)
	}

	if catalogWasHydrated {
		if err := s.rebuildSourceSummariesFromDurableState(); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: source summaries: %w", err)
		}
		if err := s.rebuildEngineRecords(); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: hot-window rebuild: %w", err)
		}
		s.engineHotHydrated.Store(true)
	} else if hotWindowWasHydrated && s.recordCatalog != nil {
		if _, err := s.recordCatalog.ReplayEngineHotWindow(s, engineOMMSchemaName, s.engineHotWindow); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: compact hot-window rebuild: %w", err)
		}
		s.engineHotHydrated.Store(true)
	}

	log.Infof("FlatSQL engine rebuilt after poisoning (epoch %d)", s.engineEpoch)
	return s.engineEpoch, nil
}
