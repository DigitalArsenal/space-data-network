#!/usr/bin/env bash
# Build ONE self-contained node bundle natively on this machine.
#
#   deployment/release/build-local-node-bundle.sh --version 1.0.5-beta.1 [--output-dir DIR]
#
# These are cut-release-local.sh's darwin steps, factored out so something
# other than a release can ask for a bundle: the desktop app packages one, and
# it has no business cutting a release to get it. cut-release-local.sh is still
# the script that cuts releases; this one builds a single local bundle and
# prints the directory it staged.
#
# One difference from the release lane, and it is deliberate: the dashboard is
# compiled into the node binary, so `sdn-js/ui/dist` no longer exists in this
# repo. build-self-contained-cli.mjs still wants a directory for it (and for the
# IPFS web UI), so anything the repo actually built is used and an empty
# directory stands in for what it did not.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KUBO_VERSION="${KUBO_VERSION:-v0.39.0}"
WASMEDGE_VERSION="${WASMEDGE_VERSION:-0.16.4}"

os="$(uname -s)"
arch="$(uname -m)"
case "${os}/${arch}" in
  Darwin/arm64) target_os=darwin; target_arch=arm64; kubo_platform=darwin-arm64 ;;
  Darwin/x86_64) target_os=darwin; target_arch=amd64; kubo_platform=darwin-amd64 ;;
  Linux/x86_64) target_os=linux; target_arch=amd64; kubo_platform=linux-amd64 ;;
  Linux/aarch64) target_os=linux; target_arch=arm64; kubo_platform=linux-arm64 ;;
  *) echo "build-local-node-bundle.sh does not build ${os}/${arch} natively" >&2; exit 2 ;;
esac

HOST_WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.local/share/spacedatanetwork/wasmedge-sdk/${WASMEDGE_VERSION}-${target_os}-${target_arch}}"

version=""
output_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
version="${version#v}"
if [[ -z "$version" || ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "--version must be a semver like 1.0.5-beta.1 (got '${version}')" >&2
  exit 2
fi
output_dir="${output_dir:-$root/dist/local-node-bundle/$version}"
commit="$(git -C "$root" rev-parse HEAD)"
tag="v${version}"

log() { printf '[node-bundle] %s\n' "$*" >&2; }

inputs="$output_dir/inputs"
rm -rf "$inputs"
mkdir -p "$inputs" "$output_dir/out"

# --- the binary -------------------------------------------------------------
[[ -d "$HOST_WASMEDGE_DIR/lib" ]] || { echo "host WasmEdge SDK not found at $HOST_WASMEDGE_DIR" >&2; exit 1; }
log "building ${target_os}/${target_arch} with WasmEdge ${WASMEDGE_VERSION}"
mkdir -p "$inputs/bin"
(cd "$root/sdn-server" && WASMEDGE_DIR="$HOST_WASMEDGE_DIR" GOOS="$target_os" GOARCH="$target_arch" \
  "$root/scripts/go-with-wasmedge.sh" build \
  -ldflags="-s -w -X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=${tag}" \
  -o "$inputs/bin/spacedatanetwork" ./cmd/spacedatanetwork)

# --- Kubo -------------------------------------------------------------------
if [[ ! -x "$inputs/kubo/kubo/ipfs" ]]; then
  log "downloading Kubo ${KUBO_VERSION} for ${kubo_platform}"
  "$root/deployment/release/download-kubo.sh" --version "$KUBO_VERSION" --platform "$kubo_platform" \
    --archive tar.gz --output-dir "$inputs/kubo" >/dev/null
fi

# --- wallet sign-in assets --------------------------------------------------
log "staging wallet sign-in assets"
"$root/deployment/wallet-wasm/stage-wallet-wasm.sh" "$inputs/wallet-wasm" "$inputs/wallet-ui" >/dev/null

# --- the UI trees the bundle layout still names -----------------------------
sdn_ui="$root/sdn-js/ui/dist"
if [[ ! -f "$sdn_ui/index.html" ]]; then
  log "no sdn-js/ui/dist in this repo; the node serves its compiled-in dashboard"
  sdn_ui="$inputs/empty-sdn-ui"; mkdir -p "$sdn_ui"
fi
webui="$root/webui/build"
if [[ ! -f "$webui/index.html" ]]; then
  log "no webui/build in this repo; staging an empty IPFS web UI"
  webui="$inputs/empty-webui"; mkdir -p "$webui"
fi

updater_wasm="$root/packages/sdn-updater-module/dist/isomorphic/module.wasm"
hd_wallet_wasm="$root/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm"
[[ -f "$hd_wallet_wasm" ]] || hd_wallet_wasm="$root/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm"
for required in "$updater_wasm" "$hd_wallet_wasm" "$root/LICENSE" "$root/README.md"; do
  [[ -e "$required" ]] || { echo "missing bundle input: $required" >&2; exit 1; }
done

# --- stage ------------------------------------------------------------------
log "bundling ${target_os}/${target_arch} ${version}"
# --channel release: the desktop app ships this bundle to users, so its
# updater must read the public feed and never the internal fleet dev lane
# (see build-self-contained-cli.mjs).
node "$root/deployment/release/build-self-contained-cli.mjs" \
  --version "$version" --os "$target_os" --arch "$target_arch" --channel release \
  --output-dir "$output_dir/out" \
  --binary-path "$inputs/bin/spacedatanetwork" \
  --kubo-path "$inputs/kubo/kubo/ipfs" \
  --sdnUIPath "$sdn_ui" --webUIPath "$webui" \
  --updater-wasm-path "$updater_wasm" \
  --hd-wallet-wasm-path "$hd_wallet_wasm" \
  --wallet-wasm-path "$inputs/wallet-wasm" \
  --wallet-ui-path "$inputs/wallet-ui" \
  --wasmedge-path "$HOST_WASMEDGE_DIR" \
  --license-path "$root/LICENSE" --readme-path "$root/README.md" \
  --manifest-signature "local:${tag}:${commit}" >/dev/null

bundle="$output_dir/out/spacedatanetwork-${version}-${target_os}-${target_arch}"
test -f "$bundle/manifest.json"
log "staged $bundle"
printf '%s\n' "$bundle"
