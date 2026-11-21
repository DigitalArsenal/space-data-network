# IPNS pointers: a lane's current DPM under a fixed name

`publishing.ipns_pointers` names a Kubo key per lane. After each series the lane publishes, the node signs `/ipfs/<DPM CID>` under that key, so a reader can find the newest catalog through one fixed IPNS name. The CelesTrak GP lane on host-02 is one (key `celestrak-ipns`); the gallery's `celestrakSdnCatalog.js` resolves its name at delegated-ipfs.dev.

## Delegated publishers

The node's embedded Kubo routes over libp2p only: it has no delegated routers or publishers. An online Kubo answers `name/publish` only after it has put the record into the DHT. On host-02 (1 vCPU, a thin routing table) that put never finished inside the node's 3-minute deadline, from 2026-11-19 05:28 UTC on, and the name stood still until its record was about to expire.

Kubo signs the record and stores it in its own datastore before the DHT put. With `delegated_publishers` set, the node reads that signed record back (`name/get` with `offline=true`, no network lookup), checks it against the name, and sends it to each Routing V1 HTTP publisher (`PUT /routing/v1/ipns/<name>`). delegated-ipfs.dev puts it into the DHT. Kubo's own DHT put runs on in the background for up to 15 minutes and its outcome is only logged. The pointer counts as published when one publisher accepts the record. The publisher only carries the record: every reader verifies it against the key in the name.

```yaml
publishing:
  ipns_pointers:
    - schema: OMM.fbs
      source_name: celestrak-gp
      key: celestrak-ipns
      lifetime: 168h
      republish_every: 12h
      delegated_publishers:
        - https://delegated-ipfs.dev
```

- `lifetime`: how long the signed record is valid (Kubo's default is 48 h).
- `republish_every`: re-sign and re-send the current pointer at this interval while no newer series replaces it. DHT nodes drop a record after about 48 hours, so without it a long upstream outage (no new series) would let the name disappear while its record is still valid. Must be shorter than `lifetime`. A minute after the node starts, each such key's current record (the signed record Kubo holds, if it is still valid) is re-sent, so a restart followed by no new series does not stop the republishing.
- `delegated_publishers`: Routing V1 base URLs. Empty keeps the Kubo DHT put alone.

The node logs one line per pointer: `IPNS pointer <lane>: /ipns/<name> sequence <n> valid until <time> sent to <k> of <m> delegated publishers`, or the reason it failed.
