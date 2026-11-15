#!/bin/bash
# Space Data Network Install Script
# Usage: curl -fsSL https://spacedatanetwork.org/install.sh | bash
#
# Environment variables:
#   SDN_VERSION     Release tag or version to install (default: latest)
#   SDN_INSTALL_DIR Command link directory (default: ~/.spacedatanetwork/bin)
#   SDN_BUNDLE_DIR  Bundle parent directory (default: ~/.spacedatanetwork/bundles)
#   SDN_SKIP_INIT   Set to 1 to skip first-run node identity initialization
#   SDN_KEY_PASSWORD_FILE  Your own key password file; when set, the installer
#                   does not generate one (see prepare_key_password_file)

set -e

REPO="DigitalArsenal/space-data-network"
PRIMARY_BINARY_NAME="spacedatanetwork"
ALIAS_BINARY_NAME="sdn"
INSTALL_DIR="${SDN_INSTALL_DIR:-$HOME/.spacedatanetwork/bin}"
BUNDLE_PARENT_DIR="${SDN_BUNDLE_DIR:-$HOME/.spacedatanetwork/bundles}"
PRIMARY_COMMAND_PATH="${INSTALL_DIR}/${PRIMARY_BINARY_NAME}"
ALIAS_COMMAND_PATH="${INSTALL_DIR}/${ALIAS_BINARY_NAME}"
TMP_DIR=""

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

cleanup() {
    if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
}

trap cleanup EXIT

fetch_url_stdout() {
    local url="$1"

    if command -v curl &> /dev/null; then
        curl -fsSL "$url"
    elif command -v wget &> /dev/null; then
        wget -qO- "$url"
    else
        log_error "Neither curl nor wget found. Please install one of them."
        exit 1
    fi
}

download_url() {
    local url="$1"
    local output="$2"

    if command -v curl &> /dev/null; then
        curl -fsSL "$url" -o "$output"
    elif command -v wget &> /dev/null; then
        wget -q "$url" -O "$output"
    else
        log_error "Neither curl nor wget found. Please install one of them."
        exit 1
    fi
}

calculate_sha256() {
    local file="$1"

    if command -v sha256sum &> /dev/null; then
        sha256sum "$file" | awk '{print $1}'
    elif command -v shasum &> /dev/null; then
        shasum -a 256 "$file" | awk '{print $1}'
    else
        log_error "No SHA-256 checksum tool found. Please install sha256sum or shasum."
        exit 1
    fi
}

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

    if [ "$OS" = "windows" ] && [ "$ARCH" = "arm64" ]; then
        ARCH="amd64"
    fi

    PLATFORM="${OS}-${ARCH}"
    log_info "Detected platform: $PLATFORM"
}

# select_node_release_tag reads a GitHub releases list (JSON) on stdin and
# prints the tag of the newest node release: the first tag of the form
# v<digit>... in GitHub's newest-first order, never the sdn-js-v* library
# tags. The prerelease flag is not consulted: this repository has labelled
# every node beta both ways over time, and the newest build is what the
# installer must resolve. Pin an older build with SDN_VERSION.
select_node_release_tag() {
    grep -oE '"tag_name": *"[^"]*"' | sed -E 's/^"tag_name": *"//; s/"$//' | grep -E '^v[0-9]' | head -n 1
}

get_latest_version() {
    if [ -n "$SDN_VERSION" ]; then
        VERSION="$SDN_VERSION"
        log_info "Using specified version: $VERSION"
    else
        log_info "Fetching latest version..."
        # The repository also publishes library releases (sdn-js-v*), which
        # GitHub's latest-release endpoint may return; the node's own releases
        # are the v<semver> tags, newest first.
        VERSION=$(fetch_url_stdout "https://api.github.com/repos/${REPO}/releases?per_page=50" | select_node_release_tag)

        if [ -z "$VERSION" ]; then
            log_error "Failed to fetch latest version: no v<semver> node release found among the newest 50 releases of ${REPO}"
            exit 1
        fi
        log_info "Latest version: $VERSION"
    fi
}

normalize_version_names() {
    if [ -z "$VERSION" ]; then
        log_error "Version is empty"
        exit 1
    fi

    case "$VERSION" in
        v*)
            RELEASE_TAG="$VERSION"
            ASSET_VERSION="${VERSION#v}"
            ;;
        *)
            RELEASE_TAG="v${VERSION}"
            ASSET_VERSION="$VERSION"
            ;;
    esac

    case "$ASSET_VERSION" in
        ""|*[!A-Za-z0-9._-]*)
            log_error "Unsupported version string: $VERSION"
            exit 1
            ;;
    esac
}

