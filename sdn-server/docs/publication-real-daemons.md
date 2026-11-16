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
call and concurrent receiver polling every 100 milliseconds. The opt-in
`SDN_E2E_PUBLICATION_OBSERVATIONS` file records only timestamps, schemas, and
manifest CIDs; it is unset during ordinary operation. Raw observations accompany
the daemon logs. Per-operation RSS is sampled every 200 milliseconds. CPU
counters are `ps` process CPU time.
The SQL reads use an EPOCH window for OMM and a CREATION_DATE window for OEM.
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
