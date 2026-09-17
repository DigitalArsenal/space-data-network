package storage

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// EVICTION MUST STILL PICK THE OLDEST ROWS. The per-source merge replaced a
// `ORDER BY _rowid ASC LIMIT n` over the UNION ALL view, which is what forced a
// sort of the whole relation. Same answer, or the hot window evicts the wrong
// records.
func TestEvictionCandidatesAreTheOldestAcrossSources(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	// Two sources so the k-way merge is exercised, not just a single partition.
	for i := 0; i < 10; i++ {
		source := "evict-a"
		if i%2 == 1 {
			source = "evict-b"
		}
		tags := SourceTags{ProviderID: "prov-evict", SourceName: source, BatchID: "evict-batch"}
		record := buildEngineOMM(t, uint32(7300+i), "EVICT-SAT", int64(1700000000+i))
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-evict", nil, tags); err != nil {
			t.Fatalf("store record %d: %v", i, err)
		}
	}

	const limit = 4
	store.mu.Lock()
	got, err := store.oldestEngineRowsPerSource("OMM", limit)
	// The union query this replaced, as the oracle.
	res, unionErr := store.engineDB.Query(
		`SELECT _source, _rowid FROM "OMM" ORDER BY _rowid ASC LIMIT ?1`, int64(limit))
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("oldestEngineRowsPerSource: %v", err)
	}
	if unionErr != nil {
		t.Fatalf("union oracle: %v", unionErr)
	}
	if len(got) != limit {
		t.Fatalf("per-source merge returned %d candidates, want %d", len(got), limit)
	}
	if len(res.Rows) != limit {
		t.Fatalf("union oracle returned %d rows, want %d — fixture cannot discriminate", len(res.Rows), limit)
	}
	for i, row := range res.Rows {
		wantSource, _ := row[0].(string)
		wantRowid, _ := row[1].(int64)
		if got[i].source != wantSource || got[i].rowid != wantRowid {
			t.Fatalf("candidate %d = %s@%d, union oracle said %s@%d — the merge does not reproduce the eviction order",
				i, got[i].source, got[i].rowid, wantSource, wantRowid)
		}
	}
}

// THE OUTCOME, STATED: A READER MAKES PROGRESS WHILE A WRITER STREAM RUNS.
//
// This is the defect in one assertion. Go's sync.RWMutex is write-preferring,
// so a sustained writer stream on s.mu starves readers indefinitely — on
// host-01 `/tiles/meta` (ONE query against an EMPTY relation) had a median of
// 1.2 s and a maximum of 90 s while materialization ran.
//
// The bound is deliberately generous: this runs on a shared laptop and an
// absolute latency here is not admissible evidence for a host. What it refuses
// is the FAILURE MODE — a reader that completes zero acquisitions, or one whose
// worst wait is a multiple of the whole writer stream's duration. The live
// A/B on host-01 is the evidence for the numbers.
func TestReadersMakeProgressUnderAContinuousWriter(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "prov-lock", SourceName: "lock-src", BatchID: "lock-batch"}
	for i := 0; i < 25; i++ {
		record := buildEngineOMM(t, uint32(7500+i), "LOCK-SAT", int64(1700000000+i))
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-lock", nil, tags); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
	}

	var (
		wg          sync.WaitGroup
		stop        atomic.Bool
		readerCount atomic.Int64
		worstWait   atomic.Int64
	)

	wg.Add(1)
	go func() { // the writer stream
		defer wg.Done()
		for i := 0; i < 40 && !stop.Load(); i++ {
			record := buildEngineOMM(t, uint32(7900+i), "LOCK-WRITER", int64(1700001000+i))
			if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-lock", nil, tags); err != nil {
				return
			}
		}
	}()

	started := time.Now()
	wg.Add(1)
	go func() { // the reader
		defer wg.Done()
		for time.Since(started) < 5*time.Second {
			queued := time.Now()
			_, err := store.QueryRawStream(`SELECT _data FROM "OMM" LIMIT 1`)
			waited := time.Since(queued)
			if int64(waited) > worstWait.Load() {
				worstWait.Store(int64(waited))
			}
			if err == nil {
				readerCount.Add(1)
			}
			if readerCount.Load() >= 30 {
				return
			}
		}
	}()

	wg.Wait()
	stop.Store(true)

	if readerCount.Load() == 0 {
		t.Fatal("the reader completed ZERO queries while a writer stream ran — that is the starvation this task exists for")
	}
	worst := time.Duration(worstWait.Load())
	if worst > 30*time.Second {
		t.Fatalf("worst single reader acquisition was %s — a reader is still being starved for the length of the writer stream", worst)
	}

	// And the account must be able to SEE it. The whole reason this ran
	// undiagnosed is that no instrument measured s.mu.
	acct := store.StoreLockStats()
	if acct.ReadAcquires == 0 {
		t.Fatal("StoreLockStats recorded no read acquisitions — the store lock is unaccounted again")
	}
	if acct.WriteAcquires == 0 {
		t.Fatal("StoreLockStats recorded no write acquisitions")
	}
	t.Logf("store lock account: %d reads (wait %s, held %s), %d writes (wait %s, held %s), %d slow; reader completed %d queries, worst wait %s",
		acct.ReadAcquires, acct.ReadWait.Round(time.Millisecond), acct.ReadHeld.Round(time.Millisecond),
		acct.WriteAcquires, acct.WriteWait.Round(time.Millisecond), acct.WriteHeld.Round(time.Millisecond),
		acct.Slow, readerCount.Load(), worst.Round(time.Millisecond))
}

