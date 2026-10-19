# Rollout: flatsql-sync 2.0.0 (FlatBuffers only)

1.0.0 and 2.0.0 nodes refuse each other: a 1.0.0 dial fails protocol
negotiation, feed heads move to a new topic, and every JSON form is gone. There
is no dual protocol. host-01, host-02 and the weather VM (vm-orbit-det-01) roll
in one window, and the console's sdn-js ships in the same window; a node or
console left on 1.0.0 stops syncing with the rest until it is updated.

Changed wire surfaces (old -> new):

- libp2p sync: `/space-data-network/flatsql-sync/1.0.0` + JSON `op` frames (u32
  big-endian + JSON) -> `/space-data-network/flatsql-sync/2.0.0/<op>`, one
  `$QRP` Request frame in, `$QRP` Page / Error frames out (u32 little-endian).
  Ops: read-chunk, open-manifest, list-published, read-shard, read-index,
  read-shard-batch, wire-speed-probe, list-datastores. Removed: scan,
  open_snapshot, ack_progress, the read_chunk record-ref form.
- Feed heads: `/space-data-network/feed-heads/1.0.0/<schema>` JSON + hex
  signature -> `/space-data-network/feed-heads/2.0.0/<SCHEMA>.fbs`, one `$QRP`
  Page with one SEGMENT row, Ed25519 signature and key as Bytes cells.
- `POST /api/v1/data/remote/{peerID}` JSON relay -> `POST
  /api/v1/data/remote/{peerID}/{op}`, one `$QRP` Request frame, reply verbatim.
- `POST /api/v1/pubsub/publish` JSON + base64 -> one size-prefixed SDS frame;
  reply one `$QRP` Page.
- `GET /api/v1/pubsub/messages` JSON + base64 -> `$QRP` Page + record frames.
- `GET /api/v1/pubsub/topics` JSON -> one `$QRP` Page (TABLES).
- `/ws` JSON text messages -> binary frames: `$DSS` subscribe / unsubscribe,
  SDS record frames publish and deliver, `$QRP` Error frames; text refused.
- CA result summaries (`/sdn/private-node/...` JSON): removed (no consumer).

Console (spaceaware-ui) gate before the window: PublicationDiscovery.ts,
sources/sourceLoader.ts, dashboard ApiPanel.svelte and data/remote-records.js
still speak 1.0.0; they move to sdn-js `requestFlatSqlPublishedList` /
`decodeFlatSqlPublishedList` / `FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID` and
`/api/v1/data/remote/{peerID}/{op}` before the console ships.
