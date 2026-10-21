#!/usr/bin/env bash
# Builds the node and this harness from the candidate (default WasmEdge
# environment), then runs the harness. Pass --binary <node> to skip the node
# build; other flags go to the harness.
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
export GOLOG_LOG_LEVEL=warn
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/dpm-manifest-e2e-bin.XXXXXX")"
trap 'rm -rf "$e2e_dir"' EXIT
args=("$@")
if [[ " $* " != *" --binary "* ]]; then
  ./scripts/go-with-wasmedge.sh build -o "$e2e_dir/spacedatanetwork" ./cmd/spacedatanetwork
  args+=(--binary "$e2e_dir/spacedatanetwork")
fi
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/dpm-manifest-e2e" ./cmd/dpm-manifest-e2e
"$e2e_dir/dpm-manifest-e2e" "${args[@]}"
