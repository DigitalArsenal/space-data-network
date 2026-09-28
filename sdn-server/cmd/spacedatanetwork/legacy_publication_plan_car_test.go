package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Shards over the 512 MiB bound each stand alone, and a one-shard CAR is the
// shard's bytes pinned a second time: the legacy registration and the CAR
// rebuild record no bundle for them, and the rebuild still verifies.
func TestLegacyCARPathsRecordNoBundleForLoneShards(t *testing.T) {
	tmpDir := t.TempDir()
	pinned := map[string][]byte{}
	kubo := newImportLegacyKuboTestServer(t, pinned)
	defer kubo.Close()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	storagePath := filepath.Join(tmpDir, "sdn")
	store, err := storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	publishedAt := time.Unix(1_778_888_000, 0).UTC()
	for i := 0; i < 3; i++ {
		shardBytes := []byte(fmt.Sprintf("lone shard %d", i))
		shardCID := importLegacyCIDV1RawSHA256ForTest(t, shardBytes)
		pinned[shardCID] = shardBytes
		if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
			SchemaName:   "TBS.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "mls-final-full-cell-export",
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       i * 50_000,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    int64(700 << 20),
			ShardCID:     shardCID,
			IndexCID:     importLegacyCIDV1RawSHA256ForTest(t, []byte(fmt.Sprintf("lone index %d", i))),
			ManifestCID:  importLegacyCIDV1RawSHA256ForTest(t, []byte(fmt.Sprintf("lone manifest %d", i))),
			FeedSequence: int64(i + 1),
			FeedHead:     "lone-feed-head",
			PublishedAt:  publishedAt.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("UpsertDatasetShardPublication %d failed: %v", i, err)
		}
	}
	query := storage.DatasetShardPublicationQuery{
		SchemaName:   "TBS.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "mls-final-full-cell-export",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	}
	publications, err := store.ListDatasetShardPublications(query)
	if err != nil || len(publications) != 3 {
		t.Fatalf("ListDatasetShardPublications: %d (err %v)", len(publications), err)
	}
	pinsBefore := len(pinned)
	if err := recordRegisteredShardGroupCARBundle(context.Background(), store, kubo.URL, filepath.Join(tmpDir, "registered-output"), publications, "16Uiu2HAmProvider", "provider-public-key"); err != nil {
		t.Fatalf("recordRegisteredShardGroupCARBundle failed: %v", err)
	}
	if len(pinned) != pinsBefore {
		t.Fatalf("registration pinned %d new object(s) for shards that stand alone", len(pinned)-pinsBefore)
	}
	carQuery := storage.PinLedgerQuery{
		SchemaName: "TBS.fbs", ProviderID: "space-data-network-02", SourceName: "mls-final-full-cell-export",
		QueryProfile: storage.DatasetPublicationQueryProfile, Role: "shard-group-car", VerificationState: "verified",
	}
	if entries, err := store.ListPinLedgerEntries(carQuery); err != nil || len(entries) != 0 {
		t.Fatalf("registered CAR bundles: %d (err %v), want none", len(entries), err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	result, err := rebuildDatasetPublicationShardGroupCARBundles(context.Background(), datasetPublicationCARRebuildOptions{
		StoragePath:       storagePath,
		IPFSAPIURL:        kubo.URL,
		OutputDir:         filepath.Join(tmpDir, "dataset-publications"),
		Schema:            "TBS.fbs",
		ProviderID:        "space-data-network-02",
		SourceName:        "mls-final-full-cell-export",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		ProviderPeerID:    "16Uiu2HAmProvider",
		ProviderPublicKey: "provider-public-key",
	})
	if err != nil {
		t.Fatalf("rebuild of a lane of lone shards must verify: %v", err)
	}
	if result.Publications != 3 || result.Bundles != 0 {
		t.Fatalf("rebuild result = %#v, want 3 publications and no bundle", result)
	}
	if len(pinned) != pinsBefore {
		t.Fatalf("rebuild pinned %d new object(s)", len(pinned)-pinsBefore)
	}

	store, err = storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	manifest, err := datasync.OpenManifest(store, datasync.QueryRequest{
		Schema:       "TBS.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "mls-final-full-cell-export",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Limit:        50_000,
	}, datasync.MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("OpenManifest failed: %v", err)
	}
	if len(manifest.ArtifactBundles) != 0 || len(manifest.Segments) != 3 {
		t.Fatalf("manifest: %d bundles over %d segments, want 0 over 3 (each segment is its own shard)", len(manifest.ArtifactBundles), len(manifest.Segments))
	}
}

// Coverage is judged against the plan: segments the plan bundles must be
// covered, and a lone shard needs no bundle.
func TestVerifyShardGroupCARCoverageFollowsThePlan(t *testing.T) {
	planned := []storage.ShardGroupCARBundle{{SegmentStart: 0, SegmentCount: 2, Rows: 200}}
	if err := verifyShardGroupCARCoverage([]storage.PinLedgerEntry{{SegmentStart: 0, SegmentCount: 2, RowCount: 200}}, planned, 3); err != nil {
		t.Fatalf("a bundle over [0:2] with segment 2 standing alone: %v", err)
	}
	if err := verifyShardGroupCARCoverage(nil, planned, 3); err == nil {
		t.Fatal("a planned bundle with no recorded CAR verified")
	}
	if err := verifyShardGroupCARCoverage([]storage.PinLedgerEntry{{SegmentStart: 0, SegmentCount: 1, RowCount: 100}}, planned, 3); err == nil {
		t.Fatal("a recorded CAR over [0:1] verified a planned bundle over [0:2]")
	}
	if err := verifyShardGroupCARCoverage(nil, nil, 3); err != nil {
		t.Fatalf("a lane of lone shards needs no bundle: %v", err)
	}
}
