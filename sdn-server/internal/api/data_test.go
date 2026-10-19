package api

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestBulkExportCSVUsesGeneratedBindingFieldOrderAndRFC4180(t *testing.T) {
	store := newDataAPITestStore(t)
	for index := 0; index < 25; index++ {
		name := fmt.Sprintf("OMM-%02d", index)
		if index == 7 {
			name = `ALPHA, "QUOTED"`
		}
		storeDataAPITestOMM(t, store, uint32(70000+index), name, "2026-05-05")
		storeDataAPITestCAT(t, store, uint32(80000+index), fmt.Sprintf("CAT-%02d", index))
	}

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)
	tests := []struct {
		name       string
		path       string
		binding    reflect.Type
		quotedName string
	}{
		{
			name:       "OMM",
			path:       "/api/v1/data/omm/bulk?format=csv&epoch=2026-05-05T12:00:00Z&source=catalogfixture-gp&limit=25",
			binding:    reflect.TypeOf((*OMM.OMM)(nil)),
			quotedName: `ALPHA, "QUOTED"`,
		},
		{
			name:    "CAT",
			path:    "/api/v1/data/cat/bulk?format=csv&source=catalogfixture-cat&limit=25",
			binding: reflect.TypeOf((*CAT.CAT)(nil)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := serveDataAPIRequest(t, mux, test.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			rows, err := csv.NewReader(bytes.NewReader(rec.Body.Bytes())).ReadAll()
			if err != nil {
				t.Fatalf("RFC 4180 parse failed: %v", err)
			}
			if len(rows) != 26 {
				t.Fatalf("CSV line count = %d, want 26", len(rows))
			}
			wantHeader := generatedBindingHeaderForTest(t, test.binding, "")
			if !reflect.DeepEqual(rows[0], wantHeader) {
				t.Fatalf("CSV header does not match generated binding order\n got: %v\nwant: %v", rows[0], wantHeader)
			}
			if test.quotedName != "" {
				nameColumn := indexOfTestString(rows[0], "OBJECT_NAME")
				if nameColumn < 0 {
					t.Fatal("OBJECT_NAME column missing")
				}
				found := false
				for _, row := range rows[1:] {
					if row[nameColumn] == test.quotedName {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("quoted value %q did not round-trip", test.quotedName)
				}
			}
		})
	}
}

func TestBulkExportJSONLMatchesJSONArrayElementForElement(t *testing.T) {
	store := newDataAPITestStore(t)
	for index := 0; index < 25; index++ {
		storeDataAPITestOMM(t, store, uint32(71000+index), fmt.Sprintf("JSONL-%02d", index), "2026-05-05")
	}
	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)
	base := "/api/v1/data/omm/bulk?epoch=2026-05-05T12:00:00Z&source=catalogfixture-gp&limit=25&format="
	jsonRec := serveDataAPIRequest(t, mux, base+"json")
	jsonlRec := serveDataAPIRequest(t, mux, base+"jsonl")
	if jsonRec.Code != http.StatusOK || jsonlRec.Code != http.StatusOK {
		t.Fatalf("json status=%d jsonl status=%d", jsonRec.Code, jsonlRec.Code)
	}
	var elements []interface{}
	if err := json.Unmarshal(jsonRec.Body.Bytes(), &elements); err != nil {
		t.Fatalf("decode JSON array: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(jsonlRec.Body.Bytes()))
	line := 0
	for scanner.Scan() {
		var object interface{}
		if err := json.Unmarshal(scanner.Bytes(), &object); err != nil {
			t.Fatalf("decode JSONL line %d: %v", line, err)
		}
		if line >= len(elements) {
			t.Fatalf("JSONL has extra line %d", line)
		}
		if !reflect.DeepEqual(object, elements[line]) {
			t.Fatalf("JSONL line %d differs from JSON element", line)
		}
		line++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	if line != 25 || len(elements) != 25 {
		t.Fatalf("jsonl lines=%d json elements=%d, want 25 each", line, len(elements))
	}
}

func TestBulkExportJSONLStreams100KWithinMemoryBound(t *testing.T) {
	payload := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("MEMORY-BOUND").
		WithEpoch("2026-05-05T12:00:00Z").
		WithEpochTimestamp(float64(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC).Unix())).
		Build()
	record := &storage.Record{Data: payload}
	records := make([]*storage.Record, 100000)
	for index := range records {
		records[index] = record
	}
	binding, err := bulkBindingForSchema("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	writer := &lineCountingWriter{}
	if err := writeBulkJSONLines(writer, binding, records); err != nil {
		t.Fatalf("stream JSONL: %v", err)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	var retained uint64
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	if retained >= 32<<20 {
		t.Fatalf("retained heap = %d bytes, want < %d", retained, 32<<20)
	}
	if writer.lines != 100000 {
		t.Fatalf("writer lines = %d, want 100000", writer.lines)
	}
	t.Logf("streamed lines=%d retained_heap_bytes=%d", writer.lines, retained)
}

func TestBulkExportFormatsHeadersAndFilename(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 72000, "HEADERS", "2026-05-05")
	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)
	base := "/api/v1/data/omm/bulk?epoch=2026-05-05T12:00:00Z&source=catalogfixture-gp&limit=1"

	xlsx := serveDataAPIRequest(t, mux, base+"&format=xlsx")
	if xlsx.Code != http.StatusBadRequest {
		t.Fatalf("xlsx status = %d, want 400", xlsx.Code)
	}
	message := strings.TrimSpace(xlsx.Body.String())
	if xlsx.Header().Get("Content-Type") != "text/plain; charset=utf-8" || strings.Count(message, ".") != 1 || !strings.HasSuffix(message, ".") {
		t.Fatalf("xlsx response content-type=%q message=%q, want one plain sentence", xlsx.Header().Get("Content-Type"), message)
	}

	csvRec := serveDataAPIRequest(t, mux, base+"&format=csv&as_of=2026-05-05")
	if got := csvRec.Header().Get("Content-Type"); got != ContentTypeCSV {
		t.Fatalf("CSV Content-Type = %q, want %q", got, ContentTypeCSV)
	}
	if got := csvRec.Header().Get("Content-Disposition"); got != `attachment; filename="omm-2026-05-05.csv"` {
		t.Fatalf("CSV Content-Disposition = %q", got)
	}

	jsonlRec := serveDataAPIRequest(t, mux, base+"&format=jsonl")
	if got := jsonlRec.Header().Get("Content-Type"); got != ContentTypeJSONL {
		t.Fatalf("JSONL Content-Type = %q, want %q", got, ContentTypeJSONL)
	}
}

func TestBulkExportOpenAPIEnumeratesCSVAndJSONL(t *testing.T) {
	doc := generateTestSpec(t)
	paths := doc["paths"].(map[string]interface{})
	operation := paths["/api/v1/data/omm/bulk"].(map[string]interface{})["get"].(map[string]interface{})
	params := operation["parameters"].([]interface{})
	var formats []interface{}
	for _, raw := range params {
		param := raw.(map[string]interface{})
		if param["name"] == "format" {
			formats = param["schema"].(map[string]interface{})["enum"].([]interface{})
			break
		}
	}
	if !reflect.DeepEqual(formats, []interface{}{"flatbuffer", "json", "csv", "jsonl"}) {
		t.Fatalf("format enum = %v", formats)
	}
}

func serveDataAPIRequest(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

type lineCountingWriter struct {
	lines int
}

func (w *lineCountingWriter) Write(data []byte) (int, error) {
	w.lines += bytes.Count(data, []byte{'\n'})
	return len(data), nil
}

func generatedBindingHeaderForTest(t *testing.T, rootType reflect.Type, prefix string) []string {
	t.Helper()
	type accessor struct {
		method reflect.Method
		line   int
	}
	accessors := make([]accessor, 0, rootType.NumMethod())
	for index := 0; index < rootType.NumMethod(); index++ {
		method := rootType.Method(index)
		if strings.HasPrefix(method.Name, "Mutate") || strings.HasSuffix(method.Name, "Length") || strings.HasSuffix(method.Name, "Bytes") {
			continue
		}
		if method.Name != strings.ToUpper(method.Name) && !strings.HasSuffix(method.Name, "_type") {
			continue
		}
		entry := accessor{method: method}
		if fn := runtime.FuncForPC(method.Func.Pointer()); fn != nil {
			_, entry.line = fn.FileLine(method.Func.Pointer())
		}
		accessors = append(accessors, entry)
	}
	sort.SliceStable(accessors, func(i, j int) bool {
		if accessors[i].line == accessors[j].line {
			return accessors[i].method.Name < accessors[j].method.Name
		}
		return accessors[i].line < accessors[j].line
	})
	var header []string
	for _, entry := range accessors {
		method := entry.method
		name := prefix + method.Name
		switch {
		case method.Type.NumIn() == 1 && method.Type.NumOut() == 1:
			header = append(header, name)
		case method.Type.NumIn() == 2 && method.Type.NumOut() == 1 &&
			method.Type.In(1).Kind() == reflect.Ptr && method.Type.Out(0).Kind() == reflect.Ptr:
			header = append(header, generatedBindingHeaderForTest(t, method.Type.In(1), name+".")...)
		case method.Type.NumIn() == 2 && method.Type.NumOut() == 1 &&
			method.Type.In(1).Kind() == reflect.Ptr && method.Type.Out(0).Kind() == reflect.Bool:
			header = append(header, name)
		case method.Type.NumIn() == 2 || method.Type.NumIn() == 3:
			if _, ok := rootType.MethodByName(method.Name + "Length"); ok {
				header = append(header, name)
			}
		}
	}
	return header
}

func indexOfTestString(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func TestDataEpochQueryReturnsMatchQuality(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 25544, "ISS-BACKFILL", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS-FORWARD", "2026-05-12")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/epoch?schema=OMM.fbs&profile=epoch.nearest&at=2026-05-11T12:00:00Z&norad_cat_id=25544&provider_id=space-data-network-02&source_name=catalogfixture-gp&limit=10", nil)
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
			NoradCatID   uint32 `json:"NORAD_CAT_ID"`
			ObjectName   string `json:"OBJECT_NAME"`
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/epoch?schema=OMM.fbs&profile=epoch.day&day=2026-05-11&provider_id=space-data-network-02&source_name=catalogfixture-gp&limit=1", nil)
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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
	if got := apiSourceCount(body.Sources, "OMM.fbs", "space-data-network-02", "catalogfixture-gp"); got != 1 {
		t.Fatalf("OMM catalogfixture source count = %d, want 1", got)
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
		SourceName:    "catalogfixture-gp-historical",
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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
	if got.Identity.SchemaName != "OMM.fbs" || got.Identity.ProviderID != "space-data-network-02" || got.Identity.SourceName != "catalogfixture-gp-historical" || got.Identity.QueryProfile != storage.DatasetPublicationQueryProfile {
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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
	payload := storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"catalogfixture-gp","limit":10}`))
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
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")
	wantedPayload := storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-11")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"catalogfixture-gp","sync_filter":"NORAD_CAT_ID = 25544","limit":10,"include_data":true}`))
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

func TestDataLocalReplicaStatsReportsRowsPinsAndHead(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")
	verifiedAt := time.Unix(1_778_436_120, 0).UTC()
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/local-replica-stats?schema=OMM.fbs&provider_id=space-data-network-02&source_name=catalogfixture-gp&batch_id=test-batch&query_profile="+storage.DatasetPublicationQueryProfile, nil)
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
	if got.SchemaName != "OMM.fbs" || got.ProviderID != "space-data-network-02" || got.SourceName != "catalogfixture-gp" || got.BatchID != "test-batch" {
		t.Fatalf("unexpected source identity: %#v", got)
	}
	if got.ProviderPeerID != "16Uiu2HCatalogFixture" || got.ProviderPublicKey != "provider-public-key" {
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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
	return storeDataAPITestOMMWithSource(t, store, norad, objectName, day, batchID, "space-data-network-02", "catalogfixture-gp")
}

func storeDataAPITestOMMWithSource(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName, day, batchID, providerID, sourceName string) []byte {
	t.Helper()

	epoch, err := time.Parse(time.RFC3339, day+"T12:00:00Z")
	if err != nil {
		t.Fatalf("parse epoch failed: %v", err)
	}
	payload := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(objectName).
		WithEpoch(epoch.Format(time.RFC3339)).
		WithEpochTimestamp(float64(epoch.Unix())).
		Build()
	tags := storage.SourceTags{
		ProviderID: providerID,
		SourceName: sourceName,
		SourceURL:  "https://provider.example/data",
		BatchID:    batchID,
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:"+sourceName, nil, tags); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}
	return payload
}

func storeDataAPITestCAT(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName string) []byte {
	t.Helper()
	payload := sds.NewCATBuilder().
		WithNoradCatID(norad).
		WithObjectName(objectName).
		Build()
	tags := storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-cat",
		SourceURL:  "https://provider.example/catalog",
		BatchID:    "test-cat-batch",
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", payload, "source:catalogfixture-cat", nil, tags); err != nil {
		t.Fatalf("store CAT failed: %v", err)
	}
	return payload
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
