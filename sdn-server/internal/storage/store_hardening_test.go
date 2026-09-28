package storage

// Interim hardening of the record store from the 2026-09-27 stress campaign:
// reads after Close are errors (D7), eviction picks only live rows and runs in
// bounded steps (D5), a clean stop reopens warm, the engine arena is held
// under a byte budget (D4), the source-filtered window is driven from the
// batch (D2), and a dataset import honours its caller's deadline (D8).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// seedHardeningStore stores n OMM records under one source in batches.
func seedHardeningOMM(t *testing.T, s *FlatSQLStore, source string, base uint32, n int) [][]byte {
	t.Helper()
	records := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, buildEngineOMM(t, base+uint32(i), fmt.Sprintf("H%d", base), 1_700_000_000+int64(i)*60))
	}
	tags := SourceTags{ProviderID: "prov", SourceName: source, BatchID: source + "-batch"}
	for start := 0; start < len(records); start += 500 {
		end := start + 500
		if end > len(records) {
			end = len(records)
		}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", records[start:end], "peer", nil, tags); err != nil {
			t.Fatalf("store %s records %d-%d: %v", source, start, end, err)
		}
	}
	return records
}

func ledgerCount(t *testing.T, s *FlatSQLStore, schemaName string) int64 {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, err := s.engineResidencyCount(schemaName)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// ==================== D7: reads after Close ====================

// Every read entry point a daemon's API or background lanes reach after Close
// answers an error — the store's own ErrStoreClosed where the store decides —
// instead of dereferencing a nil database (a SIGSEGV with no traceback once
// the engine had run).
func TestReadsAfterCloseReturnErrors(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	records := seedHardeningOMM(t, store, "closed", 61000, 20)
	cid := computeCID(records[0])
	if _, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 5}); err != nil {
		t.Fatalf("read before Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	window := IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "prov", SourceName: "closed", BatchID: "closed-batch", Limit: 10}
	reads := map[string]func() error{
		"QueryIndexedRecords": func() error { _, err := store.QueryIndexedRecords(window); return err },
		"IndexedRecordWindowLimitForBytes": func() error {
			_, _, err := store.IndexedRecordWindowLimitForBytes(window, 1<<20)
			return err
		},
		"GetRecord":       func() error { _, err := store.GetRecord("OMM.fbs", cid); return err },
		"Get":             func() error { _, err := store.Get("OMM.fbs", cid); return err },
		"Query":           func() error { _, err := store.Query("OMM.fbs", ""); return err },
		"QueryAll":        func() error { _, err := store.QueryAll("OMM.fbs", 10); return err },
		"Count":           func() error { _, err := store.Count("OMM.fbs"); return err },
		"Stats":           func() error { _, err := store.Stats(); return err },
		"DataSummary":     func() error { _, err := store.DataSummary(); return err },
		"LiveRecordBytes": func() error { _, err := store.LiveRecordBytes(); return err },
		"CountRawRecords": func() error {
			_, err := store.CountRawRecords(RawRecordQuery{SchemaName: "OMM.fbs"})
			return err
		},
		"QueryRawRecords": func() error {
			_, err := store.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Limit: 5})
			return err
		},
		"GetRawRecord":       func() error { _, err := store.GetRawRecord("OMM.fbs", cid); return err },
		"QueryRecentRecords": func() error { _, err := store.QueryRecentRecords("OMM.fbs", 5); return err },
		"GetSourceTags":      func() error { _, err := store.GetSourceTags("OMM.fbs", cid); return err },
		"SourceBatchProgress": func() error {
			_, err := store.SourceBatchProgress()
			return err
		},
		"QueryRawStream": func() error {
			_, err := store.QueryRawStream(`SELECT _data FROM "OMM" LIMIT 1`)
			return err
		},
		"EngineRecordCount": func() error { _, err := store.EngineRecordCount("OMM.fbs"); return err },
		"GarbageCollectToQuota": func() error {
			_, err := store.GarbageCollectToQuota(1)
			return err
		},
	}
	for name, read := range reads {
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("%s after Close panicked: %v", name, p)
				}
			}()
			err = read()
		}()
		if err == nil {
			t.Errorf("%s after Close returned no error", name)
		}
	}
	// The store's own read paths name the condition.
	for _, name := range []string{"QueryIndexedRecords", "IndexedRecordWindowLimitForBytes", "GetRecord", "Count", "DataSummary", "CountRawRecords", "QueryRawRecords", "Stats"} {
		if err := reads[name](); !errors.Is(err, ErrStoreClosed) {
			t.Errorf("%s after Close: %v, want ErrStoreClosed", name, err)
		}
	}
}

