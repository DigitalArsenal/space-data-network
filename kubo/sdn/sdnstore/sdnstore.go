// Package sdnstore is the SDN storage adapter: it stores Space Data Standards
// records keyed by (source id, 3-letter SDS type) over kubo's content-addressed
// blockstore, with a durable datastore index and the FlatSQL WASI engine as a
// bounded, lazily-repopulated SQL query cache.
//
// # Storage model (owner directive)
//
// "the flatsql runs as a wasm module ... no replays needed, no hydration ...
// the flatsql stores data by source id and standard 3 letter id." Raw SDS
// records STAY FlatBuffers (they are never re-encoded into SQLite rows or
// stored as opaque blobs in SQL tables); canonical tables use the SDS names
// (OMM/MPE/CAT...); source/provenance is first-class.
//
// # Where durability lives
//
// There is NO journal, NO boot replay, and NO hydration-on-open. The two
// durable stores are:
//
//   - the blockstore: each record's FlatBuffer bytes are put as a raw
//     CID-addressed block (content-addressed => automatic dedup, GC-able,
//     bitswap-replicable). This is THE durable record store.
//   - the datastore index: a namespaced keyspace records, per stored record,
//     (source, type, cid, monotonic sequence, epoch-if-parsable). A tiny
//     catalog keyspace records the set of (source, type) pairs that exist.
//
// The FlatSQL engine holds NO durable state. On reopen it starts EMPTY and is
// repopulated only as a BOUNDED hot window (the HotWindow most-recent records
// per (source, type)), lazily, by reading the index and fetching those blocks —
// bounded and lazy, never a full-catalog replay. This respects the ~4 GiB
// wasm32 linear-memory residency ceiling: engine residency is bounded by
// HotWindow x record-size x number-of-(source,type)-pairs, and HotWindow is the
// operator's lever. Records outside the hot window remain fully durable in the
// blockstore and are served by ReadBySourceType, which reconstructs directly
// from the index + blockstore and never touches the engine.
//
// This is deliberately NOT a port of the sdn-server record-catalog journal,
// replay, compaction, store-lock, or flatsqldrv machinery — none of that is
// needed once the blockstore + datastore index are the durable truth.
package sdnstore

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	blockstore "github.com/ipfs/boxo/blockstore"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dsq "github.com/ipfs/go-datastore/query"
	mh "github.com/multiformats/go-multihash"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
)

// DefaultHotWindow is the per-(source, type) record cap the FlatSQL query
// cache is repopulated to on reopen when Config.HotWindow is unset.
const DefaultHotWindow = 4096

// keyspace roots in the datastore index. The blockstore owns its own keyspace
// (typically /blocks); these are the sdnstore index/catalog namespaces.
const (
	nsRoot = "/sdn/sdnstore"
	nsRec  = nsRoot + "/rec" // /rec/<hexType>/<hexSource>/<cid> -> indexEntry
	nsCat  = nsRoot + "/cat" // /cat/<hexType>/<hexSource>       -> catEntry
	nsSeq  = nsRoot + "/seq" // monotonic 8-byte big-endian counter
)

// SchemaProvider resolves an SDS 3-letter type to the FlatSQL schema that
// defines its table, the 4-byte FlatBuffer file identifier that routes ingest,
// and the SQL table name. Keeping schema resolution behind this interface keeps
// sdnstore implementation-neutral: it embeds no SDS schemas of its own and can
// store any 3-letter type the provider knows.
type SchemaProvider interface {
	// Schema returns the .fbs schema text, the FlatBuffer file identifier
	// (e.g. "$OMM"), and the SQL table name (e.g. "OMM") for sdsType, or
	// ok=false if the type is unknown.
	Schema(sdsType string) (schema, fileID, tableName string, ok bool)
}

// SchemaProviderFunc adapts a function to SchemaProvider.
type SchemaProviderFunc func(sdsType string) (schema, fileID, tableName string, ok bool)

// Schema implements SchemaProvider.
func (f SchemaProviderFunc) Schema(sdsType string) (string, string, string, bool) {
	return f(sdsType)
}

// EpochExtractor best-effort extracts a sortable epoch (Unix seconds) from a
// record's FlatBuffer bytes so the hot window can prefer the most-recent
// records. It is optional; when nil or ok=false the store falls back to the
// monotonic store sequence ("most-recent" == most-recently-stored). Keeping it
// pluggable keeps sdnstore neutral: it does not itself parse any SDS type.
type EpochExtractor func(sdsType string, fb []byte) (epoch int64, ok bool)

