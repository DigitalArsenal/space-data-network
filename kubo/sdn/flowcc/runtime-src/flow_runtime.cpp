/*
 * Generic compiled-flow runtime — FLOW-AGNOSTIC vendored variant.
 *
 * This is the SDN node's own copy of the SDK flow-compiler runtime template,
 * refactored so it is compiled EXACTLY ONCE and merely LINKED against a small
 * per-flow descriptor object for every flow (Phase-0b interactive-bake win).
 *
 * DIVERGENCE FROM UPSTREAM (space-data-module-sdk src/flow/runtime-src/
 * flow_runtime.cpp): upstream `#include "flow_generated.inc"` and bakes the flow
 * graph in as COMPILE-TIME data (FLOW_*_COUNT macros as array sizes, static
 * g_edges/g_dispatch_descriptors/... tables, a flow_call_entry switch), so every
 * new flow recompiles all 867 lines (~34s cold). Here the flow graph arrives at
 * LINK time through `extern "C" FlowProgramC g_flow_program` (see
 * flow_descriptor_abi.h), DEFINED by the per-flow descriptor object:
 *   - the FLOW_*_COUNT macros became `g_flow_program.*_count` runtime variables,
 *   - g_queues / g_node_states / g_ingress_states are heap-allocated at init
 *     from those counts (were statically-sized arrays),
 *   - g_edges / g_trigger_bindings / g_required_ports / g_dispatch_descriptors /
 *     g_dependency_descriptors are descriptor-owned pointers (were static tables),
 *   - the flow_call_entry switch became a node-indexed function-pointer table
 *     (g_flow_program.entry[]) dispatched via call_indirect,
 *   - flow_node_is_linked became a per-node data lookup (g_flow_program.node_linked[]).
 * Everything below the descriptor boundary (the scheduler, the SDK invoke-ABI
 * shim, and the space_data_module_runtime_* export surface) is byte-for-byte the
 * upstream behaviour. Keep in sync when upstreaming.
 */

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <string>
#include <vector>

#include "space_data_module_invoke.h"
#include "flow_descriptor_abi.h"

#define FLOW_EXPORT extern "C" __attribute__((visibility("default")))

#ifdef SDN_FLATSQL_LINKED
static void sdn_flatsql_link_reset_refs(void);
#endif

// Guest-link objects compiled from Emscripten C++ may import the memory
// growth notification; provide the same weak no-op the module compiler uses.
extern "C" __attribute__((weak)) void emscripten_notify_memory_growth(int) {}

// ---------------------------------------------------------------------------
// Runtime-only ABI structs — layouts must match sdn-server internal/flowrt/
// abi.go exactly. (The descriptor-shared structs live in flow_descriptor_abi.h.)
// ---------------------------------------------------------------------------

struct FlowFrameDescriptorC {
  uint32_t ingress_index;
  uint32_t type_descriptor_idx;
  uint32_t port_id_ptr;
  uint32_t alignment;
  uint32_t offset;
  uint32_t size;
  uint32_t stream_id;
  uint32_t sequence;
  uint64_t trace_token;
  uint8_t end_of_stream;
  uint8_t occupied;
  uint8_t _pad[6];
};
static_assert(sizeof(FlowFrameDescriptorC) == 48, "FlowFrameDescriptor must be 48 bytes");

struct FlowInvocationDescriptorC {
  uint32_t node_index;
  uint32_t dispatch_descriptor_idx;
  uint32_t plugin_id_ptr;
  uint32_t method_id_ptr;
  uint32_t frames_ptr;
  uint32_t frame_count;
};
static_assert(sizeof(FlowInvocationDescriptorC) == 24, "FlowInvocationDescriptor must be 24 bytes");

struct FlowIngressRuntimeStateC {
  uint64_t total_received;
  uint64_t total_dropped;
  uint32_t queued_frames;
  uint8_t _pad[4];
};
static_assert(sizeof(FlowIngressRuntimeStateC) == 24, "FlowIngressRuntimeState must be 24 bytes");

struct FlowNodeRuntimeStateC {
  uint64_t invocation_count;
  uint64_t consumed_frames;
  uint32_t queued_frames;
  uint32_t backlog_remaining;
  uint32_t last_status;
  uint8_t ready;
  uint8_t yielded;
  uint8_t _pad[2];
};
static_assert(sizeof(FlowNodeRuntimeStateC) == 32, "FlowNodeRuntimeState must be 32 bytes");

// ---------------------------------------------------------------------------
// Per-flow graph — bound at LINK time by the descriptor object.
// ---------------------------------------------------------------------------

extern "C" FlowProgramC g_flow_program;

// ---------------------------------------------------------------------------
// Scheduler state
// ---------------------------------------------------------------------------

struct QueuedFrame {
  std::string port;
  std::vector<uint8_t> payload;
  uint32_t stream_id = 0;
  uint32_t sequence = 0;
  uint8_t end_of_stream = 0;
};

