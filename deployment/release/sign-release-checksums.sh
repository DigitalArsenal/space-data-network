#!/usr/bin/env bash
# Offline release signing for the Space Data Network installers (security audit
# SEC-045). Run by the OWNER on a trusted machine that holds the release
# private key. CI never runs this and never holds the key.
#
#   sign-release-checksums.sh keygen <out-dir>
#       Create a new ECDSA P-256 release key pair in <out-dir> (mode 0700):
#       release-key.pem (PRIVATE: keep offline) and release-key.pub.pem
#       (PUBLIC: paste between the BEGIN/END SDN RELEASE SIGNING KEY markers in
#       scripts/install.sh and docs/install.sh).
#
#   sign-release-checksums.sh sign <private-key.pem> <checksums.txt>
#       Write <checksums.txt>.sig (DER ECDSA over SHA-256 of the file) and
#       verify it against the PUBLIC key embedded in scripts/install.sh.
#
#   sign-release-checksums.sh verify <checksums.txt> [<signature>]
#       Verify a signature against the PUBLIC key embedded in
#       scripts/install.sh.
#
#   sign-release-checksums.sh installer-pin <checksums.txt>
#       Print the SHA-256 the signed list gives install.sh, for
#       SDN_INSTALLER_SHA256 in docs/install.sh.
#
# See deployment/release/RELEASE-SIGNING.md for the whole release procedure.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"

die() {
  echo "sign-release-checksums: $*" >&2
  exit 1
}

command -v openssl >/dev/null 2>&1 || die "openssl is required"

embedded_public_key() {
  local file="${root}/scripts/install.sh"
  awk '/^# BEGIN SDN RELEASE SIGNING KEY$/ { inside = 1; next }
       /^# END SDN RELEASE SIGNING KEY$/ { inside = 0 }
       inside' "$file" \
    | sed -e "s/^SDN_RELEASE_SIGNING_KEY_PEM='//" -e "s/'$//"
}

verify() {
  local file="$1"
  local signature="${2:-$1.sig}"
  local key
  key="$(mktemp)"
  trap 'rm -f "$key"' RETURN
  embedded_public_key > "$key"
  grep -q 'BEGIN PUBLIC KEY' "$key" || die "scripts/install.sh carries no release public key yet"
  openssl dgst -sha256 -verify "$key" -signature "$signature" "$file"
}

case "${1:-}" in
  keygen)
    out="${2:?usage: keygen <out-dir>}"
    [ ! -e "${out}/release-key.pem" ] || die "${out}/release-key.pem already exists; refusing to overwrite a key"
    mkdir -p "$out"
    chmod 700 "$out"
    (umask 077 && openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "${out}/release-key.pem")
    openssl pkey -in "${out}/release-key.pem" -pubout -out "${out}/release-key.pub.pem"
    echo "Private key: ${out}/release-key.pem (keep OFFLINE; never commit, never give to CI)"
    echo "Public key to embed in scripts/install.sh and docs/install.sh:"
    cat "${out}/release-key.pub.pem"
    ;;
  sign)
    private_key="${2:?usage: sign <private-key.pem> <checksums.txt>}"
    file="${3:?usage: sign <private-key.pem> <checksums.txt>}"
    echo "Signing this checksum list:"
    cat "$file"
    openssl dgst -sha256 -sign "$private_key" -out "${file}.sig" "$file"
    verify "$file" "${file}.sig"
    echo "Wrote ${file}.sig"
    ;;
  verify)
    verify "${2:?usage: verify <checksums.txt> [<signature>]}" "${3:-}"
    ;;
  installer-pin)
    file="${2:?usage: installer-pin <checksums.txt>}"
    verify "$file" "${file}.sig" >/dev/null
    awk '$2 == "install.sh" || $2 == "*install.sh" { print $1; found = 1; exit } END { if (!found) exit 1 }' "$file" \
      || die "${file} does not list install.sh"
    ;;
  *)
    sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
    ;;
esac
