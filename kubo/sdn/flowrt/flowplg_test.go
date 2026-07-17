package flowrt

// flowplg_test.go verifies the V1 $PLG flow-graph plumbing (BuildFlowPLG +
// parsePLGGraph): a flow expressed as a $PLG FlatBuffer round-trips to the SAME
// node/edge/trigger structure the bake pipeline consumed from JSON before. It
// also DOUBLES as the fixture generator: it converts the existing
// celestrak-gp-ingest flow into a $PLG FlatBuffer file next to the source
// (when the modules monorepo flows dir is available) — keeping the graph
// semantics identical.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// celestrakGPIngestFlowSpec is the celestrak-gp-ingest flow graph, transcribed
// verbatim from space-data-network-modules/flows/celestrak-gp-ingest.flow.json
// (6 nodes, 10 edges, 1 timer trigger, 1 binding). This is the "small helper
// that builds the $PLG from the graph" the migration calls for.
func celestrakGPIngestFlowSpec() FlowSpec {
	return FlowSpec{
		ProgramID:   "com.digitalarsenal.flows.celestrak-gp-ingest",
		Name:        "CelesTrak GP Ingest Flow",
		Version:     "0.1.0",
		Description: "Per-provider ingest flow (loop C.8a), GP catalog.",
		Nodes: []FlowNodeSpec{
			{NodeID: "req", PluginID: "com.digitalarsenal.data-source.celestrak-request", MethodID: "gp", Kind: "transform"},
			{NodeID: "http", PluginID: "com.digitalarsenal.hostcap.http-request", MethodID: "request", Kind: "transform"},
			{NodeID: "parse", PluginID: "com.digitalarsenal.data-source.celestrak-parser", MethodID: "parse_gp", Kind: "transform"},
			{NodeID: "ingest_omm", PluginID: "com.digitalarsenal.hostcap.storage-ingest", MethodID: "ingest", Kind: "transform"},
			{NodeID: "ingest_mpe", PluginID: "com.digitalarsenal.hostcap.storage-ingest", MethodID: "ingest", Kind: "transform"},
			{NodeID: "egress", PluginID: "sdn.flow.egress", MethodID: "emit", Kind: "sink"},
		},
		Edges: []FlowEdgeSpec{
			{EdgeID: "req-http", FromNodeID: "req", FromPortID: "request", ToNodeID: "http", ToPortID: "request"},
			{EdgeID: "req-parse-job", FromNodeID: "req", FromPortID: "job", ToNodeID: "parse", ToPortID: "job"},
			{EdgeID: "http-parse", FromNodeID: "http", FromPortID: "response", ToNodeID: "parse", ToPortID: "response"},
			{EdgeID: "parse-omm-meta", FromNodeID: "parse", FromPortID: "omm_meta", ToNodeID: "ingest_omm", ToPortID: "meta"},
			{EdgeID: "parse-omm-records", FromNodeID: "parse", FromPortID: "omm_records", ToNodeID: "ingest_omm", ToPortID: "records"},
			{EdgeID: "parse-omm-raw", FromNodeID: "parse", FromPortID: "raw", ToNodeID: "ingest_omm", ToPortID: "raw"},
			{EdgeID: "parse-mpe-meta", FromNodeID: "parse", FromPortID: "mpe_meta", ToNodeID: "ingest_mpe", ToPortID: "meta"},
			{EdgeID: "parse-mpe-records", FromNodeID: "parse", FromPortID: "mpe_records", ToNodeID: "ingest_mpe", ToPortID: "records"},
			{EdgeID: "ingest-omm-egress", FromNodeID: "ingest_omm", FromPortID: "result", ToNodeID: "egress", ToPortID: "result"},
			{EdgeID: "ingest-mpe-egress", FromNodeID: "ingest_mpe", FromPortID: "result", ToNodeID: "egress", ToPortID: "result"},
		},
		Triggers: []FlowTriggerSpec{
			{TriggerID: "timer-gp", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 10800000},
		},
		TriggerBindings: []FlowTriggerBindingSpec{
			{TriggerID: "timer-gp", TargetNodeID: "req", TargetPortID: "tick"},
		},
	}
}

