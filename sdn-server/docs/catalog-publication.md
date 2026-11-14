# Durable catalog publication

Retained publication adds synchronous disk writes before ingestion and keeps a
second copy of CAT and OCM input. Its disk use grows with ingestion history;
automatic reclamation is absent. Snapshot selection reads retained history and
holds selected records in memory. Retained writes are serialized per store, and
snapshot capture holds that serialization while validating its selection.
Ordinary record readers keep the existing CAT overwrite policy. Sources without
these options keep their existing ingestion and publication behavior.

This configuration requires store format 4 and empty sources at activation.
Enable it before the initial catalog ingest. Activation refuses a nonempty,
unretained source because its overwritten history cannot be reconstructed.
Configuration is durable and additive: removing a publishing rule does not
remove retained bytes or disable retention for an already configured source.

```yaml
publishing:
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
      catalog_product_source: catalog-products
      min_interval: 24h
```

Matching retention overrides also need `keep_series: 0`. The OCM batch rule
uses durable deferred publication even without its own periodic source rule.
The CAT source rule produces the compact product snapshot before publishing
its CAT snapshot; both use one validated selection. Additional ordinary OCM
source rules are unnecessary. Retention requires exact provider and source
identities; wildcards are unsupported for these storage options.

Each retained ingestion first synchronizes a size-prefixed SDS stream and
metadata, then atomically renames the prepared transaction and synchronizes its
parent directory. Only then does the existing engine PUT run. An applied marker
is synchronized after successful storage. Preparation is a durable intent to
finish ingestion: an interrupted call can commit during recovery even if its
caller received no success response. Startup replays an unapplied intent before
returning the store. A failed retained write blocks further retained writes;
unrecoverable validation or storage errors fail startup instead of discarding
pending data. Replay preserves input bytes and verifies their checksum.

The publication service discovers retained batch identities independently of
live records and exports their exact retained bytes. The existing completed
series fingerprint acknowledges successful announcement. A shard alone is not
completion. A crash after announcement but before completion can repeat the
same content identifier; consumers deduplicate identifiers. Input expiry,
replacement, and deletion do not remove pending publication bytes.

A snapshot cutoff is a completed journal transaction sequence. The latest CAT
record for each catalog identity at that cutoff selects its OCM records by
content identifier. Resolution accepts the exact payload CID and the exact
size-prefixed SDS record CID used by catalog controllers. Every reference must
resolve before either selection is returned. Products committed without a CAT
update leave the previous selection intact. A missing reference refuses export;
a previous cutoff remains available. An empty product list records removal.
Deleting live records alone does not revise the retained catalog generation.

`CaptureCatalogSnapshot` returns immutable index and product selections.
`ExportCatalogSnapshot` validates the pair and returns its deterministic exports
together. Production publication exports all selected products before the index,
using those same selections across every bounded shard window. The existing
JSON index sidecar carries `publicationCutoff`; no SDS schema, signing method,
or transport is changed. The cutoff is local to the producer's retained store. Treat the CAT publication
as the generation commit and pair it with the OCM sidecar at that same cutoff;
an OCM announcement alone does not commit a catalog generation. An index with
no product references has an empty closure and needs no product shard. Retained older
series remain available across interruption between the two announcements.

To reconstruct a later catalog, apply the snapshot and changed batches whose
latest journal sequence exceeds its cutoff. A streamed batch crossing the
cutoff is replayed in full; duplicate prefix records are harmless. Apply CAT
selections by their `PRODUCTS_AS_OF` ordering and retain the exact referenced
products. The resulting selected map and product bytes equal a fresh snapshot
at the later cutoff. Arbitrarily reordered same-time, conflicting CAT selections
are not a defined catalog update order.

Reclamation requires both successful changed-batch publication and absence from
all retained snapshot or selection references. The implementation conservatively
retains the journal and its immutable bytes, including completed batches. A
future collector must enforce both conditions and retain completion receipts;
ordinary live-record expiry cannot serve as that collector. Exported immutable
shards remain subject to the existing publication-asset retention policy.

Tests use synthetic SDS fixtures and an inert announcement service. They cover
same-object replacement, rate limits and restart, compactness over four
editions, process failure around durable intent and storage commit, interruption
between product and index commits, concurrent replacement, input expiry,
missing-reference refusal, and snapshot-plus-post-cutoff convergence. Signed
network delivery remains covered by the separate publication integration tests.
