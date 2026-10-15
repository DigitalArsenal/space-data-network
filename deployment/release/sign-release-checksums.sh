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
#   sign-release-checksums.sh sign <private-key.pem> <tag> <release-dir>
#       <release-dir> holds the DRAFT release's spacedatanetwork-checksums.txt
#       and install.sh (gh release download <tag> -p
#       spacedatanetwork-checksums.txt -p install.sh). Before signing it
#       refuses unless: <tag> is v<semver> and a tag in this checkout whose
#       commit is on origin/main; the draft's install.sh is byte-identical to
#       `git show <tag>:scripts/install.sh`; that installer embeds the public
#       half of <private-key.pem>; the list gives install.sh its real SHA-256;
#       every installer archive the list names carries <tag>'s version; and
#       every other listed file present in <release-dir> matches. The owner's
#       signature must never approve an installer CI produced unseen. Writes
#       spacedatanetwork-checksums.txt.sig (DER ECDSA over SHA-256) and
#       verifies it.
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
  local file="${1:-${root}/scripts/install.sh}"
  awk '/^# BEGIN SDN RELEASE SIGNING KEY$/ { inside = 1; next }
       /^# END SDN RELEASE SIGNING KEY$/ { inside = 0 }
       inside' "$file" \
    | sed -e "s/^SDN_RELEASE_SIGNING_KEY_PEM='//" -e "s/'$//"
}

verify() {
  local file="$1"
  local signature="${2:-$1.sig}"
  local installer="${3:-${root}/scripts/install.sh}"
  local key
  key="$(mktemp)"
  trap 'rm -f "$key"' RETURN
  embedded_public_key "$installer" > "$key"
  grep -q 'BEGIN PUBLIC KEY' "$key" || die "${installer} carries no release public key yet"
  openssl dgst -sha256 -verify "$key" -signature "$signature" "$file"
}

sha256_of() {
  openssl dgst -sha256 -r "$1" | awk '{ print $1 }'
}

# SHA-256 of a public key's DER encoding, from a PEM public or private key.
public_key_id() {
  local kind="$1" file="$2"
  if [ "$kind" = private ]; then
    openssl pkey -in "$file" -pubout -outform DER
  else
    openssl pkey -pubin -in "$file" -outform DER
  fi | openssl dgst -sha256 -r | awk '{ print $1 }'
}

# Every refusal sign makes before it touches the private key's signature.
check_release_for_signing() {
  local private_key="$1" tag="$2" dir="$3"
  local list="${dir}/spacedatanetwork-checksums.txt"
  local installer="${dir}/install.sh"
  local version="${tag#v}"

  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "tag '${tag}' is not v<semver>"
  [ -f "$list" ] || die "${list} is missing (gh release download ${tag} -p spacedatanetwork-checksums.txt)"
  [ -f "$installer" ] || die "${installer} is missing (gh release download ${tag} -p install.sh)"
  [ -f "$private_key" ] || die "${private_key} is missing"

  # The tag is a reviewed commit: it exists here and is on origin/main.
  command -v git >/dev/null 2>&1 || die "git is required"
  local commit
  commit="$(git -C "$root" rev-parse --verify --quiet "refs/tags/${tag}^{commit}")" \
    || die "tag ${tag} is not in this checkout (git fetch --tags origin)"
  git -C "$root" rev-parse --verify --quiet refs/remotes/origin/main >/dev/null \
    || die "origin/main is not in this checkout (git fetch origin main)"
  git -C "$root" merge-base --is-ancestor "$commit" refs/remotes/origin/main \
    || die "tag ${tag} (${commit}) is not on origin/main"

  # The draft's installer is the reviewed one, byte for byte.
  local reviewed
  reviewed="$(mktemp)"
  if ! git -C "$root" show "${commit}:scripts/install.sh" > "$reviewed"; then
    rm -f "$reviewed"
    die "${tag} has no scripts/install.sh"
  fi
  if ! cmp -s "$reviewed" "$installer"; then
    rm -f "$reviewed"
    die "the draft's install.sh is not scripts/install.sh at ${tag}; refusing to sign"
  fi
  rm -f "$reviewed"

  # That installer trusts THIS key, not one CI put in.
  local embedded
  embedded="$(mktemp)"
  embedded_public_key "$installer" > "$embedded"
  if ! grep -q 'BEGIN PUBLIC KEY' "$embedded"; then
    rm -f "$embedded"
    die "install.sh at ${tag} carries no release public key"
  fi
  local want have
  want="$(public_key_id private "$private_key")" || { rm -f "$embedded"; die "cannot read ${private_key}"; }
  have="$(public_key_id public "$embedded")" || { rm -f "$embedded"; die "install.sh's embedded key does not parse"; }
  rm -f "$embedded"
  [ -n "$want" ] && [ "$want" = "$have" ] || die "install.sh at ${tag} embeds a different public key than ${private_key}"

  # The list: well formed, install.sh at its real digest, archives of this
  # version only, and every listed file we hold matches.
  local line digest name installer_entries=0 own_archives=0
  while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] || continue
    [[ "$line" =~ ^([0-9a-f]{64})\ [\ \*](.+)$ ]] || die "malformed checksum line: ${line}"
    digest="${BASH_REMATCH[1]}"
    name="${BASH_REMATCH[2]}"
    case "$name" in
      */*|.*) die "checksum list names a path, not a release asset: ${name}" ;;
    esac
    if [ "$name" = install.sh ]; then
      installer_entries=$((installer_entries + 1))
      [ "$digest" = "$(sha256_of "$installer")" ] || die "the list's install.sh digest is not the draft's install.sh"
    fi
    if [[ "$name" =~ ^spacedatanetwork-([0-9][^/]*)-(linux|darwin|windows)-(amd64|arm64)\.(tar\.gz|zip)$ ]]; then
      [ "${BASH_REMATCH[1]}" = "$version" ] || die "the list names an archive of another version: ${name}"
      own_archives=$((own_archives + 1))
    fi
    if [ -f "${dir}/${name}" ] && [ "$name" != install.sh ]; then
      [ "$digest" = "$(sha256_of "${dir}/${name}")" ] || die "${name} does not match its listed digest"
    fi
  done < "$list"
  [ "$installer_entries" = 1 ] || die "the list must name install.sh exactly once (found ${installer_entries})"
  [ "$own_archives" -gt 0 ] || die "the list names no spacedatanetwork-${version}-<os>-<arch> archive"
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
    [ "$#" -eq 4 ] || die "usage: sign <private-key.pem> <tag> <release-dir>"
    private_key="$2"
    tag="$3"
    dir="$4"
    check_release_for_signing "$private_key" "$tag" "$dir"
    file="${dir}/spacedatanetwork-checksums.txt"
    echo "install.sh is scripts/install.sh at ${tag} and embeds this key."
    echo "Signing this checksum list:"
    cat "$file"
    openssl dgst -sha256 -sign "$private_key" -out "${file}.sig" "$file"
    verify "$file" "${file}.sig" "${dir}/install.sh"
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
    sed -n '2,34p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
    ;;
esac
