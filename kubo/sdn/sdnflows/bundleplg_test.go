package sdnflows_test

// bundleplg_test.go bridges the compiled flow bundles (which still ship a
// flow.json authored by the SDK/editor) to the V1 flow-runtime, which reads the
// flow graph from a flow.plg ($PLG FlatBuffer) — see sdn/flowrt/flowplg.go and
// cronmount.go. Until the editor/SDK emits flow.plg directly (a later phase),
// ensureBundlePLG transcodes a bundle's flow.json into a sibling flow.plg via
// flowrt.BuildFlowPLG, keeping the graph semantics identical. Reading flow.json
// here is a TEST-ONLY migration bridge; the production bake path is JSON-free.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipfs/kubo/sdn/flowrt"
)

// srcFlowGraph mirrors the subset of a compiled bundle's flow.json that the
// flow-runtime topology needs (graph structure + trigger cadence/bindings).
type srcFlowGraph struct {
	ProgramID   string `json:"programId"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Nodes       []struct {
		NodeID        string `json:"nodeId"`
		PluginID      string `json:"pluginId"`
		MethodID      string `json:"methodId"`
		Kind          string `json:"kind"`
		DispatchModel string `json:"dispatchModel"`
	} `json:"nodes"`
	Edges []struct {
		EdgeID     string `json:"edgeId"`
		FromNodeID string `json:"fromNodeId"`
		FromPortID string `json:"fromPortId"`
		ToNodeID   string `json:"toNodeId"`
		ToPortID   string `json:"toPortId"`
	} `json:"edges"`
	Triggers []struct {
		TriggerID         string `json:"triggerId"`
		Kind              string `json:"kind"`
		Source            string `json:"source"`
		DefaultIntervalMs uint64 `json:"defaultIntervalMs"`
		HTTPPath          string `json:"httpPath"`
	} `json:"triggers"`
	TriggerBindings []struct {
		TriggerID    string `json:"triggerId"`
		TargetNodeID string `json:"targetNodeId"`
		TargetPortID string `json:"targetPortId"`
	} `json:"triggerBindings"`
}

// ensureBundlePLG writes bundleDir/flow.plg from bundleDir/flow.json (idempotent;
// overwrites). A bundle with no flow.json is left untouched. It is the fixture
// bridge that lets the flow-runtime (which now reads flow.plg) load the existing
// compiled bundles in tests.
func ensureBundlePLG(t *testing.T, bundleDir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundleDir, "flow.json"))
	if err != nil {
		return // no flow.json to transcode (bundle may already ship flow.plg)
	}
	var g srcFlowGraph
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("ensureBundlePLG: parse %s/flow.json: %v", bundleDir, err)
	}
	spec := flowrt.FlowSpec{
		ProgramID:   g.ProgramID,
		Name:        g.Name,
		Version:     g.Version,
		Description: g.Description,
	}
	for _, n := range g.Nodes {
		spec.Nodes = append(spec.Nodes, flowrt.FlowNodeSpec{
			NodeID: n.NodeID, PluginID: n.PluginID, MethodID: n.MethodID,
			Kind: n.Kind, DispatchModel: n.DispatchModel,
		})
	}
	for _, e := range g.Edges {
		spec.Edges = append(spec.Edges, flowrt.FlowEdgeSpec{
			EdgeID: e.EdgeID, FromNodeID: e.FromNodeID, FromPortID: e.FromPortID,
			ToNodeID: e.ToNodeID, ToPortID: e.ToPortID,
		})
	}
	for _, tr := range g.Triggers {
		spec.Triggers = append(spec.Triggers, flowrt.FlowTriggerSpec{
			TriggerID: tr.TriggerID, Kind: tr.Kind, Source: tr.Source,
			DefaultIntervalMs: tr.DefaultIntervalMs, HTTPPath: tr.HTTPPath,
		})
	}
	for _, tb := range g.TriggerBindings {
		spec.TriggerBindings = append(spec.TriggerBindings, flowrt.FlowTriggerBindingSpec{
			TriggerID: tb.TriggerID, TargetNodeID: tb.TargetNodeID, TargetPortID: tb.TargetPortID,
		})
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "flow.plg"), flowrt.BuildFlowPLG(spec), 0o644); err != nil {
		t.Fatalf("ensureBundlePLG: write %s/flow.plg: %v", bundleDir, err)
	}
}