// TestFlowPLGRoundTrip proves a small flow with EVERY field populated round-trips
// through BuildFlowPLG -> parsePLGGraph with identical semantics (this is the
// JSON-free replacement for the old json.Marshal/Unmarshal round-trip).
func TestFlowPLGRoundTrip(t *testing.T) {
	spec := FlowSpec{
		ProgramID:   "com.example.flow.rt",
		Name:        "Round Trip",
		Version:     "1.2.3",
		Description: "desc",
		Nodes: []FlowNodeSpec{
			{NodeID: "a", PluginID: "p.a", MethodID: "m.a", Kind: "transform"},
			{NodeID: "b", PluginID: "p.b", MethodID: "m.b", Kind: "sink", DispatchModel: "host-capability"},
		},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "a", FromPortID: "out", ToNodeID: "b", ToPortID: "in"},
		},
		Triggers: []FlowTriggerSpec{
			{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 5000},
			{TriggerID: "t1", Kind: "http", Source: "host-http", HTTPPath: "/hook"},
		},
		TriggerBindings: []FlowTriggerBindingSpec{
			{TriggerID: "t0", TargetNodeID: "a", TargetPortID: "tick"},
		},
	}

	buf := BuildFlowPLG(spec)
	g, err := parsePLGGraph(buf)
	if err != nil {
		t.Fatalf("parsePLGGraph: %v", err)
	}
	if g.ProgramID != spec.ProgramID || g.Name != spec.Name || g.Version != spec.Version {
		t.Fatalf("identity mismatch: %+v", g)
	}
	if len(g.Nodes) != 2 || len(g.Edges) != 1 || len(g.Triggers) != 2 || len(g.TriggerBindings) != 1 {
		t.Fatalf("count mismatch: nodes=%d edges=%d triggers=%d bindings=%d", len(g.Nodes), len(g.Edges), len(g.Triggers), len(g.TriggerBindings))
	}
	if g.Nodes[1].DispatchModel != "host-capability" || g.Nodes[1].Kind != "sink" {
		t.Fatalf("node[1] fields lost: %+v", g.Nodes[1])
	}
	if g.Edges[0].FromNodeID != "a" || g.Edges[0].ToPortID != "in" {
		t.Fatalf("edge fields lost: %+v", g.Edges[0])
	}
	if g.Triggers[0].DefaultIntervalMs != 5000 || g.Triggers[0].Kind != "timer" {
		t.Fatalf("trigger[0] fields lost: %+v", g.Triggers[0])
	}
	if g.Triggers[1].HTTPPath != "/hook" {
		t.Fatalf("trigger[1] http path lost: %+v", g.Triggers[1])
	}
	if g.TriggerBindings[0].TargetNodeID != "a" || g.TriggerBindings[0].TargetPortID != "tick" {
		t.Fatalf("binding fields lost: %+v", g.TriggerBindings[0])
	}

	// FlowProgram parse (flowplugin path) must see the triggers too.
	prog, err := parseFlowProgramPLG(buf)
	if err != nil {
		t.Fatalf("parseFlowProgramPLG: %v", err)
	}
	if prog.ProgramID != spec.ProgramID || len(prog.Triggers) != 2 || prog.Triggers[0].DefaultIntervalMs != 5000 {
		t.Fatalf("FlowProgram mismatch: %+v", prog)
	}
}

