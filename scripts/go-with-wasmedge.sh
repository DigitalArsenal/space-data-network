#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WASMEDGE_DIR="${WASMEDGE_DIR:-}"

if [[ -z "$WASMEDGE_DIR" ]]; then
  cat >&2 <<'EOF'
[wasmedge] WASMEDGE_DIR must point to an existing WasmEdge header/library layout.
[wasmedge] Automatic local WasmEdge runtime installation is disabled; use an
[wasmedge] explicit system/toolchain path instead of ~/.wasmedge.
EOF
  exit 1
fi

# MSYS2/MinGW receives a native Windows path (D:\a\repo\repo, straight from
# ${{ github.workspace }}). A backslash is an escape character to almost
# everything it then passes through, and the damage is silent: sed read
# "D:\a\space-data-network\..." as a replacement string, ate \a and turned \s
# into s, and ld.exe was handed "D:space-data-networkspace-data-network/..." —
# "cannot find libwasmedge.a: Invalid argument", once per archive. cygpath -m
# gives the mixed form (D:/a/repo/repo) that ld.exe, cmake and the shell all
# accept unchanged.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    if command -v cygpath >/dev/null 2>&1; then
      WASMEDGE_DIR="$(cygpath -m "$WASMEDGE_DIR")"
    else
      WASMEDGE_DIR="${WASMEDGE_DIR//\\//}"
    fi
    ;;
esac

if [[ ! -f "$WASMEDGE_DIR/include/wasmedge/wasmedge.h" || ! -d "$WASMEDGE_DIR/lib" ]]; then
  printf '[wasmedge] invalid WASMEDGE_DIR: %s\n' "$WASMEDGE_DIR" >&2
  printf '[wasmedge] expected include/wasmedge/wasmedge.h and lib/ under that path\n' >&2
  exit 1
fi

if [[ -f "$WASMEDGE_DIR/env" ]]; then
  set +u
  # shellcheck disable=SC1090
  source "$WASMEDGE_DIR/env"
  set -u
fi

export WASMEDGE_DIR
if [[ -d "$WASMEDGE_DIR/bin" ]]; then
  export PATH="$WASMEDGE_DIR/bin:$PATH"
fi
export GOCACHE="${GOCACHE:-$ROOT/.gocache}"

strip_path_entry() {
  local var_name="$1"
  local remove_entry="$2"
  local current="${!var_name:-}"
  local next=""
  local entry

  if [[ -z "$current" ]]; then
    return 0
  fi

  IFS=':' read -r -a entries <<<"$current"
  for entry in "${entries[@]}"; do
    if [[ "$entry" == "$remove_entry" ]]; then
      continue
    fi
    next="${next}${next:+:}${entry}"
  done

  if [[ -n "$next" ]]; then
    export "$var_name=$next"
  else
    unset "$var_name"
  fi
}

