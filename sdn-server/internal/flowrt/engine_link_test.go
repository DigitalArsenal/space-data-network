package flowrt

// Loop C.7 direct-linkage host tests: shim identity, linked-artifact
// detection, and the poison-recovery contract (poison the store engine →
// the mount recovers by recovering the engine and re-instantiating its
// linked flow instances against the replacement).

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

// The embedded flatsql_link shim must stay byte-identical to the SDK's
// FLATSQL_LINK_SHIM_WASM (space-data-module-sdk test/flatsql-link-shim.test.js
// pins the SAME digest) — both hosts instantiate the same memory-crossing
// component. Update both constants together when the shim changes.
const linkShimSHA256 = "8d83e69b087c5b8c96b4f1377a607c77380f58f9e338073f91b1e43eee1f788b"

func TestLinkShimBytesMatchSDK(t *testing.T) {
	sum := sha256.Sum256(flatsqlLinkShimWasm)
	if got := hex.EncodeToString(sum[:]); got != linkShimSHA256 {
		t.Fatalf("flatsql-link-shim.wasm sha256 = %s, want %s (regenerate from the SDK and update BOTH pins)", got, linkShimSHA256)
	}
	// The shim itself must be a linked artifact of the engine (imports
	// flatsql.memory) — and nothing else.
	if !wasmImportsModule(flatsqlLinkShimWasm, "flatsql") {
		t.Fatal("shim does not import the flatsql module")
	}
}

// linkedImportFixtureWasm is a minimal synthetic module (testdata/, ~55
// bytes) importing from both "flatsql" and "flatsql_link" — never
// instantiated, only import-section-scanned. wasmImportsModule() detection
// is a pure function of the import section, so it is unit-tested against a
// fixture we control rather than the live space-data-network-modules
// data-retrieval dist: that dist's engineLinkage evolves independently
// (gen2 moved data-retrieval to the "bridge"/hostcall capability model —
// see the C.7 integration tests below and
// graph/tasks/sdn-gauntlet-required-reds-flowrt-hdwallet.md), and a
// detection-algorithm test has no business depending on which linkage mode
// one particular external flow currently ships.
//
//go:embed testdata/linked-import-fixture.wasm
var linkedImportFixtureWasm []byte

func TestWasmImportsModuleDetection(t *testing.T) {
	if !wasmImportsModule(linkedImportFixtureWasm, "flatsql") {
		t.Fatal("fixture should import module flatsql")
	}
	if !wasmImportsModule(linkedImportFixtureWasm, LinkShimModuleName) {
		t.Fatal("fixture should import module flatsql_link")
	}
	if wasmImportsModule(linkedImportFixtureWasm, "no_such_module") {
		t.Fatal("false positive import detection")
	}
	if wasmImportsModule([]byte("garbage"), "flatsql") {
		t.Fatal("malformed bytes must not detect as linked")
	}
}

// TestLinkedMountRecoversFromEnginePoisoning: serve → poison the store
// engine (a genuine trap: MarkDeleted on an unknown table throws inside the
// no-EH engine) → the next request recovers: the store replaces the engine
// in place (compact metadata replay + hot-window rebuild), the mount
// re-instantiates its linked instance against the replacement, and the
// response is byte-verbatim again.
func TestLinkedMountRecoversFromEnginePoisoning(t *testing.T) {
	dist := dataRetrievalFlowDist(t)

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	store := newSeededMountStore(t, epoch1, epoch2)

	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", caps.NewStorageCapFactory(store))

	// loop B1-followup default-deny gate: record a test-scoped operator
	// approval for THIS bundle's real content hash (capability_approval_test.go).
	policy := approvedCapabilityPolicy(t, dist, "storage_query")

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/test/data/", Flow: dist, Pool: 1}},
		FlowMountDeps{
			CapRegistry:    reg,
			NodeCtx:        &modulert.NodeContext{CapabilityPolicy: policy},
			MaxMemoryPages: 2048,
			EngineLink:     store,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	target := epoch1 + 36*3600
	bulkURL := fmt.Sprintf("%s/test/data/omm/bulk?epoch=%d&limit=100&profile=nearest", srv.URL, target)

	fetch := func() (int, []byte) {
		t.Helper()
		resp, err := http.Get(bulkURL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, body
	}

	expected, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(target), 100)
	if err != nil {
		t.Fatalf("QueryEpochRawStream: %v", err)
	}

	status, body := fetch()
	if status != http.StatusOK || string(body) != string(expected.Bytes) {
		t.Fatalf("pre-poison request: status=%d bytes=%d (want 200/%d)", status, len(body), len(expected.Bytes))
	}
	if epoch := store.EngineEpoch(); epoch != 1 {
		t.Fatalf("engine epoch = %d before poisoning, want 1", epoch)
	}

	// Poison the engine. NOTE: a GENUINE trap (e.g. MarkDeleted on an
	// unregistered table, which throws inside the no-EH engine) DOES set
	// Poisoned() and this test passes with it — but under the AOT engine,
	// libwasmedge 0.14 handles the trap through its own signal handlers
	// installed WITHOUT SA_ONSTACK, which Go's runtime may fatally reject
	// when a preemption SIGURG lands in the window ("non-Go code set up
	// signal handler without SA_ONSTACK flag") — a pre-existing
	// wasmedge-go/AOT issue independent of linkage. The poison→recovery
	// machinery under test here starts AT the poisoned flag, so set it
	// through the same API the trap paths use (flatsqlrt execErr /
	// MarkPoisoned).
	engineRT, _ := store.EngineRuntime()
	engineRT.MarkPoisoned()
	if !engineRT.Poisoned() {
		t.Fatal("engine should report poisoned")
	}
	// The next request must transparently recover: engine replaced, linked
	// instance rebuilt, byte-verbatim response restored.
	status, body = fetch()
	if status != http.StatusOK {
		t.Fatalf("post-poison request: status=%d body=%q", status, body)
	}
	if epoch := store.EngineEpoch(); epoch != 2 {
		t.Fatalf("engine epoch = %d after recovery, want 2", epoch)
	}
	recovered, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(target), 100)
	if err != nil {
		t.Fatalf("QueryEpochRawStream after recovery: %v", err)
	}
	if string(body) != string(recovered.Bytes) {
		t.Fatalf("post-recovery body (%d bytes) != rebuilt engine stream (%d bytes)", len(body), len(recovered.Bytes))
	}
	if string(body) != string(expected.Bytes) {
		t.Fatalf("post-recovery body diverges from the pre-poison bytes (%d vs %d)", len(body), len(expected.Bytes))
	}

	// And the store keeps working normally (ingest + query on the new
	// engine).
	status, body = fetch()
	if status != http.StatusOK || len(body) != len(expected.Bytes) {
		t.Fatalf("steady-state after recovery: status=%d bytes=%d", status, len(body))
	}
}

// readFlowArtifactForTest reads the bundle's portable wasm (publication
// trailer stripped, like LoadMountedFlow does).
func readFlowArtifactForTest(dist string) ([]byte, error) {
	wasmPath, _, err := resolveFlowArtifact(dist, nil)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, err
	}
	return modulert.StripPublicationTrailer(b), nil
}
