#!/bin/bash
# Space Data Network Install Script
# Usage: curl -sSL https://digitalarsenal.github.io/space-data-network/install.sh | bash
#
# Environment variables:
#   SDN_VERSION  - Specific version to install (default: latest)
#   SDN_INSTALL_DIR - Installation directory (default: /usr/local/bin)

set -e

# Configuration
REPO="DigitalArsenal/go-space-data-network"
BINARY_NAME="spacedatanetwork"
INSTALL_DIR="${SDN_INSTALL_DIR:-/usr/local/bin}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

# Detect OS and architecture
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$OS" in
        linux)
            OS="linux"
            ;;
        darwin)
            OS="darwin"
            ;;
        mingw*|msys*|cygwin*)
            OS="windows"
            ;;
        *)
            log_error "Unsupported operating system: $OS"
            exit 1
            ;;
    esac

    case "$ARCH" in
        x86_64|amd64)
            ARCH="amd64"
            ;;
        aarch64|arm64)
            ARCH="arm64"
            ;;
        armv7l|armhf)
            ARCH="arm"
            ;;
        *)
            log_error "Unsupported architecture: $ARCH"
            exit 1
            ;;
    esac

    PLATFORM="${OS}-${ARCH}"
    log_info "Detected platform: $PLATFORM"
}

# Get latest version from GitHub
get_latest_version() {
    if [ -n "$SDN_VERSION" ]; then
        VERSION="$SDN_VERSION"
        log_info "Using specified version: $VERSION"
    else
        log_info "Fetching latest version..."
        VERSION=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

        if [ -z "$VERSION" ]; then
            log_error "Failed to fetch latest version"
            exit 1
        fi
        log_info "Latest version: $VERSION"
    fi
}

# Download binary
download_binary() {
    local url="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}-${PLATFORM}"

    if [ "$OS" = "windows" ]; then
        url="${url}.exe"
        BINARY_NAME="${BINARY_NAME}.exe"
    fi

    log_info "Downloading from: $url"

    TMP_DIR=$(mktemp -d)
    TMP_FILE="${TMP_DIR}/${BINARY_NAME}"

    if command -v curl &> /dev/null; then
        curl -sL "$url" -o "$TMP_FILE"
    elif command -v wget &> /dev/null; then
        wget -q "$url" -O "$TMP_FILE"
    else
        log_error "Neither curl nor wget found. Please install one of them."
        exit 1
    fi

    if [ ! -f "$TMP_FILE" ] || [ ! -s "$TMP_FILE" ]; then
        log_error "Download failed"
        exit 1
    fi

    log_info "Downloaded successfully"
}

# Verify checksum (if available)
verify_checksum() {
    local checksum_url="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

    log_info "Verifying checksum..."

    if curl -sL "$checksum_url" -o "${TMP_DIR}/checksums.txt" 2>/dev/null; then
        EXPECTED=$(grep "${BINARY_NAME}-${PLATFORM}" "${TMP_DIR}/checksums.txt" | awk '{print $1}')

        if [ -n "$EXPECTED" ]; then
            if command -v sha256sum &> /dev/null; then
                ACTUAL=$(sha256sum "$TMP_FILE" | awk '{print $1}')
            elif command -v shasum &> /dev/null; then
                ACTUAL=$(shasum -a 256 "$TMP_FILE" | awk '{print $1}')
            else
                log_warn "No checksum tool found, skipping verification"
                return
            fi

            if [ "$EXPECTED" = "$ACTUAL" ]; then
                log_info "Checksum verified"
            else
                log_error "Checksum mismatch!"
                log_error "Expected: $EXPECTED"
                log_error "Actual:   $ACTUAL"
                exit 1
            fi
        else
            log_warn "Checksum not found in release, skipping verification"
        fi
    else
        log_warn "Checksums file not available, skipping verification"
    fi
}

# Install binary
install_binary() {
    log_info "Installing to $INSTALL_DIR..."

    chmod +x "$TMP_FILE"

    # Check if we need sudo
    if [ -w "$INSTALL_DIR" ]; then
        mv "$TMP_FILE" "${INSTALL_DIR}/${BINARY_NAME}"
    else
        log_info "Requesting sudo permission..."
        sudo mv "$TMP_FILE" "${INSTALL_DIR}/${BINARY_NAME}"
    fi

    # Cleanup
    rm -rf "$TMP_DIR"

    log_info "Installed to ${INSTALL_DIR}/${BINARY_NAME}"
}

# Verify installation
verify_installation() {
    if command -v "$BINARY_NAME" &> /dev/null; then
        log_info "Installation successful!"
        echo ""
        $BINARY_NAME version
        echo ""
        log_info "Run '$BINARY_NAME init' to initialize configuration"
        log_info "Run '$BINARY_NAME daemon' to start the node"
    else
        log_warn "Binary installed but not in PATH"
        log_info "Add $INSTALL_DIR to your PATH or run: ${INSTALL_DIR}/${BINARY_NAME}"
    fi
}

# Main
main() {
    echo ""
    echo -e "${BLUE}╔═══════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║     Space Data Network Installer          ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════╝${NC}"
    echo ""

    detect_platform
    get_latest_version
    download_binary
    verify_checksum
    install_binary
    verify_installation

    echo ""
    log_info "Documentation: https://docs.digitalarsenal.github.io/space-data-network"
    log_info "GitHub: https://github.com/${REPO}"
    echo ""
}

main "$@"
