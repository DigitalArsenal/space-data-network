# CelesTrak Provider Node Deployment

This directory contains host-specific, non-secret deployment assets for
`space-data-network-02` / `celestrak.eth`.

The SSH host alias, address, and operator keys stay in `~/.ssh/config` and on
the target host. Do not commit production mnemonics, Space-Track credentials, or
private Kubo/SDN key material.

## Target

- SSH alias: `celestrak.eth` or `space-data-network-02`
- Role: SDN full node, CelesTrak public provider ingest worker, local Kubo node
- Production seed:
  `/ip4/104.131.11.220/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`

## Install Shape

The CelesTrak host runs three systemd units:

- `spacedatanetwork.service`: SDN full node and public/API surface
- `spacedatanetwork-ingest.service`: CelesTrak ingest worker
- `kubo.service`: local Kubo RPC/gateway node used by SDN WebUI and publication helpers

Kubo intentionally uses non-default local RPC ports so it does not collide with
the SDN admin/API listener:

- Kubo RPC: `127.0.0.1:5002`
- Kubo gateway: `127.0.0.1:8081`
- Kubo swarm: TCP/UDP `4002`

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
systemctl enable --now kubo spacedatanetwork spacedatanetwork-ingest
```

## Verification

```sh
systemctl --no-pager --full status kubo spacedatanetwork spacedatanetwork-ingest
curl -fsS http://127.0.0.1:5001/api/node/info
curl -fsS http://127.0.0.1:5002/api/v0/id -X POST
ss -ltnup | grep -E ':4001|:4002|:5001|:5002|:8080|:8081'
journalctl -u spacedatanetwork -n 200 --no-pager
journalctl -u spacedatanetwork-ingest -n 200 --no-pager
```

If `ss` shows `ipfs` listening on `4001`, Kubo was started before the CelesTrak
port config was applied. Restart Kubo through `kubo.service` after confirming no
coordinator deploy is in progress; the checked-in config expects Kubo swarm on
`4002` so SDN can own `4001`.

The ingest worker is configured for CelesTrak-only fetches by default. Enable
Space-Track gap-fill only through a private systemd drop-in that sets
`SPACETRACK_IDENTITY` and `SPACETRACK_PASSWORD`, then changes the service
argument to `--spacetrack-enabled true`.
