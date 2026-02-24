#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

print_usage() {
  cat <<'USAGE'
Usage:
  run-plugin-harness.sh <plugin-repo-path> [options]

Arguments:
  plugin-repo-path    Path to the plugin workspace repository.

Options:
  --admin-addr <host:port>      SDN admin bind address. Default: 127.0.0.1:5010
  --artifact-dir <path>          Existing encrypted artifact dir (for --skip-build).
  --plugin-id <id>              Plugin id expected in SDN manifest. Default: plugin-key-broker.
  --skip-build                  Do not run the plugin build command.
  --derivation-secret <hex>      Optional derivation secret (64 hex chars).
  --keep-workspace              Keep temporary workspace on exit.
  --repo <path>                 Repository path if not set as first positional arg.
  --help                        Show this help.

Environment:
  PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX
                              X25519 public key used to stage encrypted artifacts.
                              If omitted, an ephemeral keypair is generated for the run.
  PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX
                              Matching X25519 private key for decrypting prebuilt artifacts.
                              Required only when --skip-build is used.
  PLUGIN_SERVER_PRIVATE_KEY_HEX
                              Optional P-256 private key for key-broker runtime.
  PLUGIN_HARNESS_PLUGIN_ID              Plugin id expected in SDN manifest. Default: plugin-key-broker.
  PLUGIN_HARNESS_REQUIRED_SCOPE          Scope written into generated catalog entry. Default: plugin:base.
  PLUGIN_HARNESS_PUBLIC_KEY_PATH          Expected public-key API suffix on plugin endpoint. Default: /v1/public-key.
  PLUGIN_HARNESS_BUILD_COMMAND           Command to build the plugin artifact. Default: npm run build:key-server
  PLUGIN_HARNESS_BUILD_HELPER_SCRIPT      Optional helper file expected for plugin workspaces. Default: build-plugin-release.js.
  PLUGIN_HARNESS_LOADER_PATH              Path to loader module used for legacy decryption fallback.
  PLUGIN_HARNESS_ARTIFACT_SUBDIR           Relative path used for default --skip-build artifact location. Default: Build/plugin/licensing-server.
  PLUGIN_HARNESS_DECRYPT_HELPER            Decrypt helper path. Default: scripts/decrypt-plugin-license-artifact.mjs

Example:
  npm run plugin-harness -- ../path/to/plugin-repo
USAGE
}

normalize_hex() {
  local value=$1
  value="${value#0x}"
  printf '%s' "$value" | tr '[:upper:]' '[:lower:]'
}

is_hex_32() {
  local value=$1
  [[ "$value" =~ ^[0-9a-f]{64}$ ]]
}

require_command() {
  local command_name=$1
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "missing required command: $command_name" >&2
    exit 1
  }
}

fail() {
  echo "error: $*" >&2
  exit 1
}

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    for _ in {1..40}; do
      if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        break
      fi
      sleep 0.1
    done
    if kill -0 "$SERVER_PID" 2>/dev/null; then
      kill -9 "$SERVER_PID" 2>/dev/null || true
      wait "$SERVER_PID" 2>/dev/null || true
    fi
  fi

  if [[ "${KEEP_WORKSPACE:-false}" == "false" && -n "${WORKSPACE_DIR:-}" && -d "$WORKSPACE_DIR" ]]; then
    rm -rf "$WORKSPACE_DIR"
  fi
}

trap cleanup EXIT INT TERM