select_archive() {
    BUNDLE_NAME="spacedatanetwork-${ASSET_VERSION}-${OS}-${ARCH}"

    if [ "$OS" = "windows" ]; then
        ARCHIVE_NAME="spacedatanetwork-${ASSET_VERSION}-${OS}-${ARCH}.zip"
    else
        ARCHIVE_NAME="spacedatanetwork-${ASSET_VERSION}-${OS}-${ARCH}.tar.gz"
    fi

    BUNDLE_ROOT="${BUNDLE_PARENT_DIR}/${BUNDLE_NAME}"
    log_info "Selected archive: $ARCHIVE_NAME"
}

download_archive() {
    local url="https://github.com/${REPO}/releases/download/${RELEASE_TAG}/${ARCHIVE_NAME}"

    TMP_DIR=$(mktemp -d)
    TMP_FILE="${TMP_DIR}/${ARCHIVE_NAME}"

    log_info "Downloading from: $url"
    download_url "$url" "$TMP_FILE"

    if [ ! -f "$TMP_FILE" ] || [ ! -s "$TMP_FILE" ]; then
        log_error "Download failed"
        exit 1
    fi

    log_info "Downloaded successfully"
}

verify_checksum() {
    local checksum_url="https://github.com/${REPO}/releases/download/${RELEASE_TAG}/spacedatanetwork-checksums.txt"
    local checksum_file="${TMP_DIR}/spacedatanetwork-checksums.txt"

    log_info "Verifying checksum..."
    download_url "$checksum_url" "$checksum_file"

    EXPECTED=$(awk -v name="$ARCHIVE_NAME" '$2 == name { print $1; exit }' "$checksum_file")
    if [ -z "$EXPECTED" ]; then
        log_error "Checksum for $ARCHIVE_NAME not found in spacedatanetwork-checksums.txt"
        exit 1
    fi

    ACTUAL=$(calculate_sha256 "$TMP_FILE")
    if [ "$EXPECTED" = "$ACTUAL" ]; then
        log_info "Checksum verified"
    else
        log_error "Checksum mismatch!"
        log_error "Expected: $EXPECTED"
        log_error "Actual:   $ACTUAL"
        exit 1
    fi
}

extract_archive() {
    log_info "Extracting bundle to $BUNDLE_PARENT_DIR..."
    mkdir -p "$BUNDLE_PARENT_DIR"
    rm -rf "$BUNDLE_ROOT"

    case "$ARCHIVE_NAME" in
        *.zip)
            if ! command -v unzip &> /dev/null; then
                log_error "unzip is required to extract Windows archives"
                exit 1
            fi
            unzip -q "$TMP_FILE" -d "$BUNDLE_PARENT_DIR"
            ;;
        *.tar.gz)
            tar -xzf "$TMP_FILE" -C "$BUNDLE_PARENT_DIR"
            ;;
        *)
            log_error "Unsupported archive type: $ARCHIVE_NAME"
            exit 1
            ;;
    esac

    if [ "$OS" = "windows" ]; then
        if [ ! -f "${BUNDLE_ROOT}/bin/spacedatanetwork.exe" ] || [ ! -f "${BUNDLE_ROOT}/bin/sdn.exe" ]; then
            log_error "Extracted Windows bundle is missing expected CLI binaries"
            exit 1
        fi
    elif [ ! -e "${BUNDLE_ROOT}/bin/${PRIMARY_BINARY_NAME}" ] || [ ! -e "${BUNDLE_ROOT}/bin/${ALIAS_BINARY_NAME}" ]; then
        log_error "Extracted bundle is missing expected CLI binaries"
        exit 1
    fi

    if [ ! -f "${BUNDLE_ROOT}/runtime/modules/org.spacedatanetwork.updater.wasm" ]; then
        log_error "Extracted bundle is missing the SDN updater module"
        exit 1
    fi

    if [ ! -f "${BUNDLE_ROOT}/runtime/modules/hd-wallet-wasi.wasm" ]; then
        log_error "Extracted bundle is missing the SDN HD wallet module"
        exit 1
    fi

    if [ ! -f "${BUNDLE_ROOT}/manifest.json" ]; then
        log_error "Extracted bundle is missing manifest.json"
        exit 1
    fi

    log_info "Extracted to $BUNDLE_ROOT"
}

