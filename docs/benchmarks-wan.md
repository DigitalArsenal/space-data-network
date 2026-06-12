# WAN GossipSub Benchmarks — Production Bootstrap Nodes

Date: 2026-06-12. Companion to [benchmarks-latency.md](benchmarks-latency.md),
which defines the loopback methodology this measurement extends over a real
WAN path between the two production bootstrap nodes.

## Topology

| Node | Host | Region | Specs |
|---|---|---|---|
| sdn.spaceaware.io | 159.203.150.8 (primary IP 104.131.11.220) | DigitalOcean NYC3 | 2 vCPU / 8 GB |
| celestrak.eth | 167.172.219.213 | DigitalOcean SFO2 | 1 vCPU / 2 GB |

Both nodes run the `promise-gap-closure` build (commit c916be2e) under
systemd with `Restart=always`, listening on tcp/4001, quic-v1/udp/4001 and
ws/8080 (on sdn.spaceaware.io the websocket listener is the co-hosted relay
identity; see `sdn-server/internal/bootstrap/bootstrap.go`).

## Method

`sdn-server/cmd/wan-latency-bench` reproduces the loopback benchmark
cross-host: two libp2p hosts (TCP transport) join
`/spacedatanetwork/sds/BENCH.fbs` via GossipSub. After mesh formation and a
3-second excluded warm-up, the publisher sends 1,000 OMM-sized (512 B)
messages at ~400 msg/s, each carrying its send timestamp; the subscriber
records receive time on delivery. A dedicated bench topic is used so the test
does not inject synthetic payloads into the production `OMM.fbs` topic; the
GossipSub mechanics per topic are identical.

Publish->receive latency across hosts compares two different clocks. Both
hosts are NTP-synchronized (`systemd-timesyncd`, `System clock synchronized:
yes`), and the benchmark was run in both directions: with a symmetric path,
the residual clock offset appears as half the difference between the two
directions' medians.

Raw logs: `/tmp/sdn-wan-evidence/` on the operator workstation
(`bench-01pub-02sub.log`, `bench-02pub-01sub.log`, `rtt-*.txt`,
`partition-heal.log`).

## Network baseline (ping, 20 packets, 0% loss)

| Path | min | avg | max | mdev |
|---|---|---|---|---|
| NYC3 -> SFO2 | 67.751 ms | 68.132 ms | 69.756 ms | 0.507 ms |
| SFO2 -> NYC3 | 59.071 ms | 59.371 ms | 61.252 ms | 0.452 ms |

(The asymmetry reflects different return paths: the second measurement pings
the NYC3 node's DigitalOcean reserved IP, which is NATed to the droplet.)

## Publish->receive latency (1,000 x 512 B per direction)

| Scenario | Delivered | Payload | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| NYC3 pub -> SFO2 sub, direct GossipSub | 1000/1000 (100.0%) | 512 B | 28.82 ms | 29.43 ms | 40.02 ms | 58.46 ms |
| SFO2 pub -> NYC3 sub, direct GossipSub | 1000/1000 (100.0%) | 512 B | 32.52 ms | 33.83 ms | 34.65 ms | 41.82 ms |

Consistency check: the directional medians sum to 61.3 ms, within the
59.4–68.1 ms ping RTT band, implying a residual clock offset of roughly 2 ms
and GossipSub protocol overhead of single-digit milliseconds over raw network
distance. Delivery rate was 100% in both directions (the loopback gate
requires >= 99%); p99 stayed two orders of magnitude under the 1-second
tactical-latency bound.

## Partition / heal demonstration

The CelesTrak ingestion worker on celestrak.eth (SFO2) continuously
materializes OMM records; sdn.spaceaware.io replicates them. The partition
was induced on celestrak.eth by dropping all traffic to/from both of the
NYC3 node's public IPs (SSH access from the operator workstation is
unaffected — only the peer host is blocked):

```sh
iptables -I INPUT  -s 159.203.150.8   -j DROP
iptables -I OUTPUT -d 159.203.150.8   -j DROP
iptables -I INPUT  -s 104.131.11.220  -j DROP
iptables -I OUTPUT -d 104.131.11.220  -j DROP
```

Exact revert (also scheduled as an automatic fallback before the partition
was applied):

```sh
iptables -D INPUT  -s 159.203.150.8   -j DROP
iptables -D OUTPUT -d 159.203.150.8   -j DROP
iptables -D INPUT  -s 104.131.11.220  -j DROP
iptables -D OUTPUT -d 104.131.11.220  -j DROP
```

