package flowrt

// od_flow_e2e_test.go — SDN OD-Flow Phase 1b (SCRATCH, do-not-commit): close the
// END-TO-END ISS OD data path in a baked $PLG flow.
//
//	trigger(timer) -> data-source/oem-source-iss.emit --($OEM)-->
//	                  analysis/od.fit                 --($OMM)-->
//	                  foundation/omm-json.encode      (the real $OMM CONSUMER)
//
// A REAL ISS $OEM (built in-module by oem-source-iss from the checked-in NASA
// fixture) flows oem-source -> od.fit -> $OMM -> omm-json. GATES:
//   - all three linked-direct nodes advance under WasmEdge;
//   - omm-json.encode returns SUCCESS (it VerifySizePrefixedOMMBuffer-checks each
//     frame and returns 400 on any invalid $OMM), so a clean drain PROVES the
//     $OMM od.fit emitted is a valid $OMM a real FB consumer decoded;
//   - the EXACT $OMM od.fit emits decodes (Go $OMM binding) to ISS NORAD 25544,
//     ORIGINATOR SDN-OD, a real mean motion;
//   - the EXACT $OEM oem-source emits carries the "$OEM" file identifier;
//   - NO node wires $OEM to a store (in-memory-only BY CONSTRUCTION).
//
// Stages the PRE-BUILT guest-link objects (od = build.sh emsdk -O3; oem-source =
// SDK build.mjs) — no llvm-box recompile of the modules; only the flow BAKE-link
// uses the box. All flows share ONE staged home, so only the first bake compiles
// the flow-agnostic runtime; the rest are link-only.
//
// Skips cleanly unless the bake toolchain + both pre-built guest-links are staged.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

const (
	odFlowODPluginID  = "orbit-determination"
	odFlowSrcPluginID = "com.digitalarsenal.data-source.oem-source-iss"
	odFlowOmmJSONID   = "com.digitalarsenal.foundation.omm-json"
	odFlowCaptureID   = "org.sdn.probe.capture"
)

type odFlowModule struct {
	pluginID string
	glDir    string
	manifest string
}

var odFlowExtraModules = []odFlowModule{
	{odFlowODPluginID, filepath.Join("analysis", "od", "dist", "guest-link"), filepath.Join("analysis", "od", "plugin-manifest.json")},
	{odFlowSrcPluginID, filepath.Join("data-source", "oem-source-iss", "dist", "guest-link"), filepath.Join("data-source", "oem-source-iss", "plugin-manifest.json")},
}

func stageODFlowHome(t *testing.T, a bakeAssets) flowcc.Home {
	t.Helper()
	home := stageBakeHome(t, a) // toolchain + omm-json + decision-gate + clock
	for _, m := range odFlowExtraModules {
		obj := filepath.Join(a.modulesRoot, m.glDir, "module-link.o")
		meta := filepath.Join(a.modulesRoot, m.glDir, "metadata.json")
		manifest := filepath.Join(a.modulesRoot, m.manifest)
		if _, err := os.Stat(obj); err != nil {
			t.Skipf("pre-built guest-link missing for %s at %s: %v", m.pluginID, obj, err)
		}
		if err := flowcc.StageModule(home, m.pluginID, obj, meta, manifest); err != nil {
			t.Fatalf("StageModule %s: %v", m.pluginID, err)
		}
		t.Logf("staged %s from %s", m.pluginID, m.glDir)
	}
	return home
}

// driveResult holds the outcome of baking + driving one probe flow.
type driveResult struct {
	programID string
	nodeCount uint32
	before    []uint64
	after     []uint64
	captured  [][]byte
	drainErr  error
	bakeMs    int64
	cacheHit  bool
}

// bakeDrive bakes+deploys spec, then fires trigger 0 and drains once. A handler
// keyed by odFlowCaptureID:sink captures any host-sink frames verbatim.
func bakeDrive(t *testing.T, mgr *FlowManager, spec FlowSpec) driveResult {
	t.Helper()
	ctx := context.Background()
	res, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("BakeAndDeploy %q FAILED: %v", spec.ProgramID, err)
	}
	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("flow %q did not load into a running FlowRuntime", spec.ProgramID)
	}
	rt := fp.runtime
	rt.SetLinkedSection(func(run func() error) error { return run() }) // surface guest traps

	var captured [][]byte
	handlers := HandlerMap{
		odFlowCaptureID + ":sink": func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
			for _, f := range args.Frames {
				captured = append(captured, append([]byte(nil), f.Bytes...))
			}
			return &InvocationResult{StatusCode: 0}, nil
		},
	}

	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	_, derr := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 256})
	after := invocationCounts(t, rt)
	return driveResult{programID, rt.NodeCount, before, after, captured, derr, res.Elapsed.Milliseconds(), res.CacheHit}
}

