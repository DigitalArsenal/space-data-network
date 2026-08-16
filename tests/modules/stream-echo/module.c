/*
 * stream-echo — guest test module for the generic byte-stream connector
 * (task sdn-stream-connector).
 *
 * Contract exercised (identical tri-runtime; see
 * kubo/sdn/sdnservices/stream_cap.go for the authoritative envelope):
 *
 *   run_echo:
 *     1. hostcall "stream.open"  {"kind":"websocket","url":<from input>}
 *     2. hostcall "stream.send"  {"handle":h,"data":"stream-echo-probe","encoding":"utf8"}
 *     3. await on_stream_frame "frame" whose base64 payload decodes to the probe
 *     4. hostcall "stream.close" {"handle":h}
 *     5. await terminal "closed" event; report {opened,echoed,closed,dropped}
 *
 *   on_stream_frame: receives every host-push envelope
 *     {"handle","event":"opened"|"frame"|"closed"|"error",
 *      "data":<b64>,"encoding":"base64","seq","dropped","reason"?}
 *
 * BUILD: this source compiles with the space-data-module-sdk CLI
 * (isomorphic-pthreads law: clang wasm32-wasip1-threads, never emcc -pthread):
 *
 *   space-data-module build tests/modules/stream-echo --manifest manifest.json
 *
 * The compiled artifact + docker-harness wiring land with the module-SDK
 * op-surface amendment (Janus ruling 2026-08-16: the guest-facing stream.*
 * allowlist in space-data-module-sdk browserHost/nodeHost is Janus-owned and
 * filed as a follow-up graph task). Until that lands, the host-side contract
 * is enforced end-to-end by kubo/sdn/sdnservices/stream_cap_test.go and
 * sdn-server/internal/modulert/caps/stream_test.go, which run inside the
 * release WasmEdge container via scripts/test-docker.sh.
 */

#include <stdint.h>
#include <string.h>

#include "space_data_module_invoke.h"

/* Latched state written by on_stream_frame, read by run_echo's poll loop. */
static volatile int g_opened = 0;
static volatile int g_echoed = 0;
static volatile int g_closed = 0;

/* The host-push inbound sink — the fixed method name the host invokes
 * (streamInboundMethod). The payload is the JSON envelope; this test module
 * only needs coarse event classification, so it substring-matches. */
int on_stream_frame(void) {
  const plugin_input_frame_t *frame = plugin_get_input_frame(0);
  if (!frame || !frame->payload) return 0;
  const char *p = (const char *)frame->payload;
  size_t n = frame->payload_length;
  if (memmem(p, n, "\"event\":\"opened\"", 16)) g_opened = 1;
  /* "stream-echo-probe" base64 = c3RyZWFtLWVjaG8tcHJvYmU= */
  if (memmem(p, n, "c3RyZWFtLWVjaG8tcHJvYmU=", 24)) g_echoed = 1;
  if (memmem(p, n, "\"event\":\"closed\"", 16) ||
      memmem(p, n, "\"event\":\"error\"", 15))
    g_closed = 1;
  return 0;
}

/* run_echo drives open -> send -> (await echo) -> close -> (await terminal).
 * Input frame 0 carries the websocket URL as UTF-8 (the docker harness
 * supplies the local test server's ws:// address). */
int run_echo(void) {
  const plugin_input_frame_t *frame = plugin_get_input_frame(0);
  if (!frame) {
    plugin_set_error("missing-frame", "stream-echo needs the ws:// url frame");
    return 3;
  }

  char open_req[512];
  char url[384];
  size_t ulen = frame->payload_length < sizeof(url) - 1 ? frame->payload_length
                                                        : sizeof(url) - 1;
  memcpy(url, frame->payload, ulen);
  url[ulen] = 0;
  snprintf(open_req, sizeof(open_req),
           "{\"kind\":\"websocket\",\"url\":\"%s\"}", url);

  char handle[32];
  if (plugin_hostcall_json("stream.open", open_req, handle, sizeof(handle),
                           "result.handle") != 0) {
    plugin_set_error("open-failed", "stream.open refused");
    return 1;
  }

  char send_req[128];
  snprintf(send_req, sizeof(send_req),
           "{\"handle\":\"%s\",\"data\":\"stream-echo-probe\",\"encoding\":\"utf8\"}",
           handle);
  if (plugin_hostcall_json("stream.send", send_req, NULL, 0, NULL) != 0) {
    plugin_set_error("send-failed", "stream.send refused");
    return 1;
  }

  /* Await the echo push, then close and await the terminal event. */
  for (int i = 0; i < 500 && !g_echoed; i++) plugin_yield_ms(10);

  char close_req[64];
  snprintf(close_req, sizeof(close_req), "{\"handle\":\"%s\"}", handle);
  plugin_hostcall_json("stream.close", close_req, NULL, 0, NULL);
  for (int i = 0; i < 500 && !g_closed; i++) plugin_yield_ms(10);

  char report[128];
  snprintf(report, sizeof(report),
           "{\"opened\":%d,\"echoed\":%d,\"closed\":%d}", g_opened, g_echoed,
           g_closed);
  plugin_push_output("report", "json", "JSON", (const uint8_t *)report,
                     strlen(report));
  return (g_opened && g_echoed && g_closed) ? 0 : 2;
}
