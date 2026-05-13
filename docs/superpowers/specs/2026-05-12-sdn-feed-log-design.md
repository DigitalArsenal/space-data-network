# SDN Feed Log Design

## Goal

Adopt the OrbitDB-style replication shape without replacing FlatSQL: providers publish small mutable heads that point to immutable FlatBuffer shard artifacts, replicas sync by head/manifest/CID, and FlatSQL remains the local query engine. The CelesTrak published-shard download phase must sustain at least 80% of the measured wire-speed capacity for the same replica, transport path, and shard seed set used in the acceptance run.

## Architecture

The feed log is SDN metadata around FlatSQL, not an upstream FlatSQL feature. A feed is keyed by `schema + provider peer/public key + provider_id + source_name + batch_id + query_profile`. The feed head is a deterministic hash of the ordered published shard entries. Each entry points to immutable IPFS CIDs for:

- native FlatSQL size-prefixed FlatBuffer shard bytes;
- the shard query/index sidecar;
- the signed DPM manifest;
- the PNM announcement when present.

The first implementation uses `sdn_dataset_shard_publications` as the local feed-entry index. `open_manifest` now has a feed-first fast path: if published shard entries exist, it builds the manifest directly from those entries and does not count or scan raw OMM/CAT/SPW rows. The row scan path is only for unpublished local datasets; configured remote provider sync must use the published feed path.

## Sync Flow

1. Replica dials `/space-data-network/flatsql-sync/1.0.0` with `op: "open_manifest"` and query profile `dataset-publication-offset-v1`.
2. Provider returns the feed head, high-water mark, total published rows/bytes, and ordered CID segments from the feed-entry index.
3. Replica fetches shard CIDs through the local IPFS node/gateway and verifies SHA-256.
4. Replica ingests raw FlatBuffer shard streams into its local SDN-managed FlatSQL datastore namespace.
5. All SQL and explorer queries run locally.

## Performance Target

For the CelesTrak historical dataset, manifest discovery must be O(number of published shards), not O(number of records). The acceptance target is shard download throughput of at least 80% of measured wire-speed capacity for the same replica, transport path, and shard seed set used for the dataset transfer, measured with the libp2p `wire_speed_probe` immediately before the transfer. Pass/fail uses only bytes received during the published-shard download window, measured wall-clock from first shard byte request to final shard byte received. Manifest discovery, SHA-256/signature verification, pinning, and FlatSQL materialization are separate timing fields so storage or ingest bottlenecks cannot hide network underuse.

On a measured 2 Gbps path, the target is about 1.6 Gbps, or 200 MB/s. At that target, a 550 MB transfer has a best-case network floor of about 2.75 seconds before verification, pinning, and FlatSQL materialization. Anything materially slower fails the sync target unless the timing breakdown proves the bottleneck is outside the shard download phase.

## Current MVP

- `open_manifest` can return a feed manifest from published shard metadata even when no raw rows are scanned.
- The browser/desktop worker uses published CID segments for configured provider sync. Live `read_chunk` is only for unpublished local datasets and is not a degraded provider-sync path.
- The feed head is deterministic over shard CIDs, hashes, byte counts, row counts, offsets, and publication timestamps.
- The FlatSQL sync protocol exposes a bounded `wire_speed_probe` op over libp2p so replicas can measure source-specific wire speed and compare dataset transfer against the 80% target.
- Published shard entries carry explicit feed sequence, previous-head, and current-head metadata, and dataset publication broadcasts the current feed head over a schema-scoped libp2p pubsub topic.
- `npm run measure:flatsql-sync` runs the acceptance harness: it measures libp2p wire speed, opens the feed manifest over the FlatSQL sync protocol, asks local Kubo to discover and connect IPFS providers for selected shard CIDs, optionally connects known shard seed peers with repeatable `--ipfs-peer` arguments, fetches immutable shard CIDs from the local IPFS gateway, and reports download utilization plus separate manifest, verification, and FlatSQL timing fields.
- CelesTrak OMM reached the 80% target only after the 46 immutable shard CIDs were seeded by both CelesTrak and SpaceAware. A single-provider fresh-cache download topped out at 41-54% of measured wire speed; a two-seed fresh-cache run downloaded 659,703,180 bytes in a 16.45 second network window, 127% of the measured wire probe.
- Desktop/browser sync automatically connects the local Kubo node to configured SDN artifact seed peers, selected provider first, before fetching published shard CIDs through the local gateway. With both CelesTrak and SpaceAware configured, normal app sync no longer depends on a manually supplied second seed peer.
- Observed desktop SDN peer records that match configured provider identities also advertise the provider artifact peer address, so sync retry paths can reuse artifact seeds discovered from the local peer list.
- Desktop/browser sync asks local Kubo for IPFS providers of published shard CIDs and connects to those provider multiaddrs before gateway shard reads, so multi-provider seeding can come from IPFS provider records without introducing HTTP or SSH data fallbacks.
- Browser/desktop sync passes verified published shard streams directly to FlatSQL's native size-prefixed stream ingest when the payload frames are already direct FlatBuffers, with the decoder path retained for explicit-key previews, duplicate shard keys, and nested SDN-prefixed frames.

## Follow-Up Work

- Add CAR export/import for shard groups to reduce per-CID round trips when a provider or peer offers a complete dataset snapshot.
- Extend shard seed peer discovery beyond configured, observed-known, and local Kubo provider records to generic trusted peer records.
