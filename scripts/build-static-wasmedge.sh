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
  # TOLERATE AN EMPTY objs/. The merge extracts each component into objs/<t> and
  # then re-archives `objs/*/*.o`, relying on a SHELL to expand it. cmd.exe does
  # not glob, and on Windows the extraction leaves objs/ empty anyway, so ar got
  # the literal pattern and failed "Invalid argument" at 131/131 — every object
  # compiled, dying on the last command.
  #
  # The merged archive only has to carry the CAPI objects: the link line names
  # every component archive individually (see GRP below), so nothing is lost if
  # objs/ is empty. The second command adds them when they exist and is a no-op
  # when they do not.
  perl -0pi -e 's{COMMAND \$\{CMAKE_AR\} -qcs libwasmedge\.a \$<TARGET_OBJECTS:wasmedgeCAPI> objs/\*/\*\.o}
                 {COMMAND \${CMAKE_AR} -qcs libwasmedge.a \$<TARGET_OBJECTS:wasmedgeCAPI>\n      COMMAND sh -c "\${CMAKE_AR} -qcs libwasmedge.a objs/*/*.o || true"}g' \
    "$SRC/lib/api/CMakeLists.txt"
fi

# 2. Configure + build the static library WITH LLVM.
#
# NOT optional, and the opposite of what this comment used to claim. In 0.16.4
# WASMEDGE_BUILD_AOT_RUNTIME is deprecated and aliased to WASMEDGE_USE_LLVM
# (CMakeLists.txt:69-72), so there is no load-only AOT mode: turning LLVM off
# removes the ability to LOAD a precompiled artifact, not just to produce one,
# and every query silently falls back to the interpreter at ~100x.
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

# WINDOWS: make the staged header describe the STATIC library it sits beside.
#
# wasmedge_basic.h has exactly two cases on Windows — WASMEDGE_COMPILE_LIBRARY
# means __declspec(dllexport), anything else means __declspec(dllimport) — and
# no third case for a consumer linking the static archive, which is what we are.
# So every C API call compiled against it referenced the DLL import thunk and
# the link died with "undefined reference to `__imp_WasmEdge_ASTModuleDelete'"
# once per function, against archives that hold the plain symbols.
#
# Patched HERE rather than by defining a macro in the consumer, for the same
# reason link.flags lives here: the prefix has to be self-describing. A consumer
# that has to know a magic define is a consumer that will one day forget it.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    for header in "$STATIC"/include/wasmedge/*.h; do
      [[ -f "$header" ]] || continue
      perl -0pi -e 's/#define\s+(WASMEDGE_CAPI_(?:PLUGIN_)?EXPORT)\s+__declspec\(dllimport\)/#define $1/g' "$header"
    done
    if grep -rq 'dllimport' "$STATIC/include/wasmedge/"; then
      echo "static prefix: a WasmEdge header still declares dllimport" >&2
      grep -rn 'dllimport' "$STATIC/include/wasmedge/" >&2
      exit 1
    fi
    echo "patched staged headers for static linkage (no dllimport)"
    ;;
esac

# EVERY staged WasmEdge archive, not a hand-kept list. The list silently omitted
# libwasmedgePluginWasiLogging.a, which left
# WasmEdge::Host::WasiLoggingModule::PluginDescriptor undefined — a component
# added upstream would fail the same way, and the failure is a link error a long
# way from its cause.
GRP="$STATIC/lib/libwasmedge.a"
for archive in "$STATIC"/lib/libwasmedge*.a; do
  [[ -f "$archive" && "$archive" != "$STATIC/lib/libwasmedge.a" ]] && GRP="$GRP $archive"
done
GRP="$GRP $STATIC/lib/libspdlog.a $STATIC/lib/libfmt.a"

# LLVM's own archives, in the order llvm-config gives them.
LLVM_CONFIG="${LLVM_CONFIG:-llvm-config-16}"
LLVMLIBS="$($LLVM_CONFIG --link-static --libfiles | tr '\n' ' ')"

