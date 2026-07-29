// Package flowrt provides a Go host for compiled SDN runtime WASM artifacts.
package flowrt

import (
	"encoding/binary"
	"fmt"

	"github.com/ipfs/kubo/sdn/wasmrt"
)

// InvalidIndex is the sentinel value signaling "no more ready nodes" or "no descriptor".
const InvalidIndex = 0xFFFFFFFF

const (
	runtimeExportNodeDescriptorCount         = "space_data_module_runtime_get_node_descriptor_count"
	runtimeExportEdgeDescriptorCount         = "space_data_module_runtime_get_edge_descriptor_count"
	runtimeExportTriggerDescriptorCount      = "space_data_module_runtime_get_trigger_descriptor_count"
	runtimeExportDependencyDescriptorCount   = "space_data_module_runtime_get_dependency_descriptor_count"
	runtimeExportResetState                  = "space_data_module_runtime_reset_state"
	runtimeExportReadyNode                   = "space_data_module_runtime_get_ready_node_index"
	runtimeExportBeginInvocation             = "space_data_module_runtime_begin_node_invocation"
	runtimeExportCurrentInvocation           = "space_data_module_runtime_get_current_invocation_descriptor"
	runtimeExportApplyInvocationResult       = "space_data_module_runtime_apply_node_invocation_result"
	runtimeExportCompleteInvocation          = "space_data_module_runtime_complete_node_invocation"
	runtimeExportEnqueueTriggerFrames        = "space_data_module_runtime_enqueue_trigger_frames"
	runtimeExportEnqueueTriggerFrame         = "space_data_module_runtime_enqueue_trigger_frame"
	runtimeExportEdgeDescriptors             = "space_data_module_runtime_get_edge_descriptors"
	runtimeExportDescriptorABIGeneration     = "space_data_module_runtime_get_descriptor_abi_generation"
	runtimeExportRoutingState                = "space_data_module_runtime_get_routing_state"
	runtimeExportCurrentInvocationGeneration = "space_data_module_runtime_get_current_invocation_generation"
	runtimeExportNodeDispatchDescriptors     = "space_data_module_runtime_get_node_dispatch_descriptors"
	runtimeExportDependencyDescriptors       = "space_data_module_runtime_get_dependency_descriptors"
	runtimeExportNodeStates                  = "space_data_module_runtime_get_node_states"
	runtimeExportIngressStates               = "space_data_module_runtime_get_ingress_states"
	runtimeExportDispatchCurrentInvocation   = "space_data_module_runtime_dispatch_current_invocation_direct"
	// OPTIONAL (loop C.5c): in-wasm scheduler loop running every ready
	// linked-direct node inside one call. Hosts probe for it and fall back
	// to per-node driving when the artifact predates it.
	runtimeExportDrainLinked = "space_data_module_runtime_drain_linked"
)

var compiledRuntimeExportNames = []string{
	runtimeExportNodeDescriptorCount,
	runtimeExportEdgeDescriptorCount,
	runtimeExportTriggerDescriptorCount,
	runtimeExportDependencyDescriptorCount,
	runtimeExportResetState,
	runtimeExportReadyNode,
	runtimeExportBeginInvocation,
	runtimeExportCurrentInvocation,
	runtimeExportApplyInvocationResult,
	runtimeExportCompleteInvocation,
	runtimeExportEnqueueTriggerFrames,
	runtimeExportEnqueueTriggerFrame,
	runtimeExportEdgeDescriptors,
	runtimeExportRoutingState,
	runtimeExportCurrentInvocationGeneration,
	runtimeExportNodeDispatchDescriptors,
	runtimeExportDependencyDescriptors,
	runtimeExportNodeStates,
	runtimeExportIngressStates,
	runtimeExportDispatchCurrentInvocation,
}

func underscoreRuntimeExportName(name string) string {
	return "_" + name
}

// ---------------------------------------------------------------------------
// FlowFrameDescriptor — 48 bytes, alignment 8
// ---------------------------------------------------------------------------

