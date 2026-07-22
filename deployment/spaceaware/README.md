# sdn.spaceaware.io Deployment

This directory contains the production target config for the first SDN
module-delivery and OrbPro licensing provider node.

The deployment config uses `host: sdn.spaceaware.io` on purpose. Keep the real
address and key material in `~/.ssh/config`; do not commit machine-specific SSH
paths or IP addresses here.

The production edge terminates TLS for `spaceaware.io`, `www.spaceaware.io`,
and `sdn.spaceaware.io` in Nginx. The first-stage
`install-public-host-route.mjs` keeps SpaceAware on the local Kubo gateway at
port 5020 while routing the SDN homepage, callback, and other non-IPFS paths to
the SDN HTTPS listener at port 18443. SDN WebSocket upgrades use port 18080.

After a signed SpaceAware external-proxy release has been installed, the
separate `cutover-spaceaware` command splits the shared TLS server into two
host-specific server blocks. SpaceAware and www then use the SpaceAware SDN
HTTP service at `127.0.0.1:5010`, WebSocket upgrades on `/` and `/p2p/*` use
`127.0.0.1:8080`, and terrain paths use `127.0.0.1:8081`. The SDN host remains
on ports 18443 and 18080. The private `/asset-ipfs/` route and immutable CID
cache behavior are retained on both hosts.

Both installers apply their change atomically, require a reviewed source
shape, validate Nginx before reload, and restore the original config if
validation or reload fails. Durable backups go under
`/var/backups/spacedatanetwork/nginx`, outside Nginx's `sites-enabled/*`
include. A retry validates and reloads even when the desired file is already
installed, so an interruption between replacement and reload is recoverable.
The installers serialize through `/run/sdn-public-host-route.lock`, recheck the
config immediately around replacement and reload, and never roll back over a
concurrent operator edit.
The final cutover is pinned to the reviewed production source digest and its
deterministic canonical split digest; any manual byte drift fails closed for
operator review instead of being partially transformed.

The final cutover additionally runs
`verify-spaceaware-public-host-route.mjs` after reload while the original file
is still available for rollback. The verifier connects directly to
`127.0.0.1:443` with the production SNI and Host values, bypassing external
DNS/CDN state. It requires the public SpaceAware release identity and callback
bytes to match `/opt/spaceaware/current/web`, validates callback method and
security headers, parses the health and provider JSON contracts, checks both
terrain prefixes, compares each public provider key and peer ID with its direct
5010/18443 backend, and completes RFC 6455 handshakes through both public hosts.
Any failure restores, validates, and reloads the pre-cutover Nginx file.

The normal binary deploy invokes only the first-stage SDN installer, and only
when the exact `deployment/spaceaware/servers.yaml` target is selected. The
SpaceAware cutover is an explicit second command. It refuses to run unless all
four SpaceAware units are loaded and active and direct loopback probes for
ports 5010, 8080, and 8081 pass. The loopback gate requires the exact activated
release identity, structured health/provider responses, and a valid WebSocket
handshake. It also requires `wallet.spacedatanetwork.org` to serve the HD
Wallet login surface and fetches all three activated 2.0.28 wallet assets from
`static.spacedatanetwork.org`, accepting them only when their SHA-384 bytes
match the SRI values in the activated callback and OrbPro HTML. Accepting any
arbitrary HTTP status is not sufficient. It does not restart those services.

From the `space-data-network` repository root:

```bash
./deployment/scripts/deploy.sh \
  -c deployment/spaceaware/servers.yaml \
  -u root \
  -b \
  deploy full
```

Once the SpaceAware release is active in external-proxy mode, cut its public
hosts over without redeploying or restarting either application:

```bash
./deployment/scripts/deploy.sh \
  -c deployment/spaceaware/servers.yaml \
  -u root \
  cutover-spaceaware
```

Use `-n` first to confirm the exact target. The cutover command installs the
reviewed readiness wrapper and route installer under
`/opt/spacedatanetwork/deployment/spaceaware/`, together with the shared
verifier; no route file is changed until all readiness checks pass.

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

test "$(sudo systemctl show --property=LoadState --value ipfs.service)" = "loaded"
command -v xargs >/dev/null
KUBO_ENVIRONMENT="$(sudo systemctl show --property=Environment --value ipfs.service)"
case "$KUBO_ENVIRONMENT" in
  *$'\n'*|*$'\r'*)
    unset KUBO_ENVIRONMENT
    printf 'Malformed serialized environment for ipfs.service\n' >&2
    exit 1
    ;;
