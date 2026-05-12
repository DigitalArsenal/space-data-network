package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFlatSQLStoreRecordsDatasetShardPublications(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	publishedAt := time.Unix(1_778_436_000, 0).UTC()
	pub := DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       50_000,
		Limit:        50_000,
		RecordCount:  50_000,
		ByteCount:    8_000_000,
		ShardCID:     "bafkshard",
		IndexCID:     "bafkindex",
		ManifestCID:  "bafkmanifest",
		PNMCID:       "pnm-cid",
		ShardSHA256:  "shard-sha",
		IndexSHA256:  "index-sha",
		QuerySHA256:  "query-sha",
		ResultSHA256: "result-sha",
		PublishedAt:  publishedAt,
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}

	got, found, err := store.FindDatasetShardPublication(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       50_000,
		Limit:        50_000,
		RecordCount:  50_000,
	})
	if err != nil {
		t.Fatalf("FindDatasetShardPublication failed: %v", err)
	}
	if !found {
		t.Fatal("published shard was not found")
	}
	if got.ShardCID != pub.ShardCID || got.IndexCID != pub.IndexCID || got.ManifestCID != pub.ManifestCID {
		t.Fatalf("unexpected CIDs: %#v", got)
	}
	if got.PublishedAt.Unix() != publishedAt.Unix() {
		t.Fatalf("PublishedAt = %s, want %s", got.PublishedAt, publishedAt)
	}
}

func TestFlatSQLStoreFindsLargestDatasetShardPublicationLimit(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	for _, pub := range []DatasetShardPublication{
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "batch-001",
			QueryProfile: DatasetPublicationQueryProfile,
			Offset:       0,
			Limit:        250,
			RecordCount:  250,
			ByteCount:    40_000,
			ShardCID:     "bafksmall",
			IndexCID:     "bafksmallindex",
		},
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "batch-001",
			QueryProfile: DatasetPublicationQueryProfile,
			Offset:       0,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    8_000_000,
			ShardCID:     "bafklarge",
			IndexCID:     "bafklargeindex",
		},
	} {
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
		}
	}

	limit, found, err := store.FindLargestDatasetShardPublicationLimit(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("FindLargestDatasetShardPublicationLimit failed: %v", err)
	}
	if !found {
		t.Fatal("largest publication limit was not found")
	}
	if limit != 50_000 {
		t.Fatalf("largest publication limit = %d, want 50000", limit)
	}
}