// Config configures a Store.
type Config struct {
	// Blockstore is the durable content-addressed record store (e.g.
	// core.IpfsNode.Blockstore). Required.
	Blockstore blockstore.Blockstore
	// Datastore is the durable index/catalog keyspace (e.g.
	// core.IpfsNode.Repo.Datastore()). Required. sdnstore namespaces its keys
	// under /sdn/sdnstore so it can share the repo datastore.
	Datastore ds.Datastore
	// Schemas resolves SDS types to FlatSQL schemas. Required.
	Schemas SchemaProvider
	// EpochOf optionally orders the hot window by record epoch. Optional.
	EpochOf EpochExtractor
	// HotWindow caps how many most-recent records per (source, type) are
	// repopulated into the FlatSQL query cache on reopen. 0 => DefaultHotWindow.
	HotWindow int
	// RuntimeOptions are passed through to the FlatSQL engine (e.g. an AOT
	// cache dir). Optional.
	RuntimeOptions []flatsqlrt.Option
	// OnStore, if set, is the fan-out hook: it is invoked once, after a record
	// is durably stored (block + index + engine ingest), for each NEWLY stored
	// (source, type, cid) record — never for an idempotent, byte-identical
	// re-store. The channels layer supplies a hook here (via Channels.Publisher)
	// to stream the record out to its (source, standard) gossipsub channel;
	// sdnstore itself does not import channels. A non-nil error fails the Store
	// call, so a caller wanting best-effort fan-out should swallow publish
	// errors inside the hook. Optional.
	OnStore func(ctx context.Context, source, sdsType string, recordCID cid.Cid, fb []byte) error
}

// Store keys SDS records by (source, 3-letter type) over a kubo blockstore,
// with a durable datastore index and a bounded FlatSQL query cache.
type Store struct {
	bs        blockstore.Blockstore
	idx       ds.Datastore
	schemas   SchemaProvider
	epochOf   EpochExtractor
	hotWindow int

	onStore func(ctx context.Context, source, sdsType string, recordCID cid.Cid, fb []byte) error

	mu  sync.Mutex // serializes engine + catalog mutation; the engine is single-threaded
	rt  *flatsqlrt.Runtime
	dbs map[string]*typeDB // sdsType -> engine database + registered sources
}

// typeDB is the FlatSQL database backing one SDS type (one table, per-source
// shadow tables). hydrated tracks whether the bounded hot window for this type
// has been read back from the durable stores yet (lazy, once per reopen).
type typeDB struct {
	db        *flatsqlrt.Database
	fileID    string
	tableName string
	sources   map[string]bool // sources RegisterSource'd in the engine
	hydrated  bool
}

// indexEntry is the durable per-record index value.
type indexEntry struct {
	CID   string `json:"cid"`
	Seq   uint64 `json:"seq"`
	Epoch int64  `json:"epoch,omitempty"`
	Size  int    `json:"size"`
}

// catEntry records that a (source, type) pair exists (human-readable form of
// the hex-encoded key).
type catEntry struct {
	Source string `json:"source"`
	Type   string `json:"type"`
}

// Open constructs a Store over the given blockstore + datastore. It does NOT
// read anything back into the FlatSQL engine: the engine starts empty and each
// (source, type) hot window is repopulated lazily on first access.
func Open(cfg Config) (*Store, error) {
	if cfg.Blockstore == nil {
		return nil, errors.New("sdnstore: Config.Blockstore is required")
	}
	if cfg.Datastore == nil {
		return nil, errors.New("sdnstore: Config.Datastore is required")
	}
	if cfg.Schemas == nil {
		return nil, errors.New("sdnstore: Config.Schemas is required")
	}
	hw := cfg.HotWindow
	if hw <= 0 {
		hw = DefaultHotWindow
	}
	rt, err := flatsqlrt.New(cfg.RuntimeOptions...)
	if err != nil {
		return nil, fmt.Errorf("sdnstore: start FlatSQL engine: %w", err)
	}
	return &Store{
		bs:        cfg.Blockstore,
		idx:       cfg.Datastore,
		schemas:   cfg.Schemas,
		epochOf:   cfg.EpochOf,
		hotWindow: hw,
		onStore:   cfg.OnStore,
		rt:        rt,
		dbs:       make(map[string]*typeDB),
	}, nil
}

