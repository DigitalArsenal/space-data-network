package flatsqldrv

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	_ "modernc.org/sqlite"
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

// OpenStandalone gives a subsystem its own private durable SQL database.
// These stores are not FlatSQL record streams; use a normal SQLite file
// instead of a SQL-statement replay log.
func OpenStandalone(dbPath string) (*sql.DB, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("flatsqldrv: create standalone db directory: %w", err)
	}
	// These files hold operator accounts and session tokens: owner-only,
	// whatever the process umask says (SEC-12). Create the file with 0600
	// before SQLite touches it and tighten one that already exists.
	if f, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600); err != nil {
		return nil, nil, fmt.Errorf("flatsqldrv: create standalone db file: %w", err)
	} else {
		_ = f.Close()
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return nil, nil, fmt.Errorf("flatsqldrv: restrict standalone db file: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("flatsqldrv: open standalone sqlite db: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("flatsqldrv: enable foreign keys: %w", err)
	}
	closer := func() error {
		return db.Close()
	}
	return db, closer, nil
}

// newEphemeralSQLDB is used by flatsqldrv's own tests to exercise the
// database/sql wrapper over a FlatSQL engine.
func newEphemeralSQLDB() (*sql.DB, *flatsqlrt.Runtime, *flatsqlrt.Database, error) {
	rt, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(DefaultAOTCacheDir()))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("flatsqldrv: start engine: %w", err)
	}
	edb, err := rt.CreateDatabase(standaloneSchema, "ephemeral")
	if err != nil {
		rt.Close()
		return nil, nil, nil, fmt.Errorf("flatsqldrv: create database: %w", err)
	}
	return Open(edb), rt, edb, nil
}