// Heap-allocated at init from g_flow_program's counts (were statically-sized
// arrays g_queues[FLOW_NODE_COUNT] / g_node_states[FLOW_NODE_COUNT] /
// g_ingress_states[FLOW_TRIGGER_COUNT]). Sizes are fixed for the life of the
// artifact, so the raw pointers get_node_states()/get_ingress_states() hand the
// host stay stable.
static std::vector<QueuedFrame> *g_queues = nullptr;
static FlowNodeRuntimeStateC *g_node_states = nullptr;
static FlowIngressRuntimeStateC *g_ingress_states = nullptr;

static void flow_runtime_alloc() {
  const FlowProgramC &p = g_flow_program;
  g_queues = new std::vector<QueuedFrame>[p.node_count];
  g_node_states = new FlowNodeRuntimeStateC[p.node_count];
  memset(g_node_states, 0, sizeof(FlowNodeRuntimeStateC) * p.node_count);
  const uint32_t tc = p.trigger_count > 0 ? p.trigger_count : 1;
  g_ingress_states = new FlowIngressRuntimeStateC[tc];
  memset(g_ingress_states, 0, sizeof(FlowIngressRuntimeStateC) * tc);
}

// Runs at _initialize (WASI-reactor __wasm_call_ctors). g_flow_program's scalar
// counts are constant-initialised in the descriptor's data segment, so they are
// readable here regardless of cross-TU constructor order.
static struct FlowRuntimeInit {
  FlowRuntimeInit() { flow_runtime_alloc(); }
} g_flow_runtime_init;

// Linked-direct dispatch (replaces the generated flow_call_entry switch): call
// the node's entry through the descriptor's node-indexed function-pointer table
// via call_indirect. flow_node_is_linked is now a per-node data lookup.
static inline bool flow_node_is_linked(uint32_t node) {
  return node < g_flow_program.node_count && g_flow_program.node_linked[node] != 0;
}

static inline int32_t flow_call_entry(uint32_t node) {
  if (node >= g_flow_program.node_count) return -1;
  flow_entry_fn fn = g_flow_program.entry[node];
  if (fn == nullptr) return -1;
  return fn();
}

// Current invocation (one at a time — both host drain loops are serialized).
static constexpr uint32_t kMaxInvocationFrames = 64;
static constexpr uint32_t kInvalidIndex = 0xFFFFFFFFu;
static FlowInvocationDescriptorC g_current_desc;
static FlowFrameDescriptorC g_current_frames[kMaxInvocationFrames];
static QueuedFrame g_current_owned[kMaxInvocationFrames];
static uint32_t g_current_node = kInvalidIndex;

// Readiness: a node is ready when it has queued frames AND every required
// input port of its bound method (compiled in from the dependency manifest)
// has at least one queued frame. Host-model nodes have no required-port rows
// and fire on any queued frame.
static bool flow_node_is_ready(uint32_t node) {
  if (g_queues[node].empty()) return false;
  for (uint32_t r = 0; r < g_flow_program.required_port_count; r++) {
    if (g_flow_program.required_ports[r].node_index != node) continue;
    bool present = false;
    for (const QueuedFrame &frame : g_queues[node]) {
      if (strcmp(frame.port.c_str(), g_flow_program.required_ports[r].port_id) == 0) {
        present = true;
        break;
      }
    }
    if (!present) return false;
  }
  return true;
}

static void route_output(uint32_t from_node, const char *port, const uint8_t *payload,
                         uint32_t length, uint32_t stream_id, uint32_t sequence,
                         uint8_t end_of_stream) {
  for (uint32_t e = 0; e < g_flow_program.edge_count; e++) {
    if (g_flow_program.edges[e].from_node != from_node) continue;
    if (strcmp(g_flow_program.edges[e].from_port, port) != 0) continue;
    QueuedFrame frame;
    frame.port = g_flow_program.edges[e].to_port;
    frame.payload.assign(payload, payload + length);
    frame.stream_id = stream_id;
    frame.sequence = sequence;
    frame.end_of_stream = end_of_stream;
    uint32_t to = g_flow_program.edges[e].to_node;
    g_queues[to].push_back(static_cast<QueuedFrame &&>(frame));
    g_node_states[to].queued_frames = static_cast<uint32_t>(g_queues[to].size());
    g_node_states[to].ready = flow_node_is_ready(to) ? 1 : 0;
  }
}

// ---------------------------------------------------------------------------
// SDK invoke-ABI shim: the linked guest-link method entries read their inputs
// and push their outputs through these functions (declared by the SDK's
// generated space_data_module_invoke.h). Inputs alias the current
// invocation's queued payload buffers — zero copies inside the flow.
// ---------------------------------------------------------------------------

static std::vector<plugin_input_frame_t> g_shim_inputs;

