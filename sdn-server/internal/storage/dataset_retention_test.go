package storage

// Subscription retention (ReplaceCurrent): RetainNewestSourceBatch over the
// same real provider->subscriber publication cycle dataset_latest_test.go
// drives (records stored with source tags, exported, imported through the
// chunked shard-import path, publication rows + cached files recorded like
// the feed-head materializer does).

import (
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestRetainNewestSourceBatchKeepsOnlyTheNewestBatch(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	batchA := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 6, time.Now().UTC().Add(-time.Hour))
	batchB := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-b", 70006, 3, time.Now().UTC())

	preTotal, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if preTotal != 9 {
		t.Fatalf("pre-retention total = %d, want 9 (both batches held)", preTotal)
	}

	newest, _, ok, pending, err := subscriber.NewestServableSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName)
	if err != nil {
		t.Fatalf("NewestServableSourceBatch: %v", err)
	}
	if !ok || pending || newest != "batch-b" {
		t.Fatalf("NewestServableSourceBatch = %q ok=%v pending=%v, want batch-b", newest, ok, pending)
	}

	result, keep, err := subscriber.RetainNewestSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, RetainCandidate{})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch: %v", err)
	}
	if keep != "batch-b" {
		t.Fatalf("kept batch = %q, want batch-b", keep)
	}
	if result.RecordsDeleted != 6 || result.TagsDeleted != 6 {
		t.Fatalf("evicted records/tags = %d/%d, want 6/6 (batch-a)", result.RecordsDeleted, result.TagsDeleted)
	}

	postTotal, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords post: %v", err)
	}
	if postTotal != int64(batchB.export.RecordCount) || postTotal != 3 {
		t.Fatalf("post-retention total = %d, want the newest batch size 3", postTotal)
	}
	if got := countBatchTaggedRecords(t, subscriber, batchA.tags.ProviderID, batchA.tags.SourceName, "batch-a"); got != 0 {
		t.Fatalf("batch-a records after retention = %d, want 0", got)
	}
	if got := countBatchTaggedRecords(t, subscriber, batchB.tags.ProviderID, batchB.tags.SourceName, "batch-b"); got != 3 {
		t.Fatalf("batch-b records after retention = %d, want 3", got)
	}

	// Idempotent: a second pass keeps the same batch and evicts nothing.
	again, keepAgain, err := subscriber.RetainNewestSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, RetainCandidate{})
	if err != nil {
		t.Fatalf("second RetainNewestSourceBatch: %v", err)
	}
	if keepAgain != "batch-b" || again.TagsDeleted != 0 || again.RecordsDeleted != 0 || again.FilesDeleted != 0 {
		t.Fatalf("second pass kept %q and evicted %+v, want batch-b and zero", keepAgain, again)
	}
}

func TestRetainNewestSourceBatchUsesTheImportedBatchWithoutLedgerRows(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 6, time.Now().UTC().Add(-2*time.Hour))
	batchB := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-b", 70006, 3, time.Now().UTC().Add(-time.Hour))

	// A verified-manifest import lands records under a batch tag but records
	// no publication row (the PNM replay path). It is still the newest set.
	tags := SourceTags{
		ProviderID:   batchB.tags.ProviderID,
		SourceName:   batchB.tags.SourceName,
		SourceURL:    batchB.tags.SourceURL,
		BatchID:      "batch-c",
		ContentKeyID: "public",
	}
	records := make([][]byte, 0, 4)
	for i := 0; i < 4; i++ {
		records = append(records, sds.NewCATBuilder().
			WithNoradCatID(uint32(70009+i)).
			WithObjectName("LATEST-BATCH").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build())
	}
	if _, err := subscriber.StoreBatchWithSourceTags("CAT.fbs", records, latestTestPeerID, nil, tags); err != nil {
		t.Fatalf("store batch-c on the subscriber: %v", err)
	}
	preTotal, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if preTotal != 13 {
		t.Fatalf("pre-retention total = %d, want 13", preTotal)
	}

	result, keep, err := subscriber.RetainNewestSourceBatch("CAT.fbs", tags.ProviderID, tags.SourceName, RetainCandidate{BatchID: "batch-c", PublishedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch: %v", err)
	}
	if keep != "batch-c" {
		t.Fatalf("kept batch = %q, want the just-imported batch-c", keep)
	}
	if result.RecordsDeleted != 9 {
		t.Fatalf("evicted records = %d, want 9 (batch-a + batch-b)", result.RecordsDeleted)
	}
	postTotal, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords post: %v", err)
	}
	if postTotal != 4 {
		t.Fatalf("post-retention total = %d, want 4 (batch-c only)", postTotal)
	}
	if got := countBatchTaggedRecords(t, subscriber, tags.ProviderID, tags.SourceName, "batch-c"); got != 4 {
		t.Fatalf("batch-c records after retention = %d, want 4", got)
	}

}

