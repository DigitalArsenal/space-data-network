# Complete SQL visibility

Scans, sorts, and joins can examine more records and exceed an execution budget.
Row, byte, result, deadline, and memory limits remain safeguards: exhaustion is an
error, rather than a successful result containing only the newest records.

The format-4 host registers every standard with explicit unlimited visibility
(type-spec tag 10, uint32 zero). A missing tag retains the engine's historical
default. Registration requires bit 0 of `flatsql_capabilities`; a missing export
or capability fails startup. The matching FlatSQL implementation is required;
the previously embedded artifact cannot serve this policy. SQL readers reopen
after registration to load the persisted policy. Opaque and field-sealed types
expose metadata and stored bytes, without reflected plaintext fields.

The engine promotes the STORE marker to layout 2 before persisting an unlimited
registration. Engines supporting only layout 1 refuse that store. Changing an
unlimited registration back to a bounded registration also fails. Back up the
store before enabling this policy. An interrupted marker write fails closed;
marker recovery must preserve layout 2 and use the supporting engine.

## Identify the store before enabling

Use the configured data root. Inspect `fsql4/STORE` and `fsql4/MIGRATED` with the
marker reader, checking lengths, magic, CRC32C, and matching UUIDs. The engine's
format-5 stream implementation uses this historical `fsql4` directory name:
directory names alone do not identify the disk format. STORE contains its
little-endian format at byte 4 and layout at byte 6. Format 5, layout 2 requires
unlimited-relation support. A corrupt or mixed marker pair requires recovery,
not creation of an empty replacement store.

A regular legacy `control.flatsqldb` without an activated stream store requires
the legacy migration path. Its SQL mirror, residency ledger, eviction, and
warm-boot reconciliation remain necessary. This change does not migrate legacy
data or remove those mechanisms. A separate migration must enumerate all live
durable rows, preserve bytes and provenance, validate counts and keys, survive
interruption, and retain the old store through validated activation. Comparing
only SQL mirror counts cannot prove migration completeness.

`format4.Tuning.RowCacheBytes` maps to engine config tag 51. Zero selects the
32 MiB shared row-order and verified-metadata cache. Pinned pages count against
admission; exhausted admission returns an explicit budget error. This cache
budget does not bound every engine allocation, checkpoint builder, or staging
structure.
