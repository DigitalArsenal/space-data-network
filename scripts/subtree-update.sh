#!/bin/bash
# subtree-update.sh - Update subtrees from upstream repositories
#
# Usage:
#   ./scripts/subtree-update.sh kubo    # Update Kubo subtree
#   ./scripts/subtree-update.sh desktop # Update IPFS Desktop subtree
#   ./scripts/subtree-update.sh all     # Update both subtrees

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_ROOT"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

update_kubo() {
    log_info "Updating Kubo subtree from upstream..."

    # Ensure remote exists
    if ! git remote get-url kubo-upstream &>/dev/null; then
        log_info "Adding kubo-upstream remote..."
        git remote add kubo-upstream https://github.com/ipfs/kubo.git
    fi

    # Fetch latest
    log_info "Fetching from kubo-upstream..."
    git fetch kubo-upstream

    # Get current branch for reference
    BRANCH=${KUBO_BRANCH:-master}

    # Pull subtree changes
    log_info "Pulling subtree changes from kubo-upstream/$BRANCH..."
    git subtree pull --prefix=kubo kubo-upstream "$BRANCH" --squash -m "Update Kubo subtree from upstream $BRANCH"

    log_info "Kubo subtree updated successfully!"
}

update_desktop() {
    log_info "Updating IPFS Desktop subtree from upstream..."

    # Ensure remote exists
    if ! git remote get-url desktop-upstream &>/dev/null; then
        log_info "Adding desktop-upstream remote..."
        git remote add desktop-upstream https://github.com/ipfs/ipfs-desktop.git
    fi

    # Fetch latest
    log_info "Fetching from desktop-upstream..."
    git fetch desktop-upstream

    # Get current branch for reference
    BRANCH=${DESKTOP_BRANCH:-main}

    # Pull subtree changes
    log_info "Pulling subtree changes from desktop-upstream/$BRANCH..."
    git subtree pull --prefix=desktop desktop-upstream "$BRANCH" --squash -m "Update IPFS Desktop subtree from upstream $BRANCH"

    log_info "IPFS Desktop subtree updated successfully!"
}

update_schemas() {
    log_info "Updating schemas submodule..."

    cd "$PROJECT_ROOT/schemas/sds"
    git fetch origin
    git checkout main
    git pull origin main
    cd "$PROJECT_ROOT"

    # Copy updated schemas to sdn-server
    log_info "Copying schemas to sdn-server..."
    for dir in schemas/sds/schema/*/; do
        schemaName=$(basename "$dir")
        if [ -f "$dir/main.fbs" ]; then
            cp "$dir/main.fbs" "sdn-server/internal/sds/schemas/${schemaName}.fbs"
        fi
    done

    log_info "Schemas updated successfully!"
}

show_usage() {
    echo "Usage: $0 <command>"
    echo ""
    echo "Commands:"
    echo "  kubo      Update Kubo subtree from upstream"
    echo "  desktop   Update IPFS Desktop subtree from upstream"
    echo "  schemas   Update schemas submodule and copy to sdn-server"
    echo "  all       Update all subtrees and submodules"
    echo ""
    echo "Environment variables:"
    echo "  KUBO_BRANCH     Branch to pull from kubo-upstream (default: master)"
    echo "  DESKTOP_BRANCH  Branch to pull from desktop-upstream (default: main)"
}

case "$1" in
    kubo)
        update_kubo
        ;;
    desktop)
        update_desktop
        ;;
    schemas)
        update_schemas
        ;;
    all)
        update_kubo
        update_desktop
        update_schemas
        ;;
    *)
        show_usage
        exit 1
        ;;
esac
