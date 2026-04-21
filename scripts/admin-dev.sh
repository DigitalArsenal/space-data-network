#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_root="${repo_root}/.tmp"
server_port="${SDN_DEV_SERVER_PORT:-5010}"
ui_port="${SDN_ADMIN_UI_PORT:-5173}"
server_base_url="${SDN_DEV_SERVER_BASE_URL:-http://127.0.0.1:${server_port}}"
remote_provider_url="${SDN_DEV_PROVIDER_URL:-https://sdn.spaceaware.io/api/module-delivery/provider}"
remote_bootstrap_addr="${SDN_DEV_BOOTSTRAP_ADDR:-/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45}"
storage_path="${SDN_DEV_STORAGE_PATH:-${repo_root}/data/admin-dev}"
plugin_root="${SDN_PLUGIN_ROOT:-${storage_path}/license/plugins}"
webui_path="${SDN_WEBUI_PATH:-}"
wallet_ui_path="${SDN_WALLET_UI_PATH:-}"
licensing_wasm_path="${ORBPRO_LICENSING_WASM_PATH:-}"
dev_wallet_config_path="${SDN_DEV_WALLET_CONFIG:-${repo_root}/config/dev-wallet.env}"
config_path="${tmp_root}/admin-dev.yaml"
server_pid=""
ui_pid=""

mkdir -p "${tmp_root}" "${storage_path}" "${plugin_root}"

ensure_sdn_js_dependencies() {
  local vite_pkg="${repo_root}/sdn-js/node_modules/vite/package.json"
  local monaco_pkg="${repo_root}/sdn-js/node_modules/monaco-editor/package.json"
  local sds_rec="${repo_root}/sdn-js/node_modules/spacedatastandards.org/lib/js/REC/REC.js"
  local sds_plg="${repo_root}/sdn-js/node_modules/spacedatastandards.org/lib/js/PLG/PLG.js"
  local sdk_enc="${repo_root}/sdn-js/node_modules/spacedatastandards.org/lib/js/ENC/main.js"

  if [[ -f "${vite_pkg}" && -f "${monaco_pkg}" && -f "${sds_rec}" && -f "${sds_plg}" && -f "${sdk_enc}" ]]; then
    return
  fi

  echo "Installing sdn-js dependencies..."
  (cd "${repo_root}/sdn-js" && npm install >/dev/null)
}

if [[ -z "${wallet_ui_path}" ]]; then
  for candidate in \
    "${repo_root}/../hd-wallet-wasm/wallet-ui" \
    "${repo_root}/sdn-js/node_modules/hd-wallet-ui" \
    "${repo_root}/../hd-wallet-wasm/wallet-ui/dist" \
    "${repo_root}/sdn-js/node_modules/hd-wallet-ui/dist"
  do
    if [[ -f "${candidate}/src/app.js" && -f "${candidate}/styles/widget.css" ]]; then
      wallet_ui_path="${candidate}"
      break
    fi
    if [[ -f "${candidate}/index.html" ]]; then
      wallet_ui_path="${candidate}"
      break
    fi
  done
fi

if [[ -z "${webui_path}" ]]; then
  candidate="${repo_root}/webui/build"
  if [[ -f "${candidate}/index.html" ]]; then
    webui_path="${candidate}"
  fi
fi

if [[ -z "${licensing_wasm_path}" ]]; then
  candidate="${repo_root}/../space-data-network-plugins/packages/licensing/dist/isomorphic/module.wasm"
  if [[ -f "${candidate}" ]]; then
    licensing_wasm_path="${candidate}"
  fi
fi

if [[ -f "${dev_wallet_config_path}" ]]; then
  # shellcheck disable=SC1090
  source "${dev_wallet_config_path}"
fi

dev_admin_name="${SDN_DEV_ADMIN_NAME:-${SDN_TRACKED_DEV_ADMIN_NAME:-}}"
dev_admin_xpub="${SDN_DEV_ADMIN_XPUB:-${SDN_TRACKED_DEV_ADMIN_XPUB:-}}"

