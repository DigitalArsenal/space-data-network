#!/usr/bin/env bash
# test-docker.sh — run the Go test suites inside the release build container.
#
# Tests execute in the SAME environment the linux/amd64 prod binary is built
# in (golang-bookworm + the patched WasmEdge from Dockerfile.linux), so
# wasm-runtime behavior under test matches what ships. GitHub Actions is
# owner-banned; this and ci-local.sh are the only CI.
#
# Usage:
#   scripts/test-docker.sh                      # sdn-server + kubo/sdn suites
#   scripts/test-docker.sh sdn-server ./internal/storage/
#   scripts/test-docker.sh kubo ./sdn/... -run TestFoo
#
# First run builds the test image (WasmEdge compiles from source — slow once,
# cached after). Go build/module caches persist in named volumes.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOCKERFILE="$ROOT/kubo/sdn/build/release/Dockerfile.linux"
IMAGE="sdn-test-env:wasmedge-0.14.1"
PLATFORM="${SDN_TEST_PLATFORM:-linux/amd64}"

log() { printf '\033[0;36m[test-docker]\033[0m %s\n' "$*"; }

if ! docker image inspect "$IMAGE" >/dev/null 2>&1 || [ "${SDN_TEST_REBUILD:-0}" = "1" ]; then
  log "building test image ($PLATFORM) — first build compiles WasmEdge, be patient"
  docker buildx build --platform "$PLATFORM" --target test-env \
    --load -t "$IMAGE" -f "$DOCKERFILE" "$ROOT/kubo"
fi

run_suite() {
  local module_dir="$1"; shift
  log "go test ($module_dir) $*"
  docker run --rm --platform "$PLATFORM" \
    -v "$ROOT":/src \
    -v sdn-test-gomod:/go/pkg/mod \
    -v sdn-test-gocache:/root/.cache/go-build \
    -w "/src/$module_dir" \
    "$IMAGE" go test "$@"
}

MODULE="${1:-all}"
case "$MODULE" in
  sdn-server)
    shift; run_suite sdn-server "${@:-./...}" ;;
  kubo)
    shift; run_suite kubo "${@:-./sdn/... ./plugin/plugins/sdnruntime/}" ;;
  all|"")
    run_suite sdn-server ./...
    run_suite kubo ./sdn/... ./plugin/plugins/sdnruntime/ ;;
  *)
    echo "usage: scripts/test-docker.sh [all|sdn-server|kubo] [go test args]" >&2
    exit 1 ;;
esac
log "done"
