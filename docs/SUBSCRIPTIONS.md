# Subscriptions: how a node imports lanes

From 1.1.10 a node imports only the lanes it subscribes to. A lane is one
standard, provider and source (`OMM` `celestrak.eth` / `celestrak-gp`); a
subscription names a lane and the producer peer to take it from. The node
follows the lane over a live stream (`read-chunk` with `FOLLOW` on
flatsql-sync): records arrive within seconds of the producer storing them.
The producer must be in `peers.trusted_peers`, and the node must be able to
dial it (`network.bootstrap`).

Removed in 1.1.10: `peers.trusted_import` (the allow and deny lists and
`new_lanes`), the lane ledger `trusted-import-lanes.json`, the feed-head
announcements, the five-minute catch-up of published shards and
`security.require_signed_feed_heads`. A signed `$PNM` publication from a peer
still imports, but only for a lane this node subscribes to from that peer,
and only while the lane's followed stream is not connected: a lane whose
stream is connected receives the publication's records on the stream, and
its shard is not fetched again (except under `archive-all`, which keeps each
publication's DAG). A lane whose stream the producer refuses (a
standard off its public read surface, such as `CNP` or `IQC`, to a peer it
grants no private reads) imports through its publications. A lane subscribed
with `--publications only` opens no stream and imports through its
publications alone (below).

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

# also keep the lane's publications (a gateway host); on a followed lane it
# changes only that
spacedatanetwork sync subscribe --schema OMM --provider-id space-data-network-02 \
  --source-name celestrak-gp --from <producer peer id> --publications keep

# import the lane from its publications alone, with no stream (a large
# keep-all lane); on a subscribed lane it changes only that
spacedatanetwork sync subscribe --schema OMM --provider-id space-data-network-02 \
  --source-name celestrak-gp --from <producer peer id> --publications only

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
batch the store does not hold, read from the store at each listing, so
records removed since the batch was held count until they are held again)
and the last error. "received" and "stored" count what the stream delivered
since the lane started; they are not the lane's size.

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

`--start current` or `--start beginning` overrides the rule's start. A
`--publications only` subscription has no start and no first read: `--start`
is refused with it.

