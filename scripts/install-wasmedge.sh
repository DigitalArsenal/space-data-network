#!/usr/bin/env bash
set -euo pipefail

WASMEDGE_VERSION="${WASMEDGE_VERSION:-0.14.0}"
WASMEDGE_DEFAULT_DIR="${HOME}/.wasmedge"
WASMEDGE_DIR="${WASMEDGE_DIR:-$WASMEDGE_DEFAULT_DIR}"
WASMEDGE_INSTALL_SCRIPT_URL="${WASMEDGE_INSTALL_SCRIPT_URL:-https://raw.githubusercontent.com/WasmEdge/WasmEdge/master/utils/install.sh}"

log() {
  printf '[wasmedge] %s\n' "$1"
}

fail() {
  printf '[wasmedge] %s\n' "$1" >&2
  exit 1
}

has_installation() {
  [[ -x "$WASMEDGE_DIR/bin/wasmedge" ]] \
    && [[ -f "$WASMEDGE_DIR/include/wasmedge/wasmedge.h" ]] \
    && [[ -d "$WASMEDGE_DIR/lib" ]]
}

run_installer() {
  local installer_home="${1:-}"
  local installer_tmp
  local status
  installer_tmp="$(mktemp -d)"

  if [[ -n "$installer_home" ]]; then
    if (cd "$installer_tmp" && curl -sSf "$WASMEDGE_INSTALL_SCRIPT_URL" | env -u GIT_DIR -u GIT_WORK_TREE HOME="$installer_home" bash -s -- -v "$WASMEDGE_VERSION"); then
      status=0
    else
      status=$?
    fi
  else
    if (cd "$installer_tmp" && curl -sSf "$WASMEDGE_INSTALL_SCRIPT_URL" | env -u GIT_DIR -u GIT_WORK_TREE bash -s -- -v "$WASMEDGE_VERSION"); then
      status=0
    else
      status=$?
    fi
  fi

  rm -rf "$installer_tmp"
  return "$status"
}

installed_version() {
  "$WASMEDGE_DIR/bin/wasmedge" --version 2>/dev/null | awk '{print $NF}'
}

case "$(uname -s)" in
  Darwin|Linux) ;;
  *)
    fail "unsupported platform: $(uname -s)"
    ;;
esac

if has_installation; then
  CURRENT_VERSION="$(installed_version || true)"
  if [[ "$CURRENT_VERSION" == "$WASMEDGE_VERSION" ]]; then
    log "using WasmEdge ${CURRENT_VERSION} from ${WASMEDGE_DIR}"
    exit 0
  fi
  log "found WasmEdge ${CURRENT_VERSION:-unknown} at ${WASMEDGE_DIR}; reinstalling ${WASMEDGE_VERSION}"
fi

if ! command -v curl >/dev/null 2>&1; then
  fail "curl is required to install WasmEdge"
fi

log "installing WasmEdge ${WASMEDGE_VERSION} into ${WASMEDGE_DIR}"
if [[ "$WASMEDGE_DIR" == "$WASMEDGE_DEFAULT_DIR" ]]; then
  run_installer
else
  if [[ "$(basename "$WASMEDGE_DIR")" != ".wasmedge" ]]; then
    fail "custom automatic install paths must end in .wasmedge; current value is ${WASMEDGE_DIR}"
  fi
  mkdir -p "$(dirname "$WASMEDGE_DIR")"
  run_installer "$(dirname "$WASMEDGE_DIR")"
fi

if ! has_installation; then
  fail "WasmEdge installation completed without the expected headers and libraries"
fi

CURRENT_VERSION="$(installed_version || true)"
if [[ "$CURRENT_VERSION" != "$WASMEDGE_VERSION" ]]; then
  fail "expected WasmEdge ${WASMEDGE_VERSION}, found ${CURRENT_VERSION:-unknown}"
fi

log "installed WasmEdge ${CURRENT_VERSION} at ${WASMEDGE_DIR}"
