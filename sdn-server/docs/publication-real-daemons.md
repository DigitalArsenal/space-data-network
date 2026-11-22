# Publication between real daemons

`./scripts/ci-local.sh publication` runs the publication tests against real
node processes. Every process has a separate home, store, password file,
identity, and loopback listener. The receiver bootstraps only from the
producer. DHT and mDNS discovery are disabled. The tests compare digest
multisets from the receiving and producing record APIs, including duplicate
counts, and inspect publication status, refusal reasons, and retention pins.

`SDN_E2E_PUBLICATION_BINARY` selects a prebuilt executable. Without it the
test builds the executable through `scripts/go-with-wasmedge.sh`.
`SDN_E2E_PUBLICATION_STORES` selects the parent of temporary test stores;
`SDN_E2E_PUBLICATION_LOGS` preserves daemon logs outside those stores. Test
cleanup stops the exact child processes and removes their stores.

The KMF test supplies two generated X25519 keys before startup. It checks
local read APIs, published shard bytes, stored sealed envelopes, opening with
each recipient key, and plaintext occurrences across the entire store. Key
bytes are excluded from test output. Publication exposes plaintext KEY_BYTES
in Kubo block files on both nodes, although their record frames remain sealed.
The two whole-store plaintext assertions are named, skipped defect
reproductions. `SDN_E2E_PUBLICATION_REPRO_PLAINTEXT=1` executes their failing
assertions. The producer store is also checked before publication.

## Baseline command

Run `./scripts/publication-baseline.sh RESULT_DIRECTORY`. The command builds
and starts two isolated daemons per repetition and emits JSON. It preserves
individual measurement rows, a test log, and an exit code in the selected
directory. A nonzero exit code means the result is incomplete.

`SDN_E2E_PUBLICATION_BASELINE_SMOKE=1` selects 2,048 OMM records and four OEM
records for one repetition of instrumentation validation. These rows carry
`instrumentation_smoke: true` and are not full-size baselines.
`SDN_E2E_PUBLICATION_BASELINE_CASE=wire` selects only the wire probe.

The full data sets contain 2,000,000 distinct synthetic OMM records and 1,024 OEM
records with 21,846 six-component ephemeris states each. The OEM array alone
occupies 1,048,608 bytes per record. Three repetitions report rates, CPU
seconds, sampled resident memory, machine load, store file sizes, restart
latency, and read latency during another ingest. Every rate carries the
250,000,000 bytes per second target.

The wire probe and standalone shard read use the receiving daemon's Kubo
stream forwarder, with private-read authority granted to that test peer. The
forwarder adds a loopback TCP copy to the measurement. Download-only reads
every published shard after import and therefore measures warm caches. The
primary publication duration and CPU/RSS counters include export and API
completion. Separate announcement rates use the first successful PNM publication
call and the receiver log line at which the records it reports imported
reach the published count (read every 100 milliseconds); one count afterwards
confirms the total. A count over a large lane during import is a full scan
that can take minutes, so it is not used as the poll; each row reports how
long the confirming count took. The opt-in
`SDN_E2E_PUBLICATION_OBSERVATIONS` file records only timestamps, schemas, and
manifest CIDs; it is unset during ordinary operation. Raw observations accompany
the daemon logs. Per-operation RSS is sampled every 200 milliseconds. CPU
counters are `ps` process CPU time.
The SQL reads use an EPOCH window for OMM and a CREATION_DATE window for OEM.
The OEM query projects MESSAGE_ID and CREATION_DATE, so its result does not
include up to 100 MiB of ephemeris payloads under the ordinary result byte cap.
Synthetic input files are generated before timed ingest; reported byte counts
are the exact framed input sizes. These distinctions are recorded alongside results and limit their
interpretation.

Process-exit rows also report the operating system peak RSS for each daemon
lifetime; receiver restarts produce separate peak rows.