The lane's records and bytes the producer states are its batch memberships,
the sums of the counters of the lane's batches: a record two of the lane's
batches hold counts in each, so on a keep-all lane whose batches carry most
records over they can be several times its distinct records (3.6 times on a
copy of the production catalog's `OMM` lane). `sync subscribe` prints them as
"records counted per batch". The current batch's figures are exact.

Before it subscribes the node compares the first read with what it can take,
and refuses it, naming both, when:

- its free disk under `storage.path` is below the first read's record bytes
  times 5 (what the store takes per record byte) plus 1 GiB, or
- the records it holds plus the first read pass `subscriptions.record_ceiling`
  (default 32,000,000: the store engine's ceiling in its 2 GiB heap).

`--force` subscribes anyway. A producer before 1.1.10 (or one that serves the
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
stored. A lane's `$PNM` publications are imported under the same check,
for the records the signed manifest states and the shard's bytes, before
the shard is fetched; a stated size no disk can hold (past 2^62 bytes of
store) stops the lane like any other. The stated sizes also bound the
import: the shard and the index are fetched up to the bytes the manifest
states and no further, an index listing more records than it states stores
none, and a manifest stating no records or a shard or index of 0 bytes is
refused before the fetch. Such a publication raises the alert
`publication_misstated` on the lane, naming the publication, the producer
and both figures, and is recorded as a permanent error (not fetched again).
What the other lanes are storing at the same time
counts against both: the lanes and the publication imports share one
reservation, released when each store returns, also when it fails. The
lane stops, its cursor kept; `sync subscriptions` shows why (`stopped`) and
the alert `follow_stopped` names the lane. The node checks again every 5
minutes (a publication: at the next stored-`$PNM` catch-up) and the lane
resumes once the page fits (free disk, or a raised ceiling and a restart).
`--force` does not skip this check. `GET /api/v1/sync/plan` and `sync
subscribe` show what is being stored at the moment (`reserved_records`,
`reserved_bytes`).

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
meanwhile. `sync unsubscribe` clears the lane's alerts (`follow_cursor_refused`,
`follow_stopped`, `follow_refused`, `subscription_refused`,
`publication_misstated`).

A lane whose stream is connected does not import its `$PNM` publications
(their records arrive on the stream) once the batch the producer states is
held as current. While holding it fails (its list cannot be read, or its
records fetched by content address are refused), or the producer refuses
the stream, the lane's publications import as they come. After the lane
starts (a restart, a new subscription), until its stream has held the
stated batch or failed to, and for at most 5 minutes, a publication waits:
the stored-`$PNM` catch-up takes it up again as soon as the stream settles,
so a batch the stream holds again after a restart is not imported a second
time. The 5 minutes count from the lane's start: a catch-up runs when they
end, whatever else ran it in between, so a lane whose stream never connects
imports its waiting publication 5 minutes after it starts.

A subscribed lane's current batch is its producer's: the batch the stream
holds as the producer states it, or, on a `--publications only` lane, the
publication imported last, whose retention pass runs as it lands. The
node's own publication ledger never decides it: that ledger holds only the
publications the node made itself and those a 1.1.9 node imported, and
their shard files can outlive their records. The retention pass at each
start leaves subscribed lanes alone, and a publication imported on a lane
the stream follows leaves its retention to the stream. Up to 1.1.15 that
pass judged a subscribed lane from the ledger, kept an old batch whose
files were still on disk, and removed the current batch's records at every
start.

The producer states a batch while it may still be storing it: records
carried over from its previous batch are tagged to the new batch in place
and never come on the stream, so the stream is quiet and its statement
counts the batch as it was then. A list of the batch longer than the
statement allows (its count and one list page more) asks the producer's
statement again; when it states the same batch with more records, the list
goes on under the new count. Otherwise the list is refused (alert
`follow_refused`).

A failed hold of the stated batch (its list read, its records fetched) does
not end the stream. The batch is asked for again under the stream's backoff:
at least 30 s after the last ask, and after consecutive failures 1 s doubling
up to 20 minutes, each wait drawn within 20% of it; a hold that succeeds
starts it again. `last error` in `sync subscriptions` names the next ask. A
producer's `busy` refusal is waited at least its retry hint, at most
20 minutes.

So a followed lane's node holds its records but not its publications' files
(the producer's signed manifests, shards and indexes). A node whose readers
fetch those files from it, such as a gateway host that serves a producer's
publications to browsers over Bitswap, subscribes with `sync subscribe
--publications keep`: each publication of the lane is then also fetched and
imported, as 1.1.9 imported every publication, and the lane's retention rule
decides which publications stay (`replace-current` keeps the current
batch's). On a lane already followed, `--publications keep` (or `stream`)
changes only that; the stream and its cursor stay. Turned on for a running
lane, the stored-`$PNM` catch-up runs at once and reads the publications it
had passed over. `sync subscriptions` shows such a lane with "publications:
kept" (`keep_publications` in `--json`).

### A lane imported from its publications alone

`sync subscribe --publications only` (API `POST /api/v1/sync?publications=only`)
subscribes to a lane that opens no stream: the node asks the producer nothing
for it (no statement, no batch list, no page, no record by content address)
and imports each publication of the lane through the `$PNM` path as it comes,
at once, with the lane's retention rule applied as under `--publications
keep`. At start the stored-`$PNM` catch-up imports the newest publication of
the lane that the node received and had not imported. A publication the node
did not receive (made while it was stopped or out of reach) is not fetched
afterwards: the lane is current again at the producer's next publication, and
under a keep-all rule the records only that missed batch carried are not
imported. A `sync subscribe` for the lane without `--publications` is
refused, so the producer is not asked for its statement by mistake. Each publication is imported under the running guard
above (the shared reservation, the free disk reserve, the record ceiling,
the import's size caps); there is no first read to check before the
subscription, and `--start` is refused with it. `sync subscriptions` shows
such a lane as "publications only, no stream" (`publications_only` in
`--json`); its `$DSS` reads `SYNC_PROTOCOL` `dataset publications ($PNM)`.

Use it for a large keep-all lane of a producer that publishes it, such as
the production catalog lanes (`OMM` and `MPE` `space-data-network-02` /
`celestrak-gp`: 10 to 12 million records in hundreds of batches). Following
such a lane as a stream is not cheap for its producer: each time a follower
holds a new batch, every page of the batch's list (8,192 records) walks the
whole lane (10 to 16 s a page on a copy of the production store). It runs
in one of the slots the producer gives other peers' reads, on an
interactive engine lane, so the producer's own publication keeps its lanes,
and it stops when the follower leaves (see "What a follower costs its
producer" below). A publication costs the producer nothing per follower:
its manifest, shard and index are fetched over IPFS. The lane's records then
arrive with each publication (every 3 hours on the catalog), not within
seconds of the producer storing them.

On a subscribed lane `--publications only` changes only that: the stream
stops, and the cursor is kept in the file. `--publications keep` or
`stream` on a publications-only lane starts the stream at the producer's
current batch, as a new subscription started there does (the publications
carried the lane meanwhile); the subscription and its counts stay.

A node that has imported a lane's publications also lists them (`list-published`) and serves their files (`read-shard`, `read-index`). Each row is listed `available` while its shard and index are on the node's disk. Lane retention removes the files of the batches it no longer keeps as current. When a file is gone but the node's IPFS repository still holds that content in full, with every block of its DAG local and a shard of the published size, the row is still listed `available`, and both reads serve the bytes from the repository. A Bitswap reader that falls back to the publication before the newest then finds it, for as long as the node holds it, whether that is because of a pin or because the repository collects no garbage. A row that is neither on disk nor fully in the repository is listed `unavailable` (`shard_missing` or `index_missing`). After 24 hours it is retired from the listing.

A node with its DHT off (`peers.enable_dht: false`) has no content routing,
and Kubo's Bitswap asks for a block only the peers that served it blocks
before, its LAN peers and its peered peers (broadcast control, on since Kubo
0.37). A producer it is merely connected to is never asked, so its fetches by
content address (a kept publication's shard) and its gateway's would time
out. Such a node therefore adds each producer it follows a lane from to
Kubo's peering service when the followed stream opens (for a
publications-only lane, once it knows the producer's addresses), which keeps
the connection and makes Bitswap ask it, and removes it when no lane is
subscribed from that producer. A producer in the Kubo repository's `Peering.Peers` is
left as it is. `swarm/peering/ls` on the node's Kubo API lists them.

A producer serves a peer it does not list as trusted at most 8 followed
streams (one per lane), and all such peers together 64; a peer it lists as
trusted gets 16, outside those 64. A node that follows more than 8 lanes of
one producer is listed by that producer in `peers.trusted_peers`, unless the
producer may ever run 1.1.9 again (a rollback): a 1.1.9 node imports every
lane of a peer it trusts. A followed stream past these limits is refused
busy and asked for again. Records fetched from a producer are taken only as
that producer's own: a run that names another peer as origin or producer is
refused, so a producer whose lane tags name an earlier identity of it serves
its stream but not its current batch. On a followed page and a fetch by
content address the producer names as each record's origin the producer its
lane tag names, not the peer it first stored the record's bytes from: a
node stores a record once per content address, so a record its own lane
holds may have reached it first from another peer (from the follower
itself, when it once followed or imported a lane of the follower). Up to
1.1.15 it named that first peer, and the follower refused the whole batch
over a few such records. Records the producer holds only in another
producer's lane name that producer and are still refused.

What a follower costs its producer: a lane's statement is answered from the
producer's counters, never by reading the lane's records, so it costs the
lane's batches and not its records (the lane's figures it states are batch
memberships, as above). A followed stream from the lane's beginning reads
forward from the start without counting the lane first.

A statement, a followed page, a page of a batch's list, a fetch by content
address and a `read-chunk` scan each take one of the producer's slots for
other peers' reads while they read its store, and one peer holds at most 1
slot, so a follower of many lanes holds one at most. Peers the producer
trusts (`peers.trusted_peers`, the trust that gives a follower the trusted
stream limits above) take any free slot and have 1 more of their own; all
other peers share 2 (1 on a host of 1 core). Throwaway peer IDs that keep
those 2 busy so never keep a trusted follower waiting, and at most 3 such
reads run at once (2 on a host of 1 core). The store engine (FlatSQL
3.12.14) serves reads on 4 interactive lanes, 1 bulk lane and 1 sandbox
lane whatever the host's cores (the cores set only its writer threads), and
all these reads run on interactive lanes: the node's own reads (its
publications' window probes and fingerprint scans, its API) always have an
interactive lane and the bulk lane, and ingest is written apart from every
read. A statement waits up to 20 seconds for a slot (past one 10 to 16 s
list page of its own follower), then is refused `busy`; the others wait for
a slot within their bound.

Each of these reads runs under its stream: when the peer resets the stream
or the connection closes, the producer cancels its engine read, and the
engine stops it. On a lane of 8 million tag rows, four reads of a batch's
list (2.8 to 3.0 s each when run alone to its end) canceled at a quarter of the way
returned 1 ms after the cancel, and a read asked right after was served at
once (the engine's 4 interactive lanes were free). Each read has a bound (a
statement 1 minute, its wait for a slot included; a page, a page of a
batch's list, a fetch by content address or a scan 90 seconds, the wait for
a slot included), past which the producer answers `busy`. On a store of
another format than 5 a read does not watch its stream: it runs to its end,
holding its slot, and is answered `busy` then if it passed its bound. A
follower that gives up on a read, or whose subscription is removed or
stopped while it reads, resets its stream. A peer that closes its side of
the stream after its request (as `sdn-js` and the node's remote-records
reads do) is still answered, and its read stops when its connection
closes; a reset after that close is not seen by the producer (the stream's
read side has ended), so such a read otherwise runs to its end or its
bound. The same holds while such a request waits for a slot: if its peer
resets the stream meanwhile and keeps the connection, the request still
takes the slot when one frees and reads for nobody, to its end or its
bound. Each page of a followed stream also reads the lane's statement from
the lane's counters, without a slot.

A follower waits up to 5 minutes for a statement and for each page. After a
stream that delivered no page it waits before opening the stream again: 1
second, doubling after each such stream up to 20 minutes, each wait drawn
within 20% of that, and at least the retry hint of a `busy` refusal. A
stream that delivers a page starts the wait at 1 second again. Meanwhile the
lane's `$PNM` publications import, as above.

## Where subscriptions are kept

`subscriptions.dss`, in the directory that holds `storage.path` (beside it, not in it; where 1.1.9 keeps `follows.dss`): one size-prefixed `$DSS` record per lane,
with the producer (`PROVIDER_PEER_ID`), the cursor to resume at (`CURSOR`),
where it starts (`SYNC_FILTER` `start=current` or `start=beginning`, with
`unread=<time>` while a refused cursor's gap is not acknowledged,
`subscribed=beginning` for a lane so started, `publications=keep` for a
lane that keeps its publications, which a build before 1.1.11 does not know
and so holds without running, and `stream=none` for a lane imported from its
publications alone, which a build before 1.1.13 does not know and so holds
without running), when it
was subscribed (`LAST_SYNC_STARTED_AT`) and what has been received. The cursor
is saved only after the store has taken a page, so a restart resumes with
nothing lost; a record sent again is the same content address and is stored
once.

`follows.dss`, beside it, holds, in the same form, only the subscriptions
that start at the lane's beginning, and never a publications-only one (1.1.9
would follow it as a stream). It is the file 1.1.9 reads: 1.1.9 follows
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
back with the next 1.1.10 or later start; unsubscribe again then.

A file whose bytes do not decode at start is moved aside
(`subscriptions.dss.unreadable-<time>`, likewise `follows.dss`), never
overwritten. The node then imports no lane and refuses every change to its
subscriptions, and the alert `subscriptions_unreadable` names the file; a
restart that finds the file still missing stays so. Put a readable copy back
(a backup) and restart, or remove the moved file to start with no
subscriptions. A file whose read fails (permission, an I/O error) is left
where it is: the node stays so, the alert names the error, and the next
start that reads the file loads it.

A subscription whose `SYNC_FILTER` this build cannot read (written by a
later version, then rolled back) is not run: a field it does not know, a
value of a known field it does not read (`start=<a later start>`), or a
filter that does not parse. The alert `subscription_refused` names the lane
and the field or the filter, the lane is neither followed nor imported, and
its record is written back byte for byte as each file held it (its status,
times, rates and `ERROR` included; the refusal is in the alert and in `sync
subscriptions`, not in the file). The other lanes run. To subscribe the lane again with this build, unsubscribe it
first.

A publications-only subscription is written with its own field (`stream=none`),
not as a value of `publications`: 1.1.10, 1.1.11 and 1.1.12 hold a field they
do not know as above, but 1.1.11 and 1.1.12 read an unknown value of
`publications` as a file that does not decode, which sets the whole file
aside. Rolled back to any of them, such a subscription is not followed as a
stream and its publications are not imported until the node runs 1.1.13 or
later again; the other lanes run.

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
node runs 1.1.10 or later again it finds the conversion record and the subscriptions
as it left them, and 1.1.9's follows with their newest cursors.

Until the producer runs 1.1.10 or later too, a converted subscription waits (its
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
| `POST /api/v1/sync?publications=keep\|stream\|only` | admin | Subscribe that keeps (or stops keeping) the lane's publications, or (`only`) imports the lane from its publications alone with no stream and no start (`start` is refused with it); naming an existing subscription, it changes only that (`sync subscribe --publications`) |
| `POST /api/v1/sync` | admin | Unsubscribe, with or without `PROVIDER_PEER_ID` |
| `GET /api/v1/sync/plan?schema=&provider_id=&source=&from=[&start=]` | admin | the first read, as JSON, without subscribing |
| `GET /api/v1/sync/subscriptions` | admin | the followed lanes, as JSON |
| `GET /api/v1/sync/ledger` | admin | the 1.1.9 ledger's and allow list's lanes and their producers, as JSON |
| `GET /api/v1/sync[/{schema}/{provider}/{source}]` | anyone | `$DSS` per lane: a followed lane reads `SUBSCRIBED`, `SYNC_PROTOCOL` `flatsql-sync read-chunk follow` (`dataset publications ($PNM)` for a publications-only lane), `TOTAL_ROWS` the producer's current batch |

The `$DSS` `Sync` action is refused: a followed lane is always current.

On the wire, the statement is an unfollowed `read-chunk` naming the lane and
no batch, with the filter `PROJECTION` = `STATEMENT`; the answer is one `$QRP`
page with `BATCH_ID`, `TOTAL_COUNT` (the batch's records), `RESUME_CURSOR`
(the present position) and one row of `BATCH_RECORDS`, `BATCH_BYTES`,
`LANE_RECORDS`, `LANE_BYTES`. `LANE_RECORDS` and `LANE_BYTES` count batch
memberships, not distinct records: a record two of the lane's batches hold
counts in each.

A producer logs each read a follower asks of a lane, with what it took:
`flatsql-sync: statement of …`, `batch list page of … batch <id>`, `records by
CID of … batch <id>`, `followed stream opened of …`, each `for <peer>`.
