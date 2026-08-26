package storage

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// AN EMPTY INDEX IS NOT AN IDENTITY.
//
// ReconcileSourceBatchIndexedDuplicates partitions a batch by the SATELLITE
// index — norad_cat_id / entity_id / object_type / ops_status_code / epoch —
// and keeps the newest row in each partition. That is right for $OMM and $CAT
// and catastrophic for every standard that populates none of those columns: all
// of its rows land in the single partition (-1, '', '', '', -1, '') and every
// one of them but the newest is deleted as a "duplicate".
//
// It is not hypothetical and it is not rare: "duplicates" is the DEFAULT mode of
// storage.ingest_with_source, so any flow that does not name a mode gets it. The
// cellular worldwide ingest measured it — six distinct $TBS cell sites ingested
// as one batch, ONE row surviving, and a success reported (graph:
// sdn-cellular-ingest-lands-no-batch).
func TestReconcileDoesNotCollapseRecordsThatCarryNoIndex(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "mls",
		SourceName:   "mls-final-full-cell-export",
		BatchID:      "mls@0",
		ContentKeyID: "public",
	}
	const siteCount = 6
	for i := 0; i < siteCount; i++ {
		record := newTBSRecord(fmt.Sprintf("262-1-3-%d", 10000+i), "mls", 262, 52.52+0.001*float64(i), 13.405)
		if _, err := store.StoreWithSourceTags("TBS.fbs", record, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags $TBS %d: %v", i, err)
		}
	}

	result, err := store.ReconcileSourceBatchIndexedDuplicates("TBS.fbs", tags.ProviderID, tags.SourceName, tags.BatchID, true)
	if err != nil {
		t.Fatalf("ReconcileSourceBatchIndexedDuplicates: %v", err)
	}
	if result.Matched != 0 || result.Deleted != 0 {
		t.Fatalf("index-free records were treated as duplicates: matched=%d deleted=%d", result.Matched, result.Deleted)
	}

	count, err := store.Count("TBS.fbs")
	if err != nil {
		t.Fatalf("count $TBS: %v", err)
	}
	if count != siteCount {
		t.Fatalf("after reconcile the store holds %d $TBS records, want %d — distinct sites were deleted as duplicates", count, siteCount)
	}
}

// The guard must not have bought that safety by disabling the reconcile it
// exists for: records that DO carry an index still collapse to the newest.
func TestReconcileStillCollapsesIndexedDuplicates(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "celestrak", SourceName: "celestrak-gp", BatchID: "celestrak@1"}
	// Two records, ONE satellite identity, one epoch, DIFFERENT bytes (the store
	// is content-addressed, so byte-identical records are one record and there
	// would be nothing to reconcile). Same index = same partition = the second
	// is a genuine duplicate and must not survive.
	const epoch = 1700000000
	epochText := time.Unix(epoch, 0).UTC().Format("2006-01-02T15:04:05Z")
	for i := 0; i < 2; i++ {
		data := sds.NewOMMBuilder().
			WithNoradCatID(25544).
			WithObjectName("ISS (ZARYA)").
			WithObjectID("1998-067A").
			WithEpoch(epochText).
			WithEpochTimestamp(float64(epoch)).
			WithMeanMotion(15.5 + 0.01*float64(i)).
			WithEccentricity(0.0001).
			WithInclination(51.6).
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", data[4:], "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags $OMM %d: %v", i, err)
		}
	}

	result, err := store.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", tags.ProviderID, tags.SourceName, tags.BatchID, true)
	if err != nil {
		t.Fatalf("ReconcileSourceBatchIndexedDuplicates: %v", err)
	}
	if result.Matched == 0 {
		t.Fatal("an indexed duplicate was no longer matched — the guard disabled the reconcile it was meant to narrow")
	}
}