type FlowFrameDescriptor struct {
	IngressIndex      uint32
	TypeDescriptorIdx uint32
	PortIDPointer     uint32
	Alignment         uint32
	Offset            uint32
	Size              uint32
	StreamID          uint32
	Sequence          uint32
	TraceToken        uint64
	EndOfStream       bool
	Occupied          bool
	WireFormat        byte
	Ownership         byte
	Mutability        byte
	Lifetime          byte
}

const flowFrameDescriptorSize = 48

func readFrameDescriptor(mod *wasmrt.Module, ptr uint32) (*FlowFrameDescriptor, error) {
	buf, err := mod.ReadMemory(ptr, flowFrameDescriptorSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowFrameDescriptor at %d: %w", ptr, err)
	}
	return decodeFrameDescriptorBytes(buf)
}

func decodeFrameDescriptorBytes(buf []byte) (*FlowFrameDescriptor, error) {
	if len(buf) != flowFrameDescriptorSize {
		return nil, fmt.Errorf("decode FlowFrameDescriptor: got %d bytes, want %d", len(buf), flowFrameDescriptorSize)
	}
	return &FlowFrameDescriptor{
		IngressIndex:      binary.LittleEndian.Uint32(buf[0:4]),
		TypeDescriptorIdx: binary.LittleEndian.Uint32(buf[4:8]),
		PortIDPointer:     binary.LittleEndian.Uint32(buf[8:12]),
		Alignment:         binary.LittleEndian.Uint32(buf[12:16]),
		Offset:            binary.LittleEndian.Uint32(buf[16:20]),
		Size:              binary.LittleEndian.Uint32(buf[20:24]),
		StreamID:          binary.LittleEndian.Uint32(buf[24:28]),
		Sequence:          binary.LittleEndian.Uint32(buf[28:32]),
		TraceToken:        binary.LittleEndian.Uint64(buf[32:40]),
		EndOfStream:       buf[40] != 0,
		Occupied:          buf[41] != 0,
		WireFormat:        buf[42],
		Ownership:         buf[43],
		Mutability:        buf[44],
		Lifetime:          buf[45],
	}, nil
}

// encodeFrameDescriptor serializes a frame descriptor into buf (which must
// be at least flowFrameDescriptorSize bytes).
func encodeFrameDescriptor(buf []byte, fd *FlowFrameDescriptor) {
	binary.LittleEndian.PutUint32(buf[0:4], fd.IngressIndex)
	binary.LittleEndian.PutUint32(buf[4:8], fd.TypeDescriptorIdx)
	binary.LittleEndian.PutUint32(buf[8:12], fd.PortIDPointer)
	binary.LittleEndian.PutUint32(buf[12:16], fd.Alignment)
	binary.LittleEndian.PutUint32(buf[16:20], fd.Offset)
	binary.LittleEndian.PutUint32(buf[20:24], fd.Size)
	binary.LittleEndian.PutUint32(buf[24:28], fd.StreamID)
	binary.LittleEndian.PutUint32(buf[28:32], fd.Sequence)
	binary.LittleEndian.PutUint64(buf[32:40], fd.TraceToken)
	buf[40] = 0
	if fd.EndOfStream {
		buf[40] = 1
	}
	buf[41] = 0
	if fd.Occupied {
		buf[41] = 1
	}
	buf[42] = fd.WireFormat
	buf[43] = fd.Ownership
	buf[44] = fd.Mutability
	buf[45] = fd.Lifetime
	buf[46] = 0
	buf[47] = 0
}

func writeFrameDescriptor(mod *wasmrt.Module, ptr uint32, fd *FlowFrameDescriptor) error {
	buf := make([]byte, flowFrameDescriptorSize)
	encodeFrameDescriptor(buf, fd)
	return mod.WriteMemory(ptr, buf)
}

