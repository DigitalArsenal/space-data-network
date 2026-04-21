#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
generated_config_path="${repo_root}/.tmp/admin-dev.yaml"

api_port="$(
  node -e "const net=require('node:net'); const server=net.createServer(); server.listen(0, '127.0.0.1', () => { console.log(server.address().port); server.close(); });"
)"
gateway_port="$(
  node -e "const net=require('node:net'); const server=net.createServer(); server.listen(0, '127.0.0.1', () => { console.log(server.address().port); server.close(); });"
)"

cleanup() {
  if [[ -n "${api_pid:-}" ]] && kill -0 "${api_pid}" 2>/dev/null; then
    kill "${api_pid}" 2>/dev/null || true
  fi
  if [[ -n "${gateway_pid:-}" ]] && kill -0 "${gateway_pid}" 2>/dev/null; then
    kill "${gateway_pid}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

API_PORT="${api_port}" GATEWAY_PORT="${gateway_port}" node -e "
  const http = require('node:http');
  const apiPort = Number(process.env.API_PORT);
  const gatewayPort = Number(process.env.GATEWAY_PORT);
  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/api/v0/version') {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ Version: '0.40.0-test' }));
      return;
    }
    if (req.method === 'POST' && req.url === '/api/v0/config?arg=Addresses.Gateway') {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ Key: 'Addresses.Gateway', Value: '/ip4/127.0.0.1/tcp/' + gatewayPort }));
      return;
    }
    res.writeHead(404, { 'content-type': 'text/plain' });
    res.end('not found');
  });
  server.listen(apiPort, '127.0.0.1');
" &
api_pid=$!

GATEWAY_PORT="${gateway_port}" node -e "
  const http = require('node:http');
  const port = Number(process.env.GATEWAY_PORT);
  const server = http.createServer((_req, res) => {
    res.writeHead(200, { 'content-type': 'text/plain' });
    res.end('ok');
  });
  server.listen(port, '127.0.0.1');
" &
gateway_pid=$!

for _ in $(seq 1 20); do
  if curl -fsS -X POST "http://127.0.0.1:${api_port}/api/v0/version" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

rm -f "${generated_config_path}"

SDN_ADMIN_DEV_NO_RUN=1 \
API_PORT="${api_port}" \
GATEWAY_PORT="${gateway_port}" \
SDN_DEV_IPFS_API_CANDIDATES="http://127.0.0.1:${api_port}" \
"${repo_root}/scripts/admin-dev.sh" >/dev/null

if [[ ! -f "${generated_config_path}" ]]; then
  echo "admin-dev did not write ${generated_config_path}" >&2
  exit 1
fi

grep -F "ipfs_api_url: \"http://127.0.0.1:${api_port}\"" "${generated_config_path}" >/dev/null
grep -F "ipfs_gateway_url: \"http://127.0.0.1:${gateway_port}\"" "${generated_config_path}" >/dev/null