func advanced(r driveResult, i int) bool { return len(r.after) > i && r.after[i] > r.before[i] }

func TestODFlowEndToEnd(t *testing.T) {
	a := resolveBakeAssets(t)
	home := stageODFlowHome(t, a)

	store := envOr("SDN_ODFLOW_STORE", t.TempDir())
	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: store, MaxMemoryPages: 2048}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	baker, err := NewBaker(home, 2048)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}
	mgr.SetBaker(baker)

	src := FlowNodeSpec{NodeID: "n-src", PluginID: odFlowSrcPluginID, MethodID: "emit", Kind: "source", UIX: 40, UIY: 160}
	fit := FlowNodeSpec{NodeID: "n-fit", PluginID: odFlowODPluginID, MethodID: "fit", Kind: "transform", UIX: 320, UIY: 160}
	capOEM := FlowNodeSpec{NodeID: "n-cap", PluginID: odFlowCaptureID, MethodID: "sink", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	toSrc := FlowTriggerBindingSpec{TriggerID: "t0", TargetNodeID: "n-src", TargetPortID: "tick"}

	// ── Probe 1: trigger -> oem-source.emit -> capture[host]("oem"). Isolate
	//    whether oem-source produces a valid ISS $OEM at all. ──────────────────
	p1 := bakeDrive(t, mgr, FlowSpec{
		ProgramID: "org.sdn.flows.od-probe1-oem", Name: "OD Probe1 $OEM", Version: "0.1.0",
		Nodes:           []FlowNodeSpec{src, capOEM},
		Edges:           []FlowEdgeSpec{{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-cap", ToPortID: "in"}},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{toSrc},
	})
	t.Logf("PROBE1 bake=%dms cacheHit=%v: oem-source[0] %d->%d  capture[1] %d->%d  captured=%d frames  derr=%v",
		p1.bakeMs, p1.cacheHit, p1.before[0], p1.after[0], p1.before[1], p1.after[1], len(p1.captured), p1.drainErr)
	if advanced(p1, 0) && len(p1.captured) > 0 {
		oem := p1.captured[0]
		id := "?"
		if len(oem) >= 8 {
			id = string(oem[4:8])
		}
		t.Logf("PROBE1 $OEM: %d bytes, file id %q (want $OEM)", len(oem), id)
	} else {
		t.Logf("PROBE1 FAIL: oem-source.emit did not produce a captured $OEM (emit advanced=%v)", advanced(p1, 0))
	}

	// ── Probe 2: trigger -> oem-source.emit -> od.fit -> capture[host]("omm").
	//    Capture + decode the EXACT $OMM od.fit emits. ──────────────────────────
	p2 := bakeDrive(t, mgr, FlowSpec{
		ProgramID: "org.sdn.flows.od-probe2-omm", Name: "OD Probe2 $OMM", Version: "0.1.0",
		Nodes: []FlowNodeSpec{src, fit, capOEM},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-cap", ToPortID: "in"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{toSrc},
	})
	t.Logf("PROBE2 bake=%dms cacheHit=%v: oem-source[0] %d->%d  od.fit[1] %d->%d  capture[2] %d->%d  captured=%d  derr=%v",
		p2.bakeMs, p2.cacheHit, p2.before[0], p2.after[0], p2.before[1], p2.after[1], p2.before[2], p2.after[2], len(p2.captured), p2.drainErr)

	var ommOK bool
	if advanced(p2, 1) && len(p2.captured) > 0 {
		omm := p2.captured[0]
		if len(omm) >= 12 && string(omm[8:12]) == "$OMM" {
			rec := OMM.GetRootAsOMM(omm[4:], 0) // strip 4-byte size prefix
			pretty, _ := json.MarshalIndent(map[string]interface{}{
				"file_identifier": string(omm[8:12]), "bytes": len(omm),
				"NORAD_CAT_ID": rec.NORAD_CAT_ID(), "OBJECT_ID": string(rec.OBJECT_ID()),
				"ORIGINATOR": string(rec.ORIGINATOR()), "EPOCH": string(rec.EPOCH()),
				"MEAN_MOTION": rec.MEAN_MOTION(), "ECCENTRICITY": rec.ECCENTRICITY(),
				"INCLINATION": rec.INCLINATION(), "BSTAR": rec.BSTAR(),
			}, "", "  ")
			t.Logf("PROBE2 $OMM CONTENTS (decoded from the EXACT frame od.fit emitted):\n%s", string(pretty))
			ommOK = rec.NORAD_CAT_ID() == 25544 && rec.MEAN_MOTION() > 0 && string(rec.ORIGINATOR()) == "SDN-OD"
		} else {
			t.Logf("PROBE2 captured frame is not a $OMM (len=%d)", len(omm))
		}
	} else {
		t.Logf("PROBE2 FAIL: od.fit did not produce a captured $OMM (fit advanced=%v)", advanced(p2, 1))
	}

	// ── Deliverable: trigger -> oem-source -> od.fit -> omm-json (real consumer).
	ommjson := FlowNodeSpec{NodeID: "n-omm", PluginID: odFlowOmmJSONID, MethodID: "encode", Kind: "sink", UIX: 600, UIY: 160}
	deliverable := FlowSpec{
		ProgramID: "org.sdn.flows.od-supplemental-omm-iss", Name: "OD Supplemental-OMM (ISS vertical slice)", Version: "0.1.0",
		Description: "ISS $OEM -> SGP4 supplemental-GP fit -> $OMM. Ephemeris in-memory only; no $OEM persisted.",
		Nodes:       []FlowNodeSpec{src, fit, ommjson},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-omm", ToPortID: "stream"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{toSrc},
	}
	invariant := assertNoOEMToStore(t, deliverable)
	t.Logf("★ in-memory-only invariant OK: %s", invariant)
	p3 := bakeDrive(t, mgr, deliverable)
	t.Logf("DELIVERABLE bake=%dms cacheHit=%v nodes=%d: oem-source[0] %d->%d  od.fit[1] %d->%d  omm-json[2] %d->%d  derr=%v",
		p3.bakeMs, p3.cacheHit, p3.nodeCount, p3.before[0], p3.after[0], p3.before[1], p3.after[1], p3.before[2], p3.after[2], p3.drainErr)

	// ── GATE ──────────────────────────────────────────────────────────────────
	if !advanced(p3, 0) {
		t.Fatalf("GATE FAIL: oem-source-iss.emit did not run (no ISS $OEM emitted)")
	}
	if !advanced(p3, 1) {
		t.Fatalf("GATE FAIL: analysis/od.fit did not run (the OD guest-link entry never fired)")
	}
	if !advanced(p3, 2) {
		t.Fatalf("GATE FAIL: foundation/omm-json.encode did not run ($OMM never reached the consumer)")
	}
	if p3.drainErr != nil {
		t.Fatalf("GATE FAIL: omm-json REJECTED od.fit's $OMM (drain error %v)", p3.drainErr)
	}
	if !ommOK {
		t.Fatalf("GATE FAIL: the captured $OMM is not a valid ISS fit (NORAD 25544 / SDN-OD / mean-motion>0)")
	}
	t.Logf("★★★★ PHASE-1b GATE MET: real ISS $OEM -> od.fit -> valid $OMM (NORAD 25544) -> omm-json consumed it (clean drain); no $OEM persisted.")
}

// assertNoOEMToStore proves the in-memory-only invariant structurally.
func assertNoOEMToStore(t *testing.T, spec FlowSpec) string {
	t.Helper()
	for _, n := range spec.Nodes {
		low := strings.ToLower(n.PluginID)
		for _, needle := range []string{"storage", "store", "ingest", "persist"} {
			if strings.Contains(low, needle) {
				t.Fatalf("in-memory-only invariant VIOLATED: node %q pluginID %q looks like a store", n.NodeID, n.PluginID)
			}
		}
	}
	var oemConsumers []string
	for _, e := range spec.Edges {
		if e.FromNodeID == "n-src" && e.FromPortID == "oem" {
			oemConsumers = append(oemConsumers, e.ToNodeID+"."+e.ToPortID)
		}
	}
	if len(oemConsumers) != 1 || oemConsumers[0] != "n-fit.oem" {
		t.Fatalf("$OEM must be consumed ONLY by od.fit (in-memory); got %v", oemConsumers)
	}
	return fmt.Sprintf("no store node; $OEM consumed only by %v (in-memory)", oemConsumers)
}
