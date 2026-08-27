package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// gateLoadAverage reports the 1-minute load average the gate saw when it
// started this package, or 0 when the tests were not run through the gate.
// graph/gauntlet/go-tier.mjs exports GO_TIER_LOADAVG1 for exactly this: a test
// whose premise is a wall-clock window has to be able to say whether the box
// was in a position to honour it (gauntlet-go-host-tier-tests-fail-under-
// machine-load, 2026-08-27).
func gateLoadAverage() float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("GO_TIER_LOADAVG1")), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// These tests pin the WIRING: the two anonymous store-backed surfaces really
// do go through the bounded reader, really do report their own staleness, and
// really do serve a second identical request without touching the store again.
// The budget behaviour itself is covered in boundedread_test.go.

func boundedTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "boundedfixture-gp",
		BatchID:      "batch-001",
		ContentKeyID: "public",
	}
	for _, record := range [][]byte{
		sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").WithEpoch("2026-05-12T00:00:00Z").Build(),
		sds.NewOMMBuilder().WithNoradCatID(48274).WithObjectName("TIANHE").WithEpoch("2026-05-12T01:00:00Z").Build(),
	} {
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-test", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags failed: %v", err)
		}
	}
	return store
}

func TestRecordIndexIsServedThroughTheBoundedReader(t *testing.T) {
	h := NewDataQueryHandler(boundedTestStore(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	get := func() map[string]interface{} {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/data/index?schema=OMM&limit=10", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	first := get()
	if first["stale"] != false {
		t.Fatalf("first read reported stale=%v, want false", first["stale"])
	}
	if first["as_of"] == nil {
		t.Fatal("answer carried no as_of stamp")
	}
	rows, _ := first["rows"].([]interface{})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	// Inside the min-refresh window the same query is answered from cache —
	// the store is not touched, and the answer says so honestly.
	second := get()
	if second["stale"] != true {
		t.Fatalf("second read reported stale=%v, want true (served from cache)", second["stale"])
	}
	if second["total"] != first["total"] {
		t.Fatalf("cached total %v != fresh total %v", second["total"], first["total"])
	}

	// A different query must not be answered from the first query's slot.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/data/index?schema=OMM&limit=10&norad=25544", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered status = %d", rec.Code)
	}
	var filtered map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if filtered["total"] == first["total"] {
		t.Fatalf("filtered query reused the unfiltered cache slot (total=%v)", filtered["total"])
	}
	if filtered["stale"] != false {
		t.Fatalf("distinct query reported stale=%v, want a fresh load", filtered["stale"])
	}
}

func TestStatsReportsItsOwnStaleness(t *testing.T) {
	store := boundedTestStore(t)
	h := NewCoreAPIHandler("", nil, nil, nil, store, nil, nil, nil, nil)

	get := func() map[string]interface{} {
		t.Helper()
		rec := httptest.NewRecorder()
		h.handleStats(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	// STRUCTURAL CLAIMS. True of every answer at every load: the shape of the
	// response is a function of the code, not of the scheduler.
	probe := get()
	if _, ok := probe["stale"]; !ok {
		t.Fatal("stats answer omitted the stale marker; a consumer cannot tell fresh from cached")
	}
	if probe["total_records"] == nil {
		t.Fatal("stats lost total_records")
	}
	if _, ok := probe["peers"]; !ok {
		t.Fatal("stats answer dropped the peers block")
	}

	// TIMED CLAIM. "The second read is served from cache" is only true inside
	// storeReadMinRefresh (2 s) of a FRESH first read, and "the first read is
	// fresh" is only true when the load finishes inside storeReadBudget
	// (750 ms). Both windows are wall clock, and on a box running eight other
	// lanes they can elapse between two adjacent statements — which is how this
	// test came to decide the whole sdn go-host-tier lane by ambient load
	// (measured 2026-08-14: PASS at load ~1, FAIL at load 19.35, 1.3 s in
	// isolation either way). So the premise is MEASURED rather than assumed:
	// the assertion speaks only when the box actually honoured the window, and
	// a box that never does says so instead of voting red.
	const attempts = 5
	var first, second map[string]interface{}
	var gap time.Duration
	var firstStale interface{}
	for i := 0; i < attempts; i++ {
		// Start each attempt outside the refresh window of the previous one so
		// the first read of the pair is genuinely a cold load.
		if i > 0 {
			time.Sleep(storeReadMinRefresh + 250*time.Millisecond)
		}
		f := get()
		loaded := time.Now()
		s := get()
		firstStale = f["stale"]
		if f["stale"] == false && time.Since(loaded) < storeReadMinRefresh {
			first, second, gap = f, s, time.Since(loaded)
			break
		}
	}
	if first == nil {
		t.Skipf("the bounded-read windows (budget %v, refresh %v) never held across %d attempts on this box "+
			"(last first-read stale=%v, gate 1-min loadavg %.2f): the scheduler, not handleStats, decided the timing — "+
			"nothing here is attributable to the candidate",
			storeReadBudget, storeReadMinRefresh, attempts, firstStale, gateLoadAverage())
	}

	if first["as_of"] == nil {
		t.Fatal("stats answer carried no as_of stamp")
	}
	if second["stale"] != true {
		t.Fatalf("second stats read %v after the first reported stale=%v, want true (served from cache within the %v refresh window)",
			gap, second["stale"], storeReadMinRefresh)
	}
	if second["total_records"] != first["total_records"] {
		t.Fatalf("cached total_records %v != %v", second["total_records"], first["total_records"])
	}
	// The peers block is host state, never store state: it must stay live even
	// when the store-derived blocks are being served from cache.
	if _, ok := second["peers"]; !ok {
		t.Fatal("cached stats answer dropped the peers block")
	}
}
