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
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    CGO_LDFLAGS_VALUE="${CGO_LDFLAGS_VALUE}${CGO_LDFLAGS_VALUE:+ }-L${WASMEDGE_DIR}/bin -L${WASMEDGE_DIR}/lib -lwasmedge"
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
exec go "$@"