install_unix_links() {
    log_info "Linking commands into $INSTALL_DIR..."

    if ! mkdir -p "$INSTALL_DIR"; then
        log_error "Failed to create $INSTALL_DIR"
        exit 1
    fi

    if [ ! -w "$INSTALL_DIR" ]; then
        log_error "$INSTALL_DIR is not writable. Set SDN_INSTALL_DIR to a user-writable directory."
        exit 1
    fi

    ln -sf "${BUNDLE_ROOT}/bin/${PRIMARY_BINARY_NAME}" "${INSTALL_DIR}/${PRIMARY_BINARY_NAME}"
    ln -sf "${BUNDLE_ROOT}/bin/${ALIAS_BINARY_NAME}" "${INSTALL_DIR}/${ALIAS_BINARY_NAME}"

    log_info "Installed ${PRIMARY_BINARY_NAME} and ${ALIAS_BINARY_NAME} command links"
}

print_windows_usage() {
    log_info "Extracted portable Windows bundle to ${BUNDLE_ROOT}"
    log_info "For native PowerShell, run: irm https://spacedatanetwork.org/install.ps1 | iex"
    log_info "For this Bash environment, add ${BUNDLE_ROOT}/bin to your PATH"
    log_info "Run ${BUNDLE_ROOT}/bin/spacedatanetwork.exe version"
    log_info "Alias binary: ${BUNDLE_ROOT}/bin/sdn.exe"
}

install_bundle() {
    if [ "$OS" = "windows" ]; then
        print_windows_usage
    else
        install_unix_links
    fi
}

# THE KEY PASSWORD FILE OF A FRESH NODE.
#
# A node with no key password seals its identity under a key derived from the
# machine and the account it runs as. On most Linux hosts that derivation is
# REFUSED for every account but root, by design: the kernel creates
# /sys/class/dmi/id/product_uuid readable by root only, and a key that depends
# on the privilege of the reading process is the defect the derivation refuses
# to repeat. A first start there used to fail until the operator made a
# password file by hand. This does it for them — for a FRESH node only:
#
#   - nothing happens when SDN_KEY_PASSWORD or SDN_KEY_PASSWORD_FILE is set,
#     or when the installed binary predates `key fingerprint-check` (it then
#     never answers "refused");
#   - the node itself decides the rest (`key fresh-password-file`): no file
#     when a key password is already configured, when the node already has an
#     identity (it stays sealed as it is), or when the fingerprint is not
#     refused; an existing file is never overwritten;
#   - the file is then recorded in the node's config by
#     `init --key-password-file`, so every later start (`daemon`, `start`, any
#     CLI command) finds it with nothing exported.
#
# The file is ~/sdn-key-password, OUTSIDE ~/.spacedatanetwork (the path the
# manual recipe used): a copy of the node's data directory alone (a backup, a
# snapshot, a synced folder) then does not open the identity or the credential
# store. Anyone holding both can.
KEY_PASSWORD_FILE="${HOME}/sdn-key-password"
INIT_ARGS=()

# NODE_CONFIG_PATH is the config every node command below is given with -c:
# SDN_CONFIG when the operator set it, else the home default `init` writes.
# Never left to the binary's own resolution: with no -c it asks the process
# table for a running daemon, and the process table is whatever every account
# on the machine puts there — a stranger's process named spacedatanetwork,
# claiming `daemon --config /tmp/theirs.yaml`, would choose the key directory
# the fresh-node check looks in and the config `init` saves.
NODE_CONFIG_PATH="${SDN_CONFIG:-${HOME}/.spacedatanetwork/config.yaml}"

prepare_key_password_file() {
    INIT_ARGS=()
    if [ -n "${SDN_KEY_PASSWORD:-}" ] || [ -n "${SDN_KEY_PASSWORD_FILE:-}" ]; then
        return
    fi
    local verdict outcome
    verdict="$("$PRIMARY_COMMAND_PATH" key fingerprint-check 2>/dev/null || true)"
    if [ "$verdict" != "refused" ]; then
        return
    fi
    if ! outcome="$("$PRIMARY_COMMAND_PATH" -c "$NODE_CONFIG_PATH" key fresh-password-file "$KEY_PASSWORD_FILE")"; then
        log_error "Could not prepare a key password file at $KEY_PASSWORD_FILE (see the error above)"
        exit 1
    fi
    case "$outcome" in
        "created "*)
            log_info "This host's machine fingerprint is refused for account $(id -un) ('$PRIMARY_BINARY_NAME key fingerprint-check' says why),"
            log_info "so the new node identity is sealed under a generated key password file: $KEY_PASSWORD_FILE"
            log_warn "KEEP $KEY_PASSWORD_FILE for the life of the node, and back it up apart from ~/.spacedatanetwork: the node identity cannot be opened without it, and anyone holding both can open it and the node's stored credentials."
            INIT_ARGS=(--key-password-file "$KEY_PASSWORD_FILE")
            ;;
        "exists "*)
            log_info "Sealing the new node identity under the existing key password file $KEY_PASSWORD_FILE"
            log_warn "KEEP $KEY_PASSWORD_FILE for the life of the node, and back it up apart from ~/.spacedatanetwork: the node identity cannot be opened without it, and anyone holding both can open it and the node's stored credentials."
            INIT_ARGS=(--key-password-file "$KEY_PASSWORD_FILE")
            ;;
        *)
            log_info "No key password file generated (${outcome})"
            ;;
    esac
}

