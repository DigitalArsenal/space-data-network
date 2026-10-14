package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Coordinator D2 (multi-body round 2): the unfiltered OMM bulk list is the
// satellite catalog; a named-only lane appears only when named. (Asteroids are
// $SBD, never $OMM; the default named-only lanes are the beyond-Earth ones,
// and the OMM case is configured here.)
func TestBulkExportLeavesNamedOnlyLanesOutUnlessNamed(t *testing.T) {
	store := newDataAPITestStore(t)
	for i := 0; i < 3; i++ {
		storeDataAPITestOMMInto(t, store, uint32(25544+i), "SAT", "2026-05-05")
	}
	for i, name := range []string{"Ceres", "Pallas"} {
		payload := sds.NewOMMBuilder().WithNoradCatID(uint32(900000 + i)).WithObjectName(name).WithEpoch("2026-05-05T12:00:00Z").Build()
		tags := storage.SourceTags{ProviderID: "operator-elements", SourceName: "operator-omm", SourceURL: "https://example.org/elements", BatchID: "operator-batch"}
		if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:operator", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	count := func(h *DataQueryHandler, path string) (int, []string) {
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		rec := serveDataAPIRequest(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", path, rec.Code, rec.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r["OBJECT_NAME"].(string))
		}
		return len(rows), names
	}
	const at = "&epoch=2026-05-05T12:00:00Z&format=json"
	scoped := NewDataQueryHandler(store)
	scoped.SetNamedOnlyLanes(append(config.DefaultNamedOnlyLanes(), config.LaneSelector{Schema: "OMM.fbs", ProviderID: "operator-elements"}))
	if n, names := count(scoped, "/api/v1/data/omm/bulk?limit=100"+at); n != 3 {
		t.Fatalf("unfiltered list: %d rows %v, want the 3 satellites", n, names)
	}
	if n, _ := count(scoped, "/api/v1/data/omm/bulk?provider_id=operator-elements&limit=100"+at); n != 2 {
		t.Fatalf("named lane: %d rows, want 2", n)
	}
	if n, _ := count(scoped, "/api/v1/data/omm/bulk?limit=2"+at); n != 2 {
		t.Fatalf("limit not applied after the lane merge: %d rows", n)
	}
	// Scope off (lanes.named_only: []): everything, as before.
	if n, _ := count(NewDataQueryHandler(store), "/api/v1/data/omm/bulk?limit=100"+at); n != 5 {
		t.Fatalf("unscoped list: %d rows, want 5", n)
	}
}
