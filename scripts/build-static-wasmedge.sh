#!/usr/bin/env bash
# Build WasmEdge (default 0.16.4, override WASMEDGE_VERSION) as a STATIC
# library WITH LLVM linked in, and static-link the spacedatanetwork daemon into
# a single self-contained binary — the way kubo ships one file.
#
# The result depends only on base system libraries (libstdc++, libm, libgcc_s,
# libc). No ~/.wasmedge install, no WASMEDGE_DIR, no LD_LIBRARY_PATH, and
# nothing per-host that can drift from the version the binary was built against.
# That drift is not hypothetical: the AOT cache key includes the libwasmedge
# runtime version, a miss does not fail, and production has twice fallen back to
# the roughly 100x slower interpreter because a host's install did not match.
#
# AOT IS RETAINED, and that is the whole point of linking LLVM statically.
# An earlier revision of this script built with WASMEDGE_USE_LLVM=OFF on the
# premise that "the server runs modules interpreted". That is wrong for the
# FlatSQL engine, which loads a precompiled artifact and is ~100x slower
# without one. In 0.16.4 WASMEDGE_BUILD_AOT_RUNTIME is DEPRECATED AND ALIASED
# to WASMEDGE_USE_LLVM (CMakeLists.txt:69-72), so there is no load-only mode:
# turning LLVM off removes the ability to load an AOT artifact, not just to
# compile one. Verified on the built binary: `prewarm-aot` compiles both
# artifacts in-process and reports we0.16.4, the same cache key the fleet uses.
#
# Cost: the binary is ~200 MB because LLVM is inside it. The upstream shared
# library is 175 MB for exactly the same reason.
#
# Built with clang, not gcc: WasmEdge adds -Werror unconditionally
# (cmake/Helper.cmake:43-48) and gcc-12 fails its own sources on
# -Wmaybe-uninitialized (ast/component/component_type.cpp) and -Warray-bounds
# (executor/coredump.cpp). Upstream builds with clang.
#
# Produces: $OUT (default: sdn-server/spacedatanetwork-static).
#
# Requires: git, cmake, ninja, clang, and the STATIC LLVM archives —
# on Debian/Ubuntu: llvm-16-dev liblld-16-dev libpolly-16-dev libzstd-dev
# zlib1g-dev clang-16. libPolly.a ships separately and the final archive merge
# fails without it.
set -euo pipefail

WASMEDGE_VERSION="${WASMEDGE_VERSION:-0.16.4}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${WASMEDGE_STATIC_WORK:-${ROOT}/.wasmedge-static-build}"
SRC="${WORK}/WasmEdge"
STATIC="${WORK}/prefix"          # holds lib/*.a + include/wasmedge/*.h
OUT="${OUT:-${ROOT}/sdn-server/spacedatanetwork-static}"

mkdir -p "$WORK"

# 1. WasmEdge source
if [[ ! -d "$SRC" ]]; then
  git clone --branch "$WASMEDGE_VERSION" --depth 1 --recursive \
    https://github.com/WasmEdge/WasmEdge.git "$SRC"
fi

# DROP UPSTREAM'S -Werror. WasmEdge adds it unconditionally
# (cmake/Helper.cmake) and then fails its OWN sources under every compiler it
# did not test: gcc rejects component_type.cpp on -Wmaybe-uninitialized and
# coredump.cpp on -Warray-bounds, MSYS2's clang rejects the CRTP in
# host/wasi/inode.h. We consume this dependency rather than develop it, so its
# warnings must not gate our build — and without this, each new toolchain
# breaks the build again for reasons that are never ours.
if [[ -f "$SRC/cmake/Helper.cmake" ]]; then
  perl -0pi -e 's/^\s*-Werror\s*$//mg' "$SRC/cmake/Helper.cmake"
fi

