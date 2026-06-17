package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestOMMBulkReturnsFullCatalogFlatBufferStream(t *testing.T) {
	store := newDataAPITestStore(t)
	day := "2026-05-05"
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", day)
	storeDataAPITestOMM(t, store, 40909, "STARLINK-1001", day)

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/omm/bulk?limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-SDN-Schema"); got != "OMM.fbs" {
		t.Fatalf("X-SDN-Schema = %q, want OMM.fbs", got)
	}
	if got := rec.Header().Get("X-SDN-Stream-Format"); got != "flatsql-size-prefixed-le-u32" {
		t.Fatalf("X-SDN-Stream-Format = %q", got)
	}
	if got := rec.Header().Get("X-SDN-Record-Count"); got != "2" {
		t.Fatalf("X-SDN-Record-Count = %q, want 2", got)
	}

	records := readLengthPrefixedRecords(t, rec.Body.Bytes())
	if len(records) != 2 {
		t.Fatalf("stream record count = %d, want 2", len(records))
	}
}

func TestOMMBulkReturnsLatestCelestrakCatalogBatch(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMMWithBatch(t, store, 25544, "ISS (ZARYA)", "2026-05-10", "old-batch")
	storeDataAPITestOMMWithBatch(t, store, 40909, "STARLINK-1001", "2026-05-10", "old-batch")
	storeDataAPITestOMMWithBatch(t, store, 25544, "ISS (ZARYA)", "2026-05-15", "new-batch")
	storeDataAPITestOMMWithBatch(t, store, 40909, "STARLINK-1001", "2026-05-15", "new-batch")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/omm/bulk?limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-SDN-Record-Count"); got != "2" {
		t.Fatalf("X-SDN-Record-Count = %q, want latest catalog batch count 2", got)
	}
	records := readLengthPrefixedRecords(t, rec.Body.Bytes())
	if len(records) != 2 {
		t.Fatalf("stream record count = %d, want latest catalog batch count 2", len(records))
	}
	for _, payload := range records {
		omm := OMM.GetSizePrefixedRootAsOMM(payload, 0)
		if got := string(omm.EPOCH()); got != "2026-05-15T12:00:00Z" {
			t.Fatalf("bulk OMM epoch = %q, want latest batch epoch", got)
		}
	}
}

func TestOMMBulkJSONIncludesDataForFullCatalogConsumers(t *testing.T) {
	store := newDataAPITestStore(t)
	day := "2026-05-05"
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", day)
	storeDataAPITestOMM(t, store, 40909, "STARLINK-1001", day)

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/omm/bulk?format=json&include_data=true", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema  string                   `json:"schema"`
		Count   int                      `json:"count"`
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "OMM.fbs" {
		t.Fatalf("schema = %q, want OMM.fbs", body.Schema)
	}
	if body.Count != 2 || len(body.Results) != 2 {
		t.Fatalf("count=%d len(results)=%d, want 2", body.Count, len(body.Results))
	}
	for _, row := range body.Results {
		if row["data_base64"] == "" {
			t.Fatalf("row missing data_base64: %#v", row)
		}
		if row["materialized_at"] == "" {
			t.Fatalf("row missing materialized_at: %#v", row)
		}
		if row["source_name"] != "celestrak-gp" {
			t.Fatalf("row source_name = %#v, want celestrak-gp", row["source_name"])
		}
	}
}

func TestSPWBulkReturnsSpaceWeatherFlatBufferStream(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestSPW(t, store, "2026-05-05")
	storeDataAPITestSPW(t, store, "2026-05-06")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/spw/bulk?limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-SDN-Schema"); got != "SPW.fbs" {
		t.Fatalf("X-SDN-Schema = %q, want SPW.fbs", got)
	}
	if got := rec.Header().Get("X-SDN-Stream-Format"); got != "flatsql-size-prefixed-le-u32" {
		t.Fatalf("X-SDN-Stream-Format = %q", got)
	}
	if got := rec.Header().Get("X-SDN-Record-Count"); got != "2" {
		t.Fatalf("X-SDN-Record-Count = %q, want 2", got)
	}
}

