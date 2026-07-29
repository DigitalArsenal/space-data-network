package api

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// A publication request that names a batch has already stated its scope. It
// must publish that batch, not the first defaultDatasetPublicationLimit records
// of it: a publication is idempotent, so a truncated head republished on every
// cadence returns identical CIDs and a consumer's catalog stops growing at the
// head size forever.
func TestPublishDatasetUpdatePublishesTheWholeNamedBatch(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := storage.SourceTags{
		ProviderID:     "space-data-network-02",
		SourceName:     "celestrak-gp",
		BatchID:        "batch-whole-scope",
		ContentKeyID:   "public",
		ProducerPeerID: "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U",
	}
	const total = defaultDatasetPublicationLimit + 37
	for i := 0; i < total; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(80000 + i)).
			WithObjectName("BATCH-SCOPE").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:provider", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}

	pinned := make(map[string][]byte)
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()

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

	// Exactly the body the §19 ingest-flow trigger sends: schema, provider,
	// source, batch — and nothing else.
	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:     "CAT.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.RecordCount != total {
		t.Fatalf("RecordCount = %d, want the whole batch (%d)", result.RecordCount, total)
	}
	// One chunk, not 2 — a whole-batch publication is a sync payload and is
	// chunked like one, so it is not announced as a long series of small
	// signed publications.
	if len(result.Publications) != 1 {
		t.Fatalf("publications = %d, want 1 whole-batch chunk", len(result.Publications))
	}
}

// An explicit Limit still wins: asking for a head is a legitimate request.
func TestPublishDatasetUpdateHonoursAnExplicitHeadLimit(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-explicit-head",
		ContentKeyID: "public",
	}
	for i := 0; i < 12; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(81000 + i)).
			WithObjectName("EXPLICIT-HEAD").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:provider", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}

	pinned := make(map[string][]byte)
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()

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

	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:     "CAT.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      5,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.RecordCount != 5 {
		t.Fatalf("RecordCount = %d, want the requested head of 5", result.RecordCount)
	}
}