Archive record retention and shard/index/manifest pins remain mandatory. The
`known_unpinned_announcements` subtest retains the observed failure that both
advertised PNM CIDs remain unpinned after six minutes. Set
`SDN_E2E_PUBLICATION_REPRO_ARCHIVE_PNM=1` to execute that assertion. Its cause
has not been established.

The full benchmark raises only the test writers' storage quota to 32 GiB so
the requested input fits; the ordinary per-writer default is 100 MiB. The store
cap remains 32 GB. No storage or publication algorithm is tuned.

Set `SDN_E2E_PUBLICATION_BASELINE_DATASET=OMM` or `OEM` to run the three full
repetitions for one dataset independently; unset selects both. This permits
independent measurements when a failure in one dataset blocks the combined run.

`SDN_E2E_PUBLICATION_BASELINE_CASE=direct` measures direct ingest independently
of publication, including the producer's allocated and logical storage sizes.
`SDN_E2E_PUBLICATION_BASELINE_OMM_COUNT` sets the OMM record count (1 through
2,000,000; unset is 2,000,000) so two builds can be compared at a smaller size.
`SDN_E2E_PUBLICATION_BASELINE_REPETITIONS` accepts 1 through 3; unset retains
three full repetitions (one in smoke mode). Each summary records the selected
case, dataset, and repetition count so partial reruns remain distinguishable.
`SDN_E2E_PUBLICATION_BASELINE_NO_RESTART=1` ends each dataset after the
publication and download measurements: no receiver restart and no second
ingest with reads.

