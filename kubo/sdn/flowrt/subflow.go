package flowrt

// subflow.go is the Phase-4 make-or-break: it re-exports a COMPOSED FLOW as a
// single, re-composable MODULE. A baked flow is a finalized runtime.wasm (one
// memory, every module linked in) — great to run, impossible to drop back into
// the palette and wire into ANOTHER flow. This emitter produces the OTHER form
// the recursion needs: a RELOCATABLE guest-link object (mirroring how the module
// SDK's compileModule.js emits module-link.o) whose single method entry
// `sdm_guest_<hex>_<method>` drives the flow's INNER graph and whose ABI is the
// flow's AGGREGATE external ports. Publish that object as an ordinary module and
// a NEW flow can link it as one node — "a module is a degenerate flow" closing
// the loop.
//
// # Why this needs no symbol renaming or a bundled second runtime
//
// The naive approach — bundle the flow's own flow_runtime + descriptor into the
// object — collides at the outer link: two flow_runtimes define the same
// plugin_*/space_data_module_runtime_*/g_flow_program symbols. Instead the
// emitted wrapper carries NO runtime of its own. It drives its inner nodes over
// the ONE shim the outer flow_runtime already provides, using the generic
// shim-control primitives added to flow_runtime.cpp
// (space_data_module_shim_reset_inputs / _add_input / _output_*): read the outer
// node's aggregate inputs (plugin_get_input_*), then for each ready inner node
// stage its queued frames into the shim, call the inner guest-link entry, harvest
// what it pushed (space_data_module_shim_output_*), route along the inner edges,
// and finally push the flow's aggregate outputs back (plugin_push_output). The
// object therefore DEFINES only its one entry (+ the bundled inner module
// objects, already prefixed per plugin) and IMPORTS the shim ABI + libc — exactly
// like an ordinary guest-link module. The outer bake links it identically.
//
// The object is produced by `wasm-ld -r` (relocatable partial link) of the
// generated wrapper + the flow's distinct inner module objects, so it stages and
// bakes as a single module-link.o. A finalized module.wasm (the isomorphic form)
// is optionally produced by baking the flow standalone.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// SubflowExternalPort is one aggregate (external) port of a flow-as-module: the
// port id G wires to (ExtPort) bound to an inner node's port (NodeID.Port), with
// the SDS type(s) it carries for the palette. A flow author (or auto-derivation)
// declares which inner ports are the flow's public interface.
type SubflowExternalPort struct {
	ExtPort string   `json:"extPort"`
	NodeID  string   `json:"nodeId"`
	Port    string   `json:"port"`
	Types   []string `json:"types,omitempty"`
	Any     bool     `json:"any,omitempty"`
}

// SubflowSpec asks EmitSubflowModule to wrap a composed flow as a module. Inputs
// and Outputs are the flow's aggregate ports; when both are empty they are
// AUTO-DERIVED from the graph's unbound ports (declared inner ports with no inner
// edge). A wired inner port can still be exposed by naming it explicitly (a
// Node-RED subflow output can tap an internal wire), which is why explicit
// mappings are additive over auto-derivation, not replaced by it.
type SubflowSpec struct {
	PluginID     string // module pluginId (default: flow programId)
	Method       string // aggregate method id (default: "run")
	FlowPLG      []byte // the composed flow graph as a $PLG FlatBuffer
	Inputs       []SubflowExternalPort
	Outputs      []SubflowExternalPort
	FinalizeWasm bool // also emit a finalized module.wasm (Bake the flow standalone)
}

// SubflowModule is the emitted flow-as-module: a relocatable guest-link object +
// the metadata/manifest that stage + publish it exactly like any other module,
// so it drops into the palette and composes into another flow.
type SubflowModule struct {
	PluginID     string
	Method       string
	EntrySymbol  string
	SymbolPrefix string
	GuestLinkObj []byte // relocatable module-link.o (wasm-ld -r)
	Metadata     []byte // metadata.json (methodSymbols)
	Manifest     []byte // plugin-manifest.json (aggregate typed ports)
	Finalized    []byte // finalized module.wasm (optional; Bake of the flow)
	Inputs       []SubflowExternalPort
	Outputs      []SubflowExternalPort
	Modules      []string // distinct inner pluginIds bundled, sorted
}

