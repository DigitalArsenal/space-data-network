#!/usr/bin/env bash
# Plugin Demo Integration Test Runner
#
# Usage:
#   ./run.sh              # Run integration tests
#   ./run.sh --verbose    # Verbose output (shows server logs)
#
# Prerequisites:
#   - Go 1.21+ (for building SDN server)
#   - Node.js 18+ (for running tests)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

step() { echo -e "\n${CYAN}=== $1 ===${NC}"; }

# Parse flags
VERBOSE=""
for arg in "$@"; do
  case "$arg" in
    --verbose|-v) VERBOSE="1" ;;
  esac
done

# Verify prerequisites
if ! command -v go &>/dev/null; then
  echo -e "${RED}Error: Go is not installed. Install Go 1.21+ first.${NC}"
  exit 1
fi

if ! command -v node &>/dev/null; then
  echo -e "${RED}Error: Node.js is not installed. Install Node.js 18+ first.${NC}"
  exit 1
fi

# Ensure Go server builds
step "Building SDN server"
(cd "$ROOT/sdn-server" && go build ./...)
echo -e "${GREEN}Build OK${NC}"

# Install test dependencies
step "Installing test dependencies"
npm_cache="$ROOT/.npm-cache"
if [[ "${CI:-}" == "true" || "${CI:-}" == "1" ]]; then
  (cd "$SCRIPT_DIR" && npm_config_cache="$npm_cache" npm ci 2>/dev/null || npm_config_cache="$npm_cache" npm install --no-audit --no-fund)
else
  if [[ ! -d "$SCRIPT_DIR/node_modules" ]]; then
    (cd "$SCRIPT_DIR" && npm install --no-audit --no-fund)
  else
    echo "Using existing dependencies"
  fi
fi

# Run integration tests
step "Running integration tests"
export SDN_TEST_VERBOSE="${VERBOSE:-0}"
node "$SCRIPT_DIR/integration.test.mjs"

echo -e "\n${GREEN}Plugin demo integration tests passed${NC}"
