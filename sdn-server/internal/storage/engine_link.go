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
	"context"
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
	//
	// s.controlDBPath, NOT s.dbPath. Those are different files and this path had
	// the wrong one: s.dbPath is the LEGACY v1 database (`sdn.db`, which
	// MigrateLegacyControl still reads and which host-01 still has on disk),
	// while the engine's control database is `control.flatsqldb`. As written it
	// deleted the legacy database and then opened it as the control database —
	// destroying data this store does not own, and handing the engine a file
	// whose schema is not the control schema (or, once it is a non-database
	// file, the flatsql_open_db trap: task flatsql-open-nondb-trap). Both marks
	// are reset because a discarded database carries neither.
	if s.controlDBDurable {
		s.engine.FileIO().CloseAll()
		if err := removeControlDatabaseFiles(s.controlDBPath); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: discard control database: %w", err)
		}
		s.checkpointedOffset.Store(0)
		s.auxCheckpointedOffset.Store(0)
		s.auxAppliedOffset.Store(0)
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
		engineDB, _, _, _, err = openControlDatabase(engine, s.controlDBPath, engineSchemaTextExcluding(s.engineExcluded), nil)
	} else {
		engineDB, err = engine.CreateDatabase(engineSchemaTextExcluding(s.engineExcluded), "sdn-control")
	}
	if err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: create database: %w", err)
	}
	if err := registerEngineFileIDs(engineDB, s.engineExcluded); err != nil {
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: register file identifiers: %w", err)
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
	// The replacement started from discarded files: nothing is persisted, so
	// the rebuild below is a genuine from-empty one.
	s.engineStateWarm = false
	s.engineTailFrom.Store(0)
	s.engineEpoch++
	if oldDB != nil {
		_ = oldDB.Close()
	}

	if err := s.initTables(); err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: init tables: %w", err)
	}
	// From ZERO, always: the replacement database was discarded above, so there
	// is nothing for a mark to resume into. The replay is batched (one
	// transaction per chunk) exactly as it is at boot, and the caller holds
	// s.mu, which is what makes routing the appliers through the chunk
	// transaction safe here.
	if s.auxiliaryMetadata != nil {
		applied, through, err := s.auxiliaryMetadata.ReplayFrom(s, 0)
		if err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: replay auxiliary metadata: %w", err)
		}
		s.noteAuxiliaryAppliedThrough(through)
		s.auxReplayed.Store(true)
		log.Infof("FlatSQL engine recovery: replayed %d auxiliary metadata frames through offset %d", applied, through)
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
		// ONE journal pass for ALL routed schemas. Recovery runs on demand,
		// under s.mu, while the node is serving traffic: a pass per schema
		// would freeze every read and write on the box for as long as it takes
		// to read the multi-GB journal 226 times.
		if _, err := s.recordCatalog.ReplayEngineHotWindows(context.Background(), s, s.engineRoutedSchemaNames(), s.engineWindowFor); err != nil {
			return s.engineEpoch, fmt.Errorf("recover poisoned engine: compact hot-window rebuild: %w", err)
		}
		s.engineHotHydrated.Store(true)
	}

	// EVERY ROUTED BASE NAME MUST RESOLVE AFTER RECOVERY TOO, and this is the
	// only place that can guarantee it here. The replacement database is fresh
	// (the old one was discarded above), so its lazy initializeSQLiteEngine
	// latched with no tables and the unified views are gone with the file. The
	// replays register a source per record they load — but a store whose
	// journal yields no records for a standard registers nothing, and
	// `SELECT _data FROM IRM` would then answer "no such table": the exact
	// answer firstRunSemantics promises can never happen, and the answer the
	// cellular ingest flow's resume-mark SQL cannot tell from a real failure.
	// It also un-breaks PublicQuerySurface, which aborts the WHOLE surface on
	// one unresolvable base name.
	//
	// ViewsCurrent is true only when the replays already registered sources —
	// in that case ensureEngineSource has rebuilt the views once already, so
	// this is a no-op rather than a second all-or-nothing rebuild.
	if err := s.finishEngineSourceSetup(engineBootPlan{
		Excluded:     s.engineExcluded,
		ViewsCurrent: len(s.engineSources) > 0,
	}); err != nil {
		return s.engineEpoch, fmt.Errorf("recover poisoned engine: engine source setup: %w", err)
	}

	log.Infof("FlatSQL engine rebuilt after poisoning (epoch %d)", s.engineEpoch)
	return s.engineEpoch, nil
}
