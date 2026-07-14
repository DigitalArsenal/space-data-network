package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestAdminStoreHydrateReSyncsJournalOnlyRecords drives the operator re-sync
// trigger end to end: after a "restart" (store reopened deferred, the way the
// daemon opens it) the records are journal-only and the data summary is empty;
// POST /api/v1/admin/store/hydrate must replay them back into the control
// tables + rebuild source summaries, report the counts, and make the records
// visible through the data API.
func TestAdminStoreHydrateReSyncsJournalOnlyRecords(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-store-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	basePath := filepath.Join(tmpDir, "db")

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	// Seed a store with a couple of source-tagged records, then close it.
	seed, err := storage.NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	storeDataAPITestOMMInto(t, seed, 25544, "ISS", "2026-05-10")
	storeDataAPITestOMMInto(t, seed, 40909, "STARLINK", "2026-05-11")
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	// Reopen the way the daemon does after a restart: control tables empty.
	store, err := storage.NewFlatSQLStore(basePath, validator,
		storage.WithDeferredBootRebuilds(), storage.WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("deferred reopen: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mux := http.NewServeMux()
	NewStoreAdminHandler(store).RegisterRoutes(mux)
	NewDataQueryHandler(store).RegisterRoutes(mux)

	// Pre-condition: the board is empty because records are journal-only.
	if got := summaryTotalRecords(t, mux); got != 0 {
		t.Fatalf("BEFORE hydrate: /api/v1/data/summary total_records=%d, want 0", got)
	}

	// Wrong method is rejected.
	if rec := doRequest(mux, http.MethodGet, "/api/v1/admin/store/hydrate"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET hydrate status=%d, want 405", rec.Code)
	}

	// Trigger the re-sync.
	rec := doRequest(mux, http.MethodPost, "/api/v1/admin/store/hydrate")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST hydrate status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RecordsReplayed int   `json:"records_replayed"`
		Sources         int   `json:"sources"`
		TotalRecords    int64 `json:"total_records"`
		DurationMS      int64 `json:"duration_ms"`
		Forced          bool  `json:"forced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode hydrate response: %v (body=%s)", err, rec.Body.String())
	}
	if resp.RecordsReplayed < 2 {
		t.Fatalf("records_replayed=%d, want >= 2", resp.RecordsReplayed)
	}
	if resp.Sources != 1 {
		t.Fatalf("sources=%d, want 1 (one provider/source)", resp.Sources)
	}
	if resp.TotalRecords != 2 {
		t.Fatalf("total_records=%d, want 2", resp.TotalRecords)
	}
	if resp.Forced {
		t.Fatalf("forced=true, want false (no force query param)")
	}

	// Post-condition: the data API now sees the records.
	if got := summaryTotalRecords(t, mux); got != 2 {
		t.Fatalf("AFTER hydrate: /api/v1/data/summary total_records=%d, want 2", got)
	}

	// A forced re-sync is idempotent and reports forced=true.
	recForce := doRequest(mux, http.MethodPost, "/api/v1/admin/store/hydrate?force=true")
	if recForce.Code != http.StatusOK {
		t.Fatalf("POST hydrate?force=true status=%d body=%s", recForce.Code, recForce.Body.String())
	}
	var respForce struct {
		TotalRecords int64 `json:"total_records"`
		Forced       bool  `json:"forced"`
	}
	if err := json.Unmarshal(recForce.Body.Bytes(), &respForce); err != nil {
		t.Fatalf("decode forced hydrate response: %v", err)
	}
	if !respForce.Forced {
		t.Fatalf("forced=false on ?force=true request")
	}
	if respForce.TotalRecords != 2 {
		t.Fatalf("forced re-sync total_records=%d, want 2 (idempotent)", respForce.TotalRecords)
	}
}

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
		SourceName: "celestrak-gp",
		SourceURL:  "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:    "test-batch",
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store OMM %d: %v", norad, err)
	}
}
