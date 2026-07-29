package flowrt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestCompiledRuntimeABINamesUseSDKPrefix(t *testing.T) {
	retiredPrefix := strings.Join([]string{"sdn", "flow"}, "_") + "_"
	for _, name := range compiledRuntimeExportNames {
		if strings.Contains(name, retiredPrefix) {
			t.Fatalf("compiled runtime export %q uses retired SDN flow prefix", name)
		}
		if !strings.HasPrefix(name, "space_data_module_runtime_") {
			t.Fatalf("compiled runtime export %q does not use SDK runtime prefix", name)
		}
	}
}

func TestUnderscoreRuntimeExportNameUsesSDKSymbol(t *testing.T) {
	got := underscoreRuntimeExportName(runtimeExportBeginInvocation)
	want := "_space_data_module_runtime_begin_node_invocation"
	if got != want {
		t.Fatalf("underscore fallback = %q, want %q", got, want)
	}
}

func TestFlowFrameDescriptorEncodingMatchesExactSDK48ByteLayout(t *testing.T) {
	buf := make([]byte, flowFrameDescriptorSize)
	fd := &FlowFrameDescriptor{
		IngressIndex:      0x01020304,
		TypeDescriptorIdx: 0x11121314,
		PortIDPointer:     0x21222324,
		Alignment:         0x31323334,
		Offset:            0x41424344,
		Size:              0x51525354,
		StreamID:          0x61626364,
		Sequence:          0x71727374,
		TraceToken:        0x8182838485868788,
		EndOfStream:       true,
		Occupied:          true,
		WireFormat:        1,
		Ownership:         2,
		Mutability:        1,
		Lifetime:          1,
	}

	encodeFrameDescriptor(buf, fd)

	for offset, want := range map[int]uint32{
		0:  fd.IngressIndex,
		4:  fd.TypeDescriptorIdx,
		8:  fd.PortIDPointer,
		12: fd.Alignment,
		16: fd.Offset,
		20: fd.Size,
		24: fd.StreamID,
		28: fd.Sequence,
	} {
		if got := binary.LittleEndian.Uint32(buf[offset : offset+4]); got != want {
			t.Fatalf("descriptor uint32 at byte %d = %#x, want %#x", offset, got, want)
		}
	}
	if got := binary.LittleEndian.Uint64(buf[32:40]); got != fd.TraceToken {
		t.Fatalf("descriptor TRACE_TOKEN = %#x, want %#x", got, fd.TraceToken)
	}
	if got, want := buf[40:48], []byte{1, 1, 1, 2, 1, 1, 0, 0}; string(got) != string(want) {
		t.Fatalf("descriptor safety tail = %v, want %v", got, want)
	}

	decoded, err := decodeFrameDescriptorBytes(buf)
	if err != nil {
		t.Fatalf("decodeFrameDescriptorBytes() error = %v", err)
	}
	if *decoded != *fd {
		t.Fatalf("decoded descriptor = %+v, want %+v", decoded, fd)
	}
}

func TestCanonicalWakeupFrameDescriptorUsesSafeInvocationMetadata(t *testing.T) {
	fd := canonicalWakeupFrameDescriptor(128, 256, 64)
	if fd.TypeDescriptorIdx != InvalidIndex {
		t.Fatalf("wakeup TYPE_DESCRIPTOR_INDEX = %d, want InvalidIndex", fd.TypeDescriptorIdx)
	}
	if fd.PortIDPointer != 128 || fd.Offset != 256 || fd.Size != 64 {
		t.Fatalf("wakeup descriptor pointers = %+v", fd)
	}
	if fd.Alignment != 1 || fd.WireFormat != 0 {
		t.Fatalf("wakeup alignment/wire format = %d/%d, want 1/canonical", fd.Alignment, fd.WireFormat)
	}
	if fd.Ownership != 0 || fd.Mutability != 0 || fd.Lifetime != 1 {
		t.Fatalf("wakeup ownership/mutability/lifetime = %d/%d/%d, want host-owned/immutable/invocation", fd.Ownership, fd.Mutability, fd.Lifetime)
	}
	if !fd.Occupied {
		t.Fatal("wakeup descriptor is not occupied")
	}
}

