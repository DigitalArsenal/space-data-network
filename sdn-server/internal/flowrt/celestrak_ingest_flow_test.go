package flowrt

// End-to-end CelesTrak retrieval, as it runs in production: the REAL compiled
// ingest flow bundles (space-data-network-modules flows/celestrak-ingest) load
// as timer-served flow SERVICES over the REAL http + storage_ingest capability
// handlers and a REAL FlatSQL store. Firing the timer trigger drives the whole
// cycle — request build (in wasm) -> host HTTP egress -> parse + provenance
// (in wasm) -> host guarded persistence — and the records land under their
// CelesTrak source tags.
//
// The host contributes nothing but the tick, the socket and the disk. Every
// URL, every field mapping, every provenance stamp comes out of the wasm nodes,
// which is why there is no Go CelesTrak fetcher to test.
//
// The source URL is redirected to a local httptest server through the flow
// service's node CONFIG block (plugin.getConfig), which is exactly how an
// operator retargets a source in production — configuration, never host code.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// celestrakFlowDist resolves one compiled CelesTrak ingest bundle
// (dist/gp, dist/satcat, dist/spw).
func celestrakFlowDist(t *testing.T, variant string) string {
	t.Helper()
	root := os.Getenv("SDN_CELESTRAK_INGEST_FLOW_DIST")
	if root == "" {
		root = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "celestrak-ingest", "dist")
	}
	dist := filepath.Join(root, variant)
	if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err != nil {
		t.Skipf("celestrak-ingest %s bundle not found at %s (set SDN_CELESTRAK_INGEST_FLOW_DIST): %v", variant, dist, err)
	}
	return dist
}

// celestrakFixture reads a parser test fixture (the CelesTrak payload shapes
// the parser module is itself tested against).
func celestrakFixture(t *testing.T, name string) []byte {
	t.Helper()
	root := os.Getenv("SDN_CELESTRAK_PARSER_FIXTURES")
	if root == "" {
		root = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "data-source", "celestrak-parser", "tests", "fixtures")
	}
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Skipf("celestrak parser fixture %s unavailable: %v", name, err)
	}
	return body
}

// celestrakIngestHarness wires a real store + the production capability
// handlers the ingest flows require.
type celestrakIngestHarness struct {
	store   *storage.FlatSQLStore
	reg     *modulert.CapabilityRegistry
	rawRoot string
}

func newCelestrakIngestHarness(t *testing.T) *celestrakIngestHarness {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rawRoot := t.TempDir()
	reg := modulert.NewCapabilityRegistry()
	storageFac := caps.NewStorageCapFactoryWithOptions(store, caps.StorageCapOptions{
		RawRoot: rawRoot,
		// A tiny floor: the disk guardrail must not skip the test on a CI box
		// with a modest free-space margin, but it must still be exercised.
		MinFreeDiskBytes: 1,
	})
	reg.RegisterBridgeAware("storage_ingest", storageFac)
	reg.RegisterBridgeAware("storage_query", storageFac)
	reg.RegisterBridgeAware("storage_write", storageFac)
	reg.Register("http", caps.NewHTTPCapFactory())

	return &celestrakIngestHarness{store: store, reg: reg, rawRoot: rawRoot}
}

