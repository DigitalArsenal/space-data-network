package storage

import (
	"path/filepath"
	"sync/atomic"
	"testing"
)

// engineDistinctCount reads (rows, distinct record bytes) of an engine
// partition: a record ingested twice shows as two rows with one payload.
func engineDistinctCount(t *testing.T, s *FlatSQLStore, table string) (int64, int64) {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	res, err := s.engineDB.Query(`SELECT COUNT(*), COUNT(DISTINCT _data) FROM "` + table + `"`)
	if err != nil {
		t.Fatalf("engine distinct count %s: %v", table, err)
	}
	n, _ := res.Rows[0][0].(int64)
	d, _ := res.Rows[0][1].(int64)
	return n, d
}

// A cold boot's rebuild fills the window from the control tables while the
// node keeps writing: rows that land before the pass starts and between its
// pages are resident already and must be kept — not ingested a second time
// (the dev node had 48 PRR rows twice after its 2026-09-14 rebuild: 30
// written at boot before the pass, the rest between pages).
func TestColdRebuildKeepsRowsWrittenBeforeAndDuringIt(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	storePartition(t, seed, "alpha", partitionRecords(t, 46000, 60))
	simulateCrash(t, seed) // no checkpoint: the next open is cold

	cold := reopenDeferred(t, basePath)
	defer cold.Close()
	if cold.BootState().EngineWarm {
		t.Fatalf("expected a cold engine after a crash without checkpoint: %+v", cold.BootState())
	}
	// Written before the rebuild starts …
	storePartition(t, cold, "alpha", partitionRecords(t, 47000, 3))
	// … and between two of its pages.
	prevPage := engineHotWindowPage
	engineHotWindowPage = 25
	defer func() { engineHotWindowPage = prevPage }()
	var holds atomic.Int32
	var midWrite atomic.Bool
	cold.engineHydrateBatchHook = func() {
		// Hold 1 is the preamble, hold 2 the window floor, hold 3 the first
		// page; the hook runs outside the store lock, so a write here is a
		// live write between page 1 and page 2.
		if holds.Add(1) == 4 && midWrite.CompareAndSwap(false, true) {
			storePartition(t, cold, "alpha", partitionRecords(t, 48000, 2))
		}
	}
	hydrateForTest(t, cold)
	if !midWrite.Load() {
		t.Fatalf("the mid-pass write never fired (%d lock holds) — the fixture is not discriminating", holds.Load())
	}
	rows, distinct := engineDistinctCount(t, cold, "OMM@alpha")
	if rows != 65 || distinct != 65 {
		t.Fatalf("engine OMM@alpha holds %d rows over %d distinct records, want 65 and 65", rows, distinct)
	}
	assertPartitions(t, cold, map[string]int64{"alpha": 65})
}

// The ledger admits one resident row per record: a second ingest of a
// resident record is tombstoned on the spot, whatever path produced it.
func TestSecondIngestOfAResidentRecordIsTombstoned(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	records := partitionRecords(t, 49000, 4)
	storePartition(t, store, "alpha", records)
	cids := make([]string, 0, len(records))
	for _, data := range records {
		cids = append(cids, computeCID(data))
	}
	store.mu.Lock()
	n, _, err := store.ingestEngineBatchLocked("OMM.fbs", "alpha", records, cids, nil)
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if n != 0 {
		t.Fatalf("second ingest of resident records counted %d new rows, want 0", n)
	}
	rows, distinct := engineDistinctCount(t, store, "OMM@alpha")
	if rows != 4 || distinct != 4 {
		t.Fatalf("engine OMM@alpha holds %d rows over %d distinct records after a double ingest, want 4 and 4", rows, distinct)
	}
	assertPartitions(t, store, map[string]int64{"alpha": 4})
}
