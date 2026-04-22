#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_path="${repo_root}/.tmp/admin-dev.yaml"

rm -f "${config_path}"

SDN_ADMIN_DEV_NO_RUN=1 "${repo_root}/scripts/admin-dev.sh" >/dev/null

[[ -f "${config_path}" ]]

grep -Fq -- '    - /ip4/0.0.0.0/tcp/14001' "${config_path}"
grep -Fq -- '    - /ip4/0.0.0.0/tcp/14080/ws' "${config_path}"
grep -Fq -- '    - /ip4/0.0.0.0/udp/14001/quic-v1' "${config_path}"
grep -Fq -- '  enable_relay: true' "${config_path}"
