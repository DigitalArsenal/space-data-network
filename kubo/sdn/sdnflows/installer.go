// Package sdnflows is the SDN node FLOW install + register pipeline: it turns a
// compiled flow bundle (chaining WASM module nodes) into a running,
// cron-scheduled flow on a kubo-based SDN node, and persists which flows are
// installed so the set re-registers on the next boot.
//
// It is the flow-side sibling of sdnmodules: where sdnmodules installs a single
// WASM module and registers it as a *modulert.Module with the cron scheduler,
// this installs a timer-served flow bundle (flowrt.ServiceFlow) and registers
// THAT with the same scheduler. Because a ServiceFlow satisfies
// sdncron.CronModule (ID/CronMethods/InvokeCron), a registered flow both fires
// its host-cron timer on its effective interval AND appears at
// GET /sdn/v1/modules alongside modules — flows are runnable units on the node.
//
// Given a flow bundle reference (a directory holding runtime.wasm + flow.plg,
// the deps having been linked into runtime.wasm at compile time) the installer:
//
//  1. loads the bundle through flowrt.LoadFlowService — WASI + flow host funcs +
//     the module-SDK hostcall bridge, capabilities provisioned from the node's
//     services registry FAIL CLOSED (an operator-unapproved sensitive capability,
//     keyed by the bundle's content hash, refuses the whole install);
//  2. registers the resulting ServiceFlow with the cron Scheduler under its
//     timer triggers (interval overridable by home-dir config), so the scheduler
//     fires the flow's real InvokeCron (fetch -> parse -> store) on its cadence;
//  3. records the install in a persisted installed-flows registry so a later
//     boot re-loads the bundle and re-registers it.
//
// # Home-directory layout
//
// The registry lives under the node's SDN flow root, <repo>/sdn/flows:
//
//	installed-flows.json   one entry per installed flow: id, ref (bundle dir),
//	                       name/version, intervals, config, enabled, source.
package sdnflows

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// Logger is a minimal printf-style sink (nil is silent).
type Logger func(format string, args ...interface{})

// ErrInstallDenied is returned when a flow bundle requests a sensitive
// capability no operator approval covers (fail closed).
var ErrInstallDenied = errors.New("sdnflows: flow install denied by capability policy")

// FlowSpec describes one flow to install: the bundle reference plus optional
// per-trigger interval overrides (triggerId -> Go duration string) and node
// CONFIG (served to the flow's nodes via plugin.getConfig — URL overrides etc.).
type FlowSpec struct {
	Ref       string                 `json:"ref"`
	Intervals map[string]string      `json:"intervals,omitempty"`
	Config    map[string]interface{} `json:"config,omitempty"`
}

// InstalledFlow is the read model for one installed + registered flow.
type InstalledFlow struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Version string   `json:"version,omitempty"`
	Ref     string   `json:"ref"`
	Enabled bool     `json:"enabled"`
	Source  string   `json:"source,omitempty"`
	Timers  []string `json:"timers"`
}

// InstalledIsomorphicNode is one exact, independently signed child artifact
// instantiated for a signed parent flow bundle.
type InstalledIsomorphicNode struct {
	EntryID     string `json:"entry_id"`
	EntryHash   string `json:"entry_hash"`
	ContentHash string `json:"content_hash"`
}

// InstalledIsomorphicFlow is the application-blind read model for one signed
// bundle and its independently instantiated children.
type InstalledIsomorphicFlow struct {
	ID      string                    `json:"id"`
	Ref     string                    `json:"ref"`
	Enabled bool                      `json:"enabled"`
	Source  string                    `json:"source,omitempty"`
	Nodes   []InstalledIsomorphicNode `json:"nodes"`
}

// Config wires an Installer to a live node.
type Config struct {
	// Services is the live SDN services bundle: its CapRegistry provisions the
	// flow's capabilities (fail closed), NodeCtx carries the operator policy,
	// and Scheduler fires the registered flow's timers. Required.
	Services *sdnservices.Services
	// Registry persists the installed-flows set. May be no-persistence (empty
	// dir). Required (non-nil); use NewRegistry("") for no-persistence.
	Registry *Registry
	// MaxMemoryPages caps each flow instance's linear memory (0 => 1024).
	MaxMemoryPages uint32
	// Log is an optional printf sink.
	Log Logger
	// TrustedSigners is the fail-closed publisher trust set used for both the
	// whole signed bundle and every independently signed WASM member.
	TrustedSigners []ed25519.PublicKey
	// DropinDir is an optional directory of signed isomorphic bundles scanned
	// at boot. Files are processed in lexical order.
	DropinDir string
}

type isomorphicNodeInstance interface {
	ID() string
	ContentHash() string
	BoundIdentity() (string, string)
	Manifest() *modulert.Manifest
	InvokeMethodFrameSet(context.Context, string, []modulert.InvokeInputFrame) (*modulert.InvokeFrameSetResult, error)
	InvokeScheduledMethodFrameSet(context.Context, string, []modulert.InvokeInputFrame) (*modulert.InvokeFrameSetResult, error)
	Close() error
}

type isomorphicParentRuntime interface {
	ValidateNodes([]flowrt.IsomorphicNodeArtifact) error
	PrepareActivation() error
	DrainPrepared(context.Context, flowrt.HandlerMap) error
	Activate(context.Context, flowrt.HandlerMap) error
	Release()
}

type isomorphicDispatchDescriptor struct {
	DependencyID  string
	DispatchModel string
}

type isomorphicDependencyDescriptor struct {
	DependencyID string
	SHA256       string
}

