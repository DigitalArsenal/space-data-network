# stream-echo — guest test module for the generic byte-stream connector

Task `sdn-stream-connector`. Exercises `stream.open` / `stream.send` /
`stream.close` plus the host-push `on_stream_frame` delivery against a local
test websocket server, from INSIDE a guest WASM module.

## Status

- Host-side contract (both Go hosts + the browser shim) is fully enforced by
  `kubo/sdn/sdnservices/stream_cap_test.go`,
  `sdn-server/internal/modulert/caps/stream_test.go` (both run in the release
  WasmEdge container via `scripts/test-docker.sh`), and
  `sdn-js/src/stream-cap.test.ts`.
- This guest module compiles with the space-data-module-sdk CLI
  (`space-data-module build`, clang `wasm32-wasip1-threads` — isomorphic-
  pthreads law). Its compiled-artifact lane is gated on the module-SDK
  op-surface amendment (Janus ruling 2026-08-16: the guest-facing `stream.*`
  hostcall allowlist in `space-data-module-sdk` browserHost/nodeHost is
  Janus-owned; graph task `module-sdk-stream-op-surface`). When that lands,
  wire the compiled artifact into the docker harness the same way the
  licensing-module fixture is loaded (`kubo/sdn/testsupport`).

## Contract (byte-identical tri-runtime)

Capability grants: `tcp` (23), `tls` (20), `websocket` (22) — all sensitive,
operator-gated, re-checked per call per kind. Ops: `stream.open`,
`stream.send`, `stream.close` under the `stream` hostcall prefix. Inbound:
`on_stream_frame` envelope
`{handle, event: opened|frame|closed|error, data: b64, encoding: "base64",
seq, dropped, reason?}`. Limits: 8 handles, 1 MiB default / 16 MiB ceiling
frame, 256-deep drop-oldest queue with cumulative surfaced `dropped`, 5 min
idle timeout. Reconnect is module-side only.
