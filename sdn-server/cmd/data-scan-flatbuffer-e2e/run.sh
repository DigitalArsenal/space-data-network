#!/usr/bin/env bash
# Builds the node and this harness from the candidate (default WasmEdge
# environment), then runs the harness. Pass --binary <node> to skip the node
# build; other flags go to the harness.
#
# The seeded 1M-record store is a deterministic fixture: without --data it is
# seeded once per (storage code, harness, go.sum, WasmEdge) into
# $SDN_E2E_CACHE (default ~/Library/Caches/sdn-e2e) and every run works on an
# APFS clone of it, so a run spends its time on the scan, not on seeding.
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
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/data-scan-flatbuffer-e2e-bin.XXXXXX")"
trap 'rm -rf "$e2e_dir"' EXIT
args=("$@")
if [[ " $* " != *" --binary "* ]]; then
  ./scripts/go-with-wasmedge.sh build -o "$e2e_dir/spacedatanetwork" ./cmd/spacedatanetwork
  args+=(--binary "$e2e_dir/spacedatanetwork")
fi
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/data-scan-flatbuffer-e2e" ./cmd/data-scan-flatbuffer-e2e
if [[ " $* " != *" --data "* && " $* " != *" --seed-only "* ]]; then
  key="$( { find sdn-server/internal/storage -name '*.go' ! -name '*_test.go' -print0 | sort -z | xargs -0 cat; \
    cat sdn-server/cmd/data-scan-flatbuffer-e2e/main.go sdn-server/go.sum; echo "$WASMEDGE_DIR $*"; } | shasum -a 256 | cut -c1-16)"
  cache="${SDN_E2E_CACHE:-$HOME/Library/Caches/sdn-e2e}/data-scan-wxf-$key"
  if [[ ! -f "$cache/.seeded" ]]; then
    rm -rf "$cache.tmp" && mkdir -p "$(dirname "$cache")"
    "$e2e_dir/data-scan-flatbuffer-e2e" --seed-only --data "$cache.tmp" "${args[@]}"
    mv "$cache.tmp" "$cache"
  fi
  cp -c -R "$cache" "$e2e_dir/store"
  args+=(--data "$e2e_dir/store")
fi
"$e2e_dir/data-scan-flatbuffer-e2e" "${args[@]}"
