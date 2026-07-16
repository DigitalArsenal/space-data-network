package flowcc

import _ "embed"

// runtimesrc.go embeds the FLOW-AGNOSTIC flow-runtime sources the node bakes
// against. These are a vendored, modified copy of the SDK's
// src/flow/runtime-src/flow_runtime.cpp: upstream compiles the flow graph in as
// per-flow COMPILE-TIME data (flow_generated.inc) so every flow recompiles the
// whole 867-line runtime (~34s cold); this copy binds the graph at LINK time
// through FlowProgramC (flow_descriptor_abi.h), so the runtime is compiled ONCE
// and each new flow only compiles a tiny descriptor + links. See the divergence
// note in runtime-src/flow_descriptor_abi.h for the upstreaming contract.

//go:embed runtime-src/flow_runtime.cpp
var flowRuntimeCppSource []byte

//go:embed runtime-src/flow_descriptor_abi.h
var flowDescriptorAbiHeader []byte

// VendoredFlowRuntimeCpp returns the flow-agnostic flow_runtime.cpp source. The
// Baker compiles this ONCE (content-addressed) and links it against a per-flow
// descriptor for each bake.
func VendoredFlowRuntimeCpp() []byte { return flowRuntimeCppSource }

// VendoredFlowDescriptorAbi returns the flow_descriptor_abi.h shared between the
// flow-agnostic runtime and every per-flow descriptor object.
func VendoredFlowDescriptorAbi() []byte { return flowDescriptorAbiHeader }
