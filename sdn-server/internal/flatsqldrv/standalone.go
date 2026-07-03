package flatsqldrv

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// standaloneSchema: subsystem stores only use plain SQL tables (DDL through
// the driver); the engine requires some FlatBuffer schema to boot.
const standaloneSchema = `table Standalone { id: int (id); } root_type Standalone;`

// DefaultAOTCacheDir is the machine-wide engine AOT artifact cache
// (hash-keyed inside, shared safely across processes).
func DefaultAOTCacheDir() string {
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "flatsql-aot")
	}
	return filepath.Join(os.TempDir(), "flatsql-aot")
}

// OpenStandalone gives a subsystem its own private engine-backed database:
// a dedicated FlatSQL runtime plus a statement journal at journalPath for
// durability (replayed here on open). This replaces the old pattern of
// opening a second sqlite connection onto the main store's db file — each
// subsystem now has fully isolated transactions and no shared-file
// contention. The returned closer releases the sql.DB, journal, and engine.
func OpenStandalone(journalPath string) (*sql.DB, func() error, error) {
	rt, err := flatsqlrt.New(flatsqlrt.WithAOTCache(DefaultAOTCacheDir()))
	if err != nil {
		return nil, nil, fmt.Errorf("flatsqldrv: start engine: %w", err)
	}
	edb, err := rt.CreateDatabase(standaloneSchema, filepath.Base(journalPath))
	if err != nil {
		rt.Close()
		return nil, nil, fmt.Errorf("flatsqldrv: create database: %w", err)
	}
	journal, err := OpenStatementJournal(journalPath)
	if err != nil {
		rt.Close()
		return nil, nil, err
	}
	if _, err := journal.Replay(edb); err != nil {
		journal.Close()
		rt.Close()
		return nil, nil, fmt.Errorf("flatsqldrv: replay %s: %w", journalPath, err)
	}
	db := Open(edb, journal)
	closer := func() error {
		err := db.Close()
		if jerr := journal.Close(); err == nil {
			err = jerr
		}
		edb.Destroy()
		rt.Close()
		return err
	}
	return db, closer, nil
}
