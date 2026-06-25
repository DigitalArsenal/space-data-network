# SDN Chaos And Stress Testing

This is the working chaos/stress test plan for the Space Data Network FlatSQL/libp2p replication path.

## Council Findings

The highest-risk path is not basic FlatBuffer generation. It is resumable published-shard replication under churn:

- libp2p FlatSQL sync must converge after dropped streams, corrupt shard bytes, peer restarts, and provider/requester partitions.
- The hot path must stay direct FlatSQL size-prefixed FlatBuffer bytes over `/space-data-network/flatsql-sync/1.0.0`.
- No HTTP or SSH fallback is allowed in chaos or throughput tests.
- Published shard verification must reject SHA-256 mismatches before local materialization.
- Progress must be keyed by verified shard CID and manifest/head metadata, not by local row count.
- Throughput reporting must never exceed the measured or configured wire-speed baseline.

The council recommended these concrete scenarios:

1. Wire-speed baseline vs published shard download with a 99% pass gate.
2. Interrupted published-shard resume at deterministic byte offsets.
3. Snapshot/cursor resume while provider data advances.
4. Peer churn with 8-32 requesters and repeated provider/requester restarts.
5. Network partition and heal with completed CIDs skipped on resume.
6. Corrupt shard bytes and mismatched metadata rejection.
7. Truncated and trailing `read_published_shard_batch` payload rejection.
8. Backpressure and memory-ceiling testing under slow consumers.
9. High-concurrency range storm across multiple libp2p clients.
10. Address churn across TCP, WS/WSS, WebTransport, WebRTC relay/direct, and p2p-circuit.
11. Module-delivery grant/fetch while FlatSQL sync saturates the link.
12. Post-run invariant replay: manifest segments, pin ledger, materialized rows, hashes, and feed-head chain.

## Added Harness

The local deterministic harness is:

```sh
npm run chaos:local -- [options]
```

It lives at:

- `sdn-js/scripts/chaos-local-network.mjs`
- `sdn-js/src/stress/chaos-local-network.stress.test.ts`

It models one provider and N consumers over a local virtual libp2p/FlatSQL published-shard network. It injects:

- dropped streams
- corrupted responses
- network partitions
- requester restarts
- checkpointed pin-ledger resume
- peer-to-peer shard sourcing after the first consumer pins a shard
- completed-feed resume with zero shard redownload
- feed-head advance where verified checkpoint shards are reused and only new shards are fetched

The output records provider bytes, peer bytes, duplicate bytes, retries, corruption rejections, simulated wire-speed utilization, and per-consumer convergence.

The throughput harness also has a behavioral stress test that verifies a single
published shard is split into byte ranges and fetched across multiple direct
libp2p sources. This guards against silently ignoring tuned range sizes or
collapsing back to one source.

## Local Run Results

Command:

```sh
node sdn-js/scripts/chaos-local-network.mjs \
  --json \
  --shards 256 \
  --shard-bytes 1048576 \
  --rows-per-shard 2000 \
  --consumers 8 \
  --concurrency 32 \
  --bandwidth-mbps 2000 \
  --drop-every 37 \
  --corrupt-every 53 \
  --partition-every 71 \
  --restart-every 89 \
  --checkpoint-file artifacts/chaos/local-chaos-checkpoint.json
```

Result:

- Consumers converged: `8/8`
- Verified rows: `4,096,000 / 4,096,000`
- Unique shards: `256`
- Downloaded bytes: `2,188,224,000`
- Provider bytes: `272,480,000`
- Peer bytes: `1,915,744,000`
- Duplicate bytes: `0`
- Dropped streams: `59`
- Corrupt responses rejected: `40`
- Partitions: `31`
- Restarts: `24`
- Retries: `154`
- Reported speed: `249,997,029 B/s`
- 2 Gbps baseline: `250,000,000 B/s`
- Utilization: `99.9988%`

Full JSON:

```text
artifacts/chaos/local-chaos-report.json
```

## Existing Stress Results

Live FlatSQL replication gate:

```sh
npm run stress:flatsql-replication
```

Result:

