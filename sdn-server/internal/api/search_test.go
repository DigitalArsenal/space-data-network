package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestSearchProvidersRouteMergesDirectoryAndReplicaStats(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")
	verifiedAt := time.Unix(1_778_436_120, 0).UTC()
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    "16Uiu2HCatalogFixture",
		DN:        "CatalogFixture Provider",
		LegalName: "CatalogFixture",
		Source:    "test",
		EPMJSON: `{
			"aliases": ["catalogfixture.eth"],
			"provider_id": "space-data-network-02"
		}`,
		UpdatedAt: 1_779_689_334,
	}); err != nil {
		t.Fatalf("UpsertDirectoryRecord failed: %v", err)
	}
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafkshard-omm",
		SchemaName:        "OMM.fbs",
		ProviderPeerID:    "16Uiu2HCatalogFixture",
		ProviderPublicKey: "provider-public-key",
		ProviderID:        "space-data-network-02",
		SourceName:        "catalogfixture-gp",
		BatchID:           "test-batch",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		SnapshotID:        "head-2",
		Head:              "head-2",
		HighWaterMark:     "published-feed-v1:1778436120:1:50000:8000000",
		ByteHash:          "sha256:shard",
		Role:              "shard",
		RowCount:          50000,
		ByteCount:         8_000_000,
		VerificationState: "verified",
		VerifiedAt:        verifiedAt,
	}); err != nil {
		t.Fatalf("UpsertPinLedgerEntry failed: %v", err)
	}

	mux := http.NewServeMux()
	NewSearchHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/providers", bytes.NewBufferString(`{
		"query": "catalogfixture",
		"schema": "OMM",
		"provider_id": "space-data-network-02",
		"source_name": "catalogfixture-gp",
		"query_profile": "`+storage.DatasetPublicationQueryProfile+`",
		"limit": 10
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count   int                      `json:"count"`
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search response: %v", err)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("provider search count=%d len(results)=%d body=%s", body.Count, len(body.Results), rec.Body.String())
	}
	row := body.Results[0]
	if row["peer_id"] != "16Uiu2HCatalogFixture" ||
		row["dn"] != "CatalogFixture Provider" ||
		row["provider_id"] != "space-data-network-02" ||
		row["schema_name"] != "OMM.fbs" ||
		row["source_name"] != "catalogfixture-gp" {
		t.Fatalf("unexpected provider row: %#v", row)
	}
	if row["local_rows"] != float64(1) || row["pinned_rows"] != float64(50000) || row["pinned_bytes"] != float64(8_000_000) {
		t.Fatalf("unexpected provider row counts: %#v", row)
	}
}

func TestSearchDataRouteReturnsLocalReplicaRows(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")

	mux := http.NewServeMux()
	NewSearchHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/data", bytes.NewBufferString(`{
		"schema": "OMM",
		"provider_id": "space-data-network-02",
		"source_name": "catalogfixture-gp"
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count   int                      `json:"count"`
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode data search response: %v", err)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("data search count=%d len(results)=%d body=%s", body.Count, len(body.Results), rec.Body.String())
	}
	row := body.Results[0]
	if row["schema_name"] != "OMM.fbs" || row["provider_id"] != "space-data-network-02" || row["source_name"] != "catalogfixture-gp" {
		t.Fatalf("unexpected data row: %#v", row)
	}
	if row["local_rows"] != float64(1) || row["cached_bytes"] == float64(0) {
		t.Fatalf("unexpected data row counts: %#v", row)
	}
}

func TestSearchProvidersLiveDHTModeUsesLiveBackend(t *testing.T) {
	store := newDataAPITestStore(t)
	live := &fakeLiveSearchBackend{
		providerRows: []map[string]interface{}{{
			"peer_id":     "16Uiu2HLiveCatalogFixture",
			"dn":          "Live CatalogFixture",
			"provider_id": "space-data-network-02",
			"schema_name": "OMM.fbs",
			"source_name": "catalogfixture-gp",
		}},
	}

	mux := http.NewServeMux()
	NewSearchHandlerWithOptions(store, SearchHandlerOptions{LiveBackend: live}).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/providers", bytes.NewBufferString(`{
		"mode": "live-dht",
		"query": "catalogfixture",
		"schema": "OMM",
		"provider_id": "space-data-network-02",
		"limit": 10
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if live.providerCalls != 1 || live.dataCalls != 0 {
		t.Fatalf("live backend calls provider=%d data=%d", live.providerCalls, live.dataCalls)
	}
	if live.lastProviderRequest.Mode != "live-dht" ||
		live.lastProviderRequest.Schema != "OMM" ||
		live.lastProviderRequest.ProviderID != "space-data-network-02" ||
		live.lastProviderRequest.Limit != 10 {
		t.Fatalf("live provider request = %#v", live.lastProviderRequest)
	}

	var body struct {
		Count   int                      `json:"count"`
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search response: %v", err)
	}
	if body.Count != 1 || body.Results[0]["peer_id"] != "16Uiu2HLiveCatalogFixture" {
		t.Fatalf("unexpected live provider response: %#v", body)
	}
}

func TestSearchDataLiveDHTModeRequiresLiveBackend(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "LOCAL-ROW-SHOULD-NOT-BE-USED", "2026-05-10")

	mux := http.NewServeMux()
	NewSearchHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/data", bytes.NewBufferString(`{
		"mode": "live-dht",
		"schema": "OMM"
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("live DHT search backend is unavailable")) {
		t.Fatalf("live-dht unavailable response did not explain backend state: %s", rec.Body.String())
	}
}

func TestSearchRoutesRejectNonPost(t *testing.T) {
	mux := http.NewServeMux()
	NewSearchHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/search/providers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

type fakeLiveSearchBackend struct {
	providerRows        []map[string]interface{}
	dataRows            []map[string]interface{}
	providerCalls       int
	dataCalls           int
	lastProviderRequest SearchRequest
	lastDataRequest     SearchRequest
}

func (f *fakeLiveSearchBackend) SearchProviders(ctx context.Context, req SearchRequest) ([]map[string]interface{}, error) {
	f.providerCalls++
	f.lastProviderRequest = req
	return f.providerRows, nil
}

func (f *fakeLiveSearchBackend) SearchData(ctx context.Context, req SearchRequest) ([]map[string]interface{}, error) {
	f.dataCalls++
	f.lastDataRequest = req
	return f.dataRows, nil
}
