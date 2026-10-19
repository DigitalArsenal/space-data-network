#!/usr/bin/env bash
# Builds this harness from the candidate (default WasmEdge environment) and
# runs it: two live loopback libp2p nodes with real FlatSQL stores and the
# production flatsql-sync 2.0.0, pubsub and /ws handlers. Prints one
# PASS/FAIL line per assertion; exits non-zero on any FAIL. Flags go to the
# harness (--work DIR keeps the stores, --records N sizes the lane,
# --js-client DIR reads with that sdn-js build; default: ./sdn-js when built).
set -euo pipefail
cd "$(dirname "$0")/../../.."
unset CGO_CFLAGS CGO_LDFLAGS CGO_CXXFLAGS
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.local/share/spacedatanetwork/wasmedge-sdk/0.16.4-darwin-arm64}"
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
e2e_toolchain="$(awk '$1 == "toolchain" { print $2; exit }' sdn-server/go.mod)"
e2e_go_cache="$(go env GOPATH)/pkg/mod/golang.org/toolchain@v0.0.1-${e2e_toolchain}.$(go env GOHOSTOS)-$(go env GOHOSTARCH)/bin"
if [[ -x "$e2e_go_cache/go" ]]; then
  export PATH="$e2e_go_cache:$PATH"
fi
export GOLOG_LOG_LEVEL="${GOLOG_LOG_LEVEL:-error}"
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/fb-sync-e2e-bin.XXXXXX")"
trap 'rm -rf "$e2e_dir"' EXIT
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/fb-sync-e2e" ./cmd/fb-sync-e2e
args=("$@")
if [[ " $* " != *" --work "* ]]; then
  args+=(--work "$e2e_dir/work")
fi
# (g) the sdn-js client leg reads the provider through the built sdn-js dist.
if [[ " $* " != *" --js-client "* ]]; then
  if [[ -f sdn-js/dist/ui/index.mjs ]] && command -v node >/dev/null; then
    args+=(--js-client "$PWD/sdn-js")
  else
    echo "SKIP g/js-client: sdn-js/dist is not built (npm --prefix sdn-js run build) or node is missing"
  fi
fi
"$e2e_dir/fb-sync-e2e" "${args[@]}"
