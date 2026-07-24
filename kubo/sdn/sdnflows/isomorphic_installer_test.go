package sdnflows

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

type fakeIsomorphicNode struct {
	id        string
	hash      string
	manifest  *modulert.Manifest
	inputs    []modulert.InvokeInputFrame
	closed    bool
	invokeErr error
	outputs   []modulert.InvokeOutputFrame
	yielded   bool
	backlog   uint32
}

func (node *fakeIsomorphicNode) ID() string                      { return node.id }
func (node *fakeIsomorphicNode) ContentHash() string             { return node.hash }
func (node *fakeIsomorphicNode) BoundIdentity() (string, string) { return node.hash, node.id }
func (node *fakeIsomorphicNode) Manifest() *modulert.Manifest    { return node.manifest }
func (node *fakeIsomorphicNode) Close() error                    { node.closed = true; return nil }
func (node *fakeIsomorphicNode) InvokeMethodFrameSet(_ context.Context, _ string, inputs []modulert.InvokeInputFrame) (*modulert.InvokeFrameSetResult, error) {
	node.inputs = append([]modulert.InvokeInputFrame(nil), inputs...)
	if node.invokeErr != nil {
		return nil, node.invokeErr
	}
	return &modulert.InvokeFrameSetResult{
		Outputs:          append([]modulert.InvokeOutputFrame(nil), node.outputs...),
		Yielded:          node.yielded,
		BacklogRemaining: node.backlog,
	}, nil
}
func (node *fakeIsomorphicNode) InvokeScheduledMethodFrameSet(ctx context.Context, method string, inputs []modulert.InvokeInputFrame) (*modulert.InvokeFrameSetResult, error) {
	return node.InvokeMethodFrameSet(ctx, method, inputs)
}

type fakeIsomorphicParent struct {
	validated    []flowrt.IsomorphicNodeArtifact
	handlers     flowrt.HandlerMap
	closed       bool
	bootErr      error
	drainErr     error
	activated    chan struct{}
	activateOnce sync.Once
}

type activationContextParent struct {
	activationResult chan error
}

func (*activationContextParent) ValidateNodes([]flowrt.IsomorphicNodeArtifact) error { return nil }
func (*activationContextParent) PrepareActivation() error                            { return nil }
func (*activationContextParent) DrainPrepared(ctx context.Context, _ flowrt.HandlerMap) error {
	return ctx.Err()
}
func (parent *activationContextParent) Activate(ctx context.Context, _ flowrt.HandlerMap) error {
	err := ctx.Err()
	parent.activationResult <- err
	return err
}
func (*activationContextParent) Release() {}

func (parent *fakeIsomorphicParent) ValidateNodes(nodes []flowrt.IsomorphicNodeArtifact) error {
	parent.validated = append([]flowrt.IsomorphicNodeArtifact(nil), nodes...)
	return parent.drainErr
}

func (parent *fakeIsomorphicParent) PrepareActivation() error {
	return parent.bootErr
}

func (parent *fakeIsomorphicParent) DrainPrepared(_ context.Context, handlers flowrt.HandlerMap) error {
	parent.handlers = handlers
	if parent.activated != nil {
		parent.activateOnce.Do(func() { close(parent.activated) })
	}
	return nil
}

func (parent *fakeIsomorphicParent) Activate(ctx context.Context, handlers flowrt.HandlerMap) error {
	if err := parent.PrepareActivation(); err != nil {
		return err
	}
	return parent.DrainPrepared(ctx, handlers)
}

func (parent *fakeIsomorphicParent) Release() { parent.closed = true }

type blockingLifecycleParent struct {
	mu      sync.Mutex
	entered chan struct{}
	resume  chan struct{}
	release chan struct{}
	err     error
}

func (parent *blockingLifecycleParent) ValidateNodes([]flowrt.IsomorphicNodeArtifact) error {
	return nil
}
func (parent *blockingLifecycleParent) PrepareActivation() error { return nil }
func (parent *blockingLifecycleParent) DrainPrepared(context.Context, flowrt.HandlerMap) error {
	parent.mu.Lock()
	defer parent.mu.Unlock()
	close(parent.entered)
	<-parent.resume
	return parent.err
}
func (parent *blockingLifecycleParent) Activate(ctx context.Context, handlers flowrt.HandlerMap) error {
	if err := parent.PrepareActivation(); err != nil {
		return err
	}
	return parent.DrainPrepared(ctx, handlers)
}
func (parent *blockingLifecycleParent) Release() {
	parent.mu.Lock()
	defer parent.mu.Unlock()
	close(parent.release)
}

type closeSignalNode struct{ closed chan struct{} }

