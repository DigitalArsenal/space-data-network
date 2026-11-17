# Installing a Space Data Network node

This is the operator path for a fresh node: what to install, which ports to
open, what to change in the generated config before the node is reachable,
how to keep the keys, how to mirror a publisher, and how to publish and
archive. It describes v1.1.7 (2026-11-17).

## 1. Get a build

```sh
# macOS or Linux
curl -fsSL https://spacedatanetwork.org/install.sh | bash
# Windows PowerShell
irm https://spacedatanetwork.org/install.ps1 | iex
```

The installers resolve the newest node release on GitHub (today
v1.1.7: macOS Apple Silicon and Intel, Linux x86-64 and arm64, and
Windows x86-64), verify the checksum,
unpack the self-contained bundle under `~/.spacedatanetwork/bundles`, link
`spacedatanetwork` and `sdn` under `~/.spacedatanetwork/bin`, and initialize
the node identity.

**Linux as a regular user: the key password file.** A node with no key
password seals its identity under a key derived from the machine and the
account it runs as. On most Linux machines the kernel makes
`/sys/class/dmi/id/product_uuid` readable only by root, so for any other
account that derivation is refused (`spacedatanetwork key fingerprint-check`
answers `refused`; root, macOS and Windows answer `ok` or `absent`). For a
fresh node on such a host, an `install.sh` that carries this change (v1.1.4
and earlier do not: there, make the file yourself as in section 5) generates
`~/sdn-key-password` (32 random bytes as 64 hex characters, mode 0400),
records it in the node's config as `security.key_password_file`, and seals
the new identity under it, so `daemon`, `start` and every CLI command find it
with nothing exported. It generates nothing when `SDN_KEY_PASSWORD` or
`SDN_KEY_PASSWORD_FILE` is set, when the node already has an identity (an
upgrade leaves it exactly as it is), or when the fingerprint is not refused,
and it never overwrites an existing file. The file is kept outside
`~/.spacedatanetwork` on purpose: a copy of that directory alone (a backup,
a snapshot, a synced folder) does not open the identity or the stored
credentials, and anyone holding both does. **Keep the file for the life of
the node and back it up apart from `~/.spacedatanetwork`**: without it the
node cannot open its identity.

Other platforms build from source:

```sh
git clone https://github.com/DigitalArsenal/space-data-network.git
cd space-data-network
npm run install:wasmedge      # WasmEdge 0.16.4 runtime the Go server links against
npm run server:build          # Go 1.25; produces sdn-server/spacedatanetwork
```

A plain clone is enough. The dashboard is embedded in the binary; its source
(`sdn-js/spaceaware-ui`) is a private submodule that clones skip.

A source build loads `hd-wallet-wasi.wasm` for its HD identity from
`sdn-js/node_modules/hd-wallet-wasm/dist/` (after `npm ci` in `sdn-js`), or
from `/usr/local/lib/hd-wallet-wasi.wasm`; a bundle carries it under
`runtime/modules/`.

### Desktop app

The desktop app (`space-data-network-desktop-<version>-<os>-<arch>` on the
release page) ships for Windows x64 as both an installer and a portable exe,
and for Linux x64 as an AppImage, a .deb and a .tar.xz. The macOS app, for
Apple silicon and Intel as a signed and notarized .dmg and .zip, is not in
the newest release; the newest release that carries it is v1.1.6. The app carries the
same bundle, starts the node on launch, opens the
node's own dashboard in its window, and keeps the store and keys under the
app's user-data directory. Its menu shows the node's recovery phrase once, on
request. Nothing below applies to the app except sections 4 (ports, if you want
to be reachable from the internet) and 5 (keys).

#### First launch

**macOS.** The desktop app is signed with an Apple Developer ID and notarized
by Apple, so it opens like any other downloaded app. The command-line
archive's binary is not notarized. Expanded with `tar` from a terminal it runs,
because `tar` does not carry the browser's quarantine flag onto the extracted
files. If macOS refuses it after some other way of unpacking, remove the flag:

    xattr -dr com.apple.quarantine spacedatanetwork-<version>-darwin-arm64

**Windows.** The Windows builds carry no Authenticode certificate, so Windows
refuses them on first launch. This is the signature's absence, not a finding
about the software. SmartScreen shows "Windows protected your PC"; click *More info*
→ *Run anyway*. If you downloaded the zip with a browser, clear its
mark-of-the-web before expanding, or the extracted exe may refuse to start:

    Unblock-File .\spacedatanetwork-<version>-windows-amd64.zip