// Close releases the FlatSQL engine. The durable blockstore + datastore are
// owned by the caller and are NOT touched; a subsequent Open over the same two
// stores recovers every record with no journal or replay.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rt != nil {
		s.rt.Close()
		s.rt = nil
	}
	s.dbs = nil
}

// normalizeType validates and normalizes a 3-letter SDS type.
func normalizeType(sdsType string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(sdsType))
	if len(t) != 3 {
		return "", fmt.Errorf("sdnstore: SDS type %q must be exactly 3 letters", sdsType)
	}
	return t, nil
}

func recPrefix(sdsType, source string) string {
	return nsRec + "/" + hex.EncodeToString([]byte(sdsType)) + "/" + hex.EncodeToString([]byte(source)) + "/"
}

func recKey(sdsType, source, cidStr string) ds.Key {
	return ds.RawKey(recPrefix(sdsType, source) + cidStr)
}

func catKey(sdsType, source string) ds.Key {
	return ds.RawKey(nsCat + "/" + hex.EncodeToString([]byte(sdsType)) + "/" + hex.EncodeToString([]byte(source)))
}

// blockCID derives the raw-codec, sha256 CID for a record's bytes. Identical
// bytes always map to the same CID (dedup); the codec is Raw because the bytes
// are opaque FlatBuffers, not IPLD.
func blockCID(fb []byte) (cid.Cid, error) {
	h, err := mh.Sum(fb, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, h), nil
}

// Store durably stores one SDS record (a single FlatBuffer, WITHOUT a
// size prefix) tagged by (source, 3-letter type):
//
//  1. the FlatBuffer bytes become a CID-addressed block in the blockstore
//     (durable, deduplicated, GC-able, bitswap-replicable) — the durable store;
//  2. (source, type, cid, seq, epoch-if-parsable) is written to the durable
//     datastore index, and the (source, type) pair is recorded in the catalog;
//  3. the record is ingested into the FlatSQL engine under its per-source
//     shadow table (`<TABLE>@<source>`) for SQL querying.
//
// Storing byte-identical records twice is idempotent: the block and the index
// entry are content-addressed, so the second Store is a no-op past the
// blockstore Put (and does not double-ingest into the engine).
func (s *Store) Store(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	t, err := normalizeType(sdsType)
	if err != nil {
		return cid.Undef, err
	}
	if source = strings.TrimSpace(source); source == "" {
		return cid.Undef, errors.New("sdnstore: source must be non-empty")
	}
	if len(fb) == 0 {
		return cid.Undef, errors.New("sdnstore: record bytes must be non-empty")
	}

	c, err := blockCID(fb)
	if err != nil {
		return cid.Undef, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// The bounded hot window for this type must be recovered before the first
	// live ingest so the engine's row set stays consistent with the durable
	// index (hydrate loads what already exists; the new record is ingested
	// afterwards and is not yet in the index).
	if err := s.ensureHydratedLocked(ctx, t); err != nil {
		return cid.Undef, err
	}

	// Durable block (content-addressed; Has-guarded to avoid a redundant Put).
	if has, err := s.bs.Has(ctx, c); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: blockstore Has: %w", err)
	} else if !has {
		blk, err := blocks.NewBlockWithCid(fb, c)
		if err != nil {
			return cid.Undef, err
		}
		if err := s.bs.Put(ctx, blk); err != nil {
			return cid.Undef, fmt.Errorf("sdnstore: blockstore Put: %w", err)
		}
	}

	// Durable index. A pre-existing entry means this exact record was already
	// stored (and is already represented in the engine); skip re-indexing and
	// re-ingesting so the durable layer and the engine both stay deduplicated.
	rk := recKey(t, source, c.String())
	if exists, err := s.idx.Has(ctx, rk); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: index Has: %w", err)
	} else if exists {
		return c, nil
	}

	seq, err := s.nextSeqLocked(ctx)
	if err != nil {
		return cid.Undef, err
	}
	var epoch int64
	if s.epochOf != nil {
		if e, ok := s.epochOf(t, fb); ok {
			epoch = e
		}
	}
	entryBytes, err := json.Marshal(indexEntry{CID: c.String(), Seq: seq, Epoch: epoch, Size: len(fb)})
	if err != nil {
		return cid.Undef, err
	}
	if err := s.idx.Put(ctx, rk, entryBytes); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: index Put: %w", err)
	}
	if err := s.ensureCatalogLocked(ctx, t, source); err != nil {
		return cid.Undef, err
	}

	// Ingest into the FlatSQL query cache under the per-source shadow table.
	tdb, err := s.ensureTypeDBLocked(t)
	if err != nil {
		return cid.Undef, err
	}
	if err := s.ensureSourceLocked(tdb, source); err != nil {
		return cid.Undef, err
	}
	if _, err := tdb.db.IngestOneWithSource(fb, source); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: FlatSQL ingest: %w", err)
	}

	// Fan-out hook: only for this newly stored record (the idempotent re-store
	// path returned above). Runs after the record is durable in every store.
	if s.onStore != nil {
		if err := s.onStore(ctx, source, t, c, fb); err != nil {
			return cid.Undef, fmt.Errorf("sdnstore: OnStore fan-out: %w", err)
		}
	}
	return c, nil
}

