# Durable catalog publication

Retention adds a synchronous SDS copy before the storage engine commits. Payload disk
use follows the live catalog, pending or quarantined batches, and active selections.
Completed obsolete entries are reclaimed automatically; a 48-byte completion-checkpoint frame
remains per historical batch. Retained writes
serialize per store. Concurrent calls with the same source, batch, peer, signature,
and write options coalesce while waiting for that serialization, up to 4,096
records across calls. A larger single batch retains one intent. Every caller
waits for the shared durable outcome. Separately
acknowledged sequential writes require individual transactions; producers reduce
that cost with `StoreBatchWithSourceTags`, which journals the entire call together. Identical source-tag repeats do not
journal again. Changed batch identities or export metadata require retention.

Snapshots hold immutable file references and hydrate one record at a time into
the shard writer. Readers copy the applied-entry index under a short lock;
reference validation, hashing, and export run outside the ingestion lock. Limits
are 100,000 journal entries, 100,000 records per selection, 64 MiB of accounted metadata
per shard index, and 16 MiB per payload. Each retained source has
`catalog_object_limit` (1 through 100,000; omitted or zero defaults to 100,000).
Each source has a generation allowance of
`min(100000, catalog_object_limit + ceil(catalog_object_limit / 4))`.
The store reserves four generations per source: reference capacity is
`4 * sum(generation_allowance)`, and accounted reference-byte capacity is
`256 * reference_capacity + 1 MiB * retained_source_count`. A CAT/OCM pair
configured for N objects reserves up to 10N references, capped at 800,000 for
the pair. Lowering or removing a setting preserves the durable capacity and
logs the configured capacity and retained use. Configuration validates the
range at startup. The reference structure occupies 88 bytes; binary content
digests, offsets, and lengths are inline, while paths and tags are shared per
entry. Accounted bytes also include identity strings, cached product digests,
and the tag lookup index. Pending work and held selections consume this reserve.
Use bounded export windows rather than one oversized shard. Ingestion limits are checked before replacing live records. Selection and
shard-index limits refuse export before publication. These are storage/export bounds, not a
bound on the entire node's heap. A synthetic 100,000-product selection test
checks retained heap growth against 128 MiB. Drain pending work and resolve quarantined batches before exhausting capacity.
The tag-lookup index uses one hash lookup per record. Each sequential call still
requires a durable intent; prepared scope metadata contains only recovery identity,
and its directory is synchronized once after both metadata files.

Enable retention on empty CAT and OCM sources in store format 4. Existing
unretained history cannot be reconstructed. Sealed standards are refused; CAT
and OCM have no sealed fields. Provider and source identities must be exact.
Configuration validation requires a matching batch rule, publication to be enabled, and completed series
to be retained, including matching retention overrides.

```yaml
publishing:
  enabled: true
  retention:
    keep_series: 0
  auto_publish:
    - schema: OCM.fbs
      provider_id: catalog-producer
      source_name: catalog-products
      retain_for_publication: true
      catalog_object_limit: 100000
      min_interval: 5m
      settle: 10s
    - schema: CAT.fbs
      provider_id: catalog-producer
      source_name: catalog-index
      retain_for_publication: true
      catalog_object_limit: 100000
      min_interval: 5m
      settle: 10s
    - schema: CAT.fbs
      provider_id: catalog-producer
      source_name: catalog-index
      publish_scope: source
      paired_publication: true
      catalog_product_source: catalog-products
      min_interval: 24h
```

`catalog_product_source` alone does not enable retention. Both participating
sources require `retain_for_publication`. A retained OCM source requires its CAT
index; standalone product retention is refused. A retained batch source enters deferred
publication without a separate snapshot rule. Unconfigured sources retain their
existing behavior, including the live CAT overwrite policy.

Each transaction synchronizes its size-prefixed SDS stream and metadata, renames
its directory, and synchronizes the parent before engine PUT. A durable engine-outcome file precedes
reference indexing and the applied marker; both record per-record refusals.
Recovery uses that outcome even when the reference step failed. Accepted records remain
publishable, and the caller receives the engine's refusal for rejected records.
Refused records never enter an export. Preparation is an accepted durable intent:
recovery can finish a call whose caller received no success response.

A transient completion-marker failure is retried three times without repeating
the successful engine write. If it persists, the caller receives an explicit
"engine write completed; publication acknowledgement pending" error. Thirty-second
reconciliation retries the acknowledgement in process. Other interrupted writes
are replayed idempotently. Startup makes at most three attempts per interrupted
entry. Exhausted recovery or corruption pauses the affected source, reports its
reason in logs and `recoveryErrors` in automatic-publication status, and permits
the node and other sources to operate. Unreadable metadata uses a separate scope
copy; if both copies are unusable, all retained sources are paused conservatively.
Ordinary sources remain available. Applied streams are also checksum-verified at
startup. Only unreclaimed streams are read: startup work scales with their payload
bytes plus reference metadata. Startup parses every kept CAT payload once to
cache its product identifiers. At a paired 100,000-object capacity, 800,000
records at the 16 MiB per-record limit imply a theoretical 12.5 TiB payload bound. The reference-metadata
limit can bind earlier. This is an I/O bound, not a measured startup duration.
Orphan preparation and reclaimed directories and abandoned `.write-*` files are removed.
Corrupt derived reclamation plans are rebuilt from independent allocation
watermarks and the completion checkpoint. Repairing entry metadata in place
clears source isolation on reconciliation, including an unknown-scope pause. A transient read error
never makes an applied entry eligible for an engine write. Thirty-second recovery
retries unreadable index state and clears its pause after all affected entries recover.
Export read errors fail that attempt without pausing ingestion. Restore corrupt journal
files from a verified backup; deleting a pending intent loses
the durable handoff.

