package datasync

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestOpenManifestUsesPublishedFeedWithoutScanningRawRows(t *testing.T) {
	store := newDataSyncTestStore(t)
	publishedAt := time.Unix(1_778_600_000, 0).UTC()
	publications := []storage.DatasetShardPublication{
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       0,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    15_000_000,
			ShardCID:     "bafyommshard000",
			IndexCID:     "bafyommindex000",
			ManifestCID:  "bafyommmanifest000",
			PNMCID:       "bafyommpnm000",
			ShardSHA256:  "shard-sha-000",
			IndexSHA256:  "index-sha-000",
			QuerySHA256:  "query-sha-000",
			ResultSHA256: "result-sha-000",
			PublishedAt:  publishedAt,
		},
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       50_000,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    16_000_000,
			ShardCID:     "bafyommshard001",
			IndexCID:     "bafyommindex001",
			ManifestCID:  "bafyommmanifest001",
			PNMCID:       "bafyommpnm001",
			ShardSHA256:  "shard-sha-001",
			IndexSHA256:  "index-sha-001",
			QuerySHA256:  "query-sha-001",
			ResultSHA256: "result-sha-001",
			PublishedAt:  publishedAt.Add(time.Second),
		},
	}
	for _, publication := range publications {
		if err := store.UpsertDatasetShardPublication(publication); err != nil {
			t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
		}
	}

	manifest, err := OpenManifest(store, QueryRequest{
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Limit:        10,
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("OpenManifest failed: %v", err)
	}

	if manifest.TotalCount != 100_000 || manifest.TotalBytes != 31_000_000 {
		t.Fatalf("manifest totals = rows %d bytes %d, want rows 100000 bytes 31000000", manifest.TotalCount, manifest.TotalBytes)
	}
	if manifest.Head == "" || manifest.SnapshotID == "" || manifest.ManifestID == "" {
		t.Fatalf("manifest missing feed identity: %+v", manifest)
	}
	if len(manifest.Segments) != 2 {
		t.Fatalf("segments = %d, want 2: %+v", len(manifest.Segments), manifest.Segments)
	}
	if manifest.Segments[0].CID != "bafyommshard000" || manifest.Segments[0].RowCount != 50_000 || manifest.Segments[0].ByteCount != 15_000_000 {
		t.Fatalf("first segment did not come from the published feed: %+v", manifest.Segments[0])
	}
	if manifest.Segments[0].NextCursor != EncodeCursor(50_000) {
		t.Fatalf("first segment next cursor = %q, want %q", manifest.Segments[0].NextCursor, EncodeCursor(50_000))
	}
	if manifest.Segments[1].CID != "bafyommshard001" || manifest.Segments[1].NextCursor != "" {
		t.Fatalf("second segment did not terminate the published feed: %+v", manifest.Segments[1])
	}
	if manifest.Segments[0].FeedSequence != 1 || manifest.Segments[0].PreviousHead != "" || manifest.Segments[0].FeedHead == "" {
		t.Fatalf("first segment missing feed chain metadata: %+v", manifest.Segments[0])
	}
	if manifest.Segments[1].FeedSequence != 2 || manifest.Segments[1].PreviousHead != manifest.Segments[0].FeedHead || manifest.Segments[1].FeedHead == "" {
		t.Fatalf("second segment missing feed chain metadata: %+v", manifest.Segments[1])
	}
}

func TestOpenManifestAdvertisesPublishedShardGroupCARBundle(t *testing.T) {
	store := newDataSyncTestStore(t)
	publication := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50_000,
		RecordCount:  50_000,
		ByteCount:    15_000_000,
		ShardCID:     "bafyommshard000",
		IndexCID:     "bafyommindex000",
		ManifestCID:  "bafyommmanifest000",
		ShardSHA256:  "shard-sha-000",
		IndexSHA256:  "index-sha-000",
		QuerySHA256:  "query-sha-000",
		ResultSHA256: "result-sha-000",
		PublishedAt:  time.Unix(1_778_600_000, 0).UTC(),
	}
	if err := store.UpsertDatasetShardPublication(publication); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafyshardgroupcar",
		SchemaName:        "OMM.fbs",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		Head:              publication.FeedHead,
		ByteHash:          "car-sha-256",
		Role:              "shard-group-car",
		RowCount:          50_000,
		ByteCount:         16_000_000,
		VerificationState: "verified",
		VerifiedAt:        publication.PublishedAt,
	}); err != nil {
		t.Fatalf("UpsertPinLedgerEntry failed: %v", err)
	}

	manifest, err := OpenManifest(store, QueryRequest{
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Limit:        50_000,
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("OpenManifest failed: %v", err)
	}

	if len(manifest.ArtifactBundles) != 1 {
		t.Fatalf("artifact bundles = %d, want 1: %+v", len(manifest.ArtifactBundles), manifest.ArtifactBundles)
	}
	bundle := manifest.ArtifactBundles[0]
	if bundle.Role != "shard-group-car" || bundle.CID != "bafyshardgroupcar" || bundle.SHA256 != "car-sha-256" || bundle.ByteCount != 16_000_000 {
		t.Fatalf("unexpected CAR bundle: %+v", bundle)
	}
	if bundle.SegmentStart != 0 || bundle.SegmentCount != 1 {
		t.Fatalf("CAR bundle segment range = %d/%d, want 0/1", bundle.SegmentStart, bundle.SegmentCount)
	}
}

