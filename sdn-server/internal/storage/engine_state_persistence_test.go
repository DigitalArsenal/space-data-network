package storage

// The engine's records live on disk and a restart USES them (owner law
// 2026-09-02, sdn-operating-model-streams-flatsql): a warm boot answers from
// the persisted record state without re-ingesting a single record, a crash
// after a checkpoint costs exactly the journal tail, and the per-standard
// hot-window bound holds across the restart.

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

func engineFrameCount(t *testing.T, s *FlatSQLStore, table string) int {
	t.Helper()
	stream, err := s.QueryRawStream(`SELECT _data FROM "`+table+`" ORDER BY _rowid DESC LIMIT ?`, 1000)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode %s frames: %v", table, err)
	}
	return len(frames)
}

func TestWarmBootServesPersistedEngineRecordsWithoutReingest(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if !store.BootReplay().Durable {
		t.Skip("engine has no filesystem on this host — the persisted-state lane is inert")
	}
	for i, norad := range []uint32{25544, 43013, 48274} {
		if _, err := store.Store("OMM", buildEngineOMM(t, norad, "SAT", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatalf("store OMM %d: %v", norad, err)
		}
	}
	if _, err := store.Store("IRM", buildEngineIRM(t, "job-1", 1, 4096), "peer", nil); err != nil {
		t.Fatalf("store IRM: %v", err)
	}
	if err := store.Close(); err != nil { // clean shutdown: flush + mark
		t.Fatalf("close: %v", err)
	}

	// Deferred reopen = the daemon's boot: NO rebuild and NO hydration have
	// run when the first query arrives.
	warm := reopenDeferred(t, basePath)
	defer warm.Close()
	stats := warm.BootReplay()
	if !stats.Warm || !stats.EngineWarm {
		t.Fatalf("clean restart did not open persisted state: %+v", stats)
	}
	if stats.EngineRecords != 4 {
		t.Fatalf("engine reported %d persisted records, want 4", stats.EngineRecords)
	}
	if got := engineFrameCount(t, warm, "OMM"); got != 3 {
		t.Fatalf("OMM answers %d frames before any hydration, want 3", got)
	}
	if got := engineFrameCount(t, warm, "IRM"); got != 1 {
		t.Fatalf("IRM answers %d frames before any hydration, want 1", got)
	}
	warm.mu.RLock()
	resident := warm.engineResidentCount("OMM.fbs")
	warm.mu.RUnlock()
	if resident != 3 {
		t.Fatalf("resident bookkeeping restored %d OMM, want 3", resident)
	}
	loaded, err := warm.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if loaded != 0 {
		t.Fatalf("hydration re-ingested %d records on a clean restart, want 0", loaded)
	}
	if passes := warm.recordCatalog.EngineHotWindowPasses(); passes != 0 {
		t.Fatalf("hydration scanned the journal %d time(s) on a clean restart, want 0", passes)
	}
	if got := engineFrameCount(t, warm, "OMM"); got != 3 {
		t.Fatalf("OMM answers %d frames after hydration, want 3 (no duplicates)", got)
	}
}

func TestCrashAfterCheckpointIngestsOnlyTheEngineTail(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if !store.BootReplay().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	for i := 0; i < 2; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(1000+i), "A", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Two more records after the checkpoint, then die without one.
	for i := 2; i < 4; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(1000+i), "B", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	simulateCrash(t, store)

	// Synchronous open (the CLI shape): the tail is ingested during open.
	reopened := newEngineRecordsStore(t, basePath)
	stats := reopened.BootReplay()
	if !stats.Warm || !stats.EngineWarm || stats.EngineRecords != 2 {
		t.Fatalf("crash boot: %+v (want warm with 2 persisted records)", stats)
	}
	if got := engineFrameCount(t, reopened, "OMM"); got != 4 {
		t.Fatalf("OMM answers %d frames after the tail, want 4", got)
	}
	reopened.mu.RLock()
	resident := reopened.engineResidentCount("OMM.fbs")
	reopened.mu.RUnlock()
	if resident != 4 {
		t.Fatalf("resident bookkeeping after tail = %d, want 4", resident)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	// And once more, clean: everything persisted, nothing re-ingested.
	again := reopenDeferred(t, basePath)
	defer again.Close()
	if s := again.BootReplay(); !s.EngineWarm || s.EngineRecords != 4 {
		t.Fatalf("second restart: %+v (want 4 persisted)", s)
	}
	if got := engineFrameCount(t, again, "OMM"); got != 4 {
		t.Fatalf("OMM answers %d frames on the second restart, want 4", got)
	}
}

func TestHotWindowBoundHoldsAcrossWarmBoot(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(2))
	if !store.BootReplay().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	for i := 0; i < 5; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(2000+i), "W", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := engineFrameCount(t, store, "OMM"); got != 2 {
		t.Fatalf("live window holds %d frames, want 2", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	warm := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(2), WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	defer warm.Close()
	if !warm.BootReplay().EngineWarm {
		t.Fatalf("not warm: %+v", warm.BootReplay())
	}
	// The engine does not persist tombstones: the window is re-applied at open.
	if got := engineFrameCount(t, warm, "OMM"); got != 2 {
		t.Fatalf("window after warm boot holds %d frames, want 2", got)
	}
	warm.mu.RLock()
	resident := warm.engineResidentCount("OMM.fbs")
	warm.mu.RUnlock()
	if resident != 2 {
		t.Fatalf("resident after warm boot = %d, want 2", resident)
	}
}

// TestReadersInterleaveWithBackgroundHotWindowHydration is the acceptance for
// the owner's 2026-09-02 ruling that reads are independent of data-layer
// maintenance: while the background hydration rebuilds the engine hot window
// from a cold journal, a reader that takes the store lock must answer
// promptly between ingest batches instead of waiting for the whole pass.
func TestReadersInterleaveWithBackgroundHotWindowHydration(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootReplay().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	// Two sources so the pass flushes more than one batch even below the
	// batch bound; then a crash so the reopen is cold.
	for src := 0; src < 2; src++ {
		tags := SourceTags{ProviderID: "prov", SourceName: fmt.Sprintf("interleave-%d", src), BatchID: "b"}
		records := make([][]byte, 0, 40)
		for i := 0; i < 40; i++ {
			records = append(records, buildEngineOMM(t, uint32(50000+src*100+i), "IL", 1700000000+int64(i)))
		}
		if _, err := seed.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	simulateCrash(t, seed)

	cold := reopenDeferred(t, basePath)
	defer cold.Close()
	if cold.BootReplay().EngineWarm {
		t.Fatal("expected a cold engine after a crash without checkpoint")
	}
	const hold = 400 * time.Millisecond
	var batches atomic.Int32
	cold.engineHydrateBatchHook = func() {
		batches.Add(1)
		time.Sleep(hold)
	}
	done := make(chan error, 1)
	passStart := time.Now()
	go func() {
		_, err := cold.HydrateEngineHotWindowFromRecordCatalog()
		done <- err
	}()
	// Give the pass time to enter its first batch, then read repeatedly.
	time.Sleep(hold / 2)
	var slowest time.Duration
	reads := 0
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("hydration: %v", err)
			}
			if batches.Load() < 2 {
				t.Fatalf("the pass flushed %d batch(es); the test needs at least 2 to observe interleaving", batches.Load())
			}
			if reads < 2 {
				t.Fatalf("only %d reads completed during a %d-batch pass", reads, batches.Load())
			}
			pass := time.Since(passStart)
			// The property: no reader waits for the pass. One batch's critical
			// section (ingest + a first-seen source's view rebuild) is the most
			// a reader may pay — never the sleeps that hold the pass open.
			if slowest > pass/2 || slowest > hold*2 {
				t.Fatalf("a reader waited %s of a %s pass (%d batches, hold %s): readers are parked behind the pass", slowest, pass, batches.Load(), hold)
			}
			t.Logf("pass %s over %d batches; %d reads, slowest %s", pass, batches.Load(), reads, slowest)
			if count, err := cold.EngineRecordCount("OMM.fbs"); err != nil || count != 80 {
				t.Fatalf("engine count after hydration = %d err=%v, want 80", count, err)
			}
			return
		default:
		}
		start := time.Now()
		_ = cold.BootReplay() // RLock reader
		rlock := time.Since(start)
		if _, err := cold.EngineRecordCount("OMM.fbs"); err != nil {
			t.Fatalf("read during hydration: %v", err)
		}
		if d := time.Since(start); d > slowest {
			slowest = d
			t.Logf("read %d: RLock %s, total %s (batches so far %d)", reads, rlock, d, batches.Load())
		}
		reads++
		time.Sleep(50 * time.Millisecond)
	}
}
