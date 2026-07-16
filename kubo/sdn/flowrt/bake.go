package flowrt

// bake.go is the NODE-SIDE BAKE path: it turns the proven flowcc compose spike
// (kubo/sdn/flowcc/flow_bake_test.go) into a real feature. Given a flow document
// and a set of module references, the node resolves each ref to its staged
// guest-link object, GENERATES a small per-flow descriptor, LINKS a composed
// runtime.wasm with its OWN wasm-ld (hosted on WasmEdge via flowcc), and hands
// the finalized artifact to the existing FlowStore.Install / NewFlowRuntime path.
//
// LINK-ONLY (Phase 0b): the flow-runtime translation unit is FLOW-AGNOSTIC and
// vendored (flowcc/runtime-src/flow_runtime.cpp + flow_descriptor_abi.h). It is
// compiled EXACTLY ONCE (content-addressed cache; PrewarmRuntime pays it at
// boot) and every bake — new flow or repeat — only compiles a tiny descriptor.cpp
// (counts + graph tables + a node-indexed entry table = g_flow_program) and
// links. This removes the ~34s per-flow recompile of the 867-line runtime that
// the old flow_generated.inc approach paid, taking a NEW-flow bake to ~3s (or
// less with an AOT box; see flowcc/aot.go).
//
// The bake reproduces flow_bake_test.go's proven recipe: the same EH-free compile
// flags (-fignore-exceptions, NO -fwasm-exceptions) and the same STANDALONE_WASM
// reactor link line, so the artifact loads and runs under the node's WasmEdge
// (0.16.4) the same way the deployed modules do.

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
//
// Phase-2 Part-B (network fetch-to-bake): when a referenced module is NOT staged
// locally and BundleHash is set, the node fetches its guest-link bundle by that
// content hash from the blockstore, verifies the Ed25519 Signature (over the
// bundle-hash digest) by SignerPubKey against the trusted signer set (signed-
// only, fail-closed), and stages it before linking. BundleHash addresses the
// whole module bundle envelope, distinct from ContentHash (the staged object).
type BakeModuleRef struct {
	PluginID     string `json:"pluginId"`
	ContentHash  string `json:"contentHash,omitempty"`
	BundleHash   string `json:"bundleHash,omitempty"`
	Signature    string `json:"signature,omitempty"`
	SignerPubKey string `json:"signerPubKey,omitempty"`
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

	// fetcher, when attached (SetNetModules), resolves a referenced module that
	// is NOT staged locally by fetching its signed bundle by content hash and
	// staging it before linking (Phase-2 Part-B fetch-to-bake). nil leaves the
	// bake path local-only.
	fetcher *NetModuleFetcher
	// catalog is the node's set of network-advertised signed modules the palette
	// lists (source "network"). Always non-nil (NewBaker seeds an empty catalog).
	catalog *NetModuleCatalog
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
	return &Baker{home: home, cc: cc, maxMemoryPages: maxMemoryPages, catalog: NewNetModuleCatalog()}, nil
}

// SetNetModules attaches the network-module fetcher (fetch-to-bake) enabling the
// node to resolve unstaged, signed modules by content hash. Passing a nil fetcher
// disables network fetch. The catalog (palette's network source) is always live.
func (b *Baker) SetNetModules(fetcher *NetModuleFetcher) { b.fetcher = fetcher }

// NetCatalog returns the node's network-module catalog (never nil).
func (b *Baker) NetCatalog() *NetModuleCatalog { return b.catalog }

// PublishNetworkModule verifies a signed module bundle and registers it in the
// catalog so the editor lists it as a network module. Requires a fetcher.
func (b *Baker) PublishNetworkModule(ctx context.Context, ref BakeModuleRef) (NetModuleEntry, error) {
	if b.fetcher == nil {
		return NetModuleEntry{}, fmt.Errorf("flowrt: network module path not enabled (no blockstore/fetcher attached)")
	}
	e, err := b.fetcher.PublishNetworkModule(ctx, ref)
	if err != nil {
		return NetModuleEntry{}, err
	}
	b.catalog.Put(e)
	return e, nil
}

