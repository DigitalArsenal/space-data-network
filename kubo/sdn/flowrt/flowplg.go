package flowrt

// flowplg.go is the $PLG FlatBuffer flow-graph plumbing (V1: the flow definition
// IS the SDS $PLG record — "a degenerate flow is a module, flows use the module
// schema"). It REPLACES the bespoke JSON flow.json graph entirely: the bake
// pipeline, the on-disk store, and the timer-mount topology all read the flow
// graph from a $PLG FlatBuffer via the parsers here, and tests/fixtures build one
// via BuildFlowPLG.
//
// Mapping (SDS PLG -> internal bakeGraph): PLG.PLUGIN_ID -> programId,
// PLG.NAME -> name, PLG.VERSION -> version, FLOW_NODES -> nodes,
// FLOW_EDGES -> edges, FLOW_TRIGGERS -> triggers,
// FLOW_TRIGGER_BINDINGS -> triggerBindings. Node CONFIG stays [ubyte] (never
// JSON); the descriptor generator downstream is unchanged — it still reads the
// internal bakeGraph structs, now populated from the FlatBuffer instead of JSON.

import (
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
)

// FlowNodeSpec describes one composition-graph node for BuildFlowPLG. Config is
// opaque per-node bytes ([ubyte], never JSON).
type FlowNodeSpec struct {
	NodeID        string
	PluginID      string
	MethodID      string
	Kind          string
	DispatchModel string
	Config        []byte
	UIX           float32
	UIY           float32
}

// FlowEdgeSpec describes one directed edge wiring an output port to an input port.
type FlowEdgeSpec struct {
	EdgeID     string
	FromNodeID string
	FromPortID string
	ToNodeID   string
	ToPortID   string
}

// FlowTriggerSpec describes one flow trigger (timer/http).
type FlowTriggerSpec struct {
	TriggerID         string
	Kind              string
	Source            string
	DefaultIntervalMs uint64
	HTTPPath          string
}

// FlowTriggerBindingSpec binds a trigger to the node + input port it delivers to.
type FlowTriggerBindingSpec struct {
	TriggerID    string
	TargetNodeID string
	TargetPortID string
}

// FlowSpec is a plain-Go description of a composed flow graph, the input to
// BuildFlowPLG. ProgramID maps to the $PLG PLUGIN_ID field.
type FlowSpec struct {
	ProgramID       string
	Name            string
	Version         string
	Description     string
	Nodes           []FlowNodeSpec
	Edges           []FlowEdgeSpec
	Triggers        []FlowTriggerSpec
	TriggerBindings []FlowTriggerBindingSpec
}