# LLD, EXPLICITLY. WasmEdge's AOT links its output through lld, but
# `llvm-config --libfiles` never lists lld's archives — they only reached the
# binary because the merge swept them into libwasmedge.a. Once the merge started
# skipping components it could not resolve, the link failed on
# `lld::CommonLinkerContext::destroy()`. Naming them here does not depend on how
# the merge behaves.
LLVM_LIBDIR="$($LLVM_CONFIG --libdir 2>/dev/null || true)"
if [[ -n "$LLVM_LIBDIR" && -d "$LLVM_LIBDIR" ]]; then
  for lld in "$LLVM_LIBDIR"/liblldCommon.a "$LLVM_LIBDIR"/liblldELF.a \
             "$LLVM_LIBDIR"/liblldCOFF.a "$LLVM_LIBDIR"/liblldMachO.a \
             "$LLVM_LIBDIR"/liblldMinGW.a "$LLVM_LIBDIR"/liblldWasm.a; do
    [[ -f "$lld" ]] && LLD_LIBS="${LLD_LIBS:-} $lld"
  done
fi
# lld BEFORE LLVM. A static archive must precede the archives it depends on, and
# lld calls into LLVM (llvm::parallelFor out of Support). Appending it left those
# undefined even though the archive was present.
LLVMLIBS="${LLD_LIBS:-} $LLVMLIBS"


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

# lld has gone missing from this prefix once before, and the symptom was 26
# undefined lld:: symbols at the far end of a Go build on another machine.
# Check it here, where the answer is one line.
# A plain `if`, NOT `[[ -f ]] && count=…`: under `set -e` a false test on the
# last iteration makes the loop return non-zero and kills the script before the
# diagnosis below can print. That exact shape has bitten this file once already.
lld_staged=0
for archive in "$STATIC"/lib/liblld*.a; do
  if [[ -f "$archive" ]]; then
    lld_staged=$((lld_staged + 1))
  fi
done
if [[ "$lld_staged" -eq 0 ]]; then
  echo "static prefix: no lld archives staged — WasmEdge's AOT links through lld and will not resolve" >&2
  echo "  LLVM_CONFIG=$LLVM_CONFIG libdir=${LLVM_LIBDIR:-<unset>}" >&2
  ls "${LLVM_LIBDIR:-/nonexistent}"/liblld*.a 2>&1 | head -5 >&2 || true
  exit 1
fi
echo "static prefix: $lld_staged lld archive(s) staged"

{
  # --start-group on ELF: these archives depend on each other BOTH ways (lld
  # calls LLVM, lld's own ELF/Common halves call each other), and a single-pass
  # linker cannot resolve that from any fixed order. ld64 on macOS already
  # re-scans archives, so it needs no group and does not accept one.
  case "$(uname -s)" in Darwin|MINGW*|MSYS*|CYGWIN*) : ;; *) printf -- '-Wl,--start-group ' ;; esac
  for archive in $GRP $LLVMLIBS ${ZSTD_STATIC:-}; do
    [[ -n "$archive" ]] || continue
    printf '@PREFIX@/lib/%s ' "$(basename "$archive")"
  done
  case "$(uname -s)" in Darwin|MINGW*|MSYS*|CYGWIN*) : ;; *) printf -- '-Wl,--end-group ' ;; esac
  case "$(uname -s)" in
    # -lc++abi as well as -lc++: libc++'s exception ABI lives in libc++abi on
    # macOS, and without it the link dies on ___cxa_init_primary_exception.
    Darwin) printf -- '-lc++ -lc++abi -lm -lz -lncurses\n' ;;
    # MinGW/clang on Windows: the C++ runtime and the sockets/crypto libraries
    # the runtime calls into. MSVC's lib.exe cannot do the `ar -x` extraction
    # WasmEdge's static merge performs, so CMAKE_AR must be llvm-ar there.
    # Every one of these was a name in the link error, not a guess:
    #   ntdll   NtQueryInformationFile, NtSetInformationFile,
    #           NtQueryTimerResolution, RtlGetLastNtStatus (WASI inode-win.cpp)
    #   ktmw32  CreateTransaction, CommitTransaction (transactional file ops)
    #   dbghelp SymInitializeW, SymGetModuleBase64, SymSetOptions … (LLVM's
    #           Windows backtrace support)
    #   xml2    xmlReadMemory, xmlDocDumpFormatMemoryEnc … (LLVM's
    #           WindowsManifest merger, libLLVMWindowsManifest.a)
    #   z       compress2, compressBound, crc32
    MINGW*|MSYS*|CYGWIN*) printf -- '-lstdc++ -lm -lws2_32 -lbcrypt -lole32 -luuid -lntdll -lktmw32 -ldbghelp -lxml2 -lz\n' ;;
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
