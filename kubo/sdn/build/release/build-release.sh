#!/usr/bin/env bash
#
# Reproducible, multi-platform SDN-kubo release builder.
#
# Produces self-contained, download-and-run artifacts (the ipfs binary +
# its bundled WasmEdge shared library, rpath'd next to the binary) and
# packages them for hosting. No Go, no headers, no WasmEdge install is
# needed on the target host — extract and run.
#
#   ./build-release.sh linux/amd64
#   ./build-release.sh darwin/arm64
#   ./build-release.sh all
#
# Output: <kubo>/dist/<os>-<arch>/{ipfs[,.exe], lib/} plus a packaged
# archive and appended SHA256SUMS in <kubo>/dist/.
#
# Pinned, reproducible inputs (bump deliberately):
set -euo pipefail

WASMEDGE_VER="${WASMEDGE_VER:-0.14.1}"   # AOT-capable WasmEdge; matches WasmEdge-go v0.14.0
GO_VER="${GO_VER:-1.26.1}"               # >= kubo go.mod (go 1.25)

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KUBO_ROOT="$(cd "$HERE/../../.." && pwd)"          # sdn/build/release -> kubo module root
DIST="${DIST:-$KUBO_ROOT/dist}"
VERSION="${VERSION:-$(git -C "$KUBO_ROOT" rev-parse --short HEAD 2>/dev/null || echo dev)}"

log(){ printf '\033[0;34m[build]\033[0m %s\n' "$*"; }
die(){ printf '\033[0;31m[build] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

package(){ # <os-arch dir name> <tar|zip>
  local name="$1" fmt="$2"
  local d="$DIST/$name"
  [ -x "$d/ipfs" ] || [ -f "$d/ipfs.exe" ] || die "no binary in $d"
  local base="sdn-kubo-${VERSION}-${name}"
  if [ "$fmt" = zip ]; then ( cd "$DIST" && rm -f "$base.zip" && zip -qr "$base.zip" "$name" )
  else ( cd "$DIST" && rm -f "$base.tar.gz" && tar -czf "$base.tar.gz" "$name" ); fi
  ( cd "$DIST" && shasum -a 256 "$base".* | tee -a SHA256SUMS >/dev/null )
  log "packaged $base"
}

build_linux(){ # <arch: amd64|arm64>
  local arch="$1"
  local wasmedge_build_arch
  case "$arch" in
    amd64) wasmedge_build_arch=x86_64 ;;
    arm64) wasmedge_build_arch=aarch64 ;;
    *) die "unsupported Linux architecture: $arch" ;;
  esac
  command -v docker >/dev/null || die "docker required for linux builds"
  log "linux/$arch via docker buildx (WasmEdge $WASMEDGE_VER, Go $GO_VER)"
  rm -rf "$DIST/linux-$arch"
  docker buildx build --platform "linux/$arch" \
    -f "$KUBO_ROOT/sdn/build/release/Dockerfile.linux" \
    --build-arg WASMEDGE_VER="$WASMEDGE_VER" \
    --build-arg WASMEDGE_BUILD_ARCH="$wasmedge_build_arch" \
    --build-arg GO_VER="$GO_VER" \
    --output "type=local,dest=$DIST/linux-$arch" "$KUBO_ROOT"
  package "linux-$arch" tar
}

build_darwin(){ # <arch: arm64|amd64>
  local arch="$1"
  [ "$(uname -s)" = Darwin ] || die "darwin builds must run on macOS"
  local sdk="${WASMEDGE_DIR:-${WASMEDGE_ROOT:-}}"
  [ -n "$sdk" ] && [ -d "$sdk/lib" ] || die "set WASMEDGE_DIR to a macOS WasmEdge SDK"
  log "darwin/$arch native (WasmEdge SDK $sdk)"
  local out="$DIST/darwin-$arch"; rm -rf "$out"; mkdir -p "$out/lib"
  ( cd "$KUBO_ROOT" && \
    CGO_ENABLED=1 GOTOOLCHAIN=auto GOOS=darwin GOARCH="$arch" \
    CGO_CFLAGS="-I$sdk/include" \
    CGO_LDFLAGS="-L$sdk/lib -lwasmedge -Wl,-rpath,@loader_path/lib" \
    go build -trimpath -o "$out/ipfs" ./cmd/ipfs )
  cp -a "$sdk"/lib/libwasmedge*.dylib "$out/lib/" 2>/dev/null || die "no libwasmedge*.dylib in $sdk/lib"
  package "darwin-$arch" tar
}

build_windows(){ # <arch: amd64>
  # Pending: a Windows license + toolchain. Documented so the rebuild is
  # unambiguous once the environment exists.
  cat >&2 <<'EOF'
[build] windows/amd64 is NOT YET WIRED.
  Plan (once the Windows build host / license is available):
    - Build on Windows (or mingw-w64 cross) with CGO_ENABLED=1 against the
      WasmEdge Windows SDK (wasmedge.dll + headers).
    - Bundle wasmedge.dll next to ipfs.exe (Windows resolves DLLs from the
      exe directory), then: package <name> zip.
  See sdn/build/release/README.md.
EOF
  return 3
}

main(){
  mkdir -p "$DIST"
  local t="${1:-}"; [ -n "$t" ] || die "usage: $0 <linux/amd64|linux/arm64|darwin/arm64|darwin/amd64|windows/amd64|all>"
  case "$t" in
    linux/amd64) build_linux amd64 ;;
    linux/arm64) build_linux arm64 ;;
    darwin/arm64) build_darwin arm64 ;;
    darwin/amd64) build_darwin amd64 ;;
    windows/amd64) build_windows amd64 ;;
    all)
      build_linux amd64
      build_linux arm64 || log "linux/arm64 skipped"
      [ "$(uname -s)" = Darwin ] && build_darwin arm64 || log "darwin/arm64 skipped (not on macOS)"
      build_windows amd64 || log "windows/amd64 skipped (not wired)"
      ;;
    *) die "unknown target: $t" ;;
  esac
  log "artifacts in $DIST (VERSION=$VERSION)"
}
main "$@"
