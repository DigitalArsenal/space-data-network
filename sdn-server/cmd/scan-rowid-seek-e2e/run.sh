#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
unset CGO_CFLAGS CGO_LDFLAGS CGO_CXXFLAGS
export WASMEDGE_DIR="$HOME/.sdn-wasmedge-static-macos-patched/prefix"
export GOPROXY=off GOSUMDB=off
# Use the already installed pinned toolchain without the Go launcher's network
# checksum lookup. A sufficiently recent local Go also works without a cache.
export GOTOOLCHAIN=local
e2e_toolchain="$(awk '$1 == "toolchain" { print $2; exit }' sdn-server/go.mod)"
e2e_go_cache="$(go env GOPATH)/pkg/mod/golang.org/toolchain@v0.0.1-${e2e_toolchain}.$(go env GOHOSTOS)-$(go env GOHOSTARCH)/bin"
if [[ -x "$e2e_go_cache/go" ]]; then
  export PATH="$e2e_go_cache:$PATH"
fi
export SDN_FLATSQL_SLOW_QUERY_MS=1
export GOLOG_LOG_LEVEL=warn
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/scan-rowid-seek-e2e-bin.XXXXXX")"
trap 'rm -rf "$e2e_dir"' EXIT
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/scan-rowid-seek-e2e" ./cmd/scan-rowid-seek-e2e
"$e2e_dir/scan-rowid-seek-e2e" "$@"
