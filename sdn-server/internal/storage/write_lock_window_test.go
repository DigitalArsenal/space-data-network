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
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const readBudget = 2 * time.Second

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

// assertReadsResponsive samples reads until done flips, failing if any single
// read exceeds readBudget. Returns the number of reads and the worst latency.
func assertReadsResponsive(t *testing.T, store *FlatSQLStore, schemaName string, done *atomic.Bool) (int, time.Duration) {
	t.Helper()
	reads := 0
	var worst time.Duration
	for !done.Load() {
		start := time.Now()
		if _, err := store.CountRawRecords(RawRecordQuery{SchemaName: schemaName}); err != nil {
			t.Fatalf("read %d during concurrent write failed: %v", reads, err)
		}
		elapsed := time.Since(start)
		if elapsed > worst {
			worst = elapsed
		}
		if elapsed > readBudget {
			t.Fatalf("read %d took %s during concurrent write (budget %s): writes are starving readers again", reads, elapsed, readBudget)
		}
		reads++
		time.Sleep(10 * time.Millisecond)
	}
	return reads, worst
}

func TestReadsStayResponsiveDuringBatchStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-lock-window-batch-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	store := newLockWindowStore(t, filepath.Join(tmpDir, "db"))

	const n = storeWriteChunkSize*4 + 17 // several chunk windows + a ragged tail
	records := buildLockWindowCATRecords(t, n)
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-satcat",
		SourceURL:    "https://celestrak.example/satcat",
		BatchID:      "lock-window-batch",
		ContentKeyID: "public",
	}

	var done atomic.Bool
	type result struct {
		inserted int
		err      error
	}
	writeResult := make(chan result, 1)
	go func() {
		inserted, err := store.StoreBatchWithSourceTags("CAT.fbs", records, "source:celestrak", nil, tags)
		done.Store(true)
		writeResult <- result{inserted, err}
	}()

	reads, worst := assertReadsResponsive(t, store, "CAT.fbs", &done)

	res := <-writeResult
	if res.err != nil {
		t.Fatalf("StoreBatchWithSourceTags failed: %v", res.err)
	}
	if res.inserted != n {
		t.Fatalf("inserted = %d, want %d (chunking must not drop or double records)", res.inserted, n)
	}
	count, err := store.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("final count: %v", err)
	}
	if count != int64(n) {
		t.Fatalf("final record count = %d, want %d", count, n)
	}
	if reads == 0 {
		t.Fatalf("reader never sampled during the batch write; test proves nothing")
	}
	t.Logf("batch write of %d records: %d concurrent reads, worst read %s (budget %s)", n, reads, worst, readBudget)
}

func TestReadsStayResponsiveDuringShardImport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-lock-window-import-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	providerStore := newLockWindowStore(t, filepath.Join(tmpDir, "provider-db"))
	subscriberStore := newLockWindowStore(t, filepath.Join(tmpDir, "subscriber-db"))

	const n = storeWriteChunkSize*3 + 5
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-satcat",
		SourceURL:    "https://celestrak.example/satcat",
		BatchID:      "lock-window-shard",
		ContentKeyID: "public",
	}
	if inserted, err := providerStore.StoreBatchWithSourceTags("CAT.fbs", buildLockWindowCATRecords(t, n), "source:celestrak", nil, tags); err != nil || inserted != n {
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

	var done atomic.Bool
	type result struct {
		imported int
		err      error
	}
	importResult := make(chan result, 1)
	go func() {
		imported, _, err := subscriberStore.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, "celestrak.eth")
		done.Store(true)
		importResult <- result{imported, err}
	}()

	reads, worst := assertReadsResponsive(t, subscriberStore, "CAT.fbs", &done)

	res := <-importResult
	if res.err != nil {
		t.Fatalf("ImportDatasetShardFromFiles failed: %v", res.err)
	}
	if res.imported != n {
		t.Fatalf("imported = %d, want %d (chunking must not drop or double records)", res.imported, n)
	}
	count, err := subscriberStore.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("final count: %v", err)
	}
	if count != int64(n) {
		t.Fatalf("final record count = %d, want %d", count, n)
	}
	if reads == 0 {
		t.Fatalf("reader never sampled during the shard import; test proves nothing")
	}
	t.Logf("shard import of %d records: %d concurrent reads, worst read %s (budget %s)", n, reads, worst, readBudget)
}
