#!/usr/bin/env bash
# Cut a Space Data Network release from this machine (build locally, ship
# binaries): one self-contained bundle per target, a checksums file, and —
# with --publish — a GitHub pre-release the public installers resolve.
#
#   deployment/release/cut-release-local.sh --version 1.0.4-beta.18 \
#     [--targets darwin-arm64,linux-amd64] [--publish] [--notes FILE]
#
# Targets:
#   darwin-arm64   built natively on this Mac with the host WasmEdge SDK
#   linux-amd64    built in the release Docker image (linux/amd64); the
#                  WasmEdge runtime comes out of the same image
#   linux-arm64    same, linux/arm64
#
# Every bundle carries the node binary, WasmEdge, Kubo, the UI assets, the
# updater module, the HD wallet module, the wallet sign-in assets and the
# fleet update trust roots — build-self-contained-cli.mjs decides the layout.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KUBO_VERSION="${KUBO_VERSION:-v0.39.0}"
WASMEDGE_VERSION="${WASMEDGE_VERSION:-0.16.4}"
HOST_WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.local/share/spacedatanetwork/wasmedge-sdk/${WASMEDGE_VERSION}-darwin-arm64}"
DOCKER_IMAGE_PREFIX="${DOCKER_IMAGE_PREFIX:-sdn-release-build}"

version=""
targets="darwin-arm64,linux-amd64"
publish=0
notes=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --targets) targets="${2:-}"; shift 2 ;;
    --publish) publish=1; shift ;;
    --notes) notes="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
version="${version#v}"
if [[ -z "$version" || ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "--version must be a semver like 1.0.4-beta.18 (got '${version}')" >&2
  exit 2
fi
tag="v${version}"
commit="$(git -C "$root" rev-parse HEAD)"
if [[ "$publish" == 1 && -n "$(git -C "$root" status --porcelain --untracked-files=no)" ]]; then
  echo "refusing to publish from a dirty tree: commit or stash first (a release names one commit)" >&2
  exit 1
fi
short="${commit:0:8}"

dist="$root/dist/release-local/$version"
rm -rf "$dist"
mkdir -p "$dist/inputs" "$dist/out"
log() { printf '[cut-release] %s\n' "$*"; }

# --- shared inputs ---------------------------------------------------------
sdn_ui="$root/sdn-js/ui/dist"
webui="$root/webui/build"
updater_wasm="$root/packages/sdn-updater-module/dist/isomorphic/module.wasm"
hd_wallet_wasm="$root/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm"
for required in "$sdn_ui/index.html" "$webui/index.html" "$updater_wasm" "$hd_wallet_wasm" "$root/LICENSE" "$root/README.md"; do
  [[ -e "$required" ]] || { echo "missing release input: $required" >&2; exit 1; }
done
log "staging wallet sign-in assets"
"$root/deployment/wallet-wasm/stage-wallet-wasm.sh" "$dist/inputs/wallet-wasm" "$dist/inputs/wallet-ui" >/dev/null
test -f "$dist/inputs/wallet-wasm/runtime/index.mjs"
test -f "$dist/inputs/wallet-ui/compat/index.js"

download_kubo() { # <platform>
  local platform="$1" out="$dist/inputs/kubo/$1"
  if [[ ! -x "$out/kubo/ipfs" ]]; then
    log "downloading Kubo ${KUBO_VERSION} for ${platform}"
    "$root/deployment/release/download-kubo.sh" --version "$KUBO_VERSION" --platform "$platform" --archive tar.gz --output-dir "$out" >/dev/null
  fi
  test -x "$out/kubo/ipfs"
}

build_native_darwin_arm64() {
  local out="$dist/inputs/bin/darwin-arm64"
  mkdir -p "$out"
  [[ -d "$HOST_WASMEDGE_DIR/lib" ]] || { echo "host WasmEdge SDK not found at $HOST_WASMEDGE_DIR" >&2; exit 1; }
  log "building darwin/arm64 binary with WasmEdge ${WASMEDGE_VERSION}"
  (cd "$root/sdn-server" && WASMEDGE_DIR="$HOST_WASMEDGE_DIR" GOOS=darwin GOARCH=arm64 "$root/scripts/go-with-wasmedge.sh" build -ldflags="-s -w -X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=${tag}" -o "$out/spacedatanetwork" ./cmd/spacedatanetwork)
  echo "$HOST_WASMEDGE_DIR"
}

build_docker_linux() { # <arch>
  local arch="$1" out="$dist/inputs/bin/linux-$1" image="${DOCKER_IMAGE_PREFIX}:linux-$1"
  mkdir -p "$out" "$dist/inputs/wasmedge/linux-$arch"
  log "building linux/${arch} binary in Docker (${image})"
  docker buildx build --platform "linux/${arch}" --target builder --load -t "$image" --build-arg "SDN_RELEASE_TAG=${tag}" -f "$root/deployment/docker/Dockerfile" "$root" >/dev/null
  local container
  container="$(docker create --platform "linux/${arch}" "$image")"
  docker cp "$container:/out/spacedatanetwork" "$out/spacedatanetwork"
  docker cp "$container:/root/.wasmedge/." "$dist/inputs/wasmedge/linux-$arch/"
  docker rm "$container" >/dev/null
  chmod +x "$out/spacedatanetwork"
  echo "$dist/inputs/wasmedge/linux-$arch"
}

bundle() { # <os> <arch> <binary> <wasmedge-dir> <kubo-platform>
  local os="$1" arch="$2" binary="$3" wasmedge="$4" kubo_platform="$5"
  download_kubo "$kubo_platform"
  log "bundling ${os}/${arch}"
  node "$root/deployment/release/build-self-contained-cli.mjs" \
    --version "$version" --os "$os" --arch "$arch" --channel beta \
    --output-dir "$dist/out" \
    --binary-path "$binary" \
    --kubo-path "$dist/inputs/kubo/$kubo_platform/kubo/ipfs" \
    --sdnUIPath "$sdn_ui" --webUIPath "$webui" \
    --updater-wasm-path "$updater_wasm" \
    --hd-wallet-wasm-path "$hd_wallet_wasm" \
    --wallet-wasm-path "$dist/inputs/wallet-wasm" \
    --wallet-ui-path "$dist/inputs/wallet-ui" \
    --wasmedge-path "$wasmedge" \
    --license-path "$root/LICENSE" --readme-path "$root/README.md" \
    --manifest-signature "local:${tag}:${commit}"
}

IFS=',' read -r -a target_list <<< "$targets"
for target in "${target_list[@]}"; do
  case "$target" in
    darwin-arm64)
      wasmedge="$(build_native_darwin_arm64 | tail -1)"
      bundle darwin arm64 "$dist/inputs/bin/darwin-arm64/spacedatanetwork" "$wasmedge" darwin-arm64 ;;
    linux-amd64)
      wasmedge="$(build_docker_linux amd64 | tail -1)"
      bundle linux amd64 "$dist/inputs/bin/linux-amd64/spacedatanetwork" "$wasmedge" linux-amd64 ;;
    linux-arm64)
      wasmedge="$(build_docker_linux arm64 | tail -1)"
      bundle linux arm64 "$dist/inputs/bin/linux-arm64/spacedatanetwork" "$wasmedge" linux-arm64 ;;
    *) echo "unsupported target: $target" >&2; exit 2 ;;
  esac
