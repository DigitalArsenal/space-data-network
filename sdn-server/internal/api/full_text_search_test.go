package api

import (
	"bytes"
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
		r := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewReader(BuildQRP(QRPFields{Kind: QRPKindRequest, SchemaName: "CAT.fbs", Search: "munchen optical", ProviderID: "space-data-network-02", SourceName: "catalogfixture-cat", Limit: 1})))
		r.Header.Set("Accept", StreamContentType)
		r.Header.Set("Content-Type", StreamContentType)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "2" {
		t.Fatalf("initial coverage response: %d %s", w.Code, w.Body.String())
	}
	pending, err := ParseQRP(w.Body.Bytes())
	if err != nil || int8(pending.KIND()) != QRPKindError || string(pending.ERROR_CODE()) != "search_index_building" {
		t.Fatalf("missing explicit QRP indexing status: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for w.Code == http.StatusServiceUnavailable && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		w = request()
	}
	if w.Code != http.StatusOK {
		t.Fatalf("search status: %d %s", w.Code, w.Body.String())
	}
	frames, err := SplitFrames(w.Body.Bytes())
	if err != nil || len(frames) != 2 {
		t.Fatalf("search frames: count=%d error=%v", len(frames), err)
	}
	page, err := ParseQRP(frames[0])
	if err != nil || int8(page.KIND()) != QRPKindPage || page.TOTAL_COUNT() != 1 || page.RECORD_COUNT() != 1 || string(page.SEARCH()) != "munchen optical" {
		t.Fatalf("missing search/count coverage in QRP page: %v", err)
	}
	if !bytes.Equal(frames[1], want) {
		t.Fatal("search did not return the original matching FlatBuffer")
	}
}