// runtimeNodeRoute is an application-blind, signed declaration connecting one
// child invocation output to one externally readable opaque key. The host does
// not inspect Payload or infer route names from node/application semantics.
type runtimeNodeRoute struct {
	Key       string `json:"key"`
	NodeID    string `json:"nodeId"`
	PortID    string `json:"portId"`
	MediaType string `json:"mediaType"`
}

type runtimeNodeOutput struct {
	NodeID string
	PortID string
}

type opaqueRuntimeNodeValue struct {
	Payload   []byte
	MediaType string
}

// opaqueRuntimeNodes is a per-installed-bundle latest-value view. Only output
// pairs declared by the signed artifact metadata are admitted. Its own mutex
// avoids holding Installer.mu while a child WASM invocation is in progress.
type opaqueRuntimeNodes struct {
	mu     sync.RWMutex
	routes map[runtimeNodeOutput]runtimeNodeRoute
	values map[string]opaqueRuntimeNodeValue
}

func newOpaqueRuntimeNodes(routes []runtimeNodeRoute) *opaqueRuntimeNodes {
	view := &opaqueRuntimeNodes{
		routes: make(map[runtimeNodeOutput]runtimeNodeRoute, len(routes)),
		values: make(map[string]opaqueRuntimeNodeValue, len(routes)),
	}
	for _, route := range routes {
		view.routes[runtimeNodeOutput{NodeID: route.NodeID, PortID: route.PortID}] = route
	}
	return view
}

