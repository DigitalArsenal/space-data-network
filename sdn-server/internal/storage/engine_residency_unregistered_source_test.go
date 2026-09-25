package storage

import (
	"path/filepath"
	"testing"
)

// A residency row naming a source the engine never registered must not reach
// the engine's markDeleted.
//
// The engine throws "Source not found" for an unknown partition, and the noeh
// build turns that throw into an `unreachable` trap that poisons the runtime.
// The control DB lives in the same runtime, so one stale ledger row took every
// read down (trust edges, data, the dashboard) for the ~100 s the replacement
// engine needed to hydrate. Observed on the dev store 2026-09-25: retention
// tombstoning RFB.fbs hit a ledger row with an empty cid and source.
func TestTombstoneUnregisteredSourceDoesNotPoisonEngine(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	ledgerSchema := engineLedgerSchema("OMM.fbs")
	if _, err := store.db.Exec(`INSERT INTO sdn_engine_rows (schema_name, cid, source, seq) VALUES (?, '', '', 115664)`, ledgerSchema); err != nil {
		t.Fatalf("seed stale ledger row: %v", err)
	}

	store.mu.Lock()
	removed, err := store.tombstoneEngineRecordsLocked("OMM.fbs", []string{""}, nil)
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("tombstone stale ledger row: %v", err)
	}
	if store.engine.Poisoned() {
		t.Fatal("tombstoning a row with an unregistered source poisoned the engine")
	}
	if removed != 1 {
		t.Fatalf("removed %d ledger row(s), want the 1 stale row", removed)
	}

	var ledger int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = ?`, ledgerSchema).Scan(&ledger); err != nil {
		t.Fatalf("count ledger after tombstone (engine still answering?): %v", err)
	}
	if ledger != 0 {
		t.Fatalf("ledger kept %d stale row(s)", ledger)
	}
}
