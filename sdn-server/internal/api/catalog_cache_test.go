package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestCatalogServesFromShortCacheAndExposesIndexState(t *testing.T) {
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	if _, err := store.Store("OMM.fbs", buildMinimalOMM(t), "12D3KooWCatalogTestPeer", nil); err != nil {
		t.Fatalf("store record: %v", err)
	}
	h := NewCatalogHandler(store, "", &config.Config{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("catalog status %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode catalog: %v", err)
		}
		return body
	}

	first := get()
	if first["catalog_cached"] != false {
		t.Fatalf("first response must compute: %v", first["catalog_cached"])
	}
	second := get()
	if second["catalog_cached"] != true {
		t.Fatalf("second response within the TTL must be served from cache: %v", second["catalog_cached"])
	}
	h.SetCacheTTL(0)
	if third := get(); third["catalog_cached"] != false {
		t.Fatalf("a zero TTL must disable the cache: %v", third["catalog_cached"])
	}

	schemas, _ := first["schemas"].([]any)
	if len(schemas) == 0 {
		t.Fatal("catalog lists no schemas after a store")
	}
	for _, entry := range schemas {
		m := entry.(map[string]any)
		state, _ := m["index_state"].(string)
		switch state {
		case "ready", "building", "failed", "cold":
		default:
			t.Fatalf("schema %v carries no index_state (%q)", m["name"], state)
		}
	}
}
