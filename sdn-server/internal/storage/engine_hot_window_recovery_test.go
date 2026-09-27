package storage

// Every engine call on the rebuild and recovery paths is bounded, a timeout
// never costs the record arena, and a poison recovery never holds the store
// lock through a whole rebuild. The failures these pin were measured on
// host-02, 2026-09-27 17:41–18:13Z.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// poisonAsBudgetOverrun leaves a runtime exactly as an engine call that ran
// past its per-call budget does.
func poisonAsBudgetOverrun(engine *flatsqlrt.Runtime, call string) {
	engine.WasmModule().MarkPoisoned(fmt.Errorf("dedicated execution thread abandoned mid-call in %q: %w", call, wasmrt.ErrExecutionTimeout))
}

// A cold rebuild reads at most engineHotWindowPageBytes of record payload per
// page call (never fewer than one record): with a one-byte budget every page
// holds one record, and every record still lands exactly once.
func TestColdRebuildPagesAreBoundedByBytes(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	storePartition(t, seed, "alpha", partitionRecords(t, 51000, 40))
	simulateCrash(t, seed) // no checkpoint: the next open is cold

	cold := reopenDeferred(t, basePath)
	defer cold.Close()
	if cold.BootState().EngineWarm {
		t.Fatalf("expected a cold engine: %+v", cold.BootState())
	}
	prevPage, prevBytes := engineHotWindowPage, engineHotWindowPageBytes
	engineHotWindowPage, engineHotWindowPageBytes = 25, 1
	defer func() { engineHotWindowPage, engineHotWindowPageBytes = prevPage, prevBytes }()
	var holds atomic.Int32
	cold.engineHydrateBatchHook = func() { holds.Add(1) }
	hydrateForTest(t, cold)

	rows, distinct := engineDistinctCount(t, cold, "OMM@alpha")
	if rows != 40 || distinct != 40 {
		t.Fatalf("engine OMM@alpha holds %d rows over %d distinct records, want 40 and 40", rows, distinct)
	}
	assertPartitions(t, cold, map[string]int64{"alpha": 40})
	if holds.Load() < 40 {
		t.Fatalf("40 records rebuilt in %d page calls under a one-byte budget; each call must stop after one record", holds.Load())
	}
}

// An engine call that runs out of time while opening the record state says
// nothing about the state: the open is retried on a fresh runtime and the
// arena opens WARM, not discarded and re-ingested.
func TestEngineOpenTimeoutKeepsTheRecordArena(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	storePartition(t, seed, "alpha", partitionRecords(t, 52000, 30))
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	engineOpenAttemptHook = func(attempt int, engine *flatsqlrt.Runtime) {
		if attempt == 0 {
			poisonAsBudgetOverrun(engine, "flatsql_open_state")
		}
	}
	defer func() { engineOpenAttemptHook = nil }()
	store := reopenDeferred(t, basePath)
	defer store.Close()
	if boot := store.BootState(); !boot.EngineWarm || boot.EngineRecords < 30 {
		t.Fatalf("a timed-out first open cost the arena: %+v (want warm with the 30 persisted records)", boot)
	}
	hydrateForTest(t, store)
	assertPartitions(t, store, map[string]int64{"alpha": 30})
}

// If every attempt runs out of time the open FAILS — and leaves the arena on
// disk exactly as it was, for an attempt on a host that is not starved.
func TestEngineOpenThatKeepsTimingOutFailsAndKeepsTheArena(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	storePartition(t, seed, "alpha", partitionRecords(t, 53000, 20))
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	arena := filepath.Join(basePath, flatSQLControlDBName) + ".fsdata"
	before, err := os.ReadFile(arena)
	if err != nil || len(before) == 0 {
		t.Fatalf("no persisted arena after a clean close (%d bytes, %v)", len(before), err)
	}

	engineOpenAttemptHook = func(_ int, engine *flatsqlrt.Runtime) { poisonAsBudgetOverrun(engine, "flatsql_open_state") }
	_, err = NewFlatSQLStore(basePath, bootTestValidator(t), WithDeferredBootRebuilds())
	engineOpenAttemptHook = nil
	if !errors.Is(err, errEngineOpenTimedOut) {
		t.Fatalf("open with every attempt timing out = %v, want errEngineOpenTimedOut", err)
	}
	after, err := os.ReadFile(arena)
	if err != nil || string(after) != string(before) {
		t.Fatalf("the arena changed across timed-out opens (%d -> %d bytes, %v)", len(before), len(after), err)
	}

	store := reopenDeferred(t, basePath)
	defer store.Close()
	if !store.BootState().EngineWarm {
		t.Fatalf("the arena did not open warm once the host stopped timing out: %+v", store.BootState())
	}
}

