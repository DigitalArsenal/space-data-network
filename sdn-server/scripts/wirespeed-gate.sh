#!/usr/bin/env bash
# Loop C.5 — wirespeed gate (CI-runnable).
#
# Seeds a FlatSQL store through the normal ingest path, mounts the REAL
# data-retrieval flow bundle at /api/v1/data/, and measures:
#   (a) the flow-served streaming endpoint,
#   (b) a baseline handler streaming the same pre-materialized aligned bytes
#       over the same net/http stack,
#   (c) a raw-TCP wire_speed_probe reference.
# Prints the throughput table and exits NONZERO when (a) < 99% of (b).
#
# Known-miss override (CLEARLY LABELED): loop C.5b landed AOT flow dispatch
# (nested AOT-in-AOT WasmEdge fix) + the engine raw-stream response cache;
# warm flow requests measure ~37% of the same-bytes baseline (7 ms vs
# 2.6 ms / 8.6 MB). The remaining gap is per-request byte copies in the
# flow-runtime template (C.3(c3) direct-linkage/zero-copy egress work).
#   SDN_C5_ALLOW_BLOCKED=1 scripts/wirespeed-gate.sh
#
# Tunables (see wirespeed_gate_test.go): SDN_C5_OBJECTS, SDN_C5_EPOCHS,
# SDN_C5_LIMIT, SDN_C5_RUNS, SDN_C5_WARMUP, SDN_C5_POOL, SDN_C5_PAGES,
# SDN_C5_AOT, SDN_DATA_RETRIEVAL_FLOW_DIST.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SDN_SERVER_DIR="$(dirname "$SCRIPT_DIR")"

# WasmEdge CGO env (same conventions as the rest of the repo); respect
# anything the caller already exported.
WASMEDGE_ROOT="${WASMEDGE_ROOT:-$HOME/.wasmedge}"
export CGO_CFLAGS="${CGO_CFLAGS:--I$WASMEDGE_ROOT/include}"
export CGO_LDFLAGS="${CGO_LDFLAGS:--L$WASMEDGE_ROOT/lib -lwasmedge -Wl,-rpath,$WASMEDGE_ROOT/lib}"
if [[ "$(uname)" == "Darwin" ]]; then
  export DYLD_FALLBACK_LIBRARY_PATH="${DYLD_FALLBACK_LIBRARY_PATH:-$WASMEDGE_ROOT/lib}"
else
  export LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-$WASMEDGE_ROOT/lib}"
fi

cd "$SDN_SERVER_DIR"
SDN_C5_WIRESPEED=1 exec go test ./internal/flowrt/ -run TestWirespeedGate -v -count=1 -timeout 60m "$@"
