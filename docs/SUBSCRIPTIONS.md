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
announcements, the five-minute catch-up of published shards and
`security.require_signed_feed_heads`. A signed `$PNM` publication from a peer
still imports, but only for a lane this node subscribes to from that peer,
and only while the lane's followed stream is not connected: a lane whose
stream is connected receives the publication's records on the stream, and
its shard is not fetched again (except under `archive-all`, which keeps each
publication's DAG). A lane whose stream the producer refuses (a
standard off its public read surface, such as `CNP` or `IQC`, to a peer it
grants no private reads) imports through its publications.

A record gossiped on a schema topic (`POST /api/v1/pubsub/publish`, the
sdn-js `publish`, a module's `pubsub.publish`) names no lane, so no
subscription admits it, and a node stores none, whatever its trust of the
sender and `network.accept_push_from`. The node's own identity (`$EPM`) and
its dataset publications travel as signed `$PNM` announcements, which a node
still stores from a peer it trusts.

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
subscribed, where it started (`start`; `pending_start` while it waits for
the producer's statement), why it is stopped (`stopped`), whether it is
connected, records and bytes received, records
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

`--force` subscribes anyway. A producer before 1.2.0 (or one that serves the
lane's stream only to peers it grants private reads) states no lane: the
subscription is refused; `--force` subscribes at the producer's current
batch, which it starts at once the producer states the lane, and `--force
--start beginning` follows the lane from its beginning, unsized.

## While a lane is followed

The check above runs once, before the subscription. While the lane is
followed, the node checks each page before it stores it, and each fetch of
the current batch's records by content address before it reads them (for
the most bytes one page can hold, 8 MiB): a page that would leave less than
1 GiB of free disk under `storage.path` (at 5 bytes per record byte), or
take the records the node holds past `subscriptions.record_ceiling`, is not
stored. What the other lanes are storing at the same time counts against
both: the lanes share one reservation. The lane stops, its cursor
kept; `sync subscriptions` shows why (`stopped`) and the alert
`follow_stopped` names the lane. The node checks again every 5 minutes and
the lane resumes at its cursor once the page fits (free disk, or a raised
ceiling and a restart). `--force` does not skip this check.

A producer refuses a saved cursor when its record sequence was renumbered (a
rebuilt index, a store re-derived from its journals, a wiped store). The
lane then starts again at the producer's current batch, never at the lane's
beginning, and the alert `follow_cursor_refused` names it. For a lane whose
history the node keeps (one subscribed from its beginning, or a `keep-all`
or `archive-all` lane) the records the producer stored between the last
position held and its current batch are not read. That is kept with the
subscription (`unread`, `unread_since` in `sync subscriptions --json`, "not
read" in its text), across restarts, with the alert raised again at each
start, until `sync subscribe --start beginning` (same lane and producer)
reads the whole lane again or `sync subscribe --acknowledge-unread` accepts
the gap. A lane subscribed from its beginning stays in `follows.dss`
meanwhile.

A lane whose stream is connected does not import its `$PNM` publications
(their records arrive on the stream) once the batch the producer states is
held as current. Until then, or while holding it fails (its list cannot be
read, or its records fetched by content address are refused), the lane's
publications import as they come.

A producer serves a peer it does not list as trusted at most 8 followed
streams (one per lane), and all such peers together 64; a peer it lists as
trusted gets 16, outside those 64. A node that follows more than 8 lanes of
one producer is listed by that producer in `peers.trusted_peers`, unless the
producer may ever run 1.1.9 again (a rollback): a 1.1.9 node imports every
lane of a peer it trusts. A followed stream past these limits is refused
busy and asked for again. Records fetched from a producer are taken only as
that producer's own: a run that names another peer as origin or producer is
refused, so a producer whose stored records name an earlier identity of it
serves its stream but not its current batch.

## Where subscriptions are kept

`subscriptions.dss`, in the directory that holds `storage.path` (beside it, not in it; where 1.1.9 keeps `follows.dss`): one size-prefixed `$DSS` record per lane,
with the producer (`PROVIDER_PEER_ID`), the cursor to resume at (`CURSOR`),
where it starts (`SYNC_FILTER` `start=current` or `start=beginning`, with
`unread=<time>` while a refused cursor's gap is not acknowledged, and
`subscribed=beginning` for a lane so started), when it
was subscribed (`LAST_SYNC_STARTED_AT`) and what has been received. The cursor
is saved only after the store has taken a page, so a restart resumes with
nothing lost; a record sent again is the same content address and is stored
once.

`follows.dss`, beside it, holds, in the same form, only the subscriptions
that start at the lane's beginning. It is the file 1.1.9 reads: 1.1.9 follows
each of its records from its cursor, and from the lane's beginning when it
has none, so a subscription that starts at the producer's current batch is
never in it, and a node rolled back to 1.1.9 does not read such a lane from
its beginning. At start, a record of `follows.dss` that `subscriptions.dss`
lacks (a 1.1.9 follow) becomes a subscription from the lane's beginning; a
lane `subscriptions.dss` holds is kept as it holds it, whether or not
`follows.dss` does. The node writes `follows.dss` first, then
`subscriptions.dss`, so a crash between the two writes cannot bring an
unsubscribed lane back from `follows.dss`. An unsubscribe made while the node
ran 1.1.9 (a rollback) removes the lane from `follows.dss` only: it comes
back with the next 1.2.0 start; unsubscribe again then.

A file that does not read at start is moved aside
(`subscriptions.dss.unreadable-<time>`, likewise `follows.dss`), never
overwritten. The node then imports no lane and refuses every change to its
subscriptions, and the alert `subscriptions_unreadable` names the file; a
restart that finds the file still missing stays so. Put a readable copy back
(a backup) and restart, or remove the moved file to start with no
subscriptions.

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
- lanes this node produces are left out; a lane with no known producer, an
  allow entry that does not name one lane, and an allow entry that names a
  peer this node does not trust are logged and raised as the alert
  `subscription_conversion`, and not subscribed;
- `<storage.path>/trusted-import-converted` lists what became of each lane,
  each removed key the config carries, and each deny entry (nothing is
  imported without a subscription, so nothing is denied). The conversion runs
  once;
- a ledger that cannot be read converts nothing and writes no record: the
  alert `subscription_conversion` names the error, and the next start tries
  again.

The ledger and the config are left as they are. An update that fails its
health check rolls back to 1.1.9 by itself, and 1.1.9 imports through them
again; the converted subscriptions are in `subscriptions.dss`, which 1.1.9
does not read, so it does not read their lanes from the beginning. When the
node runs 1.2.0 again it finds the conversion record and the subscriptions
as it left them, and 1.1.9's follows with their newest cursors.

Until the producer runs 1.2.0 too, a converted subscription waits (its
`last error` names the missing statement) while the lane's `$PNM`
publications import, and starts at the producer's current batch once the
producer states the lane. Every start with a removed key in the config
(`peers.trusted_import` and its parts, `security.require_signed_feed_heads`)
names each one in the log and in the alert `subscription_conversion`; remove
them. A new node (an empty storage path) whose config carries
`peers.trusted_import` refuses to start, naming `sync subscribe`.

## The API

| Route | Who | What |
|---|---|---|
| `POST /api/v1/sync[?start=current\|beginning][&force=1]` | admin | one size-prefixed `$DSS`, `REQUESTED_ACTION` Subscribe with `PROVIDER_PEER_ID`: subscribe (202), or 409 `first_read_refused`, 400 for a producer that is not trusted; a `RETENTION` in the frame decides the default start and is set on the lane only when the subscription is made |
| `POST /api/v1/sync?acknowledge_unread=1` | admin | Subscribe naming an existing subscription: clears what a refused cursor left unread on it (`sync subscribe --acknowledge-unread`) |
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
