package sdnflows_test

// End-to-end acceptance for the flow-run foundation (loop C.8a ported to kubo):
// install the REAL compiled celestrak-spw-ingest flow bundle, register its
// host-cron timer with the sdncron.Scheduler, let the scheduler FIRE it, and
// assert the flow executed its WASM nodes (celestrak-request -> http-request ->
// celestrak-parser -> storage-ingest) and STORED SPW records into sdnstore,
// readable by (source, 3-letter type).
//
// WHAT IS REAL: the compiled flow bundle, the flowrt runtime stepping its FSM,
// the sdn/modulert hostcall bridge, the http cap fetch, the celestrak-parser
// WASM parse + staleness gate, the storage.ingest_with_source cap landing
// records in a real sdnstore.Store over a real FlatSQL engine, the sdncron
// Scheduler firing the timer, and the sdnservices.BuildServices wiring (the
// SAME wiring the sdnruntime plugin runs).
//
// WHAT IS STUBBED: only the CelesTrak HOST. celestrak.org is firewalled from
// this workstation, so the flow's node CONFIG points celestrak_space_weather_url
// at a local httptest server serving a canned SW-All.csv with a Last-Modified
// header pinned to the payload's newest DATE row (keeps the parser's 7-day
// staleness gate green, deterministically and time-independently). The
// >=2.5s-spacing + 3h-ledger CelesTrak fetch policy is exercised separately and
// deterministically in http_cap_test.go (the local stub host is NOT gated).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnflows"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// canned SW-All.csv: 2 rows, newest DATE 2026-01-02. Served with Last-Modified
// in the same window so the parser's staleness gate (latest DATE vs
// Last-Modified) passes independent of wall-clock now.
const cannedSWAll = `DATE,BSRN,ND,KP1,KP2,KP3,KP4,KP5,KP6,KP7,KP8,KP_SUM,AP1,AP2,AP3,AP4,AP5,AP6,AP7,AP8,AP_AVG,CP,C9,ISN,F10.7_OBS,F10.7_ADJ,F10.7_DATA_TYPE,F10.7_OBS_CENTER81,F10.7_OBS_LAST81,F10.7_ADJ_CENTER81,F10.7_ADJ_LAST81
2026-01-01,2600,1,10,20,30,40,50,60,70,80,360,1,2,3,4,5,6,7,8,5,0.7,3,42,150.5,152.25,OBS,148.5,147.25,149.5,150.25
2026-01-02,2600,2,17,20,23,30,33,40,43,50,255,3,4,5,6,7,8,9,10,6,0.8,4,43,151.5,153.25,INT,149.5,148.25,150.5,151.25
`

const spwLastModified = "Fri, 02 Jan 2026 12:00:00 GMT"

// spwSchemas resolves the SPW 3-letter type for the store (enums + SPW table).
func spwSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "SPW" {
			return spwSchema, "$SPW", "SPW", true
		}
		return "", "", "", false
	})
}

const spwSchema = `
  enum FluxQualifier: byte { OBSERVED = 0, BURST_ADJUSTED = 1, INTERPOLATED_EXTRAPOLATED = 2, NO_OBSERVATION = 3, CELESTRAK_INTERPOLATED = 4 }
  enum F107DataType: byte { OBS = 0, INT = 1, PRD = 2, PRM = 3 }
  table SPW {
    DATE: string;
    BSRN: int;
    ND: int;
    KP1: int; KP2: int; KP3: int; KP4: int; KP5: int; KP6: int; KP7: int; KP8: int;
    KP_SUM: int;
    AP1: int; AP2: int; AP3: int; AP4: int; AP5: int; AP6: int; AP7: int; AP8: int;
    AP_AVG: int;
    CP: float;
    C9: int;
    ISN: int;
    F107_OBS: float;
    F107_ADJ: float;
    F107_DATA_TYPE: F107DataType;
    F107_OBS_CENTER81: float;
    F107_OBS_LAST81: float;
    F107_ADJ_CENTER81: float;
    F107_ADJ_LAST81: float;
  }
  root_type SPW;
  file_identifier "$SPW";
`