struct ShimOutput {
  std::string port;
  std::vector<uint8_t> payload;
  uint64_t sequence = 0;
  int32_t end_of_stream = 0;
};
static std::vector<ShimOutput> g_shim_outputs;
static std::string g_shim_error_code;
static std::string g_shim_error_message;
static int32_t g_shim_yielded = 0;
static uint32_t g_shim_backlog_remaining = 0;

extern "C" uint32_t plugin_get_input_count(void) {
  return static_cast<uint32_t>(g_shim_inputs.size());
}

extern "C" const plugin_input_frame_t *plugin_get_input_frame(uint32_t index) {
  if (index >= g_shim_inputs.size()) return nullptr;
  return &g_shim_inputs[index];
}

extern "C" int32_t plugin_find_input_index(const char *port_id, uint32_t ordinal) {
  if (port_id == nullptr) return -1;
  uint32_t seen = 0;
  for (uint32_t i = 0; i < g_shim_inputs.size(); i++) {
    if (g_shim_inputs[i].port_id != nullptr && strcmp(g_shim_inputs[i].port_id, port_id) == 0) {
      if (seen == ordinal) return static_cast<int32_t>(i);
      seen++;
    }
  }
  return -1;
}

extern "C" void plugin_reset_output_state(void) {
  g_shim_outputs.clear();
  g_shim_error_code.clear();
  g_shim_error_message.clear();
  g_shim_yielded = 0;
  g_shim_backlog_remaining = 0;
}

static int32_t shim_push_output(const char *port_id, const uint8_t *payload_ptr,
                                uint32_t payload_length) {
  ShimOutput out;
  out.port = port_id != nullptr ? port_id : "";
  if (payload_ptr != nullptr && payload_length > 0) {
    out.payload.assign(payload_ptr, payload_ptr + payload_length);
  }
  g_shim_outputs.push_back(static_cast<ShimOutput &&>(out));
  return 0;
}

extern "C" int32_t plugin_push_output(const char *port_id, const char *, const char *,
                                      const uint8_t *payload_ptr, uint32_t payload_length) {
  return shim_push_output(port_id, payload_ptr, payload_length);
}

extern "C" int32_t plugin_push_output_typed(
    const char *port_id, const char *, const char *, uint32_t, const char *,
    uint16_t, uint32_t, uint16_t, const uint8_t *payload_ptr, uint32_t payload_length) {
  return shim_push_output(port_id, payload_ptr, payload_length);
}

extern "C" int32_t plugin_push_output_ex(
    const char *port_id, const char *, const char *, uint32_t, const char *,
    uint16_t, uint16_t, const uint8_t *payload_ptr, uint32_t payload_length) {
  return shim_push_output(port_id, payload_ptr, payload_length);
}

extern "C" int32_t plugin_set_output_frame_id(uint32_t output_index, uint64_t frame_id) {
  if (output_index >= g_shim_outputs.size()) return -1;
  g_shim_outputs[output_index].sequence = frame_id >> 1;
  g_shim_outputs[output_index].end_of_stream = static_cast<int32_t>(frame_id & 1u);
  return 0;
}

extern "C" int32_t plugin_set_output_stream_frame(uint32_t output_index, uint64_t sequence,
                                                  int32_t end_of_stream) {
  if (output_index >= g_shim_outputs.size()) return -1;
  g_shim_outputs[output_index].sequence = sequence;
  g_shim_outputs[output_index].end_of_stream = end_of_stream;
  return 0;
}

extern "C" void plugin_set_yielded(int32_t yielded) { g_shim_yielded = yielded; }

extern "C" void plugin_set_backlog_remaining(uint32_t backlog_remaining) {
  g_shim_backlog_remaining = backlog_remaining;
}

extern "C" void plugin_set_error(const char *error_code, const char *error_message) {
  g_shim_error_code = error_code != nullptr ? error_code : "";
  g_shim_error_message = error_message != nullptr ? error_message : "";
}

// ---------------------------------------------------------------------------
// space_data_module_runtime_* exports
// ---------------------------------------------------------------------------

FLOW_EXPORT uint32_t space_data_module_runtime_get_node_descriptor_count(void) {
  return g_flow_program.node_count;
}
FLOW_EXPORT uint32_t space_data_module_runtime_get_edge_descriptor_count(void) {
  return g_flow_program.edge_count;
}
FLOW_EXPORT uint32_t space_data_module_runtime_get_trigger_descriptor_count(void) {
  return g_flow_program.trigger_count;
}
FLOW_EXPORT uint32_t space_data_module_runtime_get_dependency_descriptor_count(void) {
  return g_flow_program.dep_count;
}

