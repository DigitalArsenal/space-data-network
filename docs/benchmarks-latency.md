# SDN Pub/Sub Latency Benchmark

End-to-end publish-to-receive latency for Space Data Network's GossipSub
message path, measured by `sdn-server/internal/stress/latency_bench_test.go`.

## Methodology

Two in-process libp2p hosts (TCP transport, loopback) joined to the
`/spacedatanetwork/sds/OMM.fbs` topic via GossipSub. After mesh formation and
an excluded warm-up round, the publisher sends 1,000 OMM-sized (512 B)
messages at a steady ~500 msg/s, each carrying its send timestamp. The
subscriber records receive time on delivery; percentiles are computed over
all delivered messages.

Run it locally:

```sh
cd sdn-server
go test ./internal/stress -run TestPubsubLatencyBench -v
```

The test fails if p99 exceeds 1 s or delivery falls below 99%.

## Results — 2026-06-10

Hardware: Apple M1 Pro, 16 GB RAM, macOS (Darwin 25.5.0). Single machine,
loopback networking.

| Scenario | Delivered | Payload | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| direct 2-node GossipSub (localhost TCP) | 999/1000 (99.9%) | 512 B | 708 µs | 1.474 ms | 4.543 ms | 11.627 ms |

## Caveats

- Loopback latency excludes WAN propagation, NAT traversal, and relay hops.
  Internet-path latency adds the network RTT (typically 10–150 ms) plus one
  GossipSub hop per relay; the protocol overhead measured here (sub-5 ms at
  p99) is a small fraction of a sub-second budget in realistic topologies.
- GossipSub is best-effort: the warm-up round absorbs initial mesh settling,
  and the measured steady-state delivery was 99.9%. Durable delivery in SDN
  comes from the pull-based recovery path (feed heads + publication log
  resync), not the live gossip path.
- For WAN measurements, deploy two `spacedatanetwork` nodes on separate
  hosts, subscribe one to a schema topic, and publish timestamped records
  from the other; the same percentile method applies. Pair the latency table
  with `ping`-measured RTT between the hosts to separate protocol overhead
  from network distance.
