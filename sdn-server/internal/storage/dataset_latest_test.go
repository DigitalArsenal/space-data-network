package storage

// Gateway loop G.4: batch-scoped latest-dataset materialization
// (MaterializedDatasetBatch) and pinned supersede eviction
// (SupersedeSourceBatches) over a REAL provider->subscriber publication
// cycle: records stored with source tags, exported as deterministic shards,
// imported through the chunked shard-import path, publication rows +
// cached shard files recorded exactly like the feed-head materializer does.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const latestTestPeerID = "16Uiu2HAmTestCelestrakPeer"

type latestTestBatch struct {
	tags   SourceTags
	export *DatasetExport
	pub    DatasetShardPublication
}

// publishAndReplicateBatch stores records on the provider under one batch,
// exports the batch window, imports it into the subscriber via the chunked
// file-import path, and records the publication row + cached files the way
// materializeDatasetFeedHeadAnnouncement does.
func publishAndReplicateBatch(t *testing.T, tmpDir string, provider, subscriber *FlatSQLStore, batchID string, noradStart, count int, publishedAt time.Time) latestTestBatch {
	t.Helper()
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		SourceURL:    "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:      batchID,
		ContentKeyID: "public",
	}
	records := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		records = append(records, sds.NewCATBuilder().
			WithNoradCatID(uint32(noradStart+i)).
			WithObjectName("LATEST-BATCH").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build())
	}
	if _, err := provider.StoreBatchWithSourceTags("CAT.fbs", records, "celestrak.eth", nil, tags); err != nil {
		t.Fatalf("store batch (%s): %v", batchID, err)
	}
	windowLimit := count + 10
	export, err := provider.ExportDatasetWindow(filepath.Join(tmpDir, "export-"+batchID), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               windowLimit,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow (%s): %v", batchID, err)
	}

	imported, index, err := subscriber.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, latestTestPeerID)
	if err != nil {
		t.Fatalf("ImportDatasetShardFromFiles (%s): %v", batchID, err)
	}
	if index == nil {
		t.Fatalf("import returned no index (%s)", batchID)
	}
	t.Logf("batch %s: imported %d new records", batchID, imported)

	pub := DatasetShardPublication{
		SchemaName:   "CAT.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        windowLimit,
		RecordCount:  export.RecordCount,
		ByteCount:    export.ShardBytes,
		ShardCID:     export.ShardCID,
		IndexCID:     export.IndexCID,
		ShardSHA256:  export.ShardSHA256,
		IndexSHA256:  export.IndexSHA256,
		QuerySHA256:  export.QuerySHA256,
		ResultSHA256: export.ResultSHA256,
		PublishedAt:  publishedAt,
	}
	if err := subscriber.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("UpsertDatasetShardPublication (%s): %v", batchID, err)
	}
	// Cache the shard/index files at the store's deterministic publication
	// paths (what cacheDatasetFeedHeadPublicationFiles does).
	for _, asset := range []struct{ src, dest string }{
		{export.ShardPath, shardCachePath(t, subscriber, pub)},
		{export.IndexPath, indexCachePath(t, subscriber, pub)},
	} {
		if err := os.MkdirAll(filepath.Dir(asset.dest), 0o700); err != nil {
			t.Fatalf("mkdir cached publication dir: %v", err)
		}
		data, err := os.ReadFile(asset.src)
		if err != nil {
			t.Fatalf("read export asset: %v", err)
		}
		if err := os.WriteFile(asset.dest, data, 0o600); err != nil {
			t.Fatalf("write cached publication asset: %v", err)
		}
	}
	return latestTestBatch{tags: tags, export: export, pub: pub}
}

func shardCachePath(t *testing.T, store *FlatSQLStore, pub DatasetShardPublication) string {
	t.Helper()
	path, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatalf("shard publication path: %v", err)
	}
	return path
}

func indexCachePath(t *testing.T, store *FlatSQLStore, pub DatasetShardPublication) string {
	t.Helper()
	path, err := store.DatasetPublicationIndexPath(pub)
	if err != nil {
		t.Fatalf("index publication path: %v", err)
	}
	return path
}

