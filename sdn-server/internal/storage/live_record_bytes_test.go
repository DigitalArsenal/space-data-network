package storage

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// scanLiveRecordsForTest sums every partition of every recognized standard
// directly: COUNT and SUM(record_length) per (producer, type) table.
func scanLiveRecordsForTest(t *testing.T, s *FlatSQLStore) (records, bytes int64) {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	recognized := s.recognizedStandards()
	partitions, err := s.listProducerStandardTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range partitions {
		if _, ok := recognized[p.Standard]; !ok {
			continue
		}
		var c, b int64
		if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(record_length), 0) FROM %s`, p.TableName)).Scan(&c, &b); err != nil {
			t.Fatalf("scan %s: %v", p.TableName, err)
		}
		records += c
		bytes += b
	}
	return records, bytes
}

// requireCounterMatchesScan checks the partition counters against the
// partitions themselves, store-wide and one by one.
func requireCounterMatchesScan(t *testing.T, s *FlatSQLStore, step string) {
	t.Helper()
	wantRecords, wantBytes := scanLiveRecordsForTest(t, s)
	live, err := s.LiveRecordBytes()
	if err != nil {
		t.Fatalf("%s: LiveRecordBytes: %v", step, err)
	}
	if live != wantBytes {
		t.Fatalf("%s: LiveRecordBytes = %d, the partitions hold %d", step, live, wantBytes)
	}
	s.mu.RLock()
	records, _, err := s.liveRecordTotalsLocked()
	counts, cerr := s.partitionCountsLocked()
	s.mu.RUnlock()
	if err != nil || cerr != nil {
		t.Fatalf("%s: counter totals: %v / %v", step, err, cerr)
	}
	if records != wantRecords {
		t.Fatalf("%s: counted records = %d, the partitions hold %d", step, records, wantRecords)
	}
	for _, c := range counts {
		var n, b int64
		if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(record_length), 0) FROM %s`, c.Table)).Scan(&n, &b); err != nil {
			t.Fatalf("%s: scan %s: %v", step, c.Table, err)
		}
		if !c.Counted || c.Records != n || c.Bytes != b {
			t.Fatalf("%s: %s counter = %d rows / %d bytes (counted %v), partition holds %d / %d", step, c.Table, c.Records, c.Bytes, c.Counted, n, b)
		}
	}
}

func liveTestPayload(i, size int) []byte {
	p := make([]byte, size)
	for b := range p {
		p[b] = byte((i*31 + b) % 251)
	}
	return p
}

