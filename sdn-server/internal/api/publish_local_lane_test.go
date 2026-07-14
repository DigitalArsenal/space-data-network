package api

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const localLanePrincipal = "12D3KooWLocalLanePeerID"

func newLocalLaneTestMux(t *testing.T) (*http.ServeMux, *storage.FlatSQLStore) {
	t.Helper()
	// The third return is a validator built with a nil flatc module — exactly the
	// state of every packaged deployment, since findWasmPath() never resolves
	// there. Publishing junk must be rejected in THAT configuration.
	store, _, validator := newDataAPITestStoreWithBasePath(t)
	cfg := &config.PublishingConfig{
		Enabled:           true,
		DefaultQuotaBytes: 1 << 20,
		MaxRecordBytes:    1 << 20,
		MinTrustLevel:     "untrusted",
	}
	handler := NewPublishHandler(store, validator, NewStorageQuotaManager(store, cfg.DefaultQuotaBytes), cfg, nil)
	mux := http.NewServeMux()
	handler.RegisterLocalLaneRoutes(mux, localLanePrincipal)
	return mux, store
}

// localLaneRequest builds a request as it would arrive on the loopback listener.
func localLaneRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

// ---------------------------------------------------------------------------
// Validator hardening: a malformed body must never be stored.
// ---------------------------------------------------------------------------

func TestLocalLanePublishRejectsMalformedBody(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)

	cases := []struct {
		name string
		body []byte
	}{
		{"single junk byte", []byte("X")},
		{"short junk", []byte("JUNKJUNKJUNK")},
		{"json not flatbuffer", []byte(`{"NORAD_CAT_ID":25544,"OBJECT_NAME":"ISS"}`)},
		{"truncated valid buffer", buildMinimalOMM(t)[:9]},
		{"wrong schema identifier", corruptIdentifier(buildMinimalOMM(t))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := localLaneRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", tc.body)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want 4xx; body = %s", rec.Code, rec.Body.String())
			}
		})
	}

	rows, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("query published rows: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("malformed publishes stored %d records, want 0", len(rows))
	}
}

