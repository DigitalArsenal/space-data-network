package flowrt

// bake.go is the NODE-SIDE BAKE path: it turns the proven flowcc compose spike
// (kubo/sdn/flowcc/flow_bake_test.go) into a real feature. Given a flow document
// and a set of module references, the node resolves each ref to its staged
// guest-link object, GENERATES the per-flow descriptor C sources, compiles the
// flow-runtime template with its OWN clang, LINKS a composed runtime.wasm with
// its OWN wasm-ld (all hosted on WasmEdge via flowcc), and hands the finalized
// artifact to the existing FlowStore.Install / NewFlowRuntime path.
//
// The bake reproduces flow_bake_test.go's proven recipe EXACTLY: the same
// EH-free compile flags (-fignore-exceptions, NO -fwasm-exceptions) and the same
// STANDALONE_WASM reactor link line, so the artifact loads and runs under the
// node's WasmEdge (0.16.4) the same way the deployed modules do. The only new
// work here is (1) resolving module refs to staged objects and (2) GENERATING
// descriptor.c + flow_generated.inc from the flow graph instead of hand-writing
// them — everything downstream is the spike.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/flowcc"
)

// BakeModuleRef names a module to link into a baked flow. PluginID is required;
// ContentHash, when set, is verified against the staged guest-link object's
// sha256 (fail-closed on mismatch).
type BakeModuleRef struct {
	PluginID    string `json:"pluginId"`
	ContentHash string `json:"contentHash,omitempty"`
}

// BakeRequest is the POST body for the node-side bake path. FlowJSON is the full
// flow document (the same graph flowCompiler.js consumes: programId + nodes +
// edges + triggers + triggerBindings). ModuleRefs is OPTIONAL — the distinct
// pluginIds referenced by the graph's linked nodes are baked in regardless; an
// explicit ref list only adds content-hash pinning.
type BakeRequest struct {
	FlowJSON   json.RawMessage `json:"flowJson"`
	ModuleRefs []BakeModuleRef `json:"moduleRefs,omitempty"`
}

// BakeResult reports what a bake produced (for the HTTP response + tests).
type BakeResult struct {
	Wasm         []byte
	FlowJSON     []byte
	ProgramID    string
	Modules      []string      // distinct pluginIds linked, sorted
	FlowRuntimeO int           // bytes of the (possibly cached) flow_runtime.o
	CacheHit     bool          // flow_runtime.o served from cache
	Elapsed      time.Duration // wall-clock of the whole bake
}

// ---------------------------------------------------------------------------
// Flow-graph subset the descriptor generator reads.
// ---------------------------------------------------------------------------

type bakeGraph struct {
	ProgramID       string               `json:"programId"`
	Name            string               `json:"name"`
	Version         string               `json:"version"`
	Nodes           []bakeNode           `json:"nodes"`
	Edges           []bakeEdge           `json:"edges"`
	Triggers        []bakeTrigger        `json:"triggers"`
	TriggerBindings []bakeTriggerBinding `json:"triggerBindings"`
}

type bakeNode struct {
	NodeID        string `json:"nodeId"`
	PluginID      string `json:"pluginId"`
	MethodID      string `json:"methodId"`
	Kind          string `json:"kind"`
	DispatchModel string `json:"dispatchModel"` // "" -> linked-direct
}

// linked reports whether this node dispatches via a linked-direct guest-link
// entry (the bake target). Empty defaults to linked-direct.
func (n bakeNode) linked() bool {
	return n.DispatchModel == "" || n.DispatchModel == "linked-direct"
}

func (n bakeNode) model() string {
	if n.DispatchModel == "" {
		return "linked-direct"
	}
	return n.DispatchModel
}

type bakeEdge struct {
	FromNodeID string `json:"fromNodeId"`
	FromPortID string `json:"fromPortId"`
	ToNodeID   string `json:"toNodeId"`
	ToPortID   string `json:"toPortId"`
}

type bakeTrigger struct {
	TriggerID string `json:"triggerId"`
}

type bakeTriggerBinding struct {
	TriggerID    string `json:"triggerId"`
	TargetNodeID string `json:"targetNodeId"`
	TargetPortID string `json:"targetPortId"`
}

// ---------------------------------------------------------------------------
// Baker
// ---------------------------------------------------------------------------

