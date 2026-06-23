#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.wasmedge}"

"$ROOT/scripts/install-wasmedge.sh" >/dev/null

if [[ -f "$WASMEDGE_DIR/env" ]]; then
  set +u
  # shellcheck disable=SC1090
  source "$WASMEDGE_DIR/env"
  set -u
fi

export WASMEDGE_DIR
export PATH="$WASMEDGE_DIR/bin:$PATH"
export GOCACHE="${GOCACHE:-$ROOT/.gocache}"

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
export PATH="$WASMEDGE_DIR/bin:$PATH"

cd "$ROOT/sdn-server"
exec go "$@"