func newLatestTestStores(t *testing.T) (string, *FlatSQLStore, *FlatSQLStore) {
	t.Helper()
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	provider, err := NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })
	subscriber, err := NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore subscriber: %v", err)
	}
	t.Cleanup(func() { subscriber.Close() })
	return tmpDir, provider, subscriber
}

func countBatchTaggedRecords(t *testing.T, store *FlatSQLStore, providerID, sourceName, batchID string) int {
	t.Helper()
	records, err := store.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: providerID,
		SourceName: sourceName,
		BatchID:    batchID,
		Limit:      1000,
	})
	if err != nil {
		t.Fatalf("QueryIndexedRecords (%s): %v", batchID, err)
	}
	return len(records)
}

func TestMaterializedDatasetBatchServesPublishedShardBytes(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	batch := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 4, time.Now().UTC())

	content, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a", DatasetBatchOptions{IncludeBytes: true})
	if err != nil {
		t.Fatalf("MaterializedDatasetBatch: %v", err)
	}
	if !ok || content == nil {
		t.Fatalf("batch-a not servable, want servable")
	}
	if content.ProviderID != batch.tags.ProviderID || content.SourceName != batch.tags.SourceName {
		t.Fatalf("provider/source = %s/%s", content.ProviderID, content.SourceName)
	}
	if content.RecordCount != 4 || len(content.Parts) != 1 {
		t.Fatalf("recordCount=%d parts=%d, want 4/1", content.RecordCount, len(content.Parts))
	}
	shardBytes, err := os.ReadFile(batch.export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	if string(content.Bytes) != string(shardBytes) {
		t.Fatalf("served bytes differ from the published shard (%d vs %d bytes)", len(content.Bytes), len(shardBytes))
	}
	if content.FNV1a64 != flatsqlrt.FNV1a64WordFolded(shardBytes) {
		t.Fatalf("FNV1a64 mismatch")
	}

	// Servability probe (no bytes) agrees and is cheap.
	probe, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a", DatasetBatchOptions{})
	if err != nil || !ok || probe.Bytes != nil {
		t.Fatalf("probe: ok=%v err=%v bytes=%d", ok, err, len(probe.Bytes))
	}

	// Unknown batch is not servable.
	if _, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "no-such-batch", DatasetBatchOptions{}); err != nil || ok {
		t.Fatalf("unknown batch: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestMaterializedDatasetBatchRefusesTamperAndTruncation(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	batch := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 4, time.Now().UTC())

	// A full final window is indistinguishable from a mid-import series:
	// refuse to serve rather than risk a truncated dataset.
	fullWindow := batch.pub
	fullWindow.Limit = fullWindow.RecordCount
	if err := subscriber.UpsertDatasetShardPublication(fullWindow); err != nil {
		t.Fatalf("upsert full-window publication: %v", err)
	}
	if _, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a", DatasetBatchOptions{}); err != nil || ok {
		t.Fatalf("full final window served (ok=%v err=%v), want refused", ok, err)
	}
	if err := subscriber.UpsertDatasetShardPublication(batch.pub); err != nil {
		t.Fatalf("restore publication row: %v", err)
	}
	// Restore original window shape (Upsert keys on window_offset+limit, so
	// drop the full-window row).
	if _, err := subscriber.DeleteDatasetShardPublicationsAtOrAfterOffset(DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   batch.tags.ProviderID,
		SourceName:   batch.tags.SourceName,
		BatchID:      "batch-a",
		QueryProfile: DatasetPublicationQueryProfile,
		Limit:        fullWindow.Limit,
	}, 0); err != nil {
		t.Fatalf("drop full-window row: %v", err)
	}

	// Tampered cached shard bytes: SHA mismatch refuses to serve.
	shardPath := shardCachePath(t, subscriber, batch.pub)
	original, err := os.ReadFile(shardPath)
	if err != nil {
		t.Fatalf("read cached shard: %v", err)
	}
	tampered := append([]byte(nil), original...)
	tampered[len(tampered)-1] ^= 0xff
	if err := os.WriteFile(shardPath, tampered, 0o600); err != nil {
		t.Fatalf("write tampered shard: %v", err)
	}
	if _, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a", DatasetBatchOptions{IncludeBytes: true}); err != nil || ok {
		t.Fatalf("tampered shard served (ok=%v err=%v), want refused", ok, err)
	}

	// Missing cached shard: not servable.
	if err := os.Remove(shardPath); err != nil {
		t.Fatalf("remove cached shard: %v", err)
	}
	if _, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a", DatasetBatchOptions{}); err != nil || ok {
		t.Fatalf("missing shard served (ok=%v err=%v), want refused", ok, err)
	}
}

