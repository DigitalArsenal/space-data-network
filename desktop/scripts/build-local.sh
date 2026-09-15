#!/usr/bin/env bash
# Build the Space Data Network desktop app for this Mac (darwin/arm64).
#
#   bash desktop/scripts/build-local.sh [--node-bundle <dir|archive>] [--version <semver>]
#
# Three steps:
#   1. build the darwin-arm64 node bundle with
#      deployment/release/build-local-node-bundle.sh -- the same binary, Kubo,
#      WasmEdge and wallet-asset steps cut-release-local.sh runs for the darwin
#      target (or take a bundle someone else built with --node-bundle);
#   2. stage it into desktop/assets/sdn-node/;
#   3. run electron-builder for mac arm64.
#
# The result is desktop/dist/space-data-network-desktop-<version>-mac-arm64.dmg
# (and .zip). Nothing is published and nothing is signed with a real identity
# unless APPLE_ID / APPLE_APP_SPECIFIC_PASSWORD / APPLE_TEAM_ID are exported.
#
# Linux and Windows bundles are not built here. To cross-build them, run the
# desktop build inside the electron-builder images, giving each one the matching
# node bundle (they are per-architecture, so no image builds more than its own):
#
#   deployment/release/cut-release-local.sh --version <v> --targets linux-amd64
#   docker run --rm -v "$PWD":/project -w /project \
#     -e SDN_DESKTOP_BUNDLE_DIR=/project/dist/release-local/<v>/out/spacedatanetwork-<v>-linux-amd64 \
#     electronuserland/builder:wine \
#     bash -c 'npm --prefix desktop ci && node desktop/scripts/stage-node-bundle.mjs && \
#              npm --prefix desktop exec -- electron-builder --publish never --linux AppImage deb tar.xz'
#
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
desktop="${root}/desktop"
version="$(node -p "require('${desktop}/package.json').version")"
node_bundle="${SDN_DESKTOP_BUNDLE_DIR:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --node-bundle) node_bundle="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,28p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

log() { printf '[desktop-build] %s\n' "$*"; }

if [[ "$(uname -sm)" != "Darwin arm64" ]]; then
  echo "build-local.sh builds the darwin-arm64 app and must run on an Apple Silicon Mac" >&2
  exit 1
fi

# --- 1. the node bundle -----------------------------------------------------
if [[ -z "${node_bundle}" ]]; then
  # The bundle carries the updater module, which is a build output: a fresh
  # checkout has none, so build it with the command the release lane uses.
  if [[ ! -f "${root}/packages/sdn-updater-module/dist/isomorphic/module.wasm" ]]; then
    log "building the updater module wasm"
    [[ -d "${root}/node_modules" ]] || npm --prefix "${root}" ci --no-audit --fund=false
    npm --prefix "${root}/packages/sdn-updater-module" run build
  fi
  # The HD wallet wasm ships with sdn-js, which the bundle reads out of
  # node_modules rather than building.
  if [[ ! -f "${root}/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" && \
        ! -f "${root}/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" ]]; then
    log "installing sdn-js dependencies for the HD wallet module"
    npm --prefix "${root}/sdn-js" ci --no-audit --fund=false
  fi

  log "building the darwin-arm64 node bundle ${version}"
  node_bundle="$("${root}/deployment/release/build-local-node-bundle.sh" --version "${version}" | tail -1)"
fi

# --- 2. stage it ------------------------------------------------------------
log "staging the node bundle from ${node_bundle}"
node "${desktop}/scripts/stage-node-bundle.mjs" --from "${node_bundle}"

# --- 3. package the app -----------------------------------------------------
if [[ ! -d "${desktop}/node_modules" ]]; then
  log "installing desktop dependencies"
  npm --prefix "${desktop}" ci
fi

log "running electron-builder (mac arm64)"
(
  cd "${desktop}"
  # No identity here: the app is ad-hoc signed by pkgs/macos/adhoc-sign.js so it
  # runs on Apple Silicon. A real build exports APPLE_* and a signing identity.
  if [[ -z "${APPLE_TEAM_ID:-}" ]]; then export CSC_IDENTITY_AUTO_DISCOVERY=false; fi
  npx --no-install electron-builder --publish never --mac dmg zip --arm64
)

log "artifacts:"
ls -la "${desktop}/dist" | awk '/\.(dmg|zip)$/ { print "  " $5, $9 }'
