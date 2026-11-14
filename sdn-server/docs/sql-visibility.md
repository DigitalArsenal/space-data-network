# SQL visibility and store compatibility

Complete visibility can make scans, sorts and joins examine more records. Row,
byte, result, deadline and memory budgets still apply. Exhaustion is an error;
it does not turn an incomplete query into a successful result.

## Enable complete visibility explicitly

`storage.unlimited_sql_visibility` defaults to `false`. Bounded registrations
retain the existing 10,000-row default and 400,000-row OMM/TBS windows. With a
supporting engine, setting it to `true` registers explicit unlimited visibility
for every standard (type-spec tag 13, uint8 one). Missing tag 13, including the
historical tag 10 value zero, keeps bounded behavior.

The embedded FlatSQL 3.12.1 artifact does not support this option. Enabling it
requires a published artifact exporting `flatsql_capabilities` with bit 0 set.
The host checks that export before engine initialization and before creating
engine or auxiliary database files. A failed standard registration aborts
startup. Registration updates SQL schema state without a second engine open.

Back up the store before enabling the option. Registration permanently promotes
the STORE marker to layout 2. Turning the setting off afterward refuses startup;
it does not restore a bounded store. The update guard checks both stream format
and layout when considering a rollback slot. A binary declaring only layout 1
cannot reopen layout 2. Recovery from an interrupted promotion honors the
persisted intent and preserves the newer layout.

## Identify the store

The node backend is called format 4; its engine stream marker is format 5.
The directory remains `fsql4`. Directory names alone do not identify the disk
format. Use the marker reader to validate `fsql4/STORE` and `fsql4/MIGRATED`,
including lengths, magic, CRC32C and matching UUIDs. STORE encodes stream format
at byte 4 and layout at byte 6, both little-endian. A valid `STORE.unlimited`
promotion intent also requires layout 2 support. Corrupt or mixed markers
require recovery, not creation of an empty replacement store.

## Query access and budgets

Complete visibility does not grant access to additional private data. Opaque
and field-sealed types are omitted from the public SQL catalog and denied by
the sandbox, including metadata counts and raw-record shortcuts. Trusted
storage queries can read their metadata and stored bytes. Field sealing hides
the designated fields; other fields can remain readable in those bytes. CIDs
are computed from plaintext and can reveal equality with a guessed record.

The direct SQL HTTP route requires administrator trust. Module `storage_query`
uses sandboxed SQL with defaults of 200,000 output rows, five seconds and
128 MiB output. Its engine scan limits are separately one million examined rows
and 256 MiB bytes read. These are execution limits, not visibility windows.
Interactive storage queries have different limits. Point lookups with canonical
binary `_cid` equality use the index; scans and joins can still be expensive.
Unfiltered whole-type `COUNT(*)` and `COUNT(_cid)` use live metadata, subject to
quarantine and visibility rules. Filtered counts use the normal query path.

## Cache configuration

`storage.row_cache_bytes` maps to `format4.Tuning.RowCacheBytes` and engine config
tag 51. Zero selects 32 MiB. A nonzero value must be at least 16 KiB. The shared
budget covers cached row-order pages, verification metadata, views and run
descriptors, including pinned entries. Failed admission returns a distinct row-cache budget error, separate
from statement and result limits. This budget does not bound every allocation,
checkpoint builder or staging structure. Sustained ingest can exhaust the
fixed guest memory even when the row cache is below its limit.

## Legacy migration

A legacy `control.flatsqldb` needs `store-migrate --to 4` before using the stream
backend. Migration uses bounded registrations by default. Complete visibility
is enabled separately through the node setting after migration and validation;
the migration command does not silently promote layout. Its verification reads
durable records, bytes and provenance independently of SQL windows. Keep the
legacy store backup until activation and verification succeed. The legacy SQL
mirror, residency ledger, eviction and warm-boot reconciliation remain necessary
for legacy stores; this option does not retire them.