const hardeningChildEnv = "SDN_STORE_HARDENING_CHILD"

// A nil dereference inside recover() is recovered after the engine has run,
// and a read after Close returns an error: in a child process, because the
// failure being guarded against is the death of the process.
func TestNilDereferenceAfterEngineRanIsRecovered(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreHardeningChild$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), hardeningChildEnv+"=recover")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "RECOVERED") || !strings.Contains(string(out), "READ AFTER CLOSE: datastore is closed") {
		t.Fatalf("child err=%v:\n%s", err, out)
	}
}

func TestStoreHardeningChild(t *testing.T) {
	if os.Getenv(hardeningChildEnv) == "" {
		t.Skip("child process only")
	}
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	seedHardeningOMM(t, store, "child", 62000, 10)
	if _, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if got := engineCount(t, store, "OMM"); got == 0 {
		t.Fatal("the engine did not run")
	}
	func() {
		defer func() {
			if p := recover(); p != nil {
				fmt.Printf("RECOVERED: %v\n", p)
			}
		}()
		var p *FlatSQLStore
		_ = p.dbPath
	}()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 5})
	fmt.Printf("READ AFTER CLOSE: %v\n", err)
}

// ==================== D5: eviction ====================

// After a warm open the engine's partitions hold every row ever flushed —
// the evicted ones too, because the engine does not persist tombstones —
// until hydration re-applies them. A write that lands before that evicts
// from the LEDGER: the residency count comes down to the window, where
// asking the partitions for their oldest rows picked the dead rows and left
// the live overflow resident.
func TestEvictionAfterWarmOpenTakesLiveRows(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	const window = 20
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(window))
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "warm", 63000, 200) // 180 evicted, 20 live
	if got := ledgerCount(t, store, "OMM.fbs"); got != window {
		t.Fatalf("ledger holds %d rows before the restart, want %d", got, window)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	warm, err := NewFlatSQLStore(basePath, bootTestValidator(t), WithEngineHotWindow(window), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	if !warm.BootState().EngineWarm {
		t.Fatalf("expected a warm open: %+v", warm.BootState())
	}
	// The dead rows are back in the partition before hydration.
	if got := engineCount(t, warm, "OMM@warm"); got <= window {
		t.Fatalf("partition holds %d rows before hydration; the test needs the resurrected dead rows", got)
	}
	// A write before hydration: 10 new records push the window 10 over.
	seedHardeningOMM(t, warm, "warm", 64000, 10)
	if got := ledgerCount(t, warm, "OMM.fbs"); got != window {
		t.Fatalf("after a write on the warm open the ledger holds %d rows, want the window (%d): eviction took dead rows", got, window)
	}
	hydrateForTest(t, warm)
	if got := engineCount(t, warm, "OMM@warm"); got != window {
		t.Fatalf("after hydration the partition serves %d rows, want %d", got, window)
	}
	if got := ledgerCount(t, warm, "OMM.fbs"); got != window {
		t.Fatalf("after hydration the ledger holds %d rows, want %d", got, window)
	}
}

// An overflow is drained in bounded steps with the store lock released
// between them, never in one hold. The steps' lock holds are reported.
func TestEngineEvictionRunsInBoundedSteps(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(12000))
	if !seed.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, seed, "steps", 70000, 10000)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen with a window of 1000: 9000 rows over, drained by the hydration.
	store, err := NewFlatSQLStore(basePath, bootTestValidator(t), WithEngineHotWindow(1000), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engineEvictionAccount.mu.Lock()
	engineEvictionAccount.steps, engineEvictionAccount.rows, engineEvictionAccount.maxRows = 0, 0, 0
	engineEvictionAccount.longest, engineEvictionAccount.longestWork = 0, 0
	engineEvictionAccount.mu.Unlock()
	lockBefore := store.StoreLockStats()
	hydrateForTest(t, store)
	stat := EngineEvictionStats()
	if stat.Rows != 9000 {
		t.Fatalf("hydration evicted %d rows, want 9000", stat.Rows)
	}
	if stat.MaxRows > engineEvictionStep {
		t.Fatalf("one step evicted %d rows; a step is at most %d", stat.MaxRows, engineEvictionStep)
	}
	if want := (9000 + engineEvictionStep - 1) / engineEvictionStep; stat.Steps < want {
		t.Fatalf("9000 rows evicted in %d step(s), want at least %d", stat.Steps, want)
	}
	if got := ledgerCount(t, store, "OMM.fbs"); got != 1000 {
		t.Fatalf("ledger holds %d rows after the drain, want 1000", got)
	}
	if got := engineCount(t, store, "OMM@steps"); got != 1000 {
		t.Fatalf("partition serves %d rows after the drain, want 1000", got)
	}
	lock := store.StoreLockStats()
	t.Logf("eviction: %d rows in %d steps of at most %d; longest step %s (engine work %s, the rest its COMMIT); store write lock held %s over %d acquisitions during hydration",
		stat.Rows, stat.Steps, engineEvictionStep, stat.Longest, stat.LongestWork, lock.WriteHeld-lockBefore.WriteHeld, lock.WriteAcquires-lockBefore.WriteAcquires)
	// The engine work of a step is bounded by the step; its COMMIT is the
	// store's ordinary commit and is not this test's to bound.
	if stat.LongestWork > time.Second {
		t.Fatalf("one eviction step of at most %d rows spent %s of engine work under the lock", engineEvictionStep, stat.LongestWork)
	}
}