func (view *opaqueRuntimeNodes) publish(nodeID string, frames []modulert.InvokeOutputFrame) {
	if view == nil {
		return
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	for _, frame := range frames {
		route, declared := view.routes[runtimeNodeOutput{NodeID: nodeID, PortID: frame.PortID}]
		if !declared {
			continue
		}
		view.values[route.Key] = opaqueRuntimeNodeValue{
			Payload:   append([]byte(nil), frame.Payload...),
			MediaType: route.MediaType,
		}
	}
}

func (view *opaqueRuntimeNodes) read(key string) ([]byte, string, bool) {
	if view == nil {
		return nil, "", false
	}
	view.mu.RLock()
	value, ok := view.values[key]
	view.mu.RUnlock()
	if !ok {
		return nil, "", false
	}
	return append([]byte(nil), value.Payload...), value.MediaType, true
}

type liveIsomorphicFlow struct {
	view             InstalledIsomorphicFlow
	metadata         *flowrt.IsomorphicBundleMetadata
	parent           isomorphicParentRuntime
	nodes            map[string]isomorphicNodeInstance
	handlers         flowrt.HandlerMap
	runtimeNodes     *opaqueRuntimeNodes
	unregister       []func()
	activationCtx    context.Context
	activationCancel context.CancelFunc
}

// Installer installs compiled flow bundles onto a live SDN node and registers
// them with the cron scheduler. See the package doc for the flow.
type Installer struct {
	svc           *sdnservices.Services
	reg           *Registry
	max           uint32
	log           Logger
	dropinDir     string
	trustedSigner []ed25519.PublicKey

	mu           sync.Mutex
	installMu    sync.Mutex
	loaded       map[string]*flowrt.ServiceFlow // id -> live legacy flow handle
	isomorphic   map[string]*liveIsomorphicFlow
	bootFailures []BootFailure

	verifyIsomorphicBundle   func([]byte) (*flowrt.IsomorphicBundleMetadata, error)
	loadIsomorphicNode       func([]byte, string, string) (isomorphicNodeInstance, error)
	loadIsomorphicParent     func([]byte, uint32) (isomorphicParentRuntime, error)
	storeApplicationArtifact func(context.Context, []byte) error
}

// New builds an Installer. Services and Registry are required.
func New(cfg Config) (*Installer, error) {
	if cfg.Services == nil {
		return nil, errors.New("sdnflows: Config.Services is required")
	}
	if cfg.Services.Scheduler == nil {
		return nil, errors.New("sdnflows: Services.Scheduler is required")
	}
	if cfg.Services.CapReg == nil {
		return nil, errors.New("sdnflows: Services.CapReg is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("sdnflows: Config.Registry is required (use NewRegistry(\"\") for no-persistence)")
	}
	in := &Installer{
		svc:           cfg.Services,
		reg:           cfg.Registry,
		max:           cfg.MaxMemoryPages,
		log:           cfg.Log,
		loaded:        make(map[string]*flowrt.ServiceFlow),
		dropinDir:     strings.TrimSpace(cfg.DropinDir),
		trustedSigner: clonePublicKeys(cfg.TrustedSigners),
		isomorphic:    make(map[string]*liveIsomorphicFlow),
	}
	in.verifyIsomorphicBundle = func(signed []byte) (*flowrt.IsomorphicBundleMetadata, error) {
		return flowrt.LoadIsomorphicBundleMetadata(signed, in.trustedSigner)
	}
	in.loadIsomorphicNode = func(signed []byte, outerArtifactHash, entryID string) (isomorphicNodeInstance, error) {
		return in.svc.LoadModuleInstanceWithMaxMemoryPages(signed, in.max, outerArtifactHash, entryID)
	}
	in.loadIsomorphicParent = func(portable []byte, pages uint32) (isomorphicParentRuntime, error) {
		runtime, err := flowrt.NewFlowRuntime(portable, pages)
		if err != nil {
			return nil, err
		}
		return &wasmIsomorphicParent{runtime: runtime}, nil
	}
	in.storeApplicationArtifact = func(ctx context.Context, app []byte) error {
		if in.svc.Store == nil {
			return errors.New("sdnflows: application store is unavailable")
		}
		_, err := in.svc.Store.StoreManifest(ctx, "sdn", "APP", app)
		return err
	}
	return in, nil
}

func (in *Installer) logf(format string, args ...interface{}) {
	if in.log != nil {
		in.log(format, args...)
	}
}

func (in *Installer) deps() flowrt.FlowServiceDeps {
	return flowrt.FlowServiceDeps{
		CapRegistry:    in.svc.CapReg,
		NodeCtx:        in.svc.NodeCtx,
		MaxMemoryPages: in.max,
	}
}

const isomorphicSourcePrefix = "isomorphic:"

func clonePublicKeys(keys []ed25519.PublicKey) []ed25519.PublicKey {
	out := make([]ed25519.PublicKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, append(ed25519.PublicKey(nil), key...))
	}
	return out
}

// InstallSignedBundle verifies a whole SDK bundle, instantiates its signed
// parent runtime and every signed application/wasm member independently, binds
// only verified EntryIDs as dependency handlers, and publishes the install
// atomically after validation and startup succeed.
func (in *Installer) InstallSignedBundle(ctx context.Context, ref, source string) (InstalledIsomorphicFlow, error) {
	return in.installSignedBundle(ctx, ref, source, true)
}

func (in *Installer) installSignedBundle(ctx context.Context, ref, source string, persist bool, expectedContentHash ...string) (InstalledIsomorphicFlow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return InstalledIsomorphicFlow{}, errors.New("sdnflows: signed bundle ref is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Serializing installs makes the publish point atomic without holding the
	// read-model mutex across signature verification or WASM instantiation.
	in.installMu.Lock()
	defer in.installMu.Unlock()

	signed, err := os.ReadFile(ref)
	if err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("sdnflows: read signed bundle %q: %w", ref, err)
	}
	metadata, err := in.verifyIsomorphicBundle(signed)
	if err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("sdnflows: verify signed bundle %q: %w", ref, err)
	}
	if len(expectedContentHash) > 0 {
		expected := strings.TrimSpace(expectedContentHash[0])
		if expected == "" || !strings.EqualFold(metadata.ContentHash, expected) {
			return InstalledIsomorphicFlow{}, fmt.Errorf("sdnflows: signed bundle %q content hash %q does not match persisted id %q", ref, metadata.ContentHash, expected)
		}
	}
	appBytes, runtimeRoutes, err := validateRequiredBundleMembers(metadata)
	if err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("sdnflows: signed bundle %q: %w", ref, err)
	}

	in.mu.Lock()
	if existing := in.isomorphic[metadata.ContentHash]; existing != nil {
		view := existing.view
		in.mu.Unlock()
		return view, nil
	}
	in.mu.Unlock()

	parent, err := in.loadIsomorphicParent(metadata.PortableArtifact, in.max)
	if err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("sdnflows: instantiate signed parent runtime: %w", err)
	}
	// Startup remains owned by the install request below. Once committed,
	// wakeups run for the installer-owned lifetime and are canceled by teardown.
	activationCtx, activationCancel := context.WithCancel(context.Background())
	live := &liveIsomorphicFlow{
		metadata:         metadata,
		parent:           parent,
		nodes:            make(map[string]isomorphicNodeInstance, len(metadata.Nodes)),
		handlers:         make(flowrt.HandlerMap, len(metadata.Nodes)),
		runtimeNodes:     newOpaqueRuntimeNodes(runtimeRoutes),
		activationCtx:    activationCtx,
		activationCancel: activationCancel,
	}
	committed := false
	defer func() {
		if !committed {
			closeLiveIsomorphicFlow(live)
		}
	}()

	for _, artifact := range metadata.Nodes {
		if _, duplicate := live.nodes[artifact.EntryID]; duplicate {
			return InstalledIsomorphicFlow{}, fmt.Errorf("duplicate signed child EntryID %q", artifact.EntryID)
		}
		node, loadErr := in.loadIsomorphicNode(artifact.SignedArtifact, metadata.ContentHash, artifact.EntryID)
		if loadErr != nil {
			return InstalledIsomorphicFlow{}, fmt.Errorf("instantiate signed child %q: %w", artifact.EntryID, loadErr)
		}
		if !strings.EqualFold(strings.TrimSpace(node.ContentHash()), artifact.ContentHash) {
			_ = node.Close()
			return InstalledIsomorphicFlow{}, fmt.Errorf(
				"signed child %q instantiated content hash %q, want %q",
				artifact.EntryID, node.ContentHash(), artifact.ContentHash,
			)
		}
		live.nodes[artifact.EntryID] = node
		live.handlers[artifact.EntryID] = isomorphicNodeHandler(node, live.runtimeNodes)
	}
	if err := parent.ValidateNodes(metadata.Nodes); err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("validate signed parent dependency descriptors: %w", err)
	}
	// Enqueue and drain the signed startup trigger synchronously. Installation
	// is atomic only after every handler in the initial graph has succeeded;
	// failed provider, analysis, persistence, or publication work must never
	// leave behind a false-success APP or registry record.
	if err := parent.PrepareActivation(); err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("prepare signed parent startup activation: %w", err)
	}
	if err := in.registerWakeupHandlers(live); err != nil {
		return InstalledIsomorphicFlow{}, err
	}
	if err := parent.DrainPrepared(ctx, live.handlers); err != nil {
		return InstalledIsomorphicFlow{}, fmt.Errorf("drain signed parent startup activation: %w", err)
	}

	view := InstalledIsomorphicFlow{
		ID:      metadata.ContentHash,
		Ref:     ref,
		Enabled: true,
		Source:  source,
		Nodes:   make([]InstalledIsomorphicNode, 0, len(metadata.Nodes)),
	}
	for _, node := range metadata.Nodes {
		view.Nodes = append(view.Nodes, InstalledIsomorphicNode{
			EntryID: node.EntryID, EntryHash: node.EntryHash, ContentHash: node.ContentHash,
		})
	}
	sort.Slice(view.Nodes, func(i, j int) bool { return view.Nodes[i].EntryID < view.Nodes[j].EntryID })
	live.view = view

	if persist {
		if err := in.reg.Put(InstalledEntry{
			ID: metadata.ContentHash, Ref: ref, Enabled: true,
			Source: isomorphicSourcePrefix + source,
		}); err != nil {
			return InstalledIsomorphicFlow{}, fmt.Errorf("persist signed bundle registry entry: %w", err)
		}
	}
	if err := in.storeApplicationArtifact(ctx, appBytes); err != nil {
		if persist {
			_ = in.reg.Remove(metadata.ContentHash)
		}
		return InstalledIsomorphicFlow{}, fmt.Errorf("persist signed bundle APP member: %w", err)
	}

	in.mu.Lock()
	in.isomorphic[metadata.ContentHash] = live
	in.mu.Unlock()
	committed = true
	in.logf("sdnflows: installed signed isomorphic bundle %s (%d independently signed nodes) [source=%s]", metadata.ContentHash, len(metadata.Nodes), source)
	return view, nil
}