// runIngestService loads one bundle as a flow service with the given node
// CONFIG and fires every timer trigger it declares.
func (h *celestrakIngestHarness) runIngestService(t *testing.T, dist string, nodeConfig map[string]interface{}) *ServiceFlow {
	t.Helper()
	policy := approvedCapabilityPolicy(t, dist, "http", "storage_ingest")
	services := []config.FlowService{{
		Flow:        dist,
		Config:      nodeConfig,
		MemoryPages: 4096,
	}}
	loaded, err := LoadFlowServices(services, FlowMountDeps{
		CapRegistry:    h.reg,
		NodeCtx:        &modulert.NodeContext{CapabilityPolicy: policy},
		MaxMemoryPages: 4096,
	})
	if err != nil {
		t.Fatalf("LoadFlowServices(%s): %v", dist, err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d services, want 1", len(loaded))
	}
	service := loaded[0]
	t.Cleanup(func() { service.Close() })

	triggers := service.Triggers()
	if len(triggers) == 0 {
		t.Fatalf("service %s declares no timer triggers", service.ID())
	}
	for _, trigger := range triggers {
		summary, err := service.FireTrigger(t.Context(), trigger.TriggerID)
		if err != nil {
			t.Fatalf("FireTrigger(%s): %v", trigger.TriggerID, err)
		}
		// The egress summary carries every ingest result frame. A capability
		// rejection or a guardrail refusal surfaces here as an error envelope
		// rather than as a failed call, so it must be inspected.
		if strings.Contains(string(summary), `"error"`) {
			t.Fatalf("trigger %s produced an error frame: %s", trigger.TriggerID, summary)
		}
		t.Logf("trigger %s egress: %s", trigger.TriggerID, summary)
	}
	return service
}

// sourceTagCounts summarizes what landed, per (schema, provider, source).
func (h *celestrakIngestHarness) sourceTagCounts(t *testing.T) map[string]int64 {
	t.Helper()
	progress, err := h.store.SourceBatchProgress()
	if err != nil {
		t.Fatalf("SourceBatchProgress: %v", err)
	}
	counts := map[string]int64{}
	for _, p := range progress {
		counts[fmt.Sprintf("%s|%s|%s", p.SchemaName, p.ProviderID, p.SourceName)] += p.Count
	}
	return counts
}

// TestCelesTrakGPIngestFlowRetrievesOMM drives one full GP pull cycle and
// asserts OMM records land tagged as RETRIEVED from celestrak-gp.
func TestCelesTrakGPIngestFlowRetrievesOMM(t *testing.T) {
	dist := celestrakFlowDist(t, "gp")
	fixture := celestrakFixture(t, "celestrak-gp-omm.csv")
	h := newCelestrakIngestHarness(t)

	var requests int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/csv")
		w.Write(fixture)
	}))
	defer origin.Close()

	h.runIngestService(t, dist, map[string]interface{}{
		"celestrak_gp_url":          origin.URL + "/NORAD/elements/gp.php",
		"celestrak_provider_id":     "space-data-network-02",
		"celestrak_http_timeout_ms": 30000,
	})

	if requests != 1 {
		t.Fatalf("origin saw %d requests, want exactly 1 per tick", requests)
	}

	counts := h.sourceTagCounts(t)
	omm := counts["OMM.fbs|space-data-network-02|celestrak-gp"]
	if omm != 2 {
		t.Fatalf("OMM records tagged celestrak-gp = %d, want 2 (fixture rows); counts=%v", omm, counts)
	}
	// The GP flow also derives mean-element records from the same payload;
	// they must carry the SAME provenance, never a synthesized one.
	if mpe := counts["MPE.fbs|space-data-network-02|celestrak-gp"]; mpe != 2 {
		t.Fatalf("MPE records tagged celestrak-gp = %d, want 2; counts=%v", mpe, counts)
	}

	// Raw payload archived under the source name — the retrieved bytes are
	// kept verbatim so a record's provenance can be re-derived.
	var archived bool
	filepath.Walk(h.rawRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(path, "catalog.csv") {
			archived = true
		}
		return nil
	})
	if !archived {
		t.Fatalf("no raw catalog.csv archived under %s", h.rawRoot)
	}
}

