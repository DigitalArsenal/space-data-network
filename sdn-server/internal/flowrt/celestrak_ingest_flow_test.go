package flowrt

// Loop C.8a e2e: the REAL compiled celestrak-ingest flow bundles
// (space-data-network-modules/flows/celestrak-ingest/dist) served as timer
// flow services against a REAL FlatSQLStore. A fixture HTTP server plays
// CelesTrak (recorded fixtures — the same files internal/ingest's tests
// use); the flow's node CONFIG points the request-builder nodes at it. One
// FireTrigger per source proves, end to end through WASM parsing and the
// policy-mediated storage.ingest_with_source cap op:
//
//   - records land durably with FULL SourceTags attribution
//     (provider/source/url/batch = sha256(payload)/content-key),
//   - the OMM record BYTES are IDENTICAL to what the Go in-daemon ingest
//     pipeline builds for the same fixture rows (byte-parity of the WASM
//     parser with internal/sds builders + internal/ingest rules),
//   - raw payloads and wasm-authored provenance JSON are archived in the
//     runner's raw layout,
//   - datasync cursors advance (MaxRowID), and
//   - replaying the SAME batch does not duplicate records.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// celestrakIngestFlowDist locates the compiled flow bundles. Override with
// SDN_CELESTRAK_INGEST_FLOW_DIST; defaults to the sibling modules checkout.
func celestrakIngestFlowDist(t *testing.T) string {
	t.Helper()
	dist := os.Getenv("SDN_CELESTRAK_INGEST_FLOW_DIST")
	if dist == "" {
		dist = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "celestrak-ingest", "dist")
	}
	if _, err := os.Stat(filepath.Join(dist, "gp", "runtime.wasm")); err != nil {
		t.Skipf("celestrak-ingest flow bundles not found at %s (set SDN_CELESTRAK_INGEST_FLOW_DIST): %v", dist, err)
	}
	return dist
}

func readIngestFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func fixtureSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type celestrakIngestHarness struct {
	store    *storage.FlatSQLStore
	rawRoot  string
	server   *httptest.Server
	fixtures map[string][]byte
	deps     FlowMountDeps
	dist     string
}