func TestLocalLaneBatchPublishRejectsMalformedRecords(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)

	// Native FlatSQL batch stream: uint32 LE length prefix + record bytes.
	frame := func(rec []byte) []byte {
		var buf bytes.Buffer
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(rec)))
		buf.Write(rec)
		return buf.Bytes()
	}
	body := append(frame([]byte("JUNKJUNKJUNK")), frame(buildMinimalOMM(t))...)

	req := localLaneRequest(http.MethodPost, "/api/v1/data/publish/batch/OMM.fbs", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The valid record stores; the junk one is reported as a per-record error.
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("query published rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d records, want 1 (only the valid record)", len(rows))
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("validation failed")) {
		t.Fatalf("batch response did not report the malformed record: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Happy path: the on-host pipeline's tagged publish lands with its source tags.
// ---------------------------------------------------------------------------

func TestLocalLanePublishStoresWithSourceTags(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)

	target := "/api/v1/data/publish/OMM.fbs" +
		"?source_name=constellation-od" +
		"&provider_id=sdn-od" +
		"&batch_id=batch-abc123" +
		"&source_url=https%3A%2F%2Fcelestrak.org%2FNORAD%2Felements%2Fsupplemental%2Fsup-gp.php"

	req := localLaneRequest(http.MethodPost, target, buildMinimalOMM(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rows, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		SourceName: "constellation-od",
		ProviderID: "sdn-od",
		BatchID:    "batch-abc123",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query tagged rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("tagged rows = %d, want 1", len(rows))
	}

	// The write is attributed to this node's peer ID, so it stays auditable.
	owned, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		PeerID:     localLanePrincipal,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query owned rows: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("rows attributed to the node principal = %d, want 1", len(owned))
	}
}

func TestLocalLaneAdminPublishAliasStoresRecord(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)

	target := "/api/v1/admin/publish?schema=OMM.fbs&source_name=constellation-od&provider_id=sdn-od"
	req := localLaneRequest(http.MethodPost, target, buildMinimalOMM(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		SourceName: "constellation-od",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("alias-published rows = %d, want 1", len(rows))
	}
}

func TestLocalLaneAdminPublishAliasRequiresSchema(t *testing.T) {
	mux, _ := newLocalLaneTestMux(t)

	req := localLaneRequest(http.MethodPost, "/api/v1/admin/publish", buildMinimalOMM(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The lane's authority is the SOCKET. These are the fail-closed backstops that
// keep it from ever becoming an internet-reachable write path.
// ---------------------------------------------------------------------------

func TestLocalLaneRejectsNonLoopbackClient(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)

	req := localLaneRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", buildMinimalOMM(t))
	req.RemoteAddr = "203.0.113.9:44321" // routable client
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("non-loopback client stored %d records, want 0", len(rows))
	}
}

// A reverse proxy in front of this lane is the exact trap the design exists to
// avoid: the proxied request would arrive from 127.0.0.1 and look local. The
// proxy headers it carries are not trusted for auth — their presence alone is
// disqualifying.
func TestLocalLaneRejectsProxiedRequest(t *testing.T) {
	mux, _ := newLocalLaneTestMux(t)

	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "X-Forwarded-Host"} {
		t.Run(header, func(t *testing.T) {
			req := localLaneRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", buildMinimalOMM(t))
			req.RemoteAddr = "127.0.0.1:54321" // nginx would proxy from loopback
			req.Header.Set(header, "203.0.113.9")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLocalLaneRejectsBlankPrincipal(t *testing.T) {
	store, _, validator := newDataAPITestStoreWithBasePath(t)
	cfg := &config.PublishingConfig{Enabled: true, DefaultQuotaBytes: 1 << 20}
	handler := NewPublishHandler(store, validator, NewStorageQuotaManager(store, cfg.DefaultQuotaBytes), cfg, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("RegisterLocalLaneRoutes accepted a blank audit principal")
		}
	}()
	handler.RegisterLocalLaneRoutes(http.NewServeMux(), "   ")
}

// ---------------------------------------------------------------------------
// The PUBLIC publish route must reject junk too. nginx on the prod host has a
// catch-all `location /` and no /api/ block, so /api/v1/data/publish/** is
// internet-reachable — behind auth, but a body that gets past auth must still be
// a real SDS record. A dev daemon previously stored a 1-byte junk OMM POST.
// ---------------------------------------------------------------------------

func TestPublicPublishRouteRejectsMalformedBody(t *testing.T) {
	store, _, validator := newDataAPITestStoreWithBasePath(t)
	cfg := &config.PublishingConfig{
		Enabled:           true,
		DefaultQuotaBytes: 1 << 20,
		MaxRecordBytes:    1 << 20,
		MinTrustLevel:     "untrusted",
	}
	handler := NewPublishHandler(store, validator, NewStorageQuotaManager(store, cfg.DefaultQuotaBytes), cfg, nil)
	mux := http.NewServeMux()
	// The route registration used on a node with admin auth disabled — the same
	// handlePublish + validator the authenticated public route runs behind its wall.
	handler.RegisterUnauthenticatedRoutes(mux, "test-node-peer-id")

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"single junk byte", []byte("X")},
		{"short junk", []byte("JUNKJUNKJUNK")},
		{"json not flatbuffer", []byte(`{"NORAD_CAT_ID":25544}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", bytes.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want 4xx; body = %s", rec.Code, rec.Body.String())
			}
		})
	}

	rows, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("query published rows: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("public route stored %d malformed records, want 0", len(rows))
	}
}

// corruptIdentifier flips the file identifier of a size-prefixed SDS buffer so it
// is structurally intact but claims to be a different schema.
func corruptIdentifier(buf []byte) []byte {
	out := append([]byte(nil), buf...)
	copy(out[8:12], []byte("$XXX"))
	return out
}
