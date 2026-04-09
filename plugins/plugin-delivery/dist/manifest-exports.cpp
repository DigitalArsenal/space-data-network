
#include <stddef.h>
#include <stdint.h>
static const uint8_t g_manifest[] = {0x00};
extern "C" {
__attribute__((visibility("default")))
const uint8_t* plugin_get_manifest_flatbuffer() { return g_manifest; }
__attribute__((visibility("default")))
uint32_t plugin_get_manifest_flatbuffer_size() { return 0; }
}