func (in *Installer) activateIsomorphicFlow(live *liveIsomorphicFlow, source string) {
	if live == nil || live.parent == nil {
		return
	}
	err := live.parent.Activate(live.activationCtx, live.handlers)
	if err != nil && live.activationCtx.Err() == nil {
		in.logf("sdnflows: signed bundle %s %s activation failed: %v", live.metadata.ContentHash, source, err)
	}
}

func validateRequiredBundleMembers(metadata *flowrt.IsomorphicBundleMetadata) ([]byte, []runtimeNodeRoute, error) {
	if metadata == nil || metadata.Bundle == nil || !metadata.Signature.Verified {
		return nil, nil, errors.New("whole-bundle signature is not verified")
	}
	if len(metadata.ContentHash) != 64 || len(metadata.PortableArtifact) == 0 {
		return nil, nil, errors.New("signed parent runtime identity or bytes are missing")
	}
	if len(metadata.Nodes) == 0 {
		return nil, nil, errors.New("bundle has no independently signed WASM nodes")
	}

	var flowPLG, artifactMetadata, app []byte
	for _, entry := range metadata.Bundle.Entries {
		switch entry.SectionName {
		case "sdn.flow.plg":
			if flowPLG != nil {
				return nil, nil, errors.New("bundle has duplicate canonical flow.plg members")
			}
			flowPLG = entry.Payload
		case "sdn.flow.artifact":
			if artifactMetadata != nil {
				return nil, nil, errors.New("bundle has duplicate artifact metadata members")
			}
			artifactMetadata = entry.Payload
		case "sdn.app.record":
			if app != nil {
				return nil, nil, errors.New("bundle has duplicate APP members")
			}
			app = entry.Payload
		}
	}
	if len(flowPLG) < 8 || string(flowPLG[4:8]) != "$PLG" {
		return nil, nil, errors.New("bundle is missing a canonical flow.plg member")
	}
	if len(artifactMetadata) == 0 || !json.Valid(artifactMetadata) {
		return nil, nil, errors.New("bundle is missing valid opaque artifact metadata JSON")
	}
	if len(app) < 12 || string(app[8:12]) != "$APP" || binary.LittleEndian.Uint32(app[:4]) != uint32(len(app)-4) {
		return nil, nil, errors.New("bundle is missing a canonical size-prefixed APP member")
	}
	routes, err := decodeRuntimeNodeRoutes(artifactMetadata)
	if err != nil {
		return nil, nil, err
	}
	return append([]byte(nil), app...), routes, nil
}

func decodeRuntimeNodeRoutes(artifactMetadata []byte) ([]runtimeNodeRoute, error) {
	var envelope struct {
		RuntimeNodeRoutes []runtimeNodeRoute `json:"runtimeNodeRoutes"`
	}
	if err := json.Unmarshal(artifactMetadata, &envelope); err != nil {
		return nil, fmt.Errorf("decode opaque runtime-node routes: %w", err)
	}
	if len(envelope.RuntimeNodeRoutes) > 1024 {
		return nil, fmt.Errorf("opaque runtime-node route count %d exceeds 1024", len(envelope.RuntimeNodeRoutes))
	}
	keys := make(map[string]struct{}, len(envelope.RuntimeNodeRoutes))
	sources := make(map[runtimeNodeOutput]struct{}, len(envelope.RuntimeNodeRoutes))
	for index := range envelope.RuntimeNodeRoutes {
		route := &envelope.RuntimeNodeRoutes[index]
		if route.Key != strings.TrimSpace(route.Key) ||
			route.NodeID != strings.TrimSpace(route.NodeID) ||
			route.PortID != strings.TrimSpace(route.PortID) ||
			route.MediaType != strings.TrimSpace(route.MediaType) {
			return nil, fmt.Errorf("opaque runtime-node route %d fields must be exact and whitespace-free", index)
		}
		if !validOpaqueRuntimeNodeKey(route.Key) {
			return nil, fmt.Errorf("opaque runtime-node route %d has invalid key %q", index, route.Key)
		}
		if route.NodeID == "" || route.PortID == "" {
			return nil, fmt.Errorf("opaque runtime-node route %q requires nodeId and portId", route.Key)
		}
		if _, _, err := mime.ParseMediaType(route.MediaType); err != nil {
			return nil, fmt.Errorf("opaque runtime-node route %q has invalid mediaType: %w", route.Key, err)
		}
		if _, duplicate := keys[route.Key]; duplicate {
			return nil, fmt.Errorf("duplicate opaque runtime-node route key %q", route.Key)
		}
		keys[route.Key] = struct{}{}
		source := runtimeNodeOutput{NodeID: route.NodeID, PortID: route.PortID}
		if _, duplicate := sources[source]; duplicate {
			return nil, fmt.Errorf("duplicate opaque runtime-node route source %q/%q", route.NodeID, route.PortID)
		}
		sources[source] = struct{}{}
	}
	return envelope.RuntimeNodeRoutes, nil
}