- Generated one `OMM.fbs` published FlatSQL shard and registered it in the provider feed.
- Opened the published manifest over `/space-data-network/flatsql-sync/1.0.0`.
- Downloaded the shard bytes over libp2p `read_published_shard_batch` directly to a local shard file.
- Verified the shard SHA-256 and FlatSQL size-prefixed frame layout from disk.
- Imported the downloaded shard file into the subscriber FlatSQL store without hydrating the whole shard in memory.
- Node PNM/DPM materialization now uses the same file-backed shard/index fetch path before FlatSQL import.
- Interrupted one `read_published_shard` transfer after a verified byte prefix, resumed from that exact byte offset, verified the completed shard, and imported it.
- 64 MiB default run: downloaded and imported `230,000` OMM rows as a quick smoke gate.
- 16 MiB default range-resume run: resumed from byte `5,592,405`, completed `17.09 MiB` across `60,000` OMM rows in `3` range requests, and imported `60,000` rows.
- 256 MiB disk-backed gate run: downloaded `257.36 MiB` across `900,000` OMM rows at `1709.77 MiB/s`; durable FlatSQL import completed in `2m13.6945705s`.
- 1 GiB configured-link gate run: downloaded `1026.80 MiB` across `3,590,000` OMM rows at `1803.40 MiB/s`; hash verification completed in `624.200292ms`; durable FlatSQL import completed in `13m45.596499292s`.
- Configured 2 Gbit/s production gate: passed. Required throughput is `247,500,000 B/s` (1.98 Gbit/s, `236.03 MiB/s`), and the 1 GiB data-plane transfer sustained `1803.40 MiB/s`.
- HTTP fallback: none.
- SSH fallback: none.

Storage/node file-backed materialization checks:

```sh
./scripts/go-with-wasmedge.sh test ./internal/storage \
  -run 'Test(MaterializeDatasetPublication|FetchIPFSBlockByCID(ToFile)?UsesCatForChunkedUnixFSFiles|ImportDatasetShard(CountsOnlyNewRowsOnReplay|FromFilesStreamsShardIntoFlatSQL))' \
  -count=1

./scripts/go-with-wasmedge.sh test ./internal/node \
  -run 'TestMaterializeStoredDatasetPublicationPNMs(ReplaysTrustedProviderPNM|DoesNotRetryPermanentShardFrameErrors)' \
  -count=1
```

Result:

- storage materialization tests passed.
- node dataset-publication PNM replay tests passed.
- shard/index file fetcher was exercised for materialization.
- node dataset feed-head receive now subscribes to schema-scoped feed-head topics
  and imports trusted provider shard/index assets directly over
  `/space-data-network/flatsql-sync/1.0.0`, without using HTTP or SSH as a
  FlatSQL data fallback.

The target byte size can be raised with:

```sh
STRESS_LIVE_FLATSQL_BYTES=$((512*1024*1024)) npm run stress:flatsql-replication
```

Production/lab acceptance can also enable a configured-link gate in addition to
the measured `wire_speed_probe` gate:

```sh
SDN_WIRESPEED_TEST=1 \
SDN_TEST_LINK_GBIT=2 \
STRESS_LIVE_FLATSQL_BYTES=$((1024*1024*1024)) \
npm run stress:flatsql-replication
```

With `SDN_TEST_LINK_GBIT=2`, the sustained published-shard download phase must
meet `247,500,000 B/s` (1.98 Gbit/s), which is 99% of the configured 2 Gbit/s
link. The stress result reports the measured probe gate and the configured-link
gate separately through `WireSpeedTarget`, `TargetMet`,
`ConfiguredGateEnabled`, `ConfiguredLinkBytesPerSecond`,
`ConfiguredRequiredBytesPerSecond`, and `ConfiguredTargetMet`. Manifest
discovery, shard verification, and FlatSQL import remain separate timing fields
and do not hide a data-plane transfer miss.

The range-resume byte size can be raised independently with:

```sh
STRESS_LIVE_FLATSQL_RESUME_BYTES=$((256*1024*1024)) npm run stress:flatsql-replication
```

JavaScript stress suite:

```sh
npm run stress:js
```

Result:

- `2` stress files passed.
- `7` tests passed.
- Existing streaming live-node test remains skipped unless `STRESS_NODE_ADDR` is set.
- The new chaos test ran as part of the stress suite.

Live CelesTrak direct-range remote proof:

```sh
cd sdn-js
npm run measure:flatsql-sync -- \
  --peer 16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4 \
  --addr /ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4 \
  --schema OMM.fbs \
  --provider-id space-data-network-02 \
  --source-name celestrak-gp \
  --max-segments 1 \
  --json
```

Result:

- OMM: `2,471,741` remote rows, `53` segments, first segment `9,134,144` bytes verified, target met.
- CAT: `145,519` remote rows, `5` segments, first segment `6,977,308` bytes verified, target met.
- MPE: `218,256` remote rows, `7` segments, first segment `4,095,304` bytes verified, target met.
- SPW: `177,030` remote rows, `7` segments, first segment `5,548,328` bytes verified, target met.
- Transport mode: `direct-libp2p-published-shard-ranges`.
- Remote HTTP fallback: `false`.
- SSH fallback: `false`.

Go FlatBuffer/CID/pin-index stress:

```sh
STRESS_TARGET_SIZE=$((256*1024*1024)) \
  ./scripts/go-with-wasmedge.sh test -v -tags=stress -timeout=1h ./internal/stress/...
```

