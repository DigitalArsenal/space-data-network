package storage

import (
	"path/filepath"
	"testing"
)

// A window that does not commit must not leave records in the engine arena.
//
// The mirror runs BEFORE the control commit so its ledger rows land in the same
// fsync (one per window instead of two). The cost of that ordering is that a
// transaction which rolls back — including a COMMIT that itself fails — has
// already put rows in the arena, and the arena is not transactional. Those rows
// have no ledger row and no control row, and the engine would keep SERVING them
// until the next warm open: phantom records, which is strictly worse than the
// untracked-residency drift the old ordering could produce.
//
// storeBatchChunk's rollback path retires them using the rows the mirror
// returns. This asserts those returned rows actually identify the arena rows —
// the part most likely to be wrong, since a tombstone needs the right
// (source, seq) and a mismatch would fail silently.
func TestRolledBackWindowLeavesNoArenaRows(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}

	before := engineCount(t, store, "OMM")

	records := partitionRecords(t, 61000, 6)
	pending := make([]engineIngest, 0, len(records))
	for _, data := range records {
		pending = append(pending, engineIngest{cid: computeCID(data), data: data, source: "rollback-probe"})
	}

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	store.mu.Lock()
	arenaRows, err := store.ingestEngineRecords("OMM.fbs", pending, tx)
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("mirror into the transaction: %v", err)
	}
	if len(arenaRows) != len(records) {
		t.Fatalf("mirror reported %d arena row(s) for %d records — the rollback path retires exactly these, so a short list silently strands the rest", len(arenaRows), len(records))
	}

	during := engineCount(t, store, "OMM")
	if during != before+int64(len(records)) {
		t.Fatalf("engine holds %d rows mid-window, want %d — the mirror did not actually ingest", during, before+int64(len(records)))
	}

	// The window fails: roll the control rows back and retire the arena rows,
	// which is exactly what storeBatchChunk's deferred rollback does.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	binding, routed := store.engineRoutedSchemaFor("OMM.fbs")
	if !routed {
		t.Fatal("OMM.fbs is not engine-routed — this test cannot say anything")
	}
	store.mu.Lock()
	retired, err := store.tombstoneResidencyRowsLocked("OMM.fbs", binding.Table, arenaRows, nil)
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("retire arena rows: %v", err)
	}
	if retired != len(records) {
		t.Fatalf("retired %d of %d arena rows", retired, len(records))
	}

	if after := engineCount(t, store, "OMM"); after != before {
		t.Fatalf("engine holds %d rows after a rolled-back window, want %d — %d phantom record(s) the engine would serve with nothing in the control tables behind them",
			after, before, after-before)
	}

	// And the ledger went back with the transaction.
	var ledger int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = ?`, engineLedgerSchema("OMM.fbs")).Scan(&ledger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if ledger != 0 {
		t.Fatalf("ledger kept %d row(s) from a rolled-back window", ledger)
	}
}
