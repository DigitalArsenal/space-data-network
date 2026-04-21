#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
wallet_config_path="${repo_root}/config/dev-wallet.env"
generated_config_path="${repo_root}/.tmp/admin-dev.yaml"

if [[ ! -f "${wallet_config_path}" ]]; then
  echo "missing tracked dev wallet config: ${wallet_config_path}" >&2
  exit 1
fi

# shellcheck disable=SC1090
source "${wallet_config_path}"

if [[ -z "${SDN_TRACKED_DEV_ADMIN_MNEMONIC:-}" ]]; then
  echo "tracked dev wallet mnemonic is missing" >&2
  exit 1
fi

if [[ -z "${SDN_TRACKED_DEV_ADMIN_XPUB:-}" ]]; then
  echo "tracked dev wallet xpub is missing" >&2
  exit 1
fi

if [[ -z "${SDN_TRACKED_DEV_ADMIN_NAME:-}" ]]; then
  echo "tracked dev wallet name is missing" >&2
  exit 1
fi

rm -f "${generated_config_path}"

SDN_ADMIN_DEV_NO_RUN=1 "${repo_root}/scripts/admin-dev.sh" >/dev/null

if [[ ! -f "${generated_config_path}" ]]; then
  echo "admin-dev did not write ${generated_config_path}" >&2
  exit 1
fi

grep -F "xpub: \"${SDN_TRACKED_DEV_ADMIN_XPUB}\"" "${generated_config_path}" >/dev/null
grep -F "name: \"${SDN_TRACKED_DEV_ADMIN_NAME}\"" "${generated_config_path}" >/dev/null
