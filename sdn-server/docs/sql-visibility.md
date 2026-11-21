# SQL visibility and store compatibility

Complete visibility can make scans, sorts and joins examine more records. Row,
byte, result, deadline and memory budgets still apply. Exhaustion is an error;
it does not turn an incomplete query into a successful result.

## Enable complete visibility explicitly

`storage.unlimited_sql_visibility` is unset by default. Unset, the store
decides:

- A new store (an empty data directory) is created at layout 2 with unlimited
  visibility when the embedded engine supports layout 2 (the binary is stamped
  `MaxStoreLayout` 2). It holds no records a rollback to an older build would
  need. A binary stamped layout 1 creates it bounded.
- An existing store that is not promoted keeps bounded registrations (the
  existing 10,000-row default and 400,000-row OMM/TBS windows). An upgrade never
  promotes a store by default.
- A store already at layout 2 (or with a pending promotion) opens with unlimited
  visibility: a build started on a store that a later build or an explicit
  setting promoted, for example after the update channel rolled back to it,
  keeps it running.

With a supporting engine, `true`
registers explicit unlimited visibility for every standard (type-spec tag 13,
uint8 one) and promotes the store. `false` keeps bounded visibility and refuses
a promoted store. Missing tag 13, including the historical tag 10 value zero,
keeps bounded behavior.

The embedded FlatSQL 3.12.13 artifact supports this option (it exports
`flatsql_capabilities` with bit 0 set; the binary is stamped `MaxStoreLayout`
2). The host checks that export before engine initialization and before creating
engine or auxiliary database files. A failed standard registration aborts
startup. Registration updates SQL schema state without a second engine open.

Back up the store before enabling the option. Registration permanently promotes
the STORE marker to layout 2. Setting it to `false` afterward refuses startup;
it does not restore a bounded store. Removing the setting keeps the store
unlimited. The update guard checks store format and
layout when considering a rollback slot. A binary declaring only layout 1
cannot reopen layout 2. Layout-1 stores remain compatible with binaries stamped
for store format 5. Recovery from an interrupted promotion honors the persisted
intent and preserves the newer layout.

## Identify the store

The stream backend writes format 5 under `fsql5/` (retaining `fsql4/` as a
rollback copy when converted from format 4). Directory names alone do not
identify the disk format. `spacedatanetwork status --config <config>` prints
backend format, stream format, layout and required layout.
`spacedatanetwork store-migrate --store <data> --status` reads the markers
without opening an engine or changing files. The marker reader validates
lengths, magic, CRC32C and UUIDs. STORE encodes stream format at byte 4 and
layout at byte 6, both little-endian. A valid `STORE.unlimited` promotion
intent also requires layout 2 support. Corrupt or mixed markers require
recovery, not creation of an empty replacement store.

## Query access and budgets

Complete visibility does not grant access to additional private data. Opaque
and field-sealed types have no SQL relation, including through trusted module
queries, metadata counts and raw-record shortcuts. Durable CID access remains
subject to the existing storage authorization. A statement naming a relation
the store does not have (a standard the node does not register, a CID-only or
field-sealed one, an unknown source) fails on every path ("no such table";
"no SQL relation" on the trusted raw stream modules read); it is never an
empty answer. A
registered standard with no records answers an empty result.

A standard whose field SDN seals at rest is one an extraction rule reads is
not registered, on a bounded and an unlimited store alike: the node starts,
logs a warning naming the standard, the field and the rule, refuses every
write to it, and a statement naming it fails. `spacedatanetwork status` and
`store-migrate --status` list such standards (`store_unregistered_standards`,
`none` for the embedded standards). Any other failed registration of an
unlimited store aborts startup before the store is promoted.

`QueryAll` (the newest records of a standard, `GET /api/v1/pubsub/messages`)
answers at most 10,000 records and says when more are stored: the page is
marked truncated (QRP `TRUNCATED`, status partial). Complete reads page with
the data scan or keyset SQL.

The direct SQL HTTP route requires administrator trust. Public sandboxed SQL
uses defaults of 200,000 output rows, five seconds and
128 MiB output. Its engine scan limits are separately one million examined rows
and 256 MiB bytes read. These are execution limits, not visibility windows.
Interactive storage queries have different limits. Point lookups with canonical
binary `_cid` equality use the index; scans and joins can still be expensive.
Unfiltered whole-type `COUNT(*)` and `COUNT(_cid)` use live metadata, subject to
quarantine and visibility rules. Filtered counts use the normal query path.

## Cache configuration

`storage.row_cache_bytes` maps to `format4.Tuning.RowCacheBytes` and engine config
tag 52. Zero selects 32 MiB. A nonzero value must be at least 16 KiB. The shared
budget covers cached row-order pages, verification metadata, views and run
descriptors, including pinned entries. Failed admission returns a distinct row-cache budget error, separate
from statement and result limits. This budget does not bound every allocation,
checkpoint builder or staging structure. Sustained ingest can exhaust the
fixed guest memory even when the row cache is below its limit.

## Legacy migration

A legacy `control.flatsqldb` needs `store-migrate --to 5` before using the stream
backend. Migration uses bounded registrations by default. Complete visibility
is enabled separately through the node setting after migration and validation;
the migration command does not silently promote layout. Its verification reads
durable records, bytes and provenance independently of SQL windows. Keep the
legacy store backup until activation and verification succeed. The legacy SQL
mirror, residency ledger, eviction and warm-boot reconciliation remain necessary
for legacy stores; this option does not retire them.
