package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/ipfs/go-cid"
	car "github.com/ipld/go-car/v2"
	carstorage "github.com/ipld/go-car/v2/storage"
	mh "github.com/multiformats/go-multihash"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type fakeDatasetPublicationService struct {
	request DatasetPublicationRequest
	result  DatasetPublicationResult
	err     error
	called  bool
}

func (f *fakeDatasetPublicationService) PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	f.called = true
	f.request = req
	if f.err != nil {
		return nil, f.err
	}
	return &f.result, nil
}

func TestDatasetPublicationHandlerRejectsNonLocalRequests(t *testing.T) {
	service := &fakeDatasetPublicationService{}
	handler := NewDatasetPublicationHandler(service)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := bytes.NewBufferString(`{"schema":"OMM.fbs"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", body)
	req.RemoteAddr = "203.0.113.5:4321"
	res := httptest.NewRecorder()

	mux.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
	if service.called {
		t.Fatal("service was called for non-local request")
	}
}

func TestDatasetPublicationHandlerPublishesLocalRequest(t *testing.T) {
	service := &fakeDatasetPublicationService{
		result: DatasetPublicationResult{
			Schema:      "OMM.fbs",
			RecordCount: 42,
			ManifestCID: "bafymanifest",
			PNMCID:      "bafypnm",
		},
	}
	handler := NewDatasetPublicationHandler(service)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := bytes.NewBufferString(`{"schema":"OMM.fbs","sourceName":"celestrak-gp","providerId":"space-data-network-02","limit":1000,"combinedCelesTrak":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", body)
	req.RemoteAddr = "127.0.0.1:4321"
	res := httptest.NewRecorder()

	mux.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", res.Code, http.StatusAccepted, res.Body.String())
	}
	if !service.called {
		t.Fatal("service was not called")
	}
	if service.request.Schema != "OMM.fbs" || service.request.SourceName != "celestrak-gp" {
		t.Fatalf("unexpected request: %#v", service.request)
	}
	var payload DatasetPublicationResult
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ManifestCID != "bafymanifest" || payload.PNMCID != "bafypnm" {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestDatasetPublicationHandlerPreservesDatastoreKey(t *testing.T) {
	service := &fakeDatasetPublicationService{
		result: DatasetPublicationResult{
			Schema:      "OMM.fbs",
			RecordCount: 2,
			ManifestCID: "bafymanifest",
		},
	}
	handler := NewDatasetPublicationHandler(service)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := bytes.NewBufferString(`{"schema":"OMM.fbs","datastoreKey":"sdn-ds-v1-test","fullCatalog":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", body)
	req.RemoteAddr = "127.0.0.1:4321"
	res := httptest.NewRecorder()

	mux.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", res.Code, http.StatusAccepted, res.Body.String())
	}
	if service.request.DatastoreKey != "sdn-ds-v1-test" {
		t.Fatalf("DatastoreKey = %q, want sdn-ds-v1-test", service.request.DatastoreKey)
	}
}

type fakeDatasetUpdatePublisher struct {
	announcement  sdnpubsub.DatasetUpdateAnnouncement
	announcements []sdnpubsub.DatasetUpdateAnnouncement
	feedHeads     []sdnpubsub.DatasetFeedHeadAnnouncement
	called        bool
}

func (f *fakeDatasetUpdatePublisher) PublishDatasetUpdatePNM(ctx context.Context, ann sdnpubsub.DatasetUpdateAnnouncement) error {
	f.called = true
	f.announcement = ann
	f.announcements = append(f.announcements, ann)
	return nil
}

func (f *fakeDatasetUpdatePublisher) PublishDatasetFeedHead(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement) error {
	f.feedHeads = append(f.feedHeads, ann)
	return nil
}

func TestUnlimitedDatasetPublicationLimitUsesNativeIntMaximum(t *testing.T) {
	limit := unlimitedDatasetPublicationLimit()
	if limit <= 250000 {
		t.Fatalf("unlimitedDatasetPublicationLimit = %d, want above legacy cap", limit)
	}
	if limit != int(^uint(0)>>1) {
		t.Fatalf("unlimitedDatasetPublicationLimit = %d, want native int maximum", limit)
	}
}

func TestConcreteDatasetPublicationServiceExportsPinsSignsAndAnnounces(t *testing.T) {
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
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	recordB := sds.NewCATBuilder().
		WithNoradCatID(40909).
		WithObjectName("STARLINK-1001").
		WithObjectID("2015-049A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordA, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordB, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}

	pinned := make(map[string][]byte)
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()

	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(
		store,
		publisher,
		signingKey,
		"space-data-network-02",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)

	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:            "CAT",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-satcat-csv",
		BatchID:           "source-sha-001",
		DatasetID:         "celestrak-cat",
		Limit:             10,
		CombinedCelesTrak: true,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.Schema != "CAT.fbs" || result.RecordCount != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.ShardCID == "" || result.IndexCID == "" || result.ManifestCID == "" || result.PNMCID == "" {
		t.Fatalf("result missing CIDs: %#v", result)
	}
	if len(pinned) != 4 {
		t.Fatalf("pinned object count = %d, want shard, index, manifest, and shard-group CAR", len(pinned))
	}
	if _, ok := pinned[result.ShardCID]; !ok {
		t.Fatalf("shard CID %q was not pinned", result.ShardCID)
	}
	if _, ok := pinned[result.IndexCID]; !ok {
		t.Fatalf("index CID %q was not pinned", result.IndexCID)
	}
	if _, ok := pinned[result.ManifestCID]; !ok {
		t.Fatalf("manifest CID %q was not pinned", result.ManifestCID)
	}
	manifest := dpm.GetRootAsDPM(pinned[result.ManifestCID], 0)
	var asset dpm.DPMAsset
	if !manifest.ASSETS(&asset, 0) {
		t.Fatal("manifest missing first asset")
	}
	schemaHash := string(asset.SCHEMA_HASH())
	if schemaHash == "" || schemaHash == "sdn-schema-current" {
		t.Fatalf("manifest schema hash was not bound to embedded SDS content: %q", schemaHash)
	}
	if !publisher.called {
		t.Fatal("publisher was not called")
	}
	if !publisher.announcement.CombinedCelesTrak {
		t.Fatal("CombinedCelesTrak was not preserved")
	}
	if len(publisher.announcement.Schemas) != 1 || publisher.announcement.Schemas[0] != "CAT.fbs" {
		t.Fatalf("unexpected announcement schemas: %#v", publisher.announcement.Schemas)
	}
	if len(publisher.announcement.PNM) == 0 {
		t.Fatal("publisher received empty PNM")
	}
	publishedShard, found, err := store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-satcat-csv",
		BatchID:      "source-sha-001",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        10,
		RecordCount:  2,
	})
	if err != nil {
		t.Fatalf("FindDatasetShardPublication failed: %v", err)
	}
	if !found {
		t.Fatal("published shard registry entry was not stored")
	}
	if publishedShard.ShardCID != result.ShardCID || publishedShard.IndexCID != result.IndexCID || publishedShard.ManifestCID != result.ManifestCID || publishedShard.PNMCID != result.PNMCID {
		t.Fatalf("published shard registry entry mismatch: %#v result=%#v", publishedShard, result)
	}
	if len(publisher.feedHeads) != 1 {
		t.Fatalf("feed head announcements = %d, want 1", len(publisher.feedHeads))
	}
	if publisher.feedHeads[0].FeedHead != publishedShard.FeedHead || publisher.feedHeads[0].FeedSequence != publishedShard.FeedSequence {
		t.Fatalf("feed head announcement mismatch: announcement=%#v stored=%#v", publisher.feedHeads[0], publishedShard)
	}

	ledger, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:     "CAT.fbs",
		ProviderPeerID: "space-data-network-02",
		QueryProfile:   storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListPinLedgerEntries failed: %v", err)
	}
	if len(ledger) != 5 {
		t.Fatalf("pin ledger entries = %d, want shard, index, manifest, pnm, and shard-group CAR: %#v", len(ledger), ledger)
	}
	wantPublicKey := hex.EncodeToString(signingKey.Public().(ed25519.PublicKey))
	entriesByRole := map[string]storage.PinLedgerEntry{}
	for _, entry := range ledger {
		entriesByRole[entry.Role] = entry
		if entry.ProviderPublicKey != wantPublicKey {
			t.Fatalf("pin ledger provider public key = %q, want %q", entry.ProviderPublicKey, wantPublicKey)
		}
		if entry.ProviderID != "space-data-network-02" || entry.SourceName != "celestrak-satcat-csv" || entry.BatchID != "source-sha-001" {
			t.Fatalf("pin ledger source identity mismatch: %#v", entry)
		}
		if entry.SnapshotID != publishedShard.FeedHead || entry.Head != publishedShard.FeedHead || entry.VerificationState != "verified" {
			t.Fatalf("pin ledger verification/head mismatch: %#v published=%#v", entry, publishedShard)
		}
	}
	if entriesByRole["shard"].CID != result.ShardCID || entriesByRole["shard"].ByteHash != publishedShard.ShardSHA256 {
		t.Fatalf("shard pin ledger mismatch: %#v published=%#v", entriesByRole["shard"], publishedShard)
	}
	if entriesByRole["shard"].RowCount != int64(publishedShard.RecordCount) || entriesByRole["shard"].HighWaterMark == "" {
		t.Fatalf("shard pin ledger row/high-water metadata missing: %#v published=%#v", entriesByRole["shard"], publishedShard)
	}
	if entriesByRole["index"].CID != result.IndexCID || entriesByRole["index"].ByteHash != publishedShard.IndexSHA256 {
		t.Fatalf("index pin ledger mismatch: %#v published=%#v", entriesByRole["index"], publishedShard)
	}
	if entriesByRole["manifest"].CID != result.ManifestCID {
		t.Fatalf("manifest pin ledger mismatch: %#v result=%#v", entriesByRole["manifest"], result)
	}
	if entriesByRole["pnm"].CID != result.PNMCID {
		t.Fatalf("pnm pin ledger mismatch: %#v result=%#v", entriesByRole["pnm"], result)
	}
	if carEntry := entriesByRole["shard-group-car"]; carEntry.CID == "" || carEntry.ByteHash == "" || carEntry.ByteCount <= 0 || carEntry.Head != publishedShard.FeedHead {
		t.Fatalf("shard-group CAR pin ledger mismatch: %#v published=%#v", carEntry, publishedShard)
	}
}

func TestConcreteDatasetPublicationServicePublishesRegisteredDatastoreNamespace(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	dir := t.TempDir()
	storeBase := filepath.Join(dir, "store")
	rootStore, err := storage.NewFlatSQLStore(storeBase, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer rootStore.Close()

	identity := storage.DatastoreIdentity{
		SchemaName:    "CAT.fbs",
		SourcePeerID:  "source:legacy-sqlite",
		ProviderID:    "space-data-network-02",
		SourceName:    "celestrak-cat-historical",
		BatchHead:     "historical-head",
		QueryProfile:  storage.DatasetPublicationQueryProfile,
		SnapshotID:    "historical-head",
		HighWaterMark: "historical-head",
		ArtifactHash:  "historical-head",
	}
	datastoreKey, err := identity.Key()
	if err != nil {
		t.Fatalf("identity key failed: %v", err)
	}
	namespaceStore, err := storage.NewFlatSQLStoreForIdentity(storeBase, validator, identity)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreForIdentity failed: %v", err)
	}
	recordA := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	recordB := sds.NewCATBuilder().
		WithNoradCatID(1).
		WithObjectName("SPUTNIK 1").
		WithObjectID("1957-001A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("DECAYED").
		Build()
	if _, err := namespaceStore.StoreBatch("CAT.fbs", [][]byte{recordA, recordB}, "source:legacy-sqlite", nil); err != nil {
		t.Fatalf("StoreBatch failed: %v", err)
	}
	if err := namespaceStore.Close(); err != nil {
		t.Fatalf("close namespace store failed: %v", err)
	}

	pinned := make(map[string][]byte)
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()

	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(
		rootStore,
		publisher,
		signingKey,
		"space-data-network-02",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)

	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:       "CAT.fbs",
		DatastoreKey: datastoreKey,
		DatasetID:    "celestrak-cat-historical",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.Schema != "CAT.fbs" || result.RecordCount != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(publisher.feedHeads) != 1 {
		t.Fatalf("feed head announcements = %d, want 1", len(publisher.feedHeads))
	}
	if publisher.feedHeads[0].ProviderID != "space-data-network-02" || publisher.feedHeads[0].SourceName != "celestrak-cat-historical" || publisher.feedHeads[0].BatchID != "historical-head" {
		t.Fatalf("feed head source identity mismatch: %#v", publisher.feedHeads[0])
	}

	if rootPublications, err := rootStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	}); err != nil {
		t.Fatalf("root ListDatasetShardPublications failed: %v", err)
	} else if len(rootPublications) != 0 {
		t.Fatalf("root publication registry = %d, want 0: %#v", len(rootPublications), rootPublications)
	}

	reopenedNamespaceStore, err := rootStore.OpenRegisteredDatastore(datastoreKey)
	if err != nil {
		t.Fatalf("OpenRegisteredDatastore failed: %v", err)
	}
	defer reopenedNamespaceStore.Close()
	publishedShard, found, err := reopenedNamespaceStore.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-cat-historical",
		BatchID:      "historical-head",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        10,
		RecordCount:  2,
	})
	if err != nil {
		t.Fatalf("FindDatasetShardPublication failed: %v", err)
	}
	if !found {
		t.Fatal("namespace publication registry entry was not stored")
	}
	if publishedShard.ShardCID != result.ShardCID || publishedShard.ManifestCID != result.ManifestCID || publishedShard.PNMCID != result.PNMCID {
		t.Fatalf("namespace publication mismatch: published=%#v result=%#v", publishedShard, result)
	}
}

func TestConcreteDatasetPublicationServicePublishesFullCatalogAsDPMSeries(t *testing.T) {
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
		SourceName:   "celestrak-satcat-csv",
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	for i := 0; i < 5; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(50000 + i)).
			WithObjectName("FULL-CATALOG-TEST").
			WithObjectID("2026-001A").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:celestrak", nil, tags); err != nil {
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
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(
		store,
		publisher,
		signingKey,
		"16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)

	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:      "CAT.fbs",
		ProviderID:  "space-data-network-02",
		SourceName:  "celestrak-satcat-csv",
		BatchID:     "source-sha-001",
		DatasetID:   "celestrak-cat-full",
		FullCatalog: true,
		ChunkSize:   2,
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.RecordCount != 5 {
		t.Fatalf("RecordCount = %d, want 5", result.RecordCount)
	}
	if len(result.Publications) != 3 {
		t.Fatalf("publications = %d, want 3: %#v", len(result.Publications), result.Publications)
	}
	if len(publisher.announcements) != 3 {
		t.Fatalf("announcements = %d, want 3", len(publisher.announcements))
	}
	if len(pinned) != 10 {
		t.Fatalf("pinned object count = %d, want 3 shard/index/manifest groups plus one shard-group CAR", len(pinned))
	}
	total := 0
	for i, publication := range result.Publications {
		if publication.ManifestCID == "" || publication.ShardCID == "" || publication.IndexCID == "" || publication.PNMCID == "" {
			t.Fatalf("publication %d missing CIDs: %#v", i, publication)
		}
		if publication.RecordCount <= 0 || publication.RecordCount > 2 {
			t.Fatalf("publication %d RecordCount = %d, want 1..2", i, publication.RecordCount)
		}
		total += publication.RecordCount
		manifest := dpm.GetRootAsDPM(pinned[publication.ManifestCID], 0)
		if got, want := string(manifest.FILE_ID()), "celestrak-cat-full:CAT.fbs:source-sha-001:part-"+fmt.Sprintf("%06d", i+1); got != want {
			t.Fatalf("publication %d FILE_ID = %q, want %q", i, got, want)
		}
	}
	if total != 5 {
		t.Fatalf("series record total = %d, want 5", total)
	}
}

func TestConcreteDatasetPublicationServiceSkipsUnchangedFullCatalogShards(t *testing.T) {
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
		BatchID:      "source-sha-unchanged",
		ContentKeyID: "public",
	}
	for i := 0; i < 3; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(51000 + i)).
			WithObjectName("UNCHANGED-FULL-CATALOG-TEST").
			WithObjectID("2026-002A").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:celestrak", nil, tags); err != nil {
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
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(
		store,
		publisher,
		signingKey,
		"16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)
	baseTime := time.Date(2026, 5, 12, 22, 0, 0, 0, time.UTC)
	nowCalls := 0
	service.now = func() time.Time {
		nowCalls++
		return baseTime.Add(time.Duration(nowCalls) * time.Second)
	}

	req := DatasetPublicationRequest{
		Schema:      "CAT.fbs",
		ProviderID:  "space-data-network-02",
		SourceName:  "celestrak-gp",
		DatasetID:   "celestrak-cat-full",
		FullCatalog: true,
		ChunkSize:   2,
	}
	first, err := service.PublishDatasetUpdate(context.Background(), req)
	if err != nil {
		t.Fatalf("first PublishDatasetUpdate failed: %v", err)
	}
	if len(first.Publications) != 2 {
		t.Fatalf("first publications = %d, want 2", len(first.Publications))
	}
	pinnedAfterFirst := len(pinned)
	announcementsAfterFirst := len(publisher.announcements)
	headsAfterFirst := len(publisher.feedHeads)

	second, err := service.PublishDatasetUpdate(context.Background(), req)
	if err != nil {
		t.Fatalf("second PublishDatasetUpdate failed: %v", err)
	}
	if len(second.Publications) != len(first.Publications) {
		t.Fatalf("second publications = %d, want %d", len(second.Publications), len(first.Publications))
	}
	if len(pinned) != pinnedAfterFirst {
		t.Fatalf("second publish pinned %d objects, want unchanged %d", len(pinned), pinnedAfterFirst)
	}
	if len(publisher.announcements) != announcementsAfterFirst+len(first.Publications) {
		t.Fatalf("second publish announcements = %d, want %d re-announced reusable PNMs", len(publisher.announcements), announcementsAfterFirst+len(first.Publications))
	}
	if len(publisher.feedHeads) != headsAfterFirst+len(first.Publications) {
		t.Fatalf("second publish feed heads = %d, want %d re-announced reusable feed heads", len(publisher.feedHeads), headsAfterFirst+len(first.Publications))
	}
	for i := range first.Publications {
		if second.Publications[i].ShardCID != first.Publications[i].ShardCID ||
			second.Publications[i].IndexCID != first.Publications[i].IndexCID ||
			second.Publications[i].ManifestCID != first.Publications[i].ManifestCID ||
			second.Publications[i].PNMCID != first.Publications[i].PNMCID {
			t.Fatalf("publication %d was not reused: first=%#v second=%#v", i, first.Publications[i], second.Publications[i])
		}
	}
}

func TestConcreteDatasetPublicationServicePrunesStaleFullCatalogShards(t *testing.T) {
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
		BatchID:      "source-sha-shrinking",
		ContentKeyID: "public",
	}
	var cids []string
	for i := 0; i < 5; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(52000 + i)).
			WithObjectName("SHRINKING-FULL-CATALOG-TEST").
			WithObjectID("2026-003A").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		cid, err := store.StoreWithSourceTags("CAT.fbs", record, "source:celestrak", nil, tags)
		if err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
		cids = append(cids, cid)
	}

	pinned := make(map[string][]byte)
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()

	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(
		store,
		publisher,
		signingKey,
		"16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)

	req := DatasetPublicationRequest{
		Schema:      "CAT.fbs",
		ProviderID:  "space-data-network-02",
		SourceName:  "celestrak-gp",
		BatchID:     "source-sha-shrinking",
		DatasetID:   "celestrak-cat-full",
		FullCatalog: true,
		ChunkSize:   2,
	}
	first, err := service.PublishDatasetUpdate(context.Background(), req)
	if err != nil {
		t.Fatalf("first PublishDatasetUpdate failed: %v", err)
	}
	if len(first.Publications) != 3 {
		t.Fatalf("first publications = %d, want 3", len(first.Publications))
	}
	firstCAR := mustLatestShardGroupCAR(t, store, "CAT.fbs", "space-data-network-02", "celestrak-gp", "verified")
	if _, ok := pinned[firstCAR.CID]; !ok {
		t.Fatalf("first shard-group CAR %q was not pinned", firstCAR.CID)
	}

	for _, cid := range cids[2:] {
		if err := store.Delete("CAT.fbs", cid); err != nil {
			t.Fatalf("delete record %s failed: %v", cid, err)
		}
	}

	second, err := service.PublishDatasetUpdate(context.Background(), req)
	if err != nil {
		t.Fatalf("second PublishDatasetUpdate failed: %v", err)
	}
	if len(second.Publications) != 1 {
		t.Fatalf("second publications = %d, want 1: %#v", len(second.Publications), second.Publications)
	}

	publications, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "source-sha-shrinking",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Limit:        2,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications failed: %v", err)
	}
	if len(publications) != 1 {
		t.Fatalf("stored publications = %d, want 1: %#v", len(publications), publications)
	}
	if publications[0].Offset != 0 || publications[0].RecordCount != 2 {
		t.Fatalf("remaining publication = %#v, want offset 0 record count 2", publications[0])
	}
	currentCAR := mustLatestShardGroupCAR(t, store, "CAT.fbs", "space-data-network-02", "celestrak-gp", "verified")
	if currentCAR.CID == firstCAR.CID {
		t.Fatalf("current shard-group CAR reused stale CID %q", currentCAR.CID)
	}
	if currentCAR.Head != publications[0].FeedHead {
		t.Fatalf("current shard-group CAR head = %q, want %q", currentCAR.Head, publications[0].FeedHead)
	}
	staleCARs, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		CID:               firstCAR.CID,
		SchemaName:        "CAT.fbs",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		Role:              "shard-group-car",
		VerificationState: "stale",
	})
	if err != nil {
		t.Fatalf("ListPinLedgerEntries stale failed: %v", err)
	}
	if len(staleCARs) != 1 {
		t.Fatalf("stale shard-group CAR entries = %d, want 1 for %s: %#v", len(staleCARs), firstCAR.CID, staleCARs)
	}
	if _, ok := pinned[firstCAR.CID]; ok {
		t.Fatalf("stale shard-group CAR %q remained pinned", firstCAR.CID)
	}
}

func TestConcreteDatasetPublicationServiceDefaultsFullCatalogToLargeSyncChunks(t *testing.T) {
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
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	for i := 0; i < defaultDatasetPublicationLimit+1; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(60000 + i)).
			WithObjectName("FULL-CATALOG-DEFAULT-CHUNK").
			WithObjectID("2026-001A").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:celestrak", nil, tags); err != nil {
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
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(
		store,
		publisher,
		signingKey,
		"16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4",
		"bafy-provider-epm",
		kubo.URL,
		filepath.Join(dir, "publications"),
	)

	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema:      "CAT.fbs",
		ProviderID:  "space-data-network-02",
		SourceName:  "celestrak-gp",
		BatchID:     "source-sha-001",
		DatasetID:   "celestrak-cat-full",
		FullCatalog: true,
		Limit:       defaultDatasetPublicationLimit + 1,
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdate failed: %v", err)
	}
	if result.RecordCount != defaultDatasetPublicationLimit+1 {
		t.Fatalf("RecordCount = %d, want %d", result.RecordCount, defaultDatasetPublicationLimit+1)
	}
	if len(result.Publications) != 1 {
		t.Fatalf("publications = %d, want one large sync chunk: %#v", len(result.Publications), result.Publications)
	}
	if len(publisher.announcements) != 1 {
		t.Fatalf("announcements = %d, want one large sync chunk", len(publisher.announcements))
	}
	if len(pinned) != 4 {
		t.Fatalf("pinned object count = %d, want shard, index, manifest, and shard-group CAR", len(pinned))
	}
	publishedShard, found, err := store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "source-sha-001",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        defaultDatasetPublicationLimit + 1,
		RecordCount:  defaultDatasetPublicationLimit + 1,
	})
	if err != nil {
		t.Fatalf("FindDatasetShardPublication failed: %v", err)
	}
	if !found {
		t.Fatal("published full-catalog shard was not stored under the large sync chunk size")
	}
	if publishedShard.ShardCID != result.ShardCID {
		t.Fatalf("published shard CID = %q, want %q", publishedShard.ShardCID, result.ShardCID)
	}
}

func cidV1RawSHA256ForTest(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	hash, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		t.Fatalf("encode multihash: %v", err)
	}
	return cid.NewCidV1(cid.Raw, hash).String()
}

func mustLatestShardGroupCAR(t *testing.T, store *storage.FlatSQLStore, schema, providerID, sourceName, verificationState string) storage.PinLedgerEntry {
	t.Helper()
	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:        schema,
		ProviderID:        providerID,
		SourceName:        sourceName,
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		Role:              "shard-group-car",
		VerificationState: verificationState,
	})
	if err != nil {
		t.Fatalf("ListPinLedgerEntries failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no %s shard-group CAR entries found for %s/%s/%s", verificationState, schema, providerID, sourceName)
	}
	return entries[0]
}

func newDatasetPublicationKuboTestServer(t *testing.T, pinned map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var responseKey string
		var wantField string
		switch r.URL.Path {
		case "/api/v0/add":
			responseKey = "Hash"
			wantField = "file"
			if r.URL.Query().Get("pin") != "true" || r.URL.Query().Get("cid-version") != "1" || r.URL.Query().Get("raw-leaves") != "true" || r.URL.Query().Get("hash") != "sha2-256" {
				t.Fatalf("unexpected IPFS add query: %s", r.URL.RawQuery)
			}
		case "/api/v0/block/put":
			responseKey = "Key"
			wantField = "data"
			if r.URL.Query().Get("pin") != "true" || r.URL.Query().Get("format") != "raw" {
				t.Fatalf("unexpected IPFS block put query: %s", r.URL.RawQuery)
			}
		case "/api/v0/dag/export":
			rootCID := r.URL.Query().Get("arg")
			body, ok := pinned[rootCID]
			if !ok {
				t.Fatalf("dag/export root %q was not pinned", rootCID)
			}
			decoded, err := cid.Decode(rootCID)
			if err != nil {
				t.Fatalf("decode dag/export root CID: %v", err)
			}
			w.Header().Set("Content-Type", "application/vnd.ipld.car")
			writeSingleBlockCARForTest(t, w, decoded, body)
			return
		case "/api/v0/pin/rm":
			cidValue := r.URL.Query().Get("arg")
			delete(pinned, cidValue)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Pins":["` + cidValue + `"]}` + "\n"))
			return
		default:
			t.Fatalf("unexpected IPFS path: %s", r.URL.Path)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		if part.FormName() != wantField {
			t.Fatalf("multipart field = %q, want %s", part.FormName(), wantField)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		cidValue := cidV1RawSHA256ForTest(t, body)
		pinned[cidValue] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"` + responseKey + `":"` + cidValue + `"}` + "\n"))
	}))
}

func writeSingleBlockCARForTest(t *testing.T, w io.Writer, root cid.Cid, body []byte) {
	t.Helper()
	writer, err := carstorage.NewWritable(w, []cid.Cid{root}, car.WriteAsCarV1(true))
	if err != nil {
		t.Fatalf("create CAR writer: %v", err)
	}
	if err := writer.Put(context.Background(), root.KeyString(), body); err != nil {
		t.Fatalf("write CAR block: %v", err)
	}
	if err := writer.Finalize(); err != nil {
		t.Fatalf("finalize CAR: %v", err)
	}
}