func TestSPWBulkJSONIncludesSpaceWeatherFreshness(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestSPW(t, store, "2026-05-05")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/spw/bulk?format=json&include_data=true", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema  string                   `json:"schema"`
		Count   int                      `json:"count"`
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "SPW.fbs" {
		t.Fatalf("schema = %q, want SPW.fbs", body.Schema)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("count=%d len(results)=%d, want 1", body.Count, len(body.Results))
	}
	row := body.Results[0]
	if row["date"] != "2026-05-05" {
		t.Fatalf("row date = %#v, want 2026-05-05", row["date"])
	}
	if row["source_name"] != "celestrak-space-weather" {
		t.Fatalf("row source_name = %#v, want celestrak-space-weather", row["source_name"])
	}
	if row["materialized_at"] == "" {
		t.Fatalf("row missing materialized_at: %#v", row)
	}
	if row["data_base64"] == "" {
		t.Fatalf("row missing data_base64: %#v", row)
	}
}

func TestDataEpochQueryReturnsMatchQuality(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 25544, "ISS-BACKFILL", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS-FORWARD", "2026-05-12")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/epoch?schema=OMM.fbs&profile=epoch.nearest&at=2026-05-11T12:00:00Z&norad_cat_id=25544&provider_id=space-data-network-02&source_name=celestrak-gp&limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema  string `json:"schema"`
		Profile string `json:"profile"`
		Count   int    `json:"count"`
		Results []struct {
			EntityKey    string `json:"entity_key"`
			NoradCatID   uint32 `json:"norad_cat_id"`
			ObjectName   string `json:"object_name"`
			MatchQuality struct {
				RequestedEpoch string `json:"requested_epoch"`
				MatchedEpoch   string `json:"matched_epoch"`
				DeltaSeconds   int64  `json:"delta_seconds"`
				MatchType      string `json:"match_type"`
			} `json:"match_quality"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "OMM.fbs" || body.Profile != "epoch.nearest" || body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("unexpected epoch response: %#v", body)
	}
	result := body.Results[0]
	if result.EntityKey != "25544" || result.NoradCatID != 25544 || result.ObjectName != "ISS-BACKFILL" {
		t.Fatalf("unexpected epoch result identity: %#v", result)
	}
	if result.MatchQuality.RequestedEpoch != "2026-05-11T12:00:00Z" ||
		result.MatchQuality.MatchedEpoch != "2026-05-10T12:00:00Z" ||
		result.MatchQuality.DeltaSeconds != 86400 ||
		result.MatchQuality.MatchType != "nearest" {
		t.Fatalf("unexpected match quality: %#v", result.MatchQuality)
	}
}

func TestDataEpochQueryReportsTotalCountBeyondReturnedPage(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 40909, "DAY-A", "2026-05-11")
	storeDataAPITestOMM(t, store, 41000, "DAY-B", "2026-05-11")
	storeDataAPITestOMM(t, store, 50000, "OTHER-DAY", "2026-05-12")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/epoch?schema=OMM.fbs&profile=epoch.day&day=2026-05-11&provider_id=space-data-network-02&source_name=celestrak-gp&limit=1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema     string                   `json:"schema"`
		Profile    string                   `json:"profile"`
		Count      int                      `json:"count"`
		TotalCount int64                    `json:"total_count"`
		Results    []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "OMM.fbs" || body.Profile != "epoch.day" {
		t.Fatalf("unexpected epoch response identity: %#v", body)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("page count=%d len(results)=%d, want 1", body.Count, len(body.Results))
	}
	if body.TotalCount != 2 {
		t.Fatalf("total_count = %d, want 2", body.TotalCount)
	}
}

func TestDataSummaryGroupsBySchemaAndProducer(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-05")
	localEPM := sds.NewEPMBuilder().
		WithDN("Local Node").
		WithEmail("local@example.test").
		WithMultiAddrs([]string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLocalEPM"}).
		Build()
	if err := store.SaveLocalEPM("12D3KooWLocalEPM", localEPM); err != nil {
		t.Fatalf("SaveLocalEPM failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/summary", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		TotalRecords int                   `json:"total_records"`
		Schemas      []apiSchemaSummaryRow `json:"schemas"`
		Sources      []apiSourceSummaryRow `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.TotalRecords < 2 {
		t.Fatalf("total_records = %d, want at least 2", body.TotalRecords)
	}
	if got := apiSchemaCount(body.Schemas, "OMM.fbs"); got != 1 {
		t.Fatalf("OMM schema count = %d, want 1", got)
	}
	if got := apiSchemaCount(body.Schemas, "EPM.fbs"); got != 1 {
		t.Fatalf("EPM schema count = %d, want local EPM count 1", got)
	}
	if got := apiSourceCount(body.Sources, "OMM.fbs", "space-data-network-02", "celestrak-gp"); got != 1 {
		t.Fatalf("OMM celestrak source count = %d, want 1", got)
	}
	if got := apiSourceCount(body.Sources, "EPM.fbs", "local-node", "local-epm"); got != 1 {
		t.Fatalf("local EPM source count = %d, want 1", got)
	}
}

func TestDataDatastoresListsRegisteredNamespaces(t *testing.T) {
	store, basePath, validator := newDataAPITestStoreWithBasePath(t)
	identity := storage.DatastoreIdentity{
		SchemaName:    "OMM.fbs",
		SourcePeerID:  "source:legacy-sqlite",
		ProviderID:    "space-data-network-02",
		SourceName:    "celestrak-gp-historical",
		BatchHead:     "historical-head",
		QueryProfile:  storage.DatasetPublicationQueryProfile,
		SnapshotID:    "historical-head",
		HighWaterMark: "historical-head",
		ArtifactHash:  "historical-head",
	}
	namespaceStore, err := storage.NewFlatSQLStoreForIdentity(basePath, validator, identity)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreForIdentity failed: %v", err)
	}
	if err := namespaceStore.Close(); err != nil {
		t.Fatalf("close namespace store failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/datastores", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count   int `json:"count"`
		Results []struct {
			Key      string `json:"key"`
			Identity struct {
				SchemaName   string `json:"schema_name"`
				ProviderID   string `json:"provider_id"`
				SourceName   string `json:"source_name"`
				QueryProfile string `json:"query_profile"`
			} `json:"identity"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("count=%d len(results)=%d, want 1", body.Count, len(body.Results))
	}
	got := body.Results[0]
	if got.Key == "" {
		t.Fatal("datastore key is empty")
	}
	if got.Identity.SchemaName != "OMM.fbs" || got.Identity.ProviderID != "space-data-network-02" || got.Identity.SourceName != "celestrak-gp-historical" || got.Identity.QueryProfile != storage.DatasetPublicationQueryProfile {
		t.Fatalf("unexpected datastore identity: %#v", got.Identity)
	}
}

func TestDataQueryReturnsRawFlatBufferRowsForEPM(t *testing.T) {
	store := newDataAPITestStore(t)
	epmBytes := sds.NewEPMBuilder().
		WithDN("Local Query Node").
		WithEmail("query@example.test").
		WithMultiAddrs([]string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWQueryEPM"}).
		Build()
	if err := store.SaveLocalEPM("12D3KooWQueryEPM", epmBytes); err != nil {
		t.Fatalf("SaveLocalEPM failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"EPM.fbs","provider_id":"local-node","source_name":"local-epm","limit":10,"include_data":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema  string `json:"schema"`
		Count   int    `json:"count"`
		Results []struct {
			CID        string `json:"cid"`
			SchemaName string `json:"schema_name"`
			PeerID     string `json:"peer_id"`
			ProviderID string `json:"provider_id"`
			SourceName string `json:"source_name"`
			SizeBytes  int    `json:"size_bytes"`
			DataBase64 string `json:"data_base64"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "EPM.fbs" || body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("unexpected query response: %#v", body)
	}
	row := body.Results[0]
	if row.SchemaName != "EPM.fbs" || row.PeerID != "12D3KooWQueryEPM" || row.ProviderID != "local-node" || row.SourceName != "local-epm" {
		t.Fatalf("unexpected row metadata: %#v", row)
	}
	raw, err := base64.StdEncoding.DecodeString(row.DataBase64)
	if err != nil {
		t.Fatalf("data_base64 decode failed: %v", err)
	}
	if string(raw) != string(epmBytes) {
		t.Fatal("query did not return original EPM FlatBuffer bytes")
	}
	if row.SizeBytes != len(epmBytes) {
		t.Fatalf("size_bytes = %d, want %d", row.SizeBytes, len(epmBytes))
	}
}

func TestDataQueryStreamsRawFlatBuffersWithoutBase64(t *testing.T) {
	store := newDataAPITestStore(t)
	payload := storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"celestrak-gp","limit":10}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
		t.Fatalf("Content-Type = %q, want raw FlatBuffer stream", got)
	}
	if got := rec.Header().Get("X-SDN-Schema"); got != "OMM.fbs" {
		t.Fatalf("X-SDN-Schema = %q, want OMM.fbs", got)
	}
	if got := rec.Header().Get("X-SDN-Stream-Format"); got != "flatsql-size-prefixed-le-u32" {
		t.Fatalf("X-SDN-Stream-Format = %q", got)
	}
	records := readLengthPrefixedRecords(t, rec.Body.Bytes())
	if len(records) != 1 {
		t.Fatalf("stream record count = %d, want 1", len(records))
	}
	if string(records[0]) != string(payload) {
		t.Fatal("stream did not return original raw OMM FlatBuffer bytes")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("data_base64")) {
		t.Fatal("raw FlatBuffer stream must not contain JSON/base64 fields")
	}
}