FLOW_EXPORT void space_data_module_runtime_reset_state(void) {
  for (uint32_t n = 0; n < g_flow_program.node_count; n++) {
    g_queues[n].clear();
    memset(&g_node_states[n], 0, sizeof(FlowNodeRuntimeStateC));
  }
  const uint32_t tc = g_flow_program.trigger_count > 0 ? g_flow_program.trigger_count : 1;
  for (uint32_t t = 0; t < tc; t++) {
    memset(&g_ingress_states[t], 0, sizeof(FlowIngressRuntimeStateC));
  }
  g_current_node = kInvalidIndex;
#ifdef SDN_FLATSQL_LINKED
  sdn_flatsql_link_reset_refs();
#endif
}

FLOW_EXPORT uint32_t space_data_module_runtime_get_ready_node_index(void) {
  if (g_current_node != kInvalidIndex) return kInvalidIndex;  // invocation open
  for (uint32_t n = 0; n < g_flow_program.node_count; n++) {
    if (flow_node_is_ready(n)) return n;
  }
  return kInvalidIndex;
}

FLOW_EXPORT int32_t space_data_module_runtime_begin_node_invocation(int32_t node_index,
                                                                    int32_t frame_budget) {
  if (node_index < 0 || static_cast<uint32_t>(node_index) >= g_flow_program.node_count) return -1;
  uint32_t node = static_cast<uint32_t>(node_index);
  uint32_t budget = frame_budget > 0 ? static_cast<uint32_t>(frame_budget) : kMaxInvocationFrames;
  if (budget > kMaxInvocationFrames) budget = kMaxInvocationFrames;

  uint32_t count = 0;
  auto &queue = g_queues[node];
  while (count < budget && !queue.empty()) {
    g_current_owned[count] = static_cast<QueuedFrame &&>(queue.front());
    queue.erase(queue.begin());
    count++;
  }
  for (uint32_t i = 0; i < count; i++) {
    FlowFrameDescriptorC &fd = g_current_frames[i];
    memset(&fd, 0, sizeof(fd));
    fd.port_id_ptr = reinterpret_cast<uint32_t>(g_current_owned[i].port.c_str());
    fd.alignment = 1;
    fd.offset = reinterpret_cast<uint32_t>(g_current_owned[i].payload.data());
    fd.size = static_cast<uint32_t>(g_current_owned[i].payload.size());
    fd.stream_id = g_current_owned[i].stream_id;
    fd.sequence = g_current_owned[i].sequence;
    fd.end_of_stream = g_current_owned[i].end_of_stream;
    fd.occupied = 1;
  }

  const FlowNodeDispatchDescriptorC &dd = g_flow_program.dispatch_descriptors[node];
  g_current_desc.node_index = node;
  g_current_desc.dispatch_descriptor_idx = node;
  g_current_desc.plugin_id_ptr = dd.plugin_id_ptr;
  g_current_desc.method_id_ptr = dd.method_id_ptr;
  g_current_desc.frames_ptr = count > 0 ? reinterpret_cast<uint32_t>(&g_current_frames[0]) : 0;
  g_current_desc.frame_count = count;
  g_current_node = node;

  g_node_states[node].queued_frames = static_cast<uint32_t>(queue.size());
  g_node_states[node].ready = flow_node_is_ready(node) ? 1 : 0;
  return static_cast<int32_t>(count);
}

FLOW_EXPORT uint32_t space_data_module_runtime_get_current_invocation_descriptor(void) {
  if (g_current_node == kInvalidIndex) return 0;
  return reinterpret_cast<uint32_t>(&g_current_desc);
}

FLOW_EXPORT uint32_t space_data_module_runtime_apply_node_invocation_result(
    int32_t node_index, int32_t status_code, int32_t backlog_remaining, int32_t yielded,
    int32_t frames_ptr, int32_t frame_count) {
  if (node_index < 0 || static_cast<uint32_t>(node_index) >= g_flow_program.node_count) return 0;
  uint32_t node = static_cast<uint32_t>(node_index);
  uint32_t routed = 0;
  const FlowFrameDescriptorC *frames = reinterpret_cast<const FlowFrameDescriptorC *>(frames_ptr);
  for (int32_t i = 0; i < frame_count; i++) {
    const FlowFrameDescriptorC &fd = frames[i];
    if (!fd.occupied) continue;
    const char *port = fd.port_id_ptr != 0 ? reinterpret_cast<const char *>(fd.port_id_ptr) : "";
    const uint8_t *payload = reinterpret_cast<const uint8_t *>(fd.offset);
    route_output(node, port, payload, fd.size, fd.stream_id, fd.sequence, fd.end_of_stream);
    routed++;
  }
  g_node_states[node].invocation_count++;
  g_node_states[node].consumed_frames += g_current_desc.frame_count;
  g_node_states[node].backlog_remaining = static_cast<uint32_t>(backlog_remaining);
  g_node_states[node].last_status = static_cast<uint32_t>(status_code);
  g_node_states[node].yielded = yielded != 0 ? 1 : 0;
  return routed;
}