func (*closeSignalNode) ID() string                      { return "node" }
func (*closeSignalNode) ContentHash() string             { return strings.Repeat("d", 64) }
func (*closeSignalNode) BoundIdentity() (string, string) { return strings.Repeat("d", 64), "node" }
func (*closeSignalNode) Manifest() *modulert.Manifest    { return nil }
func (*closeSignalNode) InvokeMethodFrameSet(context.Context, string, []modulert.InvokeInputFrame) (*modulert.InvokeFrameSetResult, error) {
	return nil, nil
}
func (*closeSignalNode) InvokeScheduledMethodFrameSet(context.Context, string, []modulert.InvokeInputFrame) (*modulert.InvokeFrameSetResult, error) {
	return nil, nil
}
func (node *closeSignalNode) Close() error { close(node.closed); return nil }

func newIsomorphicInstallerForTest(t *testing.T) (*Installer, *Registry) {
	t.Helper()
	registry, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installer, err := New(Config{
		Services: &sdnservices.Services{
			Scheduler: sdncron.NewScheduler(nil, nil),
			CapReg:    modulert.NewCapabilityRegistry(),
		},
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	return installer, registry
}

func neutralSignedBundleMetadata() *flowrt.IsomorphicBundleMetadata {
	first := []byte{0x00, 0x01, 0xff, 'a'}
	second := []byte{0x10, 0x00, 0x20, 'b'}
	// Minimal shape sufficient for the generic admission checks: a canonical
	// PLG identifier and a size-prefixed APP identifier. Parsing domain fields
	// remains outside the host.
	flowPLG := []byte{0x08, 0x00, 0x00, 0x00, '$', 'P', 'L', 'G'}
	app := []byte{0x08, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, '$', 'A', 'P', 'P'}
	return &flowrt.IsomorphicBundleMetadata{
		ContentHash:      strings.Repeat("a", 64),
		SignedArtifact:   []byte("outer-signed-bundle"),
		PortableArtifact: []byte("signed-parent-runtime-portable-bytes"),
		Signature:        modulert.ModuleSignatureStatus{Signed: true, Verified: true, SignatureScope: "bundle"},
		Bundle: &modulert.VerifiedModuleBundle{Entries: []modulert.VerifiedBundleEntry{
			{EntryID: "nodes/source.wasm", SectionName: "sdn.flow.node.source", MediaType: "application/wasm", SHA256Hex: strings.Repeat("1", 64), Payload: first},
			{EntryID: "nodes/sink.wasm", SectionName: "sdn.flow.node.sink", MediaType: "application/wasm", SHA256Hex: strings.Repeat("2", 64), Payload: second},
			{EntryID: "flow.plg", SectionName: "sdn.flow.plg", TypeRef: "PLG.fbs", SHA256Hex: strings.Repeat("3", 64), Payload: flowPLG},
			{EntryID: "artifact.json", SectionName: "sdn.flow.artifact", MediaType: "application/json", SHA256Hex: strings.Repeat("4", 64), Payload: []byte(`{"format":"neutral","runtimeNodeRoutes":[{"key":"alpha.bin","nodeId":"source","portId":"alpha","mediaType":"application/x-example-opaque"}]}`)},
			{EntryID: "app.app", SectionName: "sdn.app.record", TypeRef: "APP.fbs", SHA256Hex: strings.Repeat("5", 64), Payload: app},
		}},
		Nodes: []flowrt.IsomorphicNodeArtifact{
			{EntryID: "nodes/source.wasm", EntryHash: strings.Repeat("1", 64), ContentHash: strings.Repeat("b", 64), SignedArtifact: first, PortableArtifact: []byte("source-portable"), Signature: modulert.ModuleSignatureStatus{Signed: true, Verified: true}},
			{EntryID: "nodes/sink.wasm", EntryHash: strings.Repeat("2", 64), ContentHash: strings.Repeat("c", 64), SignedArtifact: second, PortableArtifact: []byte("sink-portable"), Signature: modulert.ModuleSignatureStatus{Signed: true, Verified: true}},
		},
	}
}

func TestInstallSignedBundleInstantiatesExactMembersAndBindsEntryIDHandlers(t *testing.T) {
	installer, registry := newIsomorphicInstallerForTest(t)
	defer installer.Close()
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}

	var verifiedBytes []byte
	installer.verifyIsomorphicBundle = func(signed []byte) (*flowrt.IsomorphicBundleMetadata, error) {
		verifiedBytes = append([]byte(nil), signed...)
		return metadata, nil
	}
	var loadedBytes [][]byte
	var loadedScopes [][2]string
	schemaHash := []byte{0xde, 0xad, 0xbe, 0xef}
	nodes := []*fakeIsomorphicNode{
		{id: "org.example.source", hash: metadata.Nodes[0].ContentHash, manifest: &modulert.Manifest{PluginID: "org.example.source"}, outputs: []modulert.InvokeOutputFrame{{PortID: "alpha", Payload: []byte{0, 1}, Alignment: 8, SchemaName: "Opaque.fbs", FileIdentifier: "OPAQ", SchemaVersion: "1.0.0", SchemaHash: schemaHash, RootTypeName: "Opaque", Ownership: 0, Mutability: 0, FrameID: 41}, {PortID: "beta", Payload: []byte{2, 0, 3}, Alignment: 8, SchemaName: "Opaque.fbs", FileIdentifier: "OPAQ", SchemaVersion: "1.0.0", SchemaHash: schemaHash, RootTypeName: "Opaque", Ownership: 0, Mutability: 0, FrameID: 43}}, yielded: true, backlog: 3817},
		{id: "org.example.sink", hash: metadata.Nodes[1].ContentHash, manifest: &modulert.Manifest{PluginID: "org.example.sink"}},
	}
	loadIndex := 0
	installer.loadIsomorphicNode = func(signed []byte, outerHash, entryID string) (isomorphicNodeInstance, error) {
		loadedBytes = append(loadedBytes, append([]byte(nil), signed...))
		loadedScopes = append(loadedScopes, [2]string{outerHash, entryID})
		node := nodes[loadIndex]
		loadIndex++
		return node, nil
	}
	parent := &fakeIsomorphicParent{activated: make(chan struct{})}
	var parentBytes []byte
	installer.loadIsomorphicParent = func(portable []byte, _ uint32) (isomorphicParentRuntime, error) {
		parentBytes = append([]byte(nil), portable...)
		return parent, nil
	}
	var storedAPP []byte
	installer.storeApplicationArtifact = func(_ context.Context, app []byte) error {
		storedAPP = append([]byte(nil), app...)
		return nil
	}

	view, err := installer.InstallSignedBundle(t.Context(), bundlePath, "test")
	if err != nil {
		t.Fatalf("InstallSignedBundle() error = %v", err)
	}
	if view.ID != metadata.ContentHash || len(view.Nodes) != 2 {
		t.Fatalf("installed view = %+v", view)
	}
	if !bytes.Equal(verifiedBytes, metadata.SignedArtifact) || !bytes.Equal(parentBytes, metadata.PortableArtifact) {
		t.Fatalf("outer bytes changed: verified=%q parent=%q", verifiedBytes, parentBytes)
	}
	if len(loadedBytes) != 2 || !bytes.Equal(loadedBytes[0], metadata.Nodes[0].SignedArtifact) || !bytes.Equal(loadedBytes[1], metadata.Nodes[1].SignedArtifact) {
		t.Fatalf("child signed bytes changed: %x", loadedBytes)
	}
	if len(loadedScopes) != 2 || loadedScopes[0] != [2]string{metadata.ContentHash, metadata.Nodes[0].EntryID} || loadedScopes[1] != [2]string{metadata.ContentHash, metadata.Nodes[1].EntryID} {
		t.Fatalf("child instance scopes = %+v, want outer bundle hash + exact entry IDs", loadedScopes)
	}
	if !bytes.Equal(storedAPP, metadata.Bundle.Entries[4].Payload) {
		t.Fatalf("stored APP bytes = %x", storedAPP)
	}
	select {
	case <-parent.activated:
	case <-time.After(time.Second):
		t.Fatal("background parent activation did not start")
	}
	if len(parent.validated) != 2 || parent.handlers == nil {
		t.Fatalf("parent validation/handlers = %d/%v", len(parent.validated), parent.handlers)
	}

	// The signed parent graph selects dependency + method + input ports. The
	// host only resolves the verified EntryID and forwards exact opaque bytes.
	handler := parent.handlers.Resolve("org.example.source", "emit", metadata.Nodes[0].EntryID, "source")
	if handler == nil {
		t.Fatal("no handler bound for verified child EntryID")
	}
	result, err := handler(t.Context(), &flowrt.InvocationArgs{
		DependencyID: metadata.Nodes[0].EntryID,
		NodeID:       "source",
		MethodID:     "emit",
		Frames: []flowrt.FrameData{
			{PortID: "request-a", Bytes: []byte{0, 0xff, 1}, Alignment: 8, SchemaName: "Opaque.fbs", FileIdentifier: "OPAQ", SchemaVersion: "1.0.0", SchemaHash: schemaHash, RootTypeName: "Opaque", Ownership: 0, Mutability: 0, Lifetime: 1, FrameID: 37},
			{PortID: "request-b", Bytes: []byte{2, 0, 3}, Alignment: 8, SchemaName: "Opaque.fbs", FileIdentifier: "OPAQ", SchemaVersion: "1.0.0", SchemaHash: schemaHash, RootTypeName: "Opaque", Ownership: 0, Mutability: 0, Lifetime: 1, FrameID: 39},
		},
	})
	if err != nil {
		t.Fatalf("child handler error = %v", err)
	}
	if len(nodes[0].inputs) != 2 || nodes[0].inputs[0].PortID != "request-a" || !bytes.Equal(nodes[0].inputs[0].Payload, []byte{0, 0xff, 1}) {
		t.Fatalf("forwarded child inputs = %+v", nodes[0].inputs)
	}
	for index, input := range nodes[0].inputs {
		if input.WireFormat != 0 || input.Alignment != 8 || input.Offset != 0 ||
			input.SchemaName != "Opaque.fbs" || input.FileIdentifier != "OPAQ" ||
			input.SchemaVersion != "1.0.0" || !bytes.Equal(input.SchemaHash, schemaHash) ||
			input.RootTypeName != "Opaque" || input.FrameID != uint64(37+index*2) {
			t.Fatalf("input %d lost canonical identity/frame metadata at the instance boundary: %+v", index, input)
		}
	}
	if len(result.Outputs) != 2 || result.Outputs[0].PortID != "alpha" || !bytes.Equal(result.Outputs[1].Bytes, []byte{2, 0, 3}) {
		t.Fatalf("forwarded child outputs = %+v", result.Outputs)
	}
	if !result.Yielded || result.BacklogRemaining != 3817 {
		t.Fatalf("forwarded continuation = yielded:%v backlog:%d, want true/3817", result.Yielded, result.BacklogRemaining)
	}
	if result.Outputs[0].SchemaVersion != "1.0.0" || !bytes.Equal(result.Outputs[0].SchemaHash, schemaHash) ||
		result.Outputs[0].FrameID != 41 || result.Outputs[0].Alignment != 8 {
		t.Fatalf("forwarded output lost identity/frame metadata: %+v", result.Outputs[0])
	}
	published, mediaType, ok := installer.ReadArtifactRuntimeNode(metadata.ContentHash, "alpha.bin")
	if !ok || mediaType != "application/x-example-opaque" || !bytes.Equal(published, []byte{0, 1}) {
		t.Fatalf("declared opaque route = (%x, %q, %v), want exact alpha output", published, mediaType, ok)
	}
	published[0] = 0xff
	again, _, ok := installer.ReadArtifactRuntimeNode(metadata.ContentHash, "alpha.bin")
	if !ok || !bytes.Equal(again, []byte{0, 1}) {
		t.Fatalf("opaque route read aliases caller bytes: %x", again)
	}
	if _, _, ok := installer.ReadArtifactRuntimeNode(metadata.ContentHash, "beta"); ok {
		t.Fatal("undeclared child output became externally routable")
	}
	if _, ok, err := registry.Get(metadata.ContentHash); err != nil || !ok {
		t.Fatalf("persisted registry = ok:%v err:%v", ok, err)
	}
}

func TestInstallSignedBundleWakeupUsesInstallerLifetimeAfterCallerCancellation(t *testing.T) {
	installer, _ := newIsomorphicInstallerForTest(t)
	defer installer.Close()
	wakeups := sdnservices.NewWakeupBroker(nil)
	defer wakeups.Close()
	installer.svc.Wakeups = wakeups

	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) {
		return metadata, nil
	}
	parent := &activationContextParent{activationResult: make(chan error, 1)}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) {
		return parent, nil
	}
	timerNode := &fakeIsomorphicNode{
		id:   "neutral.timer",
		hash: metadata.Nodes[0].ContentHash,
		manifest: &modulert.Manifest{
			Capabilities: []string{"timers"},
			Methods:      []modulert.ManifestMethod{{MethodID: "on_wakeup"}},
		},
	}
	nodes := []isomorphicNodeInstance{
		timerNode,
		&fakeIsomorphicNode{id: "neutral.sink", hash: metadata.Nodes[1].ContentHash},
	}
	loadIndex := 0
	installer.loadIsomorphicNode = func([]byte, string, string) (isomorphicNodeInstance, error) {
		node := nodes[loadIndex]
		loadIndex++
		return node, nil
	}
	installer.storeApplicationArtifact = func(context.Context, []byte) error { return nil }

	callerCtx, cancelCaller := context.WithCancel(t.Context())
	if _, err := installer.InstallSignedBundle(callerCtx, bundlePath, "test"); err != nil {
		t.Fatalf("InstallSignedBundle() error = %v", err)
	}
	cancelCaller()

	identity := sdnservices.WakeupIdentity{ArtifactHash: timerNode.hash, NodeID: timerNode.id}
	if err := wakeups.Arm(identity, "neutral-token", time.Now()); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	select {
	case activationErr := <-parent.activationResult:
		if activationErr != nil {
			t.Fatalf("wakeup activation context error = %v, want nil", activationErr)
		}
	case <-time.After(time.Second):
		t.Fatal("wakeup did not activate the successfully installed flow")
	}
}