func TestDataQueryAppliesSubscriptionSyncFilter(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")
	wantedPayload := storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-11")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"celestrak-gp","sync_filter":"NORAD_CAT_ID = 25544","limit":10,"include_data":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count   int `json:"count"`
		Results []struct {
			DataBase64 string `json:"data_base64"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("unexpected filtered query response: %+v", body)
	}
	raw, err := base64.StdEncoding.DecodeString(body.Results[0].DataBase64)
	if err != nil {
		t.Fatalf("decode data_base64 failed: %v", err)
	}
	if string(raw) != string(wantedPayload) {
		t.Fatal("filtered query did not return the matching raw OMM FlatBuffer")
	}
}

func TestDataScanReturnsFilteredTotalAndHashBoundRefs(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-10")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"celestrak-gp","limit":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema        string `json:"schema"`
		TotalCount    int64  `json:"total_count"`
		Count         int    `json:"count"`
		Limit         int    `json:"limit"`
		Cursor        string `json:"cursor"`
		NextCursor    string `json:"next_cursor"`
		SnapshotID    string `json:"snapshot_id"`
		Head          string `json:"head"`
		HighWaterMark string `json:"high_water_mark"`
		ScanHash      string `json:"scan_hash"`
		ChunkHash     string `json:"chunk_hash"`
		SyncProtocol  string `json:"sync_protocol"`
		MaxChunkSize  int    `json:"max_chunk_size"`
		Results       []struct {
			SchemaName string `json:"schema_name"`
			CID        string `json:"cid"`
			ProviderID string `json:"provider_id"`
			SourceName string `json:"source_name"`
			SizeBytes  int    `json:"size_bytes"`
			DataBase64 string `json:"data_base64"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "OMM.fbs" || body.TotalCount != 2 || body.Count != 1 || body.Limit != 1 {
		t.Fatalf("unexpected scan response: %+v", body)
	}
	if body.NextCursor == "" {
		t.Fatalf("next_cursor is empty for a partial scan: %+v", body)
	}
	if body.Cursor == "" {
		t.Fatalf("cursor is empty: %+v", body)
	}
	if body.SnapshotID == "" || body.Head == "" || body.SnapshotID != body.Head {
		t.Fatalf("snapshot/head metadata not populated consistently: %+v", body)
	}
	if body.HighWaterMark == "" {
		t.Fatalf("high_water_mark is empty: %+v", body)
	}
	if body.ScanHash == "" {
		t.Fatalf("scan_hash is empty: %+v", body)
	}
	if body.ChunkHash == "" || body.ChunkHash != body.ScanHash {
		t.Fatalf("chunk_hash = %q, scan_hash = %q", body.ChunkHash, body.ScanHash)
	}
	if body.SyncProtocol != "/space-data-network/flatsql-sync/1.0.0" {
		t.Fatalf("sync_protocol = %q", body.SyncProtocol)
	}
	if body.MaxChunkSize < 1105 {
		t.Fatalf("max_chunk_size = %d, want at least large sync chunks", body.MaxChunkSize)
	}
	if len(body.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(body.Results))
	}
	row := body.Results[0]
	if row.SchemaName != "OMM.fbs" || row.ProviderID != "space-data-network-02" || row.SourceName != "celestrak-gp" {
		t.Fatalf("unexpected row metadata: %+v", row)
	}
	if row.SizeBytes == 0 {
		t.Fatalf("size_bytes was not populated: %+v", row)
	}
	if row.DataBase64 != "" {
		t.Fatalf("scan refs must not inline base64 payloads: %+v", row)
	}
}

