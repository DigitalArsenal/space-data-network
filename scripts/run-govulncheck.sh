#!/usr/bin/env bash
# run-govulncheck.sh - scan sdn-server with govulncheck under the WasmEdge cgo env.
#
# sdn-server is cgo code against WasmEdge, so govulncheck cannot build it without
# the same CGO_CFLAGS/CGO_LDFLAGS that scripts/go-with-wasmedge.sh exports. That
# script execs `go` directly and so cannot host a different binary; this is the
# same environment, applied to govulncheck.
#
# Writes the report to stdout and to the path given by --out (default:
# sdn-server/govulncheck-report.txt), so scripts/check-govulncheck.mjs can score
# it. govulncheck exits non-zero whenever it finds anything, which is why the
# gate reads the report rather than the exit code.
#
# Usage:
#   WASMEDGE_DIR=~/.wasmedge scripts/run-govulncheck.sh [--out report.txt]

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/sdn-server/govulncheck-report.txt"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out)
      OUT="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,15p' "$0"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${WASMEDGE_DIR:-}" ]]; then
  echo "[govulncheck] WASMEDGE_DIR must point to a WasmEdge header/library layout." >&2
  exit 1
fi

if [[ ! -f "$WASMEDGE_DIR/include/wasmedge/wasmedge.h" || ! -d "$WASMEDGE_DIR/lib" ]]; then
  echo "[govulncheck] invalid WASMEDGE_DIR: $WASMEDGE_DIR" >&2
  exit 1
fi

if ! command -v govulncheck >/dev/null 2>&1; then
  echo "[govulncheck] govulncheck not on PATH; install with:" >&2
  echo "  go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
  exit 1
fi

export GOCACHE="${GOCACHE:-$ROOT/.gocache}"
export CGO_CFLAGS="${CGO_CFLAGS:-} -I${WASMEDGE_DIR}/include"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -L${WASMEDGE_DIR}/lib -Wl,-rpath,${WASMEDGE_DIR}/lib"
export PATH="${WASMEDGE_DIR}/bin:$PATH"

cd "$ROOT/sdn-server"

# govulncheck's exit status encodes "found something", not "failed to run".
# scripts/check-govulncheck.mjs decides what is and is not acceptable.
govulncheck ./... 2>&1 | tee "$OUT"
status=${PIPESTATUS[0]}

if [[ ! -s "$OUT" ]]; then
  echo "[govulncheck] produced no report (exit $status)" >&2
  exit 1
fi

echo "[govulncheck] report written to $OUT (govulncheck exit $status)"
