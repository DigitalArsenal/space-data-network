# Batch updates and periodic source snapshots

Paired publication adds pending identities, durable recovery scans, and full-source
exports. Completion receipts and recoverable input consume disk space. Retained
sources refuse new batches when 4,096 batches are pending; automatic publication
also bounds its in-memory backlog at 4,096 and exposes `backlogError` when exceeded.
Stop ingestion and drain publication after that error. Ordinary append-only sources
must retain their input until completion; only the explicit CAT/OCM retention option
protects superseded or expired bytes.

Paired behavior requires an explicit source opt-in:

```yaml
publishing:
  enabled: true
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
      paired_publication: true
      min_interval: 24h
  retention:
    keep_series: 0
```

Without `paired_publication` or `retain_for_publication`, existing configurations
keep first-matching-rule behavior, including configurations containing both scopes.
The existing five-minute default remains. Opted-in source snapshots default to
24 hours. Their interval is independent of batch publication. Exact opted-in
identities select both scopes; the first rule within each scope determines its
options. Configuration requires `keep_series: 0`, including matching overrides.

Retained batches publish in journal order across restarts. Ordinary sources use
observation order in process and the storage progress table's batch-identifier
order at restart; they have no retained journal sequence.
A burst starts after `min_interval` and captures the pending ordered prefix.
Its batches dispatch back to back; the interval applies to burst start, not each
successful batch. A failed predecessor waits with backoff; only each source's head enters the dispatch
queue. After three failed attempts, a retained batch is durably set aside and newer
batches proceed. Ordinary sources without durable retention continue retrying. Batches arriving during a burst, including continuation
of an in-flight batch, wait for the following interval. Settle waits for quiet after
an observation. The bounded dispatch queue does not discard pending identities.

Startup and thirty-second reconciliation recover committed batches whose observer
never ran. A zero-insert replay can recover a committed batch. Retries use capped
backoff. A successful service completion acknowledges progress; a shard row alone
does not. Retained sources also keep per-entry completion receipts outside series
retention. Restart can repeat an announcement after handoff and before durable
completion; consumers deduplicate its content identifier.

Source snapshots check their fingerprint before export. Their last successful
publication time survives restart through the existing series ledger. Unchanged
sources are skipped at startup and at periodic intervals. A changed source becomes
eligible after its configured interval. An ordinary source snapshot contains its
retained history, including superseded products if those records remain stored.

To disable pairing without durable retention, remove `paired_publication`; the
ordinary first-rule behavior applies. For replacement-safe batches, compact
snapshots, generation commits, recovery status, operator takedowns, and the
supported retention stop procedure, see [Durable catalog publication](catalog-publication.md).

Focused tests use synthetic records and inert transport. They exercise durable
storage and bookkeeping, process interruption, ordering and retries, configuration
compatibility, and production snapshot sequencing at the signing/transport boundary.
