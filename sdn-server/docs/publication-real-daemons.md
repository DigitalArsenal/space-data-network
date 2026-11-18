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

`TestPublicationAlertClearsWhenLaterRunsPublishItRealDaemons` runs the same
flow three times, as its schedule does. The first run's publication fails
(the same blocked shard directory) and the alert says the failed batch's two
records are in no completed publication. The second run's pull returns one
of the two records: its own batch is published, and the alert stays, saying
one record is still unpublished. The third run's pull returns both records
again (in another order, so another batch): every record of the failed batch
is in a completed publication, and the alert clears.

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
fires once per process). The alert says all three records are unpublished.
A publication of a batch carrying one of them leaves the alert saying two
are, and retention lists no series made of the failed publication's rows; a
publication of a batch carrying all three clears the alert, and the failed
batch's records were counted once.

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
`follows.dss`, replaces the older batch with the newer one while the producer
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
