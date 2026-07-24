package modulert

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
)

func TestModuleAdmissionRunsBeforeGuestInitialization(t *testing.T) {
	denied := errors.New("capability denied")
	initialized := false
	err := runModuleInitializationAfterAdmission(
		func() error { return denied },
		func() error {
			initialized = true
			return nil
		},
	)
	if !errors.Is(err, denied) {
		t.Fatalf("runModuleInitializationAfterAdmission() error = %v, want %v", err, denied)
	}
	if initialized {
		t.Fatal("guest initialization ran before capability admission")
	}

	admitted := false
	initialized = false
	err = runModuleInitializationAfterAdmission(
		func() error {
			admitted = true
			return nil
		},
		func() error {
			if !admitted {
				t.Fatal("guest initialization ran before admission completed")
			}
			initialized = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runModuleInitializationAfterAdmission() admitted error = %v", err)
	}
	if !initialized {
		t.Fatal("admitted module did not initialize")
	}
}

func TestHostcallImportModuleUsesSDKName(t *testing.T) {
	if HostcallImportModule != "space_data_module_host" {
		t.Fatalf("HostcallImportModule = %q, want space_data_module_host", HostcallImportModule)
	}
}

func TestBoundIdentityUsesOuterBundleAndEntryForIsomorphicInstances(t *testing.T) {
	childHash := strings.Repeat("c", 64)
	outerHash := strings.Repeat("a", 64)
	module := &Module{
		contentHash:          childHash,
		manifest:             &Manifest{PluginID: "org.example.reusable"},
		instanceArtifactHash: outerHash,
		instanceNodeID:       "entry-" + strings.Repeat("e", 64),
	}

	artifactHash, nodeID := module.BoundIdentity()
	if artifactHash != outerHash || nodeID != "entry-"+strings.Repeat("e", 64) {
		t.Fatalf("BoundIdentity() = (%q,%q), want outer bundle + entry identity", artifactHash, nodeID)
	}

	module.instanceArtifactHash = ""
	module.instanceNodeID = ""
	artifactHash, nodeID = module.BoundIdentity()
	if artifactHash != childHash || nodeID != "org.example.reusable" {
		t.Fatalf("legacy BoundIdentity() = (%q,%q), want child content hash + plugin id", artifactHash, nodeID)
	}
}

func TestResolveModuleInstanceIdentityIsStableSafeAndEntrySpecific(t *testing.T) {
	outer := strings.Repeat("A", 64)
	artifactHash, first, err := resolveModuleInstanceIdentity(outer, "nodes/store.wasm")
	if err != nil {
		t.Fatalf("resolve first identity: %v", err)
	}
	_, second, err := resolveModuleInstanceIdentity(outer, "copies/store.wasm")
	if err != nil {
		t.Fatalf("resolve second identity: %v", err)
	}
	if artifactHash != strings.ToLower(outer) || first == second {
		t.Fatalf("resolved identities = (%q,%q,%q), want normalized outer hash and distinct entries", artifactHash, first, second)
	}
	if !strings.HasPrefix(first, "entry-") || len(first) != len("entry-")+64 {
		t.Fatalf("safe entry identity = %q", first)
	}
	if _, _, err := resolveModuleInstanceIdentity("bad", "nodes/store.wasm"); err == nil {
		t.Fatal("invalid outer hash was accepted")
	}
	if _, _, err := resolveModuleInstanceIdentity(outer, ""); err == nil {
		t.Fatal("empty entry ID was accepted")
	}
}

func TestModuleRuntimeDetectsStandardWASIThreadsContract(t *testing.T) {
	fixture, err := os.ReadFile("../wasmrt/testdata/wasithreads_fixture.wasm")
	if err != nil {
		t.Fatalf("read wasi-threads fixture: %v", err)
	}
	if !moduleDeclaresWASIThreads(fixture) {
		t.Fatal("standard wasi-threads fixture was not detected")
	}
	if moduleDeclaresWASIThreads([]byte("not wasm")) {
		t.Fatal("malformed bytes were detected as wasi-threads")
	}
}

func TestResolveModuleMemoryPagesHonorsBoundedInstallerCeiling(t *testing.T) {
	if got, err := resolveModuleMemoryPages(0); err != nil || got != defaultModuleMemoryPages {
		t.Fatalf("resolveModuleMemoryPages(0) = (%d, %v), want default %d", got, err, defaultModuleMemoryPages)
	}
	if got, err := resolveModuleMemoryPages(maxModuleMemoryPages); err != nil || got != maxModuleMemoryPages {
		t.Fatalf("resolveModuleMemoryPages(max) = (%d, %v), want %d", got, err, maxModuleMemoryPages)
	}
	if _, err := resolveModuleMemoryPages(maxModuleMemoryPages + 1); err == nil {
		t.Fatal("resolveModuleMemoryPages() accepted an unbounded child ceiling")
	}
}

func TestHostBridgeLateInitialClearCannotStealPublishedResponse(t *testing.T) {
	hb := NewHostBridge(nil, nil)
	first := []byte("first-worker-response")
	second := []byte("second-worker-response")
	hb.beginResponse(17, first)

	// Every SDK worker clears stale state immediately before call. A worker
	// that starts late must not clear an already-published exchange belonging
	// to a worker whose call has returned but has not read its response yet.
	hb.clearResponse()

	secondPublished := make(chan struct{})
	go func() {
		hb.beginResponse(23, second)
		close(secondPublished)
	}()
	select {
	case <-secondPublished:
		t.Fatal("late initial clear released the first worker's response slot")
	case <-time.After(25 * time.Millisecond):
	}

	if got := hb.responseStatus(); got != 17 {
		t.Fatalf("first response status = %d, want 17", got)
	}
	if got := hb.responseBytes(uint32(len(first))); !bytes.Equal(got, first) {
		t.Fatalf("first response bytes = %q, want %q", got, first)
	}
	hb.clearResponse()

	select {
	case <-secondPublished:
	case <-time.After(time.Second):
		t.Fatal("second completed network call did not publish after first response was consumed")
	}
	if got := hb.responseStatus(); got != 23 {
		t.Fatalf("second response status = %d, want 23", got)
	}
	if got := hb.responseBytes(uint32(len(second))); !bytes.Equal(got, second) {
		t.Fatalf("second response bytes = %q, want %q", got, second)
	}
	hb.clearResponse()
}

func TestHostBridgeResponseExchangeIsolates64ConcurrentRequests(t *testing.T) {
	hb := NewHostBridge(nil, nil)

	const requests = 64
	start := make(chan struct{})
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			want := []byte(fmt.Sprintf("opaque-response-%02d", i))
			wantStatus := int32(i + 1)
			hb.beginResponse(wantStatus, want)
			defer hb.clearResponse()

			// Force scheduling between the same response_len/read/status steps
			// used by the guest ABI. Another completed request must not be able
			// to replace this exchange until clear_response releases it.
			runtime.Gosched()
			if got := hb.responseLength(); got != int32(len(want)) {
				errs <- fmt.Errorf("request %d length = %d, want %d", i, got, len(want))
				return
			}
			runtime.Gosched()
			if got := hb.responseBytes(uint32(len(want))); !bytes.Equal(got, want) {
				errs <- fmt.Errorf("request %d bytes = %q, want %q", i, got, want)
				return
			}
			runtime.Gosched()
			if got := hb.responseStatus(); got != wantStatus {
				errs <- fmt.Errorf("request %d status = %d, want %d", i, got, wantStatus)
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestInvokeFrameSetPreservesEveryOutputPortAndExactBytes(t *testing.T) {
	arena := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0x00, 0x7f}
	response := &pluginInvokeResponse{
		StatusCode: 0,
		OutputFrames: []pluginInvokeFrame{
			{PortID: "first", Offset: 0, Size: 4, Alignment: 1},
			{PortID: "second", Offset: 4, Size: 3, Alignment: 1},
		},
		PayloadArena: arena,
	}

	got, err := invokeFrameSetFromResponse(response)
	if err != nil {
		t.Fatalf("invokeFrameSetFromResponse() error = %v", err)
	}
	if got.StatusCode != 0 || len(got.Outputs) != 2 {
		t.Fatalf("frame set = %+v, want two successful outputs", got)
	}
	if got.Outputs[0].PortID != "first" || !bytes.Equal(got.Outputs[0].Payload, arena[:4]) {
		t.Fatalf("first output = %+v", got.Outputs[0])
	}
	if got.Outputs[1].PortID != "second" || !bytes.Equal(got.Outputs[1].Payload, arena[4:]) {
		t.Fatalf("second output = %+v", got.Outputs[1])
	}

	// Child results cross an instance boundary. They must not alias the
	// child's response arena after plugin_invoke_stream returns.
	arena[0] = 0xfe
	if got.Outputs[0].Payload[0] != 0x00 {
		t.Fatalf("first output aliases child response arena: %x", got.Outputs[0].Payload)
	}
}

func TestInvokeFrameSetPreservesYieldContinuation(t *testing.T) {
	got, err := invokeFrameSetFromResponse(&pluginInvokeResponse{
		StatusCode:       0,
		Yielded:          true,
		BacklogRemaining: 3817,
	})
	if err != nil {
		t.Fatalf("invokeFrameSetFromResponse() error = %v", err)
	}
	if !got.Yielded || got.BacklogRemaining != 3817 {
		t.Fatalf("continuation = yielded:%v backlog:%d, want true/3817", got.Yielded, got.BacklogRemaining)
	}
}

func TestInvokeFrameSetCopiesCompleteAlignedContractIntoHostOwnership(t *testing.T) {
	hash := []byte{0xca, 0xfe, 0xba, 0xbe}
	response := &pluginInvokeResponse{
		OutputFrames: []pluginInvokeFrame{{
			PortID:            "ephemeris",
			Offset:            0,
			Size:              32,
			Alignment:         32,
			WireFormat:        payloadWireFormatAlignedBinary,
			SchemaName:        "Ephemeris.fbs",
			FileIdentifier:    "EPHM",
			SchemaVersion:     "3.2.1",
			SchemaHash:        hash,
			RootTypeName:      "Ephemeris",
			FixedStringLength: 64,
			ByteLength:        32,
			RequiredAlignment: 32,
			Ownership:         byte(piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]),
			Mutability:        byte(piv.EnumValuesbufferMutability["IMMUTABLE"]),
			FrameID:           0x8877665544332211,
		}},
		PayloadArena: make([]byte, 32),
	}

	got, err := invokeFrameSetFromResponse(response)
	if err != nil {
		t.Fatalf("invokeFrameSetFromResponse() error = %v", err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("output count = %d, want 1", len(got.Outputs))
	}
	frame := got.Outputs[0]
	if frame.PortID != "ephemeris" || frame.Alignment != 32 ||
		frame.WireFormat != payloadWireFormatAlignedBinary || frame.SchemaName != "Ephemeris.fbs" ||
		frame.FileIdentifier != "EPHM" || frame.SchemaVersion != "3.2.1" ||
		!bytes.Equal(frame.SchemaHash, hash) || frame.RootTypeName != "Ephemeris" ||
		frame.FixedStringLength != 64 || frame.ByteLength != 32 || frame.RequiredAlignment != 32 ||
		frame.FrameID != 0x8877665544332211 {
		t.Fatalf("copied output metadata = %+v", frame)
	}
	if frame.Ownership != byte(piv.EnumValuesbufferOwnership["HOST_OWNED"]) ||
		frame.Mutability != byte(piv.EnumValuesbufferMutability["IMMUTABLE"]) {
		t.Fatalf("copied output ownership/mutability = %d/%d, want host-owned/immutable", frame.Ownership, frame.Mutability)
	}
}

func TestInvokeFrameSetRejectsMalformedAlignedContract(t *testing.T) {
	_, err := invokeFrameSetFromResponse(&pluginInvokeResponse{
		OutputFrames: []pluginInvokeFrame{{
			PortID:            "bad",
			Offset:            1,
			Size:              16,
			Alignment:         16,
			WireFormat:        payloadWireFormatAlignedBinary,
			ByteLength:        16,
			RequiredAlignment: 16,
		}},
		PayloadArena: make([]byte, 32),
	})
	if err == nil {
		t.Fatal("invokeFrameSetFromResponse() accepted a misaligned arena offset")
	}
}

func TestInvokeFrameSetRejectsOutputOutsideChildArena(t *testing.T) {
	_, err := invokeFrameSetFromResponse(&pluginInvokeResponse{
		OutputFrames: []pluginInvokeFrame{{PortID: "bad", Offset: 2, Size: 4}},
		PayloadArena: []byte{1, 2, 3},
	})
	if err == nil {
		t.Fatal("invokeFrameSetFromResponse() = nil error for out-of-bounds output")
	}
}

func TestEncodeFrameSetRequestAllowsDeclaredNoBodyCallback(t *testing.T) {
	encoded, err := encodeFrameSetInvokeRequest("on_wakeup", nil)
	if err != nil {
		t.Fatalf("encodeFrameSetInvokeRequest() error = %v", err)
	}
	if !piv.PIVBufferHasIdentifier(encoded) {
		t.Fatalf("encoded request has no $PIV identifier: %x", encoded)
	}
	root := piv.GetRootAsPIV(encoded, 0)
	request := root.Request(nil)
	if request == nil || string(request.MethodId()) != "on_wakeup" || request.InputsLength() != 0 {
		t.Fatalf("encoded no-body request = %+v", request)
	}
}
