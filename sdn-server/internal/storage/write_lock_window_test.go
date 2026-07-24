package storage

// Chunked store-lock windows (storeWriteChunkSize): batch writes and dataset
// shard imports must NOT starve readers. The 2026-07-06 production blackout:
// a peer's full-catalog dataset announcement drove ImportDatasetShardFromFiles
// through ONE store write-lock hold + ONE transaction spanning 31.8K records —
// every /api/v1/data query waited >11 minutes on s.mu.RLock (SIGQUIT goroutine
// dump evidence). These tests run big writes concurrently with a reader and
// fail if any single read stalls behind a write for more than readBudget.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const readBudget = 2 * time.Second

func TestStoreWriteChunkSizeBoundsColdRunnerLockWork(t *testing.T) {
	// Run 29309719300 averaged about 1.7s per 128-record write window on a
	// cold runner. Keep each window at no more than half that work so the
	// existing two-second lock-wait regression budget has useful margin.
	const maxColdRunnerChunkSize = 64
	if storeWriteChunkSize > maxColdRunnerChunkSize {
		t.Fatalf("storeWriteChunkSize = %d, want <= %d to bound each write-lock window", storeWriteChunkSize, maxColdRunnerChunkSize)
	}
}

func TestLockWindowReadViolationUsesCompleteCountLatency(t *testing.T) {
	lockWait := 12 * time.Millisecond
	total := readBudget + time.Nanosecond
	err := lockWindowReadViolation(4, total, lockWait)
	if err == nil {
		t.Fatal("complete count latency above the budget must be rejected")
	}
	for _, want := range []string{total.String(), "lock wait " + lockWait.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("violation %q does not contain %q", err, want)
		}
	}
	if err := lockWindowReadViolation(4, readBudget, lockWait); err != nil {
		t.Fatalf("latency exactly at the budget must remain valid: %v", err)
	}
}

func newLockWindowStore(t *testing.T, dir string) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

type lockWindowWriteResult struct {
	written int
	err     error
}

type lockWindowWriteTask struct {
	result     chan lockWindowWriteResult
	done       chan struct{}
	waitOnce   sync.Once
	waitResult lockWindowWriteResult
}

// startLockWindowWriteTask registers its join after the store's cleanup. Go's
// LIFO cleanup order therefore guarantees the writer is finished before the
// store can close, even when the test returns early after a failed probe.
func startLockWindowWriteTask(t *testing.T, write func() (int, error)) *lockWindowWriteTask {
	t.Helper()
	task := &lockWindowWriteTask{
		result: make(chan lockWindowWriteResult, 1),
		done:   make(chan struct{}),
	}
	ready := make(chan struct{})
	start := make(chan struct{})
	go func() {
		close(ready)
		<-start
		written, err := write()
		task.result <- lockWindowWriteResult{written: written, err: err}
		close(task.done)
	}()
	<-ready
	t.Cleanup(func() { task.Wait() })
	close(start)
	return task
}

func (task *lockWindowWriteTask) Wait() lockWindowWriteResult {
	task.waitOnce.Do(func() {
		task.waitResult = <-task.result
		<-task.done
	})
	return task.waitResult
}

func (task *lockWindowWriteTask) Done() <-chan struct{} {
	return task.done
}

func TestLockWindowWriteTaskCleanupJoinsBeforeStoreCleanup(t *testing.T) {
	var writerFinished bool
	var storeCleanupSawFinished bool
	t.Run("implicit cleanup join", func(t *testing.T) {
		// This stands in for newLockWindowStore's earlier cleanup.
		t.Cleanup(func() { storeCleanupSawFinished = writerFinished })

		release := make(chan struct{})
		startLockWindowWriteTask(t, func() (int, error) {
			<-release
			writerFinished = true
			return 1, nil
		})
		// Release runs first, then the task join, then the simulated store close.
		t.Cleanup(func() { close(release) })
	})

	if !storeCleanupSawFinished {
		t.Fatal("store cleanup ran before the writer was joined")
	}
}

func buildLockWindowCATRecords(t *testing.T, n int) [][]byte {
	t.Helper()
	records := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, sds.NewCATBuilder().
			WithNoradCatID(uint32(70000+i)).
			WithObjectName(fmt.Sprintf("LOCK-WINDOW-%d", i)).
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build())
	}
	return records
}

type lockWindowReadSample struct {
	reads         int
	worstLockWait time.Duration
	worstTotal    time.Duration
	err           error
}

// countRawRecordsWithLockWait runs the same complete count path as
// CountRawRecords while reporting only the time spent acquiring the store
// read lock. Keeping lock wait separate prevents cold SQL/WASM startup from
// hiding or falsely reporting write-lock starvation in the regression test.
func (s *FlatSQLStore) countRawRecordsWithLockWait(filter RawRecordQuery) (int64, time.Duration, error) {
	start := time.Now()
	s.mu.RLock()
	lockWait := time.Since(start)
	defer s.mu.RUnlock()
	count, err := s.countRawRecordsLocked(filter)
	return count, lockWait, err
}

