#!/usr/bin/env bash
# Canonical local CI runner.
# This script is intentionally aligned with .github/workflows/ci.yml.
#
# Usage:
#   ./scripts/ci-local.sh quick   # default: preflight + go + sdn-js + plugin-sdk
#   ./scripts/ci-local.sh full    # quick + encryption tests
#   ./scripts/ci-local.sh go      # go checks only
#   ./scripts/ci-local.sh js      # sdn-js checks only
#   ./scripts/ci-local.sh plugin  # plugin-sdk conformance only

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${1:-quick}"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

step() { echo -e "\n${CYAN}=== $1 ===${NC}"; }
pass() { echo -e "${GREEN}PASS${NC}: $1"; }

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

run_preflight() {
  step "OSS preflight"
  (cd "$ROOT" && ./scripts/oss-preflight.sh)
  pass "oss-preflight"
}

run_go() {
  step "Go deps"
  (cd "$ROOT/sdn-server" && GOCACHE="$ROOT/.gocache" go mod download)
  pass "go mod download"

  step "Go tests (race)"
  (cd "$ROOT/sdn-server" && GOCACHE="$ROOT/.gocache" go test -race -count=1 ./...)
  pass "go test -race"

  step "Go build (full node)"
  (cd "$ROOT/sdn-server" && GOCACHE="$ROOT/.gocache" go build -o /tmp/spacedatanetwork ./cmd/spacedatanetwork)
  pass "go build spacedatanetwork"

  step "Go build (edge relay)"
  (cd "$ROOT/sdn-server" && GOCACHE="$ROOT/.gocache" go build -tags edge -o /tmp/spacedatanetwork-edge ./cmd/spacedatanetwork-edge)
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

  step "sdn-js tests"
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm test -- --run)
  pass "sdn-js test"

  step "sdn-js build"
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm run build)
  pass "sdn-js build"
}

run_plugin_sdk() {
  local volatile_manifest="packages/plugin-sdk/fixtures/third-party/v1/fixture-manifest.json"

  step "plugin-sdk install"
  ensure_npm_deps "$ROOT/packages/plugin-sdk"
  pass "plugin-sdk npm install"

  step "plugin-sdk generate bindings"
  (cd "$ROOT/packages/plugin-sdk" && npm_config_cache="$ROOT/.npm-cache" npm run generate:all-bindings)
  pass "plugin-sdk generate bindings"

  step "plugin-sdk conformance"
  (cd "$ROOT/packages/plugin-sdk" && npm_config_cache="$ROOT/.npm-cache" npm run test:conformance)
  pass "plugin-sdk conformance"

  # This manifest includes a generated timestamp; restore it to avoid local churn
  # while still validating all deterministic generated outputs.
  if git -C "$ROOT" cat-file -e "HEAD:$volatile_manifest" >/dev/null 2>&1; then
    git -C "$ROOT" show "HEAD:$volatile_manifest" > "$ROOT/$volatile_manifest"
  fi

  step "plugin-sdk generated artifacts are committed"
  (
    cd "$ROOT"
    git diff --exit-code -- \
      packages/plugin-sdk/src/generated \
      packages/plugin-sdk/src/generated-go \
      packages/plugin-sdk/fixtures \
      ':(exclude)packages/plugin-sdk/fixtures/third-party/v1/fixture-manifest.json'
  )
  pass "plugin-sdk generated artifacts check"
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
    run_sdn_js
    run_plugin_sdk
    ;;
  full|all)
    run_preflight
    run_go
    run_sdn_js
    run_plugin_sdk
    run_encryption
    ;;
  go)
    run_go
    ;;
  js)
    run_sdn_js
    ;;
  plugin)
    run_plugin_sdk
    ;;
  *)
    echo -e "${RED}Usage: $0 [quick|full|go|js|plugin]${NC}"
    exit 1
    ;;
esac

echo -e "\n${GREEN}CI PASSED (${MODE})${NC}"