// ==================== Clean stop ====================

// A clean stop reopens warm: the arena it flushed is mostly dead rows (every
// eviction stays in it), and the warm open re-applies their tombstones in
// batches rather than discarding the arena and rebuilding the window.
func TestCleanStopReopensWarmWithMostlyDeadArena(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	basePath := filepath.Join(t.TempDir(), "store")
	const window = 50
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(window))
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "clean", 80000, 1500) // 1450 dead, 50 live
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	warm, err := NewFlatSQLStore(basePath, bootTestValidator(t), WithEngineHotWindow(window), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	open := time.Since(started)
	boot := warm.BootState()
	if !boot.EngineWarm || boot.EngineRecords != 1500 {
		t.Fatalf("a clean stop did not reopen warm with its arena: %+v", boot)
	}
	hydrateStart := time.Now()
	hydrateForTest(t, warm)
	t.Logf("clean-stop reopen: open %s, hydration (re-applying %d tombstones) %s", open, 1500-window, time.Since(hydrateStart))
	if got := engineFrameCount(t, warm, "OMM"); got != window {
		t.Fatalf("warm window serves %d frames, want %d", got, window)
	}
	if got := ledgerCount(t, warm, "OMM.fbs"); got != window {
		t.Fatalf("ledger holds %d rows, want %d", got, window)
	}
}

// Past the compaction mark a mostly-dead arena is still discarded at open:
// that is the only way the engine gives bytes back.
func TestMostlyDeadArenaPastTheCompactionMarkIsDiscarded(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	prev := engineArenaCompactBytes
	engineArenaCompactBytes = 64 << 10
	defer func() { engineArenaCompactBytes = prev }()
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(20))
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "compact", 81000, 1000)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if size := engineStreamSize(filepath.Join(basePath, flatSQLControlDBName)); int64(size) <= engineArenaCompactBytes {
		t.Fatalf("arena is %d bytes; the test needs it past the %d-byte mark", size, engineArenaCompactBytes)
	}
	rebuilt := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(20))
	defer rebuilt.Close()
	if rebuilt.BootState().EngineWarm {
		t.Fatalf("a mostly-dead arena past the compaction mark was opened warm: %+v", rebuilt.BootState())
	}
	if got := engineFrameCount(t, rebuilt, "OMM"); got != 20 {
		t.Fatalf("window after the rebuild holds %d frames, want 20", got)
	}
}

// ==================== D4: the arena byte budget ====================

// The node never puts more bytes in the engine arena than its budget, however
// many records it stores, and the engine is never poisoned by it: the records
// beyond the budget stay in the control tables and the arena's measure agrees
// with the engine's own after a flush.
func TestEngineArenaStaysUnderItsByteBudget(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	prev := engineArenaBudgetBytes
	engineArenaBudgetBytes = 96 << 10
	defer func() { engineArenaBudgetBytes = prev }()
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(100000))
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "budget", 82000, 2000)
	bytes, budget := store.EngineArenaBytes()
	if bytes > budget {
		t.Fatalf("arena measured at %d bytes, past its %d-byte budget", bytes, budget)
	}
	if n := indexCount(t, store, "OMM.fbs"); n != 2000 {
		t.Fatalf("control tables hold %d records, want all 2000", n)
	}
	rt, _ := store.EngineRuntime()
	if rt.Poisoned() {
		t.Fatal("the engine was poisoned")
	}
	mirrored := engineCount(t, store, "OMM@budget")
	if mirrored == 0 || mirrored >= 2000 {
		t.Fatalf("engine mirrored %d records; the budget should have stopped it part-way", mirrored)
	}
	if err := store.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	flushed, err := store.engineDB.FlushedOffset()
	store.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := store.EngineArenaBytes(); after != flushed || after > budget {
		t.Fatalf("after a flush the measure is %d bytes, the engine reports %d, budget %d", after, flushed, budget)
	}
	t.Logf("arena %d of %d bytes; %d of 2000 records mirrored, %d bytes refused", flushed, budget, mirrored, store.engineArenaRefused.Load())
}

