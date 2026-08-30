package flowrt

// End-to-end cellular worldwide ingest, over the REAL compiled bundle
// (space-data-network-modules flows/cellular-network-ingest), the REAL http +
// storage capability handlers and a REAL FlatSQL store — the same shape the
// CelesTrak ingest tests use, for the same reason: the host owns none of this
// and the only way to know the lane works is to run it.
//
// WHY THIS EXISTS (graph: sdn-cellular-ingest-lands-no-batch). host-02 ran this
// flow three times against the Mozilla Location Service final full cell export.
// Every run fetched exactly 3,145,728 B (206, 883 ms — in the node's own fetch
// ledger), ran ~177 s, stored NOTHING, and reported "run completed but landed
// no batch". The node config named an MLS provider cell-tower-source's registry
// had no entry for; `parse` dropped the body and returned clean.
// Nothing in the pipeline was broken except the one thing nobody could see.
//
// So the assertion here is a COUNT AND A TAG, never "more than zero": the rows
// must land, and they must land under the provider that actually served them.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// cellularIngestFlowDist resolves the compiled cellular ingest bundle.
func cellularIngestFlowDist(t *testing.T) string {
	t.Helper()
	dist := os.Getenv("SDN_CELLULAR_INGEST_FLOW_DIST")
	if dist == "" {
		dist = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "cellular-network-ingest", "dist")
	}
	if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err != nil {
		t.Skipf("cellular-network-ingest bundle not found at %s (set SDN_CELLULAR_INGEST_FLOW_DIST): %v", dist, err)
	}
	return dist
}

// cellularBulkFixture reads the gzipped bulk-export slice cell-tower-source is
// itself tested against: 16 data rows, one out of range and dropped, 15 cells
// collapsing to SIX sites. It carries the exact column contract the live MLS
// export serves (verified against the real file 2026-08-26).
func cellularBulkFixture(t *testing.T) []byte {
	t.Helper()
	root := os.Getenv("SDN_CELL_TOWER_SOURCE_FIXTURES")
	if root == "" {
		root = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "data-source", "cell-tower-source", "tests", "fixtures")
	}
	body, err := os.ReadFile(filepath.Join(root, "opencellid-bulk.slice.csv.gz"))
	if err != nil {
		t.Skipf("cell-tower-source bulk fixture unavailable: %v", err)
	}
	return body
}

// rangeOrigin serves one body with real HTTP Range semantics, because the whole
// ingest design rests on them: the flow asks for `bytes=A-B` and reads the
// object size back out of `Content-Range`'s denominator. An origin that ignored
// Range would answer 200 with the whole body and the run would look fine while
// testing nothing.
func rangeOrigin(t *testing.T, body []byte, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Accept-Ranges", "bytes")
		spec := r.Header.Get("Range")
		if !strings.HasPrefix(spec, "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		var first, last int
		if _, err := fmt.Sscanf(strings.TrimPrefix(spec, "bytes="), "%d-%d", &first, &last); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if first >= len(body) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if last >= len(body) {
			last = len(body) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[first : last+1])
	}))
}

func cellularNodeConfig(url string, chunkBytes int) map[string]interface{} {
	// The host-02 config, verbatim in shape — including the engine-routed mark
	// query, which is the form a current node answers (`_data` off the
	// generated unified view named by the SDS code, never `sds_irm`/`rowid`).
	return map[string]interface{}{
		"cell_ingest_url":             url,
		"cell_ingest_provider_id":     "mls-archive",
		"cell_ingest_source_name":     "mls-final-full-cell-export",
		"cell_ingest_format":          "csv-gz",
		"cell_ingest_http_timeout_ms": "60000",
		"cell_ingest_mark_sql":        "SELECT _data FROM IRM ORDER BY _rowid DESC LIMIT ?",
		"cell_ingest_mark_scan_rows":  "32",
		"cell_ingest_chunk_bytes":     chunkBytes,
	}
}

