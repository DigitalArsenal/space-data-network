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

func TestDataScanReturnsFilteredTotalAndHashBoundRefs(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-10")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"catalogfixture-gp","limit":1}`))
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
	if row.SchemaName != "OMM.fbs" || row.ProviderID != "space-data-network-02" || row.SourceName != "catalogfixture-gp" {
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
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-11")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"catalogfixture-gp","sync_filter":"NORAD_CAT_ID = 25544","limit":10}`))
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
		SourceName:    "catalogfixture-gp-historical",
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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

func TestDataStreamEchoesResumableChunkMetadata(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	scanReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"catalogfixture-gp","limit":1}`))
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewBufferString(`{"schema":"OMM.fbs","provider_id":"space-data-network-02","source_name":"catalogfixture-gp","limit":1105}`))
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
	storeDataAPITestOMM(t, store, 56775, "SATELLITE-6292", "2026-05-10")
	storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-10")

	records, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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
		SourceName: "catalogfixture-gp",
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
	NewDataQueryHandler(store).RegisterRoutes(mux)

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

type recordIndexResponse struct {
	Schema string `json:"schema"`
	Total  int64  `json:"total"`
	Page   int    `json:"page"`
	Limit  int    `json:"limit"`
	Rows   []struct {
		Norad *int64  `json:"norad"`
		Epoch *string `json:"epoch"`
		CID   string  `json:"cid"`
	} `json:"rows"`
}

func getRecordIndex(t *testing.T, mux *http.ServeMux, query string) recordIndexResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/index"+query, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index %s status = %d, body=%s", query, rec.Code, rec.Body.String())
	}
	var out recordIndexResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode index response failed: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func TestDataRecordIndexRequiresExplicitSchema(t *testing.T) {
	store := newDataAPITestStore(t)
	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/data/index", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("index without schema status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestDataRecordIndexPaginationSearchAndTags(t *testing.T) {
	store := newDataAPITestStore(t)
	// 3 canonical FlatBuffer OMM records in one source lane.
	storeDataAPITestOMMWithBatch(t, store, 25544, "ISS", "2026-05-05", "batch-gp")
	storeDataAPITestOMMWithBatch(t, store, 40909, "SATELLITE-A", "2026-05-06", "batch-gp")
	storeDataAPITestOMMWithBatch(t, store, 40910, "SATELLITE-B", "2026-05-07", "batch-gp")
	// 2 canonical records in a second source lane.
	storeDataAPITestOMMWithSource(t, store, 12345, "OBJECT-A", "2026-07-14", "batch-secondary", "provider-secondary", "source-secondary")
	storeDataAPITestOMMWithSource(t, store, 12399, "OBJECT-B", "2026-07-15", "batch-secondary", "provider-secondary", "source-secondary")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	// (1) Whole schema: 5 records; "OMM" alias normalizes to OMM.fbs.
	all := getRecordIndex(t, mux, "?schema=OMM")
	if all.Schema != "OMM.fbs" {
		t.Fatalf("schema normalization = %q, want OMM.fbs", all.Schema)
	}
	if all.Total != 5 {
		t.Fatalf("total = %d, want 5", all.Total)
	}

	// (2) Source-lane filter returns only the requested provenance lane.
	gp := getRecordIndex(t, mux, "?schema=OMM.fbs&source_name=catalogfixture-gp&provider_id=space-data-network-02")
	if gp.Total != 3 || len(gp.Rows) != 3 {
		t.Fatalf("gp lane total=%d rows=%d, want 3/3", gp.Total, len(gp.Rows))
	}
	for _, r := range gp.Rows {
		if r.Norad == nil || r.Epoch == nil || r.CID == "" {
			t.Fatalf("gp row missing fields: %+v", r)
		}
	}

	// (3) The second source is newest-epoch-first.
	secondary := getRecordIndex(t, mux, "?schema=OMM.fbs&source_name=source-secondary")
	if secondary.Total != 2 || len(secondary.Rows) != 2 {
		t.Fatalf("secondary source total=%d rows=%d, want 2/2", secondary.Total, len(secondary.Rows))
	}
	if secondary.Rows[0].Norad == nil || *secondary.Rows[0].Norad != 12399 {
		t.Fatalf("secondary newest row norad = %v, want 12399", secondary.Rows[0].Norad)
	}

	// (4) NORAD substring search — "123" matches 12345 + 12399, not the GP set.
	search := getRecordIndex(t, mux, "?schema=OMM.fbs&norad=123")
	if search.Total != 2 {
		t.Fatalf("norad=123 total = %d, want 2", search.Total)
	}
	// A wildcard-injection attempt is sanitized to digits (empty -> no filter).
	inject := getRecordIndex(t, mux, "?schema=OMM.fbs&norad=%25")
	if inject.Total != 5 {
		t.Fatalf("norad=%%25 (sanitized) total = %d, want 5 (no filter)", inject.Total)
	}

	// (5) Pagination over one source: 1 per page, distinct CIDs, total stable.
	p1 := getRecordIndex(t, mux, "?schema=OMM.fbs&source_name=source-secondary&limit=1&page=1")
	p2 := getRecordIndex(t, mux, "?schema=OMM.fbs&source_name=source-secondary&limit=1&page=2")
	if p1.Total != 2 || p2.Total != 2 || len(p1.Rows) != 1 || len(p2.Rows) != 1 {
		t.Fatalf("pagination totals/rows wrong: p1=%+v p2=%+v", p1, p2)
	}
	if p1.Rows[0].CID == p2.Rows[0].CID {
		t.Fatalf("pages 1 and 2 returned the same record %s", p1.Rows[0].CID)
	}

	// (6) limit is clamped to the 200 max.
	clamped := getRecordIndex(t, mux, "?schema=OMM.fbs&limit=5000")
	if clamped.Limit != 200 {
		t.Fatalf("limit clamp = %d, want 200", clamped.Limit)
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
