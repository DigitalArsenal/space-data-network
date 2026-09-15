#!/usr/bin/env sh
# Stage the node dashboard as sdn-js/ui/dist, for anything that packages or
# serves the "admin UI" directory.
#
# THE NODE'S DASHBOARD IS THE ONE IN THE BINARY. sdn-js run build:dashboard
# writes it straight to sdn-server/cmd/spacedatanetwork/embedded/dashboard.html,
# which is tracked and served by //go:embed (conjunction_ui.go).
#
# This file exists because the callers used to run `npm run build:ui` — a script
# the UI consolidation deleted. Nothing failed at deletion time; the jobs failed
# later, one at a time, whenever one of them next ran. ipfs-deploy.sh was fixed
# first, then the release packages job died the same way on the same missing
# script. One copy of this so there is no third.
#
# Staging the tracked artifact means a bundle ships the exact bytes the binary
# serves — no drift, no second build, and no npm on the release critical path.
set -eu

root="${1:?usage: stage-admin-ui.sh <repo-root> [dest]}"
dest="${2:-${root}/sdn-js/ui/dist}"
src="${root}/sdn-server/cmd/spacedatanetwork/embedded/dashboard.html"

if [ ! -f "${src}" ]; then
  echo "missing embedded dashboard: ${src}" >&2
  echo "it is tracked in git; run 'npm --prefix sdn-js run build:dashboard' to regenerate it" >&2
  exit 1
fi

rm -rf "${dest}"
mkdir -p "${dest}"
cp "${src}" "${dest}/index.html"
echo "staged admin UI from the embedded dashboard: ${dest}/index.html"