done

# --- checksums + smoke -------------------------------------------------------
(cd "$dist/out" && rm -f spacedatanetwork-checksums.txt && for f in spacedatanetwork-${version}-*.tar.gz spacedatanetwork-${version}-*.zip; do
  [[ -f "$f" ]] && shasum -a 256 "$f" >> spacedatanetwork-checksums.txt
done; true)
log "artifacts:"; ls -la "$dist/out" | grep -E "\.(tar\.gz|zip|txt)$" | awk '{print "  " $5, $9}'

if [[ ",$targets," == *",darwin-arm64,"* && "$(uname -sm)" == "Darwin arm64" ]]; then
  smoke="$(mktemp -d)"
  tar -xzf "$dist/out/spacedatanetwork-${version}-darwin-arm64.tar.gz" -C "$smoke"
  log "smoke: $("$smoke/spacedatanetwork-${version}-darwin-arm64/bin/spacedatanetwork" version | head -1)"
  test -f "$smoke/spacedatanetwork-${version}-darwin-arm64/trust/update-roots.json"
  test -f "$smoke/spacedatanetwork-${version}-darwin-arm64/runtime/ui/wallet-ui/compat/index.js"
  rm -rf "$smoke"
fi

for linux_arch in amd64 arm64; do
  if [[ ",$targets," == *",linux-${linux_arch},"* ]] && command -v docker >/dev/null; then
    b="spacedatanetwork-${version}-linux-${linux_arch}"
    log "smoke (linux/${linux_arch} in a clean Debian container)"
    docker run --rm --platform "linux/${linux_arch}" -v "$dist/out:/rel:ro" debian:bookworm-slim sh -c \
      "set -e; mkdir -p /tmp/s && tar -xzf /rel/${b}.tar.gz -C /tmp/s && /tmp/s/${b}/bin/spacedatanetwork version | head -1 && test -f /tmp/s/${b}/trust/update-roots.json && /tmp/s/${b}/runtime/kubo/ipfs version && test -f /tmp/s/${b}/runtime/ui/wallet-ui/compat/index.js" \
      | sed 's/^/[cut-release]   /'
  fi
done

# --- publish -----------------------------------------------------------------
if [[ "$publish" == 1 ]]; then
  log "publishing GitHub pre-release ${tag} (target ${short})"
  args=(release create "$tag" --prerelease --target main --title "Space Data Network ${tag}")
  if [[ -n "$notes" ]]; then args+=(--notes-file "$notes"); else args+=(--notes "Space Data Network ${tag} — built locally from ${commit}. Install: curl https://spacedatanetwork.org/install.sh | bash"); fi
  gh "${args[@]}" "$dist"/out/spacedatanetwork-${version}-*.tar.gz "$dist"/out/spacedatanetwork-checksums.txt
  log "published: https://github.com/DigitalArsenal/space-data-network/releases/tag/${tag}"
else
  log "dry run complete (no --publish); artifacts under $dist/out"
fi