func TestSupersedeSourceBatchesEvictsPreviousPin(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)

	// Publication batches are CONTENT DELTAS: a repeat-CID store keeps its
	// original batch tag (storeOne/storeBatch dedupe), so batch-b's export
	// carries only the records first seen in batch-b. Batch A: NORAD
	// 70000..70005 (6 records). Batch B: 70006..70008 (3 new records).
	batchA := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 6, time.Now().UTC().Add(-time.Hour))
	batchB := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-b", 70006, 3, time.Now().UTC())

	if got := countBatchTaggedRecords(t, subscriber, batchA.tags.ProviderID, batchA.tags.SourceName, "batch-a"); got != 6 {
		t.Fatalf("pre-supersede batch-a records = %d, want 6", got)
	}
	if got := countBatchTaggedRecords(t, subscriber, batchB.tags.ProviderID, batchB.tags.SourceName, "batch-b"); got != 3 {
		t.Fatalf("pre-supersede batch-b records = %d, want 3", got)
	}
	preTotal, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if preTotal != 9 {
		t.Fatalf("pre-supersede total = %d, want 9", preTotal)
	}

	result, err := subscriber.SupersedeSourceBatches("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, "batch-b")
	if err != nil {
		t.Fatalf("SupersedeSourceBatches: %v", err)
	}
	// All 6 batch-a records lose their only tag and are evicted everywhere.
	if result.TagsDeleted != 6 {
		t.Fatalf("TagsDeleted = %d, want 6", result.TagsDeleted)
	}
	if result.RecordsDeleted != 6 {
		t.Fatalf("RecordsDeleted = %d, want 6", result.RecordsDeleted)
	}
	if result.FilesDeleted != 2 { // batch-a shard + index cache files
		t.Fatalf("FilesDeleted = %d, want 2", result.FilesDeleted)
	}

	postTotal, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords post: %v", err)
	}
	if postTotal != 3 {
		t.Fatalf("post-supersede total = %d, want 3 (no accumulation)", postTotal)
	}
	if got := countBatchTaggedRecords(t, subscriber, batchA.tags.ProviderID, batchA.tags.SourceName, "batch-a"); got != 0 {
		t.Fatalf("post-supersede batch-a records = %d, want 0", got)
	}
	if got := countBatchTaggedRecords(t, subscriber, batchB.tags.ProviderID, batchB.tags.SourceName, "batch-b"); got != 3 {
		t.Fatalf("post-supersede batch-b records = %d, want 3", got)
	}

	// The kept batch still serves byte-exactly.
	content, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-b", DatasetBatchOptions{IncludeBytes: true})
	if err != nil || !ok {
		t.Fatalf("batch-b not servable after supersede: ok=%v err=%v", ok, err)
	}
	shardBytes, err := os.ReadFile(batchB.export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	if string(content.Bytes) != string(shardBytes) {
		t.Fatalf("kept batch bytes changed after supersede")
	}
	// The superseded batch is no longer servable (files gone)...
	if _, ok, _ := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a", DatasetBatchOptions{}); ok {
		t.Fatalf("superseded batch-a still servable")
	}
	// ...but its publication metadata row REMAINS (catch-up dedup +
	// provenance).
	pubs, err := subscriber.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		BatchID:      "batch-a",
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("list batch-a publications: %v", err)
	}
	if len(pubs) != 1 {
		t.Fatalf("batch-a publication rows = %d, want 1 (metadata kept)", len(pubs))
	}

	// Idempotent: a second supersede evicts nothing.
	again, err := subscriber.SupersedeSourceBatches("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, "batch-b")
	if err != nil {
		t.Fatalf("second SupersedeSourceBatches: %v", err)
	}
	if again.TagsDeleted != 0 || again.RecordsDeleted != 0 || again.FilesDeleted != 0 {
		t.Fatalf("second supersede evicted %+v, want zero", again)
	}
}

