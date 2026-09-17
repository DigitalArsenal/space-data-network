package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// Two things this pins, both of which have already gone wrong once.
//
// WAL, because the control store used TRUNCATE+FULL and an fsync-per-commit
// rollback journal was the write path's dominant cost.
//
// synchronous=FULL, because WAL's default pairing in the engine is NORMAL,
// which can lose the last commits on power loss. This database is the control
// store, so it keeps the durability it had; the speedup is the free part.
//
// And the pragma sits AFTER prepare() in boot, because the engine's
// virtual-table registration is a one-shot fired by the first query — issuing
// it earlier consumed that and broke four cold-rebuild and hydration tests.
// A test that only checked journal_mode would not have noticed either mistake,
// so it checks both values that the boot order has to produce.
func TestControlDatabaseIsWALAtFullDurability(t *testing.T) {
	dir := t.TempDir()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(dir, "db"), v)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	var jm string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	var sy int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&sy); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	fmt.Printf("PROBE control db: journal_mode=%s synchronous=%d (2 = FULL)\n", jm, sy)
	if jm != "wal" {
		t.Fatalf("journal_mode = %q, want wal", jm)
	}
	if sy != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL) — the pragma did not apply after the move", sy)
	}
}
