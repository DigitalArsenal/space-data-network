#!/usr/bin/env bash
#
# build-sdn-kubo.sh — canonical SDN-kubo build command.
#
# This is the post-rebase successor to scripts/go-with-wasmedge.sh. It resolves
# the WasmEdge SDK layout, exports the CGO flags the SDN plugins need
# (sdnruntime links libwasmedge through github.com/second-state/WasmEdge-go),
# and runs `go build ./cmd/ipfs` from the kubo module. The result is a normal
# kubo binary that also preloads the SDN plugins (sdnflag, sdnruntime, sdnapi).
#
# The kubo module is otherwise pristine upstream github.com/ipfs/kubo — see
# sdn-overlay.sh for the proof that only a handful of upstream files are touched.
#
# Usage:
#   build-sdn-kubo.sh [--out FILE] [--module-root DIR] [-- <extra go build args>]
#
# Environment (either name works; WASMEDGE_ROOT wins if both are set):
#   WASMEDGE_ROOT / WASMEDGE_DIR  Path to the WasmEdge SDK (must contain
#                                 include/wasmedge/wasmedge.h and lib/).
#   SDN_KUBO_MODULE_ROOT          Override the kubo module root (default: the
#                                 module that contains this script).
#   SDN_KUBO_OUT                  Override the output binary path.
#   GOCACHE                       Honoured if already set.
#
# Platform notes (prod is Linux; this dev box is macOS):
#   macOS   lib/libwasmedge.dylib, link with -L<sdk>/lib -lwasmedge and, when the
#           dylib install name is @rpath/..., -Wl,-rpath,<sdk>/lib. Runtime lookup
#           uses DYLD_FALLBACK_LIBRARY_PATH.
#   Linux   lib/libwasmedge.so, link with -L<sdk>/lib -lwasmedge
#           -Wl,-rpath,<sdk>/lib. Runtime lookup uses LD_LIBRARY_PATH. For a
#           relocatable prod binary prefer -Wl,-rpath,'$ORIGIN/../lib' and ship
#           libwasmedge.so beside it.
#   Windows bin/ holds the import lib + dll; link -L<sdk>/bin -L<sdk>/lib
#           -lwasmedge and put bin/ on PATH.
#
set -euo pipefail

log() { printf '[build-sdn-kubo] %s\n' "$*" >&2; }
die() { printf '[build-sdn-kubo] ERROR: %s\n' "$*" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# kubo/sdn/build/build-sdn-kubo.sh -> kubo module root is two levels up.
DEFAULT_MODULE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

OUT=""
MODULE_ROOT="${SDN_KUBO_MODULE_ROOT:-$DEFAULT_MODULE_ROOT}"
EXTRA_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out)          OUT="${2:?--out needs a path}"; shift 2 ;;
    --out=*)        OUT="${1#*=}"; shift ;;
    --module-root)  MODULE_ROOT="${2:?--module-root needs a path}"; shift 2 ;;
    --module-root=*) MODULE_ROOT="${1#*=}"; shift ;;
    --)             shift; EXTRA_ARGS+=("$@"); break ;;
    -h|--help)      grep '^#' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)              die "unknown argument: $1 (use --out/--module-root, or -- for go args)" ;;
  esac
done

# --- Resolve the WasmEdge SDK -------------------------------------------------
WROOT="${WASMEDGE_ROOT:-${WASMEDGE_DIR:-}}"
[[ -n "$WROOT" ]] || die "WASMEDGE_ROOT (or WASMEDGE_DIR) must point at the WasmEdge SDK (include/ + lib/)."
WROOT="$(cd "$WROOT" 2>/dev/null && pwd)" || die "WasmEdge SDK path does not exist: ${WASMEDGE_ROOT:-$WASMEDGE_DIR}"
[[ -f "$WROOT/include/wasmedge/wasmedge.h" ]] || die "missing $WROOT/include/wasmedge/wasmedge.h"
[[ -d "$WROOT/lib" ]] || die "missing $WROOT/lib under WasmEdge SDK: $WROOT"

# --- Resolve module root + output --------------------------------------------
[[ -f "$MODULE_ROOT/go.mod" ]] || die "no go.mod under module root: $MODULE_ROOT"
grep -q '^module github.com/ipfs/kubo$' "$MODULE_ROOT/go.mod" \
  || die "module root is not github.com/ipfs/kubo: $MODULE_ROOT"
[[ -d "$MODULE_ROOT/cmd/ipfs" ]] || die "no cmd/ipfs under module root: $MODULE_ROOT"

if [[ -z "$OUT" ]]; then
  OUT="${SDN_KUBO_OUT:-$MODULE_ROOT/cmd/ipfs/ipfs}"
fi
mkdir -p "$(dirname "$OUT")"

# --- CGO / linker flags per platform -----------------------------------------
UNAME_S="$(uname -s)"
CGO_CFLAGS_VALUE="-I${WROOT}/include"
case "$UNAME_S" in
  Darwin)
    CGO_LDFLAGS_VALUE="-L${WROOT}/lib -lwasmedge"
    # Add an rpath only when the dylib's install name is @rpath-relative.
    if [[ -f "$WROOT/lib/libwasmedge.dylib" ]] \
       && otool -D "$WROOT/lib/libwasmedge.dylib" 2>/dev/null | awk 'NR==2{print $1}' | grep -q '^@rpath/'; then
      CGO_LDFLAGS_VALUE="$CGO_LDFLAGS_VALUE -Wl,-rpath,${WROOT}/lib"
    fi
    export DYLD_FALLBACK_LIBRARY_PATH="${WROOT}/lib${DYLD_FALLBACK_LIBRARY_PATH:+:$DYLD_FALLBACK_LIBRARY_PATH}"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    CGO_LDFLAGS_VALUE="-L${WROOT}/bin -L${WROOT}/lib -lwasmedge"
    export PATH="${WROOT}/bin:$PATH"
    ;;
  *)  # Linux and other ELF platforms (production target).
    CGO_LDFLAGS_VALUE="-L${WROOT}/lib -lwasmedge -Wl,-rpath,${WROOT}/lib"
    export LD_LIBRARY_PATH="${WROOT}/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
    ;;
esac

export CGO_ENABLED=1
export CGO_CFLAGS="$CGO_CFLAGS_VALUE"
export CGO_LDFLAGS="$CGO_LDFLAGS_VALUE"
# GOCACHE is honoured if set; otherwise Go's default cache is used (we do not
# create a repo-local cache dir, to keep the tree clean).
if [[ -z "${CC:-}" && "$UNAME_S" == "Darwin" && -x /usr/bin/clang ]]; then
  export CC=/usr/bin/clang
fi

log "WasmEdge SDK : $WROOT"
log "module root  : $MODULE_ROOT"
log "output       : $OUT"
log "platform     : $UNAME_S"

cd "$MODULE_ROOT"
set -x
# Bash 3.2 (macOS) errors on "${arr[@]}" when the array is empty under set -u.
go build -o "$OUT" ${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"} ./cmd/ipfs
{ set +x; } 2>/dev/null

log "built: $OUT"
