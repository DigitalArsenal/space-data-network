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
bytes are excluded from test output.

## Baseline command

Run `./scripts/publication-baseline.sh RESULT_DIRECTORY`. The command builds
and starts two isolated daemons per repetition and emits JSON. It preserves
individual measurement rows, a test log, and an exit code in the selected
directory. A nonzero exit code means the result is incomplete.

The data sets contain 2,000,000 distinct synthetic OMM records and 1,024 OEM
records with 21,846 six-component ephemeris states each. The OEM array alone
occupies 1,048,608 bytes per record. Three repetitions report rates, CPU
seconds, sampled resident memory, machine load, store file sizes, restart
latency, and read latency during another ingest. Every rate carries the
250,000,000 bytes per second target.

The wire probe and standalone shard read use the receiving daemon's Kubo
stream forwarder, with private-read authority granted to that test peer. The
forwarder adds a loopback TCP copy to the measurement. Download-only reads
one shard after import and therefore measure warm caches. The publication
interval includes export before announcement and polling at one-second
intervals; it is an upper bound on announcement-to-readable latency. RSS is
sampled every 200 milliseconds. The CPU counters are `ps` process CPU time.
The large-record query is a bounded SQL read; the OMM query includes an epoch
window. These distinctions are recorded alongside results and limit their
interpretation.
