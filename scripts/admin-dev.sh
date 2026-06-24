#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_root="${repo_root}/.tmp"
server_port="${SDN_DEV_SERVER_PORT:-${SDN_ADMIN_UI_PORT:-5173}}"
dev_tls_mode="${SDN_ADMIN_DEV_TLS_MODE:-${SDN_DEV_TLS_MODE:-managed}}"
case "${dev_tls_mode}" in
  disabled|managed|static)
    ;;
  http)
    dev_tls_mode="disabled"
    ;;
  https)
    dev_tls_mode="managed"
    ;;
  *)
    echo "Unsupported SDN_ADMIN_DEV_TLS_MODE=${dev_tls_mode}; expected managed, static, disabled, http, or https" >&2
    exit 1
    ;;
esac
server_scheme="https"
if [[ "${dev_tls_mode}" == "disabled" ]]; then
  server_scheme="http"
fi
server_base_url="${SDN_DEV_SERVER_BASE_URL:-${server_scheme}://127.0.0.1:${server_port}}"
http_challenge_port="${SDN_DEV_HTTP_CHALLENGE_PORT:-5080}"
remote_provider_url="${SDN_DEV_PROVIDER_URL:-https://sdn.spaceaware.io/api/module-delivery/provider}"
remote_bootstrap_addr="${SDN_DEV_BOOTSTRAP_ADDR:-/ip4/159.203.150.8/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45}"
storage_path="${SDN_DEV_STORAGE_PATH:-${repo_root}/data/admin-dev}"
plugin_root="${SDN_PLUGIN_ROOT:-${storage_path}/license/plugins}"
frontend_path="${SDN_FRONTEND_PATH:-${repo_root}/sdn-js/ui/dist}"
webui_path="${SDN_WEBUI_PATH:-}"
wallet_ui_path="${SDN_WALLET_UI_PATH:-}"
licensing_wasm_path="${ORBPRO_LICENSING_WASM_PATH:-}"
dev_wallet_config_path="${SDN_DEV_WALLET_CONFIG:-${repo_root}/config/dev-wallet.env}"
ipfs_api_url="${SDN_IPFS_API_URL:-${SDN_DEV_IPFS_API_URL:-}}"
ipfs_gateway_url="${SDN_IPFS_GATEWAY_URL:-${SDN_DEV_IPFS_GATEWAY_URL:-}}"
ipfs_api_candidates="${SDN_DEV_IPFS_API_CANDIDATES:-http://127.0.0.1:5001}"
config_path="${tmp_root}/admin-dev.yaml"
server_pid=""

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

ensure_webui_dependencies() {
  local react_scripts_pkg="${repo_root}/webui/node_modules/react-scripts/package.json"

  if [[ -f "${react_scripts_pkg}" ]]; then
    return
  fi

  echo "Installing webui dependencies..."
  (cd "${repo_root}/webui" && npm install >/dev/null)
}

has_newer_inputs() {
  local reference="$1"
  shift

  if [[ ! -f "${reference}" ]]; then
    return 0
  fi

  local candidate=""
  for candidate in "$@"; do
    if [[ -d "${candidate}" ]]; then
      if find "${candidate}" -type f -newer "${reference}" -print -quit | grep -q .; then
        return 0
      fi
      continue
    fi
    if [[ -f "${candidate}" && "${candidate}" -nt "${reference}" ]]; then
      return 0
    fi
  done

  return 1
}