// loadCellularService loads the bundle with ALL four capabilities it declares
// approved. The shared CelesTrak helper approves only http + storage_ingest,
// and this flow additionally reads the resume mark (storage_query) and writes
// it (storage_write) — the policy is fail-closed, so an unapproved capability
// refuses the load outright rather than degrading the run.
func loadCellularService(t *testing.T, h *celestrakIngestHarness, dist string, nodeConfig map[string]interface{}) *ServiceFlow {
	return loadCellularServiceWithPages(t, h, dist, nodeConfig, 4096)
}

// loadCellularServiceWithPages takes the linear-memory ceiling explicitly,
// because on this lane the ceiling IS a behaviour: a chunk whose decode does not
// fit stops mid-flow with no error, and the pages a box is configured with
// decide which chunk size is safe there. host-02 runs 8192.
func loadCellularServiceWithPages(t *testing.T, h *celestrakIngestHarness, dist string, nodeConfig map[string]interface{}, pages uint32) *ServiceFlow {
	t.Helper()
	policy := approvedCapabilityPolicy(t, dist, "http", "storage_ingest", "storage_query", "storage_write")
	loaded, err := LoadFlowServices([]config.FlowService{{
		Flow: dist, Config: nodeConfig, MemoryPages: pages,
	}}, FlowMountDeps{
		CapRegistry:    h.reg,
		NodeCtx:        &modulert.NodeContext{CapabilityPolicy: policy},
		MaxMemoryPages: pages,
	})
	if err != nil {
		t.Fatalf("LoadFlowServices(%s): %v", dist, err)
	}
	service := loaded[0]
	t.Cleanup(func() { service.Close() })
	return service
}

// fireCellular runs every timer trigger the service declares and fails on an
// error envelope in the egress summary.
func fireCellular(t *testing.T, service *ServiceFlow) {
	t.Helper()
	for _, trigger := range service.Triggers() {
		summary, err := service.FireTrigger(t.Context(), trigger.TriggerID)
		if err != nil {
			t.Fatalf("FireTrigger(%s): %v", trigger.TriggerID, err)
		}
		if strings.Contains(string(summary), `"error"`) {
			t.Fatalf("trigger %s produced an error frame: %s", trigger.TriggerID, summary)
		}
		t.Logf("trigger %s egress: %s", trigger.TriggerID, summary)
	}
}

// TestCellularIngestFlowLandsSitesUnderTheConfiguredProvider is the regression
// for the silent zero-record run.
func TestCellularIngestFlowLandsSitesUnderTheConfiguredProvider(t *testing.T) {
	dist := cellularIngestFlowDist(t)
	body := cellularBulkFixture(t)
	harness := newCelestrakIngestHarness(t)

	hits := 0
	origin := rangeOrigin(t, body, &hits)
	defer origin.Close()

	// One chunk wide enough for the whole fixture: this test is about the lane
	// landing rows at all, not about the seam (the module's own suite owns
	// that).
	service := loadCellularService(t, harness, dist, cellularNodeConfig(origin.URL+"/mls.csv.gz", len(body)+4096))
	fireCellular(t, service)
	// The node digest is the first thing to read when this fails: it names the
	// node that refused or the node that never ran.
	t.Logf("node digest: %v", service.retrievalDiagnosis(nil))
	for _, n := range service.lastNodeDigest {
		t.Logf("  node %-12s %-52s invocations=%d status=%d", n.NodeID, n.PluginID+":"+n.MethodID, n.Invocations, n.LastStatus)
	}

	if hits == 0 {
		t.Fatal("the run never fetched anything — the pipeline died before the http node")
	}

	counts := harness.sourceTagCounts(t)
	var landed int64
	for tag, count := range counts {
		t.Logf("landed %s = %d", tag, count)
		if strings.HasPrefix(tag, "TBS.fbs|mls-archive|") {
			landed += count
		}
	}
	// SIX collapsed sites, exactly — the number cell-tower-source's own bulk
	// tests pin. A different number means the collapse changed, not that the
	// lane "works".
	if landed != 6 {
		t.Fatalf("expected 6 $TBS sites tagged provider mls-archive, got %d (all tags: %v)", landed, counts)
	}
	// ATTRIBUTION. The MLS export shares a decoder with the OpenCelliD bulk
	// export; it must not share an identity, or stored records carry a licence
	// and an authority that never asserted them.
	for tag := range counts {
		if strings.HasPrefix(tag, "TBS.fbs|opencellid") {
			t.Fatalf("MLS rows landed under an OpenCelliD tag: %s", tag)
		}
	}
}

