package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestRawQuerySourceRunsEncoding pins the run-length shape a browser engine
// splits the aligned frame stream by.
func TestRawQuerySourceRunsEncoding(t *testing.T) {
	recs := func(names ...string) []*storage.Record {
		out := make([]*storage.Record, 0, len(names))
		for _, name := range names {
			out = append(out, &storage.Record{SourceTags: storage.SourceTags{SourceName: name}})
		}
		return out
	}
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{names: nil, want: ""},
		{names: []string{"a", "a", "b", ""}, want: "a:2,b:1,-:1"},
		{names: []string{"", "", "x"}, want: "-:2,x:1"},
		{names: []string{"celestrak-gp"}, want: "celestrak-gp:1"},
		{names: []string{"a b,c:d/e", "a b,c:d/e"}, want: "a%20b%2Cc%3Ad%2Fe:2"},
		{names: []string{"a", "b", "a"}, want: "a:1,b:1,a:1"},
	} {
		if got := encodeRawRecordSourceRuns(recs(tc.names...)); got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.names, got, tc.want)
		}
	}
}

// TestRawQueryWindowHeadersAndTotalCount drives the raw FlatBuffer lane the
// dashboard window is fed from: a page (limit/offset) of aligned frames plus
// the headers a client needs to size its window — the node's total for the
// same filter, the page position, and the per-source runs of the frames.
func TestRawQueryWindowHeadersAndTotalCount(t *testing.T) {
	store := newDataAPITestStore(t)
	storeDataAPITestOMMWithSource(t, store, 25544, "ISS", "2026-05-10", "batch-a", "space-data-network-02", "catalogfixture-gp")
	storeDataAPITestOMMWithSource(t, store, 40909, "SAT-B", "2026-05-11", "batch-a", "space-data-network-02", "catalogfixture-gp")
	storeDataAPITestOMMWithSource(t, store, 48274, "SAT-C", "2026-05-12", "batch-b", "space-data-network-02", "supplemental-gp")

	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)

	rawQuery := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("Content-Type = %q", got)
		}
		return rec
	}

	rec := rawQuery(t, `{"schema":"OMM.fbs","limit":2,"offset":1}`)
	if got := rec.Header().Get("X-SDN-Record-Count"); got != "2" {
		t.Fatalf("X-SDN-Record-Count = %q, want 2", got)
	}
	if got := rec.Header().Get("X-SDN-Total-Count"); got != "3" {
		t.Fatalf("X-SDN-Total-Count = %q, want 3 (the node's count for the filter, not the page)", got)
	}
	if got := rec.Header().Get("X-SDN-Offset"); got != "1" {
		t.Fatalf("X-SDN-Offset = %q, want 1", got)
	}
	if got := rec.Header().Get("X-SDN-Limit"); got != "2" {
		t.Fatalf("X-SDN-Limit = %q, want 2", got)
	}
	runs := rec.Header().Get("X-SDN-Source-Runs")
	if runs == "" {
		t.Fatal("X-SDN-Source-Runs missing")
	}
	sum := 0
	for _, pair := range strings.Split(runs, ",") {
		i := strings.LastIndex(pair, ":")
		if i <= 0 {
			t.Fatalf("malformed run %q in %q", pair, runs)
		}
		name := pair[:i]
		if name != "catalogfixture-gp" && name != "supplemental-gp" {
			t.Fatalf("unexpected source %q in %q", name, runs)
		}
		n, err := strconv.Atoi(pair[i+1:])
		if err != nil {
			t.Fatalf("run count %q: %v", pair, err)
		}
		sum += n
	}
	if sum != 2 {
		t.Fatalf("source run counts sum to %d, want 2 (one per frame): %q", sum, runs)
	}
	frames := readLengthPrefixedRecords(t, rec.Body.Bytes())
	if len(frames) != 2 {
		t.Fatalf("stream frames = %d, want 2", len(frames))
	}
	for i, frame := range frames {
		// Size-prefixed FlatBuffer: u32 size, u32 root offset, then the
		// 4-byte file identifier — exactly what the engine's ingest fast
		// path keys on.
		if len(frame) < 12 || string(frame[8:12]) != "$OMM" {
			t.Fatalf("frame %d does not carry the $OMM file identifier at bytes 8..12: % x", i, frame[:min(len(frame), 12)])
		}
	}

	// A source-filtered page counts only that lane.
	lane := rawQuery(t, `{"schema":"OMM.fbs","source_name":"catalogfixture-gp","limit":1,"offset":0}`)
	if got := lane.Header().Get("X-SDN-Total-Count"); got != "2" {
		t.Fatalf("lane X-SDN-Total-Count = %q, want 2", got)
	}
	if got := lane.Header().Get("X-SDN-Source-Runs"); got != "catalogfixture-gp:1" {
		t.Fatalf("lane X-SDN-Source-Runs = %q", got)
	}

	// Raw mode admits window-sized pages: 5000 is not clamped to the JSON cap.
	wide := rawQuery(t, `{"schema":"OMM.fbs","limit":5000,"offset":0}`)
	if got := wide.Header().Get("X-SDN-Limit"); got != "5000" {
		t.Fatalf("raw X-SDN-Limit = %q, want 5000 (JSON cap must not apply to the raw lane)", got)
	}
	if got := wide.Header().Get("X-SDN-Record-Count"); got != "3" {
		t.Fatalf("raw X-SDN-Record-Count = %q, want 3", got)
	}
	if got := wide.Header().Get("X-SDN-Offset"); got != "0" {
		t.Fatalf("raw X-SDN-Offset = %q, want 0", got)
	}
	over := rawQuery(t, `{"schema":"OMM.fbs","limit":999999}`)
	if got := over.Header().Get("X-SDN-Limit"); got != strconv.Itoa(rawDataMaxRawStreamLimit) {
		t.Fatalf("raw X-SDN-Limit = %q, want the raw cap %d", got, rawDataMaxRawStreamLimit)
	}

	// Beyond the end: an empty page still reports the total.
	past := rawQuery(t, `{"schema":"OMM.fbs","limit":2,"offset":10}`)
	if got := past.Header().Get("X-SDN-Record-Count"); got != "0" {
		t.Fatalf("past-end X-SDN-Record-Count = %q, want 0", got)
	}
	if got := past.Header().Get("X-SDN-Total-Count"); got != "3" {
		t.Fatalf("past-end X-SDN-Total-Count = %q, want 3", got)
	}
	if past.Body.Len() != 0 {
		t.Fatalf("past-end body = %d bytes, want empty", past.Body.Len())
	}

	// The JSON lane keeps its own cap and carries no window headers.
	jsonReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"OMM.fbs","limit":5000}`))
	jsonReq.Header.Set("Content-Type", "application/json")
	jsonRec := httptest.NewRecorder()
	mux.ServeHTTP(jsonRec, jsonReq)
	if jsonRec.Code != http.StatusOK {
		t.Fatalf("json status = %d, body=%s", jsonRec.Code, jsonRec.Body.String())
	}
	if jsonRec.Header().Get("X-SDN-Total-Count") != "" || jsonRec.Header().Get("X-SDN-Limit") != "" {
		t.Fatalf("json lane carries window headers: %v", jsonRec.Header())
	}
}
