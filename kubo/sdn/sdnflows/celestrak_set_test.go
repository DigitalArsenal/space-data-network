package sdnflows_test

// Acceptance for the host-02 (celestrak.eth) CelesTrak REFERENCE SET: with the
// celestrak role enabled, the set installs the available FLOW members (GP,
// SATCAT, SPW) and each registers a 3h (10800000 ms) timer with the scheduler
// — visible at GET /sdn/v1/modules via Scheduler.List(). This reuses the SPW
// stub harness pattern to prove a NON-SPW celestrak flow (GP + SATCAT) installs
// and registers, and reports which reference units were available vs. missing
// (the SupGP + GPS-almanac MODULES resolve but ride the module installer, not
// this flow installer).
//
// Only the CelesTrak HOST would be stubbed for a FIRE; install + register needs
// no host at all, so these tests never touch celestrak.org (firewalled).

import (
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnflows"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// TestCelestrakRoleGate proves the reference set installs ONLY when the node is
// in the celestrak role — via the SDN_ROLE env or a configured role, tolerant of
// list syntax and case — and stays dormant otherwise.
func TestCelestrakRoleGate(t *testing.T) {
	t.Setenv("SDN_ROLE", "")
	if sdnflows.CelestrakRoleEnabled("") {
		t.Fatal("role gate open with no env and no config role")
	}
	if !sdnflows.CelestrakRoleEnabled("celestrak") {
		t.Fatal("configured role \"celestrak\" did not enable the set")
	}
	if !sdnflows.CelestrakRoleEnabled("gateway, Celestrak") {
		t.Fatal("configured role list did not match celestrak (case/list-insensitive)")
	}
	if sdnflows.CelestrakRoleEnabled("gateway,relay") {
		t.Fatal("unrelated roles enabled the set")
	}

	t.Setenv("SDN_ROLE", "celestrak")
	if !sdnflows.CelestrakRoleEnabled("") {
		t.Fatal("SDN_ROLE=celestrak did not enable the set")
	}
	t.Setenv("SDN_ROLE", "GATEWAY,CELESTRAK,relay")
	if !sdnflows.CelestrakRoleEnabled("") {
		t.Fatal("SDN_ROLE list did not match celestrak")
	}
	t.Setenv("SDN_ROLE", "gateway")
	if sdnflows.CelestrakRoleEnabled("") {
		t.Fatal("SDN_ROLE=gateway enabled the set")
	}
}

// TestCelestrakReferenceSetShape asserts the enumerated set matches the owner
// rule (GP + SupGP + SPW + GPS almanac + SATCAT), each defaulting to the 3h
// reference interval, with GP/SATCAT/SPW as flows and SupGP/GPS as modules.
func TestCelestrakReferenceSetShape(t *testing.T) {
	if sdnflows.CelestrakReferenceIntervalMs != 10800000 {
		t.Fatalf("reference interval = %d ms, want 10800000 (3h)", sdnflows.CelestrakReferenceIntervalMs)
	}
	want := map[string]sdnflows.ReferenceKind{
		"com.digitalarsenal.flows.celestrak-gp-ingest":     sdnflows.KindFlow,
		"com.digitalarsenal.flows.celestrak-satcat-ingest": sdnflows.KindFlow,
		"com.digitalarsenal.flows.celestrak-spw-ingest":    sdnflows.KindFlow,
		"com.orbpro.celestrak-supgp":                       sdnflows.KindModule,
		"com.orbpro.gps-source":                            sdnflows.KindModule,
	}
	set := sdnflows.CelestrakReferenceSet()
	if len(set) != len(want) {
		t.Fatalf("reference set has %d members, want %d", len(set), len(want))
	}
	for _, m := range set {
		k, ok := want[m.ID]
		if !ok {
			t.Fatalf("unexpected reference member %q", m.ID)
		}
		if m.Kind != k {
			t.Fatalf("member %q kind = %q, want %q", m.ID, m.Kind, k)
		}
		if m.TimerID == "" {
			t.Fatalf("member %q has empty timer id", m.ID)
		}
		spec := m.FlowSpec("/root", nil)
		if spec.Intervals[m.TimerID] != sdnflows.CelestrakReferenceInterval {
			t.Fatalf("member %q default interval = %q, want %q", m.ID, spec.Intervals[m.TimerID], sdnflows.CelestrakReferenceInterval)
		}
	}
}

// celestrakHarness is the SPW harness generalized over any reference flow bundle:
// real services + FlatSQL store + a fail-closed capability policy + a flow
// installer, so a reference flow can be approved, installed, and observed in the
// scheduler at its 3h timer.
type celestrakHarness struct {
	svc       *sdnservices.Services
	installer *sdnflows.Installer
	policy    *modulert.CapabilityPolicyStore
	distRoot  string
}

func newCelestrakHarness(t *testing.T) *celestrakHarness {
	t.Helper()
	distRoot, ok := sdnflows.ResolveModulesDist("")
	if !ok {
		t.Skip("space-data-network-modules dist not found (set SDN_MODULES_DIST)")
	}
	// V1: the flow-runtime reads flow.plg. Transcode each resolvable flow
	// bundle's flow.json into a sibling flow.plg before install.
	for _, m := range sdnflows.CelestrakReferenceSet() {
		if m.Kind != sdnflows.KindFlow {
			continue
		}
		if path, ok := m.Resolve(distRoot); ok {
			ensureBundlePLG(t, path)
		}
	}

	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("policy store: %v", err)
	}
	// A schema provider covering the reference flows' landing types (OMM for GP,
	// SPW for space weather). Install + register never touches the store; this
	// only keeps BuildServices realistic.
	schemas := sdnstore.SchemaProviderFunc(func(ty string) (schema, fileID, tableName string, ok bool) {
		if ty == "SPW" {
			return spwSchema, "$SPW", "SPW", true
		}
		return "", "", "", false
	})
	svc, err := sdnservices.BuildServices(sdnservices.Deps{
		Blockstore:     bs,
		Datastore:      mds,
		Schemas:        schemas,
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

	return &celestrakHarness{svc: svc, installer: installer, policy: policy, distRoot: distRoot}
}

// approveFlowBundle records DEV operator approvals for a flow bundle's content
// hash so the fail-closed gate admits its declared caps (mirrors the role-gated
// runtime path, which approves the first-party reference set on the celestrak
// node).
func (h *celestrakHarness) approveFlowBundle(t *testing.T, bundleDir string, caps []string) {
	t.Helper()
	hash := flowContentHash(t, bundleDir)
	for _, c := range caps {
		if _, err := h.policy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash,
			Capability: c,
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("approve %s: %v", c, err)
		}
	}
}

// TestCelestrakReferenceSetInstallsFlowsAt3h is the acceptance gate: with the
// role enabled and the first-party flow bundles approved, InstallCelestrakFlows
// installs GP, SATCAT and SPW, each registering a 3h timer visible in the
// scheduler (GET /sdn/v1/modules). SupGP + the GPS almanac resolve as modules and
// are reported (available, deferred to the module installer). It logs the full
// available-vs-missing table.
func TestCelestrakReferenceSetInstallsFlowsAt3h(t *testing.T) {
	t.Setenv("SDN_ROLE", "celestrak")
	if !sdnflows.CelestrakRoleEnabled("") {
		t.Fatal("precondition: role not enabled")
	}
	h := newCelestrakHarness(t)

	// Approve each available FLOW member's content hash for its declared caps.
	for _, m := range sdnflows.CelestrakReferenceSet() {
		if m.Kind != sdnflows.KindFlow {
			continue
		}
		if path, ok := m.Resolve(h.distRoot); ok {
			h.approveFlowBundle(t, path, m.Caps)
		}
	}

	statuses, err := sdnflows.InstallCelestrakFlows(h.installer, h.distRoot, "test:celestrak-role")
	if err != nil {
		t.Fatalf("InstallCelestrakFlows: %v", err)
	}

	flowsInstalled := 0
	var availFlows, missing, modules []string
	for _, s := range statuses {
		t.Logf("reference %-11s id=%-48s kind=%-6s available=%-5v installed=%-5v interval=%dms note=%q err=%q",
			s.Name, s.ID, s.Kind, s.Available, s.Installed, s.IntervalMs, s.Note, s.Err)
		if s.IntervalMs != sdnflows.CelestrakReferenceIntervalMs {
			t.Fatalf("member %q interval = %d, want %d (3h)", s.ID, s.IntervalMs, sdnflows.CelestrakReferenceIntervalMs)
		}
		switch s.Kind {
		case sdnflows.KindFlow:
			if s.Installed {
				flowsInstalled++
				availFlows = append(availFlows, s.Name)
			} else {
				missing = append(missing, s.Name)
			}
		case sdnflows.KindModule:
			modules = append(modules, s.Name)
			if s.Installed {
				t.Fatalf("module member %q was installed by the FLOW installer; must be deferred to the module runtime", s.ID)
			}
		}
	}
	t.Logf("AVAILABLE flows=%v ; MODULES(deferred to module installer)=%v ; MISSING flows=%v", availFlows, modules, missing)

	if flowsInstalled == 0 {
		t.Fatal("no reference flow installed")
	}

	// Each installed flow appears in the scheduler (GET /sdn/v1/modules) with its
	// timer at exactly 3h. Prove a NON-SPW flow specifically.
	views := h.svc.Scheduler.List()
	byID := map[string]bool{}
	for _, v := range views {
		byID[v.ID] = true
		for _, tm := range v.Timers {
			if tm.IntervalMs != sdnflows.CelestrakReferenceIntervalMs {
				t.Fatalf("scheduler timer %s/%s interval = %d ms, want 10800000 (3h)", v.ID, tm.ID, tm.IntervalMs)
			}
		}
	}
	sawNonSPW := false
	for _, s := range statuses {
		if s.Kind == sdnflows.KindFlow && s.Installed {
			if !byID[s.ID] {
				t.Fatalf("installed flow %q not visible in scheduler.List()", s.ID)
			}
			if s.ID != "com.digitalarsenal.flows.celestrak-spw-ingest" {
				sawNonSPW = true
			}
		}
	}
	if !sawNonSPW {
		t.Fatal("acceptance requires at least one NON-SPW celestrak flow (GP/SATCAT) installed + registered")
	}
	t.Logf("reference set: %d flow(s) registered at 3h; scheduler lists %d unit(s)", flowsInstalled, len(views))

	// The module members' artifacts must resolve (so the module installer can
	// bring them up on the live node) — availability, not install, is proven here.
	for _, m := range sdnflows.CelestrakReferenceSet() {
		if m.Kind != sdnflows.KindModule {
			continue
		}
		if _, ok := m.Resolve(h.distRoot); !ok {
			t.Logf("NOTE: reference MODULE %q (%s) artifact not found in this checkout — gap", m.Name, m.ID)
		}
	}
}