FLOW_EXPORT void space_data_module_runtime_complete_node_invocation(int32_t node_index) {
  (void)node_index;
  if (g_current_node == kInvalidIndex) return;
  for (uint32_t i = 0; i < g_current_desc.frame_count; i++) {
    g_current_owned[i] = QueuedFrame();
  }
  g_current_desc = FlowInvocationDescriptorC();
  g_current_node = kInvalidIndex;
}

static void flow_enqueue_binding(const FlowTriggerBinding &binding, const char *port,
                                 const uint8_t *payload, uint32_t length, uint32_t stream_id,
                                 uint32_t sequence, uint8_t end_of_stream) {
  QueuedFrame frame;
  frame.port = (port != nullptr && port[0] != '\0') ? port : binding.port;
  if (payload != nullptr && length > 0) {
    frame.payload.assign(payload, payload + length);
  }
  frame.stream_id = stream_id;
  frame.sequence = sequence;
  frame.end_of_stream = end_of_stream;
  g_queues[binding.target_node].push_back(static_cast<QueuedFrame &&>(frame));
  g_node_states[binding.target_node].queued_frames =
      static_cast<uint32_t>(g_queues[binding.target_node].size());
  g_node_states[binding.target_node].ready =
      flow_node_is_ready(binding.target_node) ? 1 : 0;
}

FLOW_EXPORT void space_data_module_runtime_enqueue_trigger_frames(int32_t trigger_index) {
  if (trigger_index < 0 || static_cast<uint32_t>(trigger_index) >= g_flow_program.trigger_count) return;
  for (uint32_t b = 0; b < g_flow_program.trigger_binding_count; b++) {
    if (g_flow_program.trigger_bindings[b].trigger_index != static_cast<uint32_t>(trigger_index)) continue;
    flow_enqueue_binding(g_flow_program.trigger_bindings[b], nullptr, nullptr, 0, 0, 0, 0);
  }
  g_ingress_states[trigger_index].total_received++;
  g_ingress_states[trigger_index].queued_frames++;
}

FLOW_EXPORT void space_data_module_runtime_enqueue_trigger_frame(int32_t trigger_index,
                                                                 int32_t frame_ptr) {
  if (trigger_index < 0 || static_cast<uint32_t>(trigger_index) >= g_flow_program.trigger_count) return;
  if (frame_ptr == 0) {
    space_data_module_runtime_enqueue_trigger_frames(trigger_index);
    return;
  }
  const FlowFrameDescriptorC *fd = reinterpret_cast<const FlowFrameDescriptorC *>(frame_ptr);
  const char *port =
      fd->port_id_ptr != 0 ? reinterpret_cast<const char *>(fd->port_id_ptr) : nullptr;
  const uint8_t *payload =
      (fd->offset != 0 && fd->size > 0) ? reinterpret_cast<const uint8_t *>(fd->offset) : nullptr;
  for (uint32_t b = 0; b < g_flow_program.trigger_binding_count; b++) {
    if (g_flow_program.trigger_bindings[b].trigger_index != static_cast<uint32_t>(trigger_index)) continue;
    flow_enqueue_binding(g_flow_program.trigger_bindings[b], port, payload, payload != nullptr ? fd->size : 0,
                         fd->stream_id, fd->sequence, fd->end_of_stream);
  }
  g_ingress_states[trigger_index].total_received++;
  g_ingress_states[trigger_index].queued_frames++;
}

FLOW_EXPORT uint32_t space_data_module_runtime_get_node_dispatch_descriptors(void) {
  return reinterpret_cast<uint32_t>(&g_flow_program.dispatch_descriptors[0]);
}

FLOW_EXPORT uint32_t space_data_module_runtime_get_dependency_descriptors(void) {
  if (g_flow_program.dep_count == 0) return 0;
  return reinterpret_cast<uint32_t>(&g_flow_program.dependency_descriptors[0]);
}

FLOW_EXPORT uint32_t space_data_module_runtime_get_node_states(void) {
  return reinterpret_cast<uint32_t>(&g_node_states[0]);
}

FLOW_EXPORT uint32_t space_data_module_runtime_get_ingress_states(void) {
  if (g_flow_program.trigger_count == 0) return 0;
  return reinterpret_cast<uint32_t>(&g_ingress_states[0]);
}

FLOW_EXPORT int32_t space_data_module_runtime_dispatch_current_invocation_direct(
    int32_t frame_budget);

