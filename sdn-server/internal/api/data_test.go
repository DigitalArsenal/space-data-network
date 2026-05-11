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
	if got := rec.Header().Get("X-SDN-Stream-Format"); got != "uint32be-length-prefixed" {
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
	if got := rec.Header().Get("X-SDN-Stream-Format"); got != "uint32be-length-prefixed" {
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"EPM.fbs","provider_id":"local-node","source_name":"local-epm","limit":10}`))
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
	return store
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

func storeDataAPITestOMM(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName, day string) {
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
		BatchID:    "test-batch",
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}
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
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
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
