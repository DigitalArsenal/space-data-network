package flowrt

import (
	"bytes"
	"encoding/binary"
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

func TestDecodeFlowEdgeDescriptorPreservesSigned64ByteContract(t *testing.T) {
	buf := make([]byte, flowEdgeDescriptorSize)
	wants := []uint32{
		7, 101, 8, 102, 103, 104, 105, 106,
		32, 107, 1, 1, 9, 4096, 64, 32,
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
