package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	adminStoreTestSchema   = "CAT.fbs"
	adminStoreTestProvider = "provider.example"
	adminStoreTestSource   = "catalog"
	adminStoreOldBatch     = "sha256:old"
	adminStoreKeepBatch    = "sha256:keep"
)

func TestAdminStoreSupersedeRefusesUnauthenticatedAndForeignOrigin(t *testing.T) {
	store := newAdminStoreSupersedeTestStore(t)
	mux := http.NewServeMux()
	NewStoreAdminHandler(store).RegisterRoutes(mux)

	// This is the same Admin trust wrapper used by the daemon's top-level
	// admin wall. The route itself deliberately has no alternate auth or
	// loopback bypass; it is mounted beside hydrate on the protected mux.
	authHandler := auth.NewHandler(nil, nil, time.Hour, "", "")
	protected := authHandler.RequireAuth(peers.Admin, mux.ServeHTTP)

	for _, test := range []struct {
		name   string
		origin string
	}{
		{name: "unauthenticated"},
		{name: "foreign origin", origin: "https://evil.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := newAdminStoreSupersedeRequest(t, "")
			req.Host = "node.example"
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			rec := httptest.NewRecorder()
			protected(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s, want 401", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdminStoreSupersedeRefusesMissingAndInvalidInput(t *testing.T) {
	store := newAdminStoreSupersedeTestStore(t)
	storeAdminStoreSupersedeBatches(t, store)
	mux := http.NewServeMux()
	NewStoreAdminHandler(store).RegisterRoutes(mux)

	tests := []struct {
		name   string
		target string
		body   string
		status int
	}{
		{name: "empty body", body: "", status: http.StatusBadRequest},
		{name: "missing schema", body: `{"provider_id":"provider.example","source_name":"catalog","keep_batch":"sha256:keep"}`, status: http.StatusBadRequest},
		{name: "missing provider", body: `{"schema":"CAT.fbs","source_name":"catalog","keep_batch":"sha256:keep"}`, status: http.StatusBadRequest},
		{name: "missing source", body: `{"schema":"CAT.fbs","provider_id":"provider.example","keep_batch":"sha256:keep"}`, status: http.StatusBadRequest},
		{name: "missing keep batch", body: `{"schema":"CAT.fbs","provider_id":"provider.example","source_name":"catalog"}`, status: http.StatusBadRequest},
		{name: "unadmitted schema", body: `{"schema":"NOT.fbs","provider_id":"provider.example","source_name":"catalog","keep_batch":"sha256:keep"}`, status: http.StatusBadRequest},
		{name: "schema alias", body: `{"schema":"CAT","provider_id":"provider.example","source_name":"catalog","keep_batch":"sha256:keep"}`, status: http.StatusBadRequest},
		{name: "control character", body: `{"schema":"CAT.fbs","provider_id":"provider.example","source_name":"bad\nsource","keep_batch":"sha256:keep"}`, status: http.StatusBadRequest},
		{name: "unknown field", body: `{"schema":"CAT.fbs","provider_id":"provider.example","source_name":"catalog","keep_batch":"sha256:keep","apply":true}`, status: http.StatusBadRequest},
		{name: "typo keep batch", body: `{"schema":"CAT.fbs","provider_id":"provider.example","source_name":"catalog","keep_batch":"sha256:typo"}`, status: http.StatusNotFound},
		{name: "invalid dry run", target: "?dry_run=sometimes", body: validAdminStoreSupersedeBody(), status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, storeSupersedeRoute+test.target, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), test.status)
			}
		})
	}

	before := countAdminStoreBatch(t, store, adminStoreOldBatch)
	rec := requestAdminStoreSupersede(t, mux, http.MethodGet, "", validAdminStoreSupersedeBody())
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d body=%s, want 405", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow=%q, want POST", got)
	}
	if after := countAdminStoreBatch(t, store, adminStoreOldBatch); after != before {
		t.Fatalf("refused requests changed old batch count from %d to %d", before, after)
	}
}