// TestCelesTrakGPIngestFlowFeedsSourceMetrics runs the same real cycle with the
// operational ledger attached and asserts the $APPS feed's numbers come out of
// an ACTUAL retrieval — not a hand-written fixture. This is the contract Iris's
// widget reads: last_retrieved_at, debounce_hours, last_pull_size_bytes, all
// keyed by source id.
func TestCelesTrakGPIngestFlowFeedsSourceMetrics(t *testing.T) {
	dist := celestrakFlowDist(t, "gp")
	fixture := celestrakFixture(t, "celestrak-gp-omm.csv")
	h := newCelestrakIngestHarness(t)

	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()

	caps.SetFetchObserver(func(url string, status int, bytes, durationMs int64, errMsg string) {
		ledger.RecordFetch(sourcemetrics.Fetch{URL: url, Status: status, Bytes: bytes, DurationMs: durationMs, Err: errMsg})
	})
	caps.SetIngestObserver(func(obs caps.IngestObservation) {
		ledger.RecordIngest(sourcemetrics.Ingest{
			AppID: obs.ProducerID, ProviderID: obs.ProviderID, SourceName: obs.SourceName,
			SourceURL: obs.SourceURL, Schema: obs.Schema, BatchID: obs.BatchID,
			PullBytes: obs.PullBytes, Records: obs.Records, Inserted: obs.Inserted,
		})
	})
	defer func() {
		caps.SetFetchObserver(nil)
		caps.SetIngestObserver(nil)
	}()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Write(fixture)
	}))
	defer origin.Close()

	config := map[string]interface{}{
		"celestrak_gp_url":          origin.URL + "/NORAD/elements/gp.php",
		"celestrak_provider_id":     "space-data-network-02",
		"celestrak_http_timeout_ms": 30000,
	}
	h.runIngestService(t, dist, config)

	sources, err := ledger.Sources()
	if err != nil {
		t.Fatalf("ledger.Sources: %v", err)
	}
	var gp *sourcemetrics.Source
	for i := range sources {
		if sources[i].SourceName == "celestrak-gp" {
			gp = &sources[i]
		}
	}
	if gp == nil {
		t.Fatalf("no ledger row for celestrak-gp; rows=%+v", sources)
	}
	if gp.SourceID != "space-data-network-02/celestrak-gp" {
		t.Fatalf("source_id = %q, want space-data-network-02/celestrak-gp", gp.SourceID)
	}
	if gp.Origin != "retrieved" {
		t.Fatalf("origin = %q, want retrieved", gp.Origin)
	}
	if gp.LastRetrievedAt == nil {
		t.Fatal("last_retrieved_at is unset after a completed pull")
	}
	if gp.DebounceHours != sourcemetrics.DefaultDebounceHours {
		t.Fatalf("debounce_hours = %v, want %v", gp.DebounceHours, sourcemetrics.DefaultDebounceHours)
	}
	// The pull size is the RETRIEVED payload, byte for byte — not the size of
	// the normalized records.
	if gp.LastPullSizeBytes != int64(len(fixture)) {
		t.Fatalf("last_pull_size_bytes = %d, want %d (the fetched payload)", gp.LastPullSizeBytes, len(fixture))
	}
	if gp.LastStatus != http.StatusOK {
		t.Fatalf("last_status = %d, want 200", gp.LastStatus)
	}
	if gp.LastBatchID == "" || gp.LastBatchRepeated {
		t.Fatalf("first pull: batch_id=%q repeated=%v, want a hash and repeated=false", gp.LastBatchID, gp.LastBatchRepeated)
	}
	if gp.AppID == "" {
		t.Fatalf("app_id is empty; the $APPS feed cannot attribute this source to its app")
	}
	firstBatch := gp.LastBatchID

	// Same payload again: the fetch is real, the CONTENT is unchanged, and the
	// ledger says so. This is the honest debounce signal — the node re-checked
	// the publisher and nothing had been republished.
	h.runIngestService(t, dist, config)
	sources, err = ledger.Sources()
	if err != nil {
		t.Fatalf("ledger.Sources (second pull): %v", err)
	}
	for i := range sources {
		if sources[i].SourceName != "celestrak-gp" {
			continue
		}
		if sources[i].LastBatchID != firstBatch {
			t.Fatalf("unchanged payload changed batch id: %q -> %q", firstBatch, sources[i].LastBatchID)
		}
		if !sources[i].LastBatchRepeated {
			t.Fatal("second pull of identical content did not set last_batch_repeated")
		}
		if sources[i].IngestCount < 2 {
			t.Fatalf("ingest_count = %d after two pulls, want >= 2", sources[i].IngestCount)
		}
	}
}