`TestPublicationFingerprintRealDaemons` publishes 150,000 element sets and
compares the set fingerprint the producer logs ("publication fingerprint")
with the SHA-256 of the lane header and the sorted CIDs of the records the
test sent, and checks that the series' three 50,000-record windows are read
by CID from the plan that fingerprint read made ("Dataset publication
window"). The read's sort files are written under the producer's store
directory (`publication-sort`), none is left after the publication, and one
left there before a restart is gone once the store opens.

`TestPublicationOutlivesClientRealDaemons` publishes a 150,000-record batch
for a client that gives up after 300 ms, as a flow's HTTP request gives up at
its deadline: the publication completes on its own context and the receiver
holds the batch byte for byte. A second request for a batch whose
publication is running waits for that publication's result, and the batch
is read for publication once. Shard and index files that no publication row
names (what a publication that never completed leaves) are removed once a
publication completes; the published ones stay.

`TestPublicationUpdateRestartWaitsRealDaemons` runs the producer from a
bundle and makes the update helper's shutdown handshake while a
150,000-record batch is being published. The node answers that it waits at
most 30 minutes (`waitBeforeStopSeconds` 1800) for its one running
publication (`runningPublications` 1); `spacedatanetwork status` prints
`update_stop_wait=dataset-publications running=1`; a publication of another
batch requested meanwhile answers HTTP 503 and is kept. The running
publication completes and answers its client before the node logs
"Shutting down...". Started again, the node publishes the refused batch by
itself and does not read the completed one again.

`TestPublicationInterruptedRedoneRealDaemons` stops the producer (SIGINT)
while a 150,000-record batch is being published. At the next start the node
logs the publication as failed at the step "interrupted by the node's stop",
publishes the batch again (the batch is read for publication twice in all:
the interrupted run and its redo; deciding to redo it reads no record) and
the lane's `publication_failed` alert is clear once it completes; a further
restart publishes nothing again.

`TestPublicationInterruptedSupersededRealDaemons` interrupts the publication
of a 150,000-record batch, then starts the producer from a build
(`sdn_e2e_follow_faults`) that handles the mark of a `lateredo-` lane 60 s
after it is ready, publishes a newer batch of the lane meanwhile, and stops
it. At the next start the node logs that the older batch is not published
again because the lane published the newer one after it, and removes the
mark: the older batch is never read for publication again and never
completes, and the lane has no alert.

`TestPublicationInterruptedCrashBoundRealDaemons` runs a build
(`sdn_e2e_follow_faults`) whose publications of a `crash-` lane end the
process. The publication of a batch ends it; at each of the next three
starts the mark's attempt count (1, 2, 3) is written before the attempt and
the process ends again. At the fourth start the node reports the
publication FAILED, given up after 3 attempts, raises the lane's
`publication_failed` alert and removes the mark; a fifth start handles
nothing.

`TestPublicationFailureRealDaemons` runs the CelesTrak GP ingest flow of
`space-data-network-modules` (`SDN_E2E_MODULES`, the modules checkout; the
test fails when it is set and the flow is missing, and skips when it is not
set) against a local copy of the parser's GP fixture, on a node whose
publication export cannot create its shard directory (a file stands at its
path, which stops root too). The failure is logged as an error naming the
lane, the batch and the step, raises the lane's `publication_failed` alert,
and the flow's run ends with an error. A publication of another batch of the
lane and an announcement of its existing publications complete and leave the
alert raised; a publication of the failed batch clears it.

`TestPublicationSlowFlowRealDaemons` runs the same flow by hand while a
300,000-record publication of the same lane runs, on a node whose admin
write timeout is 3 s (`SDN_ADMIN_WRITE_TIMEOUT`) and whose http-request
capability waits at most 2 s for any other request
(`SDN_HTTP_REQUEST_MAX_TIMEOUT`). The node answers the flow's publication
request only after the big publication, later than both. The flow waits for
the node's answer: its run ends without error, and its batch's publication
is complete.

`TestPublicationFollowRealDaemons` follows a lane with nothing published: the
receiver, which trusts the producer and keeps the lane under
`replace-current`, subscribes with a `$DSS` Subscribe that names the producer
(`PROVIDER_PEER_ID`), receives the stored records and then live ones over the
followed read-chunk stream, keeps the subscription as a `$DSS` record in
`subscriptions.dss`, replaces the older batch with the newer one while the producer
keeps both, resumes after its own restart and after the producer's with every
record held once, and drops the records the producer removed. A producer the
receiver does not trust is refused with no stream opened. A test host
speaking the protocol, trusted by the receiver, checks the refusals: a run
whose header names another peer as its origin, a `RECORD_COUNT` of
0xFFFFFFFF and an endless run of maximum-size frames are refused with the
receiver still answering, and a stream cut in the middle of a page is read
again with nothing lost or doubled. Unsubscribing the standard stops every
followed lane of it.

`TestPublicationFollowStatusRealDaemons` reads a followed lane's `$DSS` on
both daemons: the receiver's counts the records and bytes received and the
time spent receiving, verifying and storing them; the producer's counts its
follower (`SUBSCRIBER_COUNT`) and exactly the record bytes it served
(`SERVED_BYTES`), and no follower once the receiver has stopped.

A node imports only the lanes it subscribes to (1.2.0), so every receiving
daemon of these tests subscribes to the lanes it is meant to import; one that
does not imports nothing (`TestPublicationRealDaemons/refusal_unsubscribed`,
`TestPublicationContentAddressRealDaemons`).

`TestPublicationSubscribeRealDaemons` drives the operator commands (`sync
subscribe`, `sync subscriptions`, `sync status`, `sync unsubscribe`), each
signed in to its daemon with the node's root key. A new subscription to a
`replace-current` lane whose producer holds six batches receives only the
current batch (2,000 of 12,000 records, counted on both sides and in the
receiver's received records and bytes), then live records within seconds; a
`keep-all` lane past the receiver's `subscriptions.record_ceiling` is refused
naming both figures; `--force` subscribes, and the running guard stops the
lane before the store passes the ceiling (alert `follow_stopped`, cursor
kept); with the ceiling raised it resumes at its cursor and the whole lane
arrives.

`TestPublicationUpgradeRealDaemons` needs `SDN_E2E_V119_BINARY` (a build of
tag v1.1.9; `./scripts/ci-local.sh publication` downloads the published
archive for groups `all` and `publication-6`). On 1.1.9 a receiver imports
two lanes through `peers.trusted_import` and its ledger; updated alone, it
converts them to subscriptions kept in `subscriptions.dss` (not in
`follows.dss`, which 1.1.9 reads), leaves the ledger in place, loses no
record, and
waits for the 1.1.9 producer's statement; a new subscription to that producer
is refused and `--force --start beginning` follows it; a lane the old allow
list admitted (a 1.1.9 control receiver imports it) is not imported. With the
producer updated too, the converted subscriptions start at its current batch
(only the live records are sent) and each lane equals the producer's, byte
for byte, while a 1.1.9 follower keeps following the updated producer.

`TestPublicationRollbackRealDaemons` (also `SDN_E2E_V119_BINARY`) updates a
1.1.9 receiver that imports a lane through `peers.trusted_import`, rolls it
back to 1.1.9 before the converted subscription holds a cursor, and updates
it again: on the rollback 1.1.9 imports the next publication through its old
path and never reads the lane's unpublished older batch, its own follow keeps
flowing, and back on this build the subscription starts at the producer's
current batch, live, with the conversion run once.

`TestPublicationConversionRecordRealDaemons`: a ledger that does not parse
converts nothing, writes no conversion record and raises
`subscription_conversion`; fixed, the next start converts it, and the record
names the removed keys, the deny list and an allow entry naming an untrusted
peer (not subscribed).

`TestPublicationCursorRefusedRealDaemons`: after the producer's `reindex`
expires its cursors, a lane that started at the current batch and one that
started at its beginning both start again at the producer's current batch
(received counts: the first read and the new records only), with
`follow_cursor_refused`. Both lanes (keep-all) show their unread records in
`sync subscriptions`, across a restart that raises the alert again; the one
subscribed from its beginning stays in `follows.dss`. Acknowledged, one lane
shows nothing unread and has no alert; the other, subscribed again from its
beginning, reads the whole lane. A third lane unsubscribed with its gap
unread leaves no alert.

`TestPublicationUnheldBatchImportsPNMRealDaemons`: a producer serves a lane
it imported from a third daemon, so its records name that daemon as their
producer; a receiver's stream to it is connected but every fetch of the batch
by CID is refused. The lane's signed publication brings the 300 records, and
the batch then becomes current.

`TestPublicationSubscriptionsUnreadableRealDaemons`: a receiver started on an
unreadable `subscriptions.dss` moves it aside (bytes kept), raises
`subscriptions_unreadable`, lists no subscription, refuses a new one and
writes no file, also after a restart; a readable copy put back loads the
subscription and the lane resumes. A `subscriptions.dss` without read
permission (not run as root) is not moved: the alert names the error, and the
next start after the permission is back loads it.

`TestPublicationSubscriptionUnknownFieldRealDaemons`: a subscription whose
`SYNC_FILTER` names a field this build does not know raises
`subscription_refused` (lane and field), imports nothing and is written back
as it was; the other lane keeps importing; unsubscribed, its alert clears.

`TestPublicationSubscriptionFilterValueRealDaemons`: a `SYNC_FILTER` whose
known field holds a value this build does not read, beside an unknown field
(`start=from-the-next-batch&x=1`), refuses only that subscription
(`subscription_refused`); the file is not moved aside and the other lane
keeps importing.

`TestPublicationRefusedSubscriptionKeptRealDaemons`: a refused subscription
whose record carries a status, a last synced time, an `ERROR`, rates and
times is in `subscriptions.dss` byte for byte as it was after the file is
written again (the other lane's cursor moved); the refusal shows in `sync
subscriptions`, not in the file.

`TestPublicationMisstatedSizesRealDaemons` (a producer built with the tag
`sdn_e2e_follow_faults`, whose "overstate-", "understate-bytes-" and
"understate-records-" lanes' manifests state a shard of
3,689,348,814,741,910,324 bytes, half the shard's bytes, or one record fewer
than the shard holds): of four publication-only lanes (OEM) the honest one
imports; the overstated one stops at the running guard (`follow_stopped`
naming the stated size) and the understated ones raise
`publication_misstated` naming both figures; none of their records is stored,
also after a restart and its catch-up.

`TestPublicationDeferredWaitBoundRealDaemons` (a receiver of that build, whose
"never-" lane opens no stream and whose "late270-" lane opens its first stream
270 s after it starts): the "never-" lane's publication waits for its stream
and is imported within 5 minutes and 50 s of the lane's start, although the
"late270-" lane's settled stream ran the catch-up and reset its timer to its
5-minute interval at about 270 s.

`TestPublicationStorePanicReleasesReservationRealDaemons` (a receiver built
with the tag `sdn_e2e_follow_faults`, or `SDN_E2E_PUBLICATION_FOLLOW_FAULTS_BINARY`):
the stores of two lanes panic, on followed pages and on fetches by CID; after
the panics nothing is reserved (`GET /api/v1/sync/plan`) and a third lane
stores 3,000 records under a record ceiling a few leaked reservations would
have closed.

`TestPublicationRestartStreamedPNMRealDaemons` (the same build, whose
"late-" lane's first stream opens 40 s after the start): a publication left
to the stream is not imported after a restart either; the stored-`$PNM`
catch-up at 15 s leaves it waiting, and once the stream holds the batch
again the catch-up leaves it to the stream.

`TestPublicationPNMGuardRealDaemons`: a publication-only lane (OEM) whose
publication would pass the receiver's record ceiling of 3 is not imported
(`follow_stopped`, `stopped`); with the ceiling raised the catch-up imports
it and the alert clears.

`TestPublicationSubscriptionFileOrderRealDaemons`: with `follows.dss` not
writable, an unsubscribe fails and both files still hold the lane; once
writable, the unsubscribe holds across a restart.

`TestPublicationDiskFloorRealDaemons` (macOS: the receiver's storage path is
a 3 GB disk image of its own, filled to 16 MiB above the 1 GiB reserve, and
the directory holding it is not; skipped elsewhere): a forced current-batch
subscription of 20,000 records stops (`follow_stopped`) before its fetches by
CID pass the reserve.

`TestPublicationStreamSlotsRealDaemons`: a producer serves a subscriber it
does not trust 8 followed streams and refuses the ninth busy; listing the
subscriber as trusted serves all nine.

`TestPublicationGossipGateRealDaemons`: records gossiped on the OMM topic by
a trusted producer the receiver subscribes to reach the receiver, which has
`network.accept_push_from: any`, and none is stored.

`TestPublicationPNMOnlyLaneRealDaemons`: an OEM lane whose stream the
producer refuses imports through its signed publication (nothing received on
the stream, records equal); an OMM lane whose stream is connected does not
fetch its publication's shard.

`TestPublicationLaneCountRealDaemons` compares a scan's `TOTAL_COUNT` on one
daemon with the distinct records sent: lanes with one tag instance (counted
from the lane counters) and a source whose records sit in two batches
(counted by reading the lane).

The full OEM metadata query currently returns HTTP 400 `byte-cap` or `timeout` under its
normal sandbox limits even with a narrow projection. These refusals are recorded with
separate durations and are never counted as successful SQL-read latencies.
Other measurements continue. `known_oem_metadata_query_limits` retains the
failing assertion; set `SDN_E2E_PUBLICATION_REPRO_OEM_SQL=1` to execute it.
The normal sandbox caps remain unchanged.

File-size measurements are live filesystem snapshots. Transient journal files
removed during enumeration are omitted; other filesystem errors fail the run.
