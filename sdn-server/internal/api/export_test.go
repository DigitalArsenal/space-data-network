package api

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func newExportTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "sdn-export-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedExportRecords stores 5 OMM records over two sources and returns the
// CIDs the store assigned, sorted.
func seedExportRecords(t *testing.T, store *storage.FlatSQLStore) []string {
	t.Helper()
	var cids []string
	add := func(norad uint32, name, day, batch, source string) {
		payload := storeDataAPITestOMMWithSource(t, store, norad, name, day, batch, "space-data-network-02", source)
		cids = append(cids, storage.ComputeCID(payload))
	}
	add(25544, "ISS (ZARYA)", "2026-09-01", "gp-batch-1", "catalogfixture-gp")
	add(40909, "OBJECT-B", "2026-09-02", "gp-batch-1", "catalogfixture-gp")
	add(43013, "OBJECT-C", "2026-09-03", "gp-batch-2", "catalogfixture-gp")
	add(48274, "OBJECT-D", "2026-09-01", "other-batch-1", "other-gp")
	add(49044, "OBJECT-E", "2026-09-02", "other-batch-1", "other-gp")
	sort.Strings(cids)
	return cids
}

func newExportTestMux(t *testing.T, store *storage.FlatSQLStore) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewExportHandler(&AdminMountDeps{Store: store, Config: &config.Config{}}).RegisterRoutes(mux)
	return mux
}

// recordFrameIdentifier reads the file identifier of an exported record
// frame: bytes 8..12 when the stored buffer is bare, 12..16 when the store
// holds the record with its own size prefix (the builders' form).
func recordFrameIdentifier(frame []byte) string {
	if len(frame) < 16 {
		return ""
	}
	inner := frame[4:]
	if int(binary.LittleEndian.Uint32(inner[:4]))+4 == len(inner) && len(inner) >= 12 {
		return string(inner[8:12])
	}
	return string(inner[4:8])
}

func exportGet(t *testing.T, mux *http.ServeMux, target string) (*httptest.ResponseRecorder, [][]byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("GET %s: body is not a frame stream: %v", target, err)
	}
	return rec, frames
}

func exportHeaderAndRecords(t *testing.T, rec *httptest.ResponseRecorder, frames [][]byte) (QRPFields, [][]byte) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d body = %q", rec.Code, rec.Body.String())
	}
	if len(frames) == 0 || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("first frame = %q, want the $QRP header", FrameIdentifier(frames[0]))
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		t.Fatalf("ParseQRP: %v", err)
	}
	header := QRPFields{
		Kind: int8(q.KIND()), Status: int8(q.STATUS()), SchemaName: string(q.SchemaName()), FileIdentifier: string(q.FileIdentifier()),
		SourceName: string(q.SourceName()), RecordCount: q.RecordCount(), TotalCount: q.TotalCount(), Stored: q.STORED(),
		NextCursor: string(q.NextCursor()), Cursor: string(q.CURSOR()), Limit: q.LIMIT(), Truncated: q.TRUNCATED(), Etag: string(q.ETAG()),
		GeneratedAtMs: q.GeneratedAtMs(),
	}
	records := frames[1:]
	if got := rec.Header().Get(StreamRecordCountHeader); got != strconv.Itoa(len(records)) {
		t.Fatalf("X-SDN-Record-Count = %q, want %d record frames", got, len(records))
	}
	if int(header.RecordCount) != len(records) {
		t.Fatalf("header RECORD_COUNT = %d, body has %d record frames", header.RecordCount, len(records))
	}
	for i, frame := range records {
		if got := recordFrameIdentifier(frame); got != "$OMM" {
			t.Fatalf("record frame %d identifier = %q, want $OMM", i, got)
		}
	}
	return header, records
}

