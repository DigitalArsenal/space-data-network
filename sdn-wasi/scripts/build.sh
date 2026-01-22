#!/bin/bash
# Build script for SDN WASI module
# Compiles Go code to WASM with WASI support
# Supports both standard Go (command mode) and TinyGo (library mode with exports)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="$PROJECT_DIR/dist"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[BUILD]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Try TinyGo first (proper WASM exports support)
if command -v tinygo &> /dev/null; then
    TINYGO_VERSION=$(tinygo version | head -1)
    log "Using TinyGo: $TINYGO_VERSION"

    log "Building SDN WASI module with TinyGo (with exports)..."
    cd "$PROJECT_DIR"

    tinygo build \
        -o "$OUTPUT_DIR/sdn-wasi.wasm" \
        -target=wasi \
        -scheduler=none \
        -gc=leaking \
        -no-debug \
        ./cmd/sdn-wasi-tinygo

    if [ ! -f "$OUTPUT_DIR/sdn-wasi.wasm" ]; then
        error "TinyGo build failed"
    fi

    WASM_SIZE=$(ls -lh "$OUTPUT_DIR/sdn-wasi.wasm" | awk '{print $5}')
    log "Built (TinyGo): $OUTPUT_DIR/sdn-wasi.wasm ($WASM_SIZE)"
else
    # Fall back to standard Go (command mode only)
    GO_VERSION=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1)
    log "Using Go: $GO_VERSION"
    warn "TinyGo not found - building in command mode (no library exports)"
    warn "Install TinyGo for full library support: https://tinygo.org/getting-started/install/"

    log "Building SDN WASI module with Go (command mode)..."
    cd "$PROJECT_DIR"

    GOOS=wasip1 GOARCH=wasm go build \
        -o "$OUTPUT_DIR/sdn-wasi.wasm" \
        -ldflags="-s -w" \
        ./cmd/sdn-wasi

    if [ ! -f "$OUTPUT_DIR/sdn-wasi.wasm" ]; then
        error "Go build failed"
    fi

    WASM_SIZE=$(ls -lh "$OUTPUT_DIR/sdn-wasi.wasm" | awk '{print $5}')
    log "Built (Go command mode): $OUTPUT_DIR/sdn-wasi.wasm ($WASM_SIZE)"
fi

# Optimize with wasm-opt if available
if command -v wasm-opt &> /dev/null; then
    log "Optimizing with wasm-opt..."
    wasm-opt -O3 "$OUTPUT_DIR/sdn-wasi.wasm" -o "$OUTPUT_DIR/sdn-wasi.opt.wasm"
    OPT_SIZE=$(ls -lh "$OUTPUT_DIR/sdn-wasi.opt.wasm" | awk '{print $5}')
    log "Optimized: $OUTPUT_DIR/sdn-wasi.opt.wasm ($OPT_SIZE)"
else
    warn "wasm-opt not found - skipping optimization"
    warn "Install with: brew install binaryen (macOS) or apt install binaryen (Linux)"
fi

# Build host runtime (native Go)
log "Building host runtime..."
GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) go build \
    -o "$OUTPUT_DIR/sdn-host" \
    ./cmd/sdn-host 2>/dev/null || warn "Host runtime not yet implemented"

# Copy runtime configs
log "Copying runtime configurations..."
mkdir -p "$OUTPUT_DIR/runtime"
cp "$PROJECT_DIR/runtime/"* "$OUTPUT_DIR/runtime/" 2>/dev/null || true

# Create data and config directories
mkdir -p "$OUTPUT_DIR/data" "$OUTPUT_DIR/config"

# Generate module info
cat > "$OUTPUT_DIR/module-info.json" << EOF
{
    "name": "sdn-wasi",
    "version": "1.0.0",
    "build_time": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "go_version": "$GO_VERSION",
    "exports": [
        "sdn_init",
        "sdn_version",
        "sdn_alloc",
        "sdn_free",
        "sdn_get_buffer_ptr",
        "sdn_get_buffer_len",
        "sdn_register_schema",
        "sdn_get_schema_id",
        "sdn_list_schemas",
        "sdn_validate",
        "sdn_process_message",
        "sdn_get_message_count",
        "sdn_get_message",
        "sdn_clear_messages",
        "sdn_publish",
        "sdn_subscribe",
        "sdn_is_subscribed",
        "sdn_hash_sha256",
        "sdn_verify_signature"
    ],
    "imports": [
        "env.host_log",
        "env.host_send_message",
        "env.host_subscribe",
        "env.host_get_peer_id",
        "env.host_store_data",
        "env.host_load_data"
    ]
}
EOF

log "Module info written to $OUTPUT_DIR/module-info.json"

# Summary
echo ""
log "Build complete!"
echo "  WASM module: $OUTPUT_DIR/sdn-wasi.wasm"
echo ""
echo "Run with:"
echo "  wasmtime: wasmtime run --config runtime/wasmtime.toml sdn-wasi.wasm"
echo "  wasmer:   wasmer run --config runtime/wasmer.toml sdn-wasi.wasm"
echo "  wasmedge: wasmedge --dir .:. sdn-wasi.wasm"
echo ""
