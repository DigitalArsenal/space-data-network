package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// Three things this pins, and every one of them has already gone wrong once.
//
// WAL, because the control store used TRUNCATE+FULL and an fsync-per-commit
// rollback journal was the write path's dominant cost.
//
// synchronous=NORMAL, because the pragma is issued EXPLICITLY rather than left
// to the engine's WAL default, so a silent change to that default must not
// silently change this database's durability. Until 2026-09-18 this asserted
// FULL, on the reasoning that "this database is the control store, so it keeps
// the durability it had; the speedup is the free part". That reasoning was
// abandoned on the owner's instruction of 2026-09-18: FULL costs ~2.4x on the
// store write path, and what it buys is protection against OS crash and power
// loss ONLY — a process crash, the `kill -9` of a deploy swap included, loses
// nothing at NORMAL either (flatsqlrt.TestWALProcessCrashKeepsCommittedRows
// pins that). The argument in full is in flatsql_boot_state.go above the
// pragma.
//
// The autocheckpoint window, because it is what BOUNDS the durability NORMAL
// gives up — the WAL is fsynced every wal_autocheckpoint pages, so the tail at
// risk on power loss is at most wal_autocheckpoint x page_size. The comment
// beside the pragma quotes that product as a hard number; if either factor
// moves, the quoted bound is wrong and this test is how anyone finds out.
//
// And the pragma sits AFTER prepare() in boot, because the engine's
// virtual-table registration is a one-shot fired by the first query — issuing
// it earlier consumed that and broke four cold-rebuild and hydration tests.
// A test that only checked journal_mode would not have noticed either mistake,
// so it checks every value that the boot order has to produce.
//
// WHAT THIS TEST CANNOT TELL YOU, stated so nobody over-trusts it: it pins the
// OBSERVABLE durability state, not the existence of the pragma. NORMAL is
// already flatsqlrt.JournalWAL's own default (internal/flatsqlrt/diskstate.go:
// "JournalWAL is journal_mode=WAL + synchronous=NORMAL"), so deleting the
// explicit pragma block from boot leaves this test passing — verified by
// deleting it and re-running. It therefore guards the durability the store
// RUNS AT, which is the property that matters here, and would catch the engine
// default moving underneath us. It would NOT catch the pragma being dropped
// while the default still happens to agree.
func TestControlDatabaseIsWALAtNormalDurability(t *testing.T) {
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
	var autockpt, pageSize int
	if err := store.db.QueryRow("PRAGMA wal_autocheckpoint").Scan(&autockpt); err != nil {
		t.Fatalf("wal_autocheckpoint: %v", err)
	}
	if err := store.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	fmt.Printf("PROBE control db: journal_mode=%s synchronous=%d (1 = NORMAL, 2 = FULL) "+
		"wal_autocheckpoint=%d page_size=%d -> power-loss tail bound %.1f MiB\n",
		jm, sy, autockpt, pageSize, float64(autockpt*pageSize)/(1024*1024))
	if jm != "wal" {
		t.Fatalf("journal_mode = %q, want wal", jm)
	}
	if sy != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL) — the pragma did not apply after the move", sy)
	}
	if autockpt != 1000 || pageSize != 4096 {
		t.Fatalf("wal_autocheckpoint=%d page_size=%d — the power-loss tail bound quoted beside the "+
			"pragma in flatsql_boot_state.go assumes 1000 x 4096 (~4 MiB); update that comment", autockpt, pageSize)
	}
}