A managed box with SmartScreen set to Block will refuse outright, and
Defender may take several seconds to scan a 340 MB executable before the
first output appears.

### Docker

```sh
docker run -d --name sdn --hostname sdn --restart unless-stopped \
  -v sdn-data:/app/data -p 127.0.0.1:5001:5001 -p 4001:4001 -p 4001:4001/udp \
  dockerdigitalarsenal/space-data-network:v1.1.7
```

The image (`linux/amd64`) carries the node, WasmEdge with a prewarmed AOT
engine, Kubo and the wallet sign-in assets; `config/full-docker.yaml` is baked
in as `/app/config/full-docker.yaml` and everything the node owns lives in the `/app/data`
volume (store, keys, auth, peers).

`--hostname` is required, and the node enforces it. With no
`SDN_KEY_PASSWORD` / `SDN_KEY_PASSWORD_FILE` set, the key-at-rest password is
derived from the container's hostname; Docker's auto-generated one is a fresh
container ID every time, so a recreated container could not open its own
identity. Rather than sealing into a name that will not come back, a first
start without `--hostname` refuses and prints the three ways forward — a fixed
hostname, a mounted password file, or `SDN_ALLOW_EPHEMERAL_HOSTNAME=1` for a
throwaway. Any stable value works; it is not a secret. A node already sealed
under an auto-generated hostname keeps running and warns on every start: re-seal
it with `spacedatanetwork key reseal` before recreating the container.

The container runs as uid 1000, so on most Linux hosts the machine-derived
key is refused there too (see section 1). **The recommended setup is a
password file mounted from outside the volume**, made with a umask so it is
never readable by anyone else, not even for a moment; it is what every image
needs on such a host up to v1.1.4, and still the better custody after:

```sh
sudo install -m 0400 -o 1000 -g 1000 /dev/null /etc/sdn-key-password
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' | sudo tee /etc/sdn-key-password >/dev/null
docker run -d --name sdn --hostname sdn --restart unless-stopped \
  -v /etc/sdn-key-password:/run/secrets/sdn-key-password:ro \
  -e SDN_KEY_PASSWORD_FILE=/run/secrets/sdn-key-password \
  -v sdn-data:/app/data -p 127.0.0.1:5001:5001 -p 4001:4001 -p 4001:4001/udp \
  dockerdigitalarsenal/space-data-network:v1.1.7
```

(`install` creates the file empty, already 0400 and owned by uid 1000; root
then writes the password into it.) An image that carries this change also
covers a fresh volume started with no key password at all: its entrypoint
generates `/app/data/key-password` (uid 1000, mode 0400), logs one line
naming it, and uses it on every later start; the identity sealed under it
does not depend on the hostname. It never overwrites that file, never touches
a volume that already holds an identity, and generates nothing when the
config keeps the key directory outside `/app/data` (the file must be kept and
lost with the identity). **That file is in the volume, beside the identity it
seals: a copy of the volume alone (a backup, a snapshot, `docker volume`
export) opens the node identity and its credential store.** Keep it for the
life of the node; to keep the two apart, use the mounted file above instead.

`docker exec` does not run the entrypoint, so a command started that way does
not get the generated file. Run commands through the entrypoint, which works
for every node (generated file, mounted secret or machine key):
`docker exec sdn
/app/entrypoint.sh show-identity --config /app/config/full-docker.yaml --show-mnemonic`
prints the recovery phrase. A `docker load` archive of the same image ships
with each release: `spacedatanetwork-container-1.1.7-linux-amd64.tar.gz`.
For a beta, that name spells the version with DOTS, not the tag's hyphen
(`X.Y.Z.beta.N`, not `X.Y.Z-beta.N`, which 404s). Native Linux packages use
the same dotted spelling, for the same reason: a release asset name cannot
carry SemVer's hyphen.

### Linux packages (.deb / .rpm)

`spacedatanetwork-full` installs the node under `/opt/spacedatanetwork`, its
config as `/etc/spacedatanetwork/config.yaml` and the unit
`spacedatanetwork.service`, which runs as the system user `sdn` with its data
in `/var/lib/spacedatanetwork`. The package creates the user and the
directory; it does not enable or start the service.

