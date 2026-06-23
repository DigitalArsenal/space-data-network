#!/usr/bin/env bash
# Public Space Data Network installer entrypoint for GitHub Pages.

set -euo pipefail

INSTALLER_URL="${SDN_INSTALLER_URL:-https://raw.githubusercontent.com/DigitalArsenal/space-data-network/main/scripts/install.sh}"

if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$INSTALLER_URL" | bash -s -- "$@"
elif command -v wget >/dev/null 2>&1; then
  wget -qO- "$INSTALLER_URL" | bash -s -- "$@"
else
  echo "curl or wget is required to install Space Data Network" >&2
  exit 1
fi
