package flowrt

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// WS3.1 integration: a compiled flow artifact (built by the starlink-flow
// flow compiler from flow.json) loads under the Go flow runtime; its
// linked-direct chain starlink-parser -> validator runs entirely inside the
// artifact's linear memory (zero host copies), and the terminal host-model
// sink node receives the validator's result through the drain loop.
//
// Requires the built artifact; set SDN_STARLINK_FLOW_WASM to
// .../packages/starlink-flow/dist/runtime.wasm. Skipped otherwise.
func TestStarlinkFlowLinkedDirectChain(t *testing.T) {
	wasmPath := os.Getenv("SDN_STARLINK_FLOW_WASM")
	if wasmPath == "" {
		t.Skip("set SDN_STARLINK_FLOW_WASM to the built starlink-flow runtime.wasm")
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", wasmPath, err)
	}

	rt, err := NewFlowRuntime(wasmBytes, 2048)
	if err != nil {
		t.Fatalf("NewFlowRuntime: %v", err)
	}
	defer rt.Release()

	// Descriptor counts from the compiled tables.
	if rt.NodeCount != 3 || rt.EdgeCount != 2 || rt.TriggerCount != 1 || rt.DepCount != 2 {
		t.Fatalf("counts = nodes %d edges %d triggers %d deps %d, want 3/2/1/2",
			rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount)
	}

	// Dispatch descriptors parse and carry the linked-direct model.
	parseDD, err := rt.GetNodeDispatchDescriptor(0)
	if err != nil {
		t.Fatalf("GetNodeDispatchDescriptor(0): %v", err)
	}
	if got := rt.readCStringAt(parseDD.NodeIDPointer); got != "parse" {
		t.Fatalf("node 0 id = %q, want parse", got)
	}
	if got := rt.readCStringAt(parseDD.PluginIDPointer); got != "org.digitalarsenal.ephem.starlink-parser" {
		t.Fatalf("node 0 plugin = %q", got)
	}
	if got := rt.readCStringAt(parseDD.DispatchModelPointer); got != "linked-direct" {
		t.Fatalf("node 0 dispatch model = %q, want linked-direct", got)
	}
	sinkDD, err := rt.GetNodeDispatchDescriptor(2)
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.readCStringAt(sinkDD.DispatchModelPointer); got != "host" {
		t.Fatalf("sink dispatch model = %q, want host", got)
	}
	dep0, err := rt.GetDependencyDescriptor(0)
	if err != nil {
		t.Fatalf("GetDependencyDescriptor(0): %v", err)
	}
	if got := rt.readCStringAt(dep0.PluginIDPointer); got != "org.digitalarsenal.ephem.starlink-parser" {
		t.Fatalf("dep 0 plugin = %q", got)
	}

	// Enqueue a raw MEME ephemeris frame on the ingest trigger. The frame
	// descriptor + payload live in the flow's linear memory (host allocates
	// through the artifact's malloc).
	meme := strings.Join([]string{
		"created: 2026-07-02T00:00:00Z",
		"ephemeris_start: 2026-07-02T00:00:00Z ephemeris_stop: 2026-07-03T00:00:00Z step_size: 60",
		"ephemeris_source: blend",
		"UVW",
		"2026185000000.000 6800.0 0.0 0.0 0.0 7.5 0.0",
		"2026185000060.000 6800.0 450.0 0.0 0.0 7.5 0.0",
		"2026185000120.000 6800.0 900.0 0.0 0.0 7.5 0.0",
		"",
	}, "\n")
	payloadPtr, err := rt.Module().Allocate([]byte(meme))
	if err != nil {
		t.Fatalf("allocate payload: %v", err)
	}
	portPtr, err := rt.Module().AllocateString("raw")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	framePtr, err := rt.Module().AllocateSize(flowFrameDescriptorSize)
	if err != nil {
		t.Fatalf("allocate frame: %v", err)
	}
	if err := writeFrameDescriptor(rt.Module(), framePtr, &FlowFrameDescriptor{
		PortIDPointer: portPtr,
		Offset:        payloadPtr,
		Size:          uint32(len(meme)),
		Occupied:      true,
	}); err != nil {
		t.Fatalf("writeFrameDescriptor: %v", err)
	}
	rt.EnqueueTriggerFrame(0, framePtr)

	ingress, err := rt.GetIngressRuntimeState(0)
	if err != nil {
		t.Fatalf("GetIngressRuntimeState: %v", err)
	}
	if ingress.TotalReceived != 1 {
		t.Fatalf("ingress received = %d, want 1", ingress.TotalReceived)
	}

	// Drain: parse and validate run linked-direct (no handlers registered for
	// them); the sink is host-handled and receives the validation result.
	var sinkFrames []FrameData
	handlers := HandlerMap{
		"test.sink:collect": func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
			sinkFrames = append(sinkFrames, args.Frames...)
			return &InvocationResult{StatusCode: 0}, nil
		},
	}
	result, err := rt.Drain(context.Background(), handlers, DrainOptions{MaxIterations: 50})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if result.NodesInvoked < 3 {
		t.Fatalf("NodesInvoked = %d, want >= 3 (parse, validate, sink)", result.NodesInvoked)
	}

	// Linked-direct nodes executed inside the artifact.
	for node, wantID := range map[uint32]string{0: "parse", 1: "validate"} {
		state, err := rt.GetNodeRuntimeState(node)
		if err != nil {
			t.Fatalf("GetNodeRuntimeState(%d): %v", node, err)
		}
		if state.InvocationCount != 1 {
			t.Fatalf("%s invocation count = %d, want 1", wantID, state.InvocationCount)
		}
	}

	// The sink received the validator's JSON verdict for the 3-state batch.
	if len(sinkFrames) != 1 {
		t.Fatalf("sink frames = %d, want 1", len(sinkFrames))
	}
	if sinkFrames[0].PortID != "result" {
		t.Fatalf("sink port = %q, want result", sinkFrames[0].PortID)
	}
	var verdict struct {
		OK         bool    `json:"ok"`
		Confidence float64 `json:"confidence"`
		StateCount int     `json:"stateCount"`
	}
	if err := json.Unmarshal(sinkFrames[0].Bytes, &verdict); err != nil {
		t.Fatalf("sink payload is not the validator JSON: %v (%q)", err, sinkFrames[0].Bytes)
	}
	if !verdict.OK || verdict.StateCount != 3 {
		t.Fatalf("verdict = %+v, want ok with 3 states (payload %s)", verdict, sinkFrames[0].Bytes)
	}
	t.Logf("linked-direct chain verdict: %s", sinkFrames[0].Bytes)
}
