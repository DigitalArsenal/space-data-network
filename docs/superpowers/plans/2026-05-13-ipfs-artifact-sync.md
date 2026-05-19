# IPFS Artifact Sync Plan

## Goal

Replace the slow custom FlatSQL shard request/response download path with IPFS-native published shard replication, while keeping the FlatSQL/libp2p protocol as the manifest and control plane. Provider-side CAR artifacts may be published for pinning/verification, but desktop sync must not block on monolithic CAR prefetch while Kubo gateway reads remain slow.

## Constraints

- No remote HTTP or SSH data fallback.
- The browser/desktop renderer must stay responsive; sync remains in the worker/backend path.
- Data transfer and ingest use raw FlatBuffers/FlatSQL backing bytes, not row JSON translation.
- Progress must report real snapshot progress and bounded speed math.

## Steps

- [x] Update source guardrails and throughput parser tests to require the IPFS artifact replication path.
- [x] Change the local FlatSQL worker to connect local Kubo to artifact peers/providers and fetch shard CIDs through local IPFS.
- [x] Change the throughput harness to measure the same IPFS artifact path used by the desktop sync.
- [x] Run focused JS tests for worker, throughput, and artifact helpers.
- [x] Build/package/restart the desktop app after verification.
- [ ] Reset local data and run the five fresh sync proofs against the CelesTrak node.
  - 2026-05-14: Partial remote proof completed without resetting desktop local data: one published segment each for OMM, CAT, MPE, and SPW was fetched and verified from CelesTrak over direct libp2p published-shard byte ranges with `remoteHttpFallback=false` and `sshFallback=false`. Full fresh local desktop sync remains open.