// ---------------------------------------------------------------------------
// FlowEdge — 68 bytes, alignment 4 (wasm32 pointer fields), generation 2.
// It was 64 bytes in generation 1; the trailing Opaque u32 at offset 64 is the
// difference. This comment said 64 long after the struct moved — the stale
// number is why the stride bump read as safe.
// ---------------------------------------------------------------------------

// FlowEdgeDescriptor is the exact signed edge/type table compiled into the
// parent flow runtime. Pointer fields address immutable strings/bytes in that
// runtime's linear memory; the host copies those values before dispatch.
type FlowEdgeDescriptor struct {
	FromNode                   uint32
	FromPortPointer            uint32
	ToNode                     uint32
	ToPortPointer              uint32
	SchemaNamePointer          uint32
	FileIdentifierPointer      uint32
	SchemaVersionPointer       uint32
	SchemaHashPointer          uint32
	SchemaHashSize             uint32
	RootTypeNamePointer        uint32
	CanonicalFallbackAvailable uint32
	AlignedEligible            uint32
	AlignedLayoutFields        uint32
	AlignedByteLength          uint32
	AlignedFixedStringLength   uint32
	AlignedRequiredAlignment   uint32
	// Opaque marks a byte edge: no SDS identity, no aligned layout (SDS $PLG
	// 1.0.13 / module-sdk descriptor ABI generation 2). It is the reason this
	// struct grew from 64 to 68 bytes; a host reading the old stride resolves
	// every edge past the first at the wrong offset and believes the result.
	Opaque uint32
}

const flowEdgeDescriptorSize = 68

// flowEdgeDescriptorABIGeneration is the descriptor-table generation this
// package's 68-byte stride belongs to. Generation 1 was 64 bytes with no
// Opaque field.
//
// A stride change is INVISIBLE from outside: same table pointer, same count,
// every field past the first edge read at the wrong offset — and believed. So
// the artifact states its generation and the host asserts it before reading a
// single edge. A MISSING export means generation 1; absence is never
// permission. The SDK's JS host already refuses a mismatch
// (flowRuntimeHost.js), and a host that reads garbage where the browser fails
// closed is a tri-runtime divergence of exactly the class that produced the
// alignment=0 bug.
const flowEdgeDescriptorABIGeneration = 2

func decodeFlowEdgeDescriptorBytes(buf []byte) (*FlowEdgeDescriptor, error) {
	if len(buf) != flowEdgeDescriptorSize {
		return nil, fmt.Errorf("decode FlowEdge: got %d bytes, want %d", len(buf), flowEdgeDescriptorSize)
	}
	fields := make([]uint32, flowEdgeDescriptorSize/4)
	for index := range fields {
		fields[index] = binary.LittleEndian.Uint32(buf[index*4 : index*4+4])
	}
	return &FlowEdgeDescriptor{
		FromNode:                   fields[0],
		FromPortPointer:            fields[1],
		ToNode:                     fields[2],
		ToPortPointer:              fields[3],
		SchemaNamePointer:          fields[4],
		FileIdentifierPointer:      fields[5],
		SchemaVersionPointer:       fields[6],
		SchemaHashPointer:          fields[7],
		SchemaHashSize:             fields[8],
		RootTypeNamePointer:        fields[9],
		CanonicalFallbackAvailable: fields[10],
		AlignedEligible:            fields[11],
		AlignedLayoutFields:        fields[12],
		AlignedByteLength:          fields[13],
		AlignedFixedStringLength:   fields[14],
		AlignedRequiredAlignment:   fields[15],
		Opaque:                     fields[16],
	}, nil
}

func readFlowEdgeDescriptor(mod *wasmrt.Module, ptr uint32) (*FlowEdgeDescriptor, error) {
	buf, err := mod.ReadMemory(ptr, flowEdgeDescriptorSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowEdge at %d: %w", ptr, err)
	}
	return decodeFlowEdgeDescriptorBytes(buf)
}

