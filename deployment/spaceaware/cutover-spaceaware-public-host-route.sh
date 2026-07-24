#!/usr/bin/env bash

set -euo pipefail

installer_path="${SPACEAWARE_ROUTE_INSTALLER_PATH:-/opt/spacedatanetwork/deployment/spaceaware/install-spaceaware-public-host-route.mjs}"
verifier_path="${SPACEAWARE_ROUTE_VERIFIER_PATH:-/opt/spacedatanetwork/deployment/spaceaware/verify-spaceaware-public-host-route.mjs}"
max_attempts="${SPACEAWARE_CUTOVER_MAX_ATTEMPTS:-30}"

if [[ ! "${max_attempts}" =~ ^[1-9][0-9]*$ ]]; then
    echo "SPACEAWARE_CUTOVER_MAX_ATTEMPTS must be a positive integer." >&2
    exit 1
fi
if [[ ! -f "${installer_path}" || -L "${installer_path}" ]]; then
    echo "SpaceAware route installer must be a regular file: ${installer_path}" >&2
    exit 1
fi
if [[ ! -f "${verifier_path}" || -L "${verifier_path}" ]]; then
    echo "SpaceAware route verifier must be a regular file: ${verifier_path}" >&2
    exit 1
fi

required_units=(
    spaceaware-sdn.service
    spaceaware-ipfs.service
    spaceaware-ingest.service
    spaceaware-terrain-cache.service
)

for unit in "${required_units[@]}"; do
    load_state="$(systemctl show --property=LoadState --value "${unit}")"
    if [[ "${load_state}" != "loaded" ]]; then
        echo "Required SpaceAware unit is not loaded: ${unit} (${load_state:-unknown})" >&2
        exit 1
    fi
    if ! systemctl is-active --quiet "${unit}"; then
        echo "Required SpaceAware unit is not active: ${unit}" >&2
        exit 1
    fi
done

spaceaware_ready=false
for ((attempt = 1; attempt <= max_attempts; attempt += 1)); do
    if node "${verifier_path}" --mode loopback; then
        spaceaware_ready=true
        break
    fi
    if (( attempt < max_attempts )); then
        sleep 1
    fi
done
if [[ "${spaceaware_ready}" != true ]]; then
    echo "SpaceAware loopback endpoints did not become ready after ${max_attempts} attempts." >&2
    exit 1
fi

node "${installer_path}" --verify-script "${verifier_path}"
