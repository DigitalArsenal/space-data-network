# CelesTrak Provider Node Deployment

> **STALE, 2026-07-28 — DO NOT USE FOR INSTALL.** This directory describes a
> two-daemon shape (`spacedatanetwork.service` + `kubo.service`) that
> `install-host.sh` would install on the same box. That now VIOLATES owner law
> ("never ever have more than one instance running on a box"). The box's live,
> canonical shape is `deployment/systemd/sdn-retriever.service` — ONE daemon,
> escrowed+disabled legacy units — recorded in `deployment/topology.json` under
> `cluster["celestrak.eth"]`. Treat this file as historical background only
> until it is rewritten to match.

This directory contains host-specific, non-secret deployment assets for
`space-data-network-02` / `celestrak.eth`.

The SSH host alias, address, and operator keys stay in `~/.ssh/config` and on
the target host. Do not commit production mnemonics, Space-Track credentials, or
private Kubo/SDN key material.

## Target

- SSH alias: `celestrak.eth` or `space-data-network-02`
- Role: SDN full node, CelesTrak public provider ingest worker, local Kubo node
- Production seed:
  `/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`
  (direct pinned IP multiaddrs are authoritative until `_dnsaddr.bootstrap.spacedatanetwork.org` TXT records are published)

## Install Shape

The CelesTrak host runs two systemd units:

- `spacedatanetwork.service`: SDN full node, public/API surface, AND the
  CelesTrak ingest workers (config `ingest.enabled: true` — see below)
- `kubo.service`: local Kubo RPC/gateway node used by SDN WebUI and publication helpers

There is intentionally NO separate ingest unit anymore. The FlatSQL v2 store
(in-process engine + `control.sdnj` statement journal) is single-writer: a
second writer process opening the daemon's storage path fails with a clean
store-lock error (`store.lock` flock next to `control.sdnj`). The former
`spacedatanetwork-ingest.service` topology — a second process on the same
store — would corrupt the journal and is removed; `install-host.sh` disables
and deletes it on upgraded hosts. The standalone `spacedatanetwork ingest`
verb remains for offline stores only (see
`sdn-server/deploy/spacedatanetwork-ingest.service.standalone-example`).

The private closed-module checkout is loaded separately at
`/opt/spacedatanetwork/closed-modules`. It contains the scheduler-facing
CelesTrak provider and publisher CLIs, but production secrets and host adapters
must still come from private drop-ins or the host secret manager.

Kubo intentionally uses non-default local RPC ports so it does not collide with
the SDN admin/API listener:

- Kubo RPC: `127.0.0.1:5002`
- Kubo gateway: `127.0.0.1:8081`
- Kubo swarm: TCP/UDP `4002`
- SDN WebRTC-direct transport: UDP `4003`

The SDN full node listens on:

- libp2p TCP/QUIC: `4001`
- libp2p WebSocket: `8080`
- admin/API HTTP: `5001`

## Manual Bring-Up

From a clean checkout, copy the built SDN server, UI assets, and this directory
to the host. Avoid `deployment/scripts/deploy.sh` while another agent owns a
dirty patch there.

```sh
ssh root@celestrak.eth 'mkdir -p /opt/spacedatanetwork/source'
rsync -a sdn-server scripts sdn-js/ui/dist webui/build deployment/celestrak \
  root@celestrak.eth:/opt/spacedatanetwork/source/
ssh root@celestrak.eth 'bash /opt/spacedatanetwork/source/deployment/celestrak/install-host.sh'
```

The installer also accepts the flattened rsync layout where
`/opt/spacedatanetwork/source/celestrak`, `/opt/spacedatanetwork/source/dist`,
and `/opt/spacedatanetwork/source/build` exist.

If building directly on the VM, install Go, build essentials, and run:

