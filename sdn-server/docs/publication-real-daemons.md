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

`TestPublicationAlertStaysWhenLaterRunsCarryItRealDaemons` runs the same
flow three times, as its schedule does. The first run's publication fails
(the same blocked shard directory) and the alert says no publication of the
whole failed batch (two records) completed. The second run's pull returns
one of the two records, the third both (in another order, so another batch);
each run publishes a batch of its own, and the alert stays after both: a
publication of another batch never clears a failed batch. A publication of
the failed batch whole clears it.

`TestPublicationManualRunOutlivesClientRealDaemons` runs the same flow by
hand on a node whose GP origin answers after 20 s, so every run lasts longer
than its client waits. `apps run --timeout 4s` stops waiting, says the run
continues and how to see its result, and exits non-zero; the run ends ok on
the node and `apps status` shows it. `apps run` of a running run joins it,
and SIGINT stops it waiting with the run continuing to an ok end. A raw
run-now request whose client gives up after 4 s (the shape of the 2026-11-17
cut-off) leaves the run to end ok. `apps run --detach` returns at once, and
`apps cancel` ends the run as cancelled by the operator. A forced request
(and `apps run --force`) that joins a run started without force is told the
force does not apply. `apps run` follows its own run by id: when that run is
cancelled and another starts at once, the command reports the cancelled run
and exits non-zero.

`TestPublicationManualRunStopsWithNodeRealDaemons` runs the flow by hand on
a node whose GP origin answers after two minutes. A non-forced run cancelled
by the operator gives back its attempt in the provider's minimum interval:
the next non-forced run starts. A run-now request waits for that run, and
the node is stopped (SIGTERM): the request is answered within 10 s, saying
the node is stopping, and the admin server's shutdown is not held to its
bound.

`TestPublicationManualRunBoundRealDaemons` runs the flow by hand on a node
whose service has `execution_timeout: 6s` and whose origin answers after
30 s: the run ends at its bound, saying so.

`TestPublicationAlertPartialFailureRealDaemons` fails a three-record batch's
publication, one record per window, after it wrote publication rows:
`SDN_E2E_PUBLICATION_FAULT=announce@2` fails the second window's
announcement, `SDN_E2E_PUBLICATION_FAULT="record completion"` fails the
recording of the completion after every window was announced (the fault
fires once per process). The alert says no publication of the whole batch
completed. A publication of a batch carrying one of them leaves the alert as
it was, and retention lists no series made of the failed publication's rows.
A second batch with the same three records fails at its first announcement.
A publication of a batch carrying all three completes and both failed
batches stay; a publication of each of them whole clears the alert.

`TestPublicationAlertBatchGainedRecordsRealDaemons` publishes a batch of two
records, stores a third in it, and fails its republication at the third
window's announcement (`SDN_E2E_PUBLICATION_FAULT=announce@3`); a second
batch holding the third record fails too. The alert says one of the three
records of the first batch arrived after its last completed publication,
and that no publication of the whole second batch completed. An
announcement of the batch's existing publications announces the completed
publication's window only. The publications of an unrelated batch and of a
batch holding the first two records leave the alert as it was. A
publication of the whole first batch removes it from the alert, which stays
naming the second batch (the first batch holds its record, but is another
batch); a publication of the whole second batch clears it.

`TestPublicationAlertGainedAfterFailureRealDaemons` stores two records in a
batch and fails its publication, stores a third record in it and fails its
publication again, and publishes another batch holding the first two
records: the alert stays, saying no publication of the whole batch (three
records) completed. A publication of the whole batch clears it, and the
check after it finds the batch published as it is.

`TestPublicationAlertRestartRealDaemons` fails a batch's publication and
restarts the node: the alert is raised again at start, and the batch is
checked once the node answers ready. A publication of the whole batch
clears it, and the node restarted again raises no alert.

`TestPublicationAlertSupersededBatchRealDaemons` fails a batch's publication
on a replace-current node, stores a batch of other records and supersedes
the lane by it (`POST /api/v1/admin/store/supersede`, the store's
replace-current eviction): the supersede prompts a check of the failed
batch, which holds no records any more, and the alert clears, the log
saying so, before any publication of the lane.

`TestPublicationAlertFailureDuringClearRealDaemons` holds the clearing of an
alert for 20 s after it was decided
(`SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD=20s`) by the publication of the whole
failed batch, and fails another batch's publication meanwhile: after the
hold the alert is active and names that batch.

`TestPublicationAlertOtherBatchNoCreditRealDaemons` fails a batch of two
records and publishes another batch holding both: the alert stays. A
publication of the whole failed batch clears it.

`TestPublicationAlertRecordsDuringPublicationRealDaemons` holds a
publication of the whole failed batch for 15 s after it exported its set
(`SDN_E2E_PUBLICATION_COMPLETION_HOLD=15s`) and stores a third record in the
batch meanwhile. The completed publication removes the batch from the alert
when it is reported; the check after it finds the batch holds a record that
publication did not export, and the alert is raised again, saying 1 of its 3
records arrived after its last completed publication. A publication of the
batch as it is now clears it.

`TestPublicationAlertRetractedLaneRealDaemons` fails a batch's publication
and a publication of the lane's newest records, then retracts the lane
(`POST /api/v1/admin/dataset-updates/retract?apply=true`): both failed
scopes go at the retraction, the alert is clear when the retraction
answers, and the restarted node raises nothing.

`TestPublicationAlertCheckCostRealDaemons` publishes 50 batches that each
hold a record R, then fails a batch holding R: the node's log names one
fingerprint for the check, of the failed batch itself, and none of the 50.

`TestPublicationAlertHeadPublicationRealDaemons` fails a three-record batch's
publication, then publishes a head of that batch (`limit: 1`) and a head of
the lane (no batch): neither clears the alert. A publication of the whole
batch clears it.

`TestPublicationManualRunCancelByIDRealDaemons` starts a manual run, cancels
it, and starts another. A cancel naming the first run answers 404 and the
second keeps running; a cancel naming the second ends it, and `apps cancel`
(no id) ends a third.

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
archive for groups `all` and `publication-4`). On 1.1.9 a receiver imports
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