Result:

- Live FlatSQL wire-speed gate passed: `1026.80 MiB`, `3,590,000` rows, `1803.40 MiB/s` download against a configured 2 Gbit/s production gate.
- Live FlatSQL range-resume gate passed: resumed from byte `5,592,405`, completed `17.09 MiB`, `60,000` rows, and imported `60,000` rows.
- Generated `910,000` OMM FlatBuffers, `0.25 GB`, `2169.57 MB/s`.
- Pinned/tracked `910,000` CIDs.
- Streamed pin index at `5,411,919 CIDs/sec`.
- Verified `10,000` FlatBuffer records.
- Verified CID determinism across `1,000` generated records.
- Transfer-between-nodes test skipped because `STRESS_NODE1_ADDR` and `STRESS_NODE2_ADDR` were not set.

## VM-Backed Local Network Status

Docker Desktop is available and running:

- Docker Server: `29.2.0`
- OS: Docker Desktop Linux VM
- Architecture: `aarch64`
- CPUs: `16`

The repo compose file validates after local bootstrap preparation:

```sh
deployment/scripts/local-cluster.sh prepare
docker compose --env-file deployment/.env -f deployment/docker-compose.yaml config --quiet
```

The VM-backed SDN images built successfully:

- `deployment-full-node-1`
- `deployment-full-node-2`
- `deployment-edge-relay-us`
- `deployment-edge-relay-eu`
- `deployment-edge-relay-asia`
- `deployment-registry-builder`

The VM-backed local SDN cluster now starts successfully via:

```sh
deployment/scripts/local-cluster.sh up -d
deployment/scripts/local-cluster.sh test
```

Cluster smoke result:

- `sdn-full-1`: running, Docker health `healthy`
- `sdn-full-2`: running, Docker health `healthy`
- `sdn-edge-us`: running, health endpoint OK
- `sdn-edge-eu`: running, health endpoint OK
- `sdn-edge-asia`: running, health endpoint OK
- `sdn-registry`: running
- full node WebSocket endpoints responding on `18080` and `8081`
- edge WebSocket endpoints responding on `8090`, `8092`, and `8094`
- full-node-2 connected to the generated bootstrap peer over libp2p

The old blocker was fixed:

```text
deployment/scripts/prepare-local-cluster.mjs now generates deployment/generated/node.key,
full-node configs, and deployment/.env with the matching bootstrap peer ID.
```

The source guardrail test is:

```sh
cd sdn-js
npx vitest run src/deployment/local-cluster-source.test.ts
```

## VM-Backed Chaos Result

The repeatable Docker Desktop VM chaos runner is:

```sh
npm run chaos:docker-cluster
```

It lives at:

```text
deployment/scripts/chaos-local-cluster.mjs
```

It injects and verifies recovery from:

- datastore convergence under FlatSQL sync chaos
- completed datastore resume with zero redownload
- datastore feed-head advance with only new shard downloads
- edge relay pause/unpause
- secondary full-node restart
- edge relay network disconnect/reconnect
- registry-builder restart
- post-chaos bootstrap peer connectivity

Latest result:

- baseline cluster check: passed in `142 ms`
- datastore convergence: passed, `512,000` verified rows, `273,528,000` downloaded bytes, no HTTP/SSH fallback
- completed datastore resume: passed, `512,000` verified rows, `0` downloaded bytes, `256` verified shards reused
- datastore feed advance: passed, `576,000` verified rows, `33,536,000` downloaded bytes, `256` verified shards reused and `32` new shards pinned
- edge pause/unpause recovery: passed in `1121 ms`
- full-node-2 restart recovery: passed in `1081 ms`
- edge network partition/rejoin: passed in `1365 ms`
- registry restart recovery: passed in `184 ms`
- post-chaos libp2p peer connectivity: passed in `17 ms`

Full JSON:

```text
artifacts/chaos/docker-cluster-chaos-report.json
```

## Pass/Fail Gates

Hard gates:

- accepted corrupt payloads: `0`
- duplicate materialized rows: `0`
- duplicate downloaded bytes during steady resume: `<1%`
- persistent sync error after recovery: `0`
- `pinnedRows <= localRows <= remoteRows` unless a new remote head explains the difference
- retry loops must terminate within configured attempts
- no HTTP or SSH fallback in FlatSQL sync tests

Performance gates:

- clean network published-shard sync: `>=99%` measured wire speed
- lossy/partitioned chaos: `>=60%` measured wire speed unless the scenario is intentionally saturated
- time to first local page: p95 `<=2s`
- time to first remote page: p95 `<=8s`
- time to resume after recovery: p95 `<=10s`
- memory after warm steady-state: `<=10%` growth after GC/idle
- disk amplification: `<=1.25x` verified downloaded bytes
