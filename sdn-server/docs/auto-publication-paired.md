# Batch updates and periodic source snapshots

Paired publication retains pending batch identities in memory and scans stored
batch summaries at startup and every 30 seconds. Its memory and recovery work
grow with retained batches. Full-source snapshots add export, pinning, and
announcement work. Retaining completion history and source records consumes
disk space. A crash after announcement handoff can repeat the same content
identifier; consumers deduplicate content identifiers.

Configure both scopes for the same schema, provider, and source:

```yaml
publishing:
  auto_publish:
    - schema: OCM.fbs
      provider_id: catalog-producer
      source_name: catalog-products
      publish_scope: batch
      min_interval: 5m
      settle: 10s
    - schema: OCM.fbs
      provider_id: catalog-producer
      source_name: catalog-products
      publish_scope: source
      min_interval: 24h
  retention:
    keep_series: 0
```

The batch rule publishes each changed batch. Pending requests survive rate
limits, a full 16-entry dispatch queue, and transient failures. Retries continue
with capped backoff. A zero-insert replay can recover a committed batch. Settle
waits for quiet after the last observation that inserted records.

The source rule publishes all retained records of that exact source at startup
and periodically, including when no ingest arrives. Its interval is independent
of the batch rule and defaults to 24 hours when the two scopes are paired.
Rule order does not select one scope over the other. The first matching rule
within each scope determines its options. Batch filters retain their wildcard
and case-insensitive matching; source identities are exact and case-sensitive.
A single batch or source rule without retention options retains its existing scheduling behavior,
including its five-minute default and existing retry policy.

For the configuration above, use append-retained source records, retain completed
publication series (`keep_series: 0`), and retain the producer's store across
restarts. A retention override for the configured source also needs
`keep_series: 0`. Deleting source records before export makes those records
unrecoverable. Enabling publication on an existing source includes retained
historical batches without a matching completion fingerprint.

Recovery reads committed batch identities from the store, including commits
whose ingest observer did not run. The existing publication series ledger
acknowledges an unchanged batch only after the publication service succeeds.
A shard row alone is not completion. The existing service reuses recorded
shards and announcements after an incomplete attempt; the dataset-publication
service, shard format, DPM/PNM conventions, and transport are unchanged.

An ordinary source snapshot contains retained source history. For durable CAT
replacement and compact CAT/OCM snapshots, use the explicit storage and exporter
options in [Durable catalog publication](catalog-publication.md). Those options
retain pending bytes independently of live-record replacement or expiry.

Tests use an inert publication service with real storage and completion
bookkeeping. Process exits at storage commit, shard creation, announcement
handoff, and completion verify recovery without keys, signatures, or network
traffic. They exercise both store formats. They do not exercise signed network
delivery or replace the repository's publication integration tests.