// TestCellularIngestFlowWritesTheDurableResumeMark asserts the other half of
// the loop: without a durable $IRM the next tick refetches chunk 0 forever.
func TestCellularIngestFlowWritesTheDurableResumeMark(t *testing.T) {
	dist := cellularIngestFlowDist(t)
	body := cellularBulkFixture(t)
	harness := newCelestrakIngestHarness(t)

	hits := 0
	origin := rangeOrigin(t, body, &hits)
	defer origin.Close()

	fireCellular(t, loadCellularService(t, harness, dist, cellularNodeConfig(origin.URL+"/mls.csv.gz", len(body)+4096)))

	// The mark is written through storage.write, not storage.ingest_with_source,
	// so it carries no source tags and never appears in the batch summary — it
	// is counted in the store itself.
	marks, err := harness.store.Count("IRM")
	if err != nil {
		t.Fatalf("count IRM: %v", err)
	}
	if marks != 1 {
		t.Fatalf("expected exactly ONE $IRM resume mark after one chunk, got %d; without it every later tick refetches chunk 0", marks)
	}
	// And the chunk it says to resume from must be the NEXT one, never the one
	// just read: a mark that does not advance is a run that never progresses.
	records, err := harness.store.QueryAll("IRM", 4)
	if err != nil || len(records) != 1 {
		t.Fatalf("read back the mark: %v (%d records)", err, len(records))
	}
	if len(records[0]) == 0 {
		t.Fatal("the persisted $IRM record is empty")
	}
}

// TestCellularIngestFlowNamesAnUnknownProviderInTheLedger closes the loop this
// task was opened for: the failure that used to be invisible must now be
// written down, by name, without the host knowing what a provider IS.
func TestCellularIngestFlowNamesAnUnknownProviderInTheLedger(t *testing.T) {
	dist := cellularIngestFlowDist(t)
	body := cellularBulkFixture(t)
	harness := newCelestrakIngestHarness(t)

	hits := 0
	origin := rangeOrigin(t, body, &hits)
	defer origin.Close()

	nodeConfig := cellularNodeConfig(origin.URL+"/mls.csv.gz", len(body)+4096)
	nodeConfig["cell_ingest_provider_id"] = "mls-typo"

	service := loadCellularService(t, harness, dist, nodeConfig)

	trigger := service.Triggers()[0]
	// The run itself still returns cleanly: a guest node's refusal travels
	// through the in-wasm scheduler, not the host loop, and the host does NOT
	// reinterpret that as a transport failure.
	if _, err := service.FireTrigger(t.Context(), trigger.TriggerID); err != nil {
		t.Fatalf("FireTrigger: %v", err)
	}
	// ...but the run is no longer mute. This is the text an operator reads out
	// of the source-metrics ledger instead of "run completed but landed no
	// batch" and nothing else.
	diagnosis := service.retrievalDiagnosis(nil)
	if diagnosis == nil {
		t.Fatal("a run that stored nothing because of a bad provider id reported no cause at all")
	}
	got := diagnosis.Error()
	if !strings.Contains(got, "cell-tower-source") || !strings.Contains(got, "parse") {
		t.Fatalf("the diagnosis does not name the node that refused: %s", got)
	}
	t.Logf("ledger reason: %s", got)
}
