package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFullTextQueryServesOriginalBytesAndExplicitCoverage(t *testing.T) {
	store := newDataAPITestStore(t)
	want := storeDataAPITestCAT(t, store, 25544, "München optical payload")
	storeDataAPITestCAT(t, store, 25545, "Radio instrument")
	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewBufferString(`{"schema":"CAT.fbs","search":"munchen optical","provider_id":"space-data-network-02","source_name":"catalogfixture-cat","limit":1}`))
		r.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "2" {
		t.Fatalf("initial coverage response: %d %s", w.Code, w.Body.String())
	}
	var pending struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pending); err != nil || pending.Error.Code != "SEARCH_INDEX_BUILDING" {
		t.Fatalf("missing explicit indexing status: %s", w.Body.String())
	}
	deadline := time.Now().Add(60 * time.Second)
	for w.Code == http.StatusServiceUnavailable && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		w = request()
	}
	if w.Code != http.StatusOK {
		t.Fatalf("search status: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-SDN-Total-Count") != "1" || w.Header().Get("X-SDN-Search-Applied") != "fts5-v1" {
		t.Fatalf("missing search/count coverage: %v", w.Header())
	}
	frames := readLengthPrefixedRecords(t, w.Body.Bytes())
	if len(frames) != 1 || !bytes.Equal(frames[0], want) {
		t.Fatal("search did not return the original matching FlatBuffer")
	}
}