// findSPWBundle resolves the compiled celestrak-spw flow bundle directory from a
// normal checkout or a git worktree.
func findSPWBundle(t *testing.T) string {
	t.Helper()
	suffix := filepath.Join("space-data-network-modules", "flows", "celestrak-ingest", "dist", "spw")
	if env := os.Getenv("SDN_CELESTRAK_SPW_FLOW_DIST"); env != "" {
		if _, err := os.Stat(filepath.Join(env, "runtime.wasm")); err == nil {
			return env
		}
	}
	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	anchor := filepath.Dir(callerFile) // .../kubo/sdn/sdnflows
	for _, ups := range [][]string{
		{"..", "..", "..", ".."},
		{"..", "..", "..", "..", ".."},
		{"..", "..", "..", "..", "..", ".."},
	} {
		cand := filepath.Clean(filepath.Join(append([]string{anchor}, append(ups, suffix)...)...))
		if _, err := os.Stat(filepath.Join(cand, "runtime.wasm")); err == nil {
			return cand
		}
	}
	t.Skip("celestrak-spw flow bundle not found (set SDN_CELESTRAK_SPW_FLOW_DIST)")
	return ""
}

func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return filepath.Join(base, "sdn-flatsqlrt-test-aot")
}

// flowContentHash computes the capability-policy identity of a flow bundle the
// SAME way LoadFlowService does: portable (trailer-stripped) bytes, hashed.
func flowContentHash(t *testing.T, bundleDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(bundleDir, "runtime.wasm"))
	if err != nil {
		t.Fatalf("read runtime.wasm: %v", err)
	}
	portable, _, err := modulert.EnforceModuleSignaturePolicy(nil, raw)
	if err != nil {
		t.Fatalf("strip trailer: %v", err)
	}
	return modulert.ContentHashHex(portable)
}

type spwHarness struct {
	svc       *sdnservices.Services
	installer *sdnflows.Installer
	store     *sdnstore.Store
	stub      *httptest.Server
	bundle    string
	policy    *modulert.CapabilityPolicyStore
}

func newSPWHarness(t *testing.T) *spwHarness {
	t.Helper()
	bundle := findSPWBundle(t)

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", spwLastModified)
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte(cannedSWAll))
	}))
	t.Cleanup(stub.Close)

	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	policy, err := modulert.NewCapabilityPolicyStore("") // in-memory
	if err != nil {
		t.Fatalf("policy store: %v", err)
	}

	svc, err := sdnservices.BuildServices(sdnservices.Deps{
		Blockstore:     bs,
		Datastore:      mds,
		Schemas:        spwSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
		Policy:         policy,
		FetchLedgerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(svc.Close)

	reg, err := sdnflows.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	installer, err := sdnflows.New(sdnflows.Config{Services: svc, Registry: reg, MaxMemoryPages: 4096, Log: t.Logf})
	if err != nil {
		t.Fatalf("sdnflows.New: %v", err)
	}
	t.Cleanup(installer.Close)

	return &spwHarness{svc: svc, installer: installer, store: svc.Store, stub: stub, bundle: bundle, policy: policy}
}

// approveFlow records DEV operator approvals for the SPW flow bundle's content
// hash so the fail-closed capability-policy gate admits http + storage_ingest.
func (h *spwHarness) approveFlow(t *testing.T) {
	t.Helper()
	hash := flowContentHash(t, h.bundle)
	for _, cap := range []string{"http", "storage_ingest"} {
		if _, err := h.policy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash,
			Capability: cap,
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("approve %s: %v", cap, err)
		}
	}
}

func (h *spwHarness) spec() sdnflows.FlowSpec {
	return sdnflows.FlowSpec{
		Ref:       h.bundle,
		Intervals: map[string]string{"timer-spw": "60ms"}, // fast fire for the test
		Config:    map[string]interface{}{"celestrak_space_weather_url": h.stub.URL + "/sw-all.csv"},
	}
}