// Baker bakes flows using the node's staged flowcc toolchain. It is safe for
// concurrent use: flowcc.Compiler.Run is concurrency-safe and the flow_runtime.o
// cache is content-addressed (write-once by sha).
type Baker struct {
	home           flowcc.Home
	cc             *flowcc.Compiler
	maxMemoryPages uint32
}

// NewBaker constructs a Baker against a staged flowcc home. It fails if the
// toolchain (llvm-box.wasm + sysroot + template) is not staged — callers that
// only need the prebuilt-wasm deploy path simply do not attach a Baker.
func NewBaker(home flowcc.Home, maxMemoryPages uint32) (*Baker, error) {
	if !home.Staged() {
		return nil, fmt.Errorf("flowrt: flowcc toolchain not staged at %s (run flowcc.StageToolchain)", home.Root())
	}
	cc, err := flowcc.NewWithSysroot(home.BoxPath(), home.SysrootDir())
	if err != nil {
		return nil, fmt.Errorf("flowrt: init flowcc compiler: %w", err)
	}
	return &Baker{home: home, cc: cc, maxMemoryPages: maxMemoryPages}, nil
}

// Bake resolves the request's modules, generates the per-flow descriptor,
// compiles + links the composed runtime.wasm, and returns it (NOT yet
// installed). It reproduces flow_bake_test.go's proven flags verbatim.
func (b *Baker) Bake(ctx context.Context, req BakeRequest) (*BakeResult, error) {
	start := time.Now()
	if len(req.FlowJSON) == 0 {
		return nil, fmt.Errorf("bake: missing flowJson")
	}
	var g bakeGraph
	if err := json.Unmarshal(req.FlowJSON, &g); err != nil {
		return nil, fmt.Errorf("bake: parse flowJson: %w", err)
	}
	if g.ProgramID == "" {
		return nil, fmt.Errorf("bake: flowJson missing programId")
	}
	if len(g.Nodes) == 0 {
		return nil, fmt.Errorf("bake: flow has no nodes")
	}

	// Resolve every linked node to its staged guest-link entry symbol, and
	// collect the distinct module objects to link.
	gen, deps, err := b.resolve(&g, req.ModuleRefs)
	if err != nil {
		return nil, err
	}

	// Read the fixed template files from node data.
	tplCpp, err := os.ReadFile(b.home.FlowRuntimeCppPath())
	if err != nil {
		return nil, fmt.Errorf("bake: read flow_runtime.cpp template: %w", err)
	}
	tplHdr, err := os.ReadFile(b.home.InvokeHeaderPath())
	if err != nil {
		return nil, fmt.Errorf("bake: read space_data_module_invoke.h template: %w", err)
	}

	// Stage 1: compile flow_runtime.cpp (template + generated inc). Cached by
	// the sha of (template + header + inc + flags): a re-bake of an unchanged
	// flow skips the expensive template compile.
	flowRuntimeO, cacheHit, err := b.compileFlowRuntime(ctx, tplCpp, tplHdr, gen.inc)
	if err != nil {
		return nil, err
	}

	// Stage 2: compile the generated per-flow descriptor.c (tiny; not cached).
	descriptorO, err := b.compileDescriptor(ctx, gen.descriptorC)
	if err != nil {
		return nil, err
	}

	// Stage 3: link the composed runtime.wasm.
	wasm, err := b.link(ctx, flowRuntimeO, descriptorO, deps)
	if err != nil {
		return nil, err
	}
	if len(wasm) < 8 || wasm[0] != 0x00 || wasm[1] != 'a' || wasm[2] != 's' || wasm[3] != 'm' {
		return nil, fmt.Errorf("bake: linked artifact is not a WASM module")
	}

	mods := make([]string, 0, len(deps))
	for _, d := range deps {
		mods = append(mods, d.pluginID)
	}
	return &BakeResult{
		Wasm:         wasm,
		FlowJSON:     append([]byte(nil), req.FlowJSON...),
		ProgramID:    g.ProgramID,
		Modules:      mods,
		FlowRuntimeO: len(flowRuntimeO),
		CacheHit:     cacheHit,
		Elapsed:      time.Since(start),
	}, nil
}

// depObject is one distinct module's staged guest-link object, assigned a
// stable dep index for the /work/dep<i>.o link input naming.
type depObject struct {
	pluginID string
	index    int
	bytes    []byte
}