func validOpaqueRuntimeNodeKey(key string) bool {
	if key == "" || len(key) > 255 {
		return false
	}
	for _, char := range key {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return key != "." && key != ".."
}

func isomorphicNodeHandler(node isomorphicNodeInstance, runtimeNodes *opaqueRuntimeNodes) flowrt.Handler {
	return func(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
		if args == nil {
			return nil, errors.New("isomorphic child invocation args are required")
		}
		inputs := make([]modulert.InvokeInputFrame, 0, len(args.Frames))
		for _, frame := range args.Frames {
			if frame.WireFormat != 0 {
				return nil, fmt.Errorf("frame %q requires canonical fallback across independently instantiated WASM memories", frame.PortID)
			}
			if frame.FixedStringLength > uint32(^uint16(0)) || frame.RequiredAlignment > uint32(^uint16(0)) {
				return nil, fmt.Errorf("frame %q layout exceeds PIV uint16 bounds", frame.PortID)
			}
			inputs = append(inputs, modulert.InvokeInputFrame{
				PortID:            frame.PortID,
				Payload:           append([]byte(nil), frame.Bytes...),
				SchemaName:        frame.SchemaName,
				FileIdentifier:    frame.FileIdentifier,
				SchemaVersion:     frame.SchemaVersion,
				SchemaHash:        append([]byte(nil), frame.SchemaHash...),
				RootTypeName:      frame.RootTypeName,
				WireFormat:        frame.WireFormat,
				FixedStringLength: uint16(frame.FixedStringLength),
				ByteLength:        frame.ByteLength,
				RequiredAlignment: uint16(frame.RequiredAlignment),
				Alignment:         frame.Alignment,
				Ownership:         frame.Ownership,
				Mutability:        frame.Mutability,
				FrameID:           frame.FrameID,
			})
		}
		result, err := node.InvokeScheduledMethodFrameSet(ctx, args.MethodID, inputs)
		if err != nil {
			return nil, err
		}
		out := &flowrt.InvocationResult{
			StatusCode:       result.StatusCode,
			Yielded:          result.Yielded,
			BacklogRemaining: result.BacklogRemaining,
		}
		out.Outputs = make([]flowrt.FrameOutput, 0, len(result.Outputs))
		for _, frame := range result.Outputs {
			if frame.WireFormat != 0 {
				return nil, fmt.Errorf("output frame %q requires canonical fallback across independently instantiated WASM memories", frame.PortID)
			}
			out.Outputs = append(out.Outputs, flowrt.FrameOutput{
				PortID:            frame.PortID,
				Bytes:             append([]byte(nil), frame.Payload...),
				SchemaName:        frame.SchemaName,
				FileIdentifier:    frame.FileIdentifier,
				SchemaVersion:     frame.SchemaVersion,
				SchemaHash:        append([]byte(nil), frame.SchemaHash...),
				RootTypeName:      frame.RootTypeName,
				WireFormat:        frame.WireFormat,
				FixedStringLength: uint32(frame.FixedStringLength),
				ByteLength:        frame.ByteLength,
				RequiredAlignment: uint32(frame.RequiredAlignment),
				Alignment:         frame.Alignment,
				Ownership:         frame.Ownership,
				Mutability:        frame.Mutability,
				Lifetime:          1,
				FrameID:           frame.FrameID,
			})
		}
		// Publication is declaration-driven: nodeId comes from the signed parent
		// invocation and portId from the signed child output. Unknown pairs are
		// ignored without examining the opaque payload. Publish only after every
		// child output has passed the cross-instance canonical boundary check.
		if runtimeNodes != nil {
			runtimeNodes.publish(args.NodeID, result.Outputs)
		}
		return out, nil
	}
}

func (in *Installer) registerWakeupHandlers(live *liveIsomorphicFlow) error {
	if in.svc.Wakeups == nil {
		return nil
	}
	for _, node := range live.nodes {
		manifest := node.Manifest()
		if !declaresWakeupCallback(manifest) {
			continue
		}
		artifactHash, nodeID := node.BoundIdentity()
		identity := sdnservices.WakeupIdentity{ArtifactHash: artifactHash, NodeID: nodeID}
		unregister, err := in.svc.Wakeups.Register(identity, func(sdnservices.Wakeup) {
			in.activateIsomorphicFlow(live, "wakeup")
		})
		if err != nil {
			return fmt.Errorf("register generic wakeup delivery for signed node %q: %w", node.ID(), err)
		}
		live.unregister = append(live.unregister, unregister)
	}
	return nil
}

func declaresWakeupCallback(manifest *modulert.Manifest) bool {
	if manifest == nil {
		return false
	}
	capable := false
	for _, capability := range manifest.Capabilities {
		if capability == "timers" {
			capable = true
			break
		}
	}
	if !capable {
		return false
	}
	for _, method := range manifest.Methods {
		if method.MethodID == "on_wakeup" {
			return true
		}
	}
	return false
}

func closeLiveIsomorphicFlow(live *liveIsomorphicFlow) {
	if live == nil {
		return
	}
	if live.activationCancel != nil {
		live.activationCancel()
	}
	for _, unregister := range live.unregister {
		if unregister != nil {
			unregister()
		}
	}
	// Release serializes with Activate, so it first waits for any wakeup whose
	// callback was already delivered to finish draining the parent graph. Only
	// then is it safe to close child instances the graph may invoke.
	if live.parent != nil {
		live.parent.Release()
	}
	for _, node := range live.nodes {
		_ = node.Close()
	}
}

type wasmIsomorphicParent struct {
	mu      sync.Mutex
	runtime *flowrt.FlowRuntime
}

func (parent *wasmIsomorphicParent) ValidateNodes(nodes []flowrt.IsomorphicNodeArtifact) error {
	if parent == nil || parent.runtime == nil || parent.runtime.Module() == nil {
		return errors.New("signed parent runtime is unavailable")
	}
	dispatch := make([]isomorphicDispatchDescriptor, 0, parent.runtime.NodeCount)
	for index := uint32(0); index < parent.runtime.NodeCount; index++ {
		descriptor, err := parent.runtime.GetNodeDispatchDescriptor(index)
		if err != nil {
			return fmt.Errorf("read dispatch descriptor %d: %w", index, err)
		}
		dispatch = append(dispatch, isomorphicDispatchDescriptor{
			DependencyID:  parent.readCString(descriptor.DependencyIDPointer),
			DispatchModel: parent.readCString(descriptor.DispatchModelPointer),
		})
	}
	dependencies := make([]isomorphicDependencyDescriptor, 0, parent.runtime.DepCount)
	for index := uint32(0); index < parent.runtime.DepCount; index++ {
		descriptor, err := parent.runtime.GetDependencyDescriptor(index)
		if err != nil {
			return fmt.Errorf("read dependency descriptor %d: %w", index, err)
		}
		dependencies = append(dependencies, isomorphicDependencyDescriptor{
			DependencyID: parent.readCString(descriptor.DependencyIDPointer),
			SHA256:       parent.readCString(descriptor.SHA256Pointer),
		})
	}
	return validateIsomorphicDescriptors(nodes, dispatch, dependencies)
}

func (parent *wasmIsomorphicParent) readCString(pointer uint32) string {
	if pointer == 0 || parent == nil || parent.runtime == nil || parent.runtime.Module() == nil {
		return ""
	}
	value, _ := parent.runtime.Module().ReadCString(pointer, 4096)
	return strings.TrimSpace(value)
}

func (parent *wasmIsomorphicParent) Activate(ctx context.Context, handlers flowrt.HandlerMap) error {
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if err := parent.prepareActivationLocked(); err != nil {
		return err
	}
	_, err := parent.runtime.DrainOnce(ctx, handlers)
	return err
}

func (parent *wasmIsomorphicParent) PrepareActivation() error {
	parent.mu.Lock()
	defer parent.mu.Unlock()
	return parent.prepareActivationLocked()
}

func (parent *wasmIsomorphicParent) prepareActivationLocked() error {
	if parent.runtime == nil {
		return errors.New("signed parent runtime is closed")
	}
	if parent.runtime.TriggerCount != 1 {
		return fmt.Errorf("signed isomorphic parent declares %d ingress triggers, want exactly one generic startup/wakeup ingress", parent.runtime.TriggerCount)
	}
	if err := parent.runtime.EnqueueTriggerChecked(0); err != nil {
		return fmt.Errorf("enqueue generic startup/wakeup trigger: %w", err)
	}
	return nil
}

func (parent *wasmIsomorphicParent) DrainPrepared(ctx context.Context, handlers flowrt.HandlerMap) error {
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.runtime == nil {
		return errors.New("signed parent runtime is closed")
	}
	_, err := parent.runtime.DrainOnce(ctx, handlers)
	return err
}

func (parent *wasmIsomorphicParent) Release() {
	if parent == nil {
		return
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.runtime != nil {
		parent.runtime.Release()
		parent.runtime = nil
	}
}

func validateIsomorphicDescriptors(nodes []flowrt.IsomorphicNodeArtifact, dispatch []isomorphicDispatchDescriptor, dependencies []isomorphicDependencyDescriptor) error {
	wanted := make(map[string]flowrt.IsomorphicNodeArtifact, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.EntryID)
		if id == "" {
			return errors.New("signed child has empty EntryID")
		}
		if _, duplicate := wanted[id]; duplicate {
			return fmt.Errorf("duplicate signed child EntryID %q", id)
		}
		wanted[id] = node
	}

	declared := make(map[string]isomorphicDependencyDescriptor, len(dependencies))
	for _, dependency := range dependencies {
		id := strings.TrimSpace(dependency.DependencyID)
		if _, duplicate := declared[id]; duplicate {
			return fmt.Errorf("duplicate parent dependency descriptor %q", id)
		}
		declared[id] = dependency
	}
	for id, node := range wanted {
		dependency, ok := declared[id]
		if !ok {
			return fmt.Errorf("signed child %q is absent from parent dependency descriptors", id)
		}
		if !strings.EqualFold(strings.TrimSpace(dependency.SHA256), node.EntryHash) {
			return fmt.Errorf("signed child %q descriptor hash %q does not match exact member hash %q", id, dependency.SHA256, node.EntryHash)
		}
	}
	for id := range declared {
		if _, ok := wanted[id]; !ok {
			return fmt.Errorf("parent dependency descriptor %q has no verified signed child member", id)
		}
	}

	used := make(map[string]bool, len(wanted))
	for _, descriptor := range dispatch {
		id := strings.TrimSpace(descriptor.DependencyID)
		model := strings.TrimSpace(descriptor.DispatchModel)
		_, isSignedChild := wanted[id]
		if model == "isomorphic" && !isSignedChild {
			return fmt.Errorf("isomorphic dispatch references unverified dependency %q", id)
		}
		if isSignedChild && model != "isomorphic" {
			return fmt.Errorf("signed child %q uses dispatch model %q, want isomorphic", id, model)
		}
		if isSignedChild {
			used[id] = true
		}
	}
	for id := range wanted {
		if !used[id] {
			return fmt.Errorf("signed child %q is never instantiated by an isomorphic node dispatch", id)
		}
	}
	return nil
}

// Install loads a flow bundle, registers it with the cron scheduler, and (when
// persist is set) records it in the installed-flows registry. The capability
// policy is enforced FAIL CLOSED inside LoadFlowService — a flow requesting an
// unapproved sensitive capability is refused here and is NOT registered or
// persisted. source is a provenance tag. Idempotent by flow id: re-installing
// an already-registered id refreshes the registry entry and closes the
// freshly-loaded duplicate rather than double-registering.
func (in *Installer) Install(spec FlowSpec, source string) (InstalledFlow, error) {
	return in.install(spec, source, true)
}

func (in *Installer) install(spec FlowSpec, source string, persist bool) (InstalledFlow, error) {
	if strings.TrimSpace(spec.Ref) == "" {
		return InstalledFlow{}, errors.New("sdnflows: flow spec has empty ref")
	}
	sf, err := flowrt.LoadFlowService(spec.Ref, spec.Intervals, spec.Config, in.deps())
	if err != nil {
		if strings.Contains(err.Error(), "capability policy") {
			return InstalledFlow{}, fmt.Errorf("%w: %s: %v", ErrInstallDenied, spec.Ref, err)
		}
		return InstalledFlow{}, fmt.Errorf("sdnflows: load flow %q: %w", spec.Ref, err)
	}
	id := sf.ID()
	if strings.TrimSpace(id) == "" {
		_ = sf.Close()
		return InstalledFlow{}, fmt.Errorf("sdnflows: flow %q has empty id", spec.Ref)
	}

	in.mu.Lock()
	if _, exists := in.loaded[id]; exists {
		in.mu.Unlock()
		_ = sf.Close()
		if persist {
			if err := in.persist(id, spec, source); err != nil {
				return InstalledFlow{}, err
			}
		}
		in.logf("sdnflows: flow %q already installed; refreshed registry entry", id)
		return in.view(sf, source, persist), nil
	}

	if err := in.svc.Scheduler.Register(sdncron.Registration{
		Module:  sf,
		Name:    sf.Name(),
		Version: sf.Version(),
	}); err != nil {
		in.mu.Unlock()
		_ = sf.Close()
		return InstalledFlow{}, fmt.Errorf("sdnflows: register flow %q with scheduler: %w", id, err)
	}
	in.loaded[id] = sf
	in.mu.Unlock()

	if persist {
		if err := in.persist(id, spec, source); err != nil {
			return InstalledFlow{}, err
		}
	}
	in.logf("sdnflows: installed + registered flow %q (%d timer(s)) [source=%s]", id, len(sf.Triggers()), source)
	return in.view(sf, source, persist), nil
}

func (in *Installer) persist(id string, spec FlowSpec, source string) error {
	if err := in.reg.Put(InstalledEntry{
		ID:        id,
		Ref:       spec.Ref,
		Intervals: spec.Intervals,
		Config:    spec.Config,
		Enabled:   true,
		Source:    source,
	}); err != nil {
		return fmt.Errorf("sdnflows: persist registry entry for %q: %w", id, err)
	}
	return nil
}

func (in *Installer) view(sf *flowrt.ServiceFlow, source string, enabled bool) InstalledFlow {
	timers := make([]string, 0)
	for _, t := range sf.Triggers() {
		timers = append(timers, t.TriggerID)
	}
	sort.Strings(timers)
	return InstalledFlow{
		ID:      sf.ID(),
		Name:    sf.Name(),
		Version: sf.Version(),
		Ref:     "",
		Enabled: enabled,
		Source:  source,
		Timers:  timers,
	}
}

// BootFailure records one flow/bundle that FAILED to load during Boot. A
// skipped entry is a unit of the node that is silently not running (a
// fail-closed capability denial, a missing bundle, a hash mismatch), so the
// failure set is retained for the whole process lifetime and callers surface
// it loudly instead of letting an INFO "skipping" line be the only trace.
type BootFailure struct {
	ID     string `json:"id"`     // registry entry id / flow ref that failed
	Source string `json:"source"` // provenance (registry source, "boot-set", "drop-in")
	Error  string `json:"error"`  // the load error, verbatim
	At     string `json:"at"`     // RFC3339 UTC
}

// BootFailures returns every load failure recorded by the last Boot pass.
func (in *Installer) BootFailures() []BootFailure {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]BootFailure(nil), in.bootFailures...)
}