```sh
cd /opt/spacedatanetwork/source/sdn-server
WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge \
  /opt/spacedatanetwork/source/scripts/go-with-wasmedge.sh \
  build -o /opt/spacedatanetwork/bin/spacedatanetwork ./cmd/spacedatanetwork
```

Then start services:

```sh
systemctl daemon-reload
systemctl enable --now kubo spacedatanetwork
```

## Closed Module Load

Install Node/npm once on the CelesTrak host, then load the private module repo.
If the host has GitHub credentials for the private repository, prefer a normal
clone or pull:

```sh
ssh celestrak.eth 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs npm'
ssh celestrak.eth 'cd /opt/spacedatanetwork && git clone https://github.com/DigitalArsenal/space-data-network-closed-modules.git closed-modules'
ssh celestrak.eth 'chown -R sdn:sdn /opt/spacedatanetwork/closed-modules'
```

If the host does not have private GitHub credentials, create a bundle from the
already-pushed local checkout and clone that bundle on the host:

```sh
cd repos/main-packages/space-data-network-closed-modules
git bundle create /tmp/sdn-closed-modules.bundle main
scp /tmp/sdn-closed-modules.bundle celestrak.eth:/tmp/sdn-closed-modules.bundle
ssh celestrak.eth 'rm -rf /opt/spacedatanetwork/closed-modules && git clone -b main /tmp/sdn-closed-modules.bundle /opt/spacedatanetwork/closed-modules'
ssh celestrak.eth 'cd /opt/spacedatanetwork/closed-modules && git remote remove origin && git remote add origin https://github.com/DigitalArsenal/space-data-network-closed-modules.git'
ssh celestrak.eth 'chown -R sdn:sdn /opt/spacedatanetwork/closed-modules'
```

Smoke the loaded module with non-secret public-source config:

```sh
ssh celestrak.eth 'cd /opt/spacedatanetwork/closed-modules && npm test'
ssh celestrak.eth 'cd /opt/spacedatanetwork/closed-modules && env CELESTRAK_PROVIDER_CRON="0 */3 * * *" CELESTRAK_SOURCES="spaceWeather" SDN_PROVIDER_ID="celestrak.eth" node packages/celestrak-provider/bin/run-provider.mjs --run'
```

The daemon config's `ingest` section (checked-in `config.yaml`) is the
production three-hour CelesTrak pull path, running inside
`spacedatanetwork.service`. It posts successful OMM, CAT, and SPW syncs to
the local SDN admin publication endpoint at
`/api/v1/admin/dataset-updates/publish`, where the same daemon exports the
FlatSQL window, pins shard/index/DPM assets to local Kubo, signs the PNM, and
fans it out over SDN pub/sub. Full-catalog publication is chunked into
multi-shard DPM series instead of treating one PNM as the whole accumulated
catalog. Subscribers replay stored trusted-provider `PNM.fbs` records on a
timer so missed high-volume pub/sub chunks can be fetched and materialized after
the burst.

## Verification

```sh
systemctl --no-pager --full status kubo spacedatanetwork
curl -fsS http://127.0.0.1:5001/api/node/info
curl -fsS http://127.0.0.1:5002/api/v0/id -X POST
ss -ltnup | grep -E ':4001|:4002|:5001|:5002|:8080|:8081'
journalctl -u spacedatanetwork -n 200 --no-pager
journalctl -u spacedatanetwork -n 200 --no-pager | grep -i 'ingest'
```

In-daemon ingest logs under the `ingest` logger inside the daemon journal;
`In-daemon ingest enabled` at boot confirms the config took effect.

If `ss` shows `ipfs` listening on `4001`, Kubo was started before the CelesTrak
port config was applied. Restart Kubo through `kubo.service` after confirming no
coordinator deploy is in progress; the checked-in config expects Kubo swarm on
`4002` so SDN can own `4001`.