var _ = fmt.Sprintf

// The batch path holds the ENGINE MODULE lock across a whole bounded batch —
// IngestManyWithSource takes it once for up to engineIngestBatchRecords ingests
// where the per-record ingest() took and released it each time. The control
// database is opened on that same engine, so that lock gates reads too: a
// reader can no longer slip between two records of a batch, only between two
// batches.
//
// That is allowed — "maintenance passes take the store lock per bounded batch,
// never across a scan" — but only while a reader still ANSWERS during one. The
// sibling test above drives the per-record path and would not catch a long hold
// here, because StoreWithSourceTags never enters the batched ingest at all.
func TestReadersMakeProgressUnderABatchWriter(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "prov-batch", SourceName: "batch-src", BatchID: "batch-lock"}
	for i := 0; i < 25; i++ {
		record := buildEngineOMM(t, uint32(8500+i), "BATCH-SEED", int64(1700000000+i))
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-batch", nil, tags); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
	}

	var (
		wg          sync.WaitGroup
		stop        atomic.Bool
		readerCount atomic.Int64
		worstWait   atomic.Int64
		written     atomic.Int64
	)

	wg.Add(1)
	go func() { // a writer streaming FULL windows, not single records
		defer wg.Done()
		next := uint32(9000)
		for round := 0; round < 12 && !stop.Load(); round++ {
			window := make([][]byte, 0, 256)
			for i := 0; i < 256; i++ {
				window = append(window, buildEngineOMM(t, next, "BATCH-WRITER", int64(1700002000+int64(next))))
				next++
			}
			n, err := store.StoreBatchWithSourceTags("OMM.fbs", window, "peer-batch", nil, tags)
			if err != nil {
				return
			}
			written.Add(int64(n))
		}
	}()

	started := time.Now()
	wg.Add(1)
	go func() { // the reader
		defer wg.Done()
		for time.Since(started) < 20*time.Second {
			queued := time.Now()
			_, err := store.QueryRawStream(`SELECT _data FROM "OMM" LIMIT 1`)
			waited := time.Since(queued)
			for {
				prev := worstWait.Load()
				if int64(waited) <= prev || worstWait.CompareAndSwap(prev, int64(waited)) {
					break
				}
			}
			if err == nil {
				readerCount.Add(1)
			}
			if readerCount.Load() >= 30 {
				return
			}
		}
	}()

	wg.Wait()
	stop.Store(true)

	worst := time.Duration(worstWait.Load())
	if readerCount.Load() == 0 {
		t.Fatalf("the reader completed ZERO queries while %d records were written in windows — the batched ingest is starving readers (worst wait %s)", written.Load(), worst)
	}
	// One bounded batch is the most a reader should ever queue behind. This is
	// deliberately loose: it is a starvation gate, not a latency budget, and a
	// tight number here would fail on a loaded box rather than on a regression.
	if worst > 30*time.Second {
		t.Fatalf("worst single reader acquisition was %s across %d batched records — a reader is queuing behind more than one bounded batch", worst, written.Load())
	}
	t.Logf("batched writer: %d records written; reader completed %d queries, worst wait %s",
		written.Load(), readerCount.Load(), worst.Round(time.Millisecond))
}