func lockWindowReadViolation(read int, total, lockWait time.Duration) error {
	if total <= readBudget {
		return nil
	}
	return fmt.Errorf("read %d took %s for the complete count during concurrent write (budget %s, lock wait %s): writes are starving readers again", read, total, readBudget, lockWait)
}

// sampleReadsResponsive runs the complete raw-record count query but measures
// mutex acquisition separately from cold SQL/WASM work. It returns violations
// instead of failing so callers can always join the writer before Fatal/cleanup.
func sampleReadsResponsive(store *FlatSQLStore, schemaName string, done <-chan struct{}) lockWindowReadSample {
	var sample lockWindowReadSample
	for {
		select {
		case <-done:
			return sample
		default:
		}

		start := time.Now()
		_, lockWait, err := store.countRawRecordsWithLockWait(RawRecordQuery{SchemaName: schemaName})
		total := time.Since(start)
		if err != nil {
			sample.err = fmt.Errorf("read %d during concurrent write failed: %w", sample.reads, err)
			return sample
		}
		if lockWait > sample.worstLockWait {
			sample.worstLockWait = lockWait
		}
		if total > sample.worstTotal {
			sample.worstTotal = total
		}
		if err := lockWindowReadViolation(sample.reads, total, lockWait); err != nil {
			sample.err = err
			return sample
		}
		sample.reads++

		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-done:
			timer.Stop()
			return sample
		case <-timer.C:
		}
	}
}

func TestReadsStayResponsiveDuringBatchStore(t *testing.T) {
	tmpDir := t.TempDir()
	store := newLockWindowStore(t, filepath.Join(tmpDir, "db"))

	const n = 529 // eight 64-record windows + a ragged tail
	records := buildLockWindowCATRecords(t, n)
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-satcat",
		SourceURL:    "https://fixture.test/satcat",
		BatchID:      "lock-window-batch",
		ContentKeyID: "public",
	}
	if _, err := store.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"}); err != nil {
		t.Fatalf("warm CountRawRecords before concurrent batch: %v", err)
	}

	writeTask := startLockWindowWriteTask(t, func() (int, error) {
		return store.StoreBatchWithSourceTags("CAT.fbs", records, "source:catalogfixture", nil, tags)
	})
	readSample := sampleReadsResponsive(store, "CAT.fbs", writeTask.Done())
	res := writeTask.Wait()
	if res.err != nil {
		t.Fatalf("StoreBatchWithSourceTags failed: %v", res.err)
	}
	if res.written != n {
		t.Fatalf("inserted = %d, want %d (chunking must not drop or double records)", res.written, n)
	}
	if readSample.err != nil {
		t.Fatal(readSample.err)
	}
	count, err := store.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("final count: %v", err)
	}
	if count != int64(n) {
		t.Fatalf("final record count = %d, want %d", count, n)
	}
	if readSample.reads == 0 {
		t.Fatalf("reader never sampled during the batch write; test proves nothing")
	}
	t.Logf("batch write of %d records: %d concurrent reads, worst lock wait %s (budget %s), worst complete count %s", n, readSample.reads, readSample.worstLockWait, readBudget, readSample.worstTotal)
}

func TestReadsStayResponsiveDuringShardImport(t *testing.T) {
	tmpDir := t.TempDir()

	providerStore := newLockWindowStore(t, filepath.Join(tmpDir, "provider-db"))
	subscriberStore := newLockWindowStore(t, filepath.Join(tmpDir, "subscriber-db"))

	const n = 389 // six 64-record windows + a ragged tail
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-satcat",
		SourceURL:    "https://fixture.test/satcat",
		BatchID:      "lock-window-shard",
		ContentKeyID: "public",
	}
	if inserted, err := providerStore.StoreBatchWithSourceTags("CAT.fbs", buildLockWindowCATRecords(t, n), "source:catalogfixture", nil, tags); err != nil || inserted != n {
		t.Fatalf("seed provider store: inserted=%d err=%v", inserted, err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               n + 10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	if _, err := subscriberStore.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"}); err != nil {
		t.Fatalf("warm CountRawRecords before concurrent shard import: %v", err)
	}

	writeTask := startLockWindowWriteTask(t, func() (int, error) {
		imported, _, err := subscriberStore.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, "catalogfixture.eth")
		return imported, err
	})
	readSample := sampleReadsResponsive(subscriberStore, "CAT.fbs", writeTask.Done())
	res := writeTask.Wait()
	if res.err != nil {
		t.Fatalf("ImportDatasetShardFromFiles failed: %v", res.err)
	}
	if res.written != n {
		t.Fatalf("imported = %d, want %d (chunking must not drop or double records)", res.written, n)
	}
	if readSample.err != nil {
		t.Fatal(readSample.err)
	}
	count, err := subscriberStore.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("final count: %v", err)
	}
	if count != int64(n) {
		t.Fatalf("final record count = %d, want %d", count, n)
	}
	if readSample.reads == 0 {
		t.Fatalf("reader never sampled during the shard import; test proves nothing")
	}
	t.Logf("shard import of %d records: %d concurrent reads, worst lock wait %s (budget %s), worst complete count %s", n, readSample.reads, readSample.worstLockWait, readBudget, readSample.worstTotal)
}