func newCelestrakIngestHarness(t *testing.T) *celestrakIngestHarness {
	t.Helper()
	dist := celestrakIngestFlowDist(t)

	h := &celestrakIngestHarness{
		fixtures: map[string][]byte{
			"/gp.csv":     readIngestFixture(t, "celestrak-gp-omm.csv"),
			"/satcat.txt": readIngestFixture(t, "celestrak-satcat.txt"),
			"/satcat.csv": readIngestFixture(t, "celestrak-satcat.csv"),
			"/sw-all.csv": readIngestFixture(t, "celestrak-sw-all.csv"),
		},
		dist: dist,
	}

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := h.fixtures[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// The SPW fixture's newest DATE row is 2026-01-02; Last-Modified in
		// the same window keeps the parser's 7-day staleness gate green.
		w.Header().Set("Last-Modified", "Fri, 02 Jan 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "text/csv")
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(h.server.Close)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	h.store = store
	h.rawRoot = filepath.Join(t.TempDir(), "raw")

	// The exact registry surface the daemon builds: http + storage with the
	// dedicated storage_ingest grant, raw-archive root and guardrail policy.
	reg := modulert.NewCapabilityRegistry()
	reg.Register("http", caps.NewHTTPCapFactory())
	storageFac := caps.NewStorageCapFactoryWithOptions(store, caps.StorageCapOptions{
		RawRoot:          h.rawRoot,
		MinFreeDiskBytes: 1,
	})
	reg.RegisterBridgeAware("storage_query", storageFac)
	reg.RegisterBridgeAware("storage_write", storageFac)
	reg.RegisterBridgeAware("storage_ingest", storageFac)

	h.deps = FlowMountDeps{
		CapRegistry:    reg,
		NodeCtx:        &modulert.NodeContext{CapabilityPolicy: newTestCapabilityPolicy(t)},
		MaxMemoryPages: 4096,
	}
	return h
}

// loadService loads one bundle as a timer flow service with fixture URL
// overrides delivered as node CONFIG (config.FlowService semantics).
func (h *celestrakIngestHarness) loadService(t *testing.T, bundle string) *ServiceFlow {
	t.Helper()
	bundleDist := filepath.Join(h.dist, bundle)
	// Each celestrak-ingest bundle (gp/satcat/spw) is a SEPARATE compiled
	// artifact with its own content hash; record the approval for THIS
	// bundle's hash before loading it (loop B1-followup default-deny gate —
	// see capability_approval_test.go).
	approveFlowCapabilities(t, h.deps.NodeCtx.CapabilityPolicy, bundleDist, "http", "storage_ingest")
	services := []config.FlowService{{
		Flow: bundleDist,
		Config: map[string]interface{}{
			"celestrak_gp_url":            h.server.URL + "/gp.csv",
			"celestrak_satcat_url":        h.server.URL + "/satcat.txt",
			"celestrak_satcat_csv_url":    h.server.URL + "/satcat.csv",
			"celestrak_space_weather_url": h.server.URL + "/sw-all.csv",
		},
	}}
	loaded, err := LoadFlowServices(services, h.deps)
	if err != nil {
		t.Fatalf("LoadFlowServices(%s): %v", bundle, err)
	}
	if len(loaded) != 1 {
		t.Fatalf("LoadFlowServices(%s): loaded %d services, want 1", bundle, len(loaded))
	}
	t.Cleanup(func() { loaded[0].Close() })
	return loaded[0]
}

func (h *celestrakIngestHarness) taggedRecords(t *testing.T, schema, source, batch string) []*storage.Record {
	t.Helper()
	records, err := h.store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: schema,
		ProviderID: "space-data-network-02",
		SourceName: source,
		BatchID:    batch,
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("QuerySourceTaggedRecords(%s/%s): %v", schema, source, err)
	}
	return records
}

func fireTrigger(t *testing.T, sf *ServiceFlow, trigger string) map[string]interface{} {
	t.Helper()
	out, err := sf.InvokeCron(t.Context(), trigger, nil)
	if err != nil {
		t.Fatalf("InvokeCron(%s): %v", trigger, err)
	}
	var summary map[string]interface{}
	if err := json.Unmarshal(out, &summary); err != nil {
		t.Fatalf("summary is not JSON: %v (%s)", err, string(out))
	}
	return summary
}

func TestCelestrakIngestFlowEndToEnd(t *testing.T) {
	h := newCelestrakIngestHarness(t)

	// ---- GP: timer -> fetch -> parse -> OMM + MPE ingests ----
	gp := h.loadService(t, "gp")
	if got := gp.ID(); got != "com.digitalarsenal.flows.celestrak-gp-ingest" {
		t.Fatalf("gp service id = %q", got)
	}
	specs := gp.CronMethods()
	if len(specs) != 1 || specs[0].Method != "timer-gp" || specs[0].DefaultInterval != "10800s" {
		t.Fatalf("gp cron methods = %+v, want timer-gp @ 10800s", specs)
	}

	summary := fireTrigger(t, gp, "timer-gp")
	results, _ := summary["results"].([]interface{})
	if len(results) != 2 {
		t.Fatalf("gp egress results = %d (%v), want 2 (OMM + MPE)", len(results), summary)
	}

	gpBatch := fixtureSHA256(h.fixtures["/gp.csv"])
	ommRecords := h.taggedRecords(t, "OMM.fbs", "celestrak-gp", gpBatch)
	if len(ommRecords) != 2 {
		t.Fatalf("OMM records = %d, want 2", len(ommRecords))
	}
	mpeRecords := h.taggedRecords(t, "MPE.fbs", "celestrak-gp", gpBatch)
	if len(mpeRecords) != 2 {
		t.Fatalf("MPE records = %d, want 2", len(mpeRecords))
	}
	for _, rec := range ommRecords {
		tags, err := h.store.GetSourceTags("OMM.fbs", rec.CID)
		if err != nil {
			t.Fatalf("GetSourceTags(%s): %v", rec.CID, err)
		}
		if tags.ProviderID != "space-data-network-02" || tags.BatchID != gpBatch ||
			!strings.Contains(tags.SourceURL, "/gp.csv") || tags.ContentKeyID != "public" {
			t.Fatalf("record %s has wrong attribution: %+v", rec.CID, tags)
		}
	}
	for _, rec := range mpeRecords {
		tags, err := h.store.GetSourceTags("MPE.fbs", rec.CID)
		if err != nil {
			t.Fatalf("GetSourceTags(%s): %v", rec.CID, err)
		}
		if tags.ProviderID != "space-data-network-02" || tags.BatchID != gpBatch {
			t.Fatalf("record %s has wrong attribution: %+v", rec.CID, tags)
		}
	}

	// BYTE PARITY with the Go ingest pipeline: build the exact records the
	// in-daemon runner (internal/ingest ingestGPData over internal/sds
	// builders) produces for the fixture rows and require identical bytes —
	// same CIDs, so switching pipelines is idempotent.
	expectedOMM := map[string][]byte{}
	for _, row := range []struct {
		norad                   uint32
		name, objectID, epoch   string
		n, e, i, raan, argp, ma float64
		bstar, mmDot, mmDdot    float64
		elsetNo                 uint32
		revAtEpoch              float64
	}{
		{25544, "ISS (ZARYA)", "1998-067A", "2026-01-01T00:00:00Z", 15.48962367, 0.0006703, 51.6432, 92.1234, 45.6789, 314.1592,
			0.00012345, 0.00002182, 0.0000000001, 999, 48123},
		{40909, "STARLINK-1001", "2015-049A", "2026-01-01T00:10:00Z", 15.05512345, 0.0001234, 53.0001, 120.5678, 89.1234, 270.4567,
			0.00001234, 0.00000103, 0, 998, 31456},
	} {
		data := sds.NewOMMBuilder().
			WithNoradCatID(row.norad).
			WithObjectName(row.name).
			WithObjectID(row.objectID).
			WithCreationDate(row.epoch).
			WithEpoch(row.epoch).
			WithMeanMotion(row.n).
			WithEccentricity(row.e).
			WithInclination(row.i).
			WithRaOfAscNode(row.raan).
			WithArgOfPericenter(row.argp).
			WithMeanAnomaly(row.ma).
			WithBStar(row.bstar).
			WithMeanMotionDot(row.mmDot).
			WithMeanMotionDdot(row.mmDdot).
			WithElementSetNo(row.elsetNo).
			WithRevAtEpoch(row.revAtEpoch).
			WithClassificationType("U").
			WithEphemerisType("SGP").
			WithOriginator("CELESTRAK").
			Build()
		record := data[4:]
		sum := sha256.Sum256(record)
		expectedOMM[hex.EncodeToString(sum[:])] = record
	}
	for _, rec := range ommRecords {
		sum := sha256.Sum256(rec.Data)
		if _, ok := expectedOMM[hex.EncodeToString(sum[:])]; !ok {
			t.Fatalf("stored OMM record %s does not byte-match the Go ingest pipeline's output for the fixture", rec.CID)
		}
	}

	// Raw archive + provenance in the runner's raw layout.
	day := time.Now().UTC().Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(h.rawRoot, "celestrak", day, "catalog.csv")); err != nil {
		t.Fatalf("GP raw archive missing: %v", err)
	}
	provDir := filepath.Join(h.rawRoot, "provenance", "celestrak-gp")
	provEntries, err := os.ReadDir(provDir)
	if err != nil || len(provEntries) == 0 {
		t.Fatalf("GP provenance missing (entries=%v err=%v)", provEntries, err)
	}
	provBody, err := os.ReadFile(filepath.Join(provDir, provEntries[0].Name()))
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	var prov map[string]interface{}
	if err := json.Unmarshal(provBody, &prov); err != nil {
		t.Fatalf("provenance is not JSON: %v", err)
	}
	if prov["source_sha256"] != gpBatch || prov["parser_version"] != "celestrak-gp-wasm/v2" {
		t.Fatalf("provenance content wrong: %v", prov)
	}

	// Datasync cursor advanced (rowid-cursor head over the ingested schema).
	head, err := h.store.RawRecordHead(storage.RawRecordQuery{SchemaName: "OMM.fbs", UseRowIDCursor: true})
	if err != nil {
		t.Fatalf("RawRecordHead: %v", err)
	}
	if head.MaxRowID <= 0 {
		t.Fatalf("MaxRowID = %d after ingest, want > 0", head.MaxRowID)
	}

	// REPLAY the same trigger: same payload, same batch id — record counts
	// must not change (reconcile-guarded idempotence).
	fireTrigger(t, gp, "timer-gp")
	if again := h.taggedRecords(t, "OMM.fbs", "celestrak-gp", gpBatch); len(again) != 2 {
		t.Fatalf("after replay: OMM records = %d, want 2 (no duplicates)", len(again))
	}
	if count, err := h.store.Count("OMM.fbs"); err != nil || count != 2 {
		t.Fatalf("after replay: OMM count = %d err=%v, want 2", count, err)
	}

	// ---- SATCAT: one timer fans out BOTH sources; snapshot reconcile ----
	satcat := h.loadService(t, "satcat")
	summary = fireTrigger(t, satcat, "timer-satcat")
	if results, _ := summary["results"].([]interface{}); len(results) != 2 {
		t.Fatalf("satcat egress results = %v, want 2 (txt + csv)", summary)
	}
	txtBatch := fixtureSHA256(h.fixtures["/satcat.txt"])
	csvBatch := fixtureSHA256(h.fixtures["/satcat.csv"])
	if got := h.taggedRecords(t, "CAT.fbs", "celestrak-satcat", txtBatch); len(got) != 2 {
		t.Fatalf("CAT (txt source) records = %d, want 2", len(got))
	}
	if got := h.taggedRecords(t, "CAT.fbs", "celestrak-satcat-csv", csvBatch); len(got) != 2 {
		t.Fatalf("CAT (csv source) records = %d, want 2", len(got))
	}

	// ---- SPW: fresh source (Last-Modified pinned by the fixture server) ----
	spw := h.loadService(t, "spw")
	summary = fireTrigger(t, spw, "timer-spw")
	if results, _ := summary["results"].([]interface{}); len(results) != 1 {
		t.Fatalf("spw egress results = %v, want 1", summary)
	}
	spwBatch := fixtureSHA256(h.fixtures["/sw-all.csv"])
	if got := h.taggedRecords(t, "SPW.fbs", "celestrak-space-weather", spwBatch); len(got) != 2 {
		t.Fatalf("SPW records = %d, want 2", len(got))
	}
}