func TestDataScanAppliesSubscriptionSyncFilter(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-11")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"celestrak-gp","sync_filter":"NORAD_CAT_ID = 25544","limit":10}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		TotalCount int `json:"total_count"`
		Count      int `json:"count"`
		Results    []struct {
			CID string `json:"cid"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.TotalCount != 1 || body.Count != 1 || len(body.Results) != 1 || body.Results[0].CID == "" {
		t.Fatalf("unexpected filtered scan response: %+v", body)
	}
}

func TestDataScanCanReadRegisteredDatastoreNamespace(t *testing.T) {
	store, basePath, validator := newDataAPITestStoreWithBasePath(t)
	identity := storage.DatastoreIdentity{
		SchemaName:    "OMM.fbs",
		SourcePeerID:  "source:legacy-sqlite",
		ProviderID:    "space-data-network-02",
		SourceName:    "celestrak-gp-historical",
		BatchHead:     "historical-head",
		QueryProfile:  storage.DatasetPublicationQueryProfile,
		SnapshotID:    "historical-head",
		HighWaterMark: "historical-head",
		ArtifactHash:  "historical-head",
	}
	key, err := identity.Key()
	if err != nil {
		t.Fatalf("identity key failed: %v", err)
	}
	namespaceStore, err := storage.NewFlatSQLStoreForIdentity(basePath, validator, identity)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreForIdentity failed: %v", err)
	}
	payload := sds.NewOMMBuilder().
		WithNoradCatID(1).
		WithObjectName("SPUTNIK 1").
		WithEpoch("1959-01-11T01:49:23Z").
		Build()
	if _, err := namespaceStore.StoreBatch("OMM.fbs", [][]byte{payload}, "source:legacy-sqlite", nil); err != nil {
		t.Fatalf("store namespace OMM failed: %v", err)
	}
	if err := namespaceStore.Close(); err != nil {
		t.Fatalf("close namespace store failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","datastore_key":"`+key+`","limit":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Schema     string `json:"schema"`
		TotalCount int64  `json:"total_count"`
		Count      int    `json:"count"`
		Results    []struct {
			SchemaName string `json:"schema_name"`
			CID        string `json:"cid"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Schema != "OMM.fbs" || body.TotalCount != 1 || body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("unexpected namespace scan response: %+v", body)
	}
}

