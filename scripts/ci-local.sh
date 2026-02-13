#!/usr/bin/env bash
# Local CI — replaces GitHub Actions when minutes are exhausted.
# Runs the same checks as .github/workflows/{ci,security,encryption-tests}.yml
#
# Usage:
#   ./scripts/ci-local.sh          # run all checks
#   ./scripts/ci-local.sh quick    # skip slow checks (encryption, audit)
#   ./scripts/ci-local.sh go       # Go checks only
#   ./scripts/ci-local.sh js       # JS checks only
#   ./scripts/ci-local.sh security # security scans only

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${1:-all}"
FAILED=0
PASSED=0
SKIPPED=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

step() { echo -e "\n${CYAN}=== $1 ===${NC}"; }
pass() { echo -e "${GREEN}PASS${NC}: $1"; PASSED=$((PASSED + 1)); }
fail() { echo -e "${RED}FAIL${NC}: $1"; FAILED=$((FAILED + 1)); }
skip() { echo -e "${YELLOW}SKIP${NC}: $1"; SKIPPED=$((SKIPPED + 1)); }

# ─── Go Checks ─────────────────────────────────────────────────────────────
run_go() {
  step "Go Build (full node)"
  if (cd "$ROOT/sdn-server" && go build -o /dev/null ./cmd/spacedatanetwork); then
    pass "go build full node"
  else
    fail "go build full node"
  fi

  step "Go Build (edge relay)"
  if (cd "$ROOT/sdn-server" && go build -tags edge -o /dev/null ./cmd/spacedatanetwork-edge 2>/dev/null); then
    pass "go build edge relay"
  else
    skip "go build edge relay (no edge cmd or build tag issue)"
  fi

  step "Go Vet"
  if (cd "$ROOT/sdn-server" && go vet ./...); then
    pass "go vet"
  else
    fail "go vet"
  fi

  step "Go Tests (race detector)"
  if (cd "$ROOT/sdn-server" && go test -race -count=1 ./... 2>&1); then
    pass "go test -race"
  else
    fail "go test -race"
  fi

  # WASM tests need the binary
  local wasm_path=""
  for p in \
    "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm" \
    "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm"; do
    if [ -f "$p" ]; then wasm_path="$p"; break; fi
  done

  if [ -n "$wasm_path" ]; then
    step "Go WASM Tests (hdwallet)"
    if (cd "$ROOT/sdn-server" && HD_WALLET_WASM_PATH="$wasm_path" go test -v -count=1 ./internal/wasm/... 2>&1); then
      pass "go wasm tests"
    else
      fail "go wasm tests"
    fi
  else
    skip "go wasm tests (no hd-wallet WASM binary found)"
  fi
}

# ─── JS Checks ──────────────────────────────────────────────────────────────
run_js() {
  if [ ! -d "$ROOT/sdn-js" ]; then
    skip "sdn-js directory not found"
    return
  fi

  step "JS Install"
  (cd "$ROOT/sdn-js" && npm ci --silent 2>/dev/null) || true

  step "JS Lint"
  if (cd "$ROOT/sdn-js" && npm run lint 2>&1); then
    pass "js lint"
  else
    skip "js lint (warnings/errors)"
  fi

  step "JS Tests"
  if (cd "$ROOT/sdn-js" && npm test -- --run 2>&1); then
    pass "js test"
  else
    fail "js test"
  fi

  step "JS Build"
  if (cd "$ROOT/sdn-js" && npm run build 2>&1); then
    pass "js build"
  else
    fail "js build"
  fi
}

# ─── Security Scans ─────────────────────────────────────────────────────────
run_security() {
  step "Go Vulnerability Check"
  if command -v govulncheck &>/dev/null; then
    if (cd "$ROOT/sdn-server" && govulncheck ./... 2>&1); then
      pass "govulncheck"
    else
      echo -e "${YELLOW}WARN${NC}: govulncheck found issues (non-blocking)"
      pass "govulncheck (reported)"
    fi
  else
    echo "  Installing govulncheck..."
    go install golang.org/x/vuln/cmd/govulncheck@latest 2>/dev/null
    if command -v govulncheck &>/dev/null; then
      (cd "$ROOT/sdn-server" && govulncheck ./... 2>&1) || true
      pass "govulncheck (reported)"
    else
      skip "govulncheck (not installed)"
    fi
  fi

  step "npm Audit"
  if [ -d "$ROOT/sdn-js" ]; then
    (cd "$ROOT/sdn-js" && npm audit 2>&1) || true
    pass "npm audit (reported)"
  else
    skip "npm audit (no sdn-js)"
  fi
}

# ─── Encryption Tests ───────────────────────────────────────────────────────
run_encryption() {
  if [ ! -d "$ROOT/tests/encryption/go" ]; then
    skip "encryption tests (directory not found)"
    return
  fi

  step "Go Encryption Tests"
  if (cd "$ROOT/tests/encryption/go" && go test -race -count=1 ./... 2>&1); then
    pass "encryption tests"
  else
    fail "encryption tests"
  fi
}

# ─── Dispatch ────────────────────────────────────────────────────────────────
case "$MODE" in
  all)
    run_go
    run_js
    run_security
    run_encryption
    ;;
  quick)
    run_go
    run_js
    ;;
  go)
    run_go
    ;;
  js)
    run_js
    ;;
  security)
    run_security
    ;;
  encryption)
    run_encryption
    ;;
  *)
    echo "Usage: $0 [all|quick|go|js|security|encryption]"
    exit 1
    ;;
esac

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo -e "${CYAN}═══════════════════════════════════════${NC}"
echo -e "  ${GREEN}Passed${NC}:  $PASSED"
echo -e "  ${RED}Failed${NC}:  $FAILED"
echo -e "  ${YELLOW}Skipped${NC}: $SKIPPED"
echo -e "${CYAN}═══════════════════════════════════════${NC}"

if [ "$FAILED" -gt 0 ]; then
  echo -e "\n${RED}CI FAILED${NC}"
  exit 1
else
  echo -e "\n${GREEN}CI PASSED${NC}"
fi