// TestSPWFlowInstallsAndCronFiresAndStores is the acceptance gate: the real SPW
// flow installs, its host-cron timer registers + FIRES via the scheduler, and
// SPW records land in sdnstore under (celestrak-space-weather, SPW).
func TestSPWFlowInstallsAndCronFiresAndStores(t *testing.T) {
	h := newSPWHarness(t)
	h.approveFlow(t)

	installed, err := h.installer.Install(h.spec(), "test")
	if err != nil {
		t.Fatalf("Install SPW flow: %v", err)
	}
	if installed.ID != "com.digitalarsenal.flows.celestrak-spw-ingest" {
		t.Fatalf("installed flow id = %q", installed.ID)
	}
	if len(installed.Timers) != 1 || installed.Timers[0] != "timer-spw" {
		t.Fatalf("installed timers = %v, want [timer-spw]", installed.Timers)
	}

	// The flow is listed alongside modules (backs GET /sdn/v1/modules).
	views := h.svc.Scheduler.List()
	found := false
	for _, v := range views {
		if v.ID == installed.ID {
			found = true
			if len(v.Timers) != 1 || v.Timers[0].IntervalMs != 60 {
				t.Fatalf("scheduler view timers = %+v, want timer-spw @ 60ms", v.Timers)
			}
		}
	}
	if !found {
		t.Fatalf("installed flow %q not visible in scheduler.List() (GET /sdn/v1/modules)", installed.ID)
	}

	// Drive the REAL cron: start the scheduler and wait for the timer to fire
	// and land records.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.svc.Scheduler.Start(ctx)

	deadline := time.Now().Add(30 * time.Second)
	var recs [][]byte
	for time.Now().Before(deadline) {
		recs, err = h.store.ReadBySourceType(context.Background(), "celestrak-space-weather", "SPW")
		if err != nil {
			t.Fatalf("ReadBySourceType: %v", err)
		}
		if len(recs) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.svc.Scheduler.Stop()

	if len(recs) != 2 {
		t.Fatalf("SPW records after cron fire = %d, want 2 (canned 2-row payload)", len(recs))
	}
	for _, r := range recs {
		if len(r) == 0 {
			t.Fatalf("stored SPW record is empty")
		}
	}

	// The (source, type) pair is in the catalog.
	sources, err := h.store.Sources(context.Background(), "SPW")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 1 || sources[0] != "celestrak-space-weather" {
		t.Fatalf("SPW sources = %v, want [celestrak-space-weather]", sources)
	}
	t.Logf("SPW flow cron-fired and stored %d records under (celestrak-space-weather, SPW)", len(recs))
}

// TestSPWFlowInstallDeniedWithoutApproval proves the fail-closed capability gate
// at the FLOW-INSTALL layer: without an operator approval for the bundle's
// content hash, loading the flow (which declares sensitive http + storage_ingest)
// is refused — nothing is registered.
func TestSPWFlowInstallDeniedWithoutApproval(t *testing.T) {
	h := newSPWHarness(t) // NO approveFlow

	_, err := h.installer.Install(h.spec(), "test")
	if err == nil {
		t.Fatal("SPW flow installed without operator approval; the fail-closed gate must refuse")
	}
	if len(h.installer.List()) != 0 {
		t.Fatalf("a denied flow was registered: %v", h.installer.List())
	}
	if views := h.svc.Scheduler.List(); len(views) != 0 {
		t.Fatalf("a denied flow reached the scheduler: %v", views)
	}
	t.Logf("fail-closed: %v", err)
}

// TestSPWFlowBootReRegisters proves the persisted registry re-establishes the
// flow on a fresh boot: install, then a second installer over the SAME registry
// re-registers the flow from persistence alone.
func TestSPWFlowBootReRegisters(t *testing.T) {
	h := newSPWHarness(t)
	h.approveFlow(t)
	if _, err := h.installer.Install(h.spec(), "test"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// A fresh services build + installer over the SAME repo dirs re-registers
	// the persisted flow at Boot. Reuse the registry dir from the first
	// installer via its persisted file.
	regDir := t.TempDir()
	reg, _ := sdnflows.NewRegistry(regDir)
	_ = reg.Put(sdnflows.InstalledEntry{
		ID:      "com.digitalarsenal.flows.celestrak-spw-ingest",
		Ref:     h.bundle,
		Config:  map[string]interface{}{"celestrak_space_weather_url": h.stub.URL + "/sw-all.csv"},
		Enabled: true,
		Source:  "test",
	})

	// Fresh services so the new installer registers into a fresh scheduler.
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)
	svc2, err := sdnservices.BuildServices(sdnservices.Deps{
		Blockstore: bs, Datastore: mds, Schemas: spwSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
		Policy:         h.policy, FetchLedgerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices2: %v", err)
	}
	defer svc2.Close()
	installer2, err := sdnflows.New(sdnflows.Config{Services: svc2, Registry: reg, MaxMemoryPages: 4096, Log: t.Logf})
	if err != nil {
		t.Fatalf("sdnflows.New2: %v", err)
	}
	defer installer2.Close()

	n, err := installer2.Boot(context.Background(), nil)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if n != 1 {
		t.Fatalf("Boot re-registered %d flows, want 1", n)
	}
	if svc2.Scheduler.List()[0].ID != "com.digitalarsenal.flows.celestrak-spw-ingest" {
		t.Fatalf("boot-registered flow not in scheduler: %v", svc2.Scheduler.List())
	}
}