// TestCelesTrakSatcatIngestFlowRetrievesCAT drives one full SATCAT pull cycle.
// One tick fetches BOTH published SATCAT forms (legacy fixed-width + CSV), so
// this also proves the two-request-per-tick shape the egress pacer must space.
func TestCelesTrakSatcatIngestFlowRetrievesCAT(t *testing.T) {
	dist := celestrakFlowDist(t, "satcat")
	csvFixture := celestrakFixture(t, "celestrak-satcat.csv")
	txtFixture := celestrakFixture(t, "celestrak-satcat.txt")
	h := newCelestrakIngestHarness(t)

	var txtHits, csvHits int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".csv") {
			csvHits++
			w.Header().Set("Content-Type", "text/csv")
			w.Write(csvFixture)
			return
		}
		txtHits++
		w.Header().Set("Content-Type", "text/plain")
		w.Write(txtFixture)
	}))
	defer origin.Close()

	h.runIngestService(t, dist, map[string]interface{}{
		"celestrak_satcat_url":      origin.URL + "/pub/satcat.txt",
		"celestrak_satcat_csv_url":  origin.URL + "/pub/satcat.csv",
		"celestrak_provider_id":     "space-data-network-02",
		"celestrak_http_timeout_ms": 30000,
	})

	if txtHits != 1 || csvHits != 1 {
		t.Fatalf("satcat tick fetched txt=%d csv=%d, want 1 each", txtHits, csvHits)
	}

	counts := h.sourceTagCounts(t)
	// Both SATCAT forms are distinct SOURCES under one provider — the ledger
	// and the records agree on that split.
	if got := counts["CAT.fbs|space-data-network-02|celestrak-satcat-csv"]; got != 2 {
		t.Fatalf("CAT records from celestrak-satcat-csv = %d, want 2; counts=%v", got, counts)
	}
	if got := counts["CAT.fbs|space-data-network-02|celestrak-satcat"]; got < 1 {
		t.Fatalf("CAT records from celestrak-satcat (fixed-width) = %d, want >= 1; counts=%v", got, counts)
	}
}

// TestCelesTrakIngestFlowConfiguredURLSurvivesTheHostcall guards a defect that
// silently disabled every operator URL override: the hostcall envelope encoder
// HTML-escaped "&", and the guest's minimal JSON reader does not decode \u
// escapes, so a configured "gp.php?GROUP=stations&FORMAT=csv" left the node as
// "gp.php?GROUP=stationsu0026FORMAT=csv". The pull then burned its whole
// timeout dialling a host that does not exist — with the flow reporting a
// clean run, because nothing in the flow had failed.
//
// Every real CelesTrak endpoint carries a multi-parameter query string, so this
// broke retrieval the moment a deployment configured one.
func TestCelesTrakIngestFlowConfiguredURLSurvivesTheHostcall(t *testing.T) {
	dist := celestrakFlowDist(t, "gp")
	fixture := celestrakFixture(t, "celestrak-gp-omm.csv")
	h := newCelestrakIngestHarness(t)

	const query = "/gp.php?GROUP=stations&FORMAT=csv"
	var seen []string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		w.Header().Set("Content-Type", "text/csv")
		w.Write(fixture)
	}))
	defer origin.Close()

	h.runIngestService(t, dist, map[string]interface{}{
		"celestrak_gp_url":          origin.URL + query,
		"celestrak_provider_id":     "space-data-network-02",
		"celestrak_http_timeout_ms": 20000,
	})

	if len(seen) != 1 {
		t.Fatalf("origin saw %d requests, want 1: %v", len(seen), seen)
	}
	if seen[0] != query {
		t.Fatalf("configured URL was corrupted in flight:\n got  %q\n want %q", seen[0], query)
	}
	if got := h.sourceTagCounts(t)["OMM.fbs|space-data-network-02|celestrak-gp"]; got != 2 {
		t.Fatalf("records from the correctly-addressed source = %d, want 2", got)
	}
}

// TestCelesTrakIngestFlowsDeclareOnlyConnectorCapabilities pins the boundary:
// a retrieval app may ask the host for egress and guarded persistence and
// NOTHING else. A bundle that grows a capability beyond this pair is doing
// application work in the host and must be rejected in review.
func TestCelesTrakIngestFlowsDeclareOnlyConnectorCapabilities(t *testing.T) {
	for _, variant := range []string{"gp", "satcat", "spw"} {
		dist := celestrakFlowDist(t, variant)
		raw, err := os.ReadFile(filepath.Join(dist, "plugin-manifest.json"))
		if err != nil {
			t.Fatalf("read %s manifest: %v", variant, err)
		}
		var manifest struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("parse %s manifest: %v", variant, err)
		}
		got := map[string]bool{}
		for _, capability := range manifest.Capabilities {
			got[capability] = true
		}
		if len(got) != 2 || !got["http"] || !got["storage_ingest"] {
			t.Fatalf("%s bundle capabilities = %v, want exactly [http storage_ingest]", variant, manifest.Capabilities)
		}
	}
}
