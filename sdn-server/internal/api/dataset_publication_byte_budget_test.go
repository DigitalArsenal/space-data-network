package api

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func newByteBudgetPublicationFixture(t *testing.T, batchID string, records int) (*ConcreteDatasetPublicationService, *storage.FlatSQLStore, storage.SourceTags) {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      batchID,
		ContentKeyID: "public",
	}
	for i := 0; i < records; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(82000 + i)).
			WithObjectName("BYTE-BUDGET").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:provider", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}

	pinned := make(map[string][]byte)
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	t.Cleanup(kubo.Close)

	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	service := NewConcreteDatasetPublicationService(
		store,
		&fakeDatasetUpdatePublisher{},
		signingKey,
		"16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)
	return service, store, tags
}

// A byte budget must CUT a shard series into more, smaller shards — and lose
// nothing doing it. The cursor used to advance by the requested chunk size, so
// a smaller effective window would have skipped every record the cut deferred.
func TestPublishDatasetUpdateSeriesCutsShardsOnBytes(t *testing.T) {
	const total = 40
	service, store, tags := newByteBudgetPublicationFixture(t, "batch-byte-budget", total)

	// Size the budget from the real records, not a guess: ~8 frames per shard.
	frameProbe := storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName,
		BatchID: tags.BatchID, Limit: 1, AllowLargeResultSet: true, OrderByCID: true,
	}
	one, err := store.QueryIndexedRecords(frameProbe)
	if err != nil || len(one) != 1 {
		t.Fatalf("probe one record: %v (%d records)", err, len(one))
	}
	frame := int64(len(one[0].Data)) + storage.DatasetShardFrameOverheadBytes
	budget := frame * 8

	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:        "CAT.fbs",
		ProviderID:    tags.ProviderID,
		SourceName:    tags.SourceName,
		BatchID:       tags.BatchID,
		MaxShardBytes: budget,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.RecordCount != total {
		t.Fatalf("RecordCount = %d, want every record (%d) — a byte cut must not drop records", result.RecordCount, total)
	}
	if len(result.Publications) != total/8 {
		t.Fatalf("publications = %d, want %d shards of 8 records", len(result.Publications), total/8)
	}
	for i, publication := range result.Publications {
		if publication.RecordCount != 8 {
			t.Fatalf("shard %d holds %d records, want 8", i, publication.RecordCount)
		}
	}

	// And the published shards form a contiguous cursor: no gap, no overlap.
	publications, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications failed: %v", err)
	}
	next := 0
	seen := 0
	for _, publication := range publications {
		if publication.Offset != next {
			t.Fatalf("shard at offset %d, want %d (contiguous cursor)", publication.Offset, next)
		}
		if publication.ByteCount > budget {
			t.Fatalf("shard at offset %d is %d bytes, over the %d-byte budget", publication.Offset, publication.ByteCount, budget)
		}
		next += publication.RecordCount
		seen += publication.RecordCount
	}
	if seen != total {
		t.Fatalf("published shards cover %d records, want %d", seen, total)
	}
}

// The default must not change shard identity for ordinary small-record feeds:
// 64 MiB is far above any $CAT/$OMM window the count limits already bound, so
// an unbudgeted publication and a defaulted one are the same publication.
func TestPublishDatasetUpdateDefaultBudgetLeavesSmallFeedsUnchanged(t *testing.T) {
	const total = 24
	service, _, tags := newByteBudgetPublicationFixture(t, "batch-default-budget", total)

	defaulted, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID,
	})
	if err != nil {
		t.Fatalf("defaulted publication failed: %v", err)
	}
	unbounded, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID,
		MaxShardBytes: -1, // explicitly unbounded
	})
	if err != nil {
		t.Fatalf("unbounded publication failed: %v", err)
	}
	if defaulted.RecordCount != total || unbounded.RecordCount != total {
		t.Fatalf("record counts = %d / %d, want %d", defaulted.RecordCount, unbounded.RecordCount, total)
	}
	if defaulted.ShardCID != unbounded.ShardCID {
		t.Fatalf("default budget changed shard identity: %s vs %s", defaulted.ShardCID, unbounded.ShardCID)
	}
}