func (in *Installer) recordBootFailure(id, source string, err error) {
	failure := BootFailure{
		ID:     id,
		Source: source,
		Error:  err.Error(),
		At:     time.Now().UTC().Format(time.RFC3339),
	}
	in.mu.Lock()
	in.bootFailures = append(in.bootFailures, failure)
	in.mu.Unlock()
}

// Boot re-establishes the installed-flows set on a fresh Services build: it
// re-loads and re-registers every ENABLED persisted registry entry, then
// installs any additional configured bootSet flows not already installed. It is
// tolerant: an entry whose bundle is missing, or whose sensitive capabilities
// are unapproved, is logged and skipped rather than failing the whole boot —
// but every skip is recorded (see BootFailures) so the caller can make the
// failure loudly visible after startup.
//
// Boot registers flows but does NOT start the scheduler — the caller starts it
// after Boot so every timer begins together.
func (in *Installer) Boot(ctx context.Context, bootSet []FlowSpec) (int, error) {
	registered := 0
	seen := map[string]bool{}

	in.mu.Lock()
	in.bootFailures = nil
	in.mu.Unlock()

	entries, err := in.reg.List()
	if err != nil {
		return 0, fmt.Errorf("sdnflows: read installed registry: %w", err)
	}
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if strings.HasPrefix(e.Source, isomorphicSourcePrefix) {
			source := strings.TrimPrefix(e.Source, isomorphicSourcePrefix)
			flow, installErr := in.installSignedBundle(ctx, e.Ref, source, false, e.ID)
			if installErr != nil {
				in.logf("sdnflows: boot: restore signed bundle %q failed; skipping: %v", e.ID, installErr)
				in.recordBootFailure(e.ID, e.Source, installErr)
				continue
			}
			seen[flow.ID] = true
			registered++
			continue
		}
		if _, err := in.install(FlowSpec{Ref: e.Ref, Intervals: e.Intervals, Config: e.Config}, e.Source, false); err != nil {
			in.logf("sdnflows: boot: register %q failed; skipping: %v", e.ID, err)
			in.recordBootFailure(e.ID, e.Source, err)
			continue
		}
		seen[e.ID] = true
		registered++
	}

	for _, spec := range bootSet {
		f, err := in.install(spec, "boot-set", true)
		if err != nil {
			in.logf("sdnflows: boot: install configured flow %q failed; skipping: %v", spec.Ref, err)
			in.recordBootFailure(spec.Ref, "boot-set", err)
			continue
		}
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		registered++
	}

	if in.dropinDir != "" {
		entries, readErr := os.ReadDir(in.dropinDir)
		if readErr != nil && !os.IsNotExist(readErr) {
			return registered, fmt.Errorf("sdnflows: read signed bundle drop-in directory: %w", readErr)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ref := filepath.Join(in.dropinDir, entry.Name())
			flow, installErr := in.installSignedBundle(ctx, ref, "drop-in", true)
			if installErr != nil {
				in.logf("sdnflows: boot: signed bundle drop-in %q failed; skipping: %v", ref, installErr)
				in.recordBootFailure(ref, "drop-in", installErr)
				continue
			}
			if seen[flow.ID] {
				continue
			}
			seen[flow.ID] = true
			registered++
		}
	}
	return registered, nil
}