// In-wasm scheduler loop (loop C.5c): run every ready LINKED-DIRECT node to
// completion inside one host call — ready-node selection, invocation
// begin/dispatch/complete, and frame routing all stay in this module's
// linear memory instead of costing 3-4 host<->wasm round-trips per node.
// Returns the number of linked dispatches performed and stops (without
// consuming it) at the first ready node that is NOT linked (host-model,
// e.g. the egress sink) so the host loop handles it — node order is
// identical to the host-driven loop (first ready by index). Hosts probe for
// this export and fall back to per-node driving when absent (older
// artifacts).
FLOW_EXPORT int32_t space_data_module_runtime_drain_linked(int32_t max_iterations) {
  if (g_current_node != kInvalidIndex) return -1;  // invocation open
  if (max_iterations == 0) return 0;               // presence probe
  int32_t budget = max_iterations > 0 ? max_iterations : 1024;
  int32_t dispatched = 0;
  for (int32_t i = 0; i < budget; i++) {
    uint32_t node = kInvalidIndex;
    for (uint32_t n = 0; n < g_flow_program.node_count; n++) {
      if (flow_node_is_ready(n)) {
        node = n;
        break;
      }
    }
    if (node == kInvalidIndex || !flow_node_is_linked(node)) break;
    if (space_data_module_runtime_begin_node_invocation(static_cast<int32_t>(node), 64) < 0) {
      space_data_module_runtime_complete_node_invocation(static_cast<int32_t>(node));
      break;
    }
    space_data_module_runtime_dispatch_current_invocation_direct(64);
    space_data_module_runtime_complete_node_invocation(static_cast<int32_t>(node));
    dispatched++;
  }
  return dispatched;
}

// Linked-direct dispatch: run the current node's linked guest-link entry over
// the current invocation frames — all inside this module's linear memory.
FLOW_EXPORT int32_t space_data_module_runtime_dispatch_current_invocation_direct(
    int32_t frame_budget) {
  (void)frame_budget;
  if (g_current_node == kInvalidIndex) return -1;
  uint32_t node = g_current_node;
  if (!flow_node_is_linked(node)) return -2;

  g_shim_inputs.clear();
  for (uint32_t i = 0; i < g_current_desc.frame_count; i++) {
    const QueuedFrame &owned = g_current_owned[i];
    plugin_input_frame_t input;
    memset(&input, 0, sizeof(input));
    input.port_id = owned.port.c_str();
    input.payload = owned.payload.data();
    input.payload_length = static_cast<uint32_t>(owned.payload.size());
    input.byte_length = input.payload_length;
    input.size = input.payload_length;
    input.alignment = 8;
    input.required_alignment = 1;
    input.wire_format = PLUGIN_PAYLOAD_WIRE_FORMAT_ALIGNED_BINARY;
    input.stream_id = owned.stream_id;
    input.sequence = owned.sequence;
    input.end_of_stream = owned.end_of_stream != 0 ? 1 : 0;
    g_shim_inputs.push_back(input);
  }
  plugin_reset_output_state();

  int32_t status = flow_call_entry(node);

  uint32_t routed = 0;
  for (const ShimOutput &out : g_shim_outputs) {
    route_output(node, out.port.c_str(), out.payload.data(),
                 static_cast<uint32_t>(out.payload.size()), 0,
                 static_cast<uint32_t>(out.sequence), out.end_of_stream != 0 ? 1 : 0);
    routed++;
  }
  g_node_states[node].invocation_count++;
  g_node_states[node].consumed_frames += g_current_desc.frame_count;
  g_node_states[node].last_status = static_cast<uint32_t>(status);
  g_node_states[node].yielded = g_shim_yielded != 0 ? 1 : 0;
  g_node_states[node].backlog_remaining = g_shim_backlog_remaining;
  g_shim_inputs.clear();
  return static_cast<int32_t>(routed);
}

#ifdef SDN_FLATSQL_LINKED
// ============================================================================
// Direct FlatSQL engine linkage (loop C.7 — the B-iv end state). Compiled ONLY
// into linked-mode flows (`flow.engineLinkage == "flatsql"`); inert otherwise.
// Vendored verbatim from upstream flow_runtime.cpp (kept for fidelity; the node
// bake does not currently define SDN_FLATSQL_LINKED).
// ============================================================================

#define FSL_IMPORT(mod, name) \
  extern "C" __attribute__((import_module(mod), import_name(name)))

FSL_IMPORT("flatsql", "malloc") uint32_t fsl_engine_malloc(uint32_t size);
FSL_IMPORT("flatsql", "free") void fsl_engine_free(uint32_t ptr);
FSL_IMPORT("flatsql", "flatsql_query_raw_flatbuffer_stream")
int32_t fsl_engine_query_raw(int32_t handle, uint32_t sql_ptr, uint32_t param_ptr,
                             uint32_t param_len, int32_t param_count);
