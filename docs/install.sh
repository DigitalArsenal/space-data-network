#!/usr/bin/env bash
# Public Space Data Network shell installer entrypoint.
#
# This stub used to pipe scripts/install.sh from the repository's MAIN branch
# into bash, so any push to main ran on every new install (security audit
# SEC-045). It now runs one PINNED, SIGNED release asset instead:
#
#   - SDN_INSTALLER_RELEASE_TAG names the release whose install.sh it runs;
#   - SDN_INSTALLER_SHA256 is that install.sh's SHA-256;
#   - the release's spacedatanetwork-checksums.txt must carry a valid signature
#     by the release key below and list install.sh with that same SHA-256.
#
# The owner sets the pins after signing a release offline
# (deployment/release/RELEASE-SIGNING.md). Until they are set, or when any
# check fails, nothing is executed.

set -euo pipefail

REPO="DigitalArsenal/space-data-network"
SDN_INSTALLER_RELEASE_TAG=''
SDN_INSTALLER_SHA256=''

# The release signing PUBLIC key (PEM); the same key scripts/install.sh carries.
# BEGIN SDN RELEASE SIGNING KEY
SDN_RELEASE_SIGNING_KEY_PEM=''
# END SDN RELEASE SIGNING KEY

fail() {
  echo "Space Data Network installer: $*" >&2
  exit 1
}

# Everything runs inside main, called on the last line, so a download cut short
# by the network executes nothing.
main() {
  if [ -z "$SDN_INSTALLER_RELEASE_TAG" ] || [ -z "$SDN_INSTALLER_SHA256" ] || [ -z "$SDN_RELEASE_SIGNING_KEY_PEM" ]; then
    fail "no signed installer release is pinned yet; refusing to run an unverified installer. See https://github.com/${REPO}/blob/main/deployment/release/RELEASE-SIGNING.md"
  fi
  command -v openssl >/dev/null 2>&1 || fail "openssl is required to verify the installer's release signature"

  fetch() {
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
      wget -q "$1" -O "$2"
    else
      fail "curl or wget is required to install Space Data Network"
    fi
  }

  sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
      shasum -a 256 "$1" | awk '{print $1}'
    else
      fail "sha256sum or shasum is required to verify the installer"
    fi
  }

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  base="https://github.com/${REPO}/releases/download/${SDN_INSTALLER_RELEASE_TAG}"
  fetch "${base}/install.sh" "${tmp}/install.sh" || fail "could not download install.sh from ${SDN_INSTALLER_RELEASE_TAG}"
  fetch "${base}/spacedatanetwork-checksums.txt" "${tmp}/checksums.txt" || fail "could not download the release checksums"
  fetch "${base}/spacedatanetwork-checksums.txt.sig" "${tmp}/checksums.txt.sig" || fail "the release has no checksum signature"

  printf '%s\n' "$SDN_RELEASE_SIGNING_KEY_PEM" > "${tmp}/release-key.pem"
  openssl dgst -sha256 -verify "${tmp}/release-key.pem" -signature "${tmp}/checksums.txt.sig" "${tmp}/checksums.txt" >/dev/null 2>&1 \
    || fail "the release checksum signature does not verify; refusing to run the installer"

  signed="$(awk '$2 == "install.sh" || $2 == "*install.sh" { print $1; exit }' "${tmp}/checksums.txt")"
  actual="$(sha256_of "${tmp}/install.sh")"
  [ "$signed" = "$SDN_INSTALLER_SHA256" ] || fail "the signed checksum list does not pin this installer"
  [ "$actual" = "$SDN_INSTALLER_SHA256" ] || fail "install.sh does not match its pinned SHA-256; refusing to run it"

  bash "${tmp}/install.sh" "$@" < /dev/null
}

main "$@"
