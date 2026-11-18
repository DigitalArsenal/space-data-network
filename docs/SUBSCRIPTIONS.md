# Subscriptions: how a node imports lanes

From 1.2.0 a node imports only the lanes it subscribes to. A lane is one
standard, provider and source (`OMM` `celestrak.eth` / `celestrak-gp`); a
subscription names a lane and the producer peer to take it from. The node
follows the lane over a live stream (`read-chunk` with `FOLLOW` on
flatsql-sync): records arrive within seconds of the producer storing them.
The producer must be in `peers.trusted_peers`, and the node must be able to
dial it (`network.bootstrap`).

Removed in 1.2.0: `peers.trusted_import` (the allow and deny lists and
`new_lanes`), the lane ledger `trusted-import-lanes.json`, the feed-head
announcements and the five-minute catch-up of published shards. A signed
`$PNM` publication from a peer still imports, but only for a lane this node
subscribes to from that peer.

## Commands

Every command goes through the running daemon's admin API and signs in with
the node's root key, as `apps run` does. `--json` prints JSON.

```sh
# subscribe (prints the first read, then subscribes when it fits)
spacedatanetwork sync subscribe --schema OMM --provider-id space-data-network-02 \
  --source-name celestrak-gp --from <producer peer id>

# the lanes this node follows
spacedatanetwork sync subscriptions

# stop one lane, one producer of it, or every lane of a standard
spacedatanetwork sync unsubscribe --schema OMM --provider-id space-data-network-02 \
  --source-name celestrak-gp --from <producer peer id>
spacedatanetwork sync unsubscribe --schema OMM

# the 1.1.9 ledger and allow list as subscriptions (plan; --yes acts)
spacedatanetwork sync subscribe --from-ledger
spacedatanetwork sync subscribe --from-ledger --yes
```

`sync status` and `sync watch` read through the daemon's sync lane
(`GET /api/v1/sync`) when a daemon answers on the configured admin address,
and the store itself when none does (`--batch-id` needs the store).

`sync subscriptions` shows for each lane: the producer, when it was
subscribed, whether it is connected, records and bytes received, records
stored and removed, the batch held as the producer's current one, the
producer's stated current batch and its records, the lag (records of that
batch not held yet) and the last error.

## The first read

Where a subscription starts follows the lane's rule
(`subscriptions.default_retention`, a standard's default, or the lane's own
rule):

- `replace-current`: at the producer's current batch. The node reads the
  producer's statement of the lane (its current batch, that batch's records
  and bytes, the lane's records and bytes, and its present position), follows
  the stream from the present position, and fetches the current batch's
  records by content address. Nothing older is sent; records already held are
  tagged to the batch, not sent again.
- `keep-all` and `archive-all`: at the lane's beginning.

`--start current` or `--start beginning` overrides the rule's start.

Before it subscribes the node compares the first read with what it can take,
and refuses it, naming both, when:

- its free disk under `storage.path` is below the first read's record bytes
  times 5 (what the store takes per record byte) plus 1 GiB, or
- the records it holds plus the first read pass `subscriptions.record_ceiling`
  (default 32,000,000: the store engine's ceiling in its 2 GiB heap).

`--force` subscribes anyway. A producer before 1.2.0 states no lane: the
subscription is refused (`--force --start beginning` follows it from the
beginning, unsized).

## Where subscriptions are kept

`<storage.path>/follows.dss`: one size-prefixed `$DSS` record per lane, with
the producer (`PROVIDER_PEER_ID`), the cursor to resume at (`CURSOR`), when it
was subscribed (`LAST_SYNC_STARTED_AT`) and what has been received. The cursor
is saved only after the store has taken a page, so a restart resumes with
nothing lost; a record sent again is the same content address and is stored
once. A subscription that waits to start at the producer's current batch
carries `SYNC_FILTER` `start=current` until its cursor is saved.

## Updating a node from 1.1.9

A node updated from 1.1.9 converts its import gate on its first start, with
nobody logged in:

- each lane of `trusted-import-lanes.json`, and each allow-list entry that
  names a standard, a provider and a source, becomes a subscription from the
  peer the store names as the producer of its records (when this node trusts
  it), or from the peer the allow entry names;
- each converted subscription starts at the producer's current batch,
  whatever its rule: the old gate imported each publication as it came, so
  history the node never held is not read. The bound is logged per lane when
  the producer states it;
- lanes this node produces are left out; a lane with no known producer, and
  an allow entry that does not name one lane, are logged and raised as the
  alert `subscription_conversion`;
- the ledger is renamed `trusted-import-lanes.json.converted`, and
  `<storage.path>/trusted-import-converted` lists what became of each lane.
  The conversion runs once.

Until the producer runs 1.2.0 too, a converted subscription waits (its
`last error` names the missing statement) and starts once the producer
states the lane. A later start with `peers.trusted_import` still in the
config logs that it is ignored and raises `subscription_conversion`; remove
the key. A new node (an empty storage path) whose config carries the key
refuses to start, naming `sync subscribe`.

## The API

| Route | Who | What |
|---|---|---|
| `POST /api/v1/sync[?start=current\|beginning][&force=1]` | admin | one size-prefixed `$DSS`, `REQUESTED_ACTION` Subscribe with `PROVIDER_PEER_ID`: subscribe (202), or 409 `first_read_refused`, 400 for a producer that is not trusted |
| `POST /api/v1/sync` | admin | Unsubscribe, with or without `PROVIDER_PEER_ID` |
| `GET /api/v1/sync/plan?schema=&provider_id=&source=&from=[&start=]` | admin | the first read, as JSON, without subscribing |
| `GET /api/v1/sync/subscriptions` | admin | the followed lanes, as JSON |
| `GET /api/v1/sync/ledger` | admin | the 1.1.9 ledger's and allow list's lanes and their producers, as JSON |
| `GET /api/v1/sync[/{schema}/{provider}/{source}]` | anyone | `$DSS` per lane: a followed lane reads `SUBSCRIBED`, `SYNC_PROTOCOL` `flatsql-sync read-chunk follow`, `TOTAL_ROWS` the producer's current batch |

The `$DSS` `Sync` action is refused: a followed lane is always current.

On the wire, the statement is an unfollowed `read-chunk` naming the lane and
no batch, with the filter `PROJECTION` = `STATEMENT`; the answer is one `$QRP`
page with `BATCH_ID`, `TOTAL_COUNT` (the batch's records), `RESUME_CURSOR`
(the present position) and one row of `BATCH_RECORDS`, `BATCH_BYTES`,
`LANE_RECORDS`, `LANE_BYTES`.