Successful announcement and series bookkeeping precede a separate durable
`published` receipt on each captured journal entry. Series retention cannot erase
that receipt. Reconciliation considers only incomplete entries; a continued batch
requires completion of its new entries. Existing completed-series fingerprints
are migrated to journal receipts when encountered. A shard alone is not completion.
Live expiry, deletion, and replacement do not remove retained batch bytes.

The producer commits products before index chunks. The final index write of a
generation uses a unique batch identifier ending in `:complete`; that suffix
explicitly declares all chunks complete. It must not appear on an intermediate
chunk. An embedded producer can instead call `CompleteCatalogGeneration` after
all writes. Closure is validated at the index's cutoff before the generation
marker becomes durable. Product-only writes, partial index writes, and unresolved
new generations leave the last completed generation visible. Before the first
complete generation, snapshot capture returns an error. Closure reads use a pinned
index cutoff outside the ingestion lock. Three deterministic content failures durably
quarantine that terminal batch; later generations proceed while the previous complete
generation remains selectable. The suffix has no special meaning on a retained CAT
source without `catalog_product_source`.

Selection uses the last CAT record for each catalog identity in journal order,
then resolves exactly its referenced OCM payload or size-prefixed record CIDs.
`PRODUCTS_AS_OF` is descriptive metadata, not a conflict-resolution clock. The
cutoff is the last index-source sequence included; unrelated writes do not change
it. `CaptureCatalogSnapshot` binds both selections to that cutoff. Production
publication captures once, publishes selected products, then publishes the CAT
index. A product failure prevents index publication. The JSON sidecar carries
`publicationCutoff` for the shared internal capture and `publicationSourceCutoff`
for the source-local consumer cursor. The SDS schemas, signatures, and transport
are unchanged.
An empty product list selects no product for that catalog identity.

Treat the CAT publication as the generation commit and pair it with the product
sidecar at the shared cutoff. Apply changed batches through a completed generation.
Read the JSON index through the announced index CID: retained batches carry
`publicationSourceSequences`, the ordered durable sequence numbers of their
source transactions. `publicationSourceCutoff` identifies the source snapshot
coverage. Sequence numbers are scoped by schema, provider, and source; unrelated
sources do not advance them. After applying all shard windows, ignore already
applied sequences and require every new number to equal the previous number
plus one. A gap means a quarantined, excluded, or interrupted transaction:
stop applying deltas and fetch the latest complete snapshot with coverage at
least through the observed gap. If that snapshot is older, wait for a newer
complete generation. Replace the local selected map from that snapshot, then
resume contiguous deltas. Product and index cursors are independent; apply the
products before committing the index generation. A legacy index without source
sequence fields requires a full snapshot to establish the cursor. A continued
batch can contain several transaction numbers. All its windows carry the same
sequence list; advance only after all windows are applied. Empty excluded batches
produce no announcement and are detected through the same gap rule. A batch crossing a snapshot cutoff is replayed in full;
duplicate records are harmless. With a fixed exclusion policy, the snapshot plus
post-cutoff batches produces the same selected map as a fresh compact snapshot.
A captured selection pins its files through `Close`; callers release both sets
with `CatalogSnapshot.Close`. The production exporter releases its selections
after publication. Older unpinned cutoffs can disappear through reclamation.
Retries of a pinned selection produce identical content identifiers. A changed
selection or export metadata can produce different identifiers.

Ordinary deletion and expiry do not remove retained records from snapshots. For
an operator takedown, set `catalog_exclude_cids` on a retained CAT source rule to
an array of the CAT payload CIDs to remove. Exclusions are durable and additive;
they remove those selected index records from future snapshots and pending CAT
batch selections and their referenced products from pending OCM batch selections,
with unreferenced products omitted from compact snapshots,
without restoring older versions. A replacement full snapshot
communicates the new exclusion policy. Existing immutable exports and captured
selections are not revoked. A takedown does not erase payload bytes from a mixed
journal entry: one live or pending record retains the entire entry. The maximum
retained unit is one ingest transaction, bounded by reference capacity and the
16 MiB per-record limit; producers bound their batch sizes accordingly. A producer can instead publish a new CAT record with
an empty product list to communicate product removal in its ordinary update stream.