func TestDecodeFlowEdgeDescriptorPreservesSignedContract(t *testing.T) {
	buf := make([]byte, flowEdgeDescriptorSize)
	wants := []uint32{
		7, 101, 8, 102, 103, 104, 105, 106,
		32, 107, 1, 1, 9, 4096, 64, 32,
		0,
	}
	for index, want := range wants {
		binary.LittleEndian.PutUint32(buf[index*4:index*4+4], want)
	}

	got, err := decodeFlowEdgeDescriptorBytes(buf)
	if err != nil {
		t.Fatalf("decodeFlowEdgeDescriptorBytes() error = %v", err)
	}
	fields := []uint32{
		got.FromNode, got.FromPortPointer, got.ToNode, got.ToPortPointer,
		got.SchemaNamePointer, got.FileIdentifierPointer, got.SchemaVersionPointer,
		got.SchemaHashPointer, got.SchemaHashSize, got.RootTypeNamePointer,
		got.CanonicalFallbackAvailable, got.AlignedEligible, got.AlignedLayoutFields,
		got.AlignedByteLength, got.AlignedFixedStringLength, got.AlignedRequiredAlignment,
		got.Opaque,
	}
	for index, want := range wants {
		if fields[index] != want {
			t.Fatalf("edge descriptor field %d = %d, want %d", index, fields[index], want)
		}
	}
}

func TestCompiledRuntimeABIIncludesRoutingMetadataAndActiveGeneration(t *testing.T) {
	exports := make(map[string]bool, len(compiledRuntimeExportNames))
	for _, name := range compiledRuntimeExportNames {
		exports[name] = true
	}
	for _, name := range []string{
		runtimeExportEdgeDescriptors,
		runtimeExportRoutingState,
		runtimeExportCurrentInvocationGeneration,
	} {
		if !exports[name] {
			t.Fatalf("compiled runtime ABI omits required export %q", name)
		}
	}
}

func TestDecodeRoutingResultSurfacesRuntimeRejection(t *testing.T) {
	if routed, err := decodeRoutingResult(3); err != nil || routed != 3 {
		t.Fatalf("decodeRoutingResult(3) = (%d, %v), want (3, nil)", routed, err)
	}
	if _, err := decodeRoutingResult(-36); err == nil || !strings.Contains(err.Error(), "-36") {
		t.Fatalf("decodeRoutingResult(-36) error = %v, want surfaced rejection code", err)
	}
}

func TestValidateInvocationHandlerResultFailsClosed(t *testing.T) {
	handlerErr := errors.New("provider fetch failed")
	if _, err := validateInvocationHandlerResult(7, "provider", "pull", nil, handlerErr); err == nil || !errors.Is(err, handlerErr) {
		t.Fatalf("handler error = %v, want wrapped provider failure", err)
	}
	if _, err := validateInvocationHandlerResult(7, "provider", "pull", nil, nil); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil handler result error = %v, want fail-closed rejection", err)
	}
	if _, err := validateInvocationHandlerResult(7, "provider", "pull", &InvocationResult{StatusCode: -9}, nil); err == nil || !strings.Contains(err.Error(), "status -9") {
		t.Fatalf("nonzero handler status error = %v, want surfaced status", err)
	}
	want := &InvocationResult{StatusCode: 0, BacklogRemaining: 3}
	if got, err := validateInvocationHandlerResult(7, "provider", "pull", want, nil); err != nil || got != want {
		t.Fatalf("successful handler result = (%+v, %v), want original result", got, err)
	}
}

