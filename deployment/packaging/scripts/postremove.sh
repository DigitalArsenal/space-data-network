#!/bin/sh
# Space Data Network: after removal (and purge) of the .deb or .rpm.
#
# Only reloads systemd, which no longer has the unit. The sdn user and
# /var/lib/spacedatanetwork are deliberately LEFT on purge as well: the data
# directory holds the node's sealed identity (its PeerID) and its record store,
# and removing a system user that may still own files is what Debian policy
# advises against. An operator who wants them gone removes them by hand.
#
# The same goes for /etc/systemd/system/spacedatanetwork.service.d/
# key-password.conf, which postinstall writes on a fresh node whose machine
# fingerprint is refused: it names /var/lib/spacedatanetwork/key-password, the
# file the kept identity is sealed under, and a reinstall over that identity
# needs it (postinstall never touches a node that already has an identity).
set -e
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
exit 0