esac
if ! KUBO_ENVIRONMENT_ASSIGNMENTS="$(
  printf '%s\n' "$KUBO_ENVIRONMENT" |
    xargs -n 1 printf '%s\n' 2>/dev/null
)"; then
  unset KUBO_ENVIRONMENT
  printf 'Malformed serialized environment for ipfs.service\n' >&2
  exit 1
fi
SOURCE_IPFS_PATH_ASSIGNMENTS="$(
  printf '%s\n' "$KUBO_ENVIRONMENT_ASSIGNMENTS" |
    awk '/^IPFS_PATH=/ { print }'
)"
test "$(printf '%s\n' "$SOURCE_IPFS_PATH_ASSIGNMENTS" | awk 'NF { count++ } END { print count + 0 }')" = "1"
test "$SOURCE_IPFS_PATH_ASSIGNMENTS" = "IPFS_PATH=/var/lib/ipfs"
unset KUBO_ENVIRONMENT KUBO_ENVIRONMENT_ASSIGNMENTS SOURCE_IPFS_PATH_ASSIGNMENTS
sudo systemctl --no-pager --full status ipfs.service space-data-network.service
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
if sudo test -e /etc/systemd/system/ipfs.service.d/20-volume-repo.conf; then
  sudo cp -a /etc/systemd/system/ipfs.service.d/20-volume-repo.conf \
    /root/kubo-volume-migration-preflight/drop-in.conf
else
  sudo touch /root/kubo-volume-migration-preflight/drop-in.absent
fi
if sudo systemctl is-active --quiet ipfs.service; then
  printf 'active\n' | sudo tee /root/kubo-volume-migration-preflight/ipfs.state >/dev/null
else
  printf 'inactive\n' | sudo tee /root/kubo-volume-migration-preflight/ipfs.state >/dev/null
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
`space-data-network.service` before `ipfs.service`, copies with
`rsync -aHAX --numeric-ids`, sets
`Datastore.StorageMax` to `120GB`, and installs both `IPFS_PATH` and an additive
Kubo `ReadWritePaths` entry for the destination. The copy never uses
`--delete`, and the script never writes, changes ownership, or removes anything
under `/var/lib/ipfs`.

Before any mutation, the script also requires `ipfs.service` to be loaded and
quote/escape-aware parsing of its serialized effective `Environment` to yield
exactly one `IPFS_PATH=` assignment whose decoded value is `/var/lib/ipfs`.
Unrelated assignments, including quoted values containing spaces, are allowed.
A missing unit, malformed serialization, or a missing, duplicated, or
mismatched `IPFS_PATH` fails closed without stopping a service or changing the
source, destination, or drop-in. The failure message reports only whether the
effective `IPFS_PATH` is missing, ambiguous, or mismatched; it does not print
the unit's full environment.

After Kubo starts, the script gives the API and gateway up to 30 attempts with
a one-second delay between attempts (and bounded per-request timeouts). API
readiness is required before destination identity and pin checks; the gateway
check follows those checks, and SDN starts only after all of them pass.

### Verification

Confirm the live unit environment, repository configuration, services, API,
and one deterministic gateway sample:

```bash
set -o pipefail
command -v xargs >/dev/null
test "$(findmnt --noheadings --mountpoint /mnt/volume_nyc3_01 --output TARGET)" = "/mnt/volume_nyc3_01"
sudo systemctl show ipfs.service --property=Environment --value |
  xargs -n 1 printf '%s\n' 2>/dev/null |
  grep -Fx 'IPFS_PATH=/mnt/volume_nyc3_01/ipfs'
sudo systemctl show ipfs.service --property=ReadWritePaths --value |
  xargs -n 1 printf '%s\n' 2>/dev/null |
  grep -Fx '/mnt/volume_nyc3_01/ipfs'
for ROOT_OWNED_PATH in \
  /opt/spacedatanetwork \
  /opt/spacedatanetwork/deployment \
  /opt/spacedatanetwork/deployment/spaceaware \
  /opt/spacedatanetwork/deployment/spaceaware/migrate-kubo-repo.sh; do
  test "$(sudo stat -c '%U:%G %a' "$ROOT_OWNED_PATH")" = "root:root 755"
done
test "$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs config Datastore.StorageMax)" = "120GB"
sudo systemctl is-active --quiet ipfs.service
sudo systemctl is-active --quiet space-data-network.service

DESTINATION_PEER_ID="$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs id --format='<id>')"
DESTINATION_PIN_COUNT="$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs pin ls --type=recursive --quiet | LC_ALL=C sort -u | awk 'NF { count++ } END { print count + 0 }')"
test "$DESTINATION_PEER_ID" = "$SOURCE_PEER_ID"
test "$DESTINATION_PIN_COUNT" -ge "$SOURCE_PIN_COUNT"

curl -fsS --max-time 10 --request POST http://127.0.0.1:5002/api/v0/id >/dev/null
SAMPLE_CID="$(sudo env IPFS_PATH=/mnt/volume_nyc3_01/ipfs ipfs pin ls --type=recursive --quiet | LC_ALL=C sort -u | head -n 1)"
test -n "$SAMPLE_CID"
curl -fsSIL --max-time 20 "http://127.0.0.1:8091/ipfs/${SAMPLE_CID}" >/dev/null
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
sudo systemctl stop ipfs.service
if sudo test -e /root/kubo-volume-migration-preflight/drop-in.absent; then
  sudo rm -f /etc/systemd/system/ipfs.service.d/20-volume-repo.conf
else
  sudo cp -a /root/kubo-volume-migration-preflight/drop-in.conf \
    /etc/systemd/system/ipfs.service.d/20-volume-repo.conf
fi
sudo systemctl daemon-reload

if test "$(sudo cat /root/kubo-volume-migration-preflight/ipfs.state)" = "active"; then
  sudo systemctl start ipfs.service
else
  sudo systemctl stop ipfs.service
fi
if test "$(sudo cat /root/kubo-volume-migration-preflight/sdn.state)" = "active"; then
  sudo systemctl start space-data-network.service
else
  sudo systemctl stop space-data-network.service
fi

sudo systemctl show ipfs.service --property=Environment --value
sudo systemctl --no-pager --full status ipfs.service space-data-network.service
sudo test -d /var/lib/ipfs
```

This rollback changes only the service configuration and service states. It
does not delete either repository; `/var/lib/ipfs` remains the authoritative
rollback copy.

## Production rollout record — 2026-07-14 UTC

The guarded Kubo migration and GitHub OIDC asset-pin canary completed on
2026-07-14 UTC with SDN component commit
`b50e30505cf82334f563006fab0302cc1087c4fc` deployed.

- Kubo moved from `/var/lib/ipfs` to
  `/mnt/volume_nyc3_01/ipfs`; `/var/lib/ipfs` remains intact as the rollback
  copy.
- The recursive pin set was 2,081 before migration and 2,081 immediately
  after migration, with zero missing pins. The verified canary increased the
  live set to 2,082.
- The Kubo peer-ID SHA-256 digest was
  `f44e506fb87f5a54732778189d6f35829e638a47edbbf715abbf167959d94b45`.
  The raw peer ID is intentionally omitted from this deployment record.
- The effective Kubo repository path is the mounted volume, its storage cap is
  `120GB`, and both `ipfs.service` and `space-data-network.service` were active
  after independent API and gateway checks.
- The live SDN config uses `mode: full` because the asset reference ledger
  requires local FlatSQL storage. Asset pinning is enabled only for the exact
  `DigitalArsenal/asset-models` main-branch pin and decision workflows.
- Unauthenticated POSTs to `/api/v1/assets/pin` and
  `/api/v1/assets/reference-state` returned the bounded JSON `401` response,
  and both OpenAPI operations advertised only `githubOIDC` security.

The generated one-triangle GLB canary ran in GitHub Actions run
`29320830677` against asset-models commit
`59582effebdece854bbb5ab9bd601b864ea81b9f` and issue `#25`.

- CID: `bafkreiawliivuqf4nhinllryxnnpll5ckdipak2phi6bvo7de7idnzb2vq`
- SHA-256: `165a115a40bc69d0d5ae38bb5af5afa250d0f02b4f3a3c1abbe327d036e43aac`
- Byte length: `476`
- Ledger lifecycle: `staged` to `review_open`
- Audit results: `asset_pin_upload=pinned` and
  `asset_reference_state=review_open`

The public and local gateways returned the exact expected bytes, Kubo reported
the CID as a recursive pin, and the journal contained two distinct one-time
OIDC receipts with the expected repository, ref, workflow, run, attempt, and
commit provenance. No JWT or raw token material was recorded.