darwin_wasmedge_requires_rpath() {
  local dylib="$WASMEDGE_DIR/lib/libwasmedge.dylib"
  local install_name=""

  if [[ "$(uname -s)" != "Darwin" || ! -f "$dylib" ]]; then
    return 1
  fi

  install_name="$(otool -D "$dylib" 2>/dev/null | awk 'NR == 2 { print $1 }')"
  [[ "$install_name" == @rpath/* ]]
}

# The local build must be driven by the explicit header/library layout above.
# Stale default runtime installs in the parent shell can otherwise leak into
# CGO through linker search paths and shadow the selected WasmEdge version.
unset WASMEDGE_LIB_DIR WASMEDGE_INCLUDE_DIR
strip_path_entry LIBRARY_PATH "$HOME/.wasmedge/lib"
strip_path_entry DYLD_LIBRARY_PATH "$HOME/.wasmedge/lib"
strip_path_entry DYLD_FALLBACK_LIBRARY_PATH "$HOME/.wasmedge/lib"

mkdir -p "$GOCACHE"

if [[ -z "${CC:-}" ]] && [[ "$(uname -s)" == "Darwin" ]] && [[ -x /usr/bin/clang ]]; then
  export CC=/usr/bin/clang
fi

CGO_CFLAGS_VALUE="${CGO_CFLAGS:-}"
CGO_LDFLAGS_VALUE="${CGO_LDFLAGS:-}"

CGO_CFLAGS_VALUE="${CGO_CFLAGS_VALUE}${CGO_CFLAGS_VALUE:+ }-I${WASMEDGE_DIR}/include"

# A STATIC prefix is self-describing: build-static-wasmedge.sh leaves link.flags
# beside the archives holding the exact link line it built them for (archive
# order, LLVM's own libraries, the per-platform extras). When that file is
# present we link the runtime INTO the binary and skip every dynamic
# concern below — no -lwasmedge, no rpath, nothing for a host to provide.
if [[ -f "$WASMEDGE_DIR/link.flags" ]]; then
  # BUILD THE FLAGS FROM SCRATCH. An inherited CGO_LDFLAGS pointing at another
  # WasmEdge is the one thing that can silently undo all of this: the linker
  # finds that -L first, resolves -lwasmedge to a DYLIB there, and the binary
  # ships depending on a path only this machine has. Measured exactly that way
  # on a shell exporting the dynamic SDK. The dynamic branch below deliberately
  # inherits them; static must not.
  export CGO_CFLAGS="-I${WASMEDGE_DIR}/include"
  export CGO_LDFLAGS="-L${WASMEDGE_DIR}/lib"
  # @PREFIX@ is substituted here, which is what makes a prefix built elsewhere
  # (a cached CI artifact, another checkout) usable at whatever path it landed.
  # Literal replacement, not sed: sed's replacement text interprets backslashes
  # and &, and a filesystem path is arbitrary text that must survive verbatim.
  STATIC_LINK_TEMPLATE="$(tr -d '\n' < "$WASMEDGE_DIR/link.flags")"
  STATIC_LINK_FLAGS="${STATIC_LINK_TEMPLATE//@PREFIX@/$WASMEDGE_DIR}"
  cd "$ROOT/sdn-server"
  case "${1:-}" in
    build|install|test)
      verb="$1"; shift
      # SDN_GO_LDFLAGS is the ONLY way a caller adds its own -ldflags here. go
      # keeps the LAST -ldflags it is given, so a caller that passed its own as
      # an argument would silently drop the static link line below and get a
      # binary linked against whatever libwasmedge the machine happens to have
      # — or no link at all. Merging them into one value is what the release
      # stamp (-X ...ReleaseTag) needs.
      exec go "$verb" \
        -ldflags "${SDN_GO_LDFLAGS:+${SDN_GO_LDFLAGS} }-linkmode external -extldflags \"${STATIC_LINK_FLAGS}\"" \
        "$@"
      ;;
    *)
      exec go "$@"
      ;;
  esac
fi

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    CGO_LDFLAGS_VALUE="${CGO_LDFLAGS_VALUE}${CGO_LDFLAGS_VALUE:+ }-L${WASMEDGE_DIR}/bin -L${WASMEDGE_DIR}/lib -lwasmedge"
    ;;
  Darwin)
    CGO_LDFLAGS_VALUE="${CGO_LDFLAGS_VALUE}${CGO_LDFLAGS_VALUE:+ }-L${WASMEDGE_DIR}/lib"
    if darwin_wasmedge_requires_rpath; then
      CGO_LDFLAGS_VALUE="${CGO_LDFLAGS_VALUE} -Wl,-rpath,${WASMEDGE_DIR}/lib"
    fi
    ;;
  *)
    CGO_LDFLAGS_VALUE="${CGO_LDFLAGS_VALUE}${CGO_LDFLAGS_VALUE:+ }-L${WASMEDGE_DIR}/lib -Wl,-rpath,${WASMEDGE_DIR}/lib"
    ;;
esac

export CGO_CFLAGS="$CGO_CFLAGS_VALUE"
export CGO_LDFLAGS="$CGO_LDFLAGS_VALUE"
if [[ -d "$WASMEDGE_DIR/bin" ]]; then
  export PATH="$WASMEDGE_DIR/bin:$PATH"
fi

cd "$ROOT/sdn-server"

# Same contract as the static branch: a caller's own -ldflags travel in
# SDN_GO_LDFLAGS so the two paths behave identically.
case "${1:-}" in
  build|install|test)
    if [[ -n "${SDN_GO_LDFLAGS:-}" ]]; then
      verb="$1"; shift
      exec go "$verb" -ldflags "${SDN_GO_LDFLAGS}" "$@"
    fi
    ;;
esac

exec go "$@"
