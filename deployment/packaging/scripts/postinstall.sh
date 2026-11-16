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

# THE KEY PASSWORD FILE OF A FRESH NODE.
#
# A node with no key password seals its identity under a key derived from the
# machine and the account it runs as. On most Linux hosts that derivation is
# REFUSED for the service account: the kernel creates
# /sys/class/dmi/id/product_uuid readable by root only, and the node refuses a
# key that would depend on the privilege of the reading process. Before this
# step the first `systemctl start` then failed until the operator made a
# password file and a drop-in by hand. On a FRESH node this does that instead:
#
#   /var/lib/spacedatanetwork/key-password   32 random bytes as 64 hex, sdn:sdn 0400
#   /etc/systemd/system/spacedatanetwork.service.d/key-password.conf
#                                            Environment=SDN_KEY_PASSWORD_FILE=<that file>
#
# Fresh means: the full node is installed, nothing already gives the unit a
# key password (SDN_KEY_PASSWORD / SDN_KEY_PASSWORD_FILE in the unit or a
# drop-in other than ours), and the node itself says its fingerprint is
# refused for user sdn and that it has no identity and no configured password
# (`key fresh-password-file`, which reads the installed config, so a moved
# storage path is honoured). An upgrade over a node that has an identity
# changes nothing: the identity stays sealed as it is. An existing password
# file is never overwritten. Nothing here can fail the install.
#
# WHAT THIS COSTS. The file is where the manual recipe put it, in the
# service's data directory beside the identity: a copy of
# /var/lib/spacedatanetwork alone (a backup, a snapshot) opens the node
# identity and its credential store. To keep them apart, move the file
# elsewhere (sdn:sdn 0400; /etc/spacedatanetwork/ is one place) and point the
# drop-in at it. And because it is made HERE, at install time: a machine image
# built with the package installed carries one password for every clone; on
# each clone, remove the file and the drop-in before the first start and
# reinstall the package (apt-get install --reinstall / dnf reinstall) so it
# gets its own.
#
# The variable reaches the service only. A CLI command run beside it needs the
# same variable:
#   sudo -u sdn env SDN_KEY_PASSWORD_FILE=/var/lib/spacedatanetwork/key-password \
#     /opt/spacedatanetwork/bin/spacedatanetwork -c /etc/spacedatanetwork/config.yaml <command>
# (the node's own refusal names the file and this command when it is missing).
SDN_BIN=/opt/spacedatanetwork/bin/spacedatanetwork
SDN_UNIT=spacedatanetwork.service
SDN_CONFIG_FILE=/etc/spacedatanetwork/config.yaml
KEY_PASSWORD_FILE=/var/lib/spacedatanetwork/key-password
KEY_PASSWORD_DROPIN_DIR=/etc/systemd/system/${SDN_UNIT}.d
KEY_PASSWORD_DROPIN=${KEY_PASSWORD_DROPIN_DIR}/key-password.conf

run_as_sdn() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u sdn -- env HOME=/var/lib/spacedatanetwork "$@"
  else
    su -s /bin/sh sdn -c 'HOME=/var/lib/spacedatanetwork exec "$0" "$@"' -- "$@"
  fi
}

# unit_sets_key_password: the unit, or a drop-in other than ours, already sets
# SDN_KEY_PASSWORD or SDN_KEY_PASSWORD_FILE.
unit_sets_key_password() {
  for f in /etc/systemd/system/${SDN_UNIT} \
           /etc/systemd/system/${SDN_UNIT}.d/*.conf \
           /run/systemd/system/${SDN_UNIT}.d/*.conf \
           /usr/lib/systemd/system/${SDN_UNIT}.d/*.conf \
           /lib/systemd/system/${SDN_UNIT}.d/*.conf; do
    [ -f "$f" ] || continue
    [ "$f" = "$KEY_PASSWORD_DROPIN" ] && continue
    if grep -q 'SDN_KEY_PASSWORD' "$f"; then
      return 0
    fi
  done
  return 1
}

setup_fresh_key_password() {
  [ -x "$SDN_BIN" ] || return 0                        # edge-only install: nothing to seal
  [ -f "/etc/systemd/system/${SDN_UNIT}" ] || return 0
  unit_sets_key_password && return 0
  verdict=$(run_as_sdn "$SDN_BIN" key fingerprint-check 2>/dev/null) || return 0
  [ "$verdict" = "refused" ] || return 0
  if ! outcome=$(run_as_sdn "$SDN_BIN" -c "$SDN_CONFIG_FILE" key fresh-password-file "$KEY_PASSWORD_FILE"); then
    echo "spacedatanetwork: could not prepare $KEY_PASSWORD_FILE; see the error above, and the release notes for the manual steps" >&2
    return 0
  fi
  case "$outcome" in
    "created "*|"exists "*) ;;
    *) return 0 ;;
  esac
  if [ ! -e "$KEY_PASSWORD_DROPIN" ]; then
    mkdir -p "$KEY_PASSWORD_DROPIN_DIR"
    cat > "$KEY_PASSWORD_DROPIN" <<EOF
# Written by the spacedatanetwork package on a FRESH install: this host's
# machine fingerprint is refused for user sdn (the Linux kernel makes
# /sys/class/dmi/id/product_uuid root-only), so the node seals its identity
# under the generated key password file below. Keep this drop-in and that
# file: the identity cannot be opened without it. The file sits in the data
# directory, so a copy of /var/lib/spacedatanetwork alone opens the identity
# and the credential store; to keep them apart, move the file (sdn:sdn 0400)
# and change the path below. The package never removes either.
# CLI commands get the variable only when given it:
#   sudo -u sdn env SDN_KEY_PASSWORD_FILE=${KEY_PASSWORD_FILE} /opt/spacedatanetwork/bin/spacedatanetwork -c ${SDN_CONFIG_FILE} <command>
[Service]
Environment=SDN_KEY_PASSWORD_FILE=${KEY_PASSWORD_FILE}
EOF
    chmod 0644 "$KEY_PASSWORD_DROPIN"
  fi
  echo "spacedatanetwork: fresh node, machine fingerprint refused for user sdn: its identity will be sealed under ${KEY_PASSWORD_FILE} (${KEY_PASSWORD_DROPIN}). Keep that file for the life of the node. It is in the data directory: a copy of /var/lib/spacedatanetwork alone opens the identity and the credential store, so back the file up apart from it, or move it and edit the drop-in. CLI commands need it too: sudo -u sdn env SDN_KEY_PASSWORD_FILE=${KEY_PASSWORD_FILE} ${SDN_BIN} -c ${SDN_CONFIG_FILE} <command>"
}

setup_fresh_key_password || true

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
exit 0
