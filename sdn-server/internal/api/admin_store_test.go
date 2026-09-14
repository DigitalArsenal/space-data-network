package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func doRequest(mux *http.ServeMux, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func summaryTotalRecords(t *testing.T, mux *http.ServeMux) int64 {
	t.Helper()
	rec := doRequest(mux, http.MethodGet, "/api/v1/data/summary")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/data/summary status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		TotalRecords int64 `json:"total_records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	return body.TotalRecords
}

// storeDataAPITestOMMInto stores one source-tagged OMM record into an explicit
// store (the shared helper targets a fresh throwaway store).
func storeDataAPITestOMMInto(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName, day string) {
	t.Helper()
	payload := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(objectName).
		WithEpoch(day + "T12:00:00Z").
		Build()
	tags := storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-gp",
		SourceURL:  "https://fixture.test/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:    "test-batch",
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store OMM %d: %v", norad, err)
	}
}
