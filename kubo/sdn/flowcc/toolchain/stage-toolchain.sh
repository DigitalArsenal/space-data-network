#!/usr/bin/env bash
# stage-toolchain.sh — deterministically stage the flowcc bake toolchain into a
# node's flowcc.Home from the checksummed tarball built by package-toolchain.sh.
#
# This is the ops path a fresh node / host-01 runs ONCE to turn the bake path on
# (home.Staged() -> true, /api/v1/flows/bake -> 200). It is pure provisioning:
# verify sha -> extract into the on-disk Home layout -> assert the Home
# invariants. It NEVER runs the baker, fetches from the network, or drives a
# flow — no Go host logic, no orchestration. The programmatic equivalent is
# flowcc.StageToolchain (used by the bake tests); this script lands the identical
# on-disk layout without a Go build.
#
# Home resolution mirrors flowcc.ResolveHome() exactly:
#   --home <dir>  (explicit override), else
#   $SDN_FLOWCC_HOME, else
#   $IPFS_PATH/sdn/flowcc, else
#   ~/.ipfs/sdn/flowcc
#
# Usage:
#   stage-toolchain.sh [--home <dir>] [--tarball <path>] [--sums <path>]
#
#   --tarball  The flowcc-toolchain-<ver>.tar.gz. Default:
#              ~/.spacedatanetwork/flowcc-toolchain/flowcc-toolchain-v1.tar.gz
#   --sums     SHA256SUMS to verify against. Default: the committed copy next to
#              this script (sdn/flowcc/toolchain/SHA256SUMS), else the sibling of
#              the tarball.
set -euo pipefail

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
HOME_DIR=""
TARBALL="${HOME}/.spacedatanetwork/flowcc-toolchain/flowcc-toolchain-v1.tar.gz"
SUMS=""

while [ $# -gt 0 ]; do
  case "$1" in
    --home)    HOME_DIR="$2"; shift 2 ;;
    --tarball) TARBALL="$2"; shift 2 ;;
    --sums)    SUMS="$2"; shift 2 ;;
    -h|--help) sed -n '2,34p' "$0"; exit 0 ;;
    *) echo "stage-toolchain.sh: unknown arg: $1" >&2; exit 2 ;;
  esac
done

# Resolve Home exactly like flowcc.ResolveHome().
if [ -z "$HOME_DIR" ]; then
  if [ -n "${SDN_FLOWCC_HOME:-}" ]; then
    HOME_DIR="$SDN_FLOWCC_HOME"
  elif [ -n "${IPFS_PATH:-}" ]; then
    HOME_DIR="$IPFS_PATH/sdn/flowcc"
  else
    HOME_DIR="$HOME/.ipfs/sdn/flowcc"
  fi
fi

# Default SHA256SUMS: committed copy next to this script, else tarball sibling.
if [ -z "$SUMS" ]; then
  if [ -f "$SELF_DIR/SHA256SUMS" ]; then
    SUMS="$SELF_DIR/SHA256SUMS"
  else
    SUMS="$(dirname "$TARBALL")/SHA256SUMS"
  fi
fi

[ -f "$TARBALL" ] || { echo "stage-toolchain.sh: tarball not found: $TARBALL" >&2; exit 4; }
[ -f "$SUMS" ]    || { echo "stage-toolchain.sh: SHA256SUMS not found: $SUMS" >&2; exit 4; }

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
  sha256_stdin() { sha256sum | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a256 "$1" | awk '{print $1}'; }
  sha256_stdin() { shasum -a256 | awk '{print $1}'; }
else
  echo "stage-toolchain.sh: need sha256sum or shasum" >&2; exit 3
fi

sums_get() { awk -v k="$1" '$1==k {print $2; exit}' "$SUMS"; }

WANT_TARBALL_SHA="$(sums_get TARBALL_SHA256)"
WANT_BOX_SHA="$(sums_get BOX_SHA256)"
WANT_BOX_BYTES="$(sums_get BOX_BYTES)"
WANT_SYSROOT_ROLLUP="$(sums_get SYSROOT_ROLLUP_SHA256)"
WANT_SYSROOT_FILES="$(sums_get SYSROOT_FILES)"
WANT_INVOKE_SHA="$(sums_get INVOKE_HDR_SHA256)"

[ -n "$WANT_BOX_SHA" ] || { echo "stage-toolchain.sh: SHA256SUMS missing BOX_SHA256" >&2; exit 5; }

