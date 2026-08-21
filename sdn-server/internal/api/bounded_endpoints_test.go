package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// fakeClock is the bounded reader's injectable clock. Staleness is a property
// of LOGICAL time — how much newer one answer is than the last — never of how
// long the wall clock took between two requests. A loaded box must not decide
// these tests: measured 2026-08-14, each passed in ~1.3s alone and FAILED
// inside the parallel package run at load ~18, when the 2s min-refresh window
// lapsed while the scheduler starved the test goroutine. Advancing the fake
// clock is what crosses (or stays inside) the window.
// See gauntlet-go-host-tier-tests-fail-under-machine-load.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
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
	clock := &fakeClock{t: time.Now()}
	h.indexCache.nowFn = clock.now
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
	// the store is not touched, and the answer says so honestly. The window is
	// crossed in LOGICAL time (the fake clock advances one second no matter
	// how much wall time the machine needed), so a busy box cannot decide
	// this assertion.
	clock.advance(time.Second)
	second := get()
	if second["stale"] != true {
		t.Fatalf("second read reported stale=%v, want true (served from cache)", second["stale"])
	}
	if second["total"] != first["total"] {
		t.Fatalf("cached total %v != fresh total %v", second["total"], first["total"])
	}

	// Cross the window in logical time: the same query must reload. Staleness
	// is a boundary behavior of the reader, not a race with the wall clock.
	clock.advance(10 * time.Second)
	third := get()
	if third["stale"] != false {
		t.Fatalf("third read reported stale=%v, want false (reloaded past the refresh window)", third["stale"])
	}
	if third["total"] != first["total"] {
		t.Fatalf("reloaded total %v != %v", third["total"], first["total"])
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
	clock := &fakeClock{t: time.Now()}
	h.statsCache.nowFn = clock.now

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

	first := get()
	if _, ok := first["stale"]; !ok {
		t.Fatal("stats answer omitted the stale marker; a consumer cannot tell fresh from cached")
	}
	if first["stale"] != false {
		t.Fatalf("first stats read reported stale=%v, want false", first["stale"])
	}
	if first["total_records"] == nil {
		t.Fatal("stats lost total_records")
	}
	if first["as_of"] == nil {
		t.Fatal("stats answer carried no as_of stamp")
	}

	// The second read happens inside the min-refresh window in LOGICAL time
	// (one fake-clock second, whatever the wall clock did) — served from
	// cache, honestly marked stale. A loaded box must not decide this test:
	// measured 2026-08-14, wall-clock gaps between the two reads blew the 2s
	// window under load and flipped the gate on a pristine commit.
	clock.advance(time.Second)
	second := get()
	if second["stale"] != true {
		t.Fatalf("second stats read reported stale=%v, want true (served from cache)", second["stale"])
	}
	if second["total_records"] != first["total_records"] {
		t.Fatalf("cached total_records %v != %v", second["total_records"], first["total_records"])
	}
	// The peers block is host state, never store state: it must stay live even
	// when the store-derived blocks are being served from cache.
	if _, ok := second["peers"]; !ok {
		t.Fatal("cached stats answer dropped the peers block")
	}

	// Cross the refresh window in logical time: the next identical read must
	// reload rather than keep serving the cache — a boundary behavior, never
	// a race with the wall clock.
	clock.advance(10 * time.Second)
	third := get()
	if third["stale"] != false {
		t.Fatalf("third stats read reported stale=%v, want false (reloaded past the refresh window)", third["stale"])
	}
	if third["total_records"] != first["total_records"] {
		t.Fatalf("reloaded total_records %v != %v", third["total_records"], first["total_records"])
	}
}