REPO_PATH=""
PUBLIC_KEY="${PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX:-}"
PRIVATE_KEY="${PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX:-}"
SERVER_PRIVATE_KEY="${PLUGIN_SERVER_PRIVATE_KEY_HEX:-}"
DERIVATION_SECRET=""
ARTIFACT_DIR=""
ADMIN_ADDR="127.0.0.1:5010"
SKIP_BUILD=false
KEEP_WORKSPACE=false
PLUGIN_ID="${PLUGIN_HARNESS_PLUGIN_ID:-plugin-key-broker}"
PLUGIN_REQUIRED_SCOPE="${PLUGIN_HARNESS_REQUIRED_SCOPE:-plugin:base}"
PLUGIN_PUBLIC_KEY_PATH="${PLUGIN_HARNESS_PUBLIC_KEY_PATH:-/v1/public-key}"
PLUGIN_PUBLIC_KEY_PATH="/${PLUGIN_PUBLIC_KEY_PATH#/}"
BUILD_COMMAND="${PLUGIN_HARNESS_BUILD_COMMAND:-npm run build:key-server}"
BUILD_HELPER_SCRIPT="${PLUGIN_HARNESS_BUILD_HELPER_SCRIPT:-build-plugin-release.js}"
LOADER_PATH="${PLUGIN_HARNESS_LOADER_PATH:-}"
SKIP_ARTIFACT_SUBDIR="${PLUGIN_HARNESS_ARTIFACT_SUBDIR:-Build/plugin/licensing-server}"
DECRYPT_HELPER="${PLUGIN_HARNESS_DECRYPT_HELPER:-$ROOT_DIR/scripts/decrypt-plugin-license-artifact.mjs}"

