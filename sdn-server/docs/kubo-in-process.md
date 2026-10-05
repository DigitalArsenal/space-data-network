# Kubo inside SDN

Owner, 2026-10-05: "a single peer ID tied into the crypto subsystem of SDN, and
then updates for kubo / helia are rolled into SDN, which has the version of
those apps integrated somehow into its own version numbering system."

## What runs

- **One process.** `sdn-server` links upstream Kubo (`github.com/ipfs/kubo` at
  the release in `sdn-server/go.mod`) as a library, in `internal/kubo`. There is
  no `ipfs daemon` child, no `runtime/kubo/` in a bundle and no `ipfs.service`
  on a host.
- **One libp2p host, one peer ID.** The key is the node's HD identity key
  (secp256k1, IdentityPath). Kubo receives it in memory. The repository config
  on disk keeps the PeerID only, so the box no longer carries a plaintext
  Kubo key.
- **Loopback HTTP.** Kubo's own RPC API and gateway listen where
  `admin.ipfs_api_url` and `admin.ipfs_gateway_url` say, on loopback only:
  - The default API URL maps to `127.0.0.1:5002`, because the admin listener
    uses 5001. The gateway defaults to `127.0.0.1:8080`.
  - Port 0 picks a free port.
  - An API URL off loopback is refused, because a Kubo elsewhere would be a
    second peer.
- **Repository.** It is `asset_pins.kubo_repo_path` when that names an existing
  repository, otherwise `<data>/kubo`.

## Seams

Kubo source is never edited. Everything goes through Kubo's public library
surface:

| Seam | Use |
|---|---|
| `plugin/loader` | Kubo's built-in datastores, loaded once per process |
| `repo.Repo` wrapper | Adds the identity in memory and applies the node-owned settings on every config read |
| `BuildCfg.Host` | Builds the host from the node's own options (see below) |
| `BuildCfg.Routing` | Sets DHT participation: auto → `dht`, always → `dhtserver`, never or off → `dhtclient` |
| `corehttp` | Serves Kubo's RPC commands, and the gateway with no commands |

The host is built from the node's own options: transports, browser-friendly
limits, relay bounds, the NAT watchdog, AutoTLS and the relay candidate feed. It
uses Kubo's peerstore, and Kubo's connection gate is ANDed with the node's trust
gate.

Kubo's own resource manager, connection manager, relay service and client,
AutoTLS, mDNS and pubsub are switched off in its config. The host's versions of
these are the node's, and the node's gossipsub runs on the one host.

## Dependencies

`sdn-server/go.mod` carries Kubo's `replace` and `exclude` directives verbatim,
because those apply only in the main module. Without them SDN would build a Kubo
upstream never shipped.

`scripts/check-kubo-pin.js` checks:
- the version, the directives and every place the version is mirrored;
- with `--build-list` (the security lane), that Kubo's linked packages compile
  from the same module versions in SDN's build as in Kubo's own (owner
  2026-09-28: never change what Kubo depends on).

To move to a new Kubo:
1. Bump the requirement.
2. Take Kubo's directives and its dependency set.
3. Run the check.

`deployment/release/kubo-update-watch.mjs` reports when upstream is ahead.

## Versions

`suite.versions.json` and `scripts/generate-suite-version-info.js` produce
`internal/versioninfo` (Go) and `sdn-js/src/version-info.generated.ts`. Three
versions in them are read rather than hand-kept: `KuboVersion` from `go.mod`,
`HeliaVersion` from sdn-js's exact `helia` pin, and the node's
`SpaceDataStandardsVersion` from the schemas it embeds
(`internal/sds/search-schemas/manifest.json`). The browser SDK reports its own
SDS pin, which may move later than the node's.

The identify agent is `kubo/<kubo>/spacedatanetwork/<sdn>`. Peers recognise SDN
nodes by the `spacedatanetwork` it contains.

SDN's version moves at least as far as the strongest component move. A Kubo
minor (Kubo is 0.x) is an SDN minor. A change of Kubo's repository version is
one-way, and the release says so. Kubo 0.39.0 → 0.43.1 took SDN 1.0.5 → 1.1.0.

## Taking over a repository

A repository from the separate Kubo (the supervised child, or `ipfs.service`)
opens in place. Both 0.39.0 and 0.43.1 use repository version 18. The takeover
runs in three steps:

1. Kubo's repository lock is taken, so a Kubo still running on the repository
   stops the takeover.
2. The config, previous identity and key included, is copied to
   `config-pre-sdn-identity-*` beside it.
3. The Identity becomes the node's PeerID, with no key.

The first start after a takeover sets Kubo's own `Provide.DHT.ResumeEnabled`
to false. That clears the provider's reprovide history, so every region is
announced under the node's peer ID as soon as it is online, instead of waiting
up to `Provide.DHT.Interval` (22 h) in the resumed schedule. Later starts resume
normally. The cost is bounded by the DHT's size, about 3,000 lookups on today's
Amino DHT, not by how many keys the node holds.

To roll back to the two-process layout: stop the node, restore that copy as
`config`, and start the separate Kubo.

## Fleet

host-01 and host-02 run `ipfs.service` on the repository that
`asset_pins.kubo_repo_path` names. Rolling them is an ops task with four steps:

1. Stop and disable `ipfs.service`.
2. Give the node's service user the repository.
3. Start the node.
4. Check that `/api/v1/id`, the Kubo RPC `id` and the DHT agree on one peer.