// guestLinkSymbolPrefix mirrors the module SDK's compileModule.js
// guestLinkSymbolPrefix EXACTLY: "sdm_guest_" + hex(pluginId bytes)[:24] + "_".
// Reused so a flow-module's entry symbol is namespaced the identical way every
// other guest-link module's is.
func guestLinkSymbolPrefix(pluginID string) string {
	h := hex.EncodeToString([]byte(pluginID))
	if len(h) > 24 {
		h = h[:24]
	}
	return "sdm_guest_" + h + "_"
}

// EmitSubflowModule wraps a composed flow as a re-composable guest-link module.
// It resolves each inner node's staged guest-link entry + typed ports, derives
// the flow's aggregate ports, generates the driver wrapper, compiles it, and
// partial-links it with the distinct inner module objects into a single
// relocatable module-link.o. The result stages/publishes like any module.
func (b *Baker) EmitSubflowModule(ctx context.Context, spec SubflowSpec) (*SubflowModule, error) {
	if len(spec.FlowPLG) == 0 {
		return nil, fmt.Errorf("subflow: missing flowPlg")
	}
	g, err := parsePLGGraph(spec.FlowPLG)
	if err != nil {
		return nil, fmt.Errorf("subflow: parse flowPlg: %w", err)
	}
	if g.ProgramID == "" {
		return nil, fmt.Errorf("subflow: flowPlg missing programId (PLUGIN_ID)")
	}
	if len(g.Nodes) == 0 {
		return nil, fmt.Errorf("subflow: flow has no nodes")
	}

	pluginID := spec.PluginID
	if pluginID == "" {
		pluginID = g.ProgramID
	}
	method := spec.Method
	if method == "" {
		method = "run"
	}
	prefix := guestLinkSymbolPrefix(pluginID)
	entry := prefix + method

	// Resolve every inner node to its staged guest-link entry symbol + typed
	// input/output ports, and collect the distinct inner module objects to link.
	sub, err := b.resolveSubflow(g)
	if err != nil {
		return nil, err
	}

	// Aggregate ports: explicit mappings win; otherwise auto-derive the unbound
	// ports (declared inner ports with no inner edge on that side).
	inputs := spec.Inputs
	outputs := spec.Outputs
	if len(inputs) == 0 && len(outputs) == 0 {
		inputs, outputs = deriveAggregatePorts(g, sub)
	}
	if len(inputs) == 0 && len(outputs) == 0 {
		return nil, fmt.Errorf("subflow: flow %q exposes no aggregate ports (all inner ports are internally wired); declare Inputs/Outputs explicitly", g.ProgramID)
	}

	// Generate + compile the driver wrapper, then partial-link it with the inner
	// module objects into one relocatable guest-link object.
	wrapperCpp := generateSubflowWrapper(g, sub, inputs, outputs, entry)
	invokeHdr, err := os.ReadFile(b.home.InvokeHeaderPath())
	if err != nil {
		return nil, fmt.Errorf("subflow: read space_data_module_invoke.h: %w", err)
	}
	wrapperO, err := b.compileSubflowWrapper(ctx, wrapperCpp, invokeHdr)
	if err != nil {
		return nil, err
	}
	obj, err := b.linkSubflowObject(ctx, wrapperO, sub.deps)
	if err != nil {
		return nil, err
	}

	meta := buildSubflowMetadata(prefix, method, entry)
	manifest := buildSubflowManifest(pluginID, method, inputs, outputs)

	out := &SubflowModule{
		PluginID:     pluginID,
		Method:       method,
		EntrySymbol:  entry,
		SymbolPrefix: prefix,
		GuestLinkObj: obj,
		Metadata:     meta,
		Manifest:     manifest,
		Inputs:       inputs,
		Outputs:      outputs,
		Modules:      sub.moduleIDs,
	}

	if spec.FinalizeWasm {
		res, ferr := b.Bake(ctx, BakeRequest{FlowPLG: append([]byte(nil), spec.FlowPLG...)})
		if ferr != nil {
			return nil, fmt.Errorf("subflow: finalize module.wasm: %w", ferr)
		}
		out.Finalized = res.Wasm
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Inner-graph resolution
// ---------------------------------------------------------------------------

// subInnerNode is one resolved inner node: its bound guest-link entry symbol and
// its declared typed input/output port ids (from the staged manifest).
type subInnerNode struct {
	nodeID   string
	pluginID string
	methodID string
	symbol   string   // "" for a non-linked (host) node — unsupported in a sub-flow
	inPorts  []string // declared typed input port ids
	outPorts []string // declared typed output port ids
	depIndex int      // index into resolvedSubflow.deps (module object), -1 for host
}

// resolvedSubflow is the resolved inner graph: nodes in graph order, the distinct
// module objects to link, and the port-type index for aggregate-port typing.
type resolvedSubflow struct {
	nodes     []subInnerNode
	deps      []depObject
	moduleIDs []string
	portTypes map[string]map[string][]string // pluginId -> portId -> types
	portAny   map[string]map[string]bool     // pluginId -> portId -> any
	nodeIndex map[string]int
}

// resolveSubflow validates the inner graph, resolves each linked node's entry
// symbol + typed ports from staged metadata, and loads the distinct module
// objects (mirroring bake.resolve, but for the sub-flow's inner nodes).
func (b *Baker) resolveSubflow(g *bakeGraph) (*resolvedSubflow, error) {
	nodeIndex := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		if n.NodeID == "" {
			return nil, fmt.Errorf("subflow: node[%d] missing nodeId", i)
		}
		if _, dup := nodeIndex[n.NodeID]; dup {
			return nil, fmt.Errorf("subflow: duplicate nodeId %q", n.NodeID)
		}
		nodeIndex[n.NodeID] = i
	}

	out := &resolvedSubflow{
		nodes:     make([]subInnerNode, len(g.Nodes)),
		portTypes: map[string]map[string][]string{},
		portAny:   map[string]map[string]bool{},
		nodeIndex: nodeIndex,
	}
	depByPlugin := map[string]int{}
	for i, n := range g.Nodes {
		if !n.linked() {
			return nil, fmt.Errorf("subflow: node %q is host-model (dispatchModel=%q); a re-composable flow-module supports only linked-direct inner nodes", n.NodeID, n.model())
		}
		if n.PluginID == "" || n.MethodID == "" {
			return nil, fmt.Errorf("subflow: linked node %q missing pluginId/methodId", n.NodeID)
		}
		meta, err := b.home.LoadModuleMetadata(n.PluginID)
		if err != nil {
			return nil, err
		}
		sym := meta.MethodSymbols[n.MethodID]
		if sym == "" {
			return nil, fmt.Errorf("subflow: module %q exports no symbol for method %q (have %v)", n.PluginID, n.MethodID, methodKeys(meta.MethodSymbols))
		}
		node := subInnerNode{
			nodeID:   n.NodeID,
			pluginID: n.PluginID,
			methodID: n.MethodID,
			symbol:   sym,
			depIndex: -1,
		}
		// Typed ports for aggregate-port derivation + typing.
		for _, mp := range meta.MethodPorts {
			if mp.MethodID != n.MethodID {
				continue
			}
			for _, p := range mp.InputPorts {
				if p.PortID != "" {
					node.inPorts = append(node.inPorts, p.PortID)
				}
				out.recordPortType(n.PluginID, p.PortID, p.Types, p.Any)
			}
			for _, p := range mp.OutputPorts {
				if p.PortID != "" {
					node.outPorts = append(node.outPorts, p.PortID)
				}
				out.recordPortType(n.PluginID, p.PortID, p.Types, p.Any)
			}
		}
		if idx, ok := depByPlugin[n.PluginID]; ok {
			node.depIndex = idx
		} else {
			ob, err := os.ReadFile(b.home.ModuleLinkObjectPath(n.PluginID))
			if err != nil {
				return nil, fmt.Errorf("subflow: read staged object for %q: %w", n.PluginID, err)
			}
			d := depObject{pluginID: n.PluginID, index: len(out.deps), bytes: ob}
			node.depIndex = d.index
			out.deps = append(out.deps, d)
			depByPlugin[n.PluginID] = d.index
			out.moduleIDs = append(out.moduleIDs, n.PluginID)
		}
		out.nodes[i] = node
	}
	if len(out.deps) == 0 {
		return nil, fmt.Errorf("subflow: flow has no linked-direct inner nodes")
	}
	sort.Strings(out.moduleIDs)
	return out, nil
}

func (r *resolvedSubflow) recordPortType(pluginID, portID string, types []string, any bool) {
	if portID == "" {
		return
	}
	if r.portTypes[pluginID] == nil {
		r.portTypes[pluginID] = map[string][]string{}
		r.portAny[pluginID] = map[string]bool{}
	}
	if len(types) > 0 {
		r.portTypes[pluginID][portID] = types
	}
	if any {
		r.portAny[pluginID][portID] = true
	}
}

// deriveAggregatePorts auto-derives the flow's unbound ports: a declared inner
// input port with no inner edge targeting it becomes an aggregate INPUT; a
// declared inner output port that feeds no inner edge becomes an aggregate
// OUTPUT. Aggregate port ids are the inner port id when unique across the flow,
// else "<nodeId>__<portId>" to disambiguate. Deterministic (graph order).
func deriveAggregatePorts(g *bakeGraph, sub *resolvedSubflow) (inputs, outputs []SubflowExternalPort) {
	wiredIn := map[string]map[string]bool{}  // nodeId -> port -> true
	wiredOut := map[string]map[string]bool{} // nodeId -> port -> true
	mark := func(m map[string]map[string]bool, node, port string) {
		if port == "" {
			return
		}
		if m[node] == nil {
			m[node] = map[string]bool{}
		}
		m[node][port] = true
	}
	for _, e := range g.Edges {
		mark(wiredOut, e.FromNodeID, e.FromPortID)
		mark(wiredIn, e.ToNodeID, e.ToPortID)
	}

	inCount := map[string]int{}
	outCount := map[string]int{}
	for _, n := range sub.nodes {
		for _, p := range n.inPorts {
			if !wiredIn[n.nodeID][p] {
				inCount[p]++
			}
		}
		for _, p := range n.outPorts {
			if !wiredOut[n.nodeID][p] {
				outCount[p]++
			}
		}
	}
	extID := func(count map[string]int, node, port string) string {
		if count[port] > 1 {
			return node + "__" + port
		}
		return port
	}
	for _, n := range sub.nodes {
		for _, p := range n.inPorts {
			if wiredIn[n.nodeID][p] {
				continue
			}
			inputs = append(inputs, SubflowExternalPort{
				ExtPort: extID(inCount, n.nodeID, p), NodeID: n.nodeID, Port: p,
				Types: sub.portTypes[n.pluginID][p], Any: sub.portAny[n.pluginID][p],
			})
		}
		for _, p := range n.outPorts {
			if wiredOut[n.nodeID][p] {
				continue
			}
			outputs = append(outputs, SubflowExternalPort{
				ExtPort: extID(outCount, n.nodeID, p), NodeID: n.nodeID, Port: p,
				Types: sub.portTypes[n.pluginID][p], Any: sub.portAny[n.pluginID][p],
			})
		}
	}
	return inputs, outputs
}

// ---------------------------------------------------------------------------
// Wrapper codegen — a self-contained inner scheduler over the shared shim.
// ---------------------------------------------------------------------------

// generateSubflowWrapper emits the driver wrapper.cpp: an entry that seeds its
// inner queues from the outer node's aggregate inputs, runs the inner graph over
// the shared shim (staging each ready node's frames, calling its guest-link
// entry, routing what it pushes along the inner edges), and pushes the flow's
// aggregate outputs back to the outer runtime. All inner state is wrapper-local;
// the object imports only the shim ABI + the inner entries.
func generateSubflowWrapper(g *bakeGraph, sub *resolvedSubflow, inputs, outputs []SubflowExternalPort, entry string) string {
	var b strings.Builder
	b.WriteString("// GENERATED by sdn/flowrt subflow.go — sub-flow module wrapper. Do not edit.\n")
	b.WriteString("#include <stdint.h>\n#include <string.h>\n#include <string>\n#include <vector>\n\n")
	b.WriteString("#include \"space_data_module_invoke.h\"\n\n")

	// Generic shim-control primitives provided by the OUTER flow_runtime.
	b.WriteString("extern \"C\" void space_data_module_shim_reset_inputs(void);\n")
	b.WriteString("extern \"C\" void space_data_module_shim_add_input(const char*, const uint8_t*, uint32_t);\n")
	b.WriteString("extern \"C\" uint32_t space_data_module_shim_output_count(void);\n")
	b.WriteString("extern \"C\" const char* space_data_module_shim_output_port(uint32_t);\n")
	b.WriteString("extern \"C\" const uint8_t* space_data_module_shim_output_payload(uint32_t);\n")
	b.WriteString("extern \"C\" uint32_t space_data_module_shim_output_size(uint32_t);\n\n")

	// Inner guest-link entries (deduped) + a node-indexed entry table.
	seen := map[string]bool{}
	for _, n := range sub.nodes {
		if n.symbol != "" && !seen[n.symbol] {
			seen[n.symbol] = true
			fmt.Fprintf(&b, "extern \"C\" int32_t %s(void);\n", n.symbol)
		}
	}
	b.WriteString("\nnamespace {\n")
	b.WriteString("typedef int32_t (*sub_entry_fn)(void);\n")
	b.WriteString("static sub_entry_fn g_entry[] = {\n")
	for _, n := range sub.nodes {
		fmt.Fprintf(&b, "  %s,\n", n.symbol)
	}
	b.WriteString("};\n")
	fmt.Fprintf(&b, "static const uint32_t kNodeCount = %du;\n\n", len(sub.nodes))

	// Inner edges.
	b.WriteString("struct SubEdge { uint32_t from_node; const char* from_port; uint32_t to_node; const char* to_port; };\n")
	fmt.Fprintf(&b, "static const SubEdge g_edges[%du%s] = {\n", len(g.Edges), plusOneIfZero(len(g.Edges)))
	for _, e := range g.Edges {
		from, ok1 := sub.nodeIndex[e.FromNodeID]
		to, ok2 := sub.nodeIndex[e.ToNodeID]
		if !ok1 || !ok2 {
			continue
		}
		fmt.Fprintf(&b, "  { %du, %s, %du, %s },\n", from, cQuote(e.FromPortID), to, cQuote(e.ToPortID))
	}
	if len(g.Edges) == 0 {
		b.WriteString("  { 0u, \"\", 0u, \"\" },\n")
	}
	b.WriteString("};\n")
	fmt.Fprintf(&b, "static const uint32_t kEdgeCount = %du;\n\n", len(g.Edges))

	// Aggregate input map: ext port id -> (inner node, inner port).
	b.WriteString("struct SubIn { const char* ext_port; uint32_t node; const char* port; };\n")
	fmt.Fprintf(&b, "static const SubIn g_inputs[%du%s] = {\n", len(inputs), plusOneIfZero(len(inputs)))
	for _, p := range inputs {
		idx := sub.nodeIndex[p.NodeID]
		fmt.Fprintf(&b, "  { %s, %du, %s },\n", cQuote(p.ExtPort), idx, cQuote(p.Port))
	}
	if len(inputs) == 0 {
		b.WriteString("  { \"\", 0u, \"\" },\n")
	}
	b.WriteString("};\n")
	fmt.Fprintf(&b, "static const uint32_t kInputCount = %du;\n\n", len(inputs))

	// Aggregate output map: (inner node, inner port) -> ext port id.
	b.WriteString("struct SubOut { uint32_t node; const char* port; const char* ext_port; };\n")
	fmt.Fprintf(&b, "static const SubOut g_outputs[%du%s] = {\n", len(outputs), plusOneIfZero(len(outputs)))
	for _, p := range outputs {
		idx := sub.nodeIndex[p.NodeID]
		fmt.Fprintf(&b, "  { %du, %s, %s },\n", idx, cQuote(p.Port), cQuote(p.ExtPort))
	}
	if len(outputs) == 0 {
		b.WriteString("  { 0u, \"\", \"\" },\n")
	}
	b.WriteString("};\n")
	fmt.Fprintf(&b, "static const uint32_t kOutputCount = %du;\n\n", len(outputs))

	// Required-input-port rows (typed inputs wired by an inner edge), so a
	// multi-input inner node fires only once all its wired inputs are present —
	// mirroring the outer runtime's flow_node_is_ready gate.
	required := computeSubflowRequiredPorts(g, sub)
	b.WriteString("struct SubReq { uint32_t node; const char* port; };\n")
	fmt.Fprintf(&b, "static const SubReq g_required[%du%s] = {\n", len(required), plusOneIfZero(len(required)))
	for _, r := range required {
		fmt.Fprintf(&b, "  { %du, %s },\n", r.node, cQuote(r.port))
	}
	if len(required) == 0 {
		b.WriteString("  { 0u, \"\" },\n")
	}
	b.WriteString("};\n")
	fmt.Fprintf(&b, "static const uint32_t kRequiredCount = %du;\n\n", len(required))

	// The inner scheduler lives in the anonymous namespace (internal linkage, so
	// it never collides at the outer link); only the per-flow entry is exported.
	b.WriteString(subflowWrapperBody)
	b.WriteString("\n} // namespace\n")
	fmt.Fprintf(&b, "\nextern \"C\" __attribute__((visibility(\"default\"))) int32_t %s(void) {\n", entry)
	b.WriteString("  return sub_flow_run();\n}\n")
	return b.String()
}

// subflowWrapperBody is the flow-agnostic inner scheduler the generated tables
// drive. It stays in an anonymous namespace so it never collides at the outer
// link; only the per-flow entry (appended above) is exported.
const subflowWrapperBody = `
struct SubFrame { std::string port; std::vector<uint8_t> payload; };

static bool sub_node_ready(const std::vector<SubFrame>* q, uint32_t node) {
  if (q[node].empty()) return false;
  for (uint32_t r = 0; r < kRequiredCount; r++) {
    if (g_required[r].node != node) continue;
    bool present = false;
    for (const SubFrame& f : q[node]) {
      if (strcmp(f.port.c_str(), g_required[r].port) == 0) { present = true; break; }
    }
    if (!present) return false;
  }
  return true;
}

static void sub_push(std::vector<SubFrame>* q, uint32_t node, const char* port,
                     const uint8_t* payload, uint32_t len) {
  SubFrame f;
  f.port = port ? port : "";
  if (payload && len) f.payload.assign(payload, payload + len);
  q[node].push_back(static_cast<SubFrame&&>(f));
}

static int32_t sub_flow_run() {
  std::vector<SubFrame> q[kNodeCount];
  std::vector<SubFrame> agg_out;

  // Seed inner queues from the outer node's aggregate inputs.
  uint32_t nin = plugin_get_input_count();
  for (uint32_t i = 0; i < nin; i++) {
    const plugin_input_frame_t* f = plugin_get_input_frame(i);
    if (!f) continue;
    const char* port = f->port_id ? f->port_id : "";
    for (uint32_t k = 0; k < kInputCount; k++) {
      if (strcmp(g_inputs[k].ext_port, port) != 0) continue;
      sub_push(q, g_inputs[k].node, g_inputs[k].port, f->payload, f->payload_length);
    }
  }

  // Drive the inner graph to quiescence over the shared shim.
  for (int iter = 0; iter < 8192; iter++) {
    uint32_t node = kNodeCount;
    for (uint32_t n = 0; n < kNodeCount; n++) {
      if (sub_node_ready(q, n)) { node = n; break; }
    }
    if (node == kNodeCount) break;

    space_data_module_shim_reset_inputs();
    for (const SubFrame& f : q[node]) {
      space_data_module_shim_add_input(f.port.c_str(),
                                       f.payload.empty() ? nullptr : f.payload.data(),
                                       static_cast<uint32_t>(f.payload.size()));
    }
    q[node].clear();
    plugin_reset_output_state();

    g_entry[node]();

    uint32_t nout = space_data_module_shim_output_count();
    for (uint32_t o = 0; o < nout; o++) {
      const char* op = space_data_module_shim_output_port(o);
      const uint8_t* pp = space_data_module_shim_output_payload(o);
      uint32_t ps = space_data_module_shim_output_size(o);
      for (uint32_t e = 0; e < kEdgeCount; e++) {
        if (g_edges[e].from_node == node && strcmp(g_edges[e].from_port, op) == 0) {
          sub_push(q, g_edges[e].to_node, g_edges[e].to_port, pp, ps);
        }
      }
      for (uint32_t w = 0; w < kOutputCount; w++) {
        if (g_outputs[w].node == node && strcmp(g_outputs[w].port, op) == 0) {
          SubFrame f;
          f.port = g_outputs[w].ext_port;
          if (pp && ps) f.payload.assign(pp, pp + ps);
          agg_out.push_back(static_cast<SubFrame&&>(f));
        }
      }
    }
  }

  // Emit the flow's aggregate outputs to the outer runtime.
  plugin_reset_output_state();
  for (const SubFrame& f : agg_out) {
    plugin_push_output(f.port.c_str(), nullptr, nullptr,
                       f.payload.empty() ? nullptr : f.payload.data(),
                       static_cast<uint32_t>(f.payload.size()));
  }
  return 0;
}
`

// computeSubflowRequiredPorts derives the readiness rows: a node's typed input
// ports intersected with the ports actually wired into it (inner edge toPort).
// Only wired+declared inputs are required — so a frame can always arrive and a
// multi-input node waits for all of them. Mirrors bake.go's computeRequiredPorts.
func computeSubflowRequiredPorts(g *bakeGraph, sub *resolvedSubflow) []requiredPort {
	wired := map[string]map[string]bool{}
	for _, e := range g.Edges {
		if e.ToPortID == "" {
			continue
		}
		if wired[e.ToNodeID] == nil {
			wired[e.ToNodeID] = map[string]bool{}
		}
		wired[e.ToNodeID][e.ToPortID] = true
	}
	var rows []requiredPort
	for i, n := range sub.nodes {
		if len(wired[n.nodeID]) == 0 || len(n.inPorts) == 0 {
			continue
		}
		seen := map[string]bool{}
		for _, port := range n.inPorts {
			if port == "" || seen[port] || !wired[n.nodeID][port] {
				continue
			}
			seen[port] = true
			rows = append(rows, requiredPort{node: i, port: port})
		}
	}
	return rows
}

// ---------------------------------------------------------------------------
// Compile + link
// ---------------------------------------------------------------------------

func (b *Baker) compileSubflowWrapper(ctx context.Context, wrapperCpp string, invokeHdr []byte) ([]byte, error) {
	compile := append([]string{"clang", "clang", "-c", "/work/wrapper.cpp", "-I/work", "-o", "/work/wrapper.o"}, flowRuntimeCompileFlags...)
	res, err := b.cc.Run(ctx, compile, map[string][]byte{
		"/work/wrapper.cpp":                []byte(wrapperCpp),
		"/work/space_data_module_invoke.h": invokeHdr,
	})
	if err != nil || res.ExitCode != 0 {
		return nil, fmt.Errorf("subflow: compile wrapper.cpp: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	obj, ok := res.OutFiles["/work/wrapper.o"]
	if !ok {
		return nil, fmt.Errorf("subflow: compile produced no wrapper.o")
	}
	return obj, nil
}

// linkSubflowObject partial-links (wasm-ld -r) the wrapper + the distinct inner
// module objects into ONE relocatable guest-link object. -r resolves the
// wrapper's references to the inner entries against the bundled module objects
// while leaving the shim ABI + libc symbols undefined, so the object composes
// into an outer flow exactly like a hand-written module's module-link.o.
func (b *Baker) linkSubflowObject(ctx context.Context, wrapperO []byte, deps []depObject) ([]byte, error) {
	inFiles := map[string][]byte{"/work/wrapper.o": wrapperO}
	link := []string{"lld", "wasm-ld", "-r", "--strip-debug", "/work/wrapper.o"}
	for _, d := range deps {
		name := "/work/dep" + strconv.Itoa(d.index) + ".o"
		inFiles[name] = d.bytes
		link = append(link, name)
	}
	link = append(link, "-o", "/work/subflow-link.o")
	res, err := b.cc.Run(ctx, link, inFiles)
	if err != nil || res.ExitCode != 0 {
		return nil, fmt.Errorf("subflow: wasm-ld -r link: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	obj, ok := res.OutFiles["/work/subflow-link.o"]
	if !ok {
		return nil, fmt.Errorf("subflow: link produced no subflow-link.o")
	}
	if len(obj) < 8 || obj[0] != 0x00 || obj[1] != 'a' || obj[2] != 's' || obj[3] != 'm' {
		return nil, fmt.Errorf("subflow: linked artifact is not a WASM object")
	}
	return obj, nil
}

// ---------------------------------------------------------------------------
// Metadata + manifest (stage/publish like any module)
// ---------------------------------------------------------------------------

// buildSubflowMetadata writes the dist metadata.json a staged guest-link module
// ships: the method -> entry-symbol map the bake resolves against.
func buildSubflowMetadata(prefix, method, entry string) []byte {
	m := map[string]interface{}{
		"version":       1,
		"format":        "wasm-object",
		"language":      "c++",
		"threadModel":   "single-thread",
		"symbolPrefix":  prefix,
		"methodSymbols": map[string]string{method: entry},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

// buildSubflowManifest writes a plugin-manifest.json whose single method's
// input/output ports are the flow's aggregate ports, carrying each port's SDS
// type(s) so the editor types wires into the flow-module the same way it does a
// hand-written module.
func buildSubflowManifest(pluginID, method string, inputs, outputs []SubflowExternalPort) []byte {
	toPorts := func(ps []SubflowExternalPort) []map[string]interface{} {
		out := make([]map[string]interface{}, 0, len(ps))
		for _, p := range ps {
			allowed := make([]map[string]interface{}, 0, len(p.Types)+1)
			for _, t := range p.Types {
				allowed = append(allowed, map[string]interface{}{"fileIdentifier": "$" + t})
			}
			if p.Any || len(allowed) == 0 {
				allowed = append(allowed, map[string]interface{}{"acceptsAnyFlatbuffer": true})
			}
			out = append(out, map[string]interface{}{
				"portId":           p.ExtPort,
				"acceptedTypeSets": []map[string]interface{}{{"allowedTypes": allowed}},
			})
		}
		return out
	}
	m := map[string]interface{}{
		"pluginId":     pluginID,
		"pluginFamily": "FLOW",
		"methods": []map[string]interface{}{{
			"methodId":    method,
			"description": "Composed flow re-exported as a module (sub-flow).",
			"inputPorts":  toPorts(inputs),
			"outputPorts": toPorts(outputs),
		}},
	}
	b, _ := json.Marshal(m)
	return b
}
