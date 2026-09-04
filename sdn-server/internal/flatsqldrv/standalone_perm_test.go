package flatsqldrv

import (
	"os"
	"path/filepath"
	"testing"
)

// SEC-12: the standalone databases (auth.db, sessions) are owner-only files
// regardless of the process umask.
func TestOpenStandaloneCreatesOwnerOnlyFile(t *testing.T) {
	old := setUmask(0o022)
	defer setUmask(old)

	dbPath := filepath.Join(t.TempDir(), "auth.db")
	db, closer, err := OpenStandalone(dbPath)
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	defer closer()
	if _, err := db.Exec("CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("auth database mode = %o, want 0600", mode)
	}

	// A pre-existing world-readable file is tightened on open.
	loose := filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	db2, closer2, err := OpenStandalone(loose)
	if err != nil {
		t.Fatalf("OpenStandalone(existing): %v", err)
	}
	defer closer2()
	_ = db2
	info, err = os.Stat(loose)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("existing database mode = %o, want 0600", mode)
	}
}