# GRANT THE CRTP BASE FRIENDSHIP (Windows WASI only).
#
# FindHolderBase<T>::Proxy reaches T's PROTECTED members by writing
# `&T::doReset`, which names a protected member through T rather than through
# the accessing class. [class.protected] forbids that; MSVC accepts it as an
# extension, so this Windows-only header has never been compiled by anything
# else — gcc rejects it ("declared protected here") and clang rejects it
# ("must name member"). Upstream ships a non-conforming file, and there is no
# compiler choice that avoids it, so the base is made a friend instead.
if [[ -f "$SRC/include/host/wasi/inode.h" ]]; then
  perl -0pi -e 's/(class FindHolder : public FindHolderBase<FindHolder> \{)/$1\n  friend class FindHolderBase<FindHolder>;/g' \
    "$SRC/include/host/wasi/inode.h"
fi

# SANITISE THE STATIC-MERGE SCRATCH DIRECTORIES (Windows).
#
# The merge extracts each component into objs/<target>, and one target is
# literally named `fmt::fmt`. A colon cannot appear in a Windows path, so the
# merge dies with `Error creating directory "objs/fmt::fmt": Invalid argument`
# after every object has already compiled. The target name still has to reach
# $<TARGET_FILE:...> intact, so only the DIRECTORY is renamed.
if [[ -f "$SRC/lib/api/CMakeLists.txt" ]]; then
  perl -0pi -e 's/(function\(wasmedge_add_static_lib_component_command target\)\n)/$1  string(REPLACE "::" "_" _sanitized_target "${target}")\n/' \
    "$SRC/lib/api/CMakeLists.txt"
  perl -0pi -e 's/objs\/\$\{target\}/objs\/\$\{_sanitized_target\}/g' \
    "$SRC/lib/api/CMakeLists.txt"
  # SKIP COMPONENTS THAT ARE NOT REAL FILES. LLVM's component list can name a
  # dependency without a path (zstd on MinGW), and the merge then runs
  # `ar -x /libzstd.a` — an absolute path at the drive root — failing at 131/131
  # with everything already compiled. We link zstd explicitly from the archive
  # found above, so dropping it from the MERGE loses nothing.
  perl -0pi -e 's/(function\(wasmedge_add_libs_component_command target_path\)\n)/$1  if(NOT EXISTS "\${target_path}")\n    return()\n  endif()\n/' \
    "$SRC/lib/api/CMakeLists.txt"
  # EXPAND THE OBJECT GLOB IN A SHELL. The merge ends with
  #   ar -qcs libwasmedge.a <CAPI objects> objs/*/*.o
  # and relies on a SHELL expanding `objs/*/*.o`. CMake runs it through cmd.exe
  # on Windows, which does not glob, so ar received the pattern literally and
  # failed "objs/*/*.o: Invalid argument" at 131/131. Splitting it in two keeps
  # the generator expression (a CMake list) out of the quoted shell command,
  # which would otherwise be re-split into separate arguments.
  perl -0pi -e 's{COMMAND \$\{CMAKE_AR\} -qcs libwasmedge\.a \$<TARGET_OBJECTS:wasmedgeCAPI> objs/\*/\*\.o}
                 {COMMAND \${CMAKE_AR} -qcs libwasmedge.a \$<TARGET_OBJECTS:wasmedgeCAPI>\n      COMMAND sh -c "\${CMAKE_AR} -qcs libwasmedge.a objs/*/*.o"}g' \
    "$SRC/lib/api/CMakeLists.txt"
fi