func TestInstallSignedBundleStartupDrainUsesCallerCancellation(t *testing.T) {
	installer, registry := newIsomorphicInstallerForTest(t)
	defer installer.Close()
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) {
		return metadata, nil
	}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) {
		return &activationContextParent{activationResult: make(chan error, 1)}, nil
	}
	loadIndex := 0
	installer.loadIsomorphicNode = func([]byte, string, string) (isomorphicNodeInstance, error) {
		artifact := metadata.Nodes[loadIndex]
		loadIndex++
		return &fakeIsomorphicNode{id: artifact.EntryID, hash: artifact.ContentHash}, nil
	}
	stored := false
	installer.storeApplicationArtifact = func(context.Context, []byte) error {
		stored = true
		return nil
	}

	callerCtx, cancelCaller := context.WithCancel(t.Context())
	cancelCaller()
	_, err := installer.InstallSignedBundle(callerCtx, bundlePath, "test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InstallSignedBundle() error = %v, want caller cancellation", err)
	}
	if stored || installer.IsomorphicFlow(metadata.ContentHash) != nil {
		t.Fatal("caller-canceled startup was published")
	}
	if entries, listErr := registry.List(); listErr != nil || len(entries) != 0 {
		t.Fatalf("registry after caller-canceled startup = %+v, %v", entries, listErr)
	}
}