echo "==> verifying tarball against $SUMS"
GOT_TARBALL_SHA="$(sha256 "$TARBALL")"
if [ -n "$WANT_TARBALL_SHA" ] && [ "$GOT_TARBALL_SHA" != "$WANT_TARBALL_SHA" ]; then
  # A non-matching outer .tar.gz sha is a WARNING (tar/gzip metadata varies by
  # implementation); the extracted CONTENT hashes below are the hard gate.
  echo "    WARN: tarball sha $GOT_TARBALL_SHA != pinned $WANT_TARBALL_SHA (content hashes are authoritative)" >&2
else
  echo "    tarball sha OK ($GOT_TARBALL_SHA)"
fi

echo "==> staging into home: $HOME_DIR"
mkdir -p "$HOME_DIR"
# Extract the flowcc-toolchain/ subtree directly into the home root.
tar -xzf "$TARBALL" -C "$HOME_DIR" --strip-components=1 flowcc-toolchain

BOX="$HOME_DIR/llvm-box.wasm"
SYSROOT="$HOME_DIR/sysroot"
TPL_RUNTIME="$HOME_DIR/template/flow_runtime.cpp"
TPL_INVOKE="$HOME_DIR/template/space_data_module_invoke.h"

echo "==> asserting home.Staged() invariants + content integrity"
fail=0
# 1. box: regular file, sha + size match (Staged() check #1).
if [ ! -f "$BOX" ]; then echo "    FAIL: missing $BOX" >&2; fail=1; else
  GOT="$(sha256 "$BOX")"; SZ="$(wc -c < "$BOX" | tr -d ' ')"
  [ "$GOT" = "$WANT_BOX_SHA" ] || { echo "    FAIL: box sha $GOT != $WANT_BOX_SHA" >&2; fail=1; }
  [ -z "$WANT_BOX_BYTES" ] || [ "$SZ" = "$WANT_BOX_BYTES" ] || { echo "    FAIL: box bytes $SZ != $WANT_BOX_BYTES" >&2; fail=1; }
fi
# 2. sysroot: directory, rollup + file count match (Staged() check #2).
if [ ! -d "$SYSROOT" ]; then echo "    FAIL: missing sysroot dir $SYSROOT" >&2; fail=1; else
  GOT="$(cd "$SYSROOT" && find . -type f | LC_ALL=C sort | xargs shasum -a256 2>/dev/null | sha256_stdin)"
  NF="$(find "$SYSROOT" -type f | wc -l | tr -d ' ')"
  [ -z "$WANT_SYSROOT_ROLLUP" ] || [ "$GOT" = "$WANT_SYSROOT_ROLLUP" ] || { echo "    FAIL: sysroot rollup $GOT != $WANT_SYSROOT_ROLLUP" >&2; fail=1; }
  [ -z "$WANT_SYSROOT_FILES" ] || [ "$NF" = "$WANT_SYSROOT_FILES" ] || { echo "    FAIL: sysroot files $NF != $WANT_SYSROOT_FILES" >&2; fail=1; }
fi
# 3. template/flow_runtime.cpp: regular file (Staged() check #3).
[ -f "$TPL_RUNTIME" ] || { echo "    FAIL: missing $TPL_RUNTIME" >&2; fail=1; }
# 4. template/space_data_module_invoke.h: regular file — NOT part of Staged()
#    but the baker reads it (bake.go: os.ReadFile(home.InvokeHeaderPath())); a
#    partial extraction without it yields Staged()==true but every bake fails.
if [ ! -f "$TPL_INVOKE" ]; then echo "    FAIL: missing $TPL_INVOKE (baker reads it)" >&2; fail=1; else
  GOT="$(sha256 "$TPL_INVOKE")"
  [ -z "$WANT_INVOKE_SHA" ] || [ "$GOT" = "$WANT_INVOKE_SHA" ] || { echo "    FAIL: invoke header sha $GOT != $WANT_INVOKE_SHA" >&2; fail=1; }
fi

if [ "$fail" -ne 0 ]; then
  echo "==> STAGING FAILED — home is NOT valid" >&2
  exit 6
fi
echo "==> STAGED OK: $HOME_DIR"
echo "    home.Staged() invariants: box + sysroot + template/flow_runtime.cpp present"
echo "    baker invoke header present + verified"
echo "    the node's /api/v1/flows/bake will return 200 (not 501) once this home is the resolved flowcc home"