// List returns the read model for every flow installed in THIS process, sorted
// by id, joined with its persisted registry provenance.
func (in *Installer) List() []InstalledFlow {
	in.mu.Lock()
	ids := make([]string, 0, len(in.loaded))
	flows := make(map[string]*flowrt.ServiceFlow, len(in.loaded))
	for id, f := range in.loaded {
		ids = append(ids, id)
		flows[id] = f
	}
	in.mu.Unlock()

	sort.Strings(ids)
	out := make([]InstalledFlow, 0, len(ids))
	for _, id := range ids {
		sf := flows[id]
		source, ref, enabled := "", "", true
		if e, ok, _ := in.reg.Get(id); ok {
			source, ref, enabled = e.Source, e.Ref, e.Enabled
		}
		v := in.view(sf, source, enabled)
		v.Ref = ref
		out = append(out, v)
	}
	return out
}

// Flow returns the live ServiceFlow handle for id, or nil.
func (in *Installer) Flow(id string) *flowrt.ServiceFlow {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.loaded[id]
}

// IsomorphicFlow returns a copy of the installed signed-bundle read model, or
// nil when no bundle with that content hash is live.
func (in *Installer) IsomorphicFlow(contentHash string) *InstalledIsomorphicFlow {
	in.mu.Lock()
	defer in.mu.Unlock()
	live := in.isomorphic[strings.ToLower(strings.TrimSpace(contentHash))]
	if live == nil {
		return nil
	}
	view := live.view
	view.Nodes = append([]InstalledIsomorphicNode(nil), live.view.Nodes...)
	return &view
}