// resolve validates the graph, resolves each linked node's entry symbol from the
// staged module metadata, generates the descriptor sources, and loads the
// distinct module objects (verifying content hashes when refs supply them).
func (b *Baker) resolve(g *bakeGraph, refs []BakeModuleRef) (*generated, []depObject, error) {
	// Content-hash pins keyed by pluginId (optional).
	pin := map[string]string{}
	for _, r := range refs {
		if r.PluginID != "" && r.ContentHash != "" {
			pin[r.PluginID] = strings.ToLower(r.ContentHash)
		}
	}

	// nodeId -> index for edge/trigger resolution.
	nodeIndex := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		if n.NodeID == "" {
			return nil, nil, fmt.Errorf("bake: node[%d] missing nodeId", i)
		}
		if _, dup := nodeIndex[n.NodeID]; dup {
			return nil, nil, fmt.Errorf("bake: duplicate nodeId %q", n.NodeID)
		}
		nodeIndex[n.NodeID] = i
	}

	// Resolve each linked node's symbol; collect distinct module objects.
	depByPlugin := map[string]*depObject{}
	var deps []depObject
	symbols := make([]string, len(g.Nodes)) // "" for host nodes
	for i, n := range g.Nodes {
		if !n.linked() {
			continue
		}
		if n.PluginID == "" || n.MethodID == "" {
			return nil, nil, fmt.Errorf("bake: linked node %q missing pluginId/methodId", n.NodeID)
		}
		meta, err := b.home.LoadModuleMetadata(n.PluginID)
		if err != nil {
			return nil, nil, err
		}
		sym := meta.MethodSymbols[n.MethodID]
		if sym == "" {
			return nil, nil, fmt.Errorf("bake: module %q exports no symbol for method %q (have %v)",
				n.PluginID, n.MethodID, methodKeys(meta.MethodSymbols))
		}
		symbols[i] = sym

		if _, ok := depByPlugin[n.PluginID]; !ok {
			objPath := b.home.ModuleLinkObjectPath(n.PluginID)
			ob, err := os.ReadFile(objPath)
			if err != nil {
				return nil, nil, fmt.Errorf("bake: read staged object for %q: %w", n.PluginID, err)
			}
			if want, ok := pin[n.PluginID]; ok {
				got := sha256Hex(ob)
				if got != want {
					return nil, nil, fmt.Errorf("bake: module %q content-hash mismatch: staged %s, ref %s", n.PluginID, got, want)
				}
			}
			d := depObject{pluginID: n.PluginID, index: len(deps), bytes: ob}
			deps = append(deps, d)
			depByPlugin[n.PluginID] = &deps[len(deps)-1]
		}
	}
	if len(deps) == 0 {
		return nil, nil, fmt.Errorf("bake: flow has no linked-direct nodes to bake")
	}

	gen, err := generateDescriptorSources(g, nodeIndex, symbols, len(deps))
	if err != nil {
		return nil, nil, err
	}
	return gen, deps, nil
}