func TestDataLocalReplicaStatsReportsRowsPinsAndHead(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")
	verifiedAt := time.Unix(1_778_436_120, 0).UTC()
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafkshard-omm",
		SchemaName:        "OMM.fbs",
		ProviderPeerID:    "16Uiu2HCelesTrak",
		ProviderPublicKey: "provider-public-key",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
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
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/local-replica-stats?schema=OMM.fbs&provider_id=space-data-network-02&source_name=celestrak-gp&batch_id=test-batch&query_profile="+storage.DatasetPublicationQueryProfile, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count   int `json:"count"`
		Results []struct {
			SchemaName        string `json:"schema_name"`
			ProviderPeerID    string `json:"provider_peer_id"`
			ProviderPublicKey string `json:"provider_public_key"`
			ProviderID        string `json:"provider_id"`
			SourceName        string `json:"source_name"`
			BatchID           string `json:"batch_id"`
			QueryProfile      string `json:"query_profile"`
			LocalRows         int64  `json:"local_rows"`
			PinnedRows        int64  `json:"pinned_rows"`
			CachedBytes       int64  `json:"cached_bytes"`
			PinnedBytes       int64  `json:"pinned_bytes"`
			SnapshotID        string `json:"snapshot_id"`
			Head              string `json:"head"`
			HighWaterMark     string `json:"high_water_mark"`
			LastSyncedAt      string `json:"last_synced_at"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("stats count=%d len(results)=%d body=%s", body.Count, len(body.Results), rec.Body.String())
	}
	got := body.Results[0]
	if got.SchemaName != "OMM.fbs" || got.ProviderID != "space-data-network-02" || got.SourceName != "celestrak-gp" || got.BatchID != "test-batch" {
		t.Fatalf("unexpected source identity: %#v", got)
	}
	if got.ProviderPeerID != "16Uiu2HCelesTrak" || got.ProviderPublicKey != "provider-public-key" {
		t.Fatalf("unexpected producer identity: %#v", got)
	}
	if got.LocalRows != 1 || got.PinnedRows != 50000 || got.PinnedBytes != 8_000_000 || got.CachedBytes <= 0 {
		t.Fatalf("unexpected local replica stats: %#v", got)
	}
	if got.Head != "head-2" || got.SnapshotID != "head-2" || got.HighWaterMark != "published-feed-v1:1778436120:1:50000:8000000" {
		t.Fatalf("unexpected head stats: %#v", got)
	}
	if got.LastSyncedAt == "" {
		t.Fatalf("last_synced_at is empty: %#v", got)
	}
}

func TestDataStreamEchoesResumableChunkMetadata(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	scanReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"celestrak-gp","limit":1}`))
	scanReq.Header.Set("Content-Type", "application/json")
	scanRec := httptest.NewRecorder()
	mux.ServeHTTP(scanRec, scanReq)
	if scanRec.Code != http.StatusOK {
		t.Fatalf("scan status = %d, body=%s", scanRec.Code, scanRec.Body.String())
	}
	var scanBody struct {
		Schema        string                   `json:"schema"`
		ScanHash      string                   `json:"scan_hash"`
		ChunkHash     string                   `json:"chunk_hash"`
		SnapshotID    string                   `json:"snapshot_id"`
		Head          string                   `json:"head"`
		Cursor        string                   `json:"cursor"`
		NextCursor    string                   `json:"next_cursor"`
		TotalCount    int64                    `json:"total_count"`
		HighWaterMark string                   `json:"high_water_mark"`
		Results       []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(scanRec.Body.Bytes(), &scanBody); err != nil {
		t.Fatalf("decode scan failed: %v", err)
	}
	streamBody, err := json.Marshal(map[string]interface{}{
		"schema":          scanBody.Schema,
		"scan_hash":       scanBody.ScanHash,
		"chunk_hash":      scanBody.ChunkHash,
		"snapshot_id":     scanBody.SnapshotID,
		"head":            scanBody.Head,
		"cursor":          scanBody.Cursor,
		"next_cursor":     scanBody.NextCursor,
		"total_count":     scanBody.TotalCount,
		"high_water_mark": scanBody.HighWaterMark,
		"records":         scanBody.Results,
	})
	if err != nil {
		t.Fatalf("marshal stream request failed: %v", err)
	}

	streamReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/stream", bytes.NewReader(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamReq.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)

	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body=%s", streamRec.Code, streamRec.Body.String())
	}
	headers := streamRec.Header()
	if headers.Get("X-SDN-Sync-Protocol") != "/space-data-network/flatsql-sync/1.0.0" {
		t.Fatalf("X-SDN-Sync-Protocol = %q", headers.Get("X-SDN-Sync-Protocol"))
	}
	if headers.Get("X-SDN-Snapshot-ID") != scanBody.SnapshotID {
		t.Fatalf("X-SDN-Snapshot-ID = %q, want %q", headers.Get("X-SDN-Snapshot-ID"), scanBody.SnapshotID)
	}
	if headers.Get("X-SDN-Head") != scanBody.Head {
		t.Fatalf("X-SDN-Head = %q, want %q", headers.Get("X-SDN-Head"), scanBody.Head)
	}
	if headers.Get("X-SDN-Cursor") != scanBody.Cursor {
		t.Fatalf("X-SDN-Cursor = %q, want %q", headers.Get("X-SDN-Cursor"), scanBody.Cursor)
	}
	if headers.Get("X-SDN-Chunk-Hash") != scanBody.ChunkHash {
		t.Fatalf("X-SDN-Chunk-Hash = %q, want %q", headers.Get("X-SDN-Chunk-Hash"), scanBody.ChunkHash)
	}
	if headers.Get("X-SDN-Total-Count") != "1" {
		t.Fatalf("X-SDN-Total-Count = %q", headers.Get("X-SDN-Total-Count"))
	}
	if headers.Get("X-SDN-High-Water-Mark") != scanBody.HighWaterMark {
		t.Fatalf("X-SDN-High-Water-Mark = %q, want %q", headers.Get("X-SDN-High-Water-Mark"), scanBody.HighWaterMark)
	}
	if records := readLengthPrefixedRecords(t, streamRec.Body.Bytes()); len(records) != 1 {
		t.Fatalf("stream record count = %d, want 1", len(records))
	}
}

