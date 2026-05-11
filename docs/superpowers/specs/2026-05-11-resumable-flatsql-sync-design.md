# Resumable FlatSQL Sync Design

## Goal

SDN providers must be able to move FlatSQL-backed SDS records to browsers and desktop replicas as fast ordered chunks, resume after interruption, and keep raw SDS records as FlatBuffers end to end. SQLite remains metadata and index storage; canonical record bytes stay in the FlatSQL stream backing files.

## Protocol Shape

The SDN-owned sync protocol is `/space-data-network/flatsql-sync/1.0.0`. HTTP endpoints are compatibility wrappers for the same message shape, and libp2p WebSocket/WebRTC streams should carry the same control fields and length-prefixed payload frames.

The protocol has three phases:

1. `OpenSnapshot`: client asks for schema, source identity, query profile, and an optional previous cursor. Provider returns total rows, snapshot/head identity, first cursor, max chunk size, and source metadata.
2. `ReadChunk`: client sends snapshot/head and cursor. Provider streams raw FlatBuffer record frames in deterministic order and returns next cursor, chunk hash, row count, and total count.
3. `AckProgress`: client persists the cursor, local row count, verified chunk hash, and local cache/pin state before requesting the next chunk.

The current HTTP increment maps this to `/api/v1/data/scan` and `/api/v1/data/stream`: scan returns ordered refs and stream sends the matching raw FlatBuffer frames. The immediate fix is to make these endpoints support large chunks and avoid per-record SQL lookups. A follow-up endpoint can combine scan metadata and raw frames into one stream once libp2p framing lands.

Implementation update, 2026-05-11: the shared SDN `datasync` contract now backs HTTP scan/stream and the libp2p stream protocol. `/api/v1/data/scan` returns `snapshot_id`, `head`, `high_water_mark`, `cursor`, `next_cursor`, `scan_hash`, `chunk_hash`, `sync_protocol`, `max_chunk_size`, `transports`, `total_count`, and ordered refs. `/api/v1/data/stream` verifies `scan_hash`/`chunk_hash` against the requested refs before streaming and echoes the same resume fields in `X-SDN-*` headers.

The libp2p protocol uses one `uint32be length + JSON` control frame followed by `uint32be length + raw FlatBuffer bytes` frames for `read_chunk`. `scan`/`open_snapshot` return only the metadata frame. `ack_progress` stores/echoes resume fields so clients can persist provider progress before requesting the next chunk. The same protocol ID is available through libp2p WebSocket/WebTransport/WebRTC-capable nodes.

## Resume Model

Resume is cursor-based, not "start over and count rows" long term. A replica persists, per source/schema/profile:

- provider peer ID and public key;
- schema name and query profile;
- snapshot/head;
- next cursor;
- local row count;
- verified chunk hashes;
- pinned byte budget and current cached bytes;
- last successful sync time and last error.

On restart, refresh, transport failure, or peer reconnect, the client reopens the snapshot. If the provider reports the same head, the client resumes from the persisted cursor. If the head changed, the client keeps verified local rows, requests the provider's delta/publication log, and resumes from the closest valid high-water marker. Until PLOG/PLHD heads are fully wired, the HTTP wrapper can resume by offset/local-row count but must expose the stronger head/cursor fields so the UI and storage ledger do not bake in offset semantics.

Implementation update, 2026-05-11: browser sync persists this ledger in local storage per selected data source and SDS schema. It resumes with the provider `next_cursor` when the `head` matches. If the provider head changes, it preserves verified local rows and restarts from the current local row offset until publication-log delta sync is available.

## Chunk Format

Data frames are `uint32be length + raw FlatBuffer bytes`. The server reads payload bytes directly from the FlatSQL stream files and writes network frames without base64 or JSON translation. Control metadata is out of band for HTTP headers today and should become FlatBuffer control frames for libp2p. Control metadata is JSON for the first libp2p implementation so older HTTP clients and the browser dialer share the same field names. The data plane remains raw FlatBuffer frames with no base64 or JSON payload translation.

The record payload frame is already the SDS FlatBuffer stored by FlatSQL. If the on-disk FlatSQL stream has its own little-endian length prefix, the server verifies it and sends only the raw FlatBuffer payload inside the SDN network frame.

## Provider Storage Requirements

Provider scan paths must be metadata-only. They should return refs from SQLite metadata/index rows without opening FlatSQL backing files.

Provider stream paths must resolve requested refs in batches and read grouped stream ranges with file-handle reuse. They must not do one SQLite query or one file open per record. For CelesTrak-scale OMM chunks, the default chunk should be thousands of records, with a higher server maximum for desktop/native sync.

## Client Storage Requirements

The browser/desktop local store ingests streamed record bytes into the selected local FlatSQL datastore namespace. It persists progress after bounded batches and after every successful final chunk. Sync progress is durable enough to resume without duplicating rows or resetting local SQL caches.

The UI continues to query only the local FlatSQL datastore. Remote providers expose counts, refs, chunks, publication heads, and artifacts.

## Verification

The first implementation increment is accepted when:

- scan requests above 1,000 rows are accepted and return metadata refs without embedded payload bytes;
- stream requests above 1,000 refs are accepted;
- the stream path writes raw FlatBuffer frames in requested order;
- the stream path uses a batched ref lookup and direct FlatSQL stream reads instead of per-record SQL calls;
- browser sync can keep using the same ordered scan/stream abstraction while larger chunks and durable progress make resume practical.
- HTTP scan/stream and libp2p scan/read_chunk expose the same snapshot/head/cursor/chunk metadata;
- browser and desktop sync persist per-source/schema resume state and can restart from the provider cursor after refresh or reconnect.
