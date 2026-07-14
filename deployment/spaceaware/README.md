# sdn.spaceaware.io Deployment

This directory contains the production target config for the first SDN
module-delivery and OrbPro licensing provider node.

The deployment config uses `host: sdn.spaceaware.io` on purpose. Keep the real
address and key material in `~/.ssh/config`; do not commit machine-specific SSH
paths or IP addresses here.

From the `space-data-network` repository root:

```bash
./deployment/scripts/deploy.sh \
  -c deployment/spaceaware/servers.yaml \
  -u root \
  -b \
  deploy full
```

Before deploying from a development workstation, make sure the local desktop
tool is running so the bundled desktop/Kubo integration is healthy in the same
checkout:

```bash
./scripts/update-upstream-ipfs.sh --check
test -d desktop/node_modules || npm run install:desktop
npm --prefix desktop start
```

When a deploy touches `webui/`, `desktop/`, or `sdn-js/ui/src/upstream-webui/`,
also rebuild/relaunch the packaged desktop app and verify Kubo RPC from the
desktop-selected HTTP origin before production deploy. Keep the desktop process
open while you smoke-test the deployed node. In a second terminal, verify the
production service after deploy:

```bash
./deployment/scripts/deploy.sh \
  -c deployment/spaceaware/servers.yaml \
  -u root \
  -b \
  status
curl -fsS https://sdn.spaceaware.io/ >/dev/null
curl -fsS https://sdn.spaceaware.io/webui/ >/dev/null
```

The provider authorizes OrbPro module publishing with SDN wallet admin state,
not a shared publish token. The deployment wallet that signs `plugins
publish-orbpro` must be present as an admin in the live auth store
(`/opt/data/sdn/auth.db`) or configured as an admin user before the provider is
started.

The server must have the encrypted plugin catalog root configured:

```bash
SDN_PLUGIN_ROOT=/var/lib/spacedatanetwork/data/license/plugins
```

The OrbPro GitHub Pages workflow signs libp2p module-publish requests with an
HD-wallet signing key. The provider verifies the Ed25519 signature against the
signer xpub and accepts the update only when that xpub is an admin.

## Kubo repository volume migration

The hosted Kubo repository belongs on the attached volume at
`/mnt/volume_nyc3_01/ipfs`. The migration is deliberately copy-only:
`/var/lib/ipfs` is retained unchanged throughout rollout and remains the manual
rollback repository. Do not delete it after a successful migration.
The destination is a dedicated Kubo repository directory on the exact mounted
volume; for the first rollout it should be absent or empty and must not contain
unrelated files.

The standard SpaceAware binary deploy (the command above using the exact
`deployment/spaceaware/servers.yaml` config) installs the migration at
`/opt/spacedatanetwork/deployment/spaceaware/migrate-kubo-repo.sh`. It restores
`root:root` ownership and mode `0755` on `/opt/spacedatanetwork`, both
`deployment` directories, and the script in the same remote command that
finishes recursive service ownership, before any service restart. Other deploy
configs neither receive this script nor the NYC volume allowlist. The
SpaceAware SDN unit uses optional systemd syntax
`-/mnt/volume_nyc3_01/ipfs`, so deploying before the migration creates the
directory cannot fail solely because that path is absent.

### Preflight

Connect to `sdn.spaceaware.io`, confirm the exact attached mount (not merely a
directory on `/`), and verify that the volume has the source repository size
plus the script's 10 GiB safety margin available:

