#!/bin/bash
# Test script for SDN WASI module
# Tests the module with various WASI runtimes

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
DIST_DIR="$PROJECT_DIR/dist"
WASM_FILE="$DIST_DIR/sdn-wasi.wasm"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${GREEN}[TEST]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[FAIL]${NC} $1"; }
info() { echo -e "${BLUE}[INFO]${NC} $1"; }

# Check if WASM file exists
if [ ! -f "$WASM_FILE" ]; then
    error "WASM module not found: $WASM_FILE"
    echo "Run ./scripts/build.sh first"
    exit 1
fi

log "Testing SDN WASI module: $WASM_FILE"
echo ""

# Create test directories
mkdir -p "$DIST_DIR/data" "$DIST_DIR/config"

# Run Go tests
test_go_unit() {
    log "Running Go unit and integration tests..."
    cd "$PROJECT_DIR"
    go test -v ./... 2>&1 | grep -E "^(=== RUN|--- PASS|--- FAIL|--- SKIP|PASS|FAIL|ok)"
    echo ""
}

# Test with Go host runtime
test_go_host() {
    log "Testing with Go host runtime..."
    cd "$PROJECT_DIR"

    # Build host if needed
    if [ ! -f "$DIST_DIR/sdn-host" ]; then
        info "Building host runtime..."
        go build -o "$DIST_DIR/sdn-host" ./cmd/sdn-host
    fi

    # Run host tests via Go test (which is more reliable)
    go test -v ./internal/host -run TestHostCreation 2>&1 | tail -5
    echo ""
}

# Test with Wasmtime
test_wasmtime() {
    if ! command -v wasmtime &> /dev/null; then
        warn "wasmtime not found - skipping"
        return
    fi

    log "Testing with Wasmtime..."
    cd "$DIST_DIR"

    # Wasmtime needs stub implementations for our host imports
    # For now, just verify the module loads
    timeout 5 wasmtime run \
        --dir data::./data \
        --dir config::./config \
        sdn-wasi.wasm 2>&1 || true

    echo ""
}

# Test with Wasmer
test_wasmer() {
    if ! command -v wasmer &> /dev/null; then
        warn "wasmer not found - skipping"
        return
    fi

    log "Testing with Wasmer..."
    cd "$DIST_DIR"

    timeout 5 wasmer run \
        --dir data:./data \
        --dir config:./config \
        sdn-wasi.wasm 2>&1 || true

    echo ""
}

# Test with WasmEdge
test_wasmedge() {
    if ! command -v wasmedge &> /dev/null; then
        warn "wasmedge not found - skipping"
        return
    fi

    log "Testing with WasmEdge..."
    cd "$DIST_DIR"

    timeout 5 wasmedge \
        --dir data:./data \
        --dir config:./config \
        sdn-wasi.wasm 2>&1 || true

    echo ""
}

# Inspect WASM module
inspect_module() {
    log "Inspecting WASM module..."

    if command -v wasm-objdump &> /dev/null; then
        info "Exports:"
        wasm-objdump -x "$WASM_FILE" 2>/dev/null | grep -A 100 "Export\[" | head -30 || true
        echo ""

        info "Imports:"
        wasm-objdump -x "$WASM_FILE" 2>/dev/null | grep -A 100 "Import\[" | head -20 || true
    elif command -v wasmtime &> /dev/null; then
        info "Module exports (via wasmtime):"
        wasmtime explore "$WASM_FILE" 2>/dev/null | head -50 || true
    else
        warn "No WASM inspection tools found"
        info "Install wasm-objdump: brew install wabt (macOS)"
    fi
    echo ""
}

# Run all tests
main() {
    echo "=========================================="
    echo "  SDN WASI Module Test Suite"
    echo "=========================================="
    echo ""

    test_go_unit
    inspect_module
    test_go_host

    echo "--- External Runtime Tests ---"
    echo "(These may fail if host imports aren't provided)"
    echo ""

    test_wasmtime
    test_wasmer
    test_wasmedge

    echo "=========================================="
    log "Test suite complete"
    echo "=========================================="
}

main "$@"
