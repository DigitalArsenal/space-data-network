package api

// The table lane against a REAL engine-backed store: two source lanes of OMM
// records, then pagination totals, column sort, per-column filter, global
// search and the network-source selector, each asserted on values the seed
// makes unambiguous.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func newTableTestHandler(t *testing.T) *CoreAPIHandler {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	seed := func(sourceName string, base int, n int, namePrefix string) {
		tags := storage.SourceTags{ProviderID: "test", SourceName: sourceName, BatchID: "b1", ContentKeyID: "public"}
		for i := 0; i < n; i++ {
			record := sds.NewOMMBuilder().
				WithNoradCatID(uint32(base + i)).
				WithObjectName(fmt.Sprintf("%s-%03d", namePrefix, i)).
				WithEpoch("2026-05-12T00:00:00Z").
				Build()
			if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:"+sourceName, nil, tags); err != nil {
				t.Fatalf("store OMM: %v", err)
			}
		}
	}
	seed("lane-alpha", 10000, 30, "ALPHASAT")
	seed("lane-beta", 20000, 12, "BETABIRD")

	return NewCoreAPIHandler("", nil, nil, nil, store, nil, nil, nil, nil)
}

func tablePage(t *testing.T, h *CoreAPIHandler, query string) tableResponse {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/data/table?"+query, nil)
	w := httptest.NewRecorder()
	h.handleTablePage(w, r)
	if w.Code != 200 {
		t.Fatalf("GET ?%s -> %d: %s", query, w.Code, w.Body.String())
	}
	var resp tableResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func colIndex(t *testing.T, resp tableResponse, name string) int {
	t.Helper()
	for i, c := range resp.Columns {
		if c == name {
			return i
		}
	}
	t.Fatalf("column %s missing from %v", name, resp.Columns)
	return -1
}

func TestTableLaneAgainstRealStore(t *testing.T) {
	h := newTableTestHandler(t)

	// Pagination: totals span the whole table, pages honor limit+offset.
	page1 := tablePage(t, h, "schema=OMM&limit=25&page=1")
	if page1.Total != 42 {
		t.Fatalf("total = %d, want 42", page1.Total)
	}
	if len(page1.Rows) != 25 {
		t.Fatalf("page1 rows = %d, want 25", len(page1.Rows))
	}
	page2 := tablePage(t, h, "schema=OMM&limit=25&page=2")
	if len(page2.Rows) != 17 {
		t.Fatalf("page2 rows = %d, want 17", len(page2.Rows))
	}

	// Column sort, ascending: the lowest NORAD id leads.
	sorted := tablePage(t, h, "schema=OMM&limit=5&sort=NORAD_CAT_ID&dir=asc")
	ni := colIndex(t, sorted, "NORAD_CAT_ID")
	if sorted.Rows[0][ni] != "10000" {
		t.Fatalf("asc sort first NORAD = %s, want 10000", sorted.Rows[0][ni])
	}
	sortedDesc := tablePage(t, h, "schema=OMM&limit=5&sort=NORAD_CAT_ID&dir=desc")
	if sortedDesc.Rows[0][ni] != "20011" {
		t.Fatalf("desc sort first NORAD = %s, want 20011", sortedDesc.Rows[0][ni])
	}

	// Network-source selector: each lane answers only its own records.
	alpha := tablePage(t, h, "schema=OMM&source=OMM%40lane-alpha")
	if alpha.Total != 30 {
		t.Fatalf("lane-alpha total = %d, want 30", alpha.Total)
	}
	beta := tablePage(t, h, "schema=OMM&source=OMM%40lane-beta")
	if beta.Total != 12 {
		t.Fatalf("lane-beta total = %d, want 12", beta.Total)
	}

	// Global search hits object names across the table.
	search := tablePage(t, h, "schema=OMM&q=BETABIRD-00")
	if search.Total != 10 {
		t.Fatalf("search total = %d, want 10 (BETABIRD-000..009)", search.Total)
	}

	// Per-column filter composes with the source selector.
	filtered := tablePage(t, h, "schema=OMM&source=OMM%40lane-alpha&f.OBJECT_NAME=ALPHASAT-02")
	if filtered.Total != 10 {
		t.Fatalf("filtered total = %d, want 10 (ALPHASAT-020..029)", filtered.Total)
	}

	// Projection trims the payload to the asked-for columns.
	proj := tablePage(t, h, "schema=OMM&cols=OBJECT_NAME,NORAD_CAT_ID&limit=1")
	if len(proj.Columns) != 2 || len(proj.Rows[0]) != 2 {
		t.Fatalf("projection = %v", proj.Columns)
	}
}

func TestTableSourcesAgainstRealStore(t *testing.T) {
	h := newTableTestHandler(t)
	r := httptest.NewRequest("GET", "/api/v1/data/table/sources?schema=OMM", nil)
	w := httptest.NewRecorder()
	h.handleTableSources(w, r)
	if w.Code != 200 {
		t.Fatalf("sources -> %d: %s", w.Code, w.Body.String())
	}
	var resp tableSourcesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sources) != 2 {
		t.Fatalf("sources = %+v, want 2 lanes", resp.Sources)
	}
	if resp.Sources[0].Source != "OMM@lane-alpha" || resp.Sources[0].Count != 30 {
		t.Fatalf("first lane = %+v, want OMM@lane-alpha count 30", resp.Sources[0])
	}
}

func TestTableLaneRefusesUnknownStandard(t *testing.T) {
	h := newTableTestHandler(t)
	r := httptest.NewRequest("GET", "/api/v1/data/table?schema=ZZZ", nil)
	w := httptest.NewRecorder()
	h.handleTablePage(w, r)
	if w.Code != 404 {
		t.Fatalf("unknown standard -> %d, want 404", w.Code)
	}
}