func TestInstallSignedBundleRejectsStartupTriggerBeforePublishingApplication(t *testing.T) {
	installer, registry := newIsomorphicInstallerForTest(t)
	defer installer.Close()
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) {
		return metadata, nil
	}
	parent := &fakeIsomorphicParent{bootErr: errors.New("startup trigger descriptor rejected")}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) {
		return parent, nil
	}
	index := 0
	installer.loadIsomorphicNode = func([]byte, string, string) (isomorphicNodeInstance, error) {
		artifact := metadata.Nodes[index]
		index++
		return &fakeIsomorphicNode{id: artifact.EntryID, hash: artifact.ContentHash}, nil
	}
	stored := false
	installer.storeApplicationArtifact = func(context.Context, []byte) error {
		stored = true
		return nil
	}

	_, err := installer.InstallSignedBundle(t.Context(), bundlePath, "test")
	if err == nil || !strings.Contains(err.Error(), "startup trigger descriptor rejected") {
		t.Fatalf("InstallSignedBundle() error = %v, want observable startup preflight rejection", err)
	}
	if stored {
		t.Fatal("APP artifact was published before startup trigger preflight succeeded")
	}
	if installer.IsomorphicFlow(metadata.ContentHash) != nil {
		t.Fatal("startup-rejected bundle became visible")
	}
	if entries, listErr := registry.List(); listErr != nil || len(entries) != 0 {
		t.Fatalf("registry after startup rejection = %+v, %v", entries, listErr)
	}
	if !parent.closed {
		t.Fatal("startup-rejected parent runtime was not released")
	}
}