FSL_IMPORT("flatsql", "flatsql_response_artifact_data") uint32_t fsl_engine_artifact_data(void);
FSL_IMPORT("flatsql", "flatsql_response_artifact_size") int32_t fsl_engine_artifact_size(void);
FSL_IMPORT("flatsql", "flatsql_response_artifact_row_count") double fsl_engine_artifact_rows(void);
FSL_IMPORT("flatsql", "flatsql_response_artifact_column_count") double fsl_engine_artifact_cols(void);
FSL_IMPORT("flatsql", "flatsql_response_artifact_cache_hit") int32_t fsl_engine_artifact_cache_hit(void);
FSL_IMPORT("flatsql", "flatsql_query_cache_generation") double fsl_engine_generation(int32_t handle);
FSL_IMPORT("flatsql", "flatsql_get_error") uint32_t fsl_engine_get_error(void);
FSL_IMPORT("flatsql_link", "peek8") uint32_t fsl_link_peek8(uint32_t addr);
FSL_IMPORT("flatsql_link", "peek64") uint64_t fsl_link_peek64(uint32_t addr);
FSL_IMPORT("flatsql_link", "poke8") void fsl_link_poke8(uint32_t addr, uint32_t value);
FSL_IMPORT("flatsql_link", "fnv1a64") uint64_t fsl_link_fnv1a64(uint32_t addr, uint32_t len);
FSL_IMPORT("flatsql_link", "count_frames") int32_t fsl_link_count_frames(uint32_t addr, uint32_t len);

struct SdnFlatsqlLinkedResult {
  uint64_t generation;
  uint64_t fnv1a64;
  uint64_t token;
  uint32_t engine_ptr;
  uint32_t size;
  int32_t rows;
  int32_t cols;
  int32_t cache_hit;
  int32_t frames;
};
static_assert(sizeof(SdnFlatsqlLinkedResult) == 48, "SdnFlatsqlLinkedResult must be 48 bytes");

struct SdnEngineRefEntry {
  uint64_t token;
  uint64_t generation;
  uint64_t fnv1a64;
  uint32_t engine_ptr;
  uint32_t size;
  uint32_t frames;
  uint32_t used;
};
static_assert(sizeof(SdnEngineRefEntry) == 40, "SdnEngineRefEntry must be 40 bytes");

static constexpr uint32_t kSdnEngineRefSlots = 8;
static constexpr uint64_t kSdnEngineRefTokenMagic = 0x53444E45ull << 32;  // "SDNE"

static SdnEngineRefEntry g_engine_refs[kSdnEngineRefSlots];
static uint64_t g_engine_ref_counter = 0;
static int32_t g_fsl_db_handle = 0;
static char g_fsl_error[512];

static void sdn_flatsql_link_reset_refs(void) {
  memset(g_engine_refs, 0, sizeof(g_engine_refs));
}

FLOW_EXPORT void sdn_flatsql_link_init(int32_t db_handle) { g_fsl_db_handle = db_handle; }
FLOW_EXPORT uint32_t sdn_flatsql_link_ref_table(void) {
  return static_cast<uint32_t>(reinterpret_cast<uintptr_t>(&g_engine_refs[0]));
}
FLOW_EXPORT uint32_t sdn_flatsql_link_ref_slots(void) { return kSdnEngineRefSlots; }

extern "C" uint32_t sdn_flatsql_linked_available(void) { return g_fsl_db_handle != 0 ? 1u : 0u; }
extern "C" const char *sdn_flatsql_linked_error(void) { return g_fsl_error; }

static void fsl_set_error_from_engine(const char *fallback) {
  uint32_t ptr = fsl_engine_get_error();
  uint32_t i = 0;
  if (ptr != 0) {
    for (; i < sizeof(g_fsl_error) - 1; i++) {
      uint32_t b = fsl_link_peek8(ptr + i);
      if (b == 0) break;
      g_fsl_error[i] = static_cast<char>(b);
    }
  }
  if (i == 0 && fallback != nullptr) {
    strncpy(g_fsl_error, fallback, sizeof(g_fsl_error) - 1);
    i = static_cast<uint32_t>(strlen(g_fsl_error));
  }
  g_fsl_error[i] = '\0';
}

static void fsl_copy_to_engine(uint32_t dst, const uint8_t *src, uint32_t len) {
  for (uint32_t i = 0; i < len; i++) fsl_link_poke8(dst + i, src[i]);
}

extern "C" int32_t sdn_flatsql_linked_read(uint8_t *dst, uint32_t engine_ptr, uint32_t len) {
  uint32_t i = 0;
  for (; i + 8 <= len; i += 8) {
    uint64_t w = fsl_link_peek64(engine_ptr + i);
    memcpy(dst + i, &w, 8);
  }
  for (; i < len; i++) dst[i] = static_cast<uint8_t>(fsl_link_peek8(engine_ptr + i));
  return 0;
}

static uint64_t fsl_local_fnv(const uint8_t *data, uint32_t len) {
  uint64_t hash = 1469598103934665603ull;
  for (uint32_t i = 0; i < len; i++) {
    hash ^= data[i];
    hash *= 1099511628211ull;
  }
  return hash;
}