// BuildFlowPLG builds a $PLG FlatBuffer (with the "$PLG" file identifier) from a
// FlowSpec. It is the small helper the fixture generator + flow tests use to
// express a flow graph as the canonical $PLG record the bake pipeline consumes.
func BuildFlowPLG(spec FlowSpec) []byte {
	b := flatbuffers.NewBuilder(1024)

	// Child tables first (no table may be open while creating strings/vectors).
	nodeOffs := make([]flatbuffers.UOffsetT, len(spec.Nodes))
	for i, n := range spec.Nodes {
		nodeID := b.CreateString(n.NodeID)
		pluginID := b.CreateString(n.PluginID)
		methodID := b.CreateString(n.MethodID)
		kind := b.CreateString(n.Kind)
		dispatch := b.CreateString(n.DispatchModel)
		var cfg flatbuffers.UOffsetT
		hasCfg := len(n.Config) > 0
		if hasCfg {
			PLG.PLGFlowNodeStartCONFIGVector(b, len(n.Config))
			for k := len(n.Config) - 1; k >= 0; k-- {
				b.PrependByte(n.Config[k])
			}
			cfg = b.EndVector(len(n.Config))
		}
		PLG.PLGFlowNodeStart(b)
		PLG.PLGFlowNodeAddNODE_ID(b, nodeID)
		PLG.PLGFlowNodeAddPLUGIN_ID(b, pluginID)
		PLG.PLGFlowNodeAddMETHOD_ID(b, methodID)
		PLG.PLGFlowNodeAddKIND(b, kind)
		PLG.PLGFlowNodeAddDISPATCH_MODEL(b, dispatch)
		if hasCfg {
			PLG.PLGFlowNodeAddCONFIG(b, cfg)
		}
		PLG.PLGFlowNodeAddUI_X(b, n.UIX)
		PLG.PLGFlowNodeAddUI_Y(b, n.UIY)
		nodeOffs[i] = PLG.PLGFlowNodeEnd(b)
	}

	edgeOffs := make([]flatbuffers.UOffsetT, len(spec.Edges))
	for i, e := range spec.Edges {
		edgeID := b.CreateString(e.EdgeID)
		fromNode := b.CreateString(e.FromNodeID)
		fromPort := b.CreateString(e.FromPortID)
		toNode := b.CreateString(e.ToNodeID)
		toPort := b.CreateString(e.ToPortID)
		PLG.PLGFlowEdgeStart(b)
		PLG.PLGFlowEdgeAddEDGE_ID(b, edgeID)
		PLG.PLGFlowEdgeAddFROM_NODE_ID(b, fromNode)
		PLG.PLGFlowEdgeAddFROM_PORT_ID(b, fromPort)
		PLG.PLGFlowEdgeAddTO_NODE_ID(b, toNode)
		PLG.PLGFlowEdgeAddTO_PORT_ID(b, toPort)
		edgeOffs[i] = PLG.PLGFlowEdgeEnd(b)
	}

	trigOffs := make([]flatbuffers.UOffsetT, len(spec.Triggers))
	for i, tr := range spec.Triggers {
		trigID := b.CreateString(tr.TriggerID)
		kind := b.CreateString(tr.Kind)
		source := b.CreateString(tr.Source)
		httpPath := b.CreateString(tr.HTTPPath)
		PLG.PLGFlowTriggerStart(b)
		PLG.PLGFlowTriggerAddTRIGGER_ID(b, trigID)
		PLG.PLGFlowTriggerAddKIND(b, kind)
		PLG.PLGFlowTriggerAddSOURCE(b, source)
		PLG.PLGFlowTriggerAddDEFAULT_INTERVAL_MS(b, tr.DefaultIntervalMs)
		PLG.PLGFlowTriggerAddHTTP_PATH(b, httpPath)
		trigOffs[i] = PLG.PLGFlowTriggerEnd(b)
	}

	bindOffs := make([]flatbuffers.UOffsetT, len(spec.TriggerBindings))
	for i, tb := range spec.TriggerBindings {
		trigID := b.CreateString(tb.TriggerID)
		targetNode := b.CreateString(tb.TargetNodeID)
		targetPort := b.CreateString(tb.TargetPortID)
		PLG.PLGFlowTriggerBindingStart(b)
		PLG.PLGFlowTriggerBindingAddTRIGGER_ID(b, trigID)
		PLG.PLGFlowTriggerBindingAddTARGET_NODE_ID(b, targetNode)
		PLG.PLGFlowTriggerBindingAddTARGET_PORT_ID(b, targetPort)
		bindOffs[i] = PLG.PLGFlowTriggerBindingEnd(b)
	}

	nodesVec := buildOffsetVector(b, nodeOffs, PLG.PLGStartFLOW_NODESVector)
	edgesVec := buildOffsetVector(b, edgeOffs, PLG.PLGStartFLOW_EDGESVector)
	trigsVec := buildOffsetVector(b, trigOffs, PLG.PLGStartFLOW_TRIGGERSVector)
	bindsVec := buildOffsetVector(b, bindOffs, PLG.PLGStartFLOW_TRIGGER_BINDINGSVector)

	pluginID := b.CreateString(spec.ProgramID)
	name := b.CreateString(spec.Name)
	version := b.CreateString(spec.Version)
	description := b.CreateString(spec.Description)

	PLG.PLGStart(b)
	PLG.PLGAddPLUGIN_ID(b, pluginID)
	PLG.PLGAddNAME(b, name)
	PLG.PLGAddVERSION(b, version)
	PLG.PLGAddDESCRIPTION(b, description)
	if nodesVec != 0 {
		PLG.PLGAddFLOW_NODES(b, nodesVec)
	}
	if edgesVec != 0 {
		PLG.PLGAddFLOW_EDGES(b, edgesVec)
	}
	if trigsVec != 0 {
		PLG.PLGAddFLOW_TRIGGERS(b, trigsVec)
	}
	if bindsVec != 0 {
		PLG.PLGAddFLOW_TRIGGER_BINDINGS(b, bindsVec)
	}
	root := PLG.PLGEnd(b)
	PLG.FinishPLGBuffer(b, root)
	return b.FinishedBytes()
}