func TestExecuteRuntimeExportSelectsFallbackBeforeExecution(t *testing.T) {
	var calls []string
	result, err := executeRuntimeExport(func(name string) bool {
		return name == underscoreRuntimeExportName(runtimeExportBeginInvocation)
	}, func(name string, _ ...interface{}) ([]interface{}, error) {
		calls = append(calls, name)
		return []interface{}{int32(4)}, nil
	}, runtimeExportBeginInvocation, int32(7), int32(64))
	if err != nil || len(result) != 1 {
		t.Fatalf("executeRuntimeExport() = (%v, %v), want fallback success", result, err)
	}
	want := underscoreRuntimeExportName(runtimeExportBeginInvocation)
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("executeRuntimeExport() calls = %q, want only %q", calls, want)
	}
}

func TestExecuteRuntimeExportNeverRetriesTrappingPrimary(t *testing.T) {
	primary := errors.New("primary mutated then trapped")
	var calls []string
	_, err := executeRuntimeExport(func(string) bool { return true }, func(name string, _ ...interface{}) ([]interface{}, error) {
		calls = append(calls, name)
		if name == runtimeExportBeginInvocation {
			return nil, primary
		}
		return []interface{}{int32(4)}, nil
	}, runtimeExportBeginInvocation, int32(7), int32(64))
	if !errors.Is(err, primary) {
		t.Fatalf("executeRuntimeExport() error = %v, want primary trap", err)
	}
	if len(calls) != 1 || calls[0] != runtimeExportBeginInvocation {
		t.Fatalf("trapping export calls = %q, want primary exactly once", calls)
	}
}

func TestResolveDrainHandlerRejectsMissingIsomorphicHandler(t *testing.T) {
	info := flowNodeInfo{
		PluginID:      "org.example.provider",
		MethodID:      "pull",
		DependencyID:  "provider",
		NodeID:        "provider",
		DispatchModel: "isomorphic",
	}
	if _, direct, err := resolveDrainHandler(HandlerMap{}, info); err == nil || direct || !strings.Contains(err.Error(), "no handler") {
		t.Fatalf("missing isomorphic handler = direct:%v err:%v, want failure", direct, err)
	}
	if handler, direct, err := resolveDrainHandler(HandlerMap{}, flowNodeInfo{DispatchModel: "linked-direct"}); err != nil || handler != nil || !direct {
		t.Fatalf("linked-direct resolution = handler:%v direct:%v err:%v", handler, direct, err)
	}
}

func TestSelectOutputEdgeRequiresExactSignedIdentityAndLayout(t *testing.T) {
	hash := []byte{0xde, 0xad, 0xbe, 0xef}
	runtime := &FlowRuntime{edgeInfo: []flowEdgeInfo{{
		Index:                      3,
		FromPort:                   "ephemeris",
		SchemaName:                 "Ephemeris.fbs",
		FileIdentifier:             "EPHM",
		SchemaVersion:              "3.2.1",
		SchemaHash:                 hash,
		RootTypeName:               "Ephemeris",
		CanonicalFallbackAvailable: true,
		AlignedEligible:            true,
		Descriptor: FlowEdgeDescriptor{
			FromNode:                 2,
			AlignedByteLength:        4096,
			AlignedFixedStringLength: 64,
			AlignedRequiredAlignment: 32,
		},
	}}}

	canonical := FrameOutput{
		PortID:         "ephemeris",
		SchemaName:     "Ephemeris.fbs",
		FileIdentifier: "EPHM",
		SchemaVersion:  "3.2.1",
		SchemaHash:     append([]byte(nil), hash...),
		RootTypeName:   "Ephemeris",
	}
	edge, err := runtime.selectOutputEdge(2, canonical)
	if err != nil || edge == nil || edge.Index != 3 {
		t.Fatalf("canonical edge = (%+v, %v), want signed edge 3", edge, err)
	}

	aligned := canonical
	aligned.WireFormat = 1
	aligned.Bytes = make([]byte, 4096)
	aligned.ByteLength = 4096
	aligned.FixedStringLength = 64
	aligned.RequiredAlignment = 32
	if edge, err := runtime.selectOutputEdge(2, aligned); err != nil || edge == nil || edge.Index != 3 {
		t.Fatalf("aligned edge = (%+v, %v), want signed edge 3", edge, err)
	}

	mismatch := canonical
	mismatch.SchemaHash = []byte{0x00}
	if _, err := runtime.selectOutputEdge(2, mismatch); err == nil {
		t.Fatal("selectOutputEdge() accepted mismatched schema hash")
	}
	badLayout := aligned
	badLayout.RequiredAlignment = 16
	if _, err := runtime.selectOutputEdge(2, badLayout); err == nil {
		t.Fatal("selectOutputEdge() accepted mismatched aligned layout")
	}
}