// ==================== D2: the source-filtered window ====================

// A window filtered to one batch is driven from the batch: its plan reads the
// batch's tag rows by (schema, provider, source, batch) and looks each CID up
// in the index, instead of walking every index row of the standard.
func TestSourceWindowIsDrivenFromTheBatch(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()
	for b := 0; b < 4; b++ {
		records := make([][]byte, 0, 50)
		for i := 0; i < 50; i++ {
			records = append(records, buildEngineOMM(t, uint32(90000+b*100+i), "W", 1_700_000_000+int64(i)*60))
		}
		tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: fmt.Sprintf("batch-%d", b)}
		if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	tables, err := store.recordTablesForSchema("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	filter := IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "prov", SourceName: "gp", BatchID: "batch-2", Limit: 10}
	for _, projection := range []func(func(string) string) string{indexedRecordProjection, indexedRecordLengthProjection} {
		query, args := indexedRecordWindowSQL(filter, tables, projection)
		plan := explainForTest(t, store, query, args)
		joined := strings.Join(plan, "\n  ")
		if !strings.Contains(joined, "idx_sdn_record_source_tags_batch_cid (schema_name=? AND provider_id=? AND source_name=? AND batch_id=?)") {
			t.Fatalf("the window is not driven from the batch index:\n  %s", joined)
		}
		for _, line := range plan {
			if strings.Contains(line, "SEARCH idx ") && !strings.Contains(line, "cid=?") {
				t.Fatalf("the window walks the standard's index at %q:\n  %s", line, joined)
			}
		}
	}
	recs, err := store.QueryIndexedRecords(filter)
	if err != nil || len(recs) != 10 {
		t.Fatalf("batch window: %d records, %v", len(recs), err)
	}
	for _, r := range recs {
		if r.SourceTags.BatchID != "batch-2" {
			t.Fatalf("batch window returned a record of %q", r.SourceTags.BatchID)
		}
	}
}

// ==================== D8: import deadline ====================

// A dataset import returns at its caller's deadline even while a chunk is
// stalled inside the store (a slow COMMIT), and starts no chunk after it.
func TestDatasetImportHonoursItsDeadline(t *testing.T) {
	tmp := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	producer := openBootStore(t, filepath.Join(tmp, "producer"), validator)
	defer producer.Close()
	consumer := openBootStore(t, filepath.Join(tmp, "consumer"), validator)
	defer consumer.Close()
	tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "deadline"}
	records := make([][]byte, 0, 3*storeWriteChunkSize)
	for i := 0; i < cap(records); i++ {
		records = append(records, buildEngineOMM(t, uint32(95000+i), "D", 1_700_000_000+int64(i)))
	}
	if _, err := producer.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, tags); err != nil {
		t.Fatal(err)
	}
	export, err := producer.ExportDatasetWindow(filepath.Join(tmp, "export"), IndexedRecordQuery{
		SchemaName: "OMM.fbs", ProviderID: "prov", SourceName: "gp", BatchID: "deadline",
		Limit: len(records), AllowLargeResultSet: true, OrderByCID: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	importDatasetShardChunkHook = func() { <-release }
	defer func() { importDatasetShardChunkHook = nil }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = consumer.ImportDatasetShardFromFilesContext(ctx, export.ShardPath, export.IndexPath, "peer")
	took := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("import past its deadline: err=%v", err)
	}
	if took > 2*time.Second {
		t.Fatalf("import returned %s after a 200ms deadline", took)
	}
	close(release)
	// The abandoned chunk runs to its end (here it fails: the caller closed
	// the shard file it reads from, so it rolls back); no further chunk starts.
	time.Sleep(time.Second)
	importDatasetShardChunkHook = nil
	if n := indexCount(t, consumer, "OMM.fbs"); n > storeWriteChunkSize {
		t.Fatalf("after the deadline %d records landed, more than the one abandoned chunk (%d)", n, storeWriteChunkSize)
	}

	// A retry without a deadline converges.
	imported, _, err := consumer.ImportDatasetShardFromFilesContext(context.Background(), export.ShardPath, export.IndexPath, "peer")
	if err != nil {
		t.Fatal(err)
	}
	if n := indexCount(t, consumer, "OMM.fbs"); n != int64(len(records)) {
		t.Fatalf("after the retry the consumer holds %d records (imported %d), want %d", n, imported, len(records))
	}
	t.Logf("import returned %s after its 200ms deadline with a chunk stalled", took)
}
