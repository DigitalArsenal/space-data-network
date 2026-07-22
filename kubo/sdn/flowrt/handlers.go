package flowrt

import "context"

// Handler is a function that processes a node invocation.
type Handler func(ctx context.Context, args *InvocationArgs) (*InvocationResult, error)

// HandlerMap maps handler keys to Handler functions.
// Keys are tried in order: "dependencyId:methodId", "pluginId:methodId",
// "nodeId:methodId", "dependencyId", "pluginId", "nodeId", "methodId".
type HandlerMap map[string]Handler

// Merge combines two HandlerMaps, with other's entries taking precedence.
func (m HandlerMap) Merge(other HandlerMap) HandlerMap {
	merged := make(HandlerMap, len(m)+len(other))
	for k, v := range m {
		merged[k] = v
	}
	for k, v := range other {
		merged[k] = v
	}
	return merged
}

// Resolve finds the best-matching handler for a given invocation.
func (m HandlerMap) Resolve(pluginID, methodID, dependencyID, nodeID string) Handler {
	keys := []string{}
	if dependencyID != "" && methodID != "" {
		keys = append(keys, dependencyID+":"+methodID)
	}
	if pluginID != "" && methodID != "" {
		keys = append(keys, pluginID+":"+methodID)
	}
	if nodeID != "" && methodID != "" {
		keys = append(keys, nodeID+":"+methodID)
	}
	if dependencyID != "" {
		keys = append(keys, dependencyID)
	}
	if pluginID != "" {
		keys = append(keys, pluginID)
	}
	if nodeID != "" {
		keys = append(keys, nodeID)
	}
	if methodID != "" {
		keys = append(keys, methodID)
	}
	for _, key := range keys {
		if h, ok := m[key]; ok {
			return h
		}
	}
	return nil
}

// InvocationArgs holds the input context for a handler invocation.
type InvocationArgs struct {
	NodeIndex    uint32
	PluginID     string
	MethodID     string
	DependencyID string
	NodeID       string
	Frames       []FrameData
}

// FrameData represents a single typed frame in an invocation.
type FrameData struct {
	PortID            string
	Bytes             []byte
	TypeDescriptorIdx uint32
	SchemaName        string
	FileIdentifier    string
	SchemaVersion     string
	SchemaHash        []byte
	RootTypeName      string
	WireFormat        byte
	FixedStringLength uint32
	ByteLength        uint32
	RequiredAlignment uint32
	Alignment         uint32
	Ownership         byte
	Mutability        byte
	Lifetime          byte
	FrameID           uint64
	StreamID          uint32
	Sequence          uint32
	EndOfStream       bool
}

// InvocationResult is the output from a handler.
type InvocationResult struct {
	StatusCode       int32
	BacklogRemaining uint32
	Yielded          bool
	Outputs          []FrameOutput
}

// FrameOutput is a single output frame produced by a handler.
type FrameOutput struct {
	PortID            string
	Bytes             []byte
	TypeDescriptorIdx uint32
	SchemaName        string
	FileIdentifier    string
	SchemaVersion     string
	SchemaHash        []byte
	RootTypeName      string
	WireFormat        byte
	FixedStringLength uint32
	ByteLength        uint32
	RequiredAlignment uint32
	Alignment         uint32
	Ownership         byte
	Mutability        byte
	Lifetime          byte
	FrameID           uint64
	StreamID          uint32
	Sequence          uint32
	EndOfStream       bool
}

// DrainOptions controls the drain loop behavior.
type DrainOptions struct {
	MaxIterations int // 0 = unlimited
}

// DrainResult summarizes a drain loop run.
type DrainResult struct {
	Iterations      int
	NodesInvoked    int
	HandlersSkipped int
}
