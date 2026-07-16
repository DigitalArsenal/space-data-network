/*
 * flow_descriptor_abi.h — the ABI shared between the FLOW-AGNOSTIC runtime
 * (flow_runtime.cpp, compiled ONCE) and the tiny PER-FLOW descriptor object
 * (descriptor.cpp, generated + compiled per bake). This header is the contract
 * that lets the runtime be compiled a single time and merely LINKED against a
 * different descriptor for every flow — the Phase-0b interactive-bake win.
 *
 * DIVERGENCE NOTE (for later upstreaming into space-data-module-sdk):
 * upstream's src/flow/runtime-src/flow_runtime.cpp pulls the flow graph in as
 * COMPILE-TIME data via `#include "flow_generated.inc"` (FLOW_*_COUNT macros +
 * statically-sized g_edges/g_dispatch_descriptors/... tables + a flow_call_entry
 * switch), so every flow recompiles the 867-line runtime (~34s cold). This
 * vendored copy replaces that with a runtime-bound `FlowProgramC g_flow_program`
 * the descriptor DEFINES and the runtime READS: counts become runtime variables,
 * the graph tables become descriptor-owned pointers, and the entry switch becomes
 * a node-indexed function-pointer table dispatched via call_indirect. Keep the
 * struct layouts below byte-identical to sdn-server internal/flowrt/abi.go and to
 * upstream flow_runtime.cpp (the host reads the 60/72-byte descriptors directly).
 */

#ifndef SDN_FLOW_DESCRIPTOR_ABI_H
#define SDN_FLOW_DESCRIPTOR_ABI_H

#include <stdint.h>

// ---------------------------------------------------------------------------
// Host-visible descriptor structs — layouts must match sdn-server
// internal/flowrt/abi.go EXACTLY (the host reads these out of linear memory).
// ---------------------------------------------------------------------------

struct FlowNodeDispatchDescriptorC {
  uint32_t node_id_ptr;
  uint32_t node_index;
  uint32_t dependency_id_ptr;
  uint32_t dependency_index;
  uint32_t plugin_id_ptr;
  uint32_t method_id_ptr;
  uint32_t dispatch_model_ptr;
  uint32_t entrypoint_ptr;
  uint32_t manifest_bytes_symbol_ptr;
  uint32_t manifest_size_symbol_ptr;
  uint32_t init_symbol_ptr;
  uint32_t destroy_symbol_ptr;
  uint32_t malloc_symbol_ptr;
  uint32_t free_symbol_ptr;
  uint32_t stream_invoke_symbol_ptr;
};
static_assert(sizeof(FlowNodeDispatchDescriptorC) == 60, "FlowNodeDispatchDescriptor must be 60 bytes");

struct SignedArtifactDependencyDescriptorC {
  uint32_t dependency_id_ptr;
  uint32_t plugin_id_ptr;
  uint32_t version_ptr;
  uint32_t sha256_ptr;
  uint32_t signature_ptr;
  uint32_t signer_public_key_ptr;
  uint32_t entrypoint_ptr;
  uint32_t manifest_bytes_symbol_ptr;
  uint32_t manifest_size_symbol_ptr;
  uint32_t init_symbol_ptr;
  uint32_t destroy_symbol_ptr;
  uint32_t malloc_symbol_ptr;
  uint32_t free_symbol_ptr;
  uint32_t stream_invoke_symbol_ptr;
  uint32_t wasm_bytes_ptr;
  uint32_t wasm_size;
  uint32_t manifest_bytes_ptr;
  uint32_t manifest_size;
};
static_assert(sizeof(SignedArtifactDependencyDescriptorC) == 72, "SignedArtifactDependencyDescriptor must be 72 bytes");

// ---------------------------------------------------------------------------
// Flow-graph tables (internal to the artifact; not host-read directly).
// ---------------------------------------------------------------------------

struct FlowEdge {
  uint32_t from_node;
  const char *from_port;
  uint32_t to_node;
  const char *to_port;
};

struct FlowTriggerBinding {
  uint32_t trigger_index;
  uint32_t target_node;
  const char *port;
};

struct FlowRequiredPort {
  uint32_t node_index;
  const char *port_id;
};

// Node entry: a linked-direct guest-link method symbol. The runtime dispatches
// g_flow_program.entry[node]() via call_indirect (the linkspike-proven path).
typedef int32_t (*flow_entry_fn)(void);

// ---------------------------------------------------------------------------
// FlowProgramC — the SINGLE symbol the flow-agnostic runtime binds against.
// The per-flow descriptor object DEFINES `extern "C" FlowProgramC g_flow_program`
// as a constant-initialised aggregate (all fields are integer literals or the
// addresses of static tables, so it lands in the data segment before any dynamic
// constructor runs). The runtime READS it at init to size its heap-allocated
// scheduler state and to drive every loop / accessor.
// ---------------------------------------------------------------------------

struct FlowProgramC {
  uint32_t node_count;
  uint32_t edge_count;
  uint32_t trigger_count;  // >= 1 (bumped like the old FLOW_TRIGGER_COUNT)
  uint32_t dep_count;
  uint32_t trigger_binding_count;
  uint32_t required_port_count;
  FlowEdge *edges;
  FlowTriggerBinding *trigger_bindings;
  FlowRequiredPort *required_ports;
  FlowNodeDispatchDescriptorC *dispatch_descriptors;     // node_count entries
  SignedArtifactDependencyDescriptorC *dependency_descriptors;  // dep_count entries
  flow_entry_fn *entry;        // node_count entries; nullptr for host-model nodes
  const uint8_t *node_linked;  // node_count entries; 1 = linked-direct
};

#endif  // SDN_FLOW_DESCRIPTOR_ABI_H