# 2. Configure + build the static library (LLVM/AOT OFF).
if [[ ! -f "$SRC/build/lib/api/libwasmedge.a" ]]; then
  CC="${CC:-clang-16}" CXX="${CXX:-clang++-16}" \
  cmake -S "$SRC" -B "$SRC/build" -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DWASMEDGE_USE_LLVM=ON \
    -DWASMEDGE_LINK_LLVM_STATIC=ON \
    -DWASMEDGE_BUILD_SHARED_LIB=OFF \
    -DWASMEDGE_BUILD_STATIC_LIB=ON \
    -DWASMEDGE_BUILD_TOOLS=OFF \
    -DWASMEDGE_BUILD_PLUGINS=OFF \
    -DLLVM_DIR="${LLVM_DIR:-/usr/lib/llvm-16/lib/cmake/llvm}" \
    -DLLD_DIR="${LLD_DIR:-/usr/lib/llvm-16/lib/cmake/lld}" \
    ${CMAKE_AR:+-DCMAKE_AR="$CMAKE_AR"} \
    ${CMAKE_PREFIX_PATH:+-DCMAKE_PREFIX_PATH="$CMAKE_PREFIX_PATH"}
  JOBS="$( (command -v nproc >/dev/null && nproc) || sysctl -n hw.ncpu 2>/dev/null || echo 4 )"
  cmake --build "$SRC/build" -j"$JOBS"
fi