trim() {
  local value="${1:-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

probe_kubo_api_url() {
  local candidate=""
  local IFS=','
  for candidate in ${ipfs_api_candidates}; do
    candidate="$(trim "${candidate}")"
    if [[ -z "${candidate}" ]]; then
      continue
    fi
    candidate="${candidate%/}"
    if curl -fsS -X POST "${candidate}/api/v0/version" >/dev/null 2>&1; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}

multiaddr_to_http_url() {
  local multiaddr="${1:-}"
  if [[ "${multiaddr}" =~ ^/ip4/([^/]+)/tcp/([0-9]+)$ ]]; then
    printf 'http://%s:%s' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return 0
  fi
  if [[ "${multiaddr}" =~ ^/ip6/([^/]+)/tcp/([0-9]+)$ ]]; then
    printf 'http://[%s]:%s' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return 0
  fi
  if [[ "${multiaddr}" =~ ^/dns4/([^/]+)/tcp/([0-9]+)$ ]]; then
    printf 'http://%s:%s' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return 0
  fi
  if [[ "${multiaddr}" =~ ^/dns6/([^/]+)/tcp/([0-9]+)$ ]]; then
    printf 'http://[%s]:%s' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return 0
  fi
  return 1
}

discover_kubo_gateway_url() {
  local api_url="${1:-}"
  local response=""
  local gateway_multiaddr=""

  if [[ -z "${api_url}" ]]; then
    return 1
  fi

  if ! response="$(curl -fsS -X POST "${api_url%/}/api/v0/config?arg=Addresses.Gateway" 2>/dev/null)"; then
    return 1
  fi

  gateway_multiaddr="$(printf '%s' "${response}" | sed -n 's/.*"Value":"\([^"]*\)".*/\1/p')"
  if [[ -z "${gateway_multiaddr}" ]]; then
    return 1
  fi

  multiaddr_to_http_url "${gateway_multiaddr}"
}

webui_build_contains_embedded_auth() {
  if [[ ! -d "${repo_root}/webui/build/static/js" ]]; then
    return 1
  fi
  if command -v rg >/dev/null 2>&1; then
    rg -q "sdnAuth|selectAuthUser|client_pubkey_hex|walletIdentity" "${repo_root}/webui/build/static/js"
    return $?
  fi

  grep -R -E -q "sdnAuth|selectAuthUser|client_pubkey_hex|walletIdentity" "${repo_root}/webui/build/static/js"
}

ensure_sdn_ui_build() {
  if [[ "${frontend_path}" != "${repo_root}/sdn-js/ui/dist" ]]; then
    return
  fi

  if has_newer_inputs \
    "${frontend_path}/index.html" \
    "${repo_root}/sdn-js/package.json" \
    "${repo_root}/sdn-js/ui/index.html" \
    "${repo_root}/sdn-js/ui/vite.config.mts" \
    "${repo_root}/sdn-js/ui/src" \
    "${repo_root}/sdn-js/src"
  then
    echo "Building hosted SDN UI..."
    (
      cd "${repo_root}/sdn-js"
      export VITE_SDN_DEFAULT_PROVIDER_URL="${remote_provider_url}"
      npm run build:ui >/dev/null
    )
  fi
}

ensure_webui_build() {
  if [[ -n "${webui_path}" && "${webui_path}" != "${repo_root}/webui/build" ]]; then
    return
  fi

  ensure_webui_dependencies

  if webui_build_contains_embedded_auth || has_newer_inputs \
    "${repo_root}/webui/build/index.html" \
    "${repo_root}/webui/package.json" \
    "${repo_root}/webui/config-overrides.js" \
    "${repo_root}/webui/public" \
    "${repo_root}/webui/src"
  then
    echo "Building clean IPFS WebUI..."
    (cd "${repo_root}/webui" && npm run build >/dev/null)
  fi

  webui_path="${repo_root}/webui/build"
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
  webui_path="${repo_root}/webui/build"
fi

if [[ -z "${licensing_wasm_path}" ]]; then
  candidate="${repo_root}/../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm"
  if [[ -f "${candidate}" ]]; then
    licensing_wasm_path="${candidate}"
  fi
fi

if [[ -f "${dev_wallet_config_path}" ]]; then
  # shellcheck disable=SC1090
  source "${dev_wallet_config_path}"
fi

if [[ -z "${ipfs_api_url}" ]]; then
  detected_ipfs_api_url="$(probe_kubo_api_url || true)"
  if [[ -n "${detected_ipfs_api_url}" ]]; then
    ipfs_api_url="${detected_ipfs_api_url}"
  fi
fi

if [[ -n "${ipfs_api_url}" && -z "${ipfs_gateway_url}" ]]; then
  detected_ipfs_gateway_url="$(discover_kubo_gateway_url "${ipfs_api_url}" || true)"
  if [[ -n "${detected_ipfs_gateway_url}" ]]; then
    ipfs_gateway_url="${detected_ipfs_gateway_url}"
  fi
fi

dev_admin_name="${SDN_DEV_ADMIN_NAME:-${SDN_TRACKED_DEV_ADMIN_NAME:-}}"
dev_admin_xpub="${SDN_DEV_ADMIN_XPUB:-${SDN_TRACKED_DEV_ADMIN_XPUB:-}}"
dev_admin_signing_pubkey_hex="${SDN_DEV_ADMIN_SIGNING_PUBKEY_HEX:-${SDN_TRACKED_DEV_ADMIN_SIGNING_PUBKEY_HEX:-}}"

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

ipfs_api_yaml=""
if [[ -n "${ipfs_api_url}" ]]; then
  ipfs_api_yaml="  ipfs_api_url: \"${ipfs_api_url}\""
fi

ipfs_gateway_yaml=""
if [[ -n "${ipfs_gateway_url}" ]]; then
  ipfs_gateway_yaml="  ipfs_gateway_url: \"${ipfs_gateway_url}\""
fi

cat > "${config_path}" <<EOF
mode: full

network:
  listen:
    - /ip4/0.0.0.0/tcp/14001
    - /ip4/0.0.0.0/tcp/14080/ws
    - /ip4/0.0.0.0/udp/14001/quic-v1
  bootstrap:
    - "${remote_bootstrap_addr}"
  edge_relays:
    - "${remote_bootstrap_addr}"
  max_connections: 100
  enable_relay: true
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
  http_challenge_addr: "127.0.0.1:${http_challenge_port}"
  require_auth: true
  session_expiry: 24h
  totp_required: false
  tls_mode: ${dev_tls_mode}
  tls_cache_dir: "${storage_path}/tls"
  frontend_path: "${frontend_path}"
${webui_yaml}
${wallet_ui_yaml}
${ipfs_api_yaml}
${ipfs_gateway_yaml}

users:
  - xpub: "${dev_admin_xpub}"
    signing_pubkey_hex: "${dev_admin_signing_pubkey_hex}"
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
  if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
    kill "${server_pid}" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
  exit "${exit_code}"
}

trap cleanup INT TERM EXIT

ensure_sdn_js_dependencies
ensure_sdn_ui_build
ensure_webui_build

if [[ -n "${ipfs_api_url}" ]]; then
  echo "Using Kubo RPC API via ${ipfs_api_url}"
fi
if [[ -n "${ipfs_gateway_url}" ]]; then
  echo "Using Kubo gateway via ${ipfs_gateway_url}"
fi

echo "Using tracked dev wallet config: ${dev_wallet_config_path}"
echo "Starting single dev server on ${server_base_url} ..."
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
  if curl -kfsS "${server_base_url}/api/node/info" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! curl -kfsS "${server_base_url}/api/node/info" >/dev/null 2>&1; then
  echo "Local sdn-server did not become ready at ${server_base_url}" >&2
  exit 1
fi

echo "Remote provider seed: ${remote_provider_url}"
echo "SDN UI: ${server_base_url}/"
echo "IPFS WebUI: ${server_base_url}/webui/"
echo "Admin UI: ${server_base_url}/admin/"
echo "Bootstrap cert: ${server_base_url}/bootstrap.crt"

wait "${server_pid}"
