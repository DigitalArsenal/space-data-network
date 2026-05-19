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

func TestFlatSQLStoreChainsDatasetShardPublicationFeedEntries(t *testing.T) {
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
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    8_000_000,
			ShardCID:     "bafkshard000",
			IndexCID:     "bafkindex000",
			PublishedAt:  time.Unix(1_778_436_000, 0).UTC(),
		},
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "batch-001",
			QueryProfile: DatasetPublicationQueryProfile,
			Offset:       50_000,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    8_250_000,
			ShardCID:     "bafkshard001",
			IndexCID:     "bafkindex001",
			PublishedAt:  time.Unix(1_778_436_060, 0).UTC(),
		},
	} {
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
		}
	}

	publications, err := store.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications failed: %v", err)
	}
	if len(publications) != 2 {
		t.Fatalf("publications = %d, want 2", len(publications))
	}
	if publications[0].FeedSequence != 1 || publications[0].PreviousHead != "" || publications[0].FeedHead == "" {
		t.Fatalf("first feed entry is not the feed root: %#v", publications[0])
	}
	if publications[1].FeedSequence != 2 || publications[1].PreviousHead != publications[0].FeedHead || publications[1].FeedHead == "" {
		t.Fatalf("second feed entry did not chain from first: first=%#v second=%#v", publications[0], publications[1])
	}
	if publications[1].FeedHead == publications[0].FeedHead {
		t.Fatalf("second feed head should change after appending a shard")
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

func TestFlatSQLStoreDeletesDatasetShardPublicationsAtOrAfterOffset(t *testing.T) {
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
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    8_000_000,
			ShardCID:     "bafkshard000",
			IndexCID:     "bafkindex000",
		},
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "batch-001",
			QueryProfile: DatasetPublicationQueryProfile,
			Offset:       50_000,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    8_250_000,
			ShardCID:     "bafkshard001",
			IndexCID:     "bafkindex001",
		},
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "batch-001",
			QueryProfile: DatasetPublicationQueryProfile,
			Offset:       100_000,
			Limit:        50_000,
			RecordCount:  25_000,
			ByteCount:    4_000_000,
			ShardCID:     "bafkshard002",
			IndexCID:     "bafkindex002",
		},
		{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "other-batch",
			QueryProfile: DatasetPublicationQueryProfile,
			Offset:       50_000,
			Limit:        50_000,
			RecordCount:  50_000,
			ByteCount:    8_250_000,
			ShardCID:     "bafkother",
			IndexCID:     "bafkotherindex",
		},
	} {
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
		}
	}

	deleted, err := store.DeleteDatasetShardPublicationsAtOrAfterOffset(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: DatasetPublicationQueryProfile,
		Limit:        50_000,
	}, 50_000)
	if err != nil {
		t.Fatalf("DeleteDatasetShardPublicationsAtOrAfterOffset failed: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}

	publications, err := store.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications failed: %v", err)
	}
	if len(publications) != 1 || publications[0].Offset != 0 {
		t.Fatalf("publications after delete = %#v, want only offset 0", publications)
	}
	other, err := store.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "other-batch",
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications other batch failed: %v", err)
	}
	if len(other) != 1 || other[0].Offset != 50_000 {
		t.Fatalf("other batch publications = %#v, want unchanged offset 50000", other)
	}
}