// StoreManifest durably stores one COMPOSITION record — an SDS record that is
// read back whole rather than queried tabularly, e.g. an $APP record — tagged by
// (source, 3-letter type). Like Store it writes the FlatBuffer bytes as a
// content-addressed block, records (source, type, cid, seq) in the durable index
// and the (source, type) pair in the catalog, and is idempotent for
// byte-identical records. UNLIKE Store it does NOT ingest the record into the
// FlatSQL query engine and therefore needs NO per-type FlatSQL schema:
// composition records are served only through ReadBySourceType (whole-record) and
// enumerated through Catalog, neither of which touches the engine, so ingesting
// them would only inflate engine residency (a single $APP inlines its whole UI
// page) for a table nothing queries. This is the write-path the app installer
// (sdn/sdnapps) uses to install $APP records the node then lists at
// GET /sdn/v1/apps and serves at GET /sdn/v1/apps/<id>.
func (s *Store) StoreManifest(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	t, err := normalizeType(sdsType)
	if err != nil {
		return cid.Undef, err
	}
	if source = strings.TrimSpace(source); source == "" {
		return cid.Undef, errors.New("sdnstore: source must be non-empty")
	}
	if len(fb) == 0 {
		return cid.Undef, errors.New("sdnstore: record bytes must be non-empty")
	}

	c, err := blockCID(fb)
	if err != nil {
		return cid.Undef, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Durable block (content-addressed; Has-guarded to avoid a redundant Put).
	if has, err := s.bs.Has(ctx, c); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: blockstore Has: %w", err)
	} else if !has {
		blk, err := blocks.NewBlockWithCid(fb, c)
		if err != nil {
			return cid.Undef, err
		}
		if err := s.bs.Put(ctx, blk); err != nil {
			return cid.Undef, fmt.Errorf("sdnstore: blockstore Put: %w", err)
		}
	}

	// Durable index. A pre-existing entry means this exact record was already
	// stored; the write is idempotent.
	rk := recKey(t, source, c.String())
	if exists, err := s.idx.Has(ctx, rk); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: index Has: %w", err)
	} else if exists {
		return c, nil
	}

	seq, err := s.nextSeqLocked(ctx)
	if err != nil {
		return cid.Undef, err
	}
	entryBytes, err := json.Marshal(indexEntry{CID: c.String(), Seq: seq, Size: len(fb)})
	if err != nil {
		return cid.Undef, err
	}
	if err := s.idx.Put(ctx, rk, entryBytes); err != nil {
		return cid.Undef, fmt.Errorf("sdnstore: index Put: %w", err)
	}
	if err := s.ensureCatalogLocked(ctx, t, source); err != nil {
		return cid.Undef, err
	}
	return c, nil
}

// ReadBySourceType returns every stored record for (source, type) as raw
// FlatBuffer bytes, reconstructed directly from the durable index + blockstore.
// This path does NOT touch the FlatSQL engine, which is exactly what proves
// durability without any journal or replay: after a reopen the records are
// recovered purely from content-addressed blocks and their index. Records are
// returned oldest-first (store order).
func (s *Store) ReadBySourceType(ctx context.Context, source, sdsType string) ([][]byte, error) {
	t, err := normalizeType(sdsType)
	if err != nil {
		return nil, err
	}
	entries, err := s.indexEntries(ctx, t, source)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })

	out := make([][]byte, 0, len(entries))
	for _, e := range entries {
		c, err := cid.Decode(e.CID)
		if err != nil {
			return nil, fmt.Errorf("sdnstore: bad cid %q in index: %w", e.CID, err)
		}
		blk, err := s.bs.Get(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("sdnstore: fetch block %s: %w", e.CID, err)
		}
		// Copy so callers cannot mutate blockstore-owned buffers.
		raw := blk.RawData()
		cp := make([]byte, len(raw))
		copy(cp, raw)
		out = append(out, cp)
	}
	return out, nil
}

