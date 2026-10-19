package api

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// dataset_publication_car_bundle_test.go — a shard-group CAR of ONE shard is
// that shard's bytes pinned a second time (host-02: single-shard TBS lanes
// carried 3.0 GB of CAR over 3.0 GB of shards). Such a lane pins no bundle, a
// lane of several shards still gets one, and one-shard bundles pinned before
// the rule are released.

const carStamp = "2026-09-28T00:00:00Z"

func (f *hygieneFixture) verifiedCARs(t *testing.T) []storage.PinLedgerEntry {
	t.Helper()
	entries, err := f.store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource,
		Role: storage.PinLedgerRoleShardGroupCAR, VerificationState: "verified",
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func (f *hygieneFixture) syncManifest(t *testing.T, batch string) *datasync.ManifestResponse {
	t.Helper()
	manifest, err := datasync.OpenManifest(f.store, datasync.QueryRequest{
		Schema: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch,
		QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 1000,
	}, datasync.MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("OpenManifest %s: %v", batch, err)
	}
	return manifest
}

func (f *hygieneFixture) rowOf(t *testing.T, batch string) storage.DatasetShardPublication {
	t.Helper()
	for _, row := range f.advertisedRows(t) {
		if row.BatchID == batch {
			return row
		}
	}
	t.Fatalf("no advertised row for batch %s", batch)
	return storage.DatasetShardPublication{}
}

func TestSingleShardPublicationPinsNoCARBundle(t *testing.T) {
	f := newHygieneFixture(t)
	hygieneIngest(t, f.store, "tbs-part-0", 0, 40, carStamp)
	result := f.publishBatch(t, "tbs-part-0", 0)
	if len(result.Publications) > 1 {
		t.Fatalf("premise: %d windows, want one", len(result.Publications))
	}

	pinned := f.kubo.pinnedSet()
	for _, value := range []string{result.ShardCID, result.IndexCID, result.ManifestCID} {
		if _, ok := pinned[value]; !ok {
			t.Fatalf("%s is not pinned", value)
		}
	}
	if len(pinned) != 3 {
		t.Fatalf("%d pins, want the shard, index and DPM only: the shard's bytes pinned once", len(pinned))
	}
	if got := f.kubo.callCount("/api/v0/dag/export"); got != 0 {
		t.Fatalf("the shard was exported %d time(s) to build a CAR nobody needs", got)
	}
	if cars := f.verifiedCARs(t); len(cars) != 0 {
		t.Fatalf("%d shard-group CAR ledger rows, want none", len(cars))
	}
	// A consumer is sent to the shard itself.
	manifest := f.syncManifest(t, "tbs-part-0")
	if len(manifest.ArtifactBundles) != 0 {
		t.Fatalf("manifest advertises %d CAR bundle(s), want none", len(manifest.ArtifactBundles))
	}
	if len(manifest.Segments) != 1 || manifest.Segments[0].CID != result.ShardCID {
		t.Fatalf("manifest segments %+v, want the one shard %s", manifest.Segments, result.ShardCID)
	}
}

func TestMultiShardPublicationStillBundlesItsShards(t *testing.T) {
	f := newHygieneFixture(t)
	hygieneIngest(t, f.store, "iqc-batch", 0, 125, carStamp)
	result := f.publishBatch(t, "iqc-batch", 64<<10)
	windows := len(result.Publications)
	if windows < 2 {
		t.Fatalf("premise: %d windows, want the byte budget to cut several", windows)
	}
	cars := f.verifiedCARs(t)
	if len(cars) != 1 {
		t.Fatalf("%d shard-group CAR bundles, want one over all %d shards", len(cars), windows)
	}
	car := cars[0]
	if car.SegmentStart != 0 || car.SegmentCount != windows || car.RowCount != int64(result.RecordCount) {
		t.Fatalf("bundle covers [%d:%d] %d rows, want [0:%d] %d rows", car.SegmentStart, car.SegmentCount, car.RowCount, windows, result.RecordCount)
	}
	if _, ok := f.kubo.pinnedSet()[car.CID]; !ok {
		t.Fatalf("bundle %s is not pinned", car.CID)
	}
	manifest := f.syncManifest(t, "iqc-batch")
	if len(manifest.ArtifactBundles) != 1 || manifest.ArtifactBundles[0].CID != car.CID {
		t.Fatalf("manifest bundles %+v, want %s", manifest.ArtifactBundles, car.CID)
	}
}