func TestIsomorphicNodeHandlerRequiresCanonicalFallbackAcrossSeparateMemories(t *testing.T) {
	node := &fakeIsomorphicNode{id: "org.example.separate", hash: strings.Repeat("e", 64)}
	handler := isomorphicNodeHandler(node, nil)

	_, err := handler(t.Context(), &flowrt.InvocationArgs{
		NodeID:   "separate",
		MethodID: "consume",
		Frames: []flowrt.FrameData{{
			PortID:       "records",
			Bytes:        make([]byte, 64),
			WireFormat:   1,
			Alignment:    8,
			Ownership:    0,
			Mutability:   0,
			Lifetime:     1,
			SchemaName:   "Records.fbs",
			RootTypeName: "Records",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "canonical fallback") {
		t.Fatalf("aligned cross-instance input error = %v, want canonical fallback requirement", err)
	}
	if len(node.inputs) != 0 {
		t.Fatalf("aligned cross-instance input reached child: %+v", node.inputs)
	}

	node.outputs = []modulert.InvokeOutputFrame{{
		PortID:       "records",
		Payload:      make([]byte, 64),
		WireFormat:   1,
		Alignment:    8,
		SchemaName:   "Records.fbs",
		RootTypeName: "Records",
	}}
	_, err = handler(t.Context(), &flowrt.InvocationArgs{
		NodeID:   "separate",
		MethodID: "produce",
		Frames: []flowrt.FrameData{{
			PortID:       "start",
			WireFormat:   0,
			Alignment:    1,
			Lifetime:     1,
			SchemaName:   "Start.fbs",
			RootTypeName: "Start",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "canonical fallback") {
		t.Fatalf("aligned cross-instance output error = %v, want canonical fallback requirement", err)
	}
}

func TestSignedRuntimeNodeRouteMetadataRejectsAmbiguousOrUnsafeRoutes(t *testing.T) {
	base := neutralSignedBundleMetadata()
	artifactIndex := 3
	for name, payload := range map[string]string{
		"duplicate key":    `{"runtimeNodeRoutes":[{"key":"same.bin","nodeId":"a","portId":"out","mediaType":"application/octet-stream"},{"key":"same.bin","nodeId":"b","portId":"out","mediaType":"application/octet-stream"}]}`,
		"duplicate source": `{"runtimeNodeRoutes":[{"key":"one.bin","nodeId":"a","portId":"out","mediaType":"application/octet-stream"},{"key":"two.bin","nodeId":"a","portId":"out","mediaType":"application/octet-stream"}]}`,
		"unsafe key":       `{"runtimeNodeRoutes":[{"key":"../escape","nodeId":"a","portId":"out","mediaType":"application/octet-stream"}]}`,
		"normalized key":   `{"runtimeNodeRoutes":[{"key":" one.bin ","nodeId":"a","portId":"out","mediaType":"application/octet-stream"}]}`,
		"missing source":   `{"runtimeNodeRoutes":[{"key":"one.bin","nodeId":"","portId":"out","mediaType":"application/octet-stream"}]}`,
		"header injection": `{"runtimeNodeRoutes":[{"key":"one.bin","nodeId":"a","portId":"out","mediaType":"application/octet-stream\\r\\nX-Bad: true"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			metadata := *base
			bundle := *base.Bundle
			bundle.Entries = append([]modulert.VerifiedBundleEntry(nil), base.Bundle.Entries...)
			bundle.Entries[artifactIndex].Payload = []byte(payload)
			metadata.Bundle = &bundle
			if _, _, err := validateRequiredBundleMembers(&metadata); err == nil {
				t.Fatal("validateRequiredBundleMembers() accepted invalid signed route metadata")
			}
		})
	}
}

func TestCloseLiveIsomorphicFlowQuiescesParentBeforeClosingChildren(t *testing.T) {
	parent := &blockingLifecycleParent{
		entered: make(chan struct{}),
		resume:  make(chan struct{}),
		release: make(chan struct{}),
	}
	node := &closeSignalNode{closed: make(chan struct{})}
	activationCtx, activationCancel := context.WithCancel(context.Background())
	live := &liveIsomorphicFlow{
		parent:           parent,
		nodes:            map[string]isomorphicNodeInstance{"node.wasm": node},
		activationCtx:    activationCtx,
		activationCancel: activationCancel,
	}
	go func() { _ = parent.Activate(activationCtx, nil) }()
	<-parent.entered
	done := make(chan struct{})
	go func() {
		closeLiveIsomorphicFlow(live)
		close(done)
	}()

	select {
	case <-activationCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("flow activation context was not canceled during shutdown")
	}
	select {
	case <-node.closed:
		t.Fatal("child closed while parent activation could still invoke it")
	case <-time.After(25 * time.Millisecond):
	}
	close(parent.resume)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closeLiveIsomorphicFlow did not finish after activation drained")
	}
	select {
	case <-parent.release:
	default:
		t.Fatal("parent was not released")
	}
	select {
	case <-node.closed:
	default:
		t.Fatal("child was not closed after parent quiesced")
	}
}

func TestInstallSignedBundleRejectsActivationFailureBeforePublishing(t *testing.T) {
	installer, registry := newIsomorphicInstallerForTest(t)
	defer installer.Close()
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) {
		return metadata, nil
	}
	parent := &blockingLifecycleParent{
		entered: make(chan struct{}),
		resume:  make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("neutral activation failed"),
	}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) {
		return parent, nil
	}
	index := 0
	installer.loadIsomorphicNode = func([]byte, string, string) (isomorphicNodeInstance, error) {
		artifact := metadata.Nodes[index]
		index++
		return &fakeIsomorphicNode{id: artifact.EntryID, hash: artifact.ContentHash}, nil
	}
	stored := false
	installer.storeApplicationArtifact = func(context.Context, []byte) error {
		stored = true
		return nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := installer.InstallSignedBundle(t.Context(), bundlePath, "test")
		result <- err
	}()

	select {
	case <-parent.entered:
	case <-time.After(time.Second):
		close(parent.resume)
		t.Fatal("startup activation did not begin")
	}
	select {
	case err := <-result:
		t.Fatalf("InstallSignedBundle returned before startup activation drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if stored {
		close(parent.resume)
		<-result
		t.Fatal("APP artifact was published before startup activation succeeded")
	}
	if installer.IsomorphicFlow(metadata.ContentHash) != nil {
		close(parent.resume)
		<-result
		t.Fatal("bundle became visible before startup activation succeeded")
	}
	if entries, err := registry.List(); err != nil || len(entries) != 0 {
		close(parent.resume)
		<-result
		t.Fatalf("registry before activation success = %+v, %v", entries, err)
	}
	close(parent.resume)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "neutral activation failed") {
		t.Fatalf("InstallSignedBundle() error = %v, want activation failure", err)
	}
	if stored || installer.IsomorphicFlow(metadata.ContentHash) != nil {
		t.Fatal("activation-failed bundle was published")
	}
	if entries, err := registry.List(); err != nil || len(entries) != 0 {
		t.Fatalf("registry after activation failure = %+v, %v", entries, err)
	}
	select {
	case <-parent.release:
	default:
		t.Fatal("activation-failed parent runtime was not released")
	}
}

func TestInstallSignedBundleRollsBackEveryInstanceOnPartialFailure(t *testing.T) {
	installer, registry := newIsomorphicInstallerForTest(t)
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) { return metadata, nil }
	first := &fakeIsomorphicNode{id: "org.example.source", hash: metadata.Nodes[0].ContentHash}
	loads := 0
	installer.loadIsomorphicNode = func([]byte, string, string) (isomorphicNodeInstance, error) {
		loads++
		if loads == 2 {
			return nil, errors.New("second child failed")
		}
		return first, nil
	}
	parent := &fakeIsomorphicParent{}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) { return parent, nil }
	stored := false
	installer.storeApplicationArtifact = func(context.Context, []byte) error { stored = true; return nil }

	if _, err := installer.InstallSignedBundle(t.Context(), bundlePath, "test"); err == nil {
		t.Fatal("InstallSignedBundle() = nil error for partial child failure")
	}
	if !first.closed || !parent.closed {
		t.Fatalf("rollback closed child/parent = %v/%v, want true/true", first.closed, parent.closed)
	}
	if stored {
		t.Fatal("APP artifact stored before all instances succeeded")
	}
	if entries, err := registry.List(); err != nil || len(entries) != 0 {
		t.Fatalf("registry after rollback = %+v, %v", entries, err)
	}
	if installer.IsomorphicFlow(metadata.ContentHash) != nil {
		t.Fatal("partially installed flow became visible")
	}
}

func TestValidateIsomorphicDescriptorsRejectsHostOrLinkedSubstitution(t *testing.T) {
	nodes := neutralSignedBundleMetadata().Nodes
	validDispatch := []isomorphicDispatchDescriptor{
		{DependencyID: nodes[0].EntryID, DispatchModel: "isomorphic"},
		{DependencyID: nodes[1].EntryID, DispatchModel: "isomorphic"},
	}
	validDependencies := []isomorphicDependencyDescriptor{
		{DependencyID: nodes[0].EntryID, SHA256: nodes[0].EntryHash},
		{DependencyID: nodes[1].EntryID, SHA256: nodes[1].EntryHash},
	}
	if err := validateIsomorphicDescriptors(nodes, validDispatch, validDependencies); err != nil {
		t.Fatalf("valid descriptors rejected: %v", err)
	}
	for _, model := range []string{"host", "linked-direct", "guest-link"} {
		dispatch := append([]isomorphicDispatchDescriptor(nil), validDispatch...)
		dispatch[0].DispatchModel = model
		if err := validateIsomorphicDescriptors(nodes, dispatch, validDependencies); err == nil {
			t.Fatalf("dispatch model %q accepted for bundled child", model)
		}
	}
	tampered := append([]isomorphicDependencyDescriptor(nil), validDependencies...)
	tampered[0].SHA256 = strings.Repeat("f", 64)
	if err := validateIsomorphicDescriptors(nodes, validDispatch, tampered); err == nil {
		t.Fatal("dependency descriptor with mismatched signed-member hash accepted")
	}
}

func TestInstallSignedBundleApplicationFailureRemovesRegistryAndClosesInstances(t *testing.T) {
	installer, registry := newIsomorphicInstallerForTest(t)
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) { return metadata, nil }
	parent := &fakeIsomorphicParent{}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) { return parent, nil }
	nodes := []*fakeIsomorphicNode{
		{id: "source", hash: metadata.Nodes[0].ContentHash},
		{id: "sink", hash: metadata.Nodes[1].ContentHash},
	}
	index := 0
	installer.loadIsomorphicNode = func([]byte, string, string) (isomorphicNodeInstance, error) {
		node := nodes[index]
		index++
		return node, nil
	}
	installer.storeApplicationArtifact = func(context.Context, []byte) error { return errors.New("APP store unavailable") }

	if _, err := installer.InstallSignedBundle(t.Context(), bundlePath, "test"); err == nil {
		t.Fatal("InstallSignedBundle() = nil error for APP persistence failure")
	}
	entries, err := registry.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("registry after APP failure = %+v, %v", entries, err)
	}
	if !parent.closed || !nodes[0].closed || !nodes[1].closed {
		t.Fatalf("APP rollback closed parent/nodes = %v/%v/%v", parent.closed, nodes[0].closed, nodes[1].closed)
	}
}

func TestBootRestoresSignedBundleAndExactChildBytes(t *testing.T) {
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/neutral-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(InstalledEntry{
		ID: metadata.ContentHash, Ref: bundlePath, Enabled: true,
		Source: isomorphicSourcePrefix + "operator",
	}); err != nil {
		t.Fatal(err)
	}
	installer, err := New(Config{
		Services: &sdnservices.Services{
			Scheduler: sdncron.NewScheduler(nil, nil),
			CapReg:    modulert.NewCapabilityRegistry(),
		},
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer installer.Close()
	installer.verifyIsomorphicBundle = func(signed []byte) (*flowrt.IsomorphicBundleMetadata, error) {
		if !bytes.Equal(signed, metadata.SignedArtifact) {
			t.Fatalf("boot verifier bytes = %x", signed)
		}
		return metadata, nil
	}
	parent := &fakeIsomorphicParent{activated: make(chan struct{})}
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) { return parent, nil }
	var loaded [][]byte
	index := 0
	installer.loadIsomorphicNode = func(signed []byte, _ string, _ string) (isomorphicNodeInstance, error) {
		loaded = append(loaded, append([]byte(nil), signed...))
		node := &fakeIsomorphicNode{id: metadata.Nodes[index].EntryID, hash: metadata.Nodes[index].ContentHash}
		index++
		return node, nil
	}
	installer.storeApplicationArtifact = func(context.Context, []byte) error { return nil }

	count, err := installer.Boot(t.Context(), nil)
	if err != nil {
		t.Fatalf("Boot() error = %v", err)
	}
	select {
	case <-parent.activated:
	case <-time.After(time.Second):
		t.Fatal("restored background parent activation did not start")
	}
	if count != 1 || installer.IsomorphicFlow(metadata.ContentHash) == nil || parent.handlers == nil {
		t.Fatalf("restored count/flow/handlers = %d/%v/%v", count, installer.IsomorphicFlow(metadata.ContentHash), parent.handlers)
	}
	if len(loaded) != 2 || !bytes.Equal(loaded[0], metadata.Nodes[0].SignedArtifact) || !bytes.Equal(loaded[1], metadata.Nodes[1].SignedArtifact) {
		t.Fatalf("boot child bytes = %x", loaded)
	}
}

func TestBootRejectsSignedBundleWhoseCurrentHashDiffersFromPersistedID(t *testing.T) {
	metadata := neutralSignedBundleMetadata()
	bundlePath := t.TempDir() + "/substituted-flow.wasm"
	if err := writeTestBundle(bundlePath, metadata.SignedArtifact); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	persistedID := strings.Repeat("f", 64)
	if persistedID == metadata.ContentHash {
		persistedID = strings.Repeat("e", 64)
	}
	if err := registry.Put(InstalledEntry{
		ID: persistedID, Ref: bundlePath, Enabled: true,
		Source: isomorphicSourcePrefix + "operator",
	}); err != nil {
		t.Fatal(err)
	}
	installer, err := New(Config{
		Services: &sdnservices.Services{Scheduler: sdncron.NewScheduler(nil, nil), CapReg: modulert.NewCapabilityRegistry()},
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer installer.Close()
	installer.verifyIsomorphicBundle = func([]byte) (*flowrt.IsomorphicBundleMetadata, error) { return metadata, nil }
	loaded := false
	installer.loadIsomorphicParent = func([]byte, uint32) (isomorphicParentRuntime, error) {
		loaded = true
		return &fakeIsomorphicParent{}, nil
	}

	count, err := installer.Boot(t.Context(), nil)
	if err != nil {
		t.Fatalf("Boot() error = %v", err)
	}
	if count != 0 || loaded || installer.IsomorphicFlow(metadata.ContentHash) != nil {
		t.Fatalf("substituted bundle restored: count=%d loaded=%v flow=%v", count, loaded, installer.IsomorphicFlow(metadata.ContentHash))
	}
}

func writeTestBundle(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
