package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/ipfs/go-cid"
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

type fakeDatasetUpdatePublisher struct {
	announcement  sdnpubsub.DatasetUpdateAnnouncement
	announcements []sdnpubsub.DatasetUpdateAnnouncement
	called        bool
}

func (f *fakeDatasetUpdatePublisher) PublishDatasetUpdatePNM(ctx context.Context, ann sdnpubsub.DatasetUpdateAnnouncement) error {
	f.called = true
	f.announcement = ann
	f.announcements = append(f.announcements, ann)
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
	kubo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/block/put" {
			t.Fatalf("unexpected IPFS path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("pin") != "true" || r.URL.Query().Get("format") != "raw" {
			t.Fatalf("unexpected IPFS query: %s", r.URL.RawQuery)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		if part.FormName() != "data" {
			t.Fatalf("multipart field = %q, want data", part.FormName())
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		cidValue := cidV1RawSHA256ForTest(t, body)
		pinned[cidValue] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Key":"` + cidValue + `"}`))
	}))
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
	if len(pinned) != 3 {
		t.Fatalf("pinned object count = %d, want 3", len(pinned))
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
	kubo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		cidValue := cidV1RawSHA256ForTest(t, body)
		pinned[cidValue] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Key":"` + cidValue + `"}`))
	}))
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
	if len(pinned) != 9 {
		t.Fatalf("pinned object count = %d, want 9", len(pinned))
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
	kubo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		cidValue := cidV1RawSHA256ForTest(t, body)
		pinned[cidValue] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Key":"` + cidValue + `"}`))
	}))
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
	if len(pinned) != 3 {
		t.Fatalf("pinned object count = %d, want 3", len(pinned))
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