initialize_unix_node() {
    if [ "$OS" = "windows" ]; then
        return
    fi
    if [ "${SDN_SKIP_INIT:-}" = "1" ]; then
        log_info "Skipping node identity initialization because SDN_SKIP_INIT=1"
        return
    fi
    if [ ! -x "$PRIMARY_COMMAND_PATH" ]; then
        log_warn "Cannot initialize node identity because $PRIMARY_COMMAND_PATH is not executable"
        return
    fi

    log_info "Initializing local node identity..."
    prepare_key_password_file
    "$PRIMARY_COMMAND_PATH" -c "$NODE_CONFIG_PATH" init "${INIT_ARGS[@]}"
    log_info "Verifying local node identity..."
    "$PRIMARY_COMMAND_PATH" -c "$NODE_CONFIG_PATH" show-identity >/dev/null 2>&1
}

verify_installation() {
    if [ "$OS" = "windows" ]; then
        return
    fi

    if [ -x "$PRIMARY_COMMAND_PATH" ] && [ -x "$ALIAS_COMMAND_PATH" ]; then
        # RUN IT BEFORE CLAIMING SUCCESS. This used to print "Installation
        # successful!", then downgrade a binary that cannot execute at all to a
        # warning, and finish by telling the operator to run it. The common way
        # to hit that is a host whose glibc is older than the build's: the node
        # links WasmEdge, libstdc++ and libgcc in, but glibc stays dynamic, so
        # it is the one version that can still be wrong.
        if ! version_output="$("$PRIMARY_COMMAND_PATH" version 2>&1)"; then
            log_error "The installed binary cannot run on this host."
            printf '%s\n' "$version_output" | head -3
            if printf '%s' "$version_output" | grep -q 'GLIBC_'; then
                needed="$(printf '%s' "$version_output" | grep -o 'GLIBC_2\.[0-9]*' | sort -uV | tail -1)"
                present="$(ldd --version 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+$' || true)"
                log_error "This build needs glibc ${needed#GLIBC_} or newer; this host has ${present:-an older release}."
                log_info "Use the container image, which carries its own glibc:"
                log_info "  docker run -d --name sdn --hostname sdn -v sdn-data:/app/data \\"
                log_info "    dockerdigitalarsenal/space-data-network:beta"
            fi
            log_info "Nothing was left running. Remove $INSTALL_DIR to undo this install."
            exit 1
        fi
        log_info "Installation successful!"
        echo ""
        printf '%s\n' "$version_output"
        "$ALIAS_COMMAND_PATH" status >/dev/null 2>&1 || log_warn "Alias command did not print local status"
        echo ""
        if ! command -v "$PRIMARY_BINARY_NAME" &> /dev/null || ! command -v "$ALIAS_BINARY_NAME" &> /dev/null; then
            log_warn "Command links installed but $INSTALL_DIR is not in PATH"
            log_info "Add this to your shell profile: export PATH=\"$INSTALL_DIR:\$PATH\""
            log_info "For this shell, run: export PATH=\"$INSTALL_DIR:\$PATH\""
        fi
        log_info "Run '$PRIMARY_BINARY_NAME start' to start the node as a persistent background service"
        log_info "Run '$PRIMARY_BINARY_NAME daemon' for foreground/manual mode"
        log_info "Run '$ALIAS_BINARY_NAME status' to inspect the local node"
    else
        log_error "Command links were not installed correctly"
        exit 1
    fi
}

main() {
    echo ""
    echo -e "${BLUE}===========================================${NC}"
    echo -e "${BLUE}     Space Data Network Installer          ${NC}"
    echo -e "${BLUE}===========================================${NC}"
    echo ""

    detect_platform
    get_latest_version
    normalize_version_names
    select_archive
    download_archive
    verify_checksum
    extract_archive
    install_bundle
    initialize_unix_node
    verify_installation

    echo ""
    log_info "Documentation: https://spacedatanetwork.org"
    log_info "GitHub: https://github.com/${REPO}"
    echo ""
}

main "$@"