struct FslEtagCacheEntry {
  uint64_t key_sql;
  uint64_t key_params;
  uint64_t generation;
  uint64_t fnv;
  int32_t frames;
  uint32_t valid;
};
static constexpr uint32_t kFslEtagCacheSlots = 8;
static FslEtagCacheEntry g_fsl_etag_cache[kFslEtagCacheSlots];
static uint32_t g_fsl_etag_next = 0;

extern "C" int32_t sdn_flatsql_linked_query_raw_stream(
    const char *sql, uint32_t sql_len, const uint8_t *params_tlv, uint32_t tlv_len,
    uint32_t param_count, int32_t want_ref, SdnFlatsqlLinkedResult *out) {
  g_fsl_error[0] = '\0';
  if (out == nullptr) return -1;
  memset(out, 0, sizeof(*out));
  if (g_fsl_db_handle == 0) {
    strncpy(g_fsl_error, "flatsql linkage not initialized (sdn_flatsql_link_init not called)",
            sizeof(g_fsl_error) - 1);
    return -2;
  }

  uint32_t sql_ptr = fsl_engine_malloc(sql_len + 1);
  if (sql_ptr == 0) {
    strncpy(g_fsl_error, "engine malloc failed for sql", sizeof(g_fsl_error) - 1);
    return -3;
  }
  fsl_copy_to_engine(sql_ptr, reinterpret_cast<const uint8_t *>(sql), sql_len);
  fsl_link_poke8(sql_ptr + sql_len, 0);

  uint32_t tlv_ptr = 0;
  if (tlv_len > 0) {
    tlv_ptr = fsl_engine_malloc(tlv_len);
    if (tlv_ptr == 0) {
      fsl_engine_free(sql_ptr);
      strncpy(g_fsl_error, "engine malloc failed for params", sizeof(g_fsl_error) - 1);
      return -3;
    }
    fsl_copy_to_engine(tlv_ptr, params_tlv, tlv_len);
  }

  const int32_t ok = fsl_engine_query_raw(g_fsl_db_handle, sql_ptr, tlv_ptr, tlv_len,
                                          static_cast<int32_t>(param_count));
  fsl_engine_free(sql_ptr);
  if (tlv_ptr != 0) fsl_engine_free(tlv_ptr);
  if (ok == 0) {
    fsl_set_error_from_engine("flatsql_query_raw_flatbuffer_stream failed");
    return -4;
  }

  out->engine_ptr = fsl_engine_artifact_data();
  out->size = static_cast<uint32_t>(fsl_engine_artifact_size());
  out->rows = static_cast<int32_t>(fsl_engine_artifact_rows());
  out->cols = static_cast<int32_t>(fsl_engine_artifact_cols());
  out->cache_hit = fsl_engine_artifact_cache_hit();
  out->generation = static_cast<uint64_t>(fsl_engine_generation(g_fsl_db_handle));

  if (want_ref != 0) {
    const uint64_t key_sql = fsl_local_fnv(reinterpret_cast<const uint8_t *>(sql), sql_len);
    const uint64_t key_params = tlv_len > 0 ? fsl_local_fnv(params_tlv, tlv_len) : 0;
    FslEtagCacheEntry *hit = nullptr;
    for (uint32_t i = 0; i < kFslEtagCacheSlots; i++) {
      FslEtagCacheEntry &entry = g_fsl_etag_cache[i];
      if (entry.valid && entry.key_sql == key_sql && entry.key_params == key_params &&
          entry.generation == out->generation) {
        hit = &entry;
        break;
      }
    }
    if (hit != nullptr) {
      out->fnv1a64 = hit->fnv;
      out->frames = hit->frames;
    } else {
      out->fnv1a64 = fsl_link_fnv1a64(out->engine_ptr, out->size);
      out->frames = fsl_link_count_frames(out->engine_ptr, out->size);
      FslEtagCacheEntry &slot = g_fsl_etag_cache[g_fsl_etag_next % kFslEtagCacheSlots];
      g_fsl_etag_next++;
      slot.key_sql = key_sql;
      slot.key_params = key_params;
      slot.generation = out->generation;
      slot.fnv = out->fnv1a64;
      slot.frames = out->frames;
      slot.valid = 1;
    }

    g_engine_ref_counter++;
    const uint64_t token = kSdnEngineRefTokenMagic | (g_engine_ref_counter & 0xFFFFFFFFull);
    SdnEngineRefEntry &ref = g_engine_refs[g_engine_ref_counter % kSdnEngineRefSlots];
    ref.token = token;
    ref.generation = out->generation;
    ref.fnv1a64 = out->fnv1a64;
    ref.engine_ptr = out->engine_ptr;
    ref.size = out->size;
    ref.frames = static_cast<uint32_t>(out->frames < 0 ? 0 : out->frames);
    ref.used = 1;
    out->token = token;
  }
  return 0;
}
#endif  // SDN_FLATSQL_LINKED
