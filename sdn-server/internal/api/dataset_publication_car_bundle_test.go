package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// seedOneShardCAR pins a one-shard bundle of a published part exactly as the
// node did before the rule (segmentCount 1), or as a legacy whole-scope bundle
// without a range (segmentCount 0).
func (f *hygieneFixture) seedOneShardCAR(t *testing.T, batch string, segmentCount int) storage.PinLedgerEntry {
	t.Helper()
	row := f.rowOf(t, batch)
	car, err := storage.PublishShardGroupCARToIPFS(context.Background(), f.kubo.server.URL, t.TempDir(), []string{row.ShardCID})
	if err != nil {
		t.Fatalf("pin a one-shard CAR of %s: %v", batch, err)
	}
	entry := storage.PinLedgerEntry{
		CID: car.CID, SchemaName: "IQC.fbs", ProviderPeerID: hygienePeerID,
		ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		SnapshotID:   row.FeedHead, Head: row.FeedHead, ByteHash: car.SHA256,
		Role: storage.PinLedgerRoleShardGroupCAR, SegmentStart: 0, SegmentCount: segmentCount,
		RowCount: int64(row.RecordCount), ByteCount: car.ByteCount,
		VerificationState: "verified", VerifiedAt: row.PublishedAt, UpdatedAt: row.PublishedAt,
	}
	if err := f.store.UpsertPinLedgerEntry(entry); err != nil {
		t.Fatal(err)
	}
	// ...and a member of the part's series, as afterPublication recorded it:
	// series retention alone keeps such a bundle for as long as the part.
	lane := storage.DatasetPublicationLane{SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource}
	all, err := f.store.ListDatasetPublicationSeries(lane)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, series := range all {
		if series.BatchID != batch {
			continue
		}
		series.Members = append(series.Members, storage.DatasetPublicationSeriesMember{CID: car.CID, Role: storage.PinLedgerRoleShardGroupCAR})
		if err := f.store.RecordDatasetPublicationSeries(series, hygienePeerID); err != nil {
			t.Fatal(err)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("no series for batch %s", batch)
	}
	return entry
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

// A parts lane never republishes an old part, so its one-shard bundles are
// found lane-wide after the NEXT part's publication.
func TestPublicationReleasesOneShardCARBundlesOfOldParts(t *testing.T) {
	f := newHygieneFixture(t)
	hygieneIngest(t, f.store, "tbs-part-0", 0, 40, carStamp)
	part0 := f.publishBatch(t, "tbs-part-0", 0)
	old := f.seedOneShardCAR(t, "tbs-part-0", 1)
	if _, ok := f.kubo.pinnedSet()[old.CID]; !ok {
		t.Fatal("premise: the seeded one-shard CAR is not pinned")
	}

	hygieneIngest(t, f.store, "tbs-part-1", 40, 80, carStamp)
	part1 := f.publishBatch(t, "tbs-part-1", 0)

	pinned := f.kubo.pinnedSet()
	if _, ok := pinned[old.CID]; ok {
		t.Fatalf("one-shard CAR %s of the old part is still pinned", old.CID)
	}
	for _, value := range []string{part0.ShardCID, part0.IndexCID, part0.ManifestCID, part1.ShardCID, part1.IndexCID, part1.ManifestCID} {
		if _, ok := pinned[value]; !ok {
			t.Fatalf("part CID %s was unpinned with the redundant bundle", value)
		}
	}
	if len(pinned) != 6 {
		t.Fatalf("%d pins, want each part's shard, index and DPM once", len(pinned))
	}
	retired, err := f.store.ListPinLedgerEntries(storage.PinLedgerQuery{CID: old.CID, VerificationState: storage.PinLedgerStateRetired})
	if err != nil || len(retired) != 1 {
		t.Fatalf("ledger row of the released bundle: %d retired (err %v), want 1", len(retired), err)
	}
	if rows := f.advertisedRows(t); len(rows) != 2 {
		t.Fatalf("%d parts advertised, want both", len(rows))
	}
}

// The retention route reports one-shard bundles on a dry run and releases
// them when applied, including a legacy bundle recorded without a range.
func TestRetentionRouteReleasesOneShardCARBundles(t *testing.T) {
	f := newHygieneFixture(t)
	var parts []*DatasetPublicationResult
	for i := 0; i < 2; i++ {
		batch := fmt.Sprintf("tbs-part-%d", i)
		hygieneIngest(t, f.store, batch, i*40, i*40+40, carStamp)
		parts = append(parts, f.publishBatch(t, batch, 0))
	}
	seeded := []storage.PinLedgerEntry{f.seedOneShardCAR(t, "tbs-part-0", 1), f.seedOneShardCAR(t, "tbs-part-1", 0)}
	var seededBytes int64
	for _, entry := range seeded {
		seededBytes += entry.ByteCount
	}

	mux := http.NewServeMux()
	NewDatasetPublicationHandler(f.service).RegisterRoutes(mux)
	call := func(apply bool) DatasetPublicationRetentionReport {
		t.Helper()
		body := fmt.Sprintf(`{"schema":"IQC","providerId":%q,"sourceName":%q,"apply":%v}`, hygieneProvider, hygieneSource, apply)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/retention", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("retention route: %d %s", rec.Code, rec.Body.String())
		}
		var report DatasetPublicationRetentionReport
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}

	dry := call(false)
	if len(dry.RedundantCARs) != 2 || dry.RedundantCARBytes != seededBytes {
		t.Fatalf("dry run: %d redundant CARs / %d bytes, want 2 / %d", len(dry.RedundantCARs), dry.RedundantCARBytes, seededBytes)
	}
	// The series policy keeps both parts, bundles included: releasing the
	// bundles is this pass's own work, not series retention's.
	if len(dry.Unpin) != 0 || len(dry.RetiredSeries) != 0 {
		t.Fatalf("premise: series retention would unpin %d CID(s) / retire %d series", len(dry.Unpin), len(dry.RetiredSeries))
	}
	for _, entry := range seeded {
		if _, ok := f.kubo.pinnedSet()[entry.CID]; !ok {
			t.Fatalf("the dry run unpinned %s", entry.CID)
		}
	}

	applied := call(true)
	if applied.RedundantCARsReleased != 2 {
		t.Fatalf("applied pass released %d one-shard CARs, want 2", applied.RedundantCARsReleased)
	}
	pinned := f.kubo.pinnedSet()
	for _, entry := range seeded {
		if _, ok := pinned[entry.CID]; ok {
			t.Fatalf("one-shard CAR %s is still pinned", entry.CID)
		}
	}
	for _, part := range parts {
		if _, ok := pinned[part.ShardCID]; !ok {
			t.Fatalf("shard %s was unpinned with its bundle", part.ShardCID)
		}
	}
	if cars := f.verifiedCARs(t); len(cars) != 0 {
		t.Fatalf("%d verified CAR rows remain, want none advertised", len(cars))
	}
	if again := call(true); len(again.RedundantCARs) != 0 || again.RedundantCARsReleased != 0 {
		t.Fatalf("a second pass still finds %d one-shard CARs", len(again.RedundantCARs))
	}
}
