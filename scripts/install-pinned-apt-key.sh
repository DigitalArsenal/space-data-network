#!/usr/bin/env bash
# Install ONE pinned OpenPGP key as an apt keyring (security audit SEC-042).
#
#   scripts/install-pinned-apt-key.sh <armored-key-file> <primary-fingerprint> <keyring-out>
#
# The armored file comes from the network (apt.llvm.org/llvm-snapshot.gpg.key),
# so it is untrusted. Checking only the FIRST fingerprint in it and then
# dearmoring the whole file let a file holding the real key plus any other key
# pass, and apt then trusted every key in it. This script refuses unless the
# file holds exactly one primary key with exactly the pinned fingerprint, and
# writes only that key (exported by fingerprint from a throwaway keyring).
set -euo pipefail

die() {
  echo "install-pinned-apt-key: $*" >&2
  exit 1
}

[ "$#" -eq 3 ] || die "usage: $0 <armored-key-file> <primary-fingerprint> <keyring-out>"
key_file="$1"
expected="$(printf '%s' "$2" | tr '[:lower:]' '[:upper:]')"
out="$3"

[[ "${expected}" =~ ^[0-9A-F]{40}$ ]] || die "pinned fingerprint must be 40 hex digits"
[ -s "${key_file}" ] || die "${key_file} is missing or empty"
command -v gpg >/dev/null 2>&1 || die "gpg is required"

home="$(mktemp -d)"
trap 'rm -rf "${home}"' EXIT
export GNUPGHOME="${home}"
chmod 700 "${home}"

listing="$(gpg --batch --with-colons --import-options show-only --import "${key_file}" 2>/dev/null)" \
  || die "${key_file} is not an OpenPGP key"
primaries="$(printf '%s\n' "${listing}" | awk -F: '$1 == "pub"' | wc -l | tr -d ' ')"
[ "${primaries}" = 1 ] || die "${key_file} holds ${primaries} primary keys; expected exactly the pinned one"
# The fpr record right after the pub record is the primary key's.
actual="$(printf '%s\n' "${listing}" | awk -F: '$1 == "pub" { want = 1; next } want && $1 == "fpr" { print $10; exit }')"
[ "${actual}" = "${expected}" ] || die "primary key fingerprint ${actual:-<none>} is not ${expected}"

gpg --batch --quiet --import "${key_file}" 2>/dev/null || die "could not import ${key_file}"
tmp_out="$(mktemp)"
gpg --batch --export "${expected}" > "${tmp_out}"
exported="$(gpg --batch --with-colons --import-options show-only --import "${tmp_out}" 2>/dev/null | awk -F: '$1 == "pub"' | wc -l | tr -d ' ')"
if [ "${exported}" != 1 ]; then
  rm -f "${tmp_out}"
  die "export of ${expected} did not yield exactly one key"
fi
install -m 0644 "${tmp_out}" "${out}"
rm -f "${tmp_out}"
echo "installed ${expected} as ${out}"