User `sdn` cannot read `/sys/class/dmi/id/product_uuid` on most hosts (see
section 1), so on a FRESH install a package that carries this change (v1.1.4
and earlier do not: there, make the two files yourself as below) generates
`/var/lib/spacedatanetwork/key-password` (`sdn:sdn`, mode 0400) and the drop-in
`/etc/systemd/system/spacedatanetwork.service.d/key-password.conf`
(`Environment=SDN_KEY_PASSWORD_FILE=/var/lib/spacedatanetwork/key-password`),
and the first `systemctl start spacedatanetwork` seals the new identity under
it. It does nothing when the unit already sets `SDN_KEY_PASSWORD` or
`SDN_KEY_PASSWORD_FILE`, when the config sets a key password, when the node
already has an identity (an upgrade over an existing node leaves it exactly
as it is), or when user `sdn` can read the fingerprint, and it never
overwrites an existing file. Removing or purging the package leaves the file
and the drop-in, like the identity they open.

- **The file is in the data directory, beside the identity it seals: a copy
  of `/var/lib/spacedatanetwork` alone opens the node identity and its
  credential store.** Keep the file for the life of the node and back it up
  apart from that directory. To keep the two apart on the box as well, move
  the file elsewhere (`sdn:sdn`, mode 0400; `/etc/spacedatanetwork/` is one
  place) and change the path in the drop-in.
- The file is made when the package is installed. A machine image built with
  the package installed therefore gives every clone the same password: on
  each clone, before the first start, remove the file and the drop-in and
  reinstall the package (`sudo apt-get install --reinstall
  spacedatanetwork-full`, or `sudo dnf reinstall spacedatanetwork-full`).
