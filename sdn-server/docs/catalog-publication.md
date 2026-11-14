# Durable catalog publication

Retention adds a synchronous SDS copy before the storage engine commits. Disk
use grows with history; automatic journal reclamation is absent. Retained writes
serialize per store. Separately acknowledged single-record writes each require a
durable transaction; producers reduce this cost with `StoreBatchWithSourceTags`,
which journals the entire call together. Identical source-tag repeats do not
journal again. Changed batch identities or export metadata require retention.

Snapshots hold immutable file references and hydrate one record at a time into
the shard writer. Readers copy the applied-entry index under a short lock;
reference validation, hashing, and export run outside the ingestion lock. Limits
are 100,000 journal entries, 200,000 indexed records, 128 MiB of accounted
reference metadata, 100,000 records per selection, 64 MiB of accounted metadata
per shard index, and 16 MiB per payload. Admission refuses work exceeding these
limits before replacing live records. These are storage/export bounds, not a
bound on the entire node's heap. A synthetic 100,000-product selection test
checks retained heap growth against 128 MiB. Partition sources and drain/retire
retained history before exhausting capacity.

Enable retention on empty CAT and OCM sources in store format 4. Existing
unretained history cannot be reconstructed. Sealed standards are refused; CAT
and OCM have no sealed fields. Provider and source identities must be exact.
Configuration validation requires publication to be enabled and completed series
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
      min_interval: 5m
      settle: 10s
    - schema: CAT.fbs
      provider_id: catalog-producer
      source_name: catalog-index
      retain_for_publication: true
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
sources require `retain_for_publication`. A retained batch source enters deferred
publication without a separate snapshot rule. Unconfigured sources retain their
existing behavior, including the live CAT overwrite policy.

Each transaction synchronizes its size-prefixed SDS stream and metadata, renames
its directory, and synchronizes the parent before engine PUT. The applied marker
records per-record refusals as completed outcomes. Accepted records remain
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
startup. Orphan preparation directories are removed. Restore corrupt journal
files from a verified backup before restarting; deleting a pending intent loses
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
complete generation, snapshot capture returns an error.

Selection uses the last CAT record for each catalog identity in journal order,
then resolves exactly its referenced OCM payload or size-prefixed record CIDs.
`PRODUCTS_AS_OF` is descriptive metadata, not a conflict-resolution clock. The
cutoff is the last index-source sequence included; unrelated writes do not change
it. `CaptureCatalogSnapshot` binds both selections to that cutoff. Production
publication captures once, publishes selected products, then publishes the CAT
index. A product failure prevents index publication. The JSON sidecar carries
`publicationCutoff`; the SDS schemas, signatures, and transport are unchanged.
An empty product list selects no product for that catalog identity.

Treat the CAT publication as the generation commit and pair it with the product
sidecar at the shared cutoff. Apply changed batches in journal order through a
completed generation. A batch crossing a snapshot cutoff is replayed in full;
duplicate records are harmless. With a fixed exclusion policy, the snapshot plus
post-cutoff batches produces the same selected map as a fresh compact snapshot.
Retries of a pinned selection produce identical content identifiers. A changed
selection or export metadata can produce different identifiers.

Ordinary deletion and expiry do not remove retained records from snapshots. For
an operator takedown, set `catalog_exclude_cids` on a retained CAT source rule to
an array of the CAT payload CIDs to remove. Exclusions are durable and additive;
they remove those selected index records and their unreferenced products from
future captures without restoring older versions. A replacement full snapshot
communicates the new exclusion policy. Existing immutable exports and captured
selections are not revoked. A producer can instead publish a new CAT record with
an empty product list to communicate product removal in its ordinary update stream.

To stop retention, drain every changed batch, remove `retain_for_publication`,
`paired_publication`, `catalog_product_source`, and exclusion settings for the
participating sources, and set `stop_publication_retention: true` on their exact
source rules. Restart. Pending or failed entries refuse retirement; completed
sources return to ordinary storage/export behavior. Removing a rule by itself
does not disable durable retention. If publication is disabled while durable
retention remains active, startup emits an explicit warning. After every source
has retired, stop the node and remove the `publication-journal` directory inside
its data directory. Keep backups and published shards according to their normal
retention policy. A future retained source requires an empty source at activation.

Reclamation requires successful changed-batch publication and absence from every
retained generation or live selection. The implementation conservatively retains
bytes; source retirement is the supported whole-journal removal boundary.