func TestDataScanAllowsLargeOrderedChunks(t *testing.T) {
	store := newDataAPITestStore(t)
	for index := 0; index < 1105; index++ {
		storeDataAPITestOMM(t, store, uint32(60000+index), "SYNC-TEST", "2026-05-10")
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"celestrak-gp","limit":1105}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		TotalCount int64 `json:"total_count"`
		Count      int   `json:"count"`
		Limit      int   `json:"limit"`
		Results    []struct {
			CID        string `json:"cid"`
			SizeBytes  int    `json:"size_bytes"`
			DataBase64 string `json:"data_base64"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body.TotalCount != 1105 || body.Count != 1105 || body.Limit != 1105 || len(body.Results) != 1105 {
		t.Fatalf("large scan = total %d count %d limit %d len %d, want 1105", body.TotalCount, body.Count, body.Limit, len(body.Results))
	}
	for _, row := range body.Results {
		if row.CID == "" || row.SizeBytes == 0 {
			t.Fatalf("large scan returned incomplete ref: %+v", row)
		}
		if row.DataBase64 != "" {
			t.Fatalf("large scan must not inline record payloads")
		}
	}
}

func TestDataStreamReturnsScanBoundRefsInRequestedOrder(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "STARLINK-6292", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-10")

	records, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query raw records failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	ordered := []*storage.Record{records[1], records[0]}
	requestBody := map[string]interface{}{
		"schema":    "OMM.fbs",
		"scan_hash": scanHash("OMM.fbs", ordered),
		"records": []map[string]interface{}{
			rawRecordRow("OMM.fbs", ordered[0], false),
			rawRecordRow("OMM.fbs", ordered[1], false),
		},
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
		t.Fatalf("Content-Type = %q, want raw FlatBuffer stream", got)
	}
	if got := rec.Header().Get("X-SDN-Scan-Hash"); got != requestBody["scan_hash"] {
		t.Fatalf("X-SDN-Scan-Hash = %q, want %q", got, requestBody["scan_hash"])
	}
	streamRecords := readLengthPrefixedRecords(t, rec.Body.Bytes())
	if len(streamRecords) != len(ordered) {
		t.Fatalf("stream record count = %d, want %d", len(streamRecords), len(ordered))
	}
	for index := range ordered {
		if string(streamRecords[index]) != string(ordered[index].Data) {
			t.Fatalf("stream record %d did not match requested ref order", index)
		}
	}
}

func TestDataStreamAcceptsLargeScanBoundRefChunks(t *testing.T) {
	store := newDataAPITestStore(t)
	for index := 0; index < 1105; index++ {
		storeDataAPITestOMM(t, store, uint32(62000+index), "STREAM-TEST", "2026-05-10")
	}

	records, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      1105,
	})
	if err != nil {
		t.Fatalf("query raw records failed: %v", err)
	}
	if len(records) != 1105 {
		t.Fatalf("len(records) = %d, want 1105", len(records))
	}
	requestRefs := make([]map[string]interface{}, 0, len(records))
	for _, record := range records {
		requestRefs = append(requestRefs, rawRecordRow("OMM.fbs", record, false))
	}
	requestBody := map[string]interface{}{
		"schema":    "OMM.fbs",
		"scan_hash": scanHash("OMM.fbs", records),
		"records":   requestRefs,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	streamRecords := readLengthPrefixedRecords(t, rec.Body.Bytes())
	if len(streamRecords) != len(records) {
		t.Fatalf("stream record count = %d, want %d", len(streamRecords), len(records))
	}
	if string(streamRecords[0]) != string(records[0].Data) {
		t.Fatal("large stream did not preserve first requested record")
	}
	if string(streamRecords[len(streamRecords)-1]) != string(records[len(records)-1].Data) {
		t.Fatal("large stream did not preserve last requested record")
	}
}

func TestDataRecordEndpointReturnsRawFlatBuffer(t *testing.T) {
	store := newDataAPITestStore(t)
	payload := sds.NewEPMBuilder().
		WithDN("Record Endpoint Node").
		WithEmail("record@example.test").
		WithMultiAddrs([]string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWRecordEPM"}).
		Build()
	if err := store.SaveLocalEPM("12D3KooWRecordEPM", payload); err != nil {
		t.Fatalf("SaveLocalEPM failed: %v", err)
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store, nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/records/EPM.fbs/12D3KooWRecordEPM", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-flatbuffers" {
		t.Fatalf("Content-Type = %q, want application/x-flatbuffers", got)
	}
	if got := rec.Body.Bytes(); string(got) != string(payload) {
		t.Fatal("record endpoint did not return original raw FlatBuffer payload")
	}
}

func newDataAPITestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	return store
}

func newDataAPITestStoreWithBasePath(t *testing.T) (*storage.FlatSQLStore, string, *sds.Validator) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "sdn-data-api-test-*")
	if err != nil {
		t.Fatalf("create temp dir failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("create validator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, filepath.Join(tmpDir, "db"), validator
}

type apiSchemaSummaryRow struct {
	SchemaName string `json:"schema_name"`
	Count      int    `json:"count"`
}

type apiSourceSummaryRow struct {
	SchemaName string `json:"schema_name"`
	ProviderID string `json:"provider_id"`
	SourceName string `json:"source_name"`
	Count      int    `json:"count"`
}

func apiSchemaCount(schemas []apiSchemaSummaryRow, schema string) int {
	for _, entry := range schemas {
		if entry.SchemaName == schema {
			return entry.Count
		}
	}
	return 0
}

func apiSourceCount(sources []apiSourceSummaryRow, schema, providerID, sourceName string) int {
	for _, entry := range sources {
		if entry.SchemaName == schema && entry.ProviderID == providerID && entry.SourceName == sourceName {
			return entry.Count
		}
	}
	return 0
}

func storeDataAPITestOMM(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName, day string) []byte {
	t.Helper()
	return storeDataAPITestOMMWithBatch(t, store, norad, objectName, day, "test-batch")
}

func storeDataAPITestOMMWithBatch(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName, day, batchID string) []byte {
	t.Helper()

	epoch, err := time.Parse(time.RFC3339, day+"T12:00:00Z")
	if err != nil {
		t.Fatalf("parse epoch failed: %v", err)
	}
	payload := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(objectName).
		WithEpoch(epoch.Format(time.RFC3339)).
		Build()
	tags := storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		SourceURL:  "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:    batchID,
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}
	return payload
}

func storeDataAPITestSPW(t *testing.T, store *storage.FlatSQLStore, date string) {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	dateOffset := builder.CreateString(date)
	SPW.SPWStart(builder)
	SPW.SPWAddDATE(builder, dateOffset)
	SPW.SPWAddBSRN(builder, 2569)
	SPW.SPWAddND(builder, 12)
	SPW.SPWAddKP1(builder, 13)
	SPW.SPWAddAP1(builder, 5)
	SPW.SPWAddF107_OBS(builder, 172.3)
	SPW.SPWAddF107_ADJ(builder, 170.1)
	SPW.SPWAddF107_DATA_TYPE(builder, SPW.F107DataTypeOBS)
	spw := SPW.SPWEnd(builder)
	SPW.FinishSizePrefixedSPWBuffer(builder, spw)

	tags := storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-space-weather",
		SourceURL:  "https://celestrak.org/SpaceData/SW-All.csv",
		BatchID:    "test-spw-batch",
	}
	if _, err := store.StoreWithSourceTags("SPW.fbs", builder.FinishedBytes(), "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store SPW failed: %v", err)
	}
}

func readLengthPrefixedRecords(t *testing.T, data []byte) [][]byte {
	t.Helper()

	reader := bytes.NewReader(data)
	var records [][]byte
	for reader.Len() > 0 {
		var length uint32
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			t.Fatalf("read length failed: %v", err)
		}
		payload := make([]byte, length)
		if _, err := reader.Read(payload); err != nil {
			t.Fatalf("read payload failed: %v", err)
		}
		records = append(records, payload)
	}
	return records
}