if [[ $# -eq 0 ]]; then
  print_usage
  exit 1
fi

if [[ "$1" != -* ]]; then
  REPO_PATH=$1
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h)
      print_usage
      exit 0
      ;;
    --repo)
      shift
      [[ $# -gt 0 ]] || fail "--repo requires a value"
      REPO_PATH=$1
      ;;
    --admin-addr)
      shift
      [[ $# -gt 0 ]] || fail "--admin-addr requires a value"
      ADMIN_ADDR=$1
      ;;
    --plugin-id)
      shift
      [[ $# -gt 0 ]] || fail "--plugin-id requires a value"
      PLUGIN_ID=$1
      ;;
    --artifact-dir)
      shift
      [[ $# -gt 0 ]] || fail "--artifact-dir requires a value"
      ARTIFACT_DIR=$1
      ;;
    --skip-build)
      SKIP_BUILD=true
      ;;
    --derivation-secret)
      shift
      [[ $# -gt 0 ]] || fail "--derivation-secret requires a value"
      DERIVATION_SECRET=$1
      ;;
    --keep-workspace)
      KEEP_WORKSPACE=true
      ;;
    --build-command)
      shift
      [[ $# -gt 0 ]] || fail "--build-command requires a value"
      BUILD_COMMAND=$1
      ;;
    --loader-path)
      shift
      [[ $# -gt 0 ]] || fail "--loader-path requires a value"
      LOADER_PATH=$1
      ;;
    --*)
      fail "unknown option: $1"
      ;;
    *)
      fail "unexpected argument: $1"
      ;;
  esac
  shift
done

if [[ -z "$REPO_PATH" ]]; then
  fail "plugin repo path is required (first positional arg or --repo)"
fi

REPO_PATH="$(cd "$REPO_PATH" && pwd)"
PUBLIC_KEY="$(normalize_hex "$PUBLIC_KEY")"
PRIVATE_KEY="$(normalize_hex "$PRIVATE_KEY")"
SERVER_PRIVATE_KEY="$(normalize_hex "$SERVER_PRIVATE_KEY")"
DERIVATION_SECRET="$(normalize_hex "$DERIVATION_SECRET")"

if [[ "$SKIP_BUILD" == "true" && ( -z "$PUBLIC_KEY" || -z "$PRIVATE_KEY" ) ]]; then
  fail "--skip-build requires both PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX and PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX"
fi

if [[ -n "$PUBLIC_KEY" ]] && ! is_hex_32 "$PUBLIC_KEY"; then
  fail "public key must be 64 hex chars"
fi
if [[ -n "$PRIVATE_KEY" ]] && ! is_hex_32 "$PRIVATE_KEY"; then
  fail "private key must be 64 hex chars"
fi
if [[ -n "$DERIVATION_SECRET" ]] && ! is_hex_32 "$DERIVATION_SECRET"; then
  fail "--derivation-secret must be 64 hex chars"
fi

if [[ -z "$PUBLIC_KEY" && -z "$PRIVATE_KEY" ]]; then
  KEYPAIR="$(node - <<'NODE'
const { generateKeyPairSync } = require("crypto");
const toHex = (value) => {
  const normalized = String(value).replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(Math.ceil(String(value).length / 4) * 4, "=");
  return Buffer.from(padded, "base64").toString("hex");
};
const pair = generateKeyPairSync("x25519");
const publicHex = toHex(pair.publicKey.export({ format: "jwk" }).x);
const privateHex = toHex(pair.privateKey.export({ format: "jwk" }).d);
process.stdout.write(`${publicHex}:${privateHex}`);
NODE
)"
  PUBLIC_KEY="${KEYPAIR%%:*}"
  PRIVATE_KEY="${KEYPAIR##*:}"
  echo "[harness] no test key provided; generated ephemeral X25519 keypair for this run"
elif [[ -z "$PUBLIC_KEY" && -n "$PRIVATE_KEY" ]]; then
  PUBLIC_KEY="$(node -e 'const { createPublicKey, createPrivateKey } = require("crypto"); const prefix = Buffer.from("302e020100300506032b656e04220420", "hex"); const privateKey = createPrivateKey({ key: Buffer.concat([prefix, Buffer.from(process.argv[1], "hex")]), format: "der", type: "pkcs8" }); const jwk = createPublicKey(privateKey).export({ format: "jwk" }); const normalized = String(jwk.x).replace(/-/g, "+").replace(/_/g, "/"); const padded = normalized.padEnd(Math.ceil(String(jwk.x).length / 4) * 4, "="); process.stdout.write(Buffer.from(padded, "base64").toString("hex"));' "$PRIVATE_KEY")"
  echo "[harness] derived artifact public key from provided private key"
elif [[ -n "$PUBLIC_KEY" && -z "$PRIVATE_KEY" ]]; then
  fail "public key is set but private key is not; provide PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX when local decryption is needed"
fi

if [[ -z "$PUBLIC_KEY" ]] || ! is_hex_32 "$PUBLIC_KEY"; then
  fail "artifact public key could not be resolved"
fi
if [[ -z "$PRIVATE_KEY" ]] || ! is_hex_32 "$PRIVATE_KEY"; then
  fail "artifact private key could not be resolved"
fi

if [[ ! -d "$REPO_PATH" ]]; then
  fail "repo path not found: $REPO_PATH"
fi
if [[ ! -f "$REPO_PATH/package.json" ]]; then
  fail "not a plugin repo: package.json not found at $REPO_PATH"
fi

WORKSPACE_DIR="$(mktemp -d /tmp/sdn-plugin-harness-XXXXXX)"
STAGE_DIR="$WORKSPACE_DIR/licensing-server"
DECRYPTED_WASM="$WORKSPACE_DIR/plugin-module.wasm"
PLUGIN_ROOT="$WORKSPACE_DIR/plugin-root"
CONFIG_PATH="$WORKSPACE_DIR/config.yaml"
LOG_PATH="$WORKSPACE_DIR/sdn.log"
MANIFEST_PATH="$WORKSPACE_DIR/manifest.json"
BUNDLE_PATH="$WORKSPACE_DIR/plugin-bundle.wasm"
PUBKEY_PATH="$WORKSPACE_DIR/key-broker-public-key.json"

if [[ "$SKIP_BUILD" == "false" ]]; then
  echo "[harness] building encrypted licensing artifacts with public key: ${PUBLIC_KEY:0:8}..."
  if [[ "$BUILD_COMMAND" == "npm run build:key-server" && ! -f "$REPO_PATH/$BUILD_HELPER_SCRIPT" && ! -f "$REPO_PATH/scripts/$BUILD_HELPER_SCRIPT" ]]; then
    fail "not a plugin workspace: missing expected $BUILD_HELPER_SCRIPT"
  fi

  (
    cd "$REPO_PATH"
    env \
      PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX="$PUBLIC_KEY" \
      PLUGIN_SERVER_PUBLIC_KEY_HEX="$PUBLIC_KEY" \
      PLUGIN_KEY_SERVER_STAGE_ROOT="$STAGE_DIR" \
      PLUGIN_KEY_SERVER_STAGE_SINGLE_DIR=1 \
      bash -lc "$BUILD_COMMAND"
  )
else
  if [[ -z "$ARTIFACT_DIR" ]]; then
    ARTIFACT_DIR="$REPO_PATH/$SKIP_ARTIFACT_SUBDIR"
  fi
  STAGE_DIR="$ARTIFACT_DIR"
  if [[ ! -d "$STAGE_DIR" ]]; then
    fail "artifact directory not found: $STAGE_DIR"
  fi
  echo "[harness] skipping build; using existing artifacts in: $STAGE_DIR"
fi

PLUGIN_MANIFEST="$STAGE_DIR/manifest.json"
if [[ ! -f "$PLUGIN_MANIFEST" ]]; then
  fail "missing manifest in artifact dir: $PLUGIN_MANIFEST"
fi

if [[ -z "$LOADER_PATH" ]]; then
  fail "set PLUGIN_HARNESS_LOADER_PATH to the plugin loader module (for example: path to protection-key-server/index.js)"
fi
if [[ ! -f "$LOADER_PATH" ]]; then
  fail "loader path not found: $LOADER_PATH"
fi

if [[ ! -f "$DECRYPT_HELPER" ]]; then
  fail "decrypt helper not found: $DECRYPT_HELPER"
fi

echo "[harness] decrypting plugin licensing artifact"
node "$DECRYPT_HELPER" \
  --artifact-dir "$STAGE_DIR" \
  --private-key "$PRIVATE_KEY" \
  --output "$DECRYPTED_WASM" \
  --loader-path "$LOADER_PATH"

if [[ ! -f "$DECRYPTED_WASM" ]] || [[ ! -s "$DECRYPTED_WASM" ]]; then
  fail "decrypted plugin missing or empty: $DECRYPTED_WASM"
fi

mkdir -p "$PLUGIN_ROOT"
PLUGIN_WASM_PATH="$PLUGIN_ID.wasm"
cp "$DECRYPTED_WASM" "$PLUGIN_ROOT/$PLUGIN_WASM_PATH"
cat > "$PLUGIN_ROOT/catalog.json" <<EOF
{
  "plugins": [
    {
      "id": "$PLUGIN_ID",
      "version": "0.0.0-harness",
      "required_scope": "$PLUGIN_REQUIRED_SCOPE",
      "plain_path": "$PLUGIN_WASM_PATH",
      "content_type": "application/wasm",
      "cache_control": "public, max-age=60"
    }
  ]
}
EOF

cat > "$CONFIG_PATH" <<EOF
mode: full
network:
  listen:
    - /ip4/127.0.0.1/tcp/0
  bootstrap: []
  edge_relays: []
  max_connections: 100
  enable_relay: false
  enable_dht: false
  max_message_size: 10485760
  max_schema_name: 256
  max_query_size: 4096
  max_messages_per_second: 100
  max_messages_per_minute: 1000
  rate_limit_burst: 50

storage:
  path: "$WORKSPACE_DIR/data"
  max_size: 1GB
  gc_interval: 1h

schemas:
  validate: true
  strict: false

peers:
  strict_mode: false
  enable_dht: false
  enable_mdns: false

admin:
  enabled: true
  listen_addr: "$ADMIN_ADDR"
  require_auth: false
EOF

if [[ -z "$SERVER_PRIVATE_KEY" ]]; then
  SERVER_PRIVATE_KEY="$(node -e 'const { createECDH } = require("crypto"); const ecdh = createECDH("prime256v1"); ecdh.generateKeys(); process.stdout.write(ecdh.getPrivateKey("hex"));')"
fi
if ! is_hex_32 "$SERVER_PRIVATE_KEY"; then
  fail "server private key must be 64 hex chars after normalization"
fi

if [[ -z "$DERIVATION_SECRET" ]]; then
  DERIVATION_SECRET="$(node -e 'const { randomBytes } = require("crypto"); process.stdout.write(randomBytes(32).toString("hex"));')"
fi
if ! is_hex_32 "$DERIVATION_SECRET"; then
  fail "failed to generate/validate derivation secret"
fi

ALLOWED_DOMAINS="${ADMIN_ADDR}"

echo "[harness] starting SDN daemon at http://$ADMIN_ADDR"
(
  cd "$ROOT_DIR/sdn-server"
  env \
    SDN_PLUGIN_ROOT="$PLUGIN_ROOT" \
    PLUGIN_SERVER_PRIVATE_KEY_HEX="$SERVER_PRIVATE_KEY" \
    DERIVATION_SECRET="$DERIVATION_SECRET" \
    PLUGIN_KEYSERVER_ALLOWED_DOMAINS="$ALLOWED_DOMAINS" \
    SDN_PLUGIN_DEBUG=1 \
    go run ./cmd/spacedatanetwork daemon --config "$CONFIG_PATH" > "$LOG_PATH" 2>&1
) &
SERVER_PID=$!

WAIT_URL="http://$ADMIN_ADDR/api/v1/plugins/manifest"
for _ in {1..80}; do
  if curl -sS "$WAIT_URL" > "$MANIFEST_PATH" 2>/dev/null; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    fail "SDN daemon exited before startup. Log: $LOG_PATH"
  fi
  sleep 0.5
done

if [[ ! -s "$MANIFEST_PATH" ]]; then
  fail "timed out waiting for SDN manifest endpoint: $WAIT_URL"
fi

for _ in {1..30}; do
  if ! curl -sS "$WAIT_URL" > "$MANIFEST_PATH" 2>/dev/null; then
    fail "unable to read plugin manifest from SDN"
  fi

  PLUGIN_STATUS="$(MANIFEST_PATH="$MANIFEST_PATH" PLUGIN_ID="$PLUGIN_ID" node -e 'const fs = require("fs"); const manifest = JSON.parse(fs.readFileSync(process.env.MANIFEST_PATH, "utf8")); const plugins = Array.isArray(manifest.plugins) ? manifest.plugins : []; const pluginId = process.env.PLUGIN_ID || ""; const item = pluginId ? plugins.find((entry) => entry && entry.id === pluginId) : plugins[0]; if (!item) { process.exit(2); } process.stdout.write(String(item.status || ""));')"

  if [[ "$PLUGIN_STATUS" == "running" ]]; then
    break
  fi
  sleep 1
done

if [[ "$PLUGIN_STATUS" != "running" ]]; then
  fail "plugin '$PLUGIN_ID' not running. Latest manifest: $(cat "$MANIFEST_PATH")"
fi

if ! curl -sS "http://$ADMIN_ADDR/api/v1/plugins/$PLUGIN_ID/bundle" > "$BUNDLE_PATH"; then
  fail "bundle endpoint request failed"
fi

if ! command -v wc >/dev/null 2>&1; then
  BUNDLE_SIZE=0
else
  BUNDLE_SIZE="$(wc -c < "$BUNDLE_PATH")"
fi
if [[ "$BUNDLE_SIZE" -le 0 ]]; then
  fail "bundle is empty"
fi

if ! curl -sS "http://$ADMIN_ADDR/$PLUGIN_ID$PLUGIN_PUBLIC_KEY_PATH" > "$PUBKEY_PATH"; then
  fail "public-key endpoint request failed"
fi
if ! node -e 'const fs=require("fs"); const payload=JSON.parse(fs.readFileSync(process.argv[1],"utf8")); if (typeof payload.publicKeyHex !== "string" || payload.publicKeyHex.length !== 64) { process.exit(1); }' "$PUBKEY_PATH"; then
  fail "public-key response validation failed"
fi

echo "[harness] manifest:"
cat "$MANIFEST_PATH"
echo "[harness] bundle size: ${BUNDLE_SIZE} bytes"
echo "[harness] public-key response:"
cat "$PUBKEY_PATH"
echo "[harness] log file: $LOG_PATH"
echo "[harness] PASS: plugin loaded and endpoints are reachable"
