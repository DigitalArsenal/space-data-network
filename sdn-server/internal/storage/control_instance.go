package storage

// control_instance.go — the control instance of a partitioned store
// (formats 2 and 4): the legacy engine on its own control database, holding
// the control tables only (directory, local EPMs, publications, pin ledger,
// log index, asset pins, licences, metadata). It opens on TRUNCATE at
// SQLite's default synchronous=FULL: it carries no record bytes, so the WAL
// patch and its NORMAL durability trade (flatsql_boot_state.go) are not
// taken. It holds NO record table, so a record path that was not moved to
// the record backend fails loudly ("no such table") instead of reading or
// writing an empty copy.

import (
	"errors"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// openControlInstance opens the control instance of a partitioned store:
// the legacy engine on dbPath, TRUNCATE, no record sources, no record state.
func openControlInstance(basePath, dbPath string) (*flatsqlrt.Runtime, *flatsqlrt.Database, bootMark, error) {
	if err := checkDatabaseFile(dbPath); err != nil {
		return nil, nil, bootMark{}, err
	}
	engine, err := flatsqlrt.New(
		flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()),
		flatsqlrt.WithFileIORoot(basePath),
	)
	if err != nil {
		return nil, nil, bootMark{}, fmt.Errorf("failed to start the control instance: %w", err)
	}
	if mode := engine.Mode(); !mode.AOT {
		log.Warnf("control instance INTERPRETED — no AOT artifact in %s (%s); run `spacedatanetwork prewarm-aot`", mode.CacheDir, mode.MissReason)
	}
	engineDB, err := engine.OpenDatabase(engineDatabaseSchema, "sdn-control", dbPath, flatsqlrt.JournalTruncate)
	if err != nil {
		engine.Close()
		return nil, nil, bootMark{}, fmt.Errorf("%w: %s: %v", errControlDatabaseUnusable, dbPath, err)
	}
	if err := registerEngineFileIDs(engineDB, nil); err != nil {
		engineDB.Destroy()
		engine.Close()
		return nil, nil, bootMark{}, fmt.Errorf("control instance: register file identifiers: %w", err)
	}
	disk, err := engineDB.IsDiskBacked()
	if err != nil || !disk {
		engineDB.Destroy()
		engine.Close()
		if err == nil {
			err = errors.New("engine opened a real path but reports NOT disk-backed")
		}
		return nil, nil, bootMark{}, err
	}
	if err := verifyControlDatabase(engineDB); err != nil {
		engineDB.Destroy()
		engine.Close()
		return nil, nil, bootMark{}, fmt.Errorf("%w: %s: %v", errControlDatabaseUnusable, dbPath, err)
	}
	return engine, engineDB, readBootMark(engineDB), nil
}

// initControlTables creates the control tables only (initTables less
// every record table, the engine rows and the partition counters).
func (s *FlatSQLStore) initControlTables() error {
	steps := []struct {
		what string
		fn   func() error
	}{
		{"metadata", s.initMetadataTable},
		{"source batch licences", s.initSourceBatchLicenseTable},
		{"ingest identities", s.initRecordIngestIdentityTable},
		{"dataset publication series", s.initDatasetPublicationSeriesTables},
		{"dataset shard publications", s.initDatasetShardPublicationTable},
		{"pin ledger", s.initPinLedgerTable},
		{"asset pin ledger", s.initAssetPinLedgerTables},
		{"publication replay state", s.initDatasetPublicationReplayStateTable},
		{"directory", s.initDirectoryTable},
		{"local EPM", s.initLocalEPMTable},
		{"log index", s.initLogIndexTable},
	}
	for _, st := range steps {
		if err := st.fn(); err != nil {
			return fmt.Errorf("%s: %w", st.what, err)
		}
	}
	return nil
}

// recoverFormat2ControlInstance replaces a poisoned control instance
// (RecoverPoisonedEngine on format 2): the same control database on a fresh
// runtime. The partition store is unaffected: its instances are their own
// poison domains (§15). Caller holds s.mu.
func (s *FlatSQLStore) recoverControlInstanceLocked() (uint64, error) {
	s.engine.FileIO().CloseAll()
	engine, engineDB, _, err := openControlInstance(s.basePath, s.controlDBPath)
	if err != nil {
		return s.engineEpoch, fmt.Errorf("recover the control instance: %w", err)
	}
	db := flatsqldrv.Open(engineDB)
	if mib := resolveEnginePageCacheMiB(); mib > 0 {
		_, _ = db.Exec(fmt.Sprintf("PRAGMA cache_size = -%d", mib*1024))
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		engine.Close()
		return s.engineEpoch, fmt.Errorf("recover the control instance: foreign keys: %w", err)
	}
	oldDB := s.db
	s.retiredEngines = append(s.retiredEngines, s.engine)
	s.db = db
	s.engine = engine
	s.engineDB = engineDB
	s.assetPinTransactions = sqlAssetPinTransactionBeginner{db: db}
	s.engineEpoch++
	if oldDB != nil {
		_ = oldDB.Close()
	}
	if err := s.initControlTables(); err != nil {
		return s.engineEpoch, fmt.Errorf("recover the control instance: control tables: %w", err)
	}
	return s.engineEpoch, nil
}