if [[ -z "${dev_admin_name}" || -z "${dev_admin_xpub}" ]]; then
  echo "admin-dev requires a tracked dev wallet config or SDN_DEV_ADMIN_NAME/SDN_DEV_ADMIN_XPUB overrides" >&2
  exit 1
fi

webui_yaml=""
if [[ -n "${webui_path}" ]]; then
  webui_yaml="  webui_path: \"${webui_path}\""
fi

wallet_ui_yaml=""
if [[ -n "${wallet_ui_path}" ]]; then
  wallet_ui_yaml="  wallet_ui_path: \"${wallet_ui_path}\""
fi

cat > "${config_path}" <<EOF
mode: full

network:
  listen:
    - /ip4/127.0.0.1/tcp/14001
    - /ip4/127.0.0.1/tcp/14080/ws
  bootstrap:
    - "${remote_bootstrap_addr}"
  edge_relays:
    - "${remote_bootstrap_addr}"
  max_connections: 100
  enable_relay: false
  max_message_size: 10485760
  max_schema_name: 256
  max_query_size: 4096

storage:
  path: "${storage_path}"
  max_size: 1GB
  gc_interval: 1h

schemas:
  validate: true
  strict: false

tor:
  enabled: false

peers:
  strict_mode: false
  enable_dht: true
  enable_mdns: true

admin:
  enabled: true
  listen_addr: "127.0.0.1:${server_port}"
  require_auth: true
  session_expiry: 24h
  totp_required: false
  tls_enabled: false
${webui_yaml}
${wallet_ui_yaml}

users:
  - xpub: "${dev_admin_xpub}"
    trust_level: "admin"
    name: "${dev_admin_name}"

setup:
  token_expiry: 10m
  data_path: "${storage_path}"
EOF

if [[ "${SDN_ADMIN_DEV_NO_RUN:-}" == "1" ]]; then
  echo "Wrote ${config_path}"
  exit 0
fi

cleanup() {
  local exit_code=$?
  if [[ -n "${ui_pid}" ]] && kill -0 "${ui_pid}" 2>/dev/null; then
    kill "${ui_pid}" 2>/dev/null || true
  fi
  if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
    kill "${server_pid}" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
  exit "${exit_code}"
}

trap cleanup INT TERM EXIT

ensure_sdn_js_dependencies

echo "Using tracked dev wallet config: ${dev_wallet_config_path}"
echo "Starting local sdn-server on ${server_base_url} ..."
(
  cd "${repo_root}"
  export SDN_PLUGIN_ROOT="${plugin_root}"
  if [[ -n "${wallet_ui_path}" ]]; then
    export SDN_WALLET_UI_PATH="${wallet_ui_path}"
  fi
  if [[ -n "${licensing_wasm_path}" ]]; then
    export ORBPRO_LICENSING_WASM_PATH="${licensing_wasm_path}"
  fi
  ./scripts/go-with-wasmedge.sh run ./cmd/spacedatanetwork daemon --config "${config_path}"
) &
server_pid=$!

for _ in $(seq 1 60); do
  if curl -fsS "${server_base_url}/api/node/info" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! curl -fsS "${server_base_url}/api/node/info" >/dev/null 2>&1; then
  echo "Local sdn-server did not become ready at ${server_base_url}" >&2
  exit 1
fi

echo "Starting Vite admin UI on http://127.0.0.1:${ui_port}/admin/ ..."
echo "Remote provider seed: ${remote_provider_url}"
(
  cd "${repo_root}/sdn-js"
  export SDN_UI_PROXY_TARGET="${server_base_url}"
  export SDN_ADMIN_UI_PORT="${ui_port}"
  export VITE_SDN_DEFAULT_PROVIDER_URL="${remote_provider_url}"
  npx vite --config ui/vite.config.mts --host 127.0.0.1 --port "${ui_port}"
) &
ui_pid=$!

wait "${ui_pid}"