// TestCelestrakGPIngestPLGRoundTrip is the acceptance round-trip for the real
// fixture: the celestrak-gp-ingest flow, expressed as a $PLG FlatBuffer, parses
// back to the SAME node/edge/trigger structure as the original JSON graph.
func TestCelestrakGPIngestPLGRoundTrip(t *testing.T) {
	buf := BuildFlowPLG(celestrakGPIngestFlowSpec())
	g, err := parsePLGGraph(buf)
	if err != nil {
		t.Fatalf("parsePLGGraph: %v", err)
	}
	if g.ProgramID != "com.digitalarsenal.flows.celestrak-gp-ingest" {
		t.Fatalf("programId=%q", g.ProgramID)
	}
	if len(g.Nodes) != 6 || len(g.Edges) != 10 || len(g.Triggers) != 1 || len(g.TriggerBindings) != 1 {
		t.Fatalf("celestrak structure mismatch: nodes=%d edges=%d triggers=%d bindings=%d (want 6/10/1/1)",
			len(g.Nodes), len(g.Edges), len(g.Triggers), len(g.TriggerBindings))
	}
	if g.Nodes[0].NodeID != "req" || g.Nodes[0].PluginID != "com.digitalarsenal.data-source.celestrak-request" || g.Nodes[0].MethodID != "gp" {
		t.Fatalf("node[0] mismatch: %+v", g.Nodes[0])
	}
	if g.Triggers[0].TriggerID != "timer-gp" || g.Triggers[0].Kind != "timer" || g.Triggers[0].DefaultIntervalMs != 10800000 {
		t.Fatalf("trigger mismatch: %+v", g.Triggers[0])
	}
	if g.TriggerBindings[0].TargetNodeID != "req" || g.TriggerBindings[0].TargetPortID != "tick" {
		t.Fatalf("binding mismatch: %+v", g.TriggerBindings[0])
	}
	t.Logf("★ celestrak-gp-ingest $PLG round-trip OK: %d nodes, %d edges, %d trigger, %d binding",
		len(g.Nodes), len(g.Edges), len(g.Triggers), len(g.TriggerBindings))
}

// TestGenerateCelestrakGPIngestFixture converts the celestrak-gp-ingest flow into
// a $PLG FlatBuffer FILE (celestrak-gp-ingest.flow.plg) next to the source, when
// the modules monorepo flows dir is available. It skips cleanly otherwise. The
// written file is re-parsed to confirm it is a valid $PLG with the expected
// structure.
func TestGenerateCelestrakGPIngestFixture(t *testing.T) {
	flowsDir := resolveFlowsFixtureDir(t)
	buf := BuildFlowPLG(celestrakGPIngestFlowSpec())

	outPath := filepath.Join(flowsDir, "celestrak-gp-ingest.flow.plg")
	if err := os.WriteFile(outPath, buf, 0o644); err != nil {
		t.Fatalf("write %s: %v", outPath, err)
	}
	t.Logf("wrote $PLG fixture: %s (%d bytes)", outPath, len(buf))

	// Re-read the file and confirm it parses back to the same structure.
	readBack, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read back %s: %v", outPath, err)
	}
	g, err := parsePLGGraph(readBack)
	if err != nil {
		t.Fatalf("parse written fixture: %v", err)
	}
	if len(g.Nodes) != 6 || len(g.Edges) != 10 || len(g.Triggers) != 1 || len(g.TriggerBindings) != 1 {
		t.Fatalf("written fixture structure mismatch: nodes=%d edges=%d triggers=%d bindings=%d",
			len(g.Nodes), len(g.Edges), len(g.Triggers), len(g.TriggerBindings))
	}
}

// resolveFlowsFixtureDir locates the modules monorepo flows directory (holding
// the *.flow.json sources), honoring SDN_FLOWS_FIXTURE_DIR, else probing paths
// relative to this test file. Skips when it cannot be found.
func resolveFlowsFixtureDir(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("SDN_FLOWS_FIXTURE_DIR"); env != "" {
		return env
	}
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller failed")
	}
	anchor := filepath.Dir(caller) // .../space-data-network/kubo/sdn/flowrt
	candidates := []string{
		filepath.Join(anchor, "..", "..", "..", "..", "space-data-network-modules", "flows"),
		filepath.Join(anchor, "..", "..", "..", "..", "..", "space-data-network-modules", "flows"),
		filepath.Join(anchor, "..", "..", "..", "..", "..", "..", "space-data-network-modules", "flows"),
	}
	for _, c := range candidates {
		cleaned := filepath.Clean(c)
		if _, err := os.Stat(filepath.Join(cleaned, "celestrak-gp-ingest.flow.json")); err == nil {
			return cleaned
		}
	}
	t.Skipf("modules flows dir not found (set SDN_FLOWS_FIXTURE_DIR); checked %v", candidates)
	return ""
}
