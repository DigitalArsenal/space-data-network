#ifndef SPACE_DATA_MODULE_INVOKE_H
#define SPACE_DATA_MODULE_INVOKE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

enum plugin_payload_wire_format_t {
  PLUGIN_PAYLOAD_WIRE_FORMAT_FLATBUFFER = 0,
  PLUGIN_PAYLOAD_WIRE_FORMAT_ALIGNED_BINARY = 1,
};

typedef struct plugin_input_frame_t {
  const char *port_id;
  const char *schema_name;
  const char *file_identifier;
  uint32_t wire_format;
  const char *root_type_name;
  uint16_t fixed_string_length;
  uint32_t byte_length;
  uint16_t required_alignment;
  uint16_t alignment;
  uint32_t size;
  uint32_t generation;
  uint64_t trace_id;
  uint32_t stream_id;
  uint64_t sequence;
  int32_t end_of_stream;
  const uint8_t *payload;
  uint32_t payload_length;
} plugin_input_frame_t;

uint32_t plugin_get_input_count(void);
const plugin_input_frame_t *plugin_get_input_frame(uint32_t index);
int32_t plugin_find_input_index(const char *port_id, uint32_t ordinal);

void plugin_reset_output_state(void);
int32_t plugin_push_output(
  const char *port_id,
  const char *schema_name,
  const char *file_identifier,
  const uint8_t *payload_ptr,
  uint32_t payload_length
);
int32_t plugin_push_output_typed(
  const char *port_id,
  const char *schema_name,
  const char *file_identifier,
  uint32_t wire_format,
  const char *root_type_name,
  uint16_t fixed_string_length,
  uint32_t byte_length,
  uint16_t required_alignment,
  const uint8_t *payload_ptr,
  uint32_t payload_length
);
int32_t plugin_push_output_ex(
  const char *port_id,
  const char *schema_name,
  const char *file_identifier,
  uint32_t wire_format,
  const char *root_type_name,
  uint16_t fixed_string_length,
  uint16_t required_alignment,
  const uint8_t *payload_ptr,
  uint32_t payload_length
);

void plugin_set_yielded(int32_t yielded);
void plugin_set_backlog_remaining(uint32_t backlog_remaining);
void plugin_set_error(const char *error_code, const char *error_message);

uint32_t plugin_alloc(uint32_t size);
void plugin_free(uint32_t ptr, uint32_t size);
uint32_t plugin_invoke_stream(
  uint32_t request_ptr,
  uint32_t request_len,
  uint32_t response_len_out_ptr
);

#ifdef __cplusplus
}
#endif

#endif
