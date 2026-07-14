package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestPublishHandlerExplicitUnauthenticatedRoutesUseAuditablePrincipal(t *testing.T) {
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	cfg := &config.PublishingConfig{
		Enabled:           true,
		DefaultQuotaBytes: 1 << 20,
		MinTrustLevel:     "untrusted",
	}
	quotas := NewStorageQuotaManager(store, cfg.DefaultQuotaBytes)
	handler := NewPublishHandler(store, nil, quotas, cfg, nil)
	mux := http.NewServeMux()
	handler.RegisterUnauthenticatedRoutes(mux, "test-node-peer-id")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", bytes.NewReader(buildMinimalOMM(t)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		PeerID:     "test-node-peer-id",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query published rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("published rows = %d, want 1", len(rows))
	}
}

func TestPublishHandlerExplicitUnauthenticatedRoutesRejectBlankPrincipal(t *testing.T) {
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	cfg := &config.PublishingConfig{Enabled: true, DefaultQuotaBytes: 1 << 20}
	handler := NewPublishHandler(store, nil, NewStorageQuotaManager(store, cfg.DefaultQuotaBytes), cfg, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("RegisterUnauthenticatedRoutes accepted a blank audit principal")
		}
	}()
	handler.RegisterUnauthenticatedRoutes(http.NewServeMux(), "  ")
}