// TestCelestrakIngestFlowFetchFailure proves a failing source stops the
// batch cleanly: the parse node rejects the non-200 response, the drain
// reports the error, and nothing lands in the store.
func TestCelestrakIngestFlowFetchFailure(t *testing.T) {
	h := newCelestrakIngestHarness(t)
	delete(h.fixtures, "/gp.csv") // fixture server now 404s the GP fetch

	gp := h.loadService(t, "gp")
	out, err := gp.InvokeCron(t.Context(), "timer-gp", nil)
	if err == nil {
		// The parse node rejects the 404 with a node error; depending on the
		// scheduler's error propagation the drain may complete with ZERO
		// egress results instead of failing — either way nothing must land.
		var summary map[string]interface{}
		if jerr := json.Unmarshal(out, &summary); jerr != nil {
			t.Fatalf("summary is not JSON: %v", jerr)
		}
		if results, _ := summary["results"].([]interface{}); len(results) != 0 {
			t.Fatalf("failed fetch produced egress results: %v", summary)
		}
	}
	if count, cerr := h.store.Count("OMM.fbs"); cerr != nil || count != 0 {
		t.Fatalf("failed fetch wrote records: count=%d err=%v", count, cerr)
	}
}

// TestCelestrakIngestPolicyRejectsMissingGrant proves capability policy: a
// registry WITHOUT the storage_ingest factory refuses to load the flow at
// all (provisioning rejects undeliverable capability sets).
func TestCelestrakIngestPolicyRejectsMissingGrant(t *testing.T) {
	h := newCelestrakIngestHarness(t)

	reg := modulert.NewCapabilityRegistry()
	reg.Register("http", caps.NewHTTPCapFactory())
	deps := h.deps
	deps.CapRegistry = reg

	_, err := LoadFlowService(filepath.Join(h.dist, "gp"), nil, deps)
	if err == nil {
		t.Fatal("flow service loaded without a storage_ingest factory; provisioning must reject")
	}
	if !strings.Contains(err.Error(), "storage_ingest") {
		t.Fatalf("rejection does not name the missing capability: %v", err)
	}

	fmt.Println("policy rejection:", err)
}