// Sources lists the sources known for a type (from the durable catalog).
func (s *Store) Sources(ctx context.Context, sdsType string) ([]string, error) {
	t, err := normalizeType(sdsType)
	if err != nil {
		return nil, err
	}
	return s.catalogSources(ctx, t)
}

// CatalogEntry is one (source, 3-letter type) pair known to the store.
type CatalogEntry struct {
	Source string `json:"source"`
	Type   string `json:"type"`
}

// Catalog returns every (source, 3-letter type) pair recorded in the durable
// catalog, sorted by (type, source). It reads only the catalog keyspace — one
// entry per pair — so its cost is proportional to the number of distinct
// (source, type) pairs, NOT the number of records. That makes it a cheap
// enumeration for a status/API surface that needs the shape of the store
// without scanning the record index.
func (s *Store) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	res, err := s.idx.Query(ctx, dsq.Query{Prefix: nsCat + "/"})
	if err != nil {
		return nil, fmt.Errorf("sdnstore: catalog query: %w", err)
	}
	defer res.Close()
	var out []CatalogEntry
	for r := range res.Next() {
		if r.Error != nil {
			return nil, r.Error
		}
		var ce CatalogEntry
		if err := json.Unmarshal(r.Value, &ce); err != nil {
			return nil, fmt.Errorf("sdnstore: decode catalog entry: %w", err)
		}
		out = append(out, ce)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

// Query runs read-only SQL for (source, type) against the FlatSQL engine and
// returns the aligned size-prefixed FlatBuffer stream (every selected cell must
// be a BLOB; use the hidden `_data` column). The (source, type) hot window is
// lazily repopulated into the engine on first use after a reopen. The table for
// SQL is the per-source shadow table `<TABLE>@<source>`.
func (s *Store) Query(ctx context.Context, source, sdsType, sql string, params ...interface{}) (*flatsqlrt.RawStream, error) {
	t, err := normalizeType(sdsType)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureHydratedLocked(ctx, t); err != nil {
		return nil, err
	}
	tdb, ok := s.dbs[t]
	if !ok {
		return nil, fmt.Errorf("sdnstore: no records for type %q", t)
	}
	if !tdb.sources[source] {
		if err := s.ensureSourceLocked(tdb, source); err != nil {
			return nil, err
		}
	}
	return tdb.db.QueryRawFlatBufferStream(sql, params...)
}

// ShadowTable is the FlatSQL per-source table name for (source, type), the name
// Query's SQL should reference.
func (s *Store) ShadowTable(source, sdsType string) (string, error) {
	t, err := normalizeType(sdsType)
	if err != nil {
		return "", err
	}
	tableName, err := s.tableNameFor(t)
	if err != nil {
		return "", err
	}
	return tableName + "@" + source, nil
}

func (s *Store) tableNameFor(t string) (string, error) {
	_, _, tableName, ok := s.schemas.Schema(t)
	if !ok {
		return "", fmt.Errorf("sdnstore: no schema registered for type %q", t)
	}
	return tableName, nil
}

// --- engine wiring (all called under s.mu) ---

func (s *Store) ensureTypeDBLocked(t string) (*typeDB, error) {
	if tdb, ok := s.dbs[t]; ok {
		return tdb, nil
	}
	schema, fileID, tableName, ok := s.schemas.Schema(t)
	if !ok {
		return nil, fmt.Errorf("sdnstore: no schema registered for type %q", t)
	}
	db, err := s.rt.CreateDatabase(schema, t)
	if err != nil {
		return nil, fmt.Errorf("sdnstore: create FlatSQL database for %q: %w", t, err)
	}
	if err := db.RegisterFileID(fileID, tableName); err != nil {
		return nil, fmt.Errorf("sdnstore: register file id %q for %q: %w", fileID, t, err)
	}
	tdb := &typeDB{db: db, fileID: fileID, tableName: tableName, sources: make(map[string]bool)}
	s.dbs[t] = tdb
	return tdb, nil
}

func (s *Store) ensureSourceLocked(tdb *typeDB, source string) error {
	if tdb.sources[source] {
		return nil
	}
	if err := tdb.db.RegisterSource(source); err != nil {
		return fmt.Errorf("sdnstore: register source %q: %w", source, err)
	}
	tdb.sources[source] = true
	return nil
}

// ensureHydratedLocked repopulates the BOUNDED hot window for type t into the
// FlatSQL engine, once per reopen. For each source of the type it reads the
// index, keeps the HotWindow most-recent records (epoch desc, then store
// sequence desc), fetches exactly those blocks, and ingests them. This is
// bounded (<= HotWindow per source) and lazy (only for a type actually
// touched) — never a full-catalog replay.
func (s *Store) ensureHydratedLocked(ctx context.Context, t string) error {
	tdb, err := s.ensureTypeDBLocked(t)
	if err != nil {
		return err
	}
	if tdb.hydrated {
		return nil
	}
	tdb.hydrated = true // set first: a failure mid-hydrate must not loop, and live ingest continues from here

	sources, err := s.catalogSources(ctx, t)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if err := s.ensureSourceLocked(tdb, source); err != nil {
			return err
		}
		entries, err := s.indexEntries(ctx, t, source)
		if err != nil {
			return err
		}
		// Most-recent first, then trim to the hot window.
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Epoch != entries[j].Epoch {
				return entries[i].Epoch > entries[j].Epoch
			}
			return entries[i].Seq > entries[j].Seq
		})
		if len(entries) > s.hotWindow {
			entries = entries[:s.hotWindow]
		}
		// Ingest oldest-first within the window so engine row order matches
		// store order.
		sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
		for _, e := range entries {
			c, err := cid.Decode(e.CID)
			if err != nil {
				return fmt.Errorf("sdnstore: bad cid %q in index: %w", e.CID, err)
			}
			blk, err := s.bs.Get(ctx, c)
			if err != nil {
				return fmt.Errorf("sdnstore: hydrate fetch block %s: %w", e.CID, err)
			}
			if _, err := tdb.db.IngestOneWithSource(blk.RawData(), source); err != nil {
				return fmt.Errorf("sdnstore: hydrate ingest: %w", err)
			}
		}
	}
	return nil
}

