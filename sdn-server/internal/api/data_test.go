package api

import (
	"bufio"
	"bytes"
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