func TestSupersedeSourceBatchesKeepsRecordsSharedWithKeptBatch(t *testing.T) {
	// Re-publication of byte-identical content under a NEW batch id (the
	// import path tags repeat CIDs with the new batch): superseding the old
	// batch must delete only its tag rows — the records survive under the
	// kept batch's tags, and the shared cache files (same query/shard hash
	// pair -> same path) must NOT be deleted.
	tmpDir, provider, subscriber := newLatestTestStores(t)
	batchA := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 6, time.Now().UTC().Add(-time.Hour))

	// batch-a2 = the SAME shard, index tags rewritten to batch-a2.
	indexBytes, err := os.ReadFile(batchA.export.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	rewritten := []byte(strings.ReplaceAll(string(indexBytes), "batch-a", "batch-a2"))
	rewrittenPath := filepath.Join(tmpDir, "index-batch-a2.json")
	if err := os.WriteFile(rewrittenPath, rewritten, 0o600); err != nil {
		t.Fatalf("write rewritten index: %v", err)
	}
	imported, _, err := subscriber.ImportDatasetShardFromFiles(batchA.export.ShardPath, rewrittenPath, latestTestPeerID)
	if err != nil {
		t.Fatalf("import rewritten batch: %v", err)
	}
	if imported != 0 {
		t.Fatalf("rewritten batch imported %d new records, want 0 (repeat CIDs)", imported)
	}
	pubA2 := batchA.pub
	pubA2.BatchID = "batch-a2"
	if err := subscriber.UpsertDatasetShardPublication(pubA2); err != nil {
		t.Fatalf("upsert batch-a2 publication: %v", err)
	}

	if got := countBatchTaggedRecords(t, subscriber, batchA.tags.ProviderID, batchA.tags.SourceName, "batch-a2"); got != 6 {
		t.Fatalf("batch-a2 tagged records = %d, want 6 (repeat CIDs re-tagged on import)", got)
	}

	result, err := subscriber.SupersedeSourceBatches("CAT.fbs", batchA.tags.ProviderID, batchA.tags.SourceName, "batch-a2")
	if err != nil {
		t.Fatalf("SupersedeSourceBatches: %v", err)
	}
	if result.TagsDeleted != 6 || result.RecordsDeleted != 0 {
		t.Fatalf("evicted tags=%d records=%d, want 6/0 (shared records survive)", result.TagsDeleted, result.RecordsDeleted)
	}
	if result.FilesDeleted != 0 {
		t.Fatalf("FilesDeleted = %d, want 0 (kept batch shares the cache files)", result.FilesDeleted)
	}
	total, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if total != 6 {
		t.Fatalf("total = %d, want 6", total)
	}
	content, ok, err := subscriber.MaterializedDatasetBatch("CAT.fbs", "batch-a2", DatasetBatchOptions{IncludeBytes: true})
	if err != nil || !ok || content.RecordCount != 6 {
		t.Fatalf("batch-a2 not servable after supersede: ok=%v err=%v", ok, err)
	}
}

func TestSupersedeSourceBatchesChunksAcrossLockWindows(t *testing.T) {
	// More superseded CIDs than one supersedeChunkSize window forces the
	// multi-chunk path; counts must stay exact across chunk boundaries.
	if testing.Short() {
		t.Skip("short mode")
	}
	tmpDir, provider, subscriber := newLatestTestStores(t)
	const oldCount = supersedeChunkSize + 40 // > one chunk
	publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-old", 100000, oldCount, time.Now().UTC().Add(-time.Hour))
	batchNew := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-new", 100000+oldCount, 5, time.Now().UTC())

	result, err := subscriber.SupersedeSourceBatches("CAT.fbs", batchNew.tags.ProviderID, batchNew.tags.SourceName, "batch-new")
	if err != nil {
		t.Fatalf("SupersedeSourceBatches: %v", err)
	}
	if result.TagsDeleted != int64(oldCount) || result.RecordsDeleted != int64(oldCount) {
		t.Fatalf("evicted tags=%d records=%d, want %d/%d", result.TagsDeleted, result.RecordsDeleted, oldCount, oldCount)
	}
	total, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if total != 5 {
		t.Fatalf("post-supersede total = %d, want 5", total)
	}
}
