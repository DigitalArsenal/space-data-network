#!/bin/sh
# Space Data Network: after removal (and purge) of the .deb or .rpm.
#
# Only reloads systemd, which no longer has the unit. The sdn user and
# /var/lib/spacedatanetwork are deliberately LEFT on purge as well: the data
# directory holds the node's sealed identity (its PeerID) and its record store,
# and removing a system user that may still own files is what Debian policy
# advises against. An operator who wants them gone removes them by hand.
set -e
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
exit 0
