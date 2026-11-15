#!/bin/sh
# The AOT artifact is native code for the CPU that compiles it, so it CANNOT be
# baked into a portable image. This used to run at build time; the image then
# carried whatever instruction set the builder happened to have, and a host
# without it died on boot with SIGILL inside WasmEdge_VMExecuteRegistered.
#
# Compiling here instead means it targets the CPU actually running the node.
# XDG_CACHE_HOME points into /app/data (the volume), so the cost is paid once
# per host rather than once per container start.
set -e

# THE KEY PASSWORD FILE OF A FRESH NODE.
#
# A node with no key password seals its identity under a key derived from the
# machine and the account it runs as. The container runs as uid 1000, and on
# most hosts the kernel makes /sys/class/dmi/id/product_uuid readable by root
# only, so that derivation is REFUSED and the first start used to fail until
# the operator mounted a password file. On a FRESH node the entrypoint now
# makes one in the data volume instead, and every later start uses it:
#
#   - nothing happens when SDN_KEY_PASSWORD or SDN_KEY_PASSWORD_FILE is set
#     (a mounted secret is still the better custody: it is not in the volume);
#   - an existing $KEY_PASSWORD_FILE is used, never overwritten;
#   - otherwise it is created only when the node says its fingerprint is
#     refused for this uid and it has no identity and no configured password
#     (`key fresh-password-file`). A volume that already holds an identity is
#     left exactly as it is.
#
# The file sits beside the identity in the volume; back it up apart from the
# node's data, because the identity cannot be opened without it.
KEY_PASSWORD_FILE=/app/data/keys/.key-password

prepare_key_password() {
  if [ -n "${SDN_KEY_PASSWORD:-}" ] || [ -n "${SDN_KEY_PASSWORD_FILE:-}" ]; then
    return 0
  fi
  if [ -f "$KEY_PASSWORD_FILE" ]; then
    export SDN_KEY_PASSWORD_FILE="$KEY_PASSWORD_FILE"
    echo "spacedatanetwork: using the key password file $KEY_PASSWORD_FILE (in the data volume)" >&2
    return 0
  fi
  case "$1" in
    daemon|init) ;;
    *) return 0 ;;
  esac
  verdict=$(/app/spacedatanetwork key fingerprint-check 2>/dev/null) || return 0
  [ "$verdict" = "refused" ] || return 0
  shift
  config=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --config|-c) config="${2:-}"; [ $# -gt 1 ] && shift ;;
      --config=*) config="${1#--config=}" ;;
    esac
    shift
  done
  if [ -n "$config" ]; then
    outcome=$(/app/spacedatanetwork -c "$config" key fresh-password-file "$KEY_PASSWORD_FILE") || return 0
  else
    outcome=$(/app/spacedatanetwork key fresh-password-file "$KEY_PASSWORD_FILE") || return 0
  fi
  case "$outcome" in
    "created "*|"exists "*)
      export SDN_KEY_PASSWORD_FILE="$KEY_PASSWORD_FILE"
      echo "spacedatanetwork: fresh node, machine fingerprint refused for uid $(id -u): its identity is sealed under the generated key password file $KEY_PASSWORD_FILE (in the data volume); keep it and back it up apart from the node's data" >&2
      ;;
  esac
  return 0
}

prepare_key_password "$@"

case "$1" in
  daemon)
    if ! /app/spacedatanetwork prewarm-aot; then
      echo "warning: AOT prewarm failed; the FlatSQL engine will run interpreted (much slower)" >&2
    fi
    ;;
esac

exec /app/spacedatanetwork "$@"