# 3. Stage archives + headers. The merged libwasmedge.a is INCOMPLETE (the
#    component-model loader lives in the sublibraries), so we link the full set
#    inside a --start-group; ld pulls only the members it needs, no duplicates.
rm -rf "$STATIC"; mkdir -p "$STATIC/lib" "$STATIC/include"
find "$SRC/build" -name "libwasmedge*.a" -exec cp {} "$STATIC/lib/" \;
cp "$SRC"/build/lib/host/wasi/*.a             "$STATIC/lib/" 2>/dev/null || true
cp "$SRC"/build/_deps/fmt-build/libfmt.a      "$STATIC/lib/"
cp "$SRC"/build/_deps/spdlog-build/libspdlog.a "$STATIC/lib/"
cp -r "$SRC"/include/api/wasmedge             "$STATIC/include/"
cp -r "$SRC"/build/include/api/wasmedge/*     "$STATIC/include/wasmedge/" 2>/dev/null || true

GRP="$STATIC/lib/libwasmedge.a"
for a in Common Loader LoaderFileMgr Validator Executor VM HostModuleWasi PO Driver System Plugin AOT LLVM; do
  [[ -f "$STATIC/lib/libwasmedge${a}.a" ]] && GRP="$GRP $STATIC/lib/libwasmedge${a}.a"
done
GRP="$GRP $STATIC/lib/libspdlog.a $STATIC/lib/libfmt.a"

# LLVM's own archives, in the order llvm-config gives them.
LLVM_CONFIG="${LLVM_CONFIG:-llvm-config-16}"
LLVMLIBS="$($LLVM_CONFIG --link-static --libfiles | tr '\n' ' ')"


# FIND THE STATIC ZSTD. LLVM's Support library calls ZSTD_decompress, so the
# archive must come with us or the link fails with undefined references to it.
# Discovered rather than required from the caller: only macOS was setting
# ZSTD_STATIC, which left Linux linking nothing at all for zstd once the
# hand-written -lzstd came off the link line.
if [[ -z "${ZSTD_STATIC:-}" ]]; then
  for cand in \
    /usr/lib/x86_64-linux-gnu/libzstd.a \
    /usr/lib/aarch64-linux-gnu/libzstd.a \
    /usr/lib/libzstd.a \
    /mingw64/lib/libzstd.a; do
    if [[ -f "$cand" ]]; then ZSTD_STATIC="$cand"; break; fi
  done
fi
# Plain ifs, not `[[ ]] && ...`: under `set -e` a trailing test that fails is
# the script's exit status, so a no-match on the LAST candidate killed the build
# outright. Linux matched its first candidate and never showed it; macOS, which
# is handed ZSTD_STATIC and skips the loop entirely, died on the report line.
if [[ -n "${ZSTD_STATIC:-}" ]]; then
  echo "static zstd: $ZSTD_STATIC"
else
  echo "no static zstd found; relying on the link line" >&2
fi

# Make the prefix SELF-CONTAINED and RELOCATABLE.
#
# LLVM's archives are copied in beside WasmEdge's, and link.flags names every
# archive through a @PREFIX@ placeholder that the consumer substitutes. Both
# matter: a prefix that points at /usr/lib/llvm-16 or at the absolute path it
# happened to be built in cannot be handed to another machine, another job, or
# another checkout — which is exactly what a cached CI artifact is. Measured the
# hard way: link.flags with absolute paths failed every downstream job with
# "cannot find .../libwasmedgeAOT.a".
for archive in $LLVMLIBS; do
  [[ -f "$archive" ]] && cp -n "$archive" "$STATIC/lib/" 2>/dev/null || true
done
[[ -n "${ZSTD_STATIC:-}" && -f "${ZSTD_STATIC}" ]] && cp -n "$ZSTD_STATIC" "$STATIC/lib/" 2>/dev/null || true

{
  for archive in $GRP $LLVMLIBS ${ZSTD_STATIC:-}; do
    [[ -n "$archive" ]] || continue
    printf '@PREFIX@/lib/%s ' "$(basename "$archive")"
  done
  case "$(uname -s)" in
    # -lc++abi as well as -lc++: libc++'s exception ABI lives in libc++abi on
    # macOS, and without it the link dies on ___cxa_init_primary_exception.
    Darwin) printf -- '-lc++ -lc++abi -lm -lz -lncurses\n' ;;
    # MinGW/clang on Windows: the C++ runtime and the sockets/crypto libraries
    # the runtime calls into. MSVC's lib.exe cannot do the `ar -x` extraction
    # WasmEdge's static merge performs, so CMAKE_AR must be llvm-ar there.
    MINGW*|MSYS*|CYGWIN*) printf -- '-lstdc++ -lm -lws2_32 -lbcrypt -lole32 -luuid\n' ;;
    *)      printf -- '-lstdc++ -lm -ldl -lpthread -lz -ltinfo\n' ;;
  esac
} > "$STATIC/link.flags"

# Stop here when the caller only wants the staged archives. A Docker layer that
# builds LLVM + WasmEdge is expensive and depends ONLY on the version, so the
# image builds it once and rebuilds it only when WASMEDGE_VERSION changes; the
# Go build then lives in a later, cheap layer.
if [[ -n "${WASMEDGE_STATIC_PREFIX_ONLY:-}" ]]; then
  echo "staged static WasmEdge prefix: $STATIC"
  exit 0
fi

# 4. Build the daemon. -extldflags places the C++ runtime + WasmEdge archives
#    LAST on the external link line (after libwasmedge.a from the binding's
#    -lwasmedge), so static symbols resolve. libstdc++/libc stay dynamic — they
#    are base system libraries present on every Ubuntu host.
cd "$ROOT/sdn-server"
CGO_ENABLED=1 \
CGO_CFLAGS="-I$STATIC/include" \
CGO_LDFLAGS="-L$STATIC/lib" \
go build -ldflags "-linkmode external -extldflags \"-Wl,--start-group $GRP $LLVMLIBS -lstdc++ -Wl,--end-group -lm -ldl -lpthread -lz -lzstd -ltinfo\"" \
  -o "$OUT" ./cmd/spacedatanetwork

echo "built: $OUT"
DEPS_TOOL="$( (command -v ldd >/dev/null && echo ldd) || (command -v otool >/dev/null && echo 'otool -L') || echo '' )"
if [[ -n "$DEPS_TOOL" ]] && $DEPS_TOOL "$OUT" 2>/dev/null | grep -qi wasmedge; then
  echo "WARNING: binary still references libwasmedge (not self-contained)" >&2
  exit 1
fi
echo "self-contained OK (no libwasmedge dependency):"
[[ -n "$DEPS_TOOL" ]] && $DEPS_TOOL "$OUT" || true