// decodeRoutingResult preserves the signed SDK result. Negative values are
// descriptor/routing rejection codes and must never be reinterpreted as a
// large successful uint32 count.
func decodeRoutingResult(result int32) (uint32, error) {
	if result < 0 {
		return 0, fmt.Errorf("flow runtime rejected invocation output with code %d", result)
	}
	return uint32(result), nil
}

// ---------------------------------------------------------------------------
// FlowInvocationDescriptor — 24 bytes, alignment 4
// ---------------------------------------------------------------------------

type FlowInvocationDescriptor struct {
	NodeIndex             uint32
	DispatchDescriptorIdx uint32
	PluginIDPointer       uint32
	MethodIDPointer       uint32
	FramesPointer         uint32
	FrameCount            uint32
}

const flowInvocationDescriptorSize = 24

func readInvocationDescriptor(mod *wasmrt.Module, ptr uint32) (*FlowInvocationDescriptor, error) {
	buf, err := mod.ReadMemory(ptr, flowInvocationDescriptorSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowInvocationDescriptor at %d: %w", ptr, err)
	}
	return &FlowInvocationDescriptor{
		NodeIndex:             binary.LittleEndian.Uint32(buf[0:4]),
		DispatchDescriptorIdx: binary.LittleEndian.Uint32(buf[4:8]),
		PluginIDPointer:       binary.LittleEndian.Uint32(buf[8:12]),
		MethodIDPointer:       binary.LittleEndian.Uint32(buf[12:16]),
		FramesPointer:         binary.LittleEndian.Uint32(buf[16:20]),
		FrameCount:            binary.LittleEndian.Uint32(buf[20:24]),
	}, nil
}

// ---------------------------------------------------------------------------
// FlowNodeDispatchDescriptor — 60 bytes, alignment 4
// ---------------------------------------------------------------------------

type FlowNodeDispatchDescriptor struct {
	NodeIDPointer              uint32
	NodeIndex                  uint32
	DependencyIDPointer        uint32
	DependencyIndex            uint32
	PluginIDPointer            uint32
	MethodIDPointer            uint32
	DispatchModelPointer       uint32
	EntrypointPointer          uint32
	ManifestBytesSymbolPointer uint32
	ManifestSizeSymbolPointer  uint32
	InitSymbolPointer          uint32
	DestroySymbolPointer       uint32
	MallocSymbolPointer        uint32
	FreeSymbolPointer          uint32
	StreamInvokeSymbolPointer  uint32
}

const flowNodeDispatchDescriptorSize = 60

