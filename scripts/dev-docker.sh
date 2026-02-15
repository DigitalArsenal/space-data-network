#!/usr/bin/env bash
# Build and run the dev Docker container.
# Copies external dependencies into the build context, then runs docker compose.
#
# Usage:
#   ./scripts/dev-docker.sh          # build and run
#   ./scripts/dev-docker.sh --clean  # wipe dev data and rebuild

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
NC='\033[0m'

CLEAN=false
for arg in "$@"; do
  case "$arg" in
    --clean) CLEAN=true ;;
  esac
done

if $CLEAN; then
  echo -e "${YELLOW}Cleaning dev Docker data...${NC}"
  rm -rf "$ROOT/data/dev-docker"
fi

# ── Copy external dependencies into build context ─────────────────────────────
echo -e "${CYAN}Preparing build context...${NC}"

# Wallet UI dist
WALLET_UI_SRC=""
for p in "$ROOT/../hd-wallet-wasm/wallet-ui/dist" "$ROOT/../hd-wallet-wasm/wallet-ui/build"; do
  if [ -d "$p" ] && [ -f "$p/index.html" ]; then
    WALLET_UI_SRC="$p"
    break
  fi
done
if [ -z "$WALLET_UI_SRC" ]; then
  echo -e "${RED}ERROR: wallet-ui dist not found at ../hd-wallet-wasm/wallet-ui/dist${NC}"
  echo "  Build it first: cd ../hd-wallet-wasm/wallet-ui && npm run build"
  exit 1
fi
rm -rf "$ROOT/wallet-ui-dist"
cp -r "$WALLET_UI_SRC" "$ROOT/wallet-ui-dist"
echo -e "  ${GREEN}Wallet UI:${NC} $WALLET_UI_SRC"

# WASM binary
WASM_SRC=""
for p in "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm" \
         "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm"; do
  if [ -f "$p" ]; then
    WASM_SRC="$p"
    break
  fi
done
if [ -z "$WASM_SRC" ]; then
  echo -e "${RED}ERROR: HD wallet WASM not found${NC}"
  echo "  Build it first in ../hd-wallet-wasm"
  exit 1
fi
cp "$WASM_SRC" "$ROOT/hd-wallet-wasi.wasm"
echo -e "  ${GREEN}WASM:${NC}      $WASM_SRC"

# Ensure data dir exists
mkdir -p "$ROOT/data/dev-docker"

# ── Build and run ─────────────────────────────────────────────────────────────
echo ""
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo -e "  ${GREEN}Space Data Network — Docker Dev Server${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo ""
echo -e "  Admin:     ${GREEN}http://localhost:5001/admin${NC}"
echo -e "  Login:     ${GREEN}http://localhost:5001/login${NC}"
echo ""
echo -e "  ${YELLOW}Test mnemonic:${NC}"
echo "  abandon abandon abandon abandon abandon abandon"
echo "  abandon abandon abandon abandon abandon about"
echo ""
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo ""

cd "$ROOT"
docker compose -f docker-compose.dev.yaml up --build "$@"