// compileFlowRuntime compiles the flow-runtime template + generated inc into
// flow_runtime.o, serving from the content-addressed cache on a hit.
func (b *Baker) compileFlowRuntime(ctx context.Context, tplCpp, tplHdr []byte, inc string) ([]byte, bool, error) {
	key := sha256Hex(bytesJoin(tplCpp, tplHdr, []byte(inc), []byte(flowRuntimeCompileFlagsKey)))
	cachePath := filepath.Join(b.home.FlowRuntimeCacheDir(), key+".o")
	if cached, err := os.ReadFile(cachePath); err == nil && len(cached) > 0 {
		return cached, true, nil
	}

	compile := append([]string{"clang", "clang", "-c", "/work/flow_runtime.cpp", "-I/work",
		"-o", "/work/flow_runtime.o"}, flowRuntimeCompileFlags...)
	res, err := b.cc.Run(ctx, compile, map[string][]byte{
		"/work/flow_runtime.cpp":           tplCpp,
		"/work/space_data_module_invoke.h": tplHdr,
		"/work/flow_generated.inc":         []byte(inc),
	})
	if err != nil || res.ExitCode != 0 {
		return nil, false, fmt.Errorf("bake: compile flow_runtime.cpp: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	obj, ok := res.OutFiles["/work/flow_runtime.o"]
	if !ok {
		return nil, false, fmt.Errorf("bake: compile produced no flow_runtime.o")
	}
	if err := os.MkdirAll(b.home.FlowRuntimeCacheDir(), 0o755); err == nil {
		tmp := cachePath + ".tmp"
		if os.WriteFile(tmp, obj, 0o644) == nil {
			_ = os.Rename(tmp, cachePath) // best-effort; atomic on the same fs
		}
	}
	return obj, false, nil
}

// compileDescriptor compiles the generated descriptor.c into descriptor.o.
func (b *Baker) compileDescriptor(ctx context.Context, descriptorC string) ([]byte, error) {
	res, err := b.cc.Run(ctx, []string{
		"clang", "clang", "-c", "/work/descriptor.c", "-o", "/work/descriptor.o",
		"-target", "wasm32-emscripten", "--sysroot=/sysroot", "-O3",
	}, map[string][]byte{"/work/descriptor.c": []byte(descriptorC)})
	if err != nil || res.ExitCode != 0 {
		return nil, fmt.Errorf("bake: compile descriptor.c: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	obj, ok := res.OutFiles["/work/descriptor.o"]
	if !ok {
		return nil, fmt.Errorf("bake: compile produced no descriptor.o")
	}
	return obj, nil
}

// link runs the STANDALONE_WASM reactor link line (flow_bake_test.go verbatim),
// producing the composed runtime.wasm.
func (b *Baker) link(ctx context.Context, flowRuntimeO, descriptorO []byte, deps []depObject) ([]byte, error) {
	inFiles := map[string][]byte{
		"/work/flow_runtime.o": flowRuntimeO,
		"/work/descriptor.o":   descriptorO,
	}
	link := []string{
		"lld", "wasm-ld",
		"--entry=_initialize",
		"--export-table",
		"--allow-undefined", "--import-undefined",
		"--max-memory=2147483648", "-z", "stack-size=65536", "--global-base=1024",
		"--strip-debug",
		"/work/flow_runtime.o", "/work/descriptor.o",
	}
	for _, d := range deps {
		name := "/work/dep" + strconv.Itoa(d.index) + ".o"
		inFiles[name] = d.bytes
		link = append(link, name)
	}
	link = append(link,
		"--export-if-defined=emscripten_stack_get_current",
		"--export-if-defined=_emscripten_stack_restore",
	)
	for _, e := range bakeRuntimeExports() {
		link = append(link, "--export="+e)
	}
	sr := "/sysroot/lib/wasm32-emscripten"
	link = append(link,
		"-L"+sr, sr+"/crt1_reactor.o",
		"-lGL", "-lal", "-lhtml5", "-lstandalonewasm", "-lstubs", "-lnoexit",
		"-lc", "-ldlmalloc", "-lcompiler_rt", "-lc++-noexcept", "-lc++abi-noexcept", "-lsockets",
		"-o", "/work/runtime.wasm",
	)
	res, err := b.cc.Run(ctx, link, inFiles)
	if err != nil || res.ExitCode != 0 {
		return nil, fmt.Errorf("bake: wasm-ld link: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	wasm, ok := res.OutFiles["/work/runtime.wasm"]
	if !ok {
		return nil, fmt.Errorf("bake: link produced no runtime.wasm")
	}
	return wasm, nil
}

// flowRuntimeCompileFlags mirror flow_bake_test.go's Stage-1 compile flags
// EXACTLY (EH-free, WasmEdge-0.16.4-compatible: -fignore-exceptions, no
// -fwasm-exceptions). flowRuntimeCompileFlagsKey folds them into the cache key.
var flowRuntimeCompileFlags = []string{
	"-target", "wasm32-emscripten", "--sysroot=/sysroot",
	"-std=c++17", "-O3", "-fignore-exceptions", "-fno-rtti",
	"-fvisibility=hidden", "-mbulk-memory", "-DNDEBUG", "-DEMSCRIPTEN",
}

const flowRuntimeCompileFlagsKey = "wasm32-emscripten|c++17|O3|fignore-exceptions|fno-rtti|fvisibility=hidden|mbulk-memory|NDEBUG|EMSCRIPTEN|v1"

// bakeRuntimeExports is the flow-artifact export surface wasm-ld must keep: the
// full compiled-runtime ABI runtime.go reads (compiledRuntimeExportNames) plus
// the in-wasm scheduler loop (drain_linked).
func bakeRuntimeExports() []string {
	out := append([]string(nil), compiledRuntimeExportNames...)
	out = append(out, runtimeExportDrainLinked)
	return out
}

// ---------------------------------------------------------------------------
// Descriptor generator — emits flow_generated.inc + descriptor.c from the graph.
// This is the ONLY new codegen; it reproduces the structure the flowcc compose
// spike hand-wrote (see kubo/sdn/flowcc/flow_bake_test.go inputs).
// ---------------------------------------------------------------------------

type generated struct {
	inc         string
	descriptorC string
}

func generateDescriptorSources(g *bakeGraph, nodeIndex map[string]int, symbols []string, depCount int) (*generated, error) {
	n := len(g.Nodes)

	// Resolve edges/trigger bindings to node/trigger indices.
	trigIndex := make(map[string]int, len(g.Triggers))
	for i, t := range g.Triggers {
		if t.TriggerID != "" {
			trigIndex[t.TriggerID] = i
		}
	}
	triggerCount := len(g.Triggers)
	if triggerCount == 0 {
		triggerCount = 1 // g_ingress_states[FLOW_TRIGGER_COUNT] must not be zero-length
	}

	var inc strings.Builder
	fmt.Fprintf(&inc, "// GENERATED by sdn/flowrt bake.go for flow %q — do not edit.\n", g.ProgramID)
	fmt.Fprintf(&inc, "#define FLOW_NODE_COUNT %du\n", n)
	fmt.Fprintf(&inc, "#define FLOW_EDGE_COUNT %du\n", len(g.Edges))
	fmt.Fprintf(&inc, "#define FLOW_TRIGGER_COUNT %du\n", triggerCount)
	fmt.Fprintf(&inc, "#define FLOW_DEP_COUNT %du\n", depCount)
	fmt.Fprintf(&inc, "#define FLOW_TRIGGER_BINDING_COUNT %du\n", len(g.TriggerBindings))
	fmt.Fprintf(&inc, "#define FLOW_REQUIRED_PORT_COUNT 0u\n\n")

	// extern "C" entry declarations (deduped).
	seen := map[string]bool{}
	for _, s := range symbols {
		if s != "" && !seen[s] {
			seen[s] = true
			fmt.Fprintf(&inc, "extern \"C\" int %s(void);\n", s)
		}
	}
	inc.WriteString("\n")

	// Per-node string constants.
	for i, node := range g.Nodes {
		fmt.Fprintf(&inc, "static const char kStr_node%d_id[] = %s;\n", i, cQuote(node.NodeID))
		fmt.Fprintf(&inc, "static const char kStr_node%d_plugin[] = %s;\n", i, cQuote(node.PluginID))
		fmt.Fprintf(&inc, "static const char kStr_node%d_method[] = %s;\n", i, cQuote(node.MethodID))
		fmt.Fprintf(&inc, "static const char kStr_node%d_model[] = %s;\n", i, cQuote(node.model()))
	}
	inc.WriteString("static const char kStr_sym_malloc[] = \"malloc\";\n")
	inc.WriteString("static const char kStr_sym_free[] = \"free\";\n\n")

	// Edges.
	fmt.Fprintf(&inc, "FlowEdge g_edges[FLOW_EDGE_COUNT%s] = {\n", plusOneIfZero(len(g.Edges)))
	for _, e := range g.Edges {
		from, ok1 := nodeIndex[e.FromNodeID]
		to, ok2 := nodeIndex[e.ToNodeID]
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("bake: edge references unknown node (%q -> %q)", e.FromNodeID, e.ToNodeID)
		}
		fmt.Fprintf(&inc, "  { %du, %s, %du, %s },\n", from, cQuote(e.FromPortID), to, cQuote(e.ToPortID))
	}
	if len(g.Edges) == 0 {
		inc.WriteString("  { 0u, \"\", 0u, \"\" },\n")
	}
	inc.WriteString("};\n")

	// Trigger bindings.
	fmt.Fprintf(&inc, "FlowTriggerBinding g_trigger_bindings[FLOW_TRIGGER_BINDING_COUNT%s] = {\n", plusOneIfZero(len(g.TriggerBindings)))
	for _, tb := range g.TriggerBindings {
		ti := trigIndex[tb.TriggerID] // 0 if unnamed
		tn, ok := nodeIndex[tb.TargetNodeID]
		if !ok {
			return nil, fmt.Errorf("bake: trigger binding references unknown node %q", tb.TargetNodeID)
		}
		fmt.Fprintf(&inc, "  { %du, %du, %s },\n", ti, tn, cQuote(tb.TargetPortID))
	}
	if len(g.TriggerBindings) == 0 {
		inc.WriteString("  { 0u, 0u, \"\" },\n")
	}
	inc.WriteString("};\n")

	// Required ports (Phase 0: none; readiness gates on queued frames only).
	inc.WriteString("FlowRequiredPort g_required_ports[FLOW_REQUIRED_PORT_COUNT + 1] = {\n  { 0u, \"\" },\n};\n\n")

	// Descriptor tables + init.
	inc.WriteString("FlowNodeDispatchDescriptorC g_dispatch_descriptors[FLOW_NODE_COUNT];\n")
	inc.WriteString("SignedArtifactDependencyDescriptorC g_dependency_descriptors[FLOW_DEP_COUNT];\n\n")
	inc.WriteString("static void flow_init_descriptors() {\n")
	for i := range g.Nodes {
		inc.WriteString("  {\n")
		fmt.Fprintf(&inc, "    FlowNodeDispatchDescriptorC &d = g_dispatch_descriptors[%d];\n", i)
		fmt.Fprintf(&inc, "    d.node_id_ptr = reinterpret_cast<uint32_t>(kStr_node%d_id);\n", i)
		fmt.Fprintf(&inc, "    d.node_index = %du;\n", i)
		fmt.Fprintf(&inc, "    d.plugin_id_ptr = reinterpret_cast<uint32_t>(kStr_node%d_plugin);\n", i)
		fmt.Fprintf(&inc, "    d.method_id_ptr = reinterpret_cast<uint32_t>(kStr_node%d_method);\n", i)
		fmt.Fprintf(&inc, "    d.dispatch_model_ptr = reinterpret_cast<uint32_t>(kStr_node%d_model);\n", i)
		inc.WriteString("    d.malloc_symbol_ptr = reinterpret_cast<uint32_t>(kStr_sym_malloc);\n")
		inc.WriteString("    d.free_symbol_ptr = reinterpret_cast<uint32_t>(kStr_sym_free);\n")
		inc.WriteString("  }\n")
	}
	inc.WriteString("}\n")
	inc.WriteString("static struct FlowDescriptorInit { FlowDescriptorInit() { flow_init_descriptors(); } } g_flow_descriptor_init;\n\n")

	// flow_node_is_linked switch.
	inc.WriteString("static inline bool flow_node_is_linked(uint32_t node) {\n  switch (node) {")
	for i, node := range g.Nodes {
		if node.linked() {
			fmt.Fprintf(&inc, " case %du:", i)
		}
	}
	inc.WriteString(" return true; default: return false; }\n}\n")

	// flow_call_entry switch.
	inc.WriteString("static inline int32_t flow_call_entry(uint32_t node) {\n  switch (node) {\n")
	for i, node := range g.Nodes {
		if node.linked() {
			fmt.Fprintf(&inc, "    case %du: return %s();\n", i, symbols[i])
		}
	}
	inc.WriteString("    default: return -1;\n  }\n}\n")

	// descriptor.c: the g_entry table (redundant with flow_call_entry but part
	// of the proven recipe; forces symbol references at link time).
	var dc strings.Builder
	dc.WriteString("#include <stdint.h>\n")
	dc.WriteString("typedef int32_t (*entry_fn)(void);\n")
	for _, s := range dedupeOrdered(symbols) {
		fmt.Fprintf(&dc, "extern int32_t %s(void);\n", s)
	}
	dc.WriteString("entry_fn g_entry[] = {\n")
	entries := 0
	for i, node := range g.Nodes {
		if node.linked() {
			fmt.Fprintf(&dc, "  %s,\n", symbols[i])
			entries++
		}
	}
	dc.WriteString("};\n")
	fmt.Fprintf(&dc, "uint32_t g_entry_count = %d;\n", entries)

	return &generated{inc: inc.String(), descriptorC: dc.String()}, nil
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func bytesJoin(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
		out = append(out, 0) // separator so concatenation is unambiguous
	}
	return out
}

// cQuote renders s as a C string literal (handles the characters that appear in
// plugin/node/method/port ids: quotes, backslashes, control bytes).
func cQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, "\\x%02x", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func plusOneIfZero(n int) string {
	if n == 0 {
		return " + 1"
	}
	return ""
}

func dedupeOrdered(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func methodKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