func TestLiveRecordCounterFollowsEveryWritePath(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	store := openBootStore(t, basePath, v)

	requireCounterMatchesScan(t, store, "empty store")

	// Untagged writes: the case the per-lane source summary cannot see.
	var rfm []string
	for i := 0; i < 6; i++ {
		cid, err := store.Store("RFM.fbs", liveTestPayload(i, 100+i), "peer-a", nil)
		if err != nil {
			t.Fatalf("Store RFM %d: %v", i, err)
		}
		rfm = append(rfm, cid)
	}
	requireCounterMatchesScan(t, store, "untagged writes")

	tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "b1"}
	var omm []string
	for i := 0; i < 4; i++ {
		cid, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, uint32(40000+i), fmt.Sprintf("SAT %d", i), 1_700_000_000+int64(i)), "peer-a", nil, tags)
		if err != nil {
			t.Fatalf("StoreWithSourceTags OMM %d: %v", i, err)
		}
		omm = append(omm, cid)
	}
	requireCounterMatchesScan(t, store, "tagged writes")

	// The same CID under a second producer: a row in each partition.
	if _, err := store.Store("RFM.fbs", liveTestPayload(0, 100), "peer-b", nil); err != nil {
		t.Fatalf("mirror RFM: %v", err)
	}
	requireCounterMatchesScan(t, store, "repeat CID from a second producer")

	// Replace: a $CAT edition supersedes the producer's previous record per
	// object, in the batch path and in the single-record path.
	catTags := SourceTags{ProviderID: "prov", SourceName: "satcat", BatchID: "edition"}
	edition := func(suffix string) [][]byte {
		return [][]byte{
			buildCATForTest("ISS "+suffix, "1998-067A", 25544, "", ""),
			buildCATForTest("HST "+suffix, "1990-037B", 20580, "", ""),
			buildCATForTest("NAMELESS "+suffix, "", 0, "", ""),
		}
	}
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", edition("v1"), "peer-a", nil, catTags); err != nil {
		t.Fatalf("CAT v1: %v", err)
	}
	requireCounterMatchesScan(t, store, "batch insert")
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", edition("v2-longer-name"), "peer-a", nil, catTags); err != nil {
		t.Fatalf("CAT v2: %v", err)
	}
	requireCounterMatchesScan(t, store, "batch supersede")
	v1 := edition("v1")[0]
	if _, err := store.Store("CAT.fbs", v1, "peer-b", nil); err != nil {
		t.Fatalf("CAT v1 under peer-b: %v", err)
	}
	requireCounterMatchesScan(t, store, "superseded CID stored again by another producer")
	if _, err := store.Store("CAT.fbs", edition("v2-longer-name")[0], "peer-b", nil); err != nil {
		t.Fatalf("CAT v2 under peer-b: %v", err)
	}
	requireCounterMatchesScan(t, store, "single-record supersede of a CID another producer holds")

	// Delete one producer's copy while another still holds the CID, then
	// the last copy.
	if _, err := store.db.Exec(`DELETE FROM sds_p_peer_b__RFM WHERE cid = ?`, rfm[0]); err != nil {
		t.Fatalf("delete peer-b copy: %v", err)
	}
	requireCounterMatchesScan(t, store, "delete of a copy another producer holds")
	if err := store.Delete("RFM.fbs", rfm[0]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	requireCounterMatchesScan(t, store, "delete of the last copy")
	if err := store.Delete("OMM.fbs", omm[1]); err != nil {
		t.Fatalf("Delete OMM: %v", err)
	}
	requireCounterMatchesScan(t, store, "tagged delete")

	// Retirement of a whole source batch.
	if _, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, 49999, "NEXT", 1_700_000_100), "peer-a", nil,
		SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "b2"}); err != nil {
		t.Fatalf("OMM b2: %v", err)
	}
	if res, err := store.ReconcileSourceBatch("OMM.fbs", "prov", "gp", "b2", true); err != nil || res.Deleted == 0 {
		t.Fatalf("ReconcileSourceBatch: %+v %v", res, err)
	}
	requireCounterMatchesScan(t, store, "source batch reconcile")

	// Age-based GC.
	for i, cid := range rfm[1:4] {
		setRecordTimestampForTest(t, store, "RFM.fbs", cid, 1_000_000_000+int64(i))
	}
	if n, err := store.GarbageCollect(24 * time.Hour); err != nil || n == 0 {
		t.Fatalf("GarbageCollect: %d %v", n, err)
	}
	requireCounterMatchesScan(t, store, "garbage collect")

	// A peer's storage is the rows stored under its ID, a shared CID
	// counting for each holder.
	for _, peer := range []string{"peer-a", "peer-b"} {
		got, err := store.PeerStorageBytes(peer)
		if err != nil {
			t.Fatalf("PeerStorageBytes(%s): %v", peer, err)
		}
		var want int64
		tables, _ := store.listProducerStandardTables()
		for _, tb := range tables {
			if tb.ProducerID != sanitizeProducerID(peer) {
				continue
			}
			var b int64
			if err := store.db.QueryRow(fmt.Sprintf(`SELECT COALESCE(SUM(record_length), 0) FROM %s WHERE peer_id = ?`, tb.TableName), peer).Scan(&b); err != nil {
				t.Fatal(err)
			}
			want += b
		}
		if got != want || want == 0 {
			t.Fatalf("PeerStorageBytes(%s) = %d, its rows hold %d", peer, got, want)
		}
	}

	// A restart reads the persisted counter: nothing left to count at open.
	wantBytes, _ := store.LiveRecordBytes()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openBootStore(t, basePath, v)
	defer store.Close()
	var uncounted int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_partition_record_bytes WHERE scanned_rowid < ?`, partitionCountComplete).Scan(&uncounted); err != nil {
		t.Fatal(err)
	}
	if uncounted != 0 {
		t.Fatalf("%d partition(s) need counting after a clean restart; the counters must persist", uncounted)
	}
	if got, err := store.LiveRecordBytes(); err != nil || got != wantBytes {
		t.Fatalf("LiveRecordBytes after restart = %d (%v), want %d", got, err, wantBytes)
	}
	requireCounterMatchesScan(t, store, "after restart")
	if _, err := store.Store("RFM.fbs", liveTestPayload(99, 333), "peer-c", nil); err != nil {
		t.Fatal(err)
	}
	requireCounterMatchesScan(t, store, "write after restart")
}

// stripPartitionCountersForTest leaves the store as a binary without the
// counters wrote it: no counter table, no triggers.
func stripPartitionCountersForTest(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := partitionTriggersInSchema(s.db)
	if err != nil {
		t.Fatal(err)
	}
	for name := range names {
		if err := execSingle(s.db, `DROP TRIGGER `+name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DROP TABLE sdn_partition_record_bytes`); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRecordCounterRebuildsAStoreWrittenBeforeIt(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	store := openBootStore(t, basePath, v)
	tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "b1"}
	var rfm []string
	for i := 0; i < 40; i++ {
		cid, err := store.Store("RFM.fbs", liveTestPayload(i, 64+i), "peer-a", nil)
		if err != nil {
			t.Fatal(err)
		}
		rfm = append(rfm, cid)
		// Every third record is also held by a second producer.
		if i%3 == 0 {
			if _, err := store.Store("RFM.fbs", liveTestPayload(i, 64+i), "peer-b", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 12; i++ {
		if _, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, uint32(41000+i), "SAT", 1_700_000_000+int64(i)), "peer-a", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	wantRecords, wantBytes := scanLiveRecordsForTest(t, store)
	stripPartitionCountersForTest(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	partitionRebuildStartPaused = true
	t.Cleanup(func() { partitionRebuildStartPaused = false })
	store = openBootStore(t, basePath, v)
	defer store.Close()

	// Uncounted: no total, and no quota decision made on a guess.
	if _, err := store.LiveRecordBytes(); !errors.Is(err, ErrLiveRecordBytesReconciling) {
		t.Fatalf("LiveRecordBytes before counting = %v, want ErrLiveRecordBytesReconciling", err)
	}
	if store.LiveRecordBytesReconciled() {
		t.Fatal("LiveRecordBytesReconciled before counting")
	}
	if _, err := store.GarbageCollectToQuota(1); !errors.Is(err, ErrLiveRecordBytesReconciling) {
		t.Fatalf("GarbageCollectToQuota before counting = %v, want ErrLiveRecordBytesReconciling", err)
	}
	// Readers that have a fallback still answer, and answer right.
	if n, err := store.Count("RFM.fbs"); err != nil || n != 40 {
		t.Fatalf("Count while counting = %d (%v), want 40", n, err)
	}
	if got, _ := scanLiveRecordsForTest(t, store); got != wantRecords {
		t.Fatalf("records changed across the restart: %d, want %d", got, wantRecords)
	}
	if summary, err := store.DataSummary(); err != nil || summary.TotalRecords == 0 {
		t.Fatalf("DataSummary while counting: %+v %v", summary, err)
	}
	_ = wantBytes

	// Count a few rows at a time while every kind of write lands between
	// chunks, in counted and uncounted regions alike.
	writes := []func(i int){
		func(i int) { // new record
			if _, err := store.Store("RFM.fbs", liveTestPayload(1000+i, 50+i), "peer-a", nil); err != nil {
				t.Fatal(err)
			}
		},
		func(i int) { // existing CID mirrored to another producer
			if _, err := store.Store("RFM.fbs", liveTestPayload(i%40, 64+i%40), "peer-c", nil); err != nil {
				t.Fatal(err)
			}
		},
		func(i int) { // delete from one producer only
			if _, err := store.db.Exec(`DELETE FROM sds_p_peer_b__RFM WHERE rowid = (SELECT MIN(rowid) FROM sds_p_peer_b__RFM)`); err != nil {
				t.Fatal(err)
			}
		},
		func(i int) { // delete a record everywhere
			if len(rfm) == 0 {
				return
			}
			cid := rfm[len(rfm)-1]
			rfm = rfm[:len(rfm)-1]
			if err := store.Delete("RFM.fbs", cid); err != nil {
				t.Fatal(err)
			}
		},
		func(i int) { // tagged write into another standard
			if _, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, uint32(42000+i), "LATE", 1_700_001_000+int64(i)), "peer-a", nil, tags); err != nil {
				t.Fatal(err)
			}
		},
	}
	steps := 0
	for {
		done, _, err := store.partitionRebuildStep(3)
		if err != nil {
			t.Fatalf("rebuild step %d: %v", steps, err)
		}
		if done {
			break
		}
		writes[steps%len(writes)](steps)
		steps++
		if steps > 10_000 {
			t.Fatal("rebuild did not finish")
		}
	}
	if steps < 10 {
		t.Fatalf("rebuild finished in %d steps; the test needs chunks interleaved with writes", steps)
	}
	requireCounterMatchesScan(t, store, "after an interleaved rebuild")
}

func TestLiveRecordCounterWorkerCountsInTheBackground(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	store := openBootStore(t, basePath, v)
	for i := 0; i < 30; i++ {
		if _, err := store.Store("RFM.fbs", liveTestPayload(i, 80), fmt.Sprintf("peer-%d", i%3), nil); err != nil {
			t.Fatal(err)
		}
	}
	_, wantBytes := scanLiveRecordsForTest(t, store)
	stripPartitionCountersForTest(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openBootStore(t, basePath, v)
	defer store.Close()
	deadline := time.Now().Add(30 * time.Second)
	for !store.LiveRecordBytesReconciled() {
		if time.Now().After(deadline) {
			t.Fatal("the background worker did not finish counting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, err := store.LiveRecordBytes(); err != nil || got != wantBytes {
		t.Fatalf("LiveRecordBytes = %d (%v), want %d", got, err, wantBytes)
	}
	requireCounterMatchesScan(t, store, "after background counting")
}

// A partition that is dropped takes its rows and its triggers with it and
// fires none of them: its counter row is simply no longer read.
func TestLiveRecordCounterForgetsADroppedPartition(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	store := openBootStore(t, basePath, v)
	for i := 0; i < 5; i++ {
		data := liveTestPayload(i, 90)
		if _, err := store.Store("RFM.fbs", data, "peer-a", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Store("RFM.fbs", data, "peer-b", nil); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	_, err := store.db.Exec(`DROP TABLE sds_p_peer_a__RFM`)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	requireCounterMatchesScan(t, store, "after dropping a partition")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openBootStore(t, basePath, v)
	defer store.Close()
	var stale int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_partition_record_bytes WHERE table_name = 'sds_p_peer_a__RFM'`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("counter row of a dropped partition survived a reopen (%d, %v)", stale, err)
	}
	if _, err := store.Store("RFM.fbs", liveTestPayload(77, 91), "peer-a", nil); err != nil {
		t.Fatalf("write into a recreated partition: %v", err)
	}
	requireCounterMatchesScan(t, store, "write into a recreated partition")
}

func TestSchemaDateRangesFromTheCounterMatchTheIndex(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()
	tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "b1"}
	for i := 0; i < 5; i++ {
		if _, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, uint32(43000+i), "SAT", 1_700_000_000+int64(i)*86400), "peer-a", nil, tags); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Store("RFM.fbs", liveTestPayload(i, 70), "peer-a", nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.SchemaDateRanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("SchemaDateRanges = %+v, want OMM and RFM", got)
	}
	for _, r := range got {
		var count int64
		var minEpoch, maxEpoch *int64
		var lo, hi int64
		row := store.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(epoch_unix), 0), COALESCE(MAX(epoch_unix), 0) FROM sdn_record_index WHERE schema_name = ?`, r.Schema)
		if err := row.Scan(&count, &lo, &hi); err != nil {
			t.Fatal(err)
		}
		if lo > 0 {
			minEpoch, maxEpoch = &lo, &hi
		}
		src, _ := store.recordReadSource(r.Schema) // one producer: the partition itself
		var bytes int64
		if err := store.db.QueryRow(fmt.Sprintf(`SELECT COALESCE(SUM(record_length), 0) FROM %s`, src)).Scan(&bytes); err != nil {
			t.Fatal(err)
		}
		if r.RecordCount != count || r.TotalBytes != bytes {
			t.Fatalf("%s: %d records / %d bytes, index and tables say %d / %d", r.Schema, r.RecordCount, r.TotalBytes, count, bytes)
		}
		if (minEpoch == nil) != (r.OldestEpoch == nil) || (minEpoch != nil && (r.OldestEpoch.Unix() != *minEpoch || r.NewestEpoch.Unix() != *maxEpoch)) {
			t.Fatalf("%s: epochs %v..%v, index says %v..%v", r.Schema, r.OldestEpoch, r.NewestEpoch, lo, hi)
		}
	}
}

// The engine returns every integer to Go as a float64, so the completion
// sentinel must be one a float64 holds exactly — math.MaxInt64 read back as
// MinInt64 on x86-64 and no partition there ever counted as done — and a
// cursor an earlier build wrote as math.MaxInt64 must still read as complete.
func TestPartitionCountCompleteSurvivesTheEngineReadPath(t *testing.T) {
	if partitionCountComplete > 1<<53 {
		t.Fatalf("partitionCountComplete = %d is not exact in a float64", partitionCountComplete)
	}
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()
	var got int64
	if err := store.db.QueryRow(`SELECT ?`, partitionCountComplete).Scan(&got); err != nil || got != partitionCountComplete {
		t.Fatalf("the sentinel read back as %d (%v), want %d", got, err, partitionCountComplete)
	}

	if _, err := store.Store("RFM.fbs", liveTestPayload(5, 77), "peer-a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sdn_partition_record_bytes SET scanned_rowid = ?`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	err := store.installPartitionCounters()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if live, err := store.LiveRecordBytes(); err != nil || live != 77 {
		t.Fatalf("LiveRecordBytes after a MaxInt64 cursor = %d (%v), want 77", live, err)
	}
}