```bash
ssh sdn.spaceaware.io

test "$(findmnt --noheadings --mountpoint /mnt/volume_nyc3_01 --output TARGET)" = "/mnt/volume_nyc3_01"
sudo test -d /var/lib/ipfs
sudo test ! -L /mnt/volume_nyc3_01/ipfs
if sudo test -e /mnt/volume_nyc3_01/ipfs; then
  test -z "$(sudo find /mnt/volume_nyc3_01/ipfs -mindepth 1 -print -quit)"
  DESTINATION_PROBE=/mnt/volume_nyc3_01/ipfs
else
  DESTINATION_PROBE=/mnt/volume_nyc3_01
fi
test "$(findmnt --noheadings --target "$DESTINATION_PROBE" --output TARGET)" = "/mnt/volume_nyc3_01"
SOURCE_KIB="$(sudo du -sk /var/lib/ipfs | awk 'NR == 1 { print $1 }')"
AVAILABLE_KIB="$(df -Pk /mnt/volume_nyc3_01 | awk 'NR == 2 { print $4 }')"
REQUIRED_KIB="$((SOURCE_KIB + 10485760))"
test "$AVAILABLE_KIB" -ge "$REQUIRED_KIB"

sudo systemctl --no-pager --full status kubo.service space-data-network.service
SOURCE_PEER_ID="$(sudo env IPFS_PATH=/var/lib/ipfs ipfs id --format='<id>')"
SOURCE_PIN_COUNT="$(sudo env IPFS_PATH=/var/lib/ipfs ipfs pin ls --type=recursive --quiet | LC_ALL=C sort -u | awk 'NF { count++ } END { print count + 0 }')"
test -n "$SOURCE_PEER_ID"
test "$SOURCE_PIN_COUNT" -ge 5
printf 'source peer=%s recursive pins=%s\n' "$SOURCE_PEER_ID" "$SOURCE_PIN_COUNT"
```

The script automatically snapshots the current drop-in and active/inactive
state of both services for failure rollback. Before a planned production run,
also keep an operator-owned snapshot so a completed migration can be reverted
later:

```bash
sudo test ! -e /root/kubo-volume-migration-preflight
sudo install -d -m 0700 /root/kubo-volume-migration-preflight
if sudo test -e /etc/systemd/system/kubo.service.d/20-volume-repo.conf; then
  sudo cp -a /etc/systemd/system/kubo.service.d/20-volume-repo.conf \
    /root/kubo-volume-migration-preflight/drop-in.conf
else
  sudo touch /root/kubo-volume-migration-preflight/drop-in.absent
fi
if sudo systemctl is-active --quiet kubo.service; then
  printf 'active\n' | sudo tee /root/kubo-volume-migration-preflight/kubo.state >/dev/null
else
  printf 'inactive\n' | sudo tee /root/kubo-volume-migration-preflight/kubo.state >/dev/null
fi
if sudo systemctl is-active --quiet space-data-network.service; then
  printf 'active\n' | sudo tee /root/kubo-volume-migration-preflight/sdn.state >/dev/null
else
  printf 'inactive\n' | sudo tee /root/kubo-volume-migration-preflight/sdn.state >/dev/null
fi
```

### Invocation

Run the checked-in script as root without path or service overrides:

```bash
sudo /opt/spacedatanetwork/deployment/spaceaware/migrate-kubo-repo.sh
```

It refuses a missing mount, insufficient free space, fewer than five recursive
pins, any destination symlink, a canonical source alias or volume escape, a
nested destination mount, a changed peer ID, a lower post-copy pin count, a
missing sample pin, or an unhealthy local API/gateway. It stops
`space-data-network.service` before `kubo.service`, copies with
`rsync -aHAX --numeric-ids`, sets
`Datastore.StorageMax` to `120GB`, and installs both `IPFS_PATH` and an additive
Kubo `ReadWritePaths` entry for the destination. The copy never uses
`--delete`, and the script never writes, changes ownership, or removes anything
under `/var/lib/ipfs`.

After Kubo starts, the script gives the API and gateway up to 30 attempts with
a one-second delay between attempts (and bounded per-request timeouts). API
readiness is required before destination identity and pin checks; the gateway
check follows those checks, and SDN starts only after all of them pass.

### Verification

Confirm the live unit environment, repository configuration, services, API,
and one deterministic gateway sample:

```bash
test "$(findmnt --noheadings --mountpoint /mnt/volume_nyc3_01 --output TARGET)" = "/mnt/volume_nyc3_01"
sudo systemctl show kubo.service --property=Environment --value | tr ' ' '\n' | grep -Fx 'IPFS_PATH=/mnt/volume_nyc3_01/ipfs'
sudo systemctl show kubo.service --property=ReadWritePaths --value | tr ' ' '\n' | grep -Fx '/mnt/volume_nyc3_01/ipfs'
for ROOT_OWNED_PATH in \
  /opt/spacedatanetwork \
  /opt/spacedatanetwork/deployment \
  /opt/spacedatanetwork/deployment/spaceaware \
  /opt/spacedatanetwork/deployment/spaceaware/migrate-kubo-repo.sh; do
  test "$(sudo stat -c '%U:%G %a' "$ROOT_OWNED_PATH")" = "root:root 755"
done
test "$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs config Datastore.StorageMax)" = "120GB"
sudo systemctl is-active --quiet kubo.service
sudo systemctl is-active --quiet space-data-network.service

DESTINATION_PEER_ID="$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs id --format='<id>')"
DESTINATION_PIN_COUNT="$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs pin ls --type=recursive --quiet | LC_ALL=C sort -u | awk 'NF { count++ } END { print count + 0 }')"
test "$DESTINATION_PEER_ID" = "$SOURCE_PEER_ID"
test "$DESTINATION_PIN_COUNT" -ge "$SOURCE_PIN_COUNT"

curl -fsS --max-time 10 --request POST http://127.0.0.1:5001/api/v0/id >/dev/null
SAMPLE_CID="$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs pin ls --type=recursive --quiet | LC_ALL=C sort -u | head -n 1)"
test -n "$SAMPLE_CID"
curl -fsSIL --max-time 20 "http://127.0.0.1:8080/ipfs/${SAMPLE_CID}" >/dev/null
sudo test -d /var/lib/ipfs
```

`SOURCE_PEER_ID` and `SOURCE_PIN_COUNT` above are the values captured in the
same shell during preflight. The final `test -d` is intentional: the old
`/var/lib/ipfs` repository must remain present.

### Rollback

Any error after service shutdown triggers the script's `ERR`/`EXIT` handler,
which restores the exact prior drop-in (including prior absence and file mode),
runs `systemctl daemon-reload`, and restores each service to its recorded
active or inactive state. Inspect the two units before retrying if the script
reports that rollback itself encountered an error.

Rollback intentionally leaves any partial destination copy in place for
inspection, while the source stays unchanged. The next run refuses a non-empty
destination. After confirming the services are restored and `/var/lib/ipfs` is
healthy, move that failed destination aside on the same mounted volume before
retrying; never move or remove `/var/lib/ipfs`:

```bash
sudo test -d /var/lib/ipfs
sudo mv /mnt/volume_nyc3_01/ipfs \
  "/mnt/volume_nyc3_01/ipfs.failed.$(date -u +%Y%m%dT%H%M%SZ)"
```

To revert a migration that already completed successfully, use the preflight
snapshot and restore Kubo before SDN:

```bash
sudo systemctl stop space-data-network.service
sudo systemctl stop kubo.service
if sudo test -e /root/kubo-volume-migration-preflight/drop-in.absent; then
  sudo rm -f /etc/systemd/system/kubo.service.d/20-volume-repo.conf
else
  sudo cp -a /root/kubo-volume-migration-preflight/drop-in.conf \
    /etc/systemd/system/kubo.service.d/20-volume-repo.conf
fi
sudo systemctl daemon-reload

if test "$(sudo cat /root/kubo-volume-migration-preflight/kubo.state)" = "active"; then
  sudo systemctl start kubo.service
else
  sudo systemctl stop kubo.service
fi
if test "$(sudo cat /root/kubo-volume-migration-preflight/sdn.state)" = "active"; then
  sudo systemctl start space-data-network.service
else
  sudo systemctl stop space-data-network.service
fi

sudo systemctl show kubo.service --property=Environment --value
sudo systemctl --no-pager --full status kubo.service space-data-network.service
sudo test -d /var/lib/ipfs
```

This rollback changes only the service configuration and service states. It
does not delete either repository; `/var/lib/ipfs` remains the authoritative
rollback copy.