// Home returns the baker's flowcc home (used by the network-module staging path).
func (b *Baker) Home() flowcc.Home { return b.home }

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
	// collect the distinct module objects to link. A referenced module that is
	// not staged locally is fetched by content hash + verified + staged first
	// (Part-B fetch-to-bake) when the ref carries a signed BundleHash.
	gen, deps, err := b.resolve(ctx, &g, req.ModuleRefs)
	if err != nil {
		return nil, err
	}

	// The flow-runtime sources are FLOW-AGNOSTIC and vendored (link-only bake):
	// flow_runtime.cpp + flow_descriptor_abi.h are compiled ONCE regardless of
	// the flow. Only space_data_module_invoke.h (the SDK invoke ABI) is still
	// read from node data — it is flow-independent too, but not vendored here.
	runtimeCpp := flowcc.VendoredFlowRuntimeCpp()
	abiHdr := flowcc.VendoredFlowDescriptorAbi()
	invokeHdr, err := os.ReadFile(b.home.InvokeHeaderPath())
	if err != nil {
		return nil, fmt.Errorf("bake: read space_data_module_invoke.h template: %w", err)
	}

	// Stage 1: compile the FLOW-AGNOSTIC flow_runtime.o. Cached by the sha of
	// (runtime source + abi header + invoke header + flags) — which no longer
	// includes any per-flow data, so the SAME cached object serves EVERY flow.
	// The 867-line runtime is compiled once (~34s cold) and every subsequent new
	// flow is link-only.
	flowRuntimeO, cacheHit, err := b.compileFlowRuntime(ctx, runtimeCpp, abiHdr, invokeHdr)
	if err != nil {
		return nil, err
	}

	// Stage 2: compile the generated per-flow descriptor.cpp (tiny; not cached).
	// It DEFINES g_flow_program (counts + tables + entry table) that the
	// flow-agnostic runtime binds at link time.
	descriptorO, err := b.compileDescriptor(ctx, gen.descriptorCpp, abiHdr)
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
// distinct module objects (verifying content hashes when refs supply them). A
// referenced module not staged locally is fetched by content hash + signature-
// verified + staged first when its ref carries a signed BundleHash (Part-B).
func (b *Baker) resolve(ctx context.Context, g *bakeGraph, refs []BakeModuleRef) (*generated, []depObject, error) {
	// Content-hash pins + full refs keyed by pluginId (optional).
	pin := map[string]string{}
	refByPlugin := map[string]BakeModuleRef{}
	for _, r := range refs {
		if r.PluginID == "" {
			continue
		}
		refByPlugin[r.PluginID] = r
		if r.ContentHash != "" {
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
		// Fetch-to-bake: if the module is not staged locally, fetch + verify +
		// stage it by its signed content hash before resolving its symbol.
		if err := b.ensureStaged(ctx, n.PluginID, refByPlugin); err != nil {
			return nil, nil, err
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

// ensureStaged makes sure pluginID's guest-link object is staged locally,
// fetching + verifying + staging it by content hash when it is not. The fetch
// ref is taken from the explicit moduleRefs, or (when the bake omitted the
// signed fields) from the node's network-module catalog entry — either way the
// signed-only gate runs before the module is staged. A module already staged, or
// one with no network ref available, is left to the normal staged-metadata read
// (which errors clearly if the module truly is not available).
func (b *Baker) ensureStaged(ctx context.Context, pluginID string, refByPlugin map[string]BakeModuleRef) error {
	if _, err := b.home.LoadModuleMetadata(pluginID); err == nil {
		return nil // already staged
	}
	if b.fetcher == nil {
		return nil // no network fetch path; the staged-metadata read reports the miss
	}
	ref, ok := refByPlugin[pluginID]
	if !ok || ref.BundleHash == "" {
		// The editor may reference a catalog module without echoing its signed
		// fields; recover them from the verified catalog entry.
		if e, found := b.catalog.Get(pluginID); found {
			ref = e.ref()
		}
	}
	if ref.BundleHash == "" {
		return nil // nothing to fetch by; leave the miss to the metadata read
	}
	if err := b.fetcher.FetchAndStage(ctx, b.home, ref); err != nil {
		return fmt.Errorf("bake: fetch-to-stage module %q: %w", pluginID, err)
	}
	return nil
}

// compileFlowRuntime compiles the FLOW-AGNOSTIC flow_runtime.cpp into
// flow_runtime.o, serving from the content-addressed cache on a hit. The cache
// key folds ONLY flow-independent inputs (runtime source + abi header + invoke
// header + flags), so the first bake compiles the runtime once and every later
// flow — new or repeated — hits the cache. Call PrewarmRuntime at boot to pay
// the one-time compile off the interactive path.
func (b *Baker) compileFlowRuntime(ctx context.Context, runtimeCpp, abiHdr, invokeHdr []byte) ([]byte, bool, error) {
	key := sha256Hex(bytesJoin(runtimeCpp, abiHdr, invokeHdr, []byte(flowRuntimeCompileFlagsKey)))
	cachePath := filepath.Join(b.home.FlowRuntimeCacheDir(), key+".o")
	if cached, err := os.ReadFile(cachePath); err == nil && len(cached) > 0 {
		return cached, true, nil
	}

	compile := append([]string{"clang", "clang", "-c", "/work/flow_runtime.cpp", "-I/work",
		"-o", "/work/flow_runtime.o"}, flowRuntimeCompileFlags...)
	res, err := b.cc.Run(ctx, compile, map[string][]byte{
		"/work/flow_runtime.cpp":           runtimeCpp,
		"/work/flow_descriptor_abi.h":      abiHdr,
		"/work/space_data_module_invoke.h": invokeHdr,
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

// PrewarmRuntime compiles + caches the flow-agnostic flow_runtime.o so the
// one-time ~34s runtime compile is paid at boot rather than on the first
// interactive bake. It is safe to call repeatedly (a cache hit is a no-op) and
// concurrently. Returns whether the object was already cached.
func (b *Baker) PrewarmRuntime(ctx context.Context) (cached bool, err error) {
	invokeHdr, err := os.ReadFile(b.home.InvokeHeaderPath())
	if err != nil {
		return false, fmt.Errorf("bake: read space_data_module_invoke.h template: %w", err)
	}
	_, cached, err = b.compileFlowRuntime(ctx, flowcc.VendoredFlowRuntimeCpp(), flowcc.VendoredFlowDescriptorAbi(), invokeHdr)
	return cached, err
}

// compileDescriptor compiles the generated per-flow descriptor.cpp into
// descriptor.o. It is compiled as C++ (matching the runtime flags) because the
// descriptor fills the dispatch-descriptor string pointers in a static
// constructor (reinterpret_cast is not a constant expression). The compile is
// tiny (~0.1s) since the descriptor is only counts + tables + an entry table.
func (b *Baker) compileDescriptor(ctx context.Context, descriptorCpp string, abiHdr []byte) ([]byte, error) {
	compile := append([]string{"clang", "clang", "-c", "/work/descriptor.cpp", "-I/work",
		"-o", "/work/descriptor.o"}, flowRuntimeCompileFlags...)
	res, err := b.cc.Run(ctx, compile, map[string][]byte{
		"/work/descriptor.cpp":        []byte(descriptorCpp),
		"/work/flow_descriptor_abi.h": abiHdr,
	})
	if err != nil || res.ExitCode != 0 {
		return nil, fmt.Errorf("bake: compile descriptor.cpp: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
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
// Descriptor generator — emits the per-flow descriptor.cpp (g_flow_program) the
// flow-agnostic runtime links against. This is the ONLY per-flow codegen; the
// runtime itself is vendored + compiled once (see flowcc/runtime-src/).
// ---------------------------------------------------------------------------

type generated struct {
	descriptorCpp string
}

// generateDescriptorSources emits the per-flow descriptor.cpp: a small
// translation unit that DEFINES `g_flow_program` (counts + graph tables +
// dispatch descriptors + a node-indexed entry table) which the flow-agnostic
// flow_runtime.cpp binds at LINK time. Everything except the dispatch-descriptor
// string pointers is constant-initialised (so g_flow_program lands in the data
// segment before any constructor runs); the string pointers are filled in a
// static constructor because reinterpret_cast is not a constant expression.
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
		triggerCount = 1 // g_ingress_states must not be zero-length (matches old FLOW_TRIGGER_COUNT)
	}
	const requiredPortCount = 0 // Phase 0: readiness gates on queued frames only.

	var b strings.Builder
	fmt.Fprintf(&b, "// GENERATED by sdn/flowrt bake.go for flow %q — do not edit.\n", g.ProgramID)
	b.WriteString("#include \"flow_descriptor_abi.h\"\n\n")

	// extern "C" entry declarations (deduped, in first-seen node order).
	for _, s := range dedupeOrdered(symbols) {
		fmt.Fprintf(&b, "extern \"C\" int32_t %s(void);\n", s)
	}
	b.WriteString("\n")

	// Per-node string constants.
	for i, node := range g.Nodes {
		fmt.Fprintf(&b, "static const char kStr_node%d_id[] = %s;\n", i, cQuote(node.NodeID))
		fmt.Fprintf(&b, "static const char kStr_node%d_plugin[] = %s;\n", i, cQuote(node.PluginID))
		fmt.Fprintf(&b, "static const char kStr_node%d_method[] = %s;\n", i, cQuote(node.MethodID))
		fmt.Fprintf(&b, "static const char kStr_node%d_model[] = %s;\n", i, cQuote(node.model()))
	}
	b.WriteString("static const char kStr_sym_malloc[] = \"malloc\";\n")
	b.WriteString("static const char kStr_sym_free[] = \"free\";\n\n")

	// Edges (constant-init; a dummy row keeps the array non-zero-length).
	fmt.Fprintf(&b, "static FlowEdge g_edges[%du%s] = {\n", len(g.Edges), plusOneIfZero(len(g.Edges)))
	for _, e := range g.Edges {
		from, ok1 := nodeIndex[e.FromNodeID]
		to, ok2 := nodeIndex[e.ToNodeID]
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("bake: edge references unknown node (%q -> %q)", e.FromNodeID, e.ToNodeID)
		}
		fmt.Fprintf(&b, "  { %du, %s, %du, %s },\n", from, cQuote(e.FromPortID), to, cQuote(e.ToPortID))
	}
	if len(g.Edges) == 0 {
		b.WriteString("  { 0u, \"\", 0u, \"\" },\n")
	}
	b.WriteString("};\n")

	// Trigger bindings.
	fmt.Fprintf(&b, "static FlowTriggerBinding g_trigger_bindings[%du%s] = {\n", len(g.TriggerBindings), plusOneIfZero(len(g.TriggerBindings)))
	for _, tb := range g.TriggerBindings {
		ti := trigIndex[tb.TriggerID] // 0 if unnamed
		tn, ok := nodeIndex[tb.TargetNodeID]
		if !ok {
			return nil, fmt.Errorf("bake: trigger binding references unknown node %q", tb.TargetNodeID)
		}
		fmt.Fprintf(&b, "  { %du, %du, %s },\n", ti, tn, cQuote(tb.TargetPortID))
	}
	if len(g.TriggerBindings) == 0 {
		b.WriteString("  { 0u, 0u, \"\" },\n")
	}
	b.WriteString("};\n")

	// Required ports (Phase 0: none; readiness gates on queued frames only).
	b.WriteString("static FlowRequiredPort g_required_ports[1] = {\n  { 0u, \"\" },\n};\n\n")

	// Dispatch + dependency descriptor storage (zero-initialised; the string
	// pointers are filled by the static constructor below).
	fmt.Fprintf(&b, "static FlowNodeDispatchDescriptorC g_dispatch_descriptors[%du];\n", n)
	fmt.Fprintf(&b, "static SignedArtifactDependencyDescriptorC g_dependency_descriptors[%du];\n\n", depCount)

	// Node-indexed entry table (call_indirect target for linked nodes; nullptr
	// for host-model nodes) + linked bitmap. This REPLACES the old flow_call_entry
	// switch — the runtime dispatches g_flow_program.entry[node]().
	b.WriteString("static flow_entry_fn g_entry[] = {\n")
	for i, node := range g.Nodes {
		if node.linked() {
			fmt.Fprintf(&b, "  %s,\n", symbols[i])
		} else {
			b.WriteString("  nullptr,\n")
		}
	}
	b.WriteString("};\n")
	b.WriteString("static const uint8_t g_node_linked[] = {\n")
	for _, node := range g.Nodes {
		if node.linked() {
			b.WriteString("  1u,\n")
		} else {
			b.WriteString("  0u,\n")
		}
	}
	b.WriteString("};\n\n")

	// String-pointer fill (reinterpret_cast is not a constant expression, so it
	// runs at _initialize via a static constructor — same proven pattern the old
	// flow_generated.inc used).
	b.WriteString("static void flow_init_descriptors() {\n")
	for i := range g.Nodes {
		b.WriteString("  {\n")
		fmt.Fprintf(&b, "    FlowNodeDispatchDescriptorC &d = g_dispatch_descriptors[%d];\n", i)
		fmt.Fprintf(&b, "    d.node_id_ptr = reinterpret_cast<uint32_t>(kStr_node%d_id);\n", i)
		fmt.Fprintf(&b, "    d.node_index = %du;\n", i)
		fmt.Fprintf(&b, "    d.plugin_id_ptr = reinterpret_cast<uint32_t>(kStr_node%d_plugin);\n", i)
		fmt.Fprintf(&b, "    d.method_id_ptr = reinterpret_cast<uint32_t>(kStr_node%d_method);\n", i)
		fmt.Fprintf(&b, "    d.dispatch_model_ptr = reinterpret_cast<uint32_t>(kStr_node%d_model);\n", i)
		b.WriteString("    d.malloc_symbol_ptr = reinterpret_cast<uint32_t>(kStr_sym_malloc);\n")
		b.WriteString("    d.free_symbol_ptr = reinterpret_cast<uint32_t>(kStr_sym_free);\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	b.WriteString("static struct FlowDescriptorInit { FlowDescriptorInit() { flow_init_descriptors(); } } g_flow_descriptor_init;\n\n")

	// The ONE symbol the flow-agnostic runtime binds. Constant-initialised: all
	// fields are integer counts or the addresses of the static tables above.
	fmt.Fprintf(&b, "extern \"C\" FlowProgramC g_flow_program = {\n")
	fmt.Fprintf(&b, "  /*node_count*/ %du,\n", n)
	fmt.Fprintf(&b, "  /*edge_count*/ %du,\n", len(g.Edges))
	fmt.Fprintf(&b, "  /*trigger_count*/ %du,\n", triggerCount)
	fmt.Fprintf(&b, "  /*dep_count*/ %du,\n", depCount)
	fmt.Fprintf(&b, "  /*trigger_binding_count*/ %du,\n", len(g.TriggerBindings))
	fmt.Fprintf(&b, "  /*required_port_count*/ %du,\n", requiredPortCount)
	b.WriteString("  g_edges,\n")
	b.WriteString("  g_trigger_bindings,\n")
	b.WriteString("  g_required_ports,\n")
	b.WriteString("  g_dispatch_descriptors,\n")
	b.WriteString("  g_dependency_descriptors,\n")
	b.WriteString("  g_entry,\n")
	b.WriteString("  g_node_linked,\n")
	b.WriteString("};\n")

	return &generated{descriptorCpp: b.String()}, nil
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