In-daemon ingest is configured for CelesTrak-only fetches by default
(`spacetrack_enabled: false`, `udl_enabled: false` in `config.yaml`). Enable
Space-Track gap-fill only through a private systemd drop-in on
`spacedatanetwork.service` that sets `SPACETRACK_IDENTITY` and
`SPACETRACK_PASSWORD`, plus `spacetrack_enabled: true` in the config's
`ingest` section.

Confirm the live publication hook is present before treating the provider as
subscriber-ready:

```sh
grep dataset_publish_url /etc/spacedatanetwork/config.yaml
journalctl -u spacedatanetwork -n 200 --no-pager | grep 'Dataset publication requested'
journalctl -u spacedatanetwork -n 200 --no-pager | grep 'Dataset publication API available'
```

Confirm subscriber catch-up on each subscriber node before treating the network
as synchronized:

```sh
journalctl -u space-data-network -n 300 --no-pager | grep 'Materialized trusted dataset update'
journalctl -u space-data-network -n 300 --no-pager | grep 'Dataset publication PNM catch-up materialized'
curl -fsS 'https://sdn.spaceaware.io/api/v1/data/omm/bulk?limit=1&format=json'
curl -fsS 'http://127.0.0.1:10080/api/v1/data/omm/bulk?limit=1&format=json'
```

## CelesTrak Source Controls

The checked-in config uses public CelesTrak sources and does not require
private credentials:

- GP full catalog:
  `https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv`
- SATCAT legacy text:
  `https://celestrak.org/pub/satcat.txt`
- SATCAT raw CSV:
  `https://celestrak.org/pub/satcat.csv`
- Space weather:
  `https://celestrak.org/SpaceData/SW-All.csv`

For readiness tests or source migrations, override these in the daemon
config's `ingest` section:

```yaml
ingest:
  celestrak_catalog_url: ...
  celestrak_satcat_url: ...
  celestrak_satcat_csv_url: ...
  celestrak_space_weather_url: ...
  space_weather_interval: 3h
```

Keep GP and space-weather intervals at or above the CelesTrak-safe minimum
enforced by the SDN ingest runner. Faster production polling requires a private
provider agreement and should be recorded in the host runbook, not in this
public deployment directory.

## Historical `/opt/data` Archive Publication

The historical OMM archive at `/opt/data/satellite_data.db` is too large to
materialize into the current 48 GB CelesTrak provider volume. Publish it as
immutable FlatBuffer artifacts from the machine that has `/opt/data`, then
register the compact plan on `celestrak.eth` so the provider signs the DPM/PNM
metadata with its own node identity.

Local artifact export, using a local Kubo RPC endpoint:

```sh
spacedatanetwork import-legacy-sqlite \
  --source-db /opt/data/satellite_data.db \
  --source-table satellite_data \
  --publish-artifacts-only \
  --publication-plan-only \
  --publication-plan-output /opt/data/celestrak-historical-omm-plan.json \
  --ipfs-api-url "$LOCAL_IPFS_API_URL" \
  --storage-path /opt/data/sdn-historical-plan-state \
  --batch-size 50000 \
  --provider-id space-data-network-02 \
  --source-name celestrak-gp-historical \
  --source-peer source:legacy-sqlite \
  --publication-provider-peer-id "$CELESTRAK_PEER_ID" \
  --publication-provider-epm-cid "$CELESTRAK_EPM_CID" \
  --publication-dataset-id sdn-omm-celestrak-gp-historical
```

Copy only the generated plan JSON to the provider, then register it there:

```sh
spacedatanetwork --config /etc/spacedatanetwork/config.yaml \
  dataset-publications register-plan \
  --plan-file /opt/spacedatanetwork/import/celestrak-historical-omm-plan.json
```

The registration step signs and pins DPM manifests on the provider, stores
PNMs and shard-publication metadata in an isolated SDN datastore namespace, and
does not insert historical `OMM` rows into the provider FlatSQL database. The
shard and index CIDs remain normal IPFS content; pin them on enough artifact
seed peers before treating the historical feed as highly available.