func readNodeDispatchDescriptor(mod *wasmrt.Module, ptr uint32) (*FlowNodeDispatchDescriptor, error) {
	buf, err := mod.ReadMemory(ptr, flowNodeDispatchDescriptorSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowNodeDispatchDescriptor at %d: %w", ptr, err)
	}
	d := &FlowNodeDispatchDescriptor{}
	for i, field := range []*uint32{
		&d.NodeIDPointer, &d.NodeIndex, &d.DependencyIDPointer, &d.DependencyIndex,
		&d.PluginIDPointer, &d.MethodIDPointer, &d.DispatchModelPointer, &d.EntrypointPointer,
		&d.ManifestBytesSymbolPointer, &d.ManifestSizeSymbolPointer,
		&d.InitSymbolPointer, &d.DestroySymbolPointer,
		&d.MallocSymbolPointer, &d.FreeSymbolPointer, &d.StreamInvokeSymbolPointer,
	} {
		*field = binary.LittleEndian.Uint32(buf[i*4 : i*4+4])
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// SignedArtifactDependencyDescriptor — 72 bytes, alignment 4
// ---------------------------------------------------------------------------

type SignedArtifactDependencyDescriptor struct {
	DependencyIDPointer        uint32
	PluginIDPointer            uint32
	VersionPointer             uint32
	SHA256Pointer              uint32
	SignaturePointer           uint32
	SignerPublicKeyPointer     uint32
	EntrypointPointer          uint32
	ManifestBytesSymbolPointer uint32
	ManifestSizeSymbolPointer  uint32
	InitSymbolPointer          uint32
	DestroySymbolPointer       uint32
	MallocSymbolPointer        uint32
	FreeSymbolPointer          uint32
	StreamInvokeSymbolPointer  uint32
	WASMBytesPointer           uint32
	WASMSize                   uint32
	ManifestBytesPointer       uint32
	ManifestSize               uint32
}

const signedArtifactDependencyDescriptorSize = 72

func readDependencyDescriptor(mod *wasmrt.Module, ptr uint32) (*SignedArtifactDependencyDescriptor, error) {
	buf, err := mod.ReadMemory(ptr, signedArtifactDependencyDescriptorSize)
	if err != nil {
		return nil, fmt.Errorf("read SignedArtifactDependencyDescriptor at %d: %w", ptr, err)
	}
	d := &SignedArtifactDependencyDescriptor{}
	for i, field := range []*uint32{
		&d.DependencyIDPointer, &d.PluginIDPointer, &d.VersionPointer, &d.SHA256Pointer,
		&d.SignaturePointer, &d.SignerPublicKeyPointer, &d.EntrypointPointer,
		&d.ManifestBytesSymbolPointer, &d.ManifestSizeSymbolPointer,
		&d.InitSymbolPointer, &d.DestroySymbolPointer,
		&d.MallocSymbolPointer, &d.FreeSymbolPointer, &d.StreamInvokeSymbolPointer,
		&d.WASMBytesPointer, &d.WASMSize,
		&d.ManifestBytesPointer, &d.ManifestSize,
	} {
		*field = binary.LittleEndian.Uint32(buf[i*4 : i*4+4])
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// FlowIngressRuntimeState — 24 bytes, alignment 8
// ---------------------------------------------------------------------------

type FlowIngressRuntimeState struct {
	TotalReceived uint64
	TotalDropped  uint64
	QueuedFrames  uint32
}

const flowIngressRuntimeStateSize = 24

func readIngressRuntimeState(mod *wasmrt.Module, ptr uint32) (*FlowIngressRuntimeState, error) {
	buf, err := mod.ReadMemory(ptr, flowIngressRuntimeStateSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowIngressRuntimeState at %d: %w", ptr, err)
	}
	return &FlowIngressRuntimeState{
		TotalReceived: binary.LittleEndian.Uint64(buf[0:8]),
		TotalDropped:  binary.LittleEndian.Uint64(buf[8:16]),
		QueuedFrames:  binary.LittleEndian.Uint32(buf[16:20]),
	}, nil
}

// ---------------------------------------------------------------------------
// FlowNodeRuntimeState — 32 bytes, alignment 8
// ---------------------------------------------------------------------------

type FlowNodeRuntimeState struct {
	InvocationCount  uint64
	ConsumedFrames   uint64
	QueuedFrames     uint32
	BacklogRemaining uint32
	LastStatus       uint32
	Ready            bool
	Yielded          bool
}

const flowNodeRuntimeStateSize = 32

func readNodeRuntimeState(mod *wasmrt.Module, ptr uint32) (*FlowNodeRuntimeState, error) {
	buf, err := mod.ReadMemory(ptr, flowNodeRuntimeStateSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowNodeRuntimeState at %d: %w", ptr, err)
	}
	return &FlowNodeRuntimeState{
		InvocationCount:  binary.LittleEndian.Uint64(buf[0:8]),
		ConsumedFrames:   binary.LittleEndian.Uint64(buf[8:16]),
		QueuedFrames:     binary.LittleEndian.Uint32(buf[16:20]),
		BacklogRemaining: binary.LittleEndian.Uint32(buf[20:24]),
		LastStatus:       binary.LittleEndian.Uint32(buf[24:28]),
		Ready:            buf[28] != 0,
		Yielded:          buf[29] != 0,
	}, nil
}
