#!/usr/bin/env bash
# Toolchain selection for the static WasmEdge prefix.
#
# This lives in a FILE, not in the workflow, for one reason: the cache key that
# decides whether a prefix is rebuilt hashes it. When the selection lived inline
# in beta-release-artifacts.yml the key had to hash the entire workflow, so every
# unrelated edit to any job threw away five prefixes and paid a full LLVM +
# WasmEdge rebuild on five runners. Hashing only what actually shapes the prefix
# keeps the guarantee and drops the tax.
#
# Source it (do not execute it) before scripts/build-static-wasmedge.sh.
# The INSTALLED package sets still live in the workflow — the actions that
# install them take literal inputs — so matrix.toolchain tags them and belongs
# in the key too.

case "$(uname -s)" in
  Darwin)
    # COMPILE WITH APPLE CLANG, link LLVM from Homebrew. The two halves must
    # agree on the C++ runtime: building WasmEdge against Homebrew's libc++ and
    # then letting the Go link use /usr/bin/clang and the system libc++ leaves
    # std::exception_ptr::__from_native_exception_pointer and
    # ___cxa_init_primary_exception undefined. Only the LLVM ARCHIVES come from
    # Homebrew; the toolchain is the system's, which is what
    # go-with-wasmedge.sh links with.
    _L="$(brew --prefix llvm@18)"
    export CC=/usr/bin/clang
    export CXX=/usr/bin/clang++
    export LLVM_DIR="${_L}/lib/cmake/llvm"
    export LLD_DIR="${_L}/lib/cmake/lld"
    export LLVM_CONFIG="${_L}/bin/llvm-config"
    export ZSTD_STATIC="$(brew --prefix zstd)/lib/libzstd.a"
    unset _L
    ;;
  MINGW*|MSYS*|CYGWIN*)
    _L=/mingw64
    if [[ ! -f "${_L}/lib/cmake/llvm/LLVMConfig.cmake" ]]; then
      echo "MSYS2 mingw-w64-x86_64-llvm did not provide LLVMConfig.cmake" >&2
      ls "${_L}/lib/cmake" 2>/dev/null >&2 || true
      exit 1
    fi
    # GCC, not clang. MSYS2's clang rejects WasmEdge's own CRTP in
    # include/host/wasi/inode.h:285 ("must name member"), which stopped the
    # build at 100/131 objects; gcc accepts it, and the MinGW link branch
    # already targets libstdc++, which is gcc's runtime.
    #
    # CMAKE_PREFIX_PATH so LLVM's exported targets resolve zstd to a REAL path.
    # Without it cmake reduced it to a bare name and the merge ran
    # `ar /libzstd.a` — an absolute path at the drive root — failing at 131/131
    # after everything had already compiled.
    export CMAKE_PREFIX_PATH="${_L}"
    export CC="${_L}/bin/gcc.exe"
    export CXX="${_L}/bin/g++.exe"
    export CMAKE_AR="${_L}/bin/ar.exe"
    export LLVM_CONFIG="${_L}/bin/llvm-config.exe"
    export LLVM_DIR="${_L}/lib/cmake/llvm"
    export LLD_DIR="${_L}/lib/cmake/lld"
    unset _L
    ;;
  *)
    # Linux: the distro's clang-16/llvm-16-dev/liblld-16-dev are already the
    # compiler and the archives, so nothing has to be pointed anywhere.
    :
    ;;
esac
