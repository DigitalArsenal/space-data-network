#!/usr/bin/env bash
# Canonical local CI runner.
# This script is intentionally aligned with .github/workflows/ci.yml.
#
# Usage:
#   ./scripts/ci-local.sh quick     # default: preflight + go + sdn-js + module-delivery + plugin-demo
#   ./scripts/ci-local.sh full      # quick + encryption tests
#   ./scripts/ci-local.sh go        # fast go checks only
#   ./scripts/ci-local.sh race      # CI-only/full race suite
#   ./scripts/ci-local.sh js        # sdn-js checks only
#   ./scripts/ci-local.sh delivery  # focused module-delivery compatibility checks
#   ./scripts/ci-local.sh plugin    # legacy alias for delivery
#   ./scripts/ci-local.sh demo      # plugin-demo integration tests only

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${1:-quick}"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

step() { echo -e "\n${CYAN}=== $1 ===${NC}"; }
pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() {
  echo -e "${RED}FAIL${NC}: $1" >&2
  exit 1
}

existing_file() {
  local candidate
  for candidate in "$@"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

nearest_stack_root() {
  local dir="$ROOT"
  while true; do
    if [[ -f "$dir/docs/repository-catalog.md" && -d "$dir/repos/main-packages" ]]; then
      printf '%s\n' "$dir"
      return 0
    fi
    local parent
    parent="$(dirname "$dir")"
    if [[ "$parent" == "$dir" ]]; then
      return 1
    fi
    dir="$parent"
  done
}

ensure_npm_deps() {
  local dir="$1"
  shift || true
  local required_bins=("$@")
  local lockfile="$dir/package-lock.json"
  local npm_cache="$ROOT/.npm-cache"

  if [[ "${CI:-}" == "true" || "${CI:-}" == "1" ]]; then
    if [[ -f "$lockfile" ]]; then
      (cd "$dir" && npm_config_cache="$npm_cache" npm ci)
    else
      (cd "$dir" && npm_config_cache="$npm_cache" npm install --no-audit --no-fund)
    fi
    return
  fi

  if [[ ! -d "$dir/node_modules" ]]; then
    echo "Missing dependencies in $dir/node_modules"
    echo "Install first, then rerun:"
    if [[ -f "$lockfile" ]]; then
      echo "  (cd \"$dir\" && npm ci)"
    else
      echo "  (cd \"$dir\" && npm install)"
    fi
    return 1
  fi

  if [[ ${#required_bins[@]} -gt 0 ]]; then
    for bin in "${required_bins[@]}"; do
      if [[ ! -x "$dir/node_modules/.bin/$bin" ]]; then
        echo "Missing required tool '$bin' in $dir/node_modules/.bin"
        echo "Reinstall dependencies:"
        if [[ -f "$lockfile" ]]; then
          echo "  (cd \"$dir\" && npm ci)"
        else
          echo "  (cd \"$dir\" && npm install)"
        fi
        return 1
      fi
    done
  fi

  echo "Using existing dependencies in $dir/node_modules"
}

prepare_go_wasm_artifacts() {
  step "Go WASM artifacts"

  if [[ -z "${HD_WALLET_WASM_PATH:-}" || ! -f "${HD_WALLET_WASM_PATH:-}" ]]; then
    local hd_wallet_path
    if hd_wallet_path="$(existing_file \
      "$ROOT/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" \
      "$ROOT/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" \
      "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm" \
      "$ROOT/../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm" \
      "$ROOT/../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm")"; then
      export HD_WALLET_WASM_PATH="$hd_wallet_path"
    else
      echo "Pure HD wallet WASI artifact not found; artifact-dependent Go tests will skip."
    fi
  fi

  if [[ -f "${HD_WALLET_WASM_PATH:-}" ]]; then
    echo "HD_WALLET_WASM_PATH=$HD_WALLET_WASM_PATH"
  fi

  if [[ -z "${ORBPRO_LICENSING_WASM_PATH:-}" || ! -f "${ORBPRO_LICENSING_WASM_PATH:-}" ]]; then
    local licensing_path
    local licensing_candidates=()
    local stack_root
    if stack_root="$(nearest_stack_root)"; then
      licensing_candidates+=(
        "$stack_root/repos/main-packages/space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
        "$stack_root/repos/ancillary-packages/space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
      )
    fi
    licensing_candidates+=(
      "$ROOT/../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
      "$ROOT/../../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
      "$ROOT/../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm"
      "$ROOT/../../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm"
    )
    if licensing_path="$(existing_file "${licensing_candidates[@]}")"; then
      export ORBPRO_LICENSING_WASM_PATH="$licensing_path"
      echo "ORBPRO_LICENSING_WASM_PATH=$ORBPRO_LICENSING_WASM_PATH"
    else
      echo "Licensing WASM artifact not found; artifact-dependent Go tests will skip."
    fi
  else
    echo "ORBPRO_LICENSING_WASM_PATH=$ORBPRO_LICENSING_WASM_PATH"
  fi

  pass "go wasm artifacts"
}

run_preflight() {
  step "OSS preflight"
  (cd "$ROOT" && ./scripts/oss-preflight.sh)
  pass "oss-preflight"
}

prepare_go_toolchain() {
  prepare_go_wasm_artifacts

  step "WasmEdge headers/libs"
  if [[ -z "${WASMEDGE_DIR:-}" ]]; then
    fail "WASMEDGE_DIR must point to existing WasmEdge headers/libs; automatic local runtime installation is disabled"
  fi
  if [[ ! -f "$WASMEDGE_DIR/include/wasmedge/wasmedge.h" || ! -d "$WASMEDGE_DIR/lib" ]]; then
    fail "invalid WASMEDGE_DIR: $WASMEDGE_DIR"
  fi
  pass "wasmedge headers/libs"

  step "Go deps"
  "$ROOT/scripts/go-with-wasmedge.sh" mod download
  pass "go mod download"
}

run_go() {
  prepare_go_toolchain

  step "Go tests"
  "$ROOT/scripts/go-with-wasmedge.sh" test -p=1 -count=1 ./...
  pass "go test"
}

run_go_race() {
  prepare_go_toolchain

  step "Go tests (race)"
  "$ROOT/scripts/go-with-wasmedge.sh" test -race -p=1 -count=1 ./...
  pass "go test -race"
}

run_go_builds() {
  prepare_go_toolchain

  step "Go build (full node)"
  "$ROOT/scripts/go-with-wasmedge.sh" build -o /tmp/spacedatanetwork ./cmd/spacedatanetwork
  pass "go build spacedatanetwork"

  step "Go build (edge relay)"
  "$ROOT/scripts/go-with-wasmedge.sh" build -tags edge -o /tmp/spacedatanetwork-edge ./cmd/spacedatanetwork-edge
  pass "go build spacedatanetwork-edge"
}

run_sdn_js() {
  step "sdn-js install"
  ensure_npm_deps "$ROOT/sdn-js" eslint vitest tsup
  pass "sdn-js npm ci"

  local eslint_config=""
  for cfg in \
    "$ROOT/sdn-js/eslint.config.js" \
    "$ROOT/sdn-js/eslint.config.cjs" \
    "$ROOT/sdn-js/eslint.config.mjs" \
    "$ROOT/sdn-js/.eslintrc" \
    "$ROOT/sdn-js/.eslintrc.js" \
    "$ROOT/sdn-js/.eslintrc.cjs" \
    "$ROOT/sdn-js/.eslintrc.json" \
    "$ROOT/sdn-js/.eslintrc.yml" \
    "$ROOT/sdn-js/.eslintrc.yaml"; do
    if [[ -f "$cfg" ]]; then
      eslint_config="$cfg"
      break
    fi
  done

  if [[ -n "$eslint_config" ]]; then
    step "sdn-js lint"
    (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm run lint)
    pass "sdn-js lint"
  else
    echo "Skipping sdn-js lint (no ESLint config found in sdn-js)"
  fi

  step "Upstream IPFS mirror check"
  (cd "$ROOT" && ./scripts/update-upstream-ipfs.sh --check)
  pass "upstream ipfs mirror check"

  step "sdn-js tests"
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm test -- --run)
  pass "sdn-js test"

  step "sdn-js build"
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm run build)
  pass "sdn-js build"
}

run_module_delivery_compat() {
  step "module-delivery compatibility deps"
  ensure_npm_deps "$ROOT"
  ensure_npm_deps "$ROOT/sdn-js" eslint vitest tsup
  pass "module-delivery compatibility deps"

  step "module-delivery compatibility"
  (cd "$ROOT" && npm_config_cache="$ROOT/.npm-cache" npm run test:module-delivery)
  pass "module-delivery compatibility"
}

run_plugin_demo() {
  step "plugin-demo install"
  ensure_npm_deps "$ROOT/plugin-demo/tests"
  pass "plugin-demo npm install"

  # Pre-build the test binary with the correct CGO flags so test-server.mjs
  # finds an already-built binary and skips the rebuild step.
  step "plugin-demo pre-build server binary"
  (cd "$ROOT/sdn-server" && "$ROOT/scripts/go-with-wasmedge.sh" build -o spacedatanetwork-test ./cmd/spacedatanetwork)
  pass "plugin-demo pre-build server binary"

  step "plugin-demo integration tests"
  node "$ROOT/plugin-demo/tests/integration.test.mjs"
  pass "plugin-demo integration tests"
}

run_encryption() {
  if [[ ! -d "$ROOT/tests/encryption/go" ]]; then
    echo "Encryption tests directory missing, skipping"
    return
  fi

  step "Encryption tests (Go)"
  (cd "$ROOT/tests/encryption/go" && GOCACHE="$ROOT/.gocache" go test -race -count=1 ./...)
  pass "encryption go tests"
}

case "$MODE" in
  quick)
    run_preflight
    run_go
    run_go_builds
    run_sdn_js
    run_module_delivery_compat
    run_plugin_demo
    ;;
  full|all)
    run_preflight
    run_go
    run_go_race
    run_go_builds
    run_sdn_js
    run_module_delivery_compat
    run_plugin_demo
    run_encryption
    ;;
  go)
    run_go
    ;;
  race)
    run_go_race
    ;;
  js)
    run_sdn_js
    ;;
  delivery|module-delivery|plugin)
    run_module_delivery_compat
    ;;
  demo|plugin-demo)
    run_plugin_demo
    ;;
  *)
    echo -e "${RED}Usage: $0 [quick|full|go|race|js|delivery|plugin|demo]${NC}"
    exit 1
    ;;
esac

echo -e "\n${GREEN}CI PASSED (${MODE})${NC}"
