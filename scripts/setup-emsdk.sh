#!/bin/bash
# setup-emsdk.sh - Download and setup Emscripten SDK
#
# This script downloads Emscripten to packages/emsdk and installs the
# specified version. Similar to how tudatpy handles it via CMake.
#
# Usage:
#   ./scripts/setup-emsdk.sh           # Install and activate emsdk
#   ./scripts/setup-emsdk.sh --update  # Update to latest version

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
PACKAGES_DIR="$PROJECT_ROOT/packages"
EMSDK_DIR="$PACKAGES_DIR/emsdk"
EMSDK_VERSION="${EMSDK_VERSION:-3.1.51}"
VERSION_FILE="$PACKAGES_DIR/.emsdk_version"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Parse arguments
UPDATE_MODE=false
if [[ "$1" == "--update" ]]; then
    UPDATE_MODE=true
fi

# Create packages directory
mkdir -p "$PACKAGES_DIR"

# Clone emsdk if not present
if [[ ! -d "$EMSDK_DIR" ]]; then
    log_info "Cloning Emscripten SDK..."
    git clone --depth 1 https://github.com/emscripten-core/emsdk.git "$EMSDK_DIR"
fi

# Check if we need to install
NEED_INSTALL=true
if [[ -f "$VERSION_FILE" ]]; then
    INSTALLED_VERSION=$(cat "$VERSION_FILE")
    if [[ "$INSTALLED_VERSION" == "$EMSDK_VERSION" ]] && [[ "$UPDATE_MODE" == false ]]; then
        NEED_INSTALL=false
        log_info "Emscripten $EMSDK_VERSION is already installed"
    fi
fi

if [[ "$NEED_INSTALL" == true ]]; then
    log_info "Installing Emscripten $EMSDK_VERSION..."

    cd "$EMSDK_DIR"

    # Update emsdk
    if [[ "$UPDATE_MODE" == true ]]; then
        log_info "Updating emsdk repository..."
        git pull
    fi

    # Install and activate
    ./emsdk install "$EMSDK_VERSION"
    ./emsdk activate "$EMSDK_VERSION"

    # Save version
    echo "$EMSDK_VERSION" > "$VERSION_FILE"

    log_info "Emscripten $EMSDK_VERSION installed and activated"
fi

# Print activation command
log_info ""
log_info "To use Emscripten in your shell, run:"
log_info "  source $EMSDK_DIR/emsdk_env.sh"
log_info ""
log_info "Emscripten tools available at:"
log_info "  emcc:  $EMSDK_DIR/upstream/emscripten/emcc"
log_info "  em++:  $EMSDK_DIR/upstream/emscripten/em++"