func TestRetainNewestSourceBatchKeepsANewerLedgerBatchOverAnOlderImport(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 6, time.Now().UTC().Add(-2*time.Hour))
	batchB := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-b", 70006, 3, time.Now().UTC().Add(-time.Hour))

	// A no-ledger import that is OLDER than the newest servable ledger batch
	// does not displace it: batch-b stays the current set and the old
	// import is evicted with batch-a.
	tags := SourceTags{
		ProviderID:   batchB.tags.ProviderID,
		SourceName:   batchB.tags.SourceName,
		SourceURL:    batchB.tags.SourceURL,
		BatchID:      "batch-old",
		ContentKeyID: "public",
	}
	records := make([][]byte, 0, 2)
	for i := 0; i < 2; i++ {
		records = append(records, sds.NewCATBuilder().
			WithNoradCatID(uint32(70020+i)).
			WithObjectName("OLD-BATCH").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build())
	}
	if _, err := subscriber.StoreBatchWithSourceTags("CAT.fbs", records, latestTestPeerID, nil, tags); err != nil {
		t.Fatalf("store batch-old on the subscriber: %v", err)
	}
	result, keep, err := subscriber.RetainNewestSourceBatch("CAT.fbs", tags.ProviderID, tags.SourceName, RetainCandidate{BatchID: "batch-old", PublishedAt: time.Now().UTC().Add(-3 * time.Hour)})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch: %v", err)
	}
	if keep != "batch-b" {
		t.Fatalf("kept batch = %q, want the newer servable batch-b", keep)
	}
	if result.RecordsDeleted != 8 {
		t.Fatalf("evicted records = %d, want 8 (batch-a + batch-old)", result.RecordsDeleted)
	}
	total, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if total != 3 {
		t.Fatalf("total after retention = %d, want 3 (batch-b only)", total)
	}

	// The stale ledger batches (files gone) never block a later, newer
	// no-ledger import: batch-b is the current set and batch-new replaces it.
	tags.BatchID = "batch-new"
	records = records[:0]
	for i := 0; i < 5; i++ {
		records = append(records, sds.NewCATBuilder().
			WithNoradCatID(uint32(70030+i)).
			WithObjectName("NEW-BATCH").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build())
	}
	if _, err := subscriber.StoreBatchWithSourceTags("CAT.fbs", records, latestTestPeerID, nil, tags); err != nil {
		t.Fatalf("store batch-new on the subscriber: %v", err)
	}
	result, keep, err = subscriber.RetainNewestSourceBatch("CAT.fbs", tags.ProviderID, tags.SourceName, RetainCandidate{BatchID: "batch-new", PublishedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch (batch-new): %v", err)
	}
	if keep != "batch-new" || result.RecordsDeleted != 3 {
		t.Fatalf("batch-new pass kept %q and evicted %d records, want batch-new / 3", keep, result.RecordsDeleted)
	}
	// And once the kept set has no ledger rows, a further no-ledger import
	// still replaces it — the stale ledger rows left behind do not read as
	// an import in flight.
	tags.BatchID = "batch-newer"
	records = records[:0]
	records = append(records, sds.NewCATBuilder().WithNoradCatID(70040).WithObjectName("NEWER-BATCH").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build())
	if _, err := subscriber.StoreBatchWithSourceTags("CAT.fbs", records, latestTestPeerID, nil, tags); err != nil {
		t.Fatalf("store batch-newer on the subscriber: %v", err)
	}
	result, keep, err = subscriber.RetainNewestSourceBatch("CAT.fbs", tags.ProviderID, tags.SourceName, RetainCandidate{BatchID: "batch-newer", PublishedAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch (batch-newer): %v", err)
	}
	if keep != "batch-newer" || result.RecordsDeleted != 5 {
		t.Fatalf("batch-newer pass kept %q and evicted %d records, want batch-newer / 5", keep, result.RecordsDeleted)
	}
	total, err = subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if total != 1 {
		t.Fatalf("total after the third replacement = %d, want 1", total)
	}
}

func TestRetainNewestSourceBatchWaitsWhileANewerBatchIsMidImport(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 70000, 6, time.Now().UTC().Add(-time.Hour))
	batchB := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-b", 70006, 3, time.Now().UTC())

	// batch-d: a newer feed-sequence whose only window is full — the shape of
	// a series still landing window by window. Not servable, so it blocks
	// eviction rather than being kept or evicted.
	midImport := DatasetShardPublication{
		SchemaName:   "CAT.fbs",
		ProviderID:   batchB.tags.ProviderID,
		SourceName:   batchB.tags.SourceName,
		BatchID:      "batch-d",
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50,
		RecordCount:  50,
		ByteCount:    4096,
		ShardCID:     "bafymidimportshard",
		IndexCID:     "bafymidimportindex",
		ShardSHA256:  strings.Repeat("d", 64),
		IndexSHA256:  strings.Repeat("e", 64),
		QuerySHA256:  strings.Repeat("f", 64),
		ResultSHA256: strings.Repeat("a", 64),
		FeedSequence: batchB.pub.FeedSequence + 5,
		PublishedAt:  time.Now().UTC().Add(time.Minute),
	}
	if err := subscriber.UpsertDatasetShardPublication(midImport); err != nil {
		t.Fatalf("upsert mid-import publication: %v", err)
	}

	newest, _, ok, pending, err := subscriber.NewestServableSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName)
	if err != nil {
		t.Fatalf("NewestServableSourceBatch: %v", err)
	}
	if !ok || newest != "batch-b" || !pending {
		t.Fatalf("NewestServableSourceBatch = %q ok=%v pending=%v, want batch-b with pending=true", newest, ok, pending)
	}

	result, keep, err := subscriber.RetainNewestSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, RetainCandidate{BatchID: "batch-d", PublishedAt: midImport.PublishedAt})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch: %v", err)
	}
	if keep != "" || result.RecordsDeleted != 0 || result.TagsDeleted != 0 || result.FilesDeleted != 0 {
		t.Fatalf("mid-import pass kept %q and evicted %+v, want nothing", keep, result)
	}
	total, err := subscriber.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if total != 9 {
		t.Fatalf("total while a newer batch is mid-import = %d, want 9 (nothing evicted)", total)
	}

	// Once the series ends on a short window the newest batch is the answer.
	if _, err := subscriber.DeleteDatasetShardPublicationsAtOrAfterOffset(DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   batchB.tags.ProviderID,
		SourceName:   batchB.tags.SourceName,
		BatchID:      "batch-d",
		QueryProfile: DatasetPublicationQueryProfile,
	}, 0); err != nil {
		t.Fatalf("drop mid-import row: %v", err)
	}
	result, keep, err = subscriber.RetainNewestSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, RetainCandidate{})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch after the series ended: %v", err)
	}
	if keep != "batch-b" || result.RecordsDeleted != 6 {
		t.Fatalf("after the series ended kept %q and evicted %d records, want batch-b / 6", keep, result.RecordsDeleted)
	}
}