func TestExportStreamsEveryRecordBehindAQRPHeader(t *testing.T) {
	store := newExportTestStore(t)
	cids := seedExportRecords(t, store)
	mux := newExportTestMux(t, store)

	rec, frames := exportGet(t, mux, ExportPath("OMM"))
	header, records := exportHeaderAndRecords(t, rec, frames)
	if len(records) != 5 || header.RecordCount != 5 || header.TotalCount != 5 || header.Stored != 5 {
		t.Fatalf("export all: %d records, header %+v", len(records), header)
	}
	if header.Kind != QRPKindPage || header.SchemaName != "OMM.fbs" || header.FileIdentifier != "$OMM" || header.NextCursor != "" || header.Truncated {
		t.Fatalf("header = %+v", header)
	}
	if header.Etag == "" || header.GeneratedAtMs == 0 {
		t.Fatalf("header ETAG/GENERATED_AT_MS = %q/%d", header.Etag, header.GeneratedAtMs)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, `attachment; filename="omm-all-`) || !strings.HasSuffix(disposition, `.sdn"`) {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if rec.Header().Get(StreamSchemaHeader) != "OMM.fbs" || rec.Header().Get(StreamFormatHeader) != StreamFormat || rec.Header().Get("Content-Type") != StreamContentType {
		t.Fatalf("stream headers = %v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length = %q, body = %d", rec.Header().Get("Content-Length"), rec.Body.Len())
	}

	// Every exported frame re-ingests into a fresh store under the same CID:
	// the .sdn is the store's own bytes, never a projection.
	fresh := newExportTestStore(t)
	var reimported []string
	for _, frame := range records {
		cid, err := fresh.StoreWithSourceTags("OMM.fbs", frame[4:], "source:reimport", nil, storage.SourceTags{
			ProviderID: "space-data-network-02", SourceName: "reimport", BatchID: "reimport-1",
		})
		if err != nil {
			t.Fatalf("re-ingest exported frame: %v", err)
		}
		reimported = append(reimported, cid)
	}
	sort.Strings(reimported)
	if strings.Join(reimported, ",") != strings.Join(cids, ",") {
		t.Fatalf("re-ingested CIDs\n%v\nwant\n%v", reimported, cids)
	}
}

func TestExportSourceFilterAndCursorResume(t *testing.T) {
	store := newExportTestStore(t)
	seedExportRecords(t, store)
	mux := newExportTestMux(t, store)

	rec, frames := exportGet(t, mux, ExportPath("OMM")+"?source=catalogfixture-gp")
	header, records := exportHeaderAndRecords(t, rec, frames)
	if len(records) != 3 || header.TotalCount != 3 || header.SourceName != "catalogfixture-gp" {
		t.Fatalf("source export: %d records, header %+v", len(records), header)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "omm-catalogfixture-gp-") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	// The CODE@source form the table lane uses is accepted too.
	rec, frames = exportGet(t, mux, ExportPath("OMM")+"?source=OMM%40catalogfixture-gp")
	if _, records := exportHeaderAndRecords(t, rec, frames); len(records) != 3 {
		t.Fatalf("OMM@source export: %d records, want 3", len(records))
	}

	// limit=2 truncates and names the cursor that resumes with the rest.
	rec, frames = exportGet(t, mux, ExportPath("OMM")+"?limit=2")
	header, records = exportHeaderAndRecords(t, rec, frames)
	if len(records) != 2 || header.NextCursor == "" || !header.Truncated || header.Status != QRPStatusPartial {
		t.Fatalf("limit=2: %d records, header %+v", len(records), header)
	}
	if rec.Header().Get(StreamNextCursorHeader) != header.NextCursor {
		t.Fatalf("X-SDN-Next-Cursor = %q, header NEXT_CURSOR = %q", rec.Header().Get(StreamNextCursorHeader), header.NextCursor)
	}
	first := map[string]bool{}
	for _, frame := range records {
		first[storage.ComputeCID(frame[4:])] = true
	}
	rec, frames = exportGet(t, mux, ExportPath("OMM")+"?cursor="+header.NextCursor)
	resumed, rest := exportHeaderAndRecords(t, rec, frames)
	if len(rest) != 3 || resumed.Cursor != header.NextCursor {
		t.Fatalf("resume: %d records (want 3), header %+v", len(rest), resumed)
	}
	for _, frame := range rest {
		if first[storage.ComputeCID(frame[4:])] {
			t.Fatalf("resumed export repeated a record from the first page")
		}
	}

	// Provider/batch filters walk the indexed query; an unknown batch is an
	// empty export with a header only.
	rec, frames = exportGet(t, mux, ExportPath("OMM")+"?provider_id=space-data-network-02&batch_id=gp-batch-2")
	if _, records := exportHeaderAndRecords(t, rec, frames); len(records) != 1 {
		t.Fatalf("batch export: %d records, want 1", len(records))
	}
	rec, frames = exportGet(t, mux, ExportPath("OMM")+"?batch_id=no-such-batch")
	if header, records := exportHeaderAndRecords(t, rec, frames); len(records) != 0 || header.RecordCount != 0 {
		t.Fatalf("empty export: %d records, header %+v", len(records), header)
	}

	// A bad cursor and a bad limit are refused with a $QRP error frame.
	for _, target := range []string{ExportPath("OMM") + "?cursor=not-a-cursor", ExportPath("OMM") + "?limit=-1"} {
		rec, frames := exportGet(t, mux, target)
		if rec.Code != http.StatusBadRequest || len(frames) != 1 || FrameIdentifier(frames[0]) != "$QRP" {
			t.Fatalf("GET %s: status %d frames %d", target, rec.Code, len(frames))
		}
	}
}