// A dirty exit — the process gone without the final checkpoint, as when an
// update shutdown cannot drain — reopens from the last flushed arena: warm,
// nothing discarded, only the tail past the coverage mark ingested.
func TestDirtyExitReopensWarmWithoutDiscarding(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	storePartition(t, store, "alpha", partitionRecords(t, 54000, 25))
	if err := store.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	storePartition(t, store, "alpha", partitionRecords(t, 54100, 5)) // past the mark
	simulateCrash(t, store)

	reopened := reopenDeferred(t, basePath)
	defer reopened.Close()
	if boot := reopened.BootState(); !boot.EngineWarm || boot.EngineRecords < 25 {
		t.Fatalf("a dirty exit reopened %+v, want warm with the 25 flushed records", boot)
	}
	hydrateForTest(t, reopened)
	rows, distinct := engineDistinctCount(t, reopened, "OMM@alpha")
	if rows != 30 || distinct != 30 {
		t.Fatalf("engine OMM@alpha holds %d rows over %d distinct records after a dirty exit, want 30 and 30", rows, distinct)
	}
}

// forgetEngineArenaForTest empties the persisted arena so the next open of
// the engine state is cold.
func forgetEngineArenaForTest(t *testing.T, basePath string) {
	t.Helper()
	if err := os.Truncate(filepath.Join(basePath, flatSQLControlDBName)+".fsdata", 0); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// A recovery whose arena reopens COLD refills the window in the background:
// RecoverPoisonedEngine returns at once, writes land while the refill runs,
// and Close cancels a refill between pages.
func TestColdRecoveryRefillsWithoutHoldingTheStoreLock(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	prevWatch := enginePoisonWatchInterval
	enginePoisonWatchInterval = time.Hour // this test drives recovery itself
	defer func() { enginePoisonWatchInterval = prevWatch }()
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	storePartition(t, store, "alpha", partitionRecords(t, 55000, 30))

	release := make(chan struct{})
	var entered atomic.Bool
	store.engineHydrateBatchHook = func() {
		if entered.CompareAndSwap(false, true) {
			<-release
		}
	}
	engine, _ := store.EngineRuntime()
	engine.MarkPoisoned()
	// The replacement reopens cold: the arena on disk no longer holds anything.
	forgetEngineArenaForTest(t, basePath)
	if _, err := store.RecoverPoisonedEngine(); err != nil {
		t.Fatalf("RecoverPoisonedEngine: %v", err)
	}
	if store.BootState().EngineWarm {
		t.Fatal("the fixture did not force a cold recovery")
	}
	deadline := time.Now().Add(30 * time.Second)
	for !entered.Load() {
		if time.Now().After(deadline) {
			t.Fatal("no background refill started after a cold recovery")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The refill is parked outside the lock: a write must land now.
	done := make(chan error, 1)
	go func() {
		_, err := store.StoreBatchWithSourceTags("OMM.fbs", partitionRecords(t, 55100, 1), "peer", nil,
			SourceTags{ProviderID: "prov", SourceName: "alpha", BatchID: "alpha-batch"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write during the refill: %v", err)
		}
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("a write waited behind the post-recovery refill: the store lock is held through the rebuild")
	}
	close(release)
	for !store.EngineHotWindowHydrated() {
		if time.Now().After(deadline) {
			t.Fatal("the background refill did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rows, distinct := engineDistinctCount(t, store, "OMM@alpha")
	if rows != 31 || distinct != 31 {
		t.Fatalf("engine OMM@alpha holds %d rows over %d distinct records after the refill, want 31 and 31", rows, distinct)
	}

	// Close cancels a refill in progress between pages.
	engine, _ = store.EngineRuntime()
	engine.MarkPoisoned()
	forgetEngineArenaForTest(t, basePath)
	release2 := make(chan struct{})
	var entered2 atomic.Bool
	store.engineHydrateBatchHook = func() {
		if entered2.CompareAndSwap(false, true) {
			<-release2
		}
	}
	if _, err := store.RecoverPoisonedEngine(); err != nil {
		t.Fatalf("second RecoverPoisonedEngine: %v", err)
	}
	for !entered2.Load() {
		if time.Now().After(deadline) {
			t.Fatal("no background refill started after the second cold recovery")
		}
		time.Sleep(5 * time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	time.Sleep(50 * time.Millisecond)
	close(release2)
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not cancel the post-recovery refill")
	}
}