// ReadArtifactRuntimeNode returns the latest exact bytes published for an
// opaque runtime-node key declared by the installed bundle's signed
// artifact.json. The content hash selects the signed parent artifact; no
// application record, FlatBuffer, or status payload is decoded here.
func (in *Installer) ReadArtifactRuntimeNode(contentHash, key string) ([]byte, string, bool) {
	contentHash = strings.ToLower(strings.TrimSpace(contentHash))
	in.mu.Lock()
	live := in.isomorphic[contentHash]
	in.mu.Unlock()
	if live == nil {
		return nil, "", false
	}
	return live.runtimeNodes.read(key)
}

// Close releases every loaded flow handle. The scheduler is owned by the
// Services bundle (svc.Close stops it); this only closes the flow runtimes.
func (in *Installer) Close() {
	in.mu.Lock()
	flows := make([]*flowrt.ServiceFlow, 0, len(in.loaded))
	for _, f := range in.loaded {
		flows = append(flows, f)
	}
	in.loaded = make(map[string]*flowrt.ServiceFlow)
	isomorphic := make([]*liveIsomorphicFlow, 0, len(in.isomorphic))
	for _, flow := range in.isomorphic {
		isomorphic = append(isomorphic, flow)
	}
	in.isomorphic = make(map[string]*liveIsomorphicFlow)
	in.mu.Unlock()
	for _, f := range flows {
		_ = f.Close()
	}
	for _, flow := range isomorphic {
		closeLiveIsomorphicFlow(flow)
	}
}
