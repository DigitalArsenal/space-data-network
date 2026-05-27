#include "space_data_module_invoke.h"

#include <stdint.h>
#include <string.h>

namespace {

int push_stub_result(const char *port_id) {
  const char *result = "{\"status\":\"stub\"}";
  const int32_t output_index = plugin_push_output(
      port_id,
      "raw.json",
      "JSON",
      reinterpret_cast<const uint8_t *>(result),
      static_cast<uint32_t>(strlen(result)));
  return output_index < 0 ? 1 : 0;
}

}  // namespace

extern "C" int checkForUpdates(void) {
  return push_stub_result("result");
}

extern "C" int planUpdate(void) {
  return push_stub_result("result");
}

extern "C" int fetchArtifact(void) {
  return push_stub_result("bytes");
}

extern "C" int verifyArtifact(void) {
  return push_stub_result("result");
}

extern "C" int stageArtifact(void) {
  return push_stub_result("result");
}

extern "C" int applyStaged(void) {
  return push_stub_result("result");
}

extern "C" int selfUpgrade(void) {
  return push_stub_result("result");
}

extern "C" int pollUpstream(void) {
  return push_stub_result("result");
}

extern "C" int buildManifest(void) {
  return push_stub_result("result");
}

extern "C" int signManifest(void) {
  return push_stub_result("result");
}

extern "C" int publishManifest(void) {
  return push_stub_result("result");
}
