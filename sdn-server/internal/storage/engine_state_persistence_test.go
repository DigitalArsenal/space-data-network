package storage

// The engine's records live on disk and a restart USES them (owner law
// 2026-09-02, sdn-operating-model-streams-flatsql): a warm boot answers from
// the persisted record state without re-ingesting a single record, a crash
// after a checkpoint costs exactly the records written since it, and the
// per-standard hot-window bound holds across the restart.

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
	if !store.BootState().Durable {
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
	stats := warm.BootState()
	if !stats.EngineWarm {
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
	loaded, err := warm.HydrateEngineHotWindow()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if loaded != 0 {
		t.Fatalf("hydration re-ingested %d records on a clean restart, want 0", loaded)
	}
	if got := engineFrameCount(t, warm, "OMM"); got != 3 {
		t.Fatalf("OMM answers %d frames after hydration, want 3 (no duplicates)", got)
	}
}

func TestCrashAfterCheckpointIngestsOnlyTheEngineTail(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	for i := 0; i < 2; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(1000+i), "A", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Checkpoint(); err != nil {
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
	stats := reopened.BootState()
	if !stats.EngineWarm || stats.EngineRecords != 2 {
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
	if s := again.BootState(); !s.EngineWarm || s.EngineRecords != 4 {
		t.Fatalf("second restart: %+v (want 4 persisted)", s)
	}
	if got := engineFrameCount(t, again, "OMM"); got != 4 {
		t.Fatalf("OMM answers %d frames on the second restart, want 4", got)
	}
}

func TestHotWindowBoundHoldsAcrossWarmBoot(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	// Window 4 of 5 records: one evicted row is a minority of the arena, so
	// the boot keeps the arena and RE-APPLIES the tombstone the engine forgot.
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(4))
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	for i := 0; i < 5; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(2000+i), "W", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := engineFrameCount(t, store, "OMM"); got != 4 {
		t.Fatalf("live window holds %d frames, want 4", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	warm := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(4), WithDeferredBootRebuilds())
	defer warm.Close()
	if !warm.BootState().EngineWarm {
		t.Fatalf("not warm: %+v", warm.BootState())
	}
	// The engine does not persist tombstones: the daemon's background
	// hydration re-applies the window from the residency ledger.
	if loaded, err := warm.HydrateEngineHotWindow(); err != nil || loaded != 0 {
		t.Fatalf("hydrate after a clean restart loaded %d (err %v), want 0", loaded, err)
	}
	if got := engineFrameCount(t, warm, "OMM"); got != 4 {
		t.Fatalf("window after warm boot holds %d frames, want 4", got)
	}
	warm.mu.RLock()
	resident := warm.engineResidentCount("OMM.fbs")
	warm.mu.RUnlock()
	if resident != 4 {
		t.Fatalf("resident after warm boot = %d, want 4", resident)
	}
}

// TestMostlyDeadArenaIsRebuiltAndShrinks: the engine keeps every row it ever
// ingested and forgets its tombstones. Once the evicted rows outnumber the
// live ones the boot discards the arena and refills the bounded window from
// the tables — and the next flush writes an arena that holds the window only.
func TestMostlyDeadArenaIsRebuiltAndShrinks(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(2))
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	for i := 0; i < 5; i++ {
		if _, err := store.Store("OMM", buildEngineOMM(t, uint32(3000+i), "D", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// 5 persisted rows, 2 live: rebuilt, not reconciled.
	rebuilt := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(2))
	if rebuilt.BootState().EngineWarm {
		t.Fatalf("a mostly-dead arena was opened warm: %+v", rebuilt.BootState())
	}
	if got := engineFrameCount(t, rebuilt, "OMM"); got != 2 {
		t.Fatalf("window after the rebuild holds %d frames, want 2", got)
	}
	if err := rebuilt.Close(); err != nil {
		t.Fatal(err)
	}
	// The flushed arena now holds the window and nothing else, so the next
	// boot is warm with exactly the live rows.
	again := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(2), WithDeferredBootRebuilds())
	defer again.Close()
	if s := again.BootState(); !s.EngineWarm || s.EngineRecords != 2 {
		t.Fatalf("after the rebuild the arena should hold exactly the window: %+v", s)
	}
	if got := engineFrameCount(t, again, "OMM"); got != 2 {
		t.Fatalf("window after the warm reopen holds %d frames, want 2", got)
	}
}

// TestReadersInterleaveWithBackgroundHotWindowHydration is the acceptance for
// the owner's 2026-09-02 ruling that reads are independent of data-layer
// maintenance: while the background hydration rebuilds the engine hot window
// from the control tables, a reader that takes the store lock must answer
// promptly between pages instead of waiting for the whole pass.
func TestReadersInterleaveWithBackgroundHotWindowHydration(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	// Two sources and a small page so the pass takes more than one lock hold;
	// then a crash so the reopen is cold.
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
	if cold.BootState().EngineWarm {
		t.Fatal("expected a cold engine after a crash without checkpoint")
	}
	prevPage := engineHotWindowPage
	engineHotWindowPage = 25
	defer func() { engineHotWindowPage = prevPage }()
	const hold = 400 * time.Millisecond
	var batches atomic.Int32
	cold.engineHydrateBatchHook = func() {
		batches.Add(1)
		time.Sleep(hold)
	}
	done := make(chan error, 1)
	passStart := time.Now()
	go func() {
		_, err := cold.HydrateEngineHotWindow()
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
		_ = cold.BootState() // RLock reader
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

// TestEngineStateFlushedAfterHydrationSurvivesACrash: the hot-window hydration
// flushes the engine's record stream as soon as it completes, so a crash before
// the next periodic checkpoint still leaves an engine state the next boot
// opens WARM.
func TestEngineStateFlushedAfterHydrationSurvivesACrash(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	for i := 0; i < 3; i++ {
		if _, err := seed.Store("OMM", buildEngineOMM(t, uint32(7000+i), "F", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	simulateCrash(t, seed) // cold next time

	cold := reopenDeferred(t, basePath)
	if cold.BootState().EngineWarm {
		t.Fatal("expected a cold engine")
	}
	// The daemon's order: the engine window hydrates in the background and
	// its flush + coverage mark make the engine survive. Die right after — no
	// further checkpoint.
	if _, err := cold.HydrateEngineHotWindow(); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, cold)

	again := reopenDeferred(t, basePath)
	defer again.Close()
	stats := again.BootState()
	if !stats.EngineWarm || stats.EngineRecords != 3 {
		t.Fatalf("after a crash following hydration the engine state must be warm with 3 records: %+v", stats)
	}
	if got := engineFrameCount(t, again, "OMM"); got != 3 {
		t.Fatalf("OMM answers %d frames, want 3", got)
	}
	if loaded, err := again.HydrateEngineHotWindow(); err != nil || loaded != 0 {
		t.Fatalf("hydration after the warm reopen loaded %d (err=%v), want 0", loaded, err)
	}
	if got := engineFrameCount(t, again, "OMM"); got != 3 {
		t.Fatalf("OMM answers %d frames after hydration, want 3 (no duplicates)", got)
	}
}
