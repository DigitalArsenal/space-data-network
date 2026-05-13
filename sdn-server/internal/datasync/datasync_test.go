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