func TestScanAppliesSubscriptionSyncFilterBeforeReturningRefs(t *testing.T) {
	store := newDataSyncTestStore(t)
	tags := storage.SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-a",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}
	oldOMM := sds.NewOMMBuilder().
		WithNoradCatID(10001).
		WithObjectID("2026-OLD").
		WithObjectName("OLD").
		WithEpoch("2026-05-10T12:00:00Z").
		Build()
	matchOMM := sds.NewOMMBuilder().
		WithNoradCatID(20002).
		WithObjectID("2026-MATCH").
		WithObjectName("MATCH").
		WithEpoch("2026-05-12T12:00:00Z").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", oldOMM, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store old OMM failed: %v", err)
	}
	matchCID, err := store.StoreWithSourceTags("OMM.fbs", matchOMM, "source:celestrak", nil, tags)
	if err != nil {
		t.Fatalf("store matching OMM failed: %v", err)
	}

	scan, records, err := Scan(store, QueryRequest{
		Schema:     "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      10,
		SyncFilter: "EPOCH_DAY = '2026-05-12'",
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if scan.TotalCount != 1 || scan.Count != 1 {
		t.Fatalf("scan count = %d/%d, want 1/1", scan.Count, scan.TotalCount)
	}
	if len(records) != 1 || records[0].CID != matchCID {
		t.Fatalf("scan records = %+v, want one CID %s", records, matchCID)
	}
	if len(scan.Results) != 1 || scan.Results[0]["cid"] != matchCID {
		t.Fatalf("scan result refs = %+v, want CID %s", scan.Results, matchCID)
	}
}

func TestScanUsesSnapshotRowIDCursorForProviderPagination(t *testing.T) {
	store := newDataSyncTestStore(t)
	tags := storage.SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-a",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}
	for _, norad := range []uint32{10001, 10002, 10003} {
		omm := sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectID("2026-001").
			WithObjectName("SAT").
			WithEpoch("2026-05-12T12:00:00Z").
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", omm, "source:celestrak", nil, tags); err != nil {
			t.Fatalf("store OMM %d failed: %v", norad, err)
		}
	}

	first, _, err := Scan(store, QueryRequest{
		Schema:     "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      2,
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("first Scan failed: %v", err)
	}
	if first.Count != 2 || first.TotalCount != 3 {
		t.Fatalf("first scan count = %d/%d, want 2/3", first.Count, first.TotalCount)
	}
	if first.NextCursor == "" {
		t.Fatalf("first scan did not return a next cursor")
	}
	if first.NextCursor == EncodeCursor(2) {
		t.Fatalf("next cursor still uses legacy offset encoding: %q", first.NextCursor)
	}

	newOMM := sds.NewOMMBuilder().
		WithNoradCatID(99999).
		WithObjectID("2026-NEW").
		WithObjectName("NEW").
		WithEpoch("2026-05-12T12:00:00Z").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", newOMM, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store post-snapshot OMM failed: %v", err)
	}

	second, records, err := Scan(store, QueryRequest{
		Schema:        "OMM.fbs",
		ProviderID:    "space-data-network-02",
		SourceName:    "celestrak-gp",
		Limit:         2,
		Cursor:        first.NextCursor,
		SnapshotID:    first.SnapshotID,
		Head:          first.Head,
		HighWaterMark: first.HighWaterMark,
		TotalCount:    first.TotalCount,
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("second Scan failed: %v", err)
	}
	if second.TotalCount != 3 || second.Count != 1 || second.NextCursor != "" {
		t.Fatalf("second scan = count %d/%d next %q, want 1/3 terminal", second.Count, second.TotalCount, second.NextCursor)
	}
	if len(records) != 1 {
		t.Fatalf("second scan records = %d, want 1", len(records))
	}
	if records[0].SourceTags.ProviderID != "space-data-network-02" {
		t.Fatalf("second scan returned unexpected record: %+v", records[0])
	}
}

func newDataSyncTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