// buildOffsetVector prepends the given table offsets (in reverse) into a
// FlatBuffer vector started by startVec. Returns 0 for an empty slice so the
// caller can omit the field.
func buildOffsetVector(b *flatbuffers.Builder, offs []flatbuffers.UOffsetT, startVec func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(offs) == 0 {
		return 0
	}
	startVec(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

// parsePLGGraph parses a $PLG FlatBuffer into the internal bakeGraph the
// descriptor generator + link logic consume. This is the JSON-free replacement
// for the old json.Unmarshal(flowJson, &bakeGraph).
func parsePLGGraph(buf []byte) (*bakeGraph, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("flowrt: $PLG buffer too small (%d bytes)", len(buf))
	}
	if !PLG.PLGBufferHasIdentifier(buf) {
		return nil, fmt.Errorf("flowrt: not a $PLG FlatBuffer (file identifier %q)", string(buf[4:8]))
	}
	root := PLG.GetRootAsPLG(buf, 0)

	g := &bakeGraph{
		ProgramID: string(root.PLUGIN_ID()),
		Name:      string(root.NAME()),
		Version:   string(root.VERSION()),
	}

	var node PLG.PLGFlowNode
	for i := 0; i < root.FLOW_NODESLength(); i++ {
		if !root.FLOW_NODES(&node, i) {
			continue
		}
		g.Nodes = append(g.Nodes, bakeNode{
			NodeID:        string(node.NODE_ID()),
			PluginID:      string(node.PLUGIN_ID()),
			MethodID:      string(node.METHOD_ID()),
			Kind:          string(node.KIND()),
			DispatchModel: string(node.DISPATCH_MODEL()),
		})
	}

	var edge PLG.PLGFlowEdge
	for i := 0; i < root.FLOW_EDGESLength(); i++ {
		if !root.FLOW_EDGES(&edge, i) {
			continue
		}
		g.Edges = append(g.Edges, bakeEdge{
			FromNodeID: string(edge.FROM_NODE_ID()),
			FromPortID: string(edge.FROM_PORT_ID()),
			ToNodeID:   string(edge.TO_NODE_ID()),
			ToPortID:   string(edge.TO_PORT_ID()),
		})
	}

	var trig PLG.PLGFlowTrigger
	for i := 0; i < root.FLOW_TRIGGERSLength(); i++ {
		if !root.FLOW_TRIGGERS(&trig, i) {
			continue
		}
		g.Triggers = append(g.Triggers, bakeTrigger{
			TriggerID:         string(trig.TRIGGER_ID()),
			Kind:              string(trig.KIND()),
			Source:            string(trig.SOURCE()),
			DefaultIntervalMs: int(trig.DEFAULT_INTERVAL_MS()),
			HTTPPath:          string(trig.HTTP_PATH()),
		})
	}

	var bind PLG.PLGFlowTriggerBinding
	for i := 0; i < root.FLOW_TRIGGER_BINDINGSLength(); i++ {
		if !root.FLOW_TRIGGER_BINDINGS(&bind, i) {
			continue
		}
		g.TriggerBindings = append(g.TriggerBindings, bakeTriggerBinding{
			TriggerID:    string(bind.TRIGGER_ID()),
			TargetNodeID: string(bind.TARGET_NODE_ID()),
			TargetPortID: string(bind.TARGET_PORT_ID()),
		})
	}

	return g, nil
}
