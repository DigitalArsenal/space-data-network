#!/bin/sh
# Wait for bootstrap node and get its peer ID

BOOTSTRAP_HOST="${BOOTSTRAP_HOST:-full-node-1}"
BOOTSTRAP_PORT="${BOOTSTRAP_PORT:-4001}"
MAX_WAIT="${MAX_WAIT:-30}"

echo "Waiting for bootstrap node ${BOOTSTRAP_HOST}:${BOOTSTRAP_PORT}..."

# Wait for the bootstrap node to be reachable
waited=0
while ! nc -z "${BOOTSTRAP_HOST}" "${BOOTSTRAP_PORT}" 2>/dev/null; do
    if [ "$waited" -ge "$MAX_WAIT" ]; then
        echo "Timeout waiting for bootstrap node"
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done

echo "Bootstrap node is reachable"

# Start the edge node with the provided arguments
exec "$@"