// --- durable index / catalog / sequence (do not require s.mu, but callers on
// the engine path hold it) ---

func (s *Store) nextSeqLocked(ctx context.Context) (uint64, error) {
	var cur uint64
	v, err := s.idx.Get(ctx, ds.RawKey(nsSeq))
	switch {
	case err == nil:
		if len(v) == 8 {
			cur = binary.BigEndian.Uint64(v)
		}
	case errors.Is(err, ds.ErrNotFound):
		cur = 0
	default:
		return 0, fmt.Errorf("sdnstore: read seq: %w", err)
	}
	next := cur + 1
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], next)
	if err := s.idx.Put(ctx, ds.RawKey(nsSeq), buf[:]); err != nil {
		return 0, fmt.Errorf("sdnstore: write seq: %w", err)
	}
	return next, nil
}

func (s *Store) ensureCatalogLocked(ctx context.Context, t, source string) error {
	k := catKey(t, source)
	if has, err := s.idx.Has(ctx, k); err != nil {
		return fmt.Errorf("sdnstore: catalog Has: %w", err)
	} else if has {
		return nil
	}
	b, err := json.Marshal(catEntry{Source: source, Type: t})
	if err != nil {
		return err
	}
	if err := s.idx.Put(ctx, k, b); err != nil {
		return fmt.Errorf("sdnstore: catalog Put: %w", err)
	}
	return nil
}

func (s *Store) catalogSources(ctx context.Context, t string) ([]string, error) {
	prefix := nsCat + "/" + hex.EncodeToString([]byte(t)) + "/"
	res, err := s.idx.Query(ctx, dsq.Query{Prefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("sdnstore: catalog query: %w", err)
	}
	defer res.Close()
	var sources []string
	for r := range res.Next() {
		if r.Error != nil {
			return nil, r.Error
		}
		var ce catEntry
		if err := json.Unmarshal(r.Value, &ce); err != nil {
			return nil, fmt.Errorf("sdnstore: decode catalog entry: %w", err)
		}
		sources = append(sources, ce.Source)
	}
	sort.Strings(sources)
	return sources, nil
}

func (s *Store) indexEntries(ctx context.Context, t, source string) ([]indexEntry, error) {
	res, err := s.idx.Query(ctx, dsq.Query{Prefix: recPrefix(t, source)})
	if err != nil {
		return nil, fmt.Errorf("sdnstore: index query: %w", err)
	}
	defer res.Close()
	var entries []indexEntry
	for r := range res.Next() {
		if r.Error != nil {
			return nil, r.Error
		}
		var e indexEntry
		if err := json.Unmarshal(r.Value, &e); err != nil {
			return nil, fmt.Errorf("sdnstore: decode index entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