To stop retention, drain every changed batch, remove `retain_for_publication`,
`paired_publication`, `catalog_product_source`, and exclusion settings for the
participating sources, and set `stop_publication_retention: true` on their exact
source rules. Restart. Pending or unapplied entries refuse retirement; completed
sources return to ordinary storage/export behavior. Removing a rule by itself
does not disable durable retention. If publication is disabled while durable
retention remains active, startup and hourly checks emit an explicit warning. Node runtime status
reports `publicationRetainedSources`, `publicationRecoveryErrors`, and
`publicationJournal` even when automatic publication is disabled. After every source
has retired, stop the node and remove the `publication-journal` directory inside
its data directory. Keep backups and published shards according to their normal
retention policy. A future retained source requires an empty source at activation.

After publication and at startup, reclamation keeps the latest source view, the
last complete generation and its product closure, unannounced or quarantined
entries, products newer than the completed index, and every active selection.
A durable sequence checkpoint and batch receipt precede directory retirement;
startup finishes deletion of retired directories. Applied, published entries with
no remaining reference lose both payloads and resident index references. Journal
statistics count entries, records, reference bytes, payload bytes, pins, reclaimed
entries, skipped sweeps, and the latest skip reason. Paused sources block only
their own catalog pair. Sweeps preserve concurrent appends and their product
closure; a busy independent source does not cancel a sweep. Cached product
digests avoid repeated index-payload reads during reclamation. File removal runs
after releasing the write lock; durable receipts and atomic directory retirement
still serialize against retained ingestion. Each pass retires at most 64 entries;
thirty-second reconciliation continues incomplete sweeps. Resident counts and backlog admission
use incremental counters; view pins visit entries rather than every reference.
Completion receipts use a single checksummed binary checkpoint, migrated from
individual receipt files. Checkpoint bytes still grow by 48 bytes per historical
receipt; exact historical completion lookup requires retaining those identities.
Small receipt metadata remains; payload and reference counts plateau with a stable
catalog and drained publications. The 30-day synthetic control ingests 2,000 objects
per day, publishes 60 batches, and compares the last snapshot with a fresh snapshot.

After three content-specific retained batch attempts, its bytes remain quarantined, its reason
appears in recovery status and logs, and dispatch continues for its source. On that
source's exact rule, set `requeue_publication_batches` to a list of batch identifiers
to retry, or `discard_publication_batches` to explicitly acknowledge their lost
publication. Restart applies these configuration actions; remove the action setting afterwards.
Without a restart, POST to `/api/v1/admin/dataset-updates/batch` from a direct
loopback client with `schema`, `providerId`, `sourceName`, `batchId`, and
`action` (`requeue` or `discard`). Remote and reverse-proxied requests are refused.
Quarantined batches also requeue automatically after six hours. Transport, service,
shutdown, and transient filesystem errors retry without a quarantine count, with
backoff capped at 30 minutes. Concurrent validation of the same captured
generation failure increments its counter once.
An operator can discard or requeue any unfinished batch, including one with no
recorded failure. A durable discard of an unapplied intent prevents its replay.
Requeue also resets terminal-generation failures. Quarantined bytes count against capacity.

Paired generation admission reserves both product and index space before its
first product transaction. Products still precede index chunks, and the final
index batch retains its `:complete` suffix. Publication of either side waits
for that generation's terminal commit. A capacity refusal cancels the staged
generation through a durable abort plan and releases its publication payloads;
live records are unchanged by cancellation. Active readers retain their pins
through cleanup. The last completed snapshot remains available. Refusals name
`catalog_object_limit`, the capacity, and retained use. Pending batches preserve
their bytes during publication lag; after completion, reclamation coalesces
history to the latest generation and remaining pending batches.
After a capacity refusal, the producer retries the complete generation, including
its products; an index-only retry cannot recover released product payloads.

First-production consumers follow snapshots only and replace their catalog on
each announced snapshot. Changed-batch consumers require additional ordering
and takedown handling.

Receipt checkpoints use one file per source with 48-byte frames. A truncated or
bad-checksum final frame is a torn tail: recovery logs, truncates, and syncs it.
A corrupt middle frame pauses its source. Legacy global checkpoints remain
readable; an ambiguous middle-frame failure in that format requires global
publication repair because its frames contain no source identity. An unreadable
watermarks file is rebuilt from entries, receipts, and reclamation metadata.
The recovered global sequence supplies a conservative source-sequence floor;
this can create a gap but cannot reuse an older cursor. Failure to persist that
repair pauses retained sources while ordinary storage remains available.

Immutable input checksum, framing, identity, and selection-bound failures are
content errors. Transport, cancellation, configuration, output-filesystem, and
resource errors remain retryable service failures. Unreadable immutable entry
payloads at recovery are isolated immediately; normal publication content
attempts retain the three-failure threshold.

Selection sorts compare digest bytes without allocating CID strings and retain
the existing lowercase-base32 lexical order, including its digit ordering.

Ordinary sources use the existing buffered shard-export path. Retained exports
stream to a temporary file and install immutable output through a hard link, with
exclusive creation and streamed copy on filesystems without hard-link support.
The output digest is checked when the destination already exists.
