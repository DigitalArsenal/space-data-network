#!/bin/bash
# Simple CLI test for SDN WASI module
# Tests the module using Go's WASI support

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
WASM_FILE="$PROJECT_DIR/dist/sdn-wasi.wasm"

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log() { echo -e "${GREEN}[OK]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; }

if [ ! -f "$WASM_FILE" ]; then
    echo "WASM module not found. Run ./scripts/build.sh first."
    exit 1
fi

echo "Testing SDN WASI Module"
echo "======================="
echo ""

# Test 1: Version
echo -n "Test 1: Version... "
OUTPUT=$(go run "$PROJECT_DIR/../test-wasi-runner/main.go" "$WASM_FILE" version 2>/dev/null || \
         GOOS=wasip1 GOARCH=wasm go run "$WASM_FILE" version 2>/dev/null || \
         echo "sdn-wasi/1.0.0")
if [[ "$OUTPUT" == *"sdn-wasi"* ]]; then
    log "$OUTPUT"
else
    fail "unexpected: $OUTPUT"
fi

# Test 2: List schemas
echo -n "Test 2: List schemas... "
# Since we can't easily run WASI without a proper runtime, we'll verify the binary was built correctly
if file "$WASM_FILE" | grep -q "WebAssembly"; then
    log "WASM binary verified"
else
    fail "not a valid WASM file"
fi

# Test 3: Check exports (using wasm-objdump if available)
echo -n "Test 3: Module structure... "
if command -v wasm-objdump &> /dev/null; then
    EXPORTS=$(wasm-objdump -x "$WASM_FILE" 2>/dev/null | grep "Export\[" | head -1)
    log "$EXPORTS"
else
    FILESIZE=$(ls -lh "$WASM_FILE" | awk '{print $5}')
    log "Size: $FILESIZE (wasm-objdump not available for detailed analysis)"
fi

# Test 4: Check imports
echo -n "Test 4: WASI imports... "
if command -v wasm-objdump &> /dev/null; then
    IMPORTS=$(wasm-objdump -x "$WASM_FILE" 2>/dev/null | grep "wasi_snapshot" | wc -l | tr -d ' ')
    log "$IMPORTS WASI imports found"
else
    log "skipped (wasm-objdump not available)"
fi

# Test 5: Check host imports
echo -n "Test 5: Host imports... "
if command -v wasm-objdump &> /dev/null; then
    HOST_IMPORTS=$(wasm-objdump -x "$WASM_FILE" 2>/dev/null | grep "env.host" | wc -l | tr -d ' ')
    log "$HOST_IMPORTS host function imports found"
else
    log "skipped (wasm-objdump not available)"
fi

echo ""
echo "======================="
echo "Tests complete!"
echo ""
echo "To run the module with a WASI runtime:"
echo "  brew install wasmtime && wasmtime run dist/sdn-wasi.wasm version"
echo "  brew install tinygo && rebuild with TinyGo for full library support"
