package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestCATIndexStaysColdReadableDuringHotWindowMaintenance is the public
// regression for the production failure: the first CAT index request after a
// restart had no bounded-reader value to fall back to and became STORE_BUSY
// while a large, unrelated TBS/OMM hot window held the store write lock.
//
// The test intentionally creates a fresh handler for every probe so no cached
// answer can mask a blocked cold read. The scheduler gate makes the 750 ms
// assertion evidence rather than ambient-load noise; the focused command is
// rerun only while the release coordinator has a quiet machine.
func TestCATIndexStaysColdReadableDuringHotWindowMaintenance(t *testing.T) {
	load, _, _ := gateLoad()
	if load >= 14 {
		t.Skipf("timing-sensitive hot-window reader check requires 1-minute load below 14, got %.2f", load)
	}

	// Seed a normal store, then reopen it the way the production daemon does:
	// compact journal present, engine/control boot work deferred. The public CAT
	// index is already durable; only the unrelated engine hot window is cold.
	seed, basePath, validator := newDataAPITestStoreWithBasePath(t)
	tags := storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "reader-availability",
	}
	cat := sds.NewCATBuilder().
		WithNoradCatID(27559).
		WithObjectName("ALGERIA-READER-PROBE").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := seed.StoreWithSourceTags("CAT.fbs", cat, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store CAT reader probe: %v", err)
	}

	// A batch keeps the setup out of the observation window. The 512 rows force
	// eight maintenance ingest chunks at the production-sized 64-row bound.
	omm := make([][]byte, 0, 512)
	for i := 0; i < 512; i++ {
		omm = append(omm, sds.NewOMMBuilder().
			WithNoradCatID(uint32(70000+i)).
			WithObjectName("HOT-WINDOW-PROBE").
			WithEpoch("2026-09-01T00:00:00Z").
			Build())
	}
	if inserted, err := seed.StoreBatchWithSourceTags("OMM.fbs", omm, "source:hot-window", nil, storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "hot-window-probe",
		BatchID:    "reader-availability",
	}); err != nil || inserted != len(omm) {
		t.Fatalf("seed hot-window rebuild = %d records, %v", inserted, err)
	}
	if err := seed.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint hot-window journal: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close hot-window seed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(basePath, validator,
		storage.WithDeferredBootRebuilds(), storage.WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("open daemon-style deferred store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := store.HydrateEngineHotWindowFromRecordCatalogContext(context.Background())
		done <- err
	}()

	// Do not count a request made before the actual daemon hydration path has
	// started. It is the long journal scan, not a cache hit, that regressed CAT.
	deadline := time.Now().Add(5 * time.Second)
	for !store.EngineHotWindowHydrating() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !store.EngineHotWindowHydrating() {
		t.Fatal("compact engine hot-window hydration never started")
	}

	probes := 0
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("hot-window maintenance: %v", err)
			}
			if probes == 0 {
				t.Fatal("hot-window maintenance completed before a cold CAT index probe ran")
			}
			return
		default:
		}

		mux := http.NewServeMux()
		NewDataQueryHandler(store).RegisterRoutes(mux)
		rec := httptest.NewRecorder()
		started := time.Now()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/data/index?schema=CAT.fbs&provider_id=space-data-network-02&source_name=celestrak-satcat-csv&norad=27559", nil))
		elapsed := time.Since(started)
		if rec.Code != http.StatusOK {
			t.Fatalf("cold CAT index during hot-window maintenance = HTTP %d after %s, want 200 (body=%s)", rec.Code, elapsed, rec.Body.String())
		}
		if elapsed >= storeReadBudget {
			t.Fatalf("cold CAT index took %s, exceeding the %s availability budget", elapsed, storeReadBudget)
		}
		probes++
	}
}