func TestBindInputFrameTypeRequiresTargetBoundSignedDescriptorForCanonicalFrames(t *testing.T) {
	hash := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	edges := []flowEdgeInfo{{
		Index:                      0,
		ToPort:                     "start",
		SchemaName:                 "TriggerStart.fbs",
		FileIdentifier:             "TRG1",
		SchemaVersion:              "4.2.0",
		SchemaHash:                 hash,
		RootTypeName:               "TriggerStart",
		CanonicalFallbackAvailable: true,
		AlignedEligible:            true,
		Descriptor: FlowEdgeDescriptor{
			ToNode:                   3,
			AlignedByteLength:        128,
			AlignedFixedStringLength: 16,
			AlignedRequiredAlignment: 8,
		},
	}}

	frame := FrameData{PortID: "start"}
	fd := &FlowFrameDescriptor{TypeDescriptorIdx: 0, WireFormat: 0}
	if err := bindInputFrameType(&frame, fd, 3, edges); err != nil {
		t.Fatalf("bindInputFrameType(valid canonical) error = %v", err)
	}
	if frame.SchemaName != "TriggerStart.fbs" || frame.FileIdentifier != "TRG1" ||
		frame.SchemaVersion != "4.2.0" || frame.RootTypeName != "TriggerStart" ||
		!bytes.Equal(frame.SchemaHash, hash) {
		t.Fatalf("bound canonical identity = %+v", frame)
	}

	missing := FrameData{PortID: "start"}
	fd.TypeDescriptorIdx = InvalidIndex
	if err := bindInputFrameType(&missing, fd, 3, edges); err == nil ||
		!strings.Contains(err.Error(), "no signed type descriptor") {
		t.Fatalf("bindInputFrameType(invalid canonical descriptor) error = %v", err)
	}
}

func TestOutputFrameLeaseReleasesEveryTransientAllocationAcrossRepeatedLargeExchanges(t *testing.T) {
	const exchanges = 4096
	allocations := []outputFrameAllocation{
		{ptr: 0x1000, size: 64 * flowFrameDescriptorSize},
		{ptr: 0x2000, size: 2*1024*1024 + 31},
		{ptr: 0x3000, size: 33},
	}
	var released []outputFrameAllocation
	for exchange := 0; exchange < exchanges; exchange++ {
		lease := &outputFrameLease{deallocate: func(ptr, size uint32) {
			released = append(released, outputFrameAllocation{ptr: ptr, size: size})
		}}
		for _, allocation := range allocations {
			lease.track(allocation.ptr, allocation.size)
		}
		lease.Release()
		lease.Release() // idempotent on every error/defer path
	}
	if got, want := len(released), exchanges*len(allocations); got != want {
		t.Fatalf("released allocation count = %d, want %d", got, want)
	}
	for exchange := 0; exchange < exchanges; exchange++ {
		base := exchange * len(allocations)
		wantReverse := []outputFrameAllocation{allocations[2], allocations[1], allocations[0]}
		if !bytes.Equal(encodeAllocationsForTest(released[base:base+3]), encodeAllocationsForTest(wantReverse)) {
			t.Fatalf("exchange %d release order = %+v, want %+v", exchange, released[base:base+3], wantReverse)
		}
	}
}

func encodeAllocationsForTest(allocations []outputFrameAllocation) []byte {
	encoded := make([]byte, len(allocations)*8)
	for index, allocation := range allocations {
		binary.LittleEndian.PutUint32(encoded[index*8:index*8+4], allocation.ptr)
		binary.LittleEndian.PutUint32(encoded[index*8+4:index*8+8], allocation.size)
	}
	return encoded
}
