# Installing a Space Data Network node

This is the operator path for a fresh node: what to install, which ports to
open, what to change in the generated config before the node is reachable,
how to keep the keys, how to mirror a publisher, and how to publish and
archive. It describes the node as built from `main` on 2026-09-04. Where the
public release still lags the source, the guide says so.

## 1. Get a build

Until the next public release is cut, build from source. The one-line
installers (`curl https://spacedatanetwork.org/install.sh | bash`,
`irm https://spacedatanetwork.org/install.ps1 | iex`) pick the newest
`v<semver>` node release on GitHub; the newest one today predates the
FlatBuffer dashboard and the sync, export and archive lanes.

```sh
git clone https://github.com/DigitalArsenal/space-data-network.git
cd space-data-network
npm run install:wasmedge      # WasmEdge 0.16.4 runtime the Go server links against
npm run server:build          # Go 1.25; produces sdn-server/spacedatanetwork
```

A plain clone is enough. The dashboard is embedded in the binary; its source
(`sdn-js/spaceaware-ui`) is a private submodule that clones skip.

The daemon loads `hd-wallet-wasi.wasm` for its HD identity. From a source
tree it finds the copy under `sdn-js/node_modules/hd-wallet-wasm/dist/`
(after `npm ci` in `sdn-js`); a self-contained bundle carries it under
`runtime/modules/`. Put it at `/usr/local/lib/hd-wallet-wasi.wasm` if you
run the bare binary from somewhere else.

## 2. Kubo

Content identifiers, pinning, dataset publication and archive restore all go
through a Kubo (go-ipfs) daemon. The node does not start Kubo for you today;
run it as its own service.

- Kubo 0.39 or newer.
- Put its RPC API on `127.0.0.1:5002`. The node's admin listener defaults to
  `127.0.0.1:5001`, the same port Kubo's API defaults to; one of them has to
  move, and moving Kubo keeps every SDN default valid.
- Set `admin.ipfs_api_url: http://127.0.0.1:5002` and, if you want the
  dashboard to open archive assets, `admin.ipfs_gateway_url:
  http://127.0.0.1:8080`. The node proxies `/ipfs/*` to that gateway for
  content it already holds only; it never fetches arbitrary CIDs for callers.
- If you expose Kubo's own gateway anywhere, set `Gateway.NoFetch=true` on it.

## 3. Initialise and edit the config

```sh
spacedatanetwork init --config /etc/space-data-network/config.yaml
```

`init` derives the node's HD identity, seals the mnemonic to this machine and
writes a complete config. Edit these before binding anything non-loopback:

| Key | Set it to |
|---|---|
| `storage.path` | a directory on the disk that will hold the records |
| `storage.max_size` | below the free space on that disk (the node refuses writes under 5 GiB free) |
| `storage.kubo_repo_path` | your Kubo repo, or delete the line |
| `admin.listen_addr` | `127.0.0.1:5001` for a loopback dashboard; a routable address only with TLS and authentication on |
| `admin.require_auth` | `true` (the default). Leave it on. |
| `admin.tls_mode` / `admin.tls_hosts` | `managed` plus your hostname for a public dashboard |
| `admin.ipfs_api_url` | the Kubo API from step 2 |
| `status.allowed_origins` | your own origin, or delete the line |
| `network.listen` | keep the defaults unless a port is taken (see step 4) |

Sign in to the dashboard with the node's own root key, or seed operators
under `users:` with their `signing_pubkey_hex`. A node whose root identity is
bound never mints an admin for a visiting wallet.

Do not run the daemon as root.

## 4. Ports

| Port | Protocol | Purpose |
|---|---|---|
| 4001 | tcp and udp (QUIC) | libp2p |
| 8080 | tcp | libp2p over WebSocket (browser peers) |
| 4003 | udp | libp2p WebRTC-direct |
| 5001 | tcp, loopback | admin API and dashboard (default) |
| 443 / 80 | tcp | only with `admin.tls_mode: managed` |

Open 4001, 8080 and 4003 inbound. Keep 5001 on loopback unless TLS and
authentication are on.

## 5. Keys

- `init` seals the mnemonic to this machine. Export it once and keep the
  export off the box: `spacedatanetwork key export --format backup`.
- Keep the Kubo peer identity recoverable: `spacedatanetwork key escrow create`.
- If you use `SDN_KEY_PASSWORD_FILE`, keep that file out of the box's backups.

## 6. Run

```sh
spacedatanetwork start    # persistent background service
spacedatanetwork daemon   # foreground
spacedatanetwork status   # local node summary
```

Probes on the admin listener: `GET /health` answers `ok` while the process
serves; `GET /ready` answers `ready`, or `503 not ready: <reason>` while the
store is linking, busy rebuilding, or the libp2p host is down; `GET /metrics`
is Prometheus text and needs an operator session.

## 7. Mirror a publisher

Add the publisher to both lists; trust alone never dials.

```yaml
peers:
  trusted_peers:
    - 16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U   # celestrak.eth
network:
  bootstrap:
    - /ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U
```

The publisher needs no configuration. Once connected, the node materialises
the publisher's signed dataset publications; the dashboard's SOURCES page
lists them by origin with SYNC, EXPORT and ARCHIVE per lane.

## 8. Publish your own records

Records are size-prefixed FlatBuffers of a Space Data Standards schema.

```sh
# a batch for one lane
curl -X POST 'http://127.0.0.1:5001/api/v1/data/publish/batch/OMM.fbs?provider_id=<you>&source_name=<lane>&batch_id=<id>' \
  -H 'Content-Type: application/vnd.sdn.flatbuffers.stream' --data-binary @records.sdn
```

The publish lane needs an operator session, or a separate loopback socket for
a pipeline on the same host: set `publishing.local_publish_addr:
127.0.0.1:5011` and post to that address instead. The default quota is
100 MB per provider (`publishing.default_quota_bytes`).

To publish the lane to the network as a signed dataset (shard, index,
manifest, announcement), from the box:

```sh
curl -X POST http://127.0.0.1:5001/api/v1/admin/dataset-updates/publish \
  -H 'Content-Type: application/json' \
  -d '{"schema":"OMM.fbs","providerId":"<you>","sourceName":"<lane>"}'
```

or configure `publishing.auto_publish` lanes, which fire for records ingested
by modules. Verify with `/api/v1/log/OMM.fbs/heads` and `ipfs pin ls`.

## 9. Archive and restore

With an operator session:

- `POST /api/v1/archive` with a `$QRP` request (schema, provider, source)
  writes a signed archive: shard, index and manifest, pinned in Kubo.
- `GET /api/v1/archives` lists them; each frame is the manifest and the
  `X-SDN-Archive-CIDs` header names them by CID.
- `POST /api/v1/archive/import` with the manifest CID re-imports one.

The dashboard's source page has the same three actions. Record the manifest
CIDs somewhere durable: nothing announces archives to other nodes yet, so
after a total disk loss a node recovers only what its trusted publishers
still hold.

## 10. Updates

Self-update needs the self-contained bundle layout with
`trust/update-roots.json`; bundles built by the release tooling carry the
fleet roots. The feed at `https://sdn.spaceaware.io/updates` publishes the
`beta` channel for linux/amd64 only; other platforms and a `stable` channel
are pending.

## Known gaps on this date

- Kubo is not supervised by the node.
- Archives are not announced, and there is no restore-from-network.
- Publishing has no dashboard or CLI front; it is the API above.
- Subscription retention (replace current set by default, archive on request)
  is landing; until then every publication accumulates.