func TestAdminStoreSupersedeDryRunAndApply(t *testing.T) {
	store := newAdminStoreSupersedeTestStore(t)
	storeAdminStoreSupersedeBatches(t, store)
	mux := http.NewServeMux()
	NewStoreAdminHandler(store).RegisterRoutes(mux)

	if got := countAdminStoreRecords(t, store, storage.RawRecordQuery{SchemaName: adminStoreTestSchema}); got != 3 {
		t.Fatalf("initial record count=%d, want 3", got)
	}

	dryRun := requestAdminStoreSupersede(t, mux, http.MethodPost, "?dry_run=true", validAdminStoreSupersedeBody())
	if dryRun.Code != http.StatusOK {
		t.Fatalf("dry run status=%d body=%s", dryRun.Code, dryRun.Body.String())
	}
	dryResponse := decodeAdminStoreSupersedeResponse(t, dryRun)
	if !dryResponse.DryRun {
		t.Fatal("dry_run=false, want true")
	}
	if dryResponse.TagsDeleted != 2 || dryResponse.RecordsDeleted != 2 || dryResponse.FilesDeleted != 0 {
		t.Fatalf("dry-run result=%+v, want 2 tags, 2 records, 0 files", dryResponse.DatasetSupersedeResult)
	}
	if !reflect.DeepEqual(dryResponse.RetiredBatches, []string{adminStoreOldBatch}) {
		t.Fatalf("dry-run retired_batches=%q", dryResponse.RetiredBatches)
	}
	if got := countAdminStoreRecords(t, store, storage.RawRecordQuery{SchemaName: adminStoreTestSchema}); got != 3 {
		t.Fatalf("dry run changed record count to %d, want 3", got)
	}
	if got := countAdminStoreBatch(t, store, adminStoreOldBatch); got != 2 {
		t.Fatalf("dry run changed old batch count to %d, want 2", got)
	}

	apply := requestAdminStoreSupersede(t, mux, http.MethodPost, "", validAdminStoreSupersedeBody())
	if apply.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", apply.Code, apply.Body.String())
	}
	applyResponse := decodeAdminStoreSupersedeResponse(t, apply)
	if applyResponse.DryRun {
		t.Fatal("dry_run=true on applied response")
	}
	if applyResponse.TagsDeleted != 2 || applyResponse.RecordsDeleted != 2 || applyResponse.FilesDeleted != 0 {
		t.Fatalf("apply result=%+v, want 2 tags, 2 records, 0 files", applyResponse.DatasetSupersedeResult)
	}
	if !reflect.DeepEqual(applyResponse.RetiredBatches, []string{adminStoreOldBatch}) {
		t.Fatalf("apply retired_batches=%q", applyResponse.RetiredBatches)
	}

	rows, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: adminStoreTestSchema,
		ProviderID: adminStoreTestProvider,
		SourceName: adminStoreTestSource,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryRawRecords after apply: %v", err)
	}
	if len(rows) != 1 || rows[0].SourceTags.BatchID != adminStoreKeepBatch {
		t.Fatalf("raw records after apply=%+v, want only keep batch", rows)
	}
	if len(rows[0].Data) == 0 {
		t.Fatal("kept raw record is not readable")
	}
	if got := countAdminStoreBatch(t, store, adminStoreOldBatch); got != 0 {
		t.Fatalf("old batch count after apply=%d, want 0", got)
	}

	again := requestAdminStoreSupersede(t, mux, http.MethodPost, "", validAdminStoreSupersedeBody())
	if again.Code != http.StatusOK {
		t.Fatalf("second apply status=%d body=%s", again.Code, again.Body.String())
	}
	againResponse := decodeAdminStoreSupersedeResponse(t, again)
	if againResponse.TagsDeleted != 0 || againResponse.RecordsDeleted != 0 || againResponse.FilesDeleted != 0 {
		t.Fatalf("second apply result=%+v, want zero", againResponse.DatasetSupersedeResult)
	}
	if len(againResponse.RetiredBatches) != 0 {
		t.Fatalf("second apply retired_batches=%q, want empty", againResponse.RetiredBatches)
	}
}

func newAdminStoreSupersedeTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func storeAdminStoreSupersedeBatches(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()
	for _, record := range []struct {
		norad uint32
		name  string
		batch string
	}{
		{norad: 70001, name: "OLD-ONE", batch: adminStoreOldBatch},
		{norad: 70002, name: "OLD-TWO", batch: adminStoreOldBatch},
		{norad: 70003, name: "KEEP", batch: adminStoreKeepBatch},
	} {
		payload := sds.NewCATBuilder().
			WithNoradCatID(record.norad).
			WithObjectName(record.name).
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		_, err := store.StoreWithSourceTags(adminStoreTestSchema, payload, "source:test", nil, storage.SourceTags{
			ProviderID: adminStoreTestProvider,
			SourceName: adminStoreTestSource,
			BatchID:    record.batch,
		})
		if err != nil {
			t.Fatalf("store %s: %v", record.name, err)
		}
	}
}

func validAdminStoreSupersedeBody() string {
	return `{"schema":"CAT.fbs","provider_id":"provider.example","source_name":"catalog","keep_batch":"sha256:keep"}`
}

func newAdminStoreSupersedeRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, storeSupersedeRoute+query, bytes.NewBufferString(validAdminStoreSupersedeBody()))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func requestAdminStoreSupersede(t *testing.T, mux *http.ServeMux, method, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, storeSupersedeRoute+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeAdminStoreSupersedeResponse(t *testing.T, rec *httptest.ResponseRecorder) storeSupersedeResponse {
	t.Helper()
	var response storeSupersedeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return response
}

func countAdminStoreBatch(t *testing.T, store *storage.FlatSQLStore, batchID string) int64 {
	t.Helper()
	return countAdminStoreRecords(t, store, storage.RawRecordQuery{
		SchemaName: adminStoreTestSchema,
		ProviderID: adminStoreTestProvider,
		SourceName: adminStoreTestSource,
		BatchID:    batchID,
	})
}

func countAdminStoreRecords(t *testing.T, store *storage.FlatSQLStore, query storage.RawRecordQuery) int64 {
	t.Helper()
	count, err := store.CountRawRecords(query)
	if err != nil {
		t.Fatalf("CountRawRecords(%+v): %v", query, err)
	}
	return count
}
