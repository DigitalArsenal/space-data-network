#!/bin/sh
# The AOT artifact is native code for the CPU that compiles it, so it CANNOT be
# baked into a portable image. This used to run at build time; the image then
# carried whatever instruction set the builder happened to have, and a host
# without it died on boot with SIGILL inside WasmEdge_VMExecuteRegistered.
#
# Compiling here instead means it targets the CPU actually running the node.
# XDG_CACHE_HOME points into /app/data (the volume), so the cost is paid once
# per host rather than once per container start.
set -e

case "$1" in
  daemon)
    if ! /app/spacedatanetwork prewarm-aot; then
      echo "warning: AOT prewarm failed; the FlatSQL engine will run interpreted (much slower)" >&2
    fi
    ;;
esac

exec /app/spacedatanetwork "$@"
