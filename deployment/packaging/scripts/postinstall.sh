#!/bin/sh
# Space Data Network: after install or upgrade of the .deb or .rpm.
#
# The systemd units run as User=sdn / Group=sdn with ReadWritePaths=
# /var/lib/spacedatanetwork, and until v1.1.0 no package created either, so a
# fresh install's `systemctl start` died with "Failed to determine credentials
# for user 'sdn': Unknown user" (status=217/USER). Create both if absent, owned
# by sdn, then reload systemd so it sees the unit. The service is NOT enabled or
# started here: the packages never did that, and an operator chooses when.
set -e

if ! getent group sdn >/dev/null 2>&1; then
  groupadd --system sdn
fi
if ! getent passwd sdn >/dev/null 2>&1; then
  useradd --system --gid sdn --home-dir /var/lib/spacedatanetwork --no-create-home \
    --shell /usr/sbin/nologin --comment "Space Data Network" sdn
fi
if [ ! -d /var/lib/spacedatanetwork ]; then
  install -d -o sdn -g sdn -m 0750 /var/lib/spacedatanetwork
fi

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
exit 0
