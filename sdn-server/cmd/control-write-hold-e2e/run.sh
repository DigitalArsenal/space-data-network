#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
unset CGO_CFLAGS CGO_LDFLAGS CGO_CXXFLAGS
export WASMEDGE_DIR="$HOME/.sdn-wasmedge-static-macos-patched/prefix"
export GOPROXY=off GOSUMDB=off
# Use an already downloaded toolchain directly; automatic selection would try
# to verify its module through the checksum service, which is offline here.
export GOTOOLCHAIN=local
e2e_go_version="$(awk '$1 == "toolchain" { print $2 }' sdn-server/go.mod)"
e2e_go_bin="$(go env GOMODCACHE)/golang.org/toolchain@v0.0.1-${e2e_go_version}.$(go env GOHOSTOS)-$(go env GOHOSTARCH)/bin"
if [[ -x "$e2e_go_bin/go" ]]; then
  export PATH="$e2e_go_bin:$PATH"
fi
printf 'runtime_prefix=%s\n' "$WASMEDGE_DIR"
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/control-write-hold-e2e.XXXXXX")"
e2e_cache="$(mktemp -d "$PWD/.control-write-build.XXXXXX")"
export GOCACHE="$e2e_cache"
trap 'rm -rf "$e2e_dir" "$e2e_cache"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export SDN_CONTROL_E2E_ROOT="$e2e_dir"
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/control-write-hold-e2e" ./cmd/control-write-hold-e2e
rm -rf "$e2e_cache"
"$e2e_dir/control-write-hold-e2e" "$@"