- The drop-in reaches the service only. CLI commands run beside it need the
  variable too (the node's refusal names the file and this command):

  ```sh
  sudo -u sdn env SDN_KEY_PASSWORD_FILE=/var/lib/spacedatanetwork/key-password \
    /opt/spacedatanetwork/bin/spacedatanetwork -c /etc/spacedatanetwork/config.yaml \
    key export --format backup
  ```

Making the two files by hand (any package version):

```sh
sudo install -m 0400 -o sdn -g sdn /dev/null /var/lib/spacedatanetwork/key-password
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' | sudo tee /var/lib/spacedatanetwork/key-password >/dev/null
sudo mkdir -p /etc/systemd/system/spacedatanetwork.service.d
printf '[Service]\nEnvironment=SDN_KEY_PASSWORD_FILE=/var/lib/spacedatanetwork/key-password\n' | sudo tee /etc/systemd/system/spacedatanetwork.service.d/key-password.conf
sudo systemctl daemon-reload
```

### Relaying

A node that is publicly reachable relays for peers that are not. This is the
default: AutoNAT decides, and a node behind NAT never relays. It is how nodes
on corporate networks and CGNAT — which can connect but cannot be dialed —
reach anyone at all.

It is bounded (32 reservations, 8 circuits per peer, 30-minute TTL), well under
libp2p's own defaults, because this is spare capacity a node contributes rather
than a service it exists to provide. Set `network.enable_relay: never` to opt
out, or `always` to run it regardless of reachability with libp2p's larger
limits. Check what your node decided with
`curl -s localhost:5001/api/v1/status/reachability`.

## 2. Kubo

Content identifiers, pinning, dataset publication and archive restore all go
through a Kubo (go-ipfs) daemon.

- **From a bundle** the node runs its own Kubo: the bundled binary, a
  repository under the node's data directory, the RPC API on
  `127.0.0.1:5002` (off the admin listener's 5001) and the gateway on
  `127.0.0.1:8080` with network fetch turned off. Nothing to install.
- **From a source build**, or to use a Kubo you already run, point
  `admin.ipfs_api_url` at its API (and `admin.ipfs_gateway_url` at its
  gateway for the dashboard's archive links); the node leaves it alone. A
  bare binary can also name a Kubo binary with `SDN_KUBO_BINARY` to have
  the node run it.
- The node proxies `/ipfs/*` to the gateway for content it already holds
  only; it never fetches arbitrary CIDs for callers. If you expose Kubo's own
  gateway anywhere, set `Gateway.NoFetch=true` on it.

## 3. Initialize and edit the config

```sh
spacedatanetwork init --config /etc/space-data-network/config.yaml
```

`init` derives the node's HD identity, seals the mnemonic to this machine and
writes a complete config. Edit these before binding anything non-loopback:

| Key | Set it to |
|---|---|
| `storage.path` | a directory on the disk that will hold the records |
| `storage.max_size` | below the free space on that disk (the node refuses writes under 5 GiB free) |
| `asset_pins.kubo_repo_path` | your existing Kubo repo, or delete the line (the generated default names a production volume) |
| `admin.listen_addr` | `127.0.0.1:5001` for a loopback dashboard; a routable address only with TLS and authentication on |
| `admin.require_auth` | `true` (the default). Leave it on. |
| `admin.tls_mode` / `admin.tls_hosts` | `managed` plus your hostname for a public dashboard |
| `admin.ipfs_api_url` | leave unset from a bundle (managed Kubo); your Kubo's API otherwise |
| `status.allowed_origins` | your own origin, or delete the line |
| `network.listen` | keep the defaults unless a port is taken (see step 4) |
| `subscriptions.default_retention` | `replace-current` (the default: each publication replaces the lane's previous set) or `archive-all` (keep and pin every publication). A subscription can choose its own rule. |

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

- `init` seals the mnemonic to this machine, or to the node's key password
  when it has one. Export it once and keep the export off the box:
  `spacedatanetwork key export --format backup`. In a container run it as
  `docker exec sdn /app/entrypoint.sh key export ...`, and for the .deb/.rpm
  service as user `sdn` with the service's key password file (section 1,
  Linux packages):
  a command started beside the daemon does not get the variable the
  entrypoint or the unit gives it.
- Keep the Kubo peer identity recoverable: `spacedatanetwork key escrow create`.
- A node with a key password file — yours (`SDN_KEY_PASSWORD_FILE`, or
  `security.key_password_file` in the config), or the one an installer
  generated for a fresh node whose machine fingerprint is refused
  (`~/sdn-key-password` from `install.sh`, `/var/lib/spacedatanetwork/key-password`
  from the package, `/app/data/key-password` from the container) — cannot
  open its identity without it. Keep it for the life of the node and back it
  up apart from the node's data, never in the same backup. The package's and
  the container's files are INSIDE the node's data (see section 1): a copy of
  that data alone opens the identity and the credential store.
- Making a key password file by hand: create it with a umask, so it is never
  readable by anyone else, then name it in `SDN_KEY_PASSWORD_FILE` for every
  start (or, with a build that carries this change, in
  `security.key_password_file` in the node's config):

  ```sh
  (umask 077 && head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > ~/sdn-key-password) && chmod 0400 ~/sdn-key-password
  ```
- The credential store (stored provider credentials, the `secrets:`
  capabilities) uses the same key password as the identity, in the same order
  (`SDN_KEY_PASSWORD`, `SDN_KEY_PASSWORD_FILE`, `security.key_password`,
  `security.key_password_file`), so it works on such a node too. Before this
  change it ignored the file and config sources and used the machine key. A
  store that was sealed that way stays sealed that way and keeps opening (an
  earlier build, after a rollback, opens it too); only where the machine key
  is now refused is it re-sealed under the password, the one way to keep its
  credentials. A store CREATED by this change on a node with a key password
  file is sealed under that password, and an earlier build cannot open it:
  after a rollback the node reports "credential store unavailable" until it
  runs this change again.

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

The publisher needs no configuration. Once connected, the node materializes
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

The default publication scope is `batch`. For a source that ingests several
raw-resource batches, opt into a complete source snapshot:

```yaml
publishing:
  auto_publish:
    - schema: NCD.fbs
      provider_id: example-provider
      source_name: example-source
      publish_scope: source
      min_interval: 5m
```

Source scope requires an exact, case-sensitive provider and source. It
publishes all currently stored records for that tuple, preserving each
record's original batch provenance. Ingest during publication or the minimum
interval remains pending and triggers a later snapshot without another
ingest event. Failed snapshots remain pending with capped retry backoff.
Starting the daemon also schedules a catchup snapshot for every configured
source lane, including records stored before the restart.

Saved module inputs from the runtime `/inputs` API are reapplied before
startup cron scheduling. A module whose saved inputs cannot be applied stays
in an error state while other modules continue starting.

Modules using the generic HTTP capability can set `follow_redirects: false`
to receive a redirect response directly. Redirect following remains enabled
by default. Responses retain the first value of each header in `headers`
and expose all values in `header_values`, including separate `Set-Cookie`
values.

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

Self-update needs the self-contained bundle layout (the command-line archive
the installers unpack). A bundle reads its updates from
`<update.feedBaseUrl>/cli-bundle/release/<os>/<arch>/index.json`, where
`update.feedBaseUrl` is `https://sdn.spaceaware.io/updates` in the bundle's
`manifest.json`, and installs only what is signed by one of its update roots.

**The update root is a demo key.** Every bundle trusts exactly one root, key
id `12cb25de07c0`:

```json
{"12cb25de07c0":"MCowBQYDK2VwAyEAi0cVzY4ICSRzddR7HYHKgEPXmXXFh1uvFizfMb/V5ZM="}
```

It is the bundle's `trust/update-roots.json`, and the same set is compiled
into the binary for a bundle whose trust file is missing (a trust file that
is present always wins). It is a demo key: whoever holds its private half can
sign an update every install accepts, so it will be replaced by a production
key through a signed rotation release.

Bundles up to v1.1.1 trusted `d4a971a7e534` instead. That root is the node
key leaked in SEC-001, derived from a published seed: anyone can sign for it.
Builds from this change on refuse it as an update root wherever it appears
(the trust file, `SDN_UPDATE_TRUST_ROOTS`, or a release's `trust_roots`), and
refuse to load a trust file that lists it.

**Do this now on every install of v1.1.1 or earlier.** Such an install
trusts `d4a971a7e534`, and its pushed-update lane (`update.enabled`, on by
default) acts on a signal from any peer: anyone can sign an update it will
fetch, install and start. No release can fix that over the channel, because
releases are signed with the demo root, which those installs do not trust.
Until you have done the step below, set `update.enabled: false` in the
node's config and restart the daemon.

**An existing install trusts the demo root after one manual step.** Re-run
the installer for the new release and restart the daemon: the new bundle
carries the demo root and the fixed update helper. Without reinstalling, a
command-line install can instead replace (not add beside) the old root:
write the demo root above into `<bundle>/trust/update-roots.json`, stop the
daemon, run `<bundle>/bin/spacedatanetwork update install --direct`, then
start it. `--direct` matters on builds from 2026-10-18 (47f043132) through
v1.1.1, whose helper reverts every update it gates (stack issue 670).
Desktop, `.deb`/`.rpm`, VM-bundle and container installs cannot update
themselves: install the new release. A bundle on the internal `beta` lane
(fleet hosts) reads `<feedBaseUrl>/cli-bundle/beta/...`, where every
manifest published so far is signed by another key: after the trust file is
replaced, `update install --direct` refuses them ("untrusted update signing
key") until a fleet release signed with the demo root is published on that
lane, so reinstall instead, or publish that release first.

**The channel needs a release's feed.** A release is installable from the
release channel once it is cut with the update signing key and its feed is
published to the server above. The current release's feed is published: a
command-line install of v1.1.4 or later takes it with
`spacedatanetwork update install`. The release workflow fails a stable
release that has no signing key, signs only with the key id derived from
that key, checks the key is the fleet root, and verifies every manifest
against `deployment/release/fleet-trust-roots.json` before it ships the feed.

How an install takes an update:

- `spacedatanetwork update install` fetches, verifies and stages, then hands
  the swap to a helper when a daemon is running. The helper stops the daemon,
  swaps, restarts it and waits for `GET /api/v1/ready` to answer 2xx, for
  `update.health_timeout_seconds` (default 600); otherwise it reverts to the
  previous build and quarantines the update under `updates/failed/`.
  A self-update whose helper never takes over leaves the daemon serving with
  nothing swapped: the daemon raises an `update_failed` alert and retries the
  same update after one `update.min_interval_seconds`, doubling for each
  further such failure up to 24 hours, without quarantining it. The same
  holds when the helper stopped the daemon and then failed before the swap
  (for example, the store lock still held): it records the failure in
  `updates/handoff-failure.json` and restarts the build that was running,
  which raises the alert and waits out the same backoff. The count survives
  restarts; a completed swap clears it.
- A pushed update signal (on by default, `update.enabled`) is acted on only
  when it is signed by an update root and its manifest and carrier lie under
  the bundle's own `update.feedBaseUrl`. A signal naming any other host, port
  or path is dropped before anything is fetched. `update install` and
  `update check` follow the same rule for the index, the manifest and the
  carrier, and for every redirect while fetching them.
- `spacedatanetwork status` prints `daemon_status=running` with
  `data_health=healthy`, or with `data_health=degraded` and the
  `alerts_error` / `alerts_warning` counts when operational alerts are active
  on a daemon that is serving. It no longer prints `data_runtime`, and prints
  `data_message` only when the health answer cannot be read.

## Known gaps on this date

- A source build (not a bundle) still needs a Kubo of your own.
- Archives are not announced, and there is no restore-from-network.
- Publishing has no dashboard or CLI front; it is the API above.