The demonstration was run twice. The first run surfaced a real gap: bootstrap
peers were only dialed at daemon startup, and the shard catch-up loop only
consults currently connected peers, so after the heal the nodes stayed
mutually isolated until restart. Commit 667dbc7b adds a once-a-minute
bootstrap reconnect loop; the second run below demonstrates automatic
recovery with that fix deployed.

Run 1 (build 078b74ff — surfaced the reconnect gap), timeline UTC:

| Time | Event | Evidence |
|---|---|---|
| 13:32:49 | Producer ingest cycle completes, publishes OMM/MPE datasets (31,532 records) | `GP sync complete: OMM=31532 MPE=31532`, `Dataset publication requested for OMM.fbs/MPE.fbs` |
| 13:46:44 | Replica catch-up materializing the new shards is cut mid-batch by the partition (applied ~13:45) | `catch-up completed with errors after materializing 3 shard(s): ... read published index asset ... timeout: no recent network activity` |
| 13:45–13:48 | Peers drop on both sides; local APIs keep serving; bench publish dial fails | peer lists: SFO2 `['16Uiu2HAmJziv…']`, NYC3 loses `9oK`; `dial tcp4 ...:49003: i/o timeout` |
| 13:48:22 | Heal (iptables rules removed) | `remaining DROP rules: 0` |
| 13:48–13:56 | No automatic reconnection (gap) | peer lists unchanged for >8 min |

Run 2 (build 667dbc7b with the bootstrap reconnect loop), timeline UTC:

| Time | Event | Evidence |
|---|---|---|
| 13:59 | Baseline: both nodes peered, bench 200/200 (100%), p50 28.7 ms | `before-partition` row above |
| 14:01:08 | Partition applied on SFO2 | 4 DROP rules active |
| 14:02:24 | Partition visible: SFO2 peer list lost both NYC3 identities; local API still serving | `02 peers: ['16Uiu2HAmJziv…']` |
| 14:02:41 | Heal: iptables rules removed | `rules remaining: 0` |
| **14:02:42** | **Automatic reconnection — 1 s after heal, no restart** | `Reconnected to bootstrap peer 16Uiu2HAm1Lbv… (peer ID verified)` |
| 14:02:47 | Full peer set restored on SFO2 | `['16Uiu2HAm1Lbv…', '16Uiu2HAmJziv…', '16Uiu2HAmP8K…']` |
| 14:03 | Gossip delivery fully restored: bench 200/200 (100%), p50 28.4 ms | `after-heal` row in the latency logs |

Reconvergence of published data is handled by the dataset shard publication
catch-up loop (every 5 minutes against connected trusted peers): the healthy
path materialized 1 shard at 13:28:46 and 3 shards at 13:46:44 from the
producer's publications, and with the reconnect loop restoring connectivity
within a minute of heal, the next tick resumes any interrupted backlog
automatically (the 13:46 batch shows exactly such an interruption and the
subsequent tick continuing).

## Caveats

- Cross-host one-way latency includes residual NTP clock offset (estimated
  ~2 ms here from the directional asymmetry). The percentile method matches
  the loopback benchmark, but absolute one-way numbers should be read with
  that error bar.
- The bench topic carries raw timestamped payloads, not signed FlatBuffer
  records; production publishes additionally pay serialization and signing
  costs before the payload reaches GossipSub, which this benchmark
  intentionally excludes (it measures transport, not encoding).
- Both measurement processes are standalone libp2p hosts on the same
  machines as the production daemons, not the daemons themselves; CPU
  contention on the 1 vCPU SFO2 host is included in the numbers, daemon
  internal queueing is not.
- The two nodes peer directly; there is no multi-hop gossip mesh in this
  topology yet, so these numbers represent the single-hop floor.
- Public reachability of 4001/8080 from arbitrary clients is still gated by
  the DigitalOcean cloud firewall at the time of measurement
  (droplet-to-droplet traffic, as measured here, is unaffected).
- Durable delivery across partitions comes from the pull-based recovery path
  (feed heads + publication log resync), not the live gossip path; the
  partition demonstration above exercises exactly that machinery.
- The CelesTrak ingest worker on celestrak.eth is gated on 5.0 GB free disk
  and the host sits at ~89% usage, so cycles intermittently stall on the
  guard; ~1 GB was reclaimed during this exercise (Kubo GC + caches) but the
  host needs a larger volume for sustained operation.
- Records materialized from the cached GP payload (3-hour refresh window)
  dedupe against existing content, so repeat cycles inside that window
  publish few or no new shards; the shard counts above reflect that.
