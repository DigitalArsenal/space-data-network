#!/usr/bin/env bash
# Local development server — runs the full SDN node with admin UI, wallet login,
# and WebUI, all served from local build artifacts.
#
# Usage:
#   ./scripts/dev-local.sh           # build if needed, then run
#   ./scripts/dev-local.sh --no-build # skip builds, just run
#   ./scripts/dev-local.sh --clean    # wipe dev data and start fresh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

SKIP_BUILD=false
CLEAN=false
for arg in "$@"; do
  case "$arg" in
    --no-build) SKIP_BUILD=true ;;
    --clean)    CLEAN=true ;;
  esac
done

# ── Clean ──────────────────────────────────────────────────────────────────────
if $CLEAN; then
  echo -e "${YELLOW}Cleaning dev data...${NC}"
  rm -rf "$ROOT/data/dev"
fi

# ── Build Go server ───────────────────────────────────────────────────────────
if ! $SKIP_BUILD; then
  SERVER_BIN="$ROOT/sdn-server/spacedatanetwork"
  SERVER_SRC="$ROOT/sdn-server/cmd/spacedatanetwork/main.go"

  if [ ! -f "$SERVER_BIN" ] || [ "$SERVER_SRC" -nt "$SERVER_BIN" ]; then
    echo -e "${CYAN}Building Go server...${NC}"
    (cd "$ROOT/sdn-server" && go build -o spacedatanetwork ./cmd/spacedatanetwork)
    echo -e "${GREEN}Server built.${NC}"
  else
    echo -e "${GREEN}Server binary is up to date.${NC}"
  fi
fi

# ── Check WebUI build ─────────────────────────────────────────────────────────
WEBUI_BUILD="$ROOT/webui/build"
if [ ! -d "$WEBUI_BUILD" ] || [ ! -f "$WEBUI_BUILD/index.html" ]; then
  if $SKIP_BUILD; then
    echo -e "${YELLOW}WARNING: WebUI build not found at $WEBUI_BUILD${NC}"
    echo "  Run: cd webui && npm run build"
  else
    echo -e "${CYAN}Building WebUI...${NC}"
    (cd "$ROOT/webui" && npm run build)
    echo -e "${GREEN}WebUI built.${NC}"
  fi
fi

# ── Resolve paths ─────────────────────────────────────────────────────────────
# HD wallet WASM binary
WASM_CANDIDATES=(
  "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm"
  "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm"
)
HD_WALLET_WASM=""
for p in "${WASM_CANDIDATES[@]}"; do
  if [ -f "$p" ]; then
    HD_WALLET_WASM="$(cd "$(dirname "$p")" && pwd)/$(basename "$p")"
    break
  fi
done
if [ -z "$HD_WALLET_WASM" ]; then
  echo -e "${YELLOW}WARNING: HD wallet WASM not found. Identity derivation will be disabled.${NC}"
  echo "  Build hd-wallet-wasm first: cd ../hd-wallet-wasm && mkdir -p build-wasi && cd build-wasi && cmake .. -DHD_WALLET_BUILD_WASI=ON && make"
fi

# Wallet UI
WALLET_UI_CANDIDATES=(
  "$ROOT/../hd-wallet-wasm/wallet-ui/dist"
  "$ROOT/../hd-wallet-wasm/wallet-ui/build"
)
WALLET_UI_PATH=""
for p in "${WALLET_UI_CANDIDATES[@]}"; do
  if [ -d "$p" ] && [ -f "$p/index.html" ]; then
    WALLET_UI_PATH="$(cd "$p" && pwd)"
    break
  fi
done
if [ -z "$WALLET_UI_PATH" ]; then
  echo -e "${YELLOW}WARNING: Wallet UI not found. Login will use CDN fallback.${NC}"
fi

# Ensure data directory exists
mkdir -p "$ROOT/data/dev"

# ── Print info ────────────────────────────────────────────────────────────────
echo ""
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo -e "  ${GREEN}Space Data Network — Local Dev Server${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo ""
echo -e "  Admin:     ${GREEN}http://localhost:5001/admin${NC}"
echo -e "  Login:     ${GREEN}http://localhost:5001/login${NC}"
echo -e "  Data API:  ${GREEN}http://localhost:5001/api/v1/data/health${NC}"
echo ""
echo -e "  ${YELLOW}Test mnemonic:${NC}"
echo "  abandon abandon abandon abandon abandon abandon"
echo "  abandon abandon abandon abandon abandon about"
echo ""
if [ -n "$HD_WALLET_WASM" ]; then
  echo -e "  WASM:      $HD_WALLET_WASM"
fi
if [ -n "$WALLET_UI_PATH" ]; then
  echo -e "  Wallet UI: $WALLET_UI_PATH"
fi
echo -e "  WebUI:     $WEBUI_BUILD"
echo -e "  Config:    $ROOT/config/dev.yaml"
echo -e "  Data:      $ROOT/data/dev"
echo ""
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo ""

# ── Run ───────────────────────────────────────────────────────────────────────
export HD_WALLET_WASM_PATH="${HD_WALLET_WASM:-}"
export SDN_WALLET_UI_PATH="${WALLET_UI_PATH:-}"
export SDN_WEBUI_PATH="$WEBUI_BUILD"

exec "$ROOT/sdn-server/spacedatanetwork" daemon --config "$ROOT/config/dev.yaml" --debug
