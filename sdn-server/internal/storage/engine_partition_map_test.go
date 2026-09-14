package storage

import (
	"fmt"
	"path/filepath"
	"testing"
)

// The engine routes every frame it replays at open through its persisted
// partition map (source → byte range of the arena). These tests pin the one
// rule the store enforces on that map: it is discarded WITH the arena, and a
// map that cannot describe the stream on disk is never trusted.
//
// Dev node, 2026-09-14: an arena discard that removed only the file left the
// old map behind; the rebuilt arena's IQC rows sat at offsets the old map
// gave to CAT's source, the next boot attributed 6,412 live rows to
// "IQC@cat-replay", and the residency reconcile tombstoned them as extras.

func partitionRecords(t *testing.T, base uint32, n int) [][]byte {
	t.Helper()
	records := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, buildEngineOMM(t, base+uint32(i), fmt.Sprintf("P%d", base), 1700000000+int64(i)))
	}
	return records
}

func storePartition(t *testing.T, s *FlatSQLStore, source string, records [][]byte) {
	t.Helper()
	tags := SourceTags{ProviderID: "prov", SourceName: source, BatchID: source + "-batch"}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, tags); err != nil {
		t.Fatalf("store %d records under %q: %v", len(records), source, err)
	}
}

func hydrateForTest(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	if _, err := s.HydrateEngineHotWindow(); err != nil {
		t.Fatalf("HydrateEngineHotWindow: %v", err)
	}
}

// assertPartitions checks the engine's per-source partitions against the
// residency ledger, which is the ground truth for what the window holds.
func assertPartitions(t *testing.T, s *FlatSQLStore, want map[string]int64) {
	t.Helper()
	for source, n := range want {
		if got := engineCount(t, s, "OMM@"+source); got != n {
			t.Errorf("engine partition OMM@%s holds %d rows, want %d", source, got, n)
		}
		var ledger int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' AND source = ?`, source).Scan(&ledger); err != nil {
			t.Fatal(err)
		}
		if ledger != n {
			t.Errorf("residency ledger lists %d rows for OMM/%s, want %d", ledger, source, n)
		}
	}
	s.mu.RLock()
	reason, bad := engineRecordStateInconsistent(s.engineDB, s.controlDBPath)
	s.mu.RUnlock()
	if bad {
		t.Errorf("engine record state is inconsistent after the boot: %s", reason)
	}
}

// The boot's own discard (a mostly-dead arena) must take the partition map
// with it. Rows a later source appends at the discarded arena's offsets belong
// to that source after the next restart — not to whoever owned those bytes
// before.
func TestArenaDiscardTakesThePartitionMapWithIt(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	alpha := partitionRecords(t, 40000, 60)
	storePartition(t, seed, "alpha", alpha)
	// Delete every alpha row: the ledger empties, the arena keeps 60 frames
	// the engine will resurrect, so the next open finds it mostly dead.
	for _, data := range alpha {
		if err := seed.Delete("OMM.fbs", computeCID(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	rebuilt := reopenDeferred(t, basePath)
	if rebuilt.BootState().EngineWarm {
		t.Fatalf("a mostly-dead arena was opened warm: %+v", rebuilt.BootState())
	}
	hydrateForTest(t, rebuilt)
	// The dev-node shape, in three moves. A SHORT beta range at offset 0,
	// flushed once; then a gamma range that runs from beta's end to past
	// where alpha's old range ended. With a stale map, the second flush
	// re-persists alpha's old range (its end is still above the mark, and
	// the engine re-writes a range until the mark passes it) OVER beta's
	// start-0 row, which is below the mark by then and never written again.
	// The next boot then routes every frame under alpha's old range — beta's
	// and gamma's first — to alpha, while gamma's tail is routed right, so
	// gamma's partition has a high max seq and the misrouted rows are
	// neither lost nor tracked: the reconcile tombstones them as extras.
	beta := partitionRecords(t, 41000, 10)
	storePartition(t, rebuilt, "beta", beta)
	if err := rebuilt.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	gamma := partitionRecords(t, 42000, 100)
	storePartition(t, rebuilt, "gamma", gamma)
	if err := rebuilt.Close(); err != nil {
		t.Fatal(err)
	}

	warm := newEngineRecordsStore(t, basePath)
	defer warm.Close()
	if !warm.BootState().EngineWarm {
		t.Fatalf("expected a warm engine after a clean close: %+v", warm.BootState())
	}
	assertPartitions(t, warm, map[string]int64{"alpha": 0, "beta": 10, "gamma": 100})
	if got := engineCount(t, warm, "OMM"); got != 110 {
		t.Fatalf("unified OMM view holds %d rows, want 110", got)
	}
}

// An arena removed by hand (or by an older binary) leaves an index that
// claims a mark over a stream that is not there. That state is discarded
// whole at the next open, before the engine restores any of it.
func TestArenaRemovedByHandDiscardsTheIndexAndMap(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	alpha := partitionRecords(t, 43000, 30)
	storePartition(t, seed, "alpha", alpha)
	controlDBPath := seed.controlDBPath
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeEngineRecordStreamForTest(controlDBPath); err != nil {
		t.Fatal(err)
	}

	cold := reopenDeferred(t, basePath)
	if cold.BootState().EngineWarm {
		t.Fatalf("an index without its arena was opened warm: %+v", cold.BootState())
	}
	hydrateForTest(t, cold) // alpha comes back from the tables
	beta := partitionRecords(t, 44000, 60)
	storePartition(t, cold, "beta", beta)
	assertPartitions(t, cold, map[string]int64{"alpha": 30, "beta": 60})
	if err := cold.Close(); err != nil {
		t.Fatal(err)
	}

	warm := newEngineRecordsStore(t, basePath)
	defer warm.Close()
	if !warm.BootState().EngineWarm {
		t.Fatalf("expected a warm engine after a clean close: %+v", warm.BootState())
	}
	assertPartitions(t, warm, map[string]int64{"alpha": 30, "beta": 60})
}

// A map that is already polluted on disk — ranges that overlap, or reach past
// the stream — is recognised at open and discarded with the arena, so the
// window is rebuilt from the tables instead of routed by the wrong map.
func TestPollutedPartitionMapIsDiscardedAtOpen(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	alpha := partitionRecords(t, 45000, 30)
	storePartition(t, seed, "alpha", alpha)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	// Plant a stale range over alpha's bytes, the way an incomplete discard
	// used to leave one behind. The store's own flush never removes a row.
	polluter := reopenDeferred(t, basePath)
	polluter.mu.Lock()
	_, err := polluter.engineDB.Query(`INSERT OR REPLACE INTO _flatsql_source_ranges("start", "stop", source) VALUES ('1', '999999999', 'ghost')`)
	polluter.mu.Unlock()
	if err != nil {
		t.Fatalf("plant a stale partition range: %v", err)
	}
	if err := polluter.Close(); err != nil {
		t.Fatal(err)
	}

	rebuilt := newEngineRecordsStore(t, basePath)
	defer rebuilt.Close()
	if rebuilt.BootState().EngineWarm {
		t.Fatalf("a polluted partition map was trusted: %+v", rebuilt.BootState())
	}
	assertPartitions(t, rebuilt, map[string]int64{"alpha": 30})
}
