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
//                          journal replay, hot-window rebuild. The old
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
// new runtime + database, control tables replayed from the statement
// journal, hot window rebuilt from stream files — the exact boot path, in
// place, holding the store write lock for the duration. Idempotent and
// cheap when the engine is healthy. Returns the (possibly bumped) epoch.
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

	engine, err := flatsqlrt.New(flatsqlrt.WithAOTCache(engineAOTCacheDir()))
	if err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: start replacement engine: %w", err)
	}
	engineDB, err := engine.CreateDatabase(engineRecordSchema, "sdn-control")
	if err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: create database: %w", err)
	}
	if err := engineDB.RegisterFileID("$OMM", "OMM"); err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: register $OMM: %w", err)
	}
	if _, err := s.journal.Replay(engineDB); err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: journal replay: %w", err)
	}
	db := flatsqldrv.Open(engineDB, s.journal)
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

	if err := s.rebuildEngineRecords(); err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: hot-window rebuild: %w", err)
	}

	log.Infof("FlatSQL engine rebuilt after poisoning (epoch %d)", s.engineEpoch)
	return s.engineEpoch, nil
}
