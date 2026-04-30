// Package flowrt provides a Go host for compiled SDN runtime WASM artifacts.
package flowrt

import (
	"encoding/binary"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// InvalidIndex is the sentinel value signaling "no more ready nodes" or "no descriptor".
const InvalidIndex = 0xFFFFFFFF

// ---------------------------------------------------------------------------
// FlowFrameDescriptor — 48 bytes, alignment 8
// ---------------------------------------------------------------------------

type FlowFrameDescriptor struct {
	IngressIndex       uint32
	TypeDescriptorIdx  uint32
	PortIDPointer      uint32
	Alignment          uint32
	Offset             uint32
	Size               uint32
	StreamID           uint32
	Sequence           uint32
	TraceToken         uint64
	EndOfStream        bool
	Occupied           bool
}

const flowFrameDescriptorSize = 48

func readFrameDescriptor(mod *wasmrt.Module, ptr uint32) (*FlowFrameDescriptor, error) {
	buf, err := mod.ReadMemory(ptr, flowFrameDescriptorSize)
	if err != nil {
		return nil, fmt.Errorf("read FlowFrameDescriptor at %d: %w", ptr, err)
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
	}, nil
}

func writeFrameDescriptor(mod *wasmrt.Module, ptr uint32, fd *FlowFrameDescriptor) error {
	buf := make([]byte, flowFrameDescriptorSize)
	binary.LittleEndian.PutUint32(buf[0:4], fd.IngressIndex)
	binary.LittleEndian.PutUint32(buf[4:8], fd.TypeDescriptorIdx)
	binary.LittleEndian.PutUint32(buf[8:12], fd.PortIDPointer)
	binary.LittleEndian.PutUint32(buf[12:16], fd.Alignment)
	binary.LittleEndian.PutUint32(buf[16:20], fd.Offset)
	binary.LittleEndian.PutUint32(buf[20:24], fd.Size)
	binary.LittleEndian.PutUint32(buf[24:28], fd.StreamID)
	binary.LittleEndian.PutUint32(buf[28:32], fd.Sequence)
	binary.LittleEndian.PutUint64(buf[32:40], fd.TraceToken)
	if fd.EndOfStream {
		buf[40] = 1
	}
	if fd.Occupied {
		buf[41] = 1
	}
	return mod.WriteMemory(ptr, buf)
}

// ---------------------------------------------------------------------------
// FlowInvocationDescriptor — 24 bytes, alignment 4
// ---------------------------------------------------------------------------

type FlowInvocationDescriptor struct {
	NodeIndex              uint32
	DispatchDescriptorIdx  uint32
	PluginIDPointer        uint32
	MethodIDPointer        uint32
	FramesPointer          uint32
	FrameCount             uint32
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
	InvocationCount uint64
	ConsumedFrames  uint64
	QueuedFrames    uint32
	BacklogRemaining uint32
	LastStatus      uint32
	Ready           bool
	Yielded         bool
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
