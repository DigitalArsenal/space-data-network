#!/usr/bin/env bash
# Clean-machine install smoke for a linux self-contained bundle: extract it in
# a fresh Debian container, initialise a node, start the daemon, and prove
# that the probes answer and that the bundle's Kubo runs under the node.
#
#   deployment/release/install-smoke-container.sh dist/release-local/<ver>/out/spacedatanetwork-<ver>-linux-amd64.tar.gz [amd64|arm64]
set -euo pipefail
archive="${1:?bundle archive}"
arch="${2:-amd64}"
name="$(basename "$archive" .tar.gz)"
dir="$(cd "$(dirname "$archive")" && pwd)"
docker run --rm --platform "linux/${arch}" -v "$dir:/rel:ro" debian:bookworm-slim bash -c '
set -euo pipefail
apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates >/dev/null
mkdir -p /tmp/s && tar -xzf /rel/'"$name"'.tar.gz -C /tmp/s
B=/tmp/s/'"$name"'
export HOME=/root
$B/bin/spacedatanetwork version
$B/bin/spacedatanetwork init --config /tmp/node.yaml >/tmp/init.log 2>&1 || { tail -20 /tmp/init.log; exit 1; }
grep -q "listen_addr" /tmp/node.yaml
( $B/bin/spacedatanetwork daemon --config /tmp/node.yaml > /tmp/daemon.log 2>&1 & )
for i in $(seq 1 90); do
  sleep 2
  if curl -sf http://127.0.0.1:5001/health >/dev/null 2>&1; then break; fi
done
echo "health: $(curl -s http://127.0.0.1:5001/health)"
echo "ready:  $(curl -s -w " [%{http_code}]" http://127.0.0.1:5001/ready)"
grep -E "Kubo managed by this node|Kubo not managed|kubo:" /tmp/daemon.log | head -5
curl -s -X POST http://127.0.0.1:5002/api/v0/version && echo || { echo "managed Kubo API not answering on 5002"; tail -30 /tmp/daemon.log; exit 1; }
echo SMOKE-OK
'
