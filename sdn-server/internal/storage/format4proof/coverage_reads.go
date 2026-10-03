package format4proof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The V coverage classes: reads of the fixture as it is, one shape per axis
// combination the benchset leaves out (COVERAGE.md). Parameters are the
// fixture's: R01's hit CIDs, R02's misses, OMM batch b052 (Inputs), the
// lanes, peers, objects and epochs of benchset fixtures.t6w.

// The fixture's producers and its IQC lane batch (benchset fixtures.t6w).
const (
	gpPeer       = "source:celestrak" // OMM, MPE, CAT
	iqcPeer      = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	iqcSigmfPeer = "source:sigmf"
	iqcBatch     = "60cc968008101521f0f062a74bf83f9c7e72fcc3bcd8bc3ed84b97d12f4a29e3"
	fixturePPeer = FixtureProvider // normalizeSourceTags back-fills the producer peer with the provider
	r16At        = int64(1789371001)
)

// nonCanonicalCIDs are other texts of a stored CID and other CIDs of its
// digest: upper-case base32, base58btc and base36 of the same CIDv1, CIDv0 of
// its multihash, a dag-pb and a dag-cbor CIDv1 of it, a raw CIDv1 of another
// hash function, and the bare sha256 hex. None is the stored text.
func nonCanonicalCIDs(text string) []string {
	out := []string{strings.ToUpper(text)}
	c, err := cid.Decode(text)
	if err != nil {
		return out
	}
	if s, err := c.StringOfBase(multibase.Base58BTC); err == nil {
		out = append(out, s)
	}
	if s, err := c.StringOfBase(multibase.Base36); err == nil {
		out = append(out, s)
	}
	out = append(out, cid.NewCidV0(c.Hash()).String(), cid.NewCidV1(cid.DagProtobuf, c.Hash()).String(),
		cid.NewCidV1(cid.DagCBOR, c.Hash()).String())
	if mh, err := multihash.Sum([]byte(text), multihash.SHA2_512, -1); err == nil {
		out = append(out, cid.NewCidV1(cid.Raw, mh).String())
	}
	if dec, err := multihash.Decode(c.Hash()); err == nil {
		out = append(out, hex.EncodeToString(dec.Digest))
	}
	return out
}

// orderedDigest reduces a long ordered answer to one row per row: its CID
// (when it has one) and a digest of every other field but the copy variants
// (C-12), in order, so a difference names its row (and C-10's collapse still
// applies per CID).
func orderedDigest(rows []Row) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, digestRow(r))
	}
	return out
}

func digestRow(r Row) Row {
	h := sha256.Sum256([]byte(strictText(r)))
	d := Row{}
	if id := r.Get("cid"); id != "" {
		d = append(d, Field{"cid", id})
	}
	return append(d, Field{"h", hex.EncodeToString(h[:12])})
}

// unorderedDigest is orderedDigest for an answer with no defined order: the
// row digests sorted.
func unorderedDigest(rows []Row) []Row {
	ds := make([]Row, 0, len(rows))
	for _, r := range rows {
		ds = append(ds, digestRow(r))
	}
	return sortRows(ds)
}

func recordRows(recs []*storage.Record) []Row {
	rows := make([]Row, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, covRecordRow(r))
	}
	return rows
}

func bytesRows(list [][]byte) []Row {
	rows := make([]Row, 0, len(list))
	for _, b := range list {
		rows = append(rows, frameRow(b))
	}
	return rows
}

func u32(v uint32) *uint32 { return &v }

func tptr(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

// readCoverage builds the V classes.
func (c *cov) readCoverage() []Shape {
	var out []Shape
	out = append(out, c.v01()...)
	out = append(out, c.v02()...)
	out = append(out, c.v03()...)
	out = append(out, c.v04()...)
	out = append(out, c.v05()...)
	out = append(out, c.v06()...)
	out = append(out, c.v07()...)
	out = append(out, c.v08()...)
	out = append(out, c.v09()...)
	return out
}

// V09: full-text search. Format 1 builds a type's search index on first
// demand (CheckFullTextSearch; WarmFullTextIndexes for every type with
// records) and answers "building" until it is ready; format 4's settled
// fixture has it built. The class starts CAT's index, waits until it is
// ready (the state is then the same on every arm), and compares the
// searches: by cursor and newest first, with a sync filter and max_rowid,
// counts and heads, datasync, a malformed expression. Other types' states
// depend on how far format 1's background builds got, so only CAT's, a type
// without records and an unknown type's are compared.
func (c *cov) v09() []Shape {
	const class = "V09"
	waitReady := c.valueCall("CAT index ready", "CAT.fbs", func(s *storage.FlatSQLStore) (Row, error) {
		_ = s.CheckFullTextSearch("CAT.fbs", "warm")
		deadline := time.Now().Add(45 * time.Minute)
		for {
			st := s.FullTextIndexState("CAT.fbs")
			if st == "ready" || st == "failed" || time.Now().After(deadline) {
				return ValueRow("state", st), nil
			}
			time.Sleep(2 * time.Second)
		}
	})
	state := func(schema string) Call {
		return c.valueCall("FullTextIndexState "+schema, schema, func(s *storage.FlatSQLStore) (Row, error) {
			return ValueRow("state", s.FullTextIndexState(schema)), nil
		})
	}
	warm := c.valueCall("WarmFullTextIndexes", "", func(s *storage.FlatSQLStore) (Row, error) {
		sch, skip, err := s.WarmFullTextIndexes()
		sort.Strings(sch)
		sort.Strings(skip)
		return ValueRow("scheduled", strings.Join(sch, ","), "skipped", strings.Join(skip, ",")), err
	})
	search := func(name string, q storage.RawRecordQuery, hydrate bool) Call {
		return c.rawQuery("search "+name, q, hydrate)
	}
	q := func(search string, limit int, cursor bool) storage.RawRecordQuery {
		return storage.RawRecordQuery{SchemaName: "CAT.fbs", Search: search, Limit: limit, UseRowIDCursor: cursor}
	}
	with := func(r storage.RawRecordQuery, mod func(*storage.RawRecordQuery)) storage.RawRecordQuery {
		mod(&r)
		return r
	}
	counts := func(name string, r storage.RawRecordQuery) Call {
		return c.valueCall("search snapshot "+name, "CAT.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			n, h, err := s.RawRecordSnapshot(r)
			return covHead(r, n, &h), err
		})
	}
	scan := c.rowsCall("search Scan 60815 2 pages", "CAT.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		req := datasync.QueryRequest{Schema: "CAT.fbs", Search: "60815", Limit: 50}
		var rows []Row
		for p := 0; p < 2; p++ {
			resp, recs, err := datasync.Scan(s, req, datasync.MaxSyncChunkLimit)
			if err != nil {
				return nil, err
			}
			a, _ := scanAnswer("", resp, recs)
			rows = append(rows, a.Rows...)
			if resp.NextCursor == "" {
				break
			}
			req.Cursor = resp.NextCursor
		}
		return rows, nil
	})
	calls := []Call{
		waitReady,
		state("CAT.fbs"), state("PNM.fbs"), state("XYZ.fbs"),
		c.valueCall("CheckFullTextSearch CAT", "CAT.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			return ValueRow("ok", "1"), s.CheckFullTextSearch("CAT.fbs", "60815")
		}),
		search("cursor 60815", q("60815", 20, true), false),
		search("cursor satcat page 2", with(q("satcat", 20, true), func(r *storage.RawRecordQuery) { r.AfterRowID = 3900000 }), false),
		search("cursor celestrak max_rowid", with(q("celestrak", 20, true), func(r *storage.RawRecordQuery) { r.MaxRowID = 3850000 }), true),
		search("newest lorem offset 5", with(q("lorem", 20, false), func(r *storage.RawRecordQuery) { r.Offset = 5 }), false),
		search("cursor 60815 with filter", with(q("60815", 20, true), func(r *storage.RawRecordQuery) { r.SyncFilter = "NORAD_CAT_ID = 60815" }), false),
		search("cursor no match", q("zzzznomatch", 20, true), false),
		search("cursor malformed", q("\"", 20, true), false),
		counts("60815", q("60815", 0, false)),
		counts("lorem", q("lorem", 0, false)),
		scan,
		warm,
	}
	return []Shape{covShape(class, "full-text search", "CAT.fbs", calls...)}
}

// V01: GetRecord and GetSourceTags over the CID and type axes R01/R02 leave
// out (error parity included).
func (c *cov) v01() []Shape {
	const class = "V01"
	omm := c.hits["OMM.fbs"]
	if len(omm) == 0 {
		return nil
	}
	hit := omm[0]
	get := func(schema, id string) Call {
		return c.valueCall("GetRecord "+schema+" "+id, schema, func(s *storage.FlatSQLStore) (Row, error) {
			r, err := s.GetRecord(schema, id)
			if err != nil {
				return nil, err
			}
			return getRow(r), nil
		})
	}
	tags := func(schema, id string) Call {
		return c.valueCall("GetSourceTags "+schema+" "+id, schema, func(s *storage.FlatSQLStore) (Row, error) {
			t, err := s.GetSourceTags(schema, id)
			if err != nil {
				return nil, err
			}
			return TagsRow(id, t), nil
		})
	}
	var gets, tagCalls []Call
	for _, id := range nonCanonicalCIDs(hit) {
		gets = append(gets, get("OMM.fbs", id))
		tagCalls = append(tagCalls, tags("OMM.fbs", id))
	}
	// Cross-type, a type with no records, an unknown type, an empty CID.
	for _, p := range [][2]string{{"MPE.fbs", hit}, {"PNM.fbs", hit}, {"XYZ.fbs", hit}, {"OMM.fbs", ""}} {
		gets = append(gets, get(p[0], p[1]))
		tagCalls = append(tagCalls, tags(p[0], p[1]))
	}
	// IQC: a CID held by both producers (C-12).
	for _, id := range c.hits["IQC.fbs"][:4] {
		gets = append(gets, get("IQC.fbs", id))
	}
	// Tag misses (R02's), the error parity R04 does not check.
	for _, schema := range pointSchemas {
		if miss := c.miss[schema]; len(miss) > 0 {
			tagCalls = append(tagCalls, tags(schema, miss[0]))
		}
	}
	return []Shape{covShape(class, "GetRecord CID and type axes", "OMM.fbs", gets...),
		covShape(class, "GetSourceTags CID and type axes", "OMM.fbs", tagCalls...)}
}

// V02: QueryRawRecordRefsByRefs: the copy a ref names, a ref's tag fields, a
// missing ref (error parity), a non-canonical CID, more than 1,024 unique
// CIDs (the GET/TAGS chunk), no refs, an unknown type.
func (c *cov) v02() []Shape {
	const class = "V02"
	refs := func(name, schema string, list []storage.RawRecordRef) Call {
		return c.recordsCall(name, schema, func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			return s.QueryRawRecordRefsByRefs(schema, list)
		})
	}
	iqc := c.hits["IQC.fbs"]
	// Every IQC record is held by both producers; format 1 serves the
	// 16Uiu2HAm1Lbv… copy (R01), so a ref naming the source:sigmf copy is
	// compared under c12Copy.
	var served, other []storage.RawRecordRef
	for _, id := range iqc[:4] {
		served = append(served, storage.RawRecordRef{CID: id, PeerID: iqcPeer}, storage.RawRecordRef{CID: id, PeerID: iqcPeer})
		other = append(other, storage.RawRecordRef{CID: id, PeerID: iqcSigmfPeer})
	}
	b052 := c.in.B052CIDs
	tagged := func(n int, mod func(*storage.RawRecordRef)) []storage.RawRecordRef {
		var out []storage.RawRecordRef
		for _, id := range b052[:n] {
			r := storage.RawRecordRef{CID: id, ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: OMMLatestBatch,
				ProducerPeerID: fixturePPeer}
			if mod != nil {
				mod(&r)
			}
			out = append(out, r)
		}
		return out
	}
	var big []storage.RawRecordRef
	for i := 0; i < 1100 && i < len(b052); i++ {
		big = append(big, storage.RawRecordRef{CID: b052[i]})
	}
	missing := []storage.RawRecordRef{{CID: c.hits["OMM.fbs"][0]}, {CID: c.miss["OMM.fbs"][0]}}
	noncanon := []storage.RawRecordRef{{CID: strings.ToUpper(c.hits["OMM.fbs"][0])}}
	bigCall := c.rowsCall("refs x1100 unique", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		recs, err := s.QueryRawRecordRefsByRefs("OMM.fbs", big)
		if err != nil {
			return nil, err
		}
		return orderedDigest(recordRows(recs)), nil
	})
	return []Shape{c12Shape(class, "QueryRawRecordRefsByRefs, the copy format 1 does not serve", "IQC.fbs",
		refs("refs IQC, the source:sigmf copies", "IQC.fbs", other)),
		covShape(class, "QueryRawRecordRefsByRefs axes", "OMM.fbs",
			refs("refs IQC, the copies format 1 serves (repeated)", "IQC.fbs", served),
			refs("refs IQC unknown peer", "IQC.fbs", []storage.RawRecordRef{{CID: iqc[0], PeerID: "16Uiu2HAmNoSuchPeer"}}),
			refs("refs OMM tag fields", "OMM.fbs", tagged(8, nil)),
			refs("refs OMM tag fields, other batch", "OMM.fbs", tagged(2, func(r *storage.RawRecordRef) { r.BatchID = "OMM-celestrak-gp-b000" })),
			refs("refs OMM producer public key", "OMM.fbs", tagged(2, func(r *storage.RawRecordRef) { r.ProducerPublicKey = "nope" })),
			refs("refs OMM provider only", "OMM.fbs", tagged(2, func(r *storage.RawRecordRef) { r.SourceName, r.BatchID, r.ProducerPeerID = "", "", "" })),
			refs("refs one missing", "OMM.fbs", missing),
			refs("refs non-canonical CID", "OMM.fbs", noncanon),
			bigCall,
			refs("refs none", "OMM.fbs", nil),
			refs("refs unknown type", "XYZ.fbs", missing[:1]),
			refs("refs empty CID", "OMM.fbs", []storage.RawRecordRef{{CID: ""}}),
		)}
}

// rawQuery is a QueryRawRecordRefs (refs=true) or QueryRawRecords (hydrated,
// opened) call. A page past 200 rows compares as a digest.
func (c *cov) rawQuery(name string, q storage.RawRecordQuery, hydrate bool) Call {
	return c.rowsCall(name, q.SchemaName, func(s *storage.FlatSQLStore) ([]Row, error) {
		var recs []*storage.Record
		var err error
		if hydrate {
			recs, err = s.QueryRawRecords(q)
		} else {
			recs, err = s.QueryRawRecordRefs(q)
		}
		if err != nil {
			return nil, err
		}
		rows := recordRows(recs)
		if len(rows) > 200 {
			return orderedDigest(rows), nil
		}
		return rows, nil
	})
}

// V03: queryRawRecords (QueryRawRecordRefs, QueryRawRecords, datasync.Scan):
// the three branches, every lane field, the copy's peer, the CID, every
// sync_filter field and operator, the clause errors, search, limit defaults
// and caps, offsets, max_rowid, an empty result.
func (c *cov) v03() []Shape {
	const class = "V03"
	cur := func(schema string, limit int) storage.RawRecordQuery {
		return storage.RawRecordQuery{SchemaName: schema, Limit: limit, UseRowIDCursor: true}
	}
	with := func(q storage.RawRecordQuery, mod func(*storage.RawRecordQuery)) storage.RawRecordQuery {
		mod(&q)
		return q
	}
	var branch, lanes, filters, errs []Call
	// Branches: default (newest first + offset), indexed off-cursor (arrival
	// order + offset), cursor; limit 0 (100) and above the cap (50,000).
	branch = append(branch,
		c.rawQuery("default OMM limit 20", storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 20}, false),
		c.rawQuery("default OMM limit 20 offset 1000", storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 20, Offset: 1000}, false),
		c.rawQuery("default IQC limit 10 hydrated", storage.RawRecordQuery{SchemaName: "IQC.fbs", Limit: 10}, true),
		c.rawQuery("default CAT max_rowid", storage.RawRecordQuery{SchemaName: "CAT.fbs", Limit: 10, MaxRowID: 3900000}, false),
		c.rawQuery("default OMM negative offset", storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 5, Offset: -3}, false),
		c.rawQuery("indexed OMM norad=25544 offset 5", storage.RawRecordQuery{SchemaName: "OMM.fbs", SyncFilter: "NORAD_CAT_ID = 25544", Limit: 10, Offset: 5}, false),
		c.rawQuery("indexed MPE entity hydrated", storage.RawRecordQuery{SchemaName: "MPE.fbs", SyncFilter: "ENTITY_ID = '22528 lorem-ipsum-dolor lorem-ipsum'", Limit: 10}, true),
		c.rawQuery("cursor OMM limit 0", cur("OMM.fbs", 0), false),
		c.rawQuery("cursor IQC limit 60000", cur("IQC.fbs", 60000), false),
		c.rawQuery("cursor OMM after max", with(cur("OMM.fbs", 10), func(q *storage.RawRecordQuery) { q.AfterRowID = 2115808 }), false),
		c.rawQuery("cursor MPE max_rowid below after", with(cur("MPE.fbs", 10), func(q *storage.RawRecordQuery) { q.AfterRowID = 2200000; q.MaxRowID = 2199999 }), false),
		c.rawQuery("cursor CAT hydrated", cur("CAT.fbs", 10), true),
	)
	// Lanes, the copy's peer, the CID. A filter on part of a lane (no batch)
	// runs on CAT: format 1 plans a partial lane over a 1.7-million-record
	// type as a sort of every match and does not answer within its engine's
	// 5-minute budget (the OMM provider-only page poisoned it).
	lanes = append(lanes,
		c.rawQuery("cursor CAT source only", with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.SourceName = "celestrak-satcat" }), false),
		c.rawQuery("cursor CAT batch only", with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.BatchID = "CAT-celestrak-satcat-csv-b000" }), false),
		c.rawQuery("cursor CAT producer peer", with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.ProducerPeerID = fixturePPeer }), false),
		c.rawQuery("cursor CAT producer public key", with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.ProducerPublicKey = "nope" }), false),
		c.rawQuery("cursor OMM full lane", with(cur("OMM.fbs", 20), func(q *storage.RawRecordQuery) {
			q.ProviderID, q.SourceName, q.BatchID, q.ProducerPeerID = FixtureProvider, "celestrak-gp", OMMLatestBatch, fixturePPeer
		}), false),
		c.rawQuery("cursor CAT no such batch", with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.BatchID = "no-such-batch" }), false),
		c.rawQuery("default IQC lane offset 100", storage.RawRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", BatchID: iqcBatch, Limit: 10, Offset: 100}, false),
		c.rawQuery("cursor IQC peer near the end", with(cur("IQC.fbs", 20), func(q *storage.RawRecordQuery) { q.PeerID, q.AfterRowID = iqcPeer, 418900 }), false),
		c.rawQuery("cursor IQC sigmf peer near the end", with(cur("IQC.fbs", 20), func(q *storage.RawRecordQuery) { q.PeerID, q.AfterRowID = iqcSigmfPeer, 418900 }), false),
		c.rawQuery("default OMM peer", storage.RawRecordQuery{SchemaName: "OMM.fbs", PeerID: gpPeer, Limit: 10}, false),
		c.rawQuery("cursor CAT unknown peer", with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.PeerID = iqcPeer }), false),
		c.rawQuery("cursor OMM cid", with(cur("OMM.fbs", 20), func(q *storage.RawRecordQuery) { q.CID = c.hits["OMM.fbs"][3] }), false),
		c.rawQuery("default IQC cid", storage.RawRecordQuery{SchemaName: "IQC.fbs", CID: c.hits["IQC.fbs"][3], Limit: 10}, false),
		c.rawQuery("cursor OMM non-canonical cid", with(cur("OMM.fbs", 20), func(q *storage.RawRecordQuery) { q.CID = strings.ToUpper(c.hits["OMM.fbs"][3]) }), false),
		c.rawQuery("cursor OMM miss cid", with(cur("OMM.fbs", 20), func(q *storage.RawRecordQuery) { q.CID = c.miss["OMM.fbs"][0] }), false),
	)
	// Every sync_filter field and operator (cursor pages of 20). A filter
	// most records meet runs on CAT (format 1 sorts every match: on OMM or
	// MPE that runs past its engine budget); OMM and MPE take selective ones.
	for _, f := range []struct{ schema, filter string }{
		{"OMM.fbs", "EPOCH < '2026-09-01T03:00:00Z'"},
		{"OMM.fbs", "EPOCH >= '2026-09-27T18:00:00Z'"},
		{"OMM.fbs", "EPOCH_UNIX > 1790540000"},
		{"OMM.fbs", "EPOCH_UNIX <= 1788231600"},
		{"OMM.fbs", "EPOCH_UNIX BETWEEN 1789371001 AND 1789374601"},
		{"CAT.fbs", "SOURCE_TIMESTAMP >= 1790651400"},
		{"CAT.fbs", "SOURCE_TIMESTAMP < 1790651400"},
		{"CAT.fbs", "EPOCH_DAY != '2026-09-14'"},
		{"CAT.fbs", "EPOCH_DAY <> '2026-09-01'"},
		{"OMM.fbs", "EPOCH_DAY LIKE '2026-09-14'"},
		{"OMM.fbs", "EPOCH_DAY > '2026-09-27'"},
		{"OMM.fbs", "NORAD_CAT_ID > 32000"},
		{"OMM.fbs", "NORAD_CAT_ID <= 3"},
		{"OMM.fbs", "NORAD_CAT_ID >= 32014"},
		{"CAT.fbs", "NORAD_CAT_ID != 1"},
		{"CAT.fbs", "NORAD_CAT_ID <> 1"},
		{"OMM.fbs", "idx.NORAD_CAT_ID = 25544"},
		{"OMM.fbs", "norad_cat_id = '25544'"},
		{"OMM.fbs", "OBJECT_ID LIKE '2026-2554%'"},
		{"OMM.fbs", "OBJECT_ID = '2026-25544A'"},
		{"CAT.fbs", "OBJECT_ID != '1990-40463A'"},
		{"OMM.fbs", "NORAD_CAT_ID >= 25000 AND NORAD_CAT_ID <= 25010 AND EPOCH_DAY = '2026-09-14'"},
		{"OMM.fbs", "EPOCH BETWEEN '2026-09-14T00:00:00Z' AND '2026-09-14T01:00:00Z' AND NORAD_CAT_ID < 50"},
		{"MPE.fbs", "ENTITY_ID LIKE '2252%'"},
		{"CAT.fbs", "ENTITY_ID != 'x'"},
		{"MPE.fbs", "EPOCH_DAY = '2026-09-20'"},
		{"IQC.fbs", "FILE_ID = 'x'"},
		{"IQC.fbs", "EPOCH_UNIX < 1000000000"},
		{"CAT.fbs", "OBJECT_TYPE = 'PAYLOAD'"},
		{"CAT.fbs", "OBJECT_TYPE != 'DEBRIS'"},
		{"CAT.fbs", "OBJECT_TYPE LIKE 'PAY%'"},
		{"CAT.fbs", "OPS_STATUS_CODE = '+'"},
		{"CAT.fbs", "OBJECT_ID LIKE '1990-4046%'"},
		{"CAT.fbs", "NORAD_CAT_ID = 40463"},
	} {
		filters = append(filters, c.rawQuery("cursor "+f.schema+" "+f.filter, with(cur(f.schema, 20), func(q *storage.RawRecordQuery) {
			q.SyncFilter = f.filter
		}), false))
	}
	// Clause errors.
	for _, bad := range []string{"FOO = 1", "NORAD_CAT_ID ~ 5", "NORAD_CAT_ID = 'abc'", "ENTITY_ID BETWEEN 'a' AND 'b'",
		"NORAD_CAT_ID LIKE '25%'", "ENTITY_ID > 'a'", "EPOCH = 'not-a-time'", "OBJECT_TYPE = ''", "AND", "EPOCH_DAY = '2026-13-45'"} {
		errs = append(errs, c.rawQuery("cursor OMM bad filter "+bad, with(cur("OMM.fbs", 5), func(q *storage.RawRecordQuery) { q.SyncFilter = bad }), false))
	}
	// An unknown type, no type, a type with no records (search: V09).
	errs = append(errs,
		c.rawQuery("cursor unknown type", cur("XYZ.fbs", 5), false),
		c.rawQuery("cursor no type", cur("", 5), false),
		c.rawQuery("cursor PNM (no records)", cur("PNM.fbs", 5), false),
	)
	// datasync.Scan: a lane page chained by its cursor, a sync filter page.
	scan := func(name string, req datasync.QueryRequest, pages int) Call {
		return c.rowsCall(name, req.Schema, func(s *storage.FlatSQLStore) ([]Row, error) {
			var rows []Row
			r := req
			for p := 0; p < pages; p++ {
				resp, recs, err := datasync.Scan(s, r, datasync.MaxSyncChunkLimit)
				if err != nil {
					return nil, err
				}
				a, _ := scanAnswer(name, resp, recs)
				rows = append(rows, a.Rows...)
				if resp.NextCursor == "" {
					break
				}
				r.Cursor = resp.NextCursor
			}
			return rows, nil
		})
	}
	scans := []Call{
		scan("Scan OMM b052 lane 2 pages", datasync.QueryRequest{Schema: "OMM.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: OMMLatestBatch, Limit: 50}, 2),
		scan("Scan IQC peer", datasync.QueryRequest{Schema: "IQC.fbs", PeerID: iqcSigmfPeer, Limit: 20}, 1),
		scan("Scan CAT filter", datasync.QueryRequest{Schema: "CAT.fbs", SyncFilter: "NORAD_CAT_ID < 100", Limit: 20}, 2),
		scan("Scan MPE offset", datasync.QueryRequest{Schema: "MPE.fbs", Limit: 20, Offset: 40}, 1),
	}
	// A cursor page by provider alone: format 1 does not answer it within
	// its engine's budget on any fixture type (it poisoned the engine on OMM
	// and on CAT), so its baseline is format 1 answering the same question
	// (as R17/R18's): the provider's CAT records are those of its two CAT
	// sources, whose provider+source pages it answers; their first 20 by
	// cursor, merged. Every other arm calls the provider alone.
	provQ := with(cur("CAT.fbs", 20), func(q *storage.RawRecordQuery) { q.ProviderID = FixtureProvider })
	prov := covShape(class, "raw records: provider only", "CAT.fbs", c.rawQuery("cursor CAT provider only", provQ, false))
	prov.Arms = []string{ArmS, ArmF2}
	provF1 := covShape(class, "raw records: provider only", "CAT.fbs", c.rowsCall("cursor CAT provider only", "CAT.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		var recs []*storage.Record
		for _, src := range []string{"celestrak-satcat", "celestrak-satcat-csv"} {
			q := provQ
			q.SourceName = src
			got, err := s.QueryRawRecordRefs(q)
			if err != nil {
				return nil, err
			}
			recs = append(recs, got...)
		}
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].RowID < recs[j].RowID })
		if len(recs) > provQ.Limit {
			recs = recs[:provQ.Limit]
		}
		return recordRows(recs), nil
	}))
	provF1.Arms = []string{ArmF1}
	return []Shape{prov, provF1, covShape(class, "raw records: branches", "OMM.fbs", branch...),
		rule(covShape(class, "raw records: lanes, peer, cid", "OMM.fbs", lanes...), "cursor IQC sigmf peer", c12PeerFilter),
		covShape(class, "raw records: sync_filter fields and operators", "OMM.fbs", filters...),
		covShape(class, "raw records: errors and search", "OMM.fbs", errs...),
		rule(covShape(class, "datasync.Scan lanes and cursors", "OMM.fbs", scans...), "Scan IQC peer", c12PeerFilter)}
}

// V04: QuerySourceTaggedRecords, QueryRecentRecords, FullTablePageWithCursor,
// Query, QueryAll, QueryAllBounded and the routed listings.
func (c *cov) v04() []Shape {
	const class = "V04"
	tagged := func(q storage.SourceTagQuery) Call {
		name := fmt.Sprintf("QuerySourceTaggedRecords %s %s/%s/%s limit=%d", q.SchemaName, q.ProviderID, q.SourceName, q.BatchID, q.Limit)
		return c.recordsCall(name, q.SchemaName, func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			return s.QuerySourceTaggedRecords(q)
		})
	}
	recent := func(schema string, limit int) Call {
		return c.rowsCall(fmt.Sprintf("QueryRecentRecords %s limit=%d", schema, limit), schema, func(s *storage.FlatSQLStore) ([]Row, error) {
			recs, err := s.QueryRecentRecords(schema, limit)
			if err != nil {
				return nil, err
			}
			rows := recordRows(recs)
			if len(rows) > 200 {
				return orderedDigest(rows), nil
			}
			return rows, nil
		})
	}
	// Full-table pages: the first page, then the page its cursor names.
	table := func(name string, q storage.FullTablePageQuery, pages int) Call {
		return c.rowsCall(name, q.SchemaName, func(s *storage.FlatSQLStore) ([]Row, error) {
			var rows []Row
			for p := 0; p < pages; p++ {
				page, err := s.FullTablePageWithCursor(q)
				if err != nil {
					return nil, err
				}
				rows = append(rows, ValueRow("page", strconv.Itoa(p), "n", strconv.Itoa(len(page.Records))))
				rows = append(rows, recordRows(page.Records)...)
				if len(page.Records) == 0 {
					break
				}
				q.Cursor = page.NextCursor
			}
			return rows, nil
		})
	}
	data := func(name, schema string, ordered bool, fn func(s *storage.FlatSQLStore) ([][]byte, error)) Call {
		return c.rowsCall(name, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
			list, err := fn(s)
			if err != nil {
				return nil, err
			}
			if ordered {
				return orderedDigest(bytesRows(list)), nil
			}
			return unorderedDigest(bytesRows(list)), nil
		})
	}
	routed := func(name string, fn func(s *storage.FlatSQLStore) ([]storage.RoutedRecord, error)) Call {
		return c.rowsCall(name, "", func(s *storage.FlatSQLStore) ([]Row, error) {
			list, err := fn(s)
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(list))
			for _, r := range list {
				rows = append(rows, ValueRow("cid", r.CID, "producer", r.ProducerID, "standard", r.Standard, "peer", r.PeerID, "ts", i64(r.Timestamp)))
			}
			return rows, nil
		})
	}
	// Format 1's limits: 0 is 100, at most 1000 (QuerySourceTaggedRecords);
	// the routed listings take theirs as given.
	taggedShape := covShape(class, "QuerySourceTaggedRecords", "OMM.fbs",
		tagged(storage.SourceTagQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: OMMLatestBatch, Limit: 20}),
		tagged(storage.SourceTagQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", Limit: 10}),
		tagged(storage.SourceTagQuery{SchemaName: "CAT.fbs", ProviderID: FixtureProvider, Limit: 0}),
		tagged(storage.SourceTagQuery{SchemaName: "MPE.fbs", BatchID: MPELatestBatch, Limit: 5000}),
		tagged(storage.SourceTagQuery{SchemaName: "OMM.fbs", BatchID: "no-such-batch", Limit: 10}),
		tagged(storage.SourceTagQuery{SchemaName: "XYZ.fbs", Limit: 10}))
	for _, l := range []struct {
		call, schema string
		limit        int
	}{{"OMM.fbs space-data-network-02/celestrak-gp/" + OMMLatestBatch + " limit=20", "OMM.fbs", 20},
		{"IQC.fbs /IQEngine/ limit=10", "IQC.fbs", 10}, {"CAT.fbs space-data-network-02// limit=0", "CAT.fbs", 100},
		{"MPE.fbs //" + MPELatestBatch + " limit=5000", "MPE.fbs", 1000}} {
		taggedShape = tieLimit(taggedShape, l.call, "~ts", l.limit, l.schema)
	}
	routedShape := covShape(class, "routed listings", "",
		routed("QueryRoutedByStandard IQC 20", func(s *storage.FlatSQLStore) ([]storage.RoutedRecord, error) {
			return s.QueryRoutedByStandard("IQC.fbs", 20)
		}),
		routed("QueryRoutedByProducer source:sigmf 20", func(s *storage.FlatSQLStore) ([]storage.RoutedRecord, error) {
			return s.QueryRoutedByProducer(iqcSigmfPeer, 20)
		}),
		routed("QueryRoutedAll 20", func(s *storage.FlatSQLStore) ([]storage.RoutedRecord, error) { return s.QueryRoutedAll(20) }))
	routedShape = tieLimit(routedShape, "QueryRouted", "ts", 20, "")
	tables := covShape(class, "FullTablePageWithCursor", "OMM.fbs",
		table("table MPE 2 pages", storage.FullTablePageQuery{SchemaName: "MPE.fbs", Limit: 25}, 2),
		table("table CAT source 2 pages", storage.FullTablePageQuery{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat", Limit: 25}, 2),
		table("table IQC offset", storage.FullTablePageQuery{SchemaName: "IQC.fbs", Limit: 10, Offset: 30}, 1),
		table("table OMM before rowid", storage.FullTablePageQuery{SchemaName: "OMM.fbs", Limit: 10, BeforeRowID: 1000000}, 1),
		table("table OMM include source", storage.FullTablePageQuery{SchemaName: "OMM.fbs", Limit: 10, IncludeSource: true}, 1),
		table("table CAT ascending", storage.FullTablePageQuery{SchemaName: "CAT.fbs", Limit: 10, Descending: false, Sort: "rowid"}, 2),
		table("table OMM limit 0", storage.FullTablePageQuery{SchemaName: "OMM.fbs"}, 1),
		table("table PNM (no records)", storage.FullTablePageQuery{SchemaName: "PNM.fbs", Limit: 10}, 1),
		table("table unknown type", storage.FullTablePageQuery{SchemaName: "XYZ.fbs", Limit: 10}, 1))
	// C-39 U4: the page before a cursor is the records below that type seq
	// (format 1: below that producer table's rowid), so its records differ;
	// the page header (its row count) does not.
	tables = rule(tables, "table OMM before rowid", c39U4, recordFields()...)
	queries := covShape(class, "Query, QueryAll, QueryAllBounded", "CAT.fbs",
		data("Query CAT (empty where)", "CAT.fbs", false, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.Query("CAT.fbs", "") }),
		data("QueryAll MPE 500", "MPE.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAll("MPE.fbs", 500) }),
		data("QueryAll OMM 0", "OMM.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAll("OMM.fbs", 0) }),
		data("QueryAll IQC 20000", "IQC.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAll("IQC.fbs", 20000) }),
		data("QueryAllBounded OMM 100 64KiB", "OMM.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAllBounded("OMM.fbs", 100, 64<<10) }),
		data("QueryAllBounded CAT 0 0", "CAT.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAllBounded("CAT.fbs", 0, 0) }),
		data("QueryAllBounded IQC 5000 1MiB", "IQC.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAllBounded("IQC.fbs", 5000, 1<<20) }),
		data("QueryWithPeerID CAT source:celestrak", "CAT.fbs", false, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryWithPeerID("CAT.fbs", gpPeer) }),
		data("QuerySince CAT", "CAT.fbs", false, func(s *storage.FlatSQLStore) ([][]byte, error) {
			return s.QuerySince("CAT.fbs", time.Unix(1790651000, 0))
		}),
		data("QueryAll unknown type", "XYZ.fbs", true, func(s *storage.FlatSQLStore) ([][]byte, error) { return s.QueryAll("XYZ.fbs", 10) }))
	// C-39 E7: every fixture IQC record carries the same source timestamp,
	// so format 1's newest N by it is an arbitrary N of them; the row count
	// is compared.
	queries = rule(queries, "QueryAll IQC 20000", c39E7, "h")
	queries = rule(queries, "QueryAllBounded IQC 5000 1MiB", c39E7, "h")
	return []Shape{
		taggedShape,
		covShape(class, "QueryRecentRecords", "OMM.fbs",
			recent("MPE.fbs", 20), recent("CAT.fbs", 20), recent("IQC.fbs", 0), recent("OMM.fbs", -1), recent("CAT.fbs", 300000),
			recent("PNM.fbs", 10), recent("XYZ.fbs", 10)),
		tables,
		queries,
		routedShape,
	}
}

// V05: windows, byte probes, publication fingerprints, index pages, counts
// and heads, and the export (exportSourceTags) over the axes R10–R15 leave
// out.
func (c *cov) v05() []Shape {
	const class = "V05"
	win := func(q storage.IndexedRecordQuery) Call {
		name := "QueryIndexedRecords " + windowName(q)
		if q.ObjectType != "" {
			name += " type=" + q.ObjectType
		}
		if q.OpsStatusCode != "" {
			name += " ops=" + q.OpsStatusCode
		}
		if q.ActivePayloads {
			name += " active"
		}
		if q.CAReadyResidentSet {
			name += " ca-ready"
		}
		if q.AllowLargeResultSet {
			name += " large"
		}
		return c.rowsCall(name, q.SchemaName, func(s *storage.FlatSQLStore) ([]Row, error) {
			recs, err := s.QueryIndexedRecords(q)
			if err != nil {
				return nil, err
			}
			rows := recordRows(recs)
			if len(rows) > 200 {
				return orderedDigest(rows), nil
			}
			return rows, nil
		})
	}
	probe := func(q storage.IndexedRecordQuery, max int64) Call {
		return c.valueCall(fmt.Sprintf("IndexedRecordWindowLimitForBytes %s max=%d", windowName(q), max), q.SchemaName, func(s *storage.FlatSQLStore) (Row, error) {
			n, more, err := s.IndexedRecordWindowLimitForBytes(q, max)
			return ValueRow("n", strconv.Itoa(n), "more", strconv.FormatBool(more)), err
		})
	}
	fp := func(schema, provider, source, batch string) Call {
		return c.valueCall(fmt.Sprintf("DatasetPublicationSetFingerprint %s %s/%s/%s", schema, provider, source, batch), schema, func(s *storage.FlatSQLStore) (Row, error) {
			f, n, err := s.DatasetPublicationSetFingerprint(schema, provider, source, batch)
			return ValueRow("fingerprint", f, "n", strconv.Itoa(n)), err
		})
	}
	page := func(q storage.RecordIndexPageQuery) Call {
		name := fmt.Sprintf("RecordIndexPage %s %s/%s/%s norad=%s lim=%d off=%d", q.SchemaName, q.ProviderID, q.SourceName, q.BatchID, q.NoradLike, q.Limit, q.Offset)
		return c.rowsCall(name, q.SchemaName, func(s *storage.FlatSQLStore) ([]Row, error) {
			rows, total, err := s.RecordIndexPage(q)
			if err != nil {
				return nil, err
			}
			out := []Row{ValueRow("total", i64(total))}
			for _, r := range rows {
				out = append(out, ValueRow("cid", r.CID, "norad", i64p(r.NoradCatID), "epoch", i64p(r.EpochUnix)))
			}
			return out, nil
		})
	}
	heads := func(name string, q storage.RawRecordQuery) []Call {
		return []Call{
			c.valueCall("CountRawRecords "+name, q.SchemaName, func(s *storage.FlatSQLStore) (Row, error) {
				n, err := s.CountRawRecords(q)
				return covHead(q, n, nil), err
			}),
			c.valueCall("RawRecordHead "+name, q.SchemaName, func(s *storage.FlatSQLStore) (Row, error) {
				h, err := s.RawRecordHead(q)
				return covHead(q, -1, &h), err
			}),
			c.valueCall("RawRecordSnapshot "+name, q.SchemaName, func(s *storage.FlatSQLStore) (Row, error) {
				n, h, err := s.RawRecordSnapshot(q)
				return covHead(q, n, &h), err
			}),
		}
	}
	count := func(schema string) []Call {
		return []Call{
			c.valueCall("Count "+schema, schema, func(s *storage.FlatSQLStore) (Row, error) {
				n, err := s.Count(schema)
				return ValueRow("n", i64(n)), err
			}),
		}
	}
	export := func(name string, q storage.IndexedRecordQuery) Call {
		return c.rowsCall("ExportDatasetWindow "+name, q.SchemaName, func(s *storage.FlatSQLStore) ([]Row, error) {
			dir := filepath.Join(scratchDir, "export-"+safeName(name))
			_ = os.RemoveAll(dir)
			exp, err := s.ExportDatasetWindow(dir, q)
			if err != nil {
				return nil, err
			}
			return exportRows(exp)
		})
	}
	var wins, heads2 []Call
	wins = append(wins,
		win(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", ObjectType: "PAYLOAD", Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", OpsStatusCode: "+", Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", ActivePayloads: true, Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", CAReadyResidentSet: true, Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Day: "2026-09-14", NoradCatID: u32(25544), Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", From: tptr("2026-09-14T00:00:00Z"), Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", To: tptr("2026-09-01T03:00:00Z"), Limit: 20, Offset: 10}),
		win(storage.IndexedRecordQuery{SchemaName: "MPE.fbs", EntityID: "22528 lorem-ipsum-dolor lorem-ipsum", From: tptr("2026-09-10T00:00:00Z"), To: tptr("2026-09-20T00:00:00Z"), Limit: 20}),
		win(storage.IndexedRecordQuery{SchemaName: "IQC.fbs", ProviderID: FixtureProvider, Limit: 10, Offset: 5}),
		win(storage.IndexedRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", BatchID: iqcBatch, OrderByCID: true, Limit: 10, Offset: 400000}),
		win(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat-csv", NoradCatID: u32(40463), Limit: 10}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 5000}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 1500, AllowLargeResultSet: true}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 0}),
		win(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", BatchID: "no-such-batch", Limit: 10}),
		win(storage.IndexedRecordQuery{SchemaName: "PNM.fbs", Limit: 10}),
		win(storage.IndexedRecordQuery{SchemaName: "XYZ.fbs", Limit: 10}),
		probe(storage.IndexedRecordQuery{SchemaName: "OMM.fbs"}, 1<<20),
		probe(storage.IndexedRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine"}, 100),
		probe(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", Day: "2026-09-14"}, 1<<16),
		probe(storage.IndexedRecordQuery{SchemaName: "MPE.fbs", SourceName: "celestrak-gp", BatchID: "no-such-batch"}, 1<<20),
		probe(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: OMMLatestBatch, OrderByCID: true, Offset: 31000}, 1<<20),
		fp("OMM.fbs", FixtureProvider, "celestrak-gp", OMMLatestBatch),
		fp("IQC.fbs", FixtureProvider, "IQEngine", iqcBatch),
		fp("CAT.fbs", FixtureProvider, "celestrak-satcat", ""),
		fp("MPE.fbs", FixtureProvider, "celestrak-gp", "no-such-batch"),
		export("CAT satcat-csv by cid 100", storage.IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-satcat-csv", Limit: 100, OrderByCID: true}),
		export("IQC lane 50", storage.IndexedRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", BatchID: iqcBatch, Limit: 50}),
		export("OMM day no lane 100", storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Day: "2026-09-14", Limit: 100}),
		export("MPE none", storage.IndexedRecordQuery{SchemaName: "MPE.fbs", BatchID: "no-such-batch", Limit: 10}),
	)
	heads2 = append(heads2, heads("OMM provider only", storage.RawRecordQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider})...)
	heads2 = append(heads2, heads("IQC peer", storage.RawRecordQuery{SchemaName: "IQC.fbs", PeerID: iqcSigmfPeer})...)
	heads2 = append(heads2, heads("OMM cid", storage.RawRecordQuery{SchemaName: "OMM.fbs", CID: c.hits["OMM.fbs"][5]})...)
	heads2 = append(heads2, heads("MPE cursor", storage.RawRecordQuery{SchemaName: "MPE.fbs", UseRowIDCursor: true, AfterRowID: 3000000, MaxRowID: 3500000})...)
	heads2 = append(heads2, heads("CAT filter", storage.RawRecordQuery{SchemaName: "CAT.fbs", SyncFilter: "NORAD_CAT_ID BETWEEN 40000 AND 40100"})...)
	heads2 = append(heads2, heads("CAT batch", storage.RawRecordQuery{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat-csv", BatchID: "CAT-celestrak-satcat-csv-b000"})...)
	heads2 = append(heads2, heads("IQC producer peer", storage.RawRecordQuery{SchemaName: "IQC.fbs", ProducerPeerID: fixturePPeer})...)
	heads2 = append(heads2, heads("OMM no such batch", storage.RawRecordQuery{SchemaName: "OMM.fbs", BatchID: "no-such-batch"})...)
	heads2 = append(heads2, heads("PNM (no records)", storage.RawRecordQuery{SchemaName: "PNM.fbs"})...)
	heads2 = append(heads2, heads("unknown type", storage.RawRecordQuery{SchemaName: "XYZ.fbs"})...)
	heads2 = append(heads2, heads("OMM bad filter", storage.RawRecordQuery{SchemaName: "OMM.fbs", SyncFilter: "FOO = 1"})...)
	heads2 = append(heads2, count("PNM.fbs")...)
	heads2 = append(heads2, count("XYZ.fbs")...)
	heads2 = append(heads2, c.valueCall("EngineRecordCount PNM.fbs", "PNM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
		n, err := s.EngineRecordCount("PNM.fbs")
		return ValueRow("n", i64(n)), err
	}))
	pages := []Call{
		page(storage.RecordIndexPageQuery{SchemaName: "OMM.fbs", NoradLike: "2554", Limit: 20}),
		page(storage.RecordIndexPageQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: OMMLatestBatch, Limit: 20, Offset: 31990}),
		page(storage.RecordIndexPageQuery{SchemaName: "MPE.fbs", BatchID: MPELatestBatch, Limit: 20}),
		page(storage.RecordIndexPageQuery{SchemaName: "IQC.fbs", ProviderID: FixtureProvider, Limit: 20, Offset: 419020}),
		page(storage.RecordIndexPageQuery{SchemaName: "CAT.fbs", Limit: 0}),
		page(storage.RecordIndexPageQuery{SchemaName: "CAT.fbs", Limit: 20, Offset: 200000}),
		page(storage.RecordIndexPageQuery{SchemaName: "OMM.fbs", NoradLike: "%", Limit: 5}),
		page(storage.RecordIndexPageQuery{SchemaName: "PNM.fbs", Limit: 5}),
		page(storage.RecordIndexPageQuery{SchemaName: "XYZ.fbs", Limit: 5}),
	}
	return []Shape{covShape(class, "windows, probes, fingerprints, exports", "OMM.fbs", wins...),
		covShape(class, "counts and heads", "OMM.fbs", heads2...),
		covShape(class, "record index pages", "OMM.fbs", pages...)}
}

// exportRows canonicalizes an export: its counts and hashes, the index
// records with their tags (exportSourceTags), and the shard bytes' digest.
func exportRows(exp *storage.DatasetExport) ([]Row, error) {
	b, err := os.ReadFile(exp.IndexPath)
	if err != nil {
		return nil, err
	}
	var idx storage.DatasetExportIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	rows := []Row{ValueRow("n", strconv.Itoa(exp.RecordCount), "query", exp.QuerySHA256, "result", exp.ResultSHA256, "shard", exp.ShardSHA256,
		"shard_cid", exp.ShardCID, "shard_bytes", i64(exp.ShardBytes), "index", exp.IndexSHA256, "ckey", exp.ContentKeyID, "policy", exp.EncryptionPolicy)}
	for _, sb := range exp.SourceBatches {
		bb, _ := json.Marshal(sb)
		rows = append(rows, append(ValueRow("source_batch", ""), flatJSON(jsonObject(bb))...))
	}
	for _, r := range idx.Records {
		rows = append(rows, append(TagsRow(r.CID, r.SourceTags), Field{"offset", i64(r.Offset)}, Field{"length", i64(r.Length)},
			Field{"norad", func() string {
				if r.NoradCatID == nil {
					return ""
				}
				return strconv.FormatUint(uint64(*r.NoradCatID), 10)
			}()}, Field{"entity", r.EntityID}, Field{"epoch_day", r.EpochDay}))
	}
	return rows, nil
}

// V06: the epoch profiles over the axes R16/R18 leave out.
func (c *cov) v06() []Shape {
	const class = "V06"
	q := func(p storage.EpochRecordQuery) Call {
		name := fmt.Sprintf("epoch %s %s", p.SchemaName, p.Profile)
		for _, kv := range [][2]string{{"day", p.Day}, {"source", p.SourceName}, {"provider", p.ProviderID}, {"batch", p.BatchID}, {"entity", p.EntityID}} {
			if kv[1] != "" {
				name += " " + kv[0] + "=" + kv[1]
			}
		}
		if !p.At.IsZero() {
			name += " at=" + i64(p.At.Unix())
		}
		if p.From != nil {
			name += " from=" + p.From.UTC().Format(time.RFC3339)
		}
		if p.To != nil {
			name += " to=" + p.To.UTC().Format(time.RFC3339)
		}
		if p.NoradCatID != nil {
			name += fmt.Sprintf(" norad=%d", *p.NoradCatID)
		}
		if p.MaxDeltaSeconds != 0 {
			name += " max_delta=" + i64(p.MaxDeltaSeconds)
		}
		if p.IncludeSource {
			name += " include_source"
		}
		name += " limit=" + strconv.Itoa(p.Limit)
		return c.rowsCall(name, p.SchemaName, func(s *storage.FlatSQLStore) ([]Row, error) {
			if p.Profile == storage.EpochProfileCoverage {
				bs, err := s.QueryEpochCoverage(p)
				if err != nil {
					return nil, err
				}
				rows := make([]Row, 0, len(bs))
				for _, b := range bs {
					rows = append(rows, ValueRow("day", b.Day, "n", i64(b.Count), "oldest", unixOf(b.OldestEpoch), "newest", unixOf(b.NewestEpoch)))
				}
				return rows, nil
			}
			n, err := s.CountEpochRecords(p)
			if err != nil {
				return nil, err
			}
			ms, err := s.QueryEpochRecords(p)
			if err != nil {
				return nil, err
			}
			a, _ := matchesAnswer(name, ms, n)
			if len(a.Rows) > 200 {
				return append(a.Rows[:1], orderedDigest(a.Rows[1:])...), nil
			}
			return a.Rows, nil
		})
	}
	at := time.Unix(r16At, 0).UTC()
	stream := func(schema, source, profile string, epoch float64, limit int) Call {
		name := fmt.Sprintf("QueryEpochRawStream %s@%s %s %.0f limit=%d", schema, source, profile, epoch, limit)
		return c.rowsCall(name, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
			st, err := s.QueryEpochRawStream(schema, source, profile, epoch, limit)
			if err != nil {
				return nil, err
			}
			rows, err := FrameRows(st.Bytes)
			if err != nil {
				return nil, err
			}
			return unorderedDigest(rows), nil
		})
	}
	calls := []Call{
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileForward, At: at, SourceName: "celestrak-gp", Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: at, ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: "OMM-celestrak-gp-b040", Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileAsOf, At: at, MaxDeltaSeconds: 3600, Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileForward, At: at, MaxDeltaSeconds: 600, NoradCatID: u32(25544), Limit: 5}),
		q(storage.EpochRecordQuery{SchemaName: "MPE.fbs", Profile: storage.EpochProfileAsOf, At: at, EntityID: "22528 lorem-ipsum-dolor lorem-ipsum", Limit: 5}),
		q(storage.EpochRecordQuery{SchemaName: "MPE.fbs", Profile: storage.EpochProfileNearest, At: at, IncludeSource: true, Limit: 20}),
		q(storage.EpochRecordQuery{SchemaName: "IQC.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 20}),
		q(storage.EpochRecordQuery{SchemaName: "CAT.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 20}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: time.Unix(1700000000, 0).UTC(), Limit: 10}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 0}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileDay, Day: "2026-09-20", SourceName: "celestrak-gp", Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileDay, Day: "2026-09-20", NoradCatID: u32(25544), Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "MPE.fbs", Profile: storage.EpochProfileDay, Day: "2026-09-03", EntityID: "22528 lorem-ipsum-dolor lorem-ipsum", Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileWindow, From: tptr("2026-09-02T02:00:01Z"), To: tptr("2026-09-02T05:00:01Z"), NoradCatID: u32(25544), Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileWindow, From: tptr("2026-09-02T02:00:01Z"), To: tptr("2026-09-02T02:10:01Z"), SourceName: "celestrak-gp", BatchID: "OMM-celestrak-gp-b002", Limit: 50}),
		q(storage.EpochRecordQuery{SchemaName: "IQC.fbs", Profile: storage.EpochProfileWindow, From: tptr("2020-01-01T00:00:00Z"), To: tptr("2030-01-01T00:00:00Z"), Limit: 20}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileCoverage, SourceName: "celestrak-gp", BatchID: OMMLatestBatch}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileCoverage, NoradCatID: u32(25544)}),
		q(storage.EpochRecordQuery{SchemaName: "MPE.fbs", Profile: storage.EpochProfileCoverage}),
		q(storage.EpochRecordQuery{SchemaName: "CAT.fbs", Profile: storage.EpochProfileCoverage}),
		q(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: "epoch.bogus", At: at, Limit: 5}),
		q(storage.EpochRecordQuery{SchemaName: "PNM.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 5}),
		q(storage.EpochRecordQuery{SchemaName: "XYZ.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 5}),
	}
	// The same question (R18's ruling): the OMM as_of stream is the source's
	// record per object on or before the epoch, ranked by the type's epoch
	// rule, which format 1's EPOCH API answers with the source filter (whole
	// seconds: the epochs are whole seconds); format 1's own stream ranks by
	// USER_DEFINED_EPOCH_TIMESTAMP (r18Accepted).
	asOf := float64(r16At) + 0.5
	asOfName := fmt.Sprintf("QueryEpochRawStream %s@%s %s %.0f limit=%d", "OMM.fbs", "celestrak-gp", "as_of", asOf, 0)
	sameAsOf := c.rowsCall(asOfName, "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		ms, err := s.QueryEpochRecords(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileAsOf,
			At: time.Unix(int64(asOf), 0).UTC(), SourceName: "celestrak-gp", Limit: epochAllObjects})
		if err != nil {
			return nil, err
		}
		rows := make([]Row, 0, len(ms))
		for _, x := range ms {
			if x.Record == nil {
				return nil, fmt.Errorf("epoch match %s without its record", x.EntityKey)
			}
			rows = append(rows, frameRow(x.Record.Data))
		}
		return unorderedDigest(rows), nil
	})
	streams := func(same bool) []Call {
		omm := stream("OMM.fbs", "celestrak-gp", "as_of", asOf, 0)
		if same {
			omm = sameAsOf
		}
		return []Call{
			stream("MPE.fbs", "celestrak-gp", "nearest", float64(r16At), 100),
			omm,
			stream("CAT.fbs", "celestrak-satcat", "nearest", float64(r16At), 50),
			stream("OMM.fbs", "no-such-source", "forward", float64(r16At), 50),
			stream("OMM.fbs", "celestrak-gp", "bogus", float64(r16At), 50),
		}
	}
	same := covShape(class, "epoch streams"+SameQuestionSuffix, "OMM.fbs", streams(true)...)
	same.Arms = []string{ArmF1}
	return []Shape{covShape(class, "epoch profiles", "OMM.fbs", calls...),
		rule(covShape(class, "epoch streams", "OMM.fbs", streams(false)...), "QueryEpochRawStream OMM.fbs@celestrak-gp", r18Accepted), same}
}

// V07: the SQL surface: sandboxed rows and streams (every fixture type,
// _source/_rowid columns, parameters of each type, aggregates), the
// sandbox's refusals and caps, SandboxedSelect, QueryRawStream and the
// public query surface. Format 1 fills its engine hot window first.
func (c *cov) v07() []Shape {
	const class = "V07"
	coverageNeedsSQL[class] = true
	caps := flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute}
	jsonRows := func(sql string, cp flatsqlrt.SandboxCaps, unordered bool, params ...any) Call {
		name := fmt.Sprintf("QuerySandboxedJSON %s %v rows=%d bytes=%d", sql, params, cp.MaxRows, cp.MaxBytes)
		return c.rowsCall(name, "", func(s *storage.FlatSQLStore) ([]Row, error) {
			payload, n, cols, err := s.QuerySandboxedJSON(sql, cp, params...)
			if err != nil {
				return nil, err
			}
			rows, err := JSONRows(payload)
			if err != nil {
				return nil, err
			}
			if unordered {
				rows = sortRows(rows)
			}
			return append([]Row{ValueRow("rows", strconv.Itoa(n), "cols", strconv.Itoa(cols))}, rows...), nil
		})
	}
	streamCall := func(sql string, cp flatsqlrt.SandboxCaps, params ...any) Call {
		name := fmt.Sprintf("QuerySandboxedStream %s %v rows=%d bytes=%d", sql, params, cp.MaxRows, cp.MaxBytes)
		return c.rowsCall(name, "", func(s *storage.FlatSQLStore) ([]Row, error) {
			st, err := s.QuerySandboxedStream(sql, cp, params...)
			if err != nil {
				return nil, err
			}
			rows, err := FrameRows(st.Bytes)
			if err != nil {
				return nil, err
			}
			return unorderedDigest(rows), nil
		})
	}
	raw := func(sql string, params ...any) Call {
		return c.rowsCall(fmt.Sprintf("QueryRawStream %s %v", sql, params), "", func(s *storage.FlatSQLStore) ([]Row, error) {
			st, err := s.QueryRawStream(sql, params...)
			if err != nil {
				return nil, err
			}
			rows, err := FrameRows(st.Bytes)
			if err != nil {
				return nil, err
			}
			return unorderedDigest(rows), nil
		})
	}
	sel := func(sql string, cp storage.SandboxSelectCaps) Call {
		return c.rowsCall(fmt.Sprintf("SandboxedSelect %s rows=%d bytes=%d", sql, cp.MaxRows, cp.MaxBytes), "", func(s *storage.FlatSQLStore) ([]Row, error) {
			res, err := s.SandboxedSelect(context.Background(), sql, cp)
			if err != nil {
				return nil, err
			}
			rows := []Row{ValueRow("columns", strings.Join(res.Columns, ","), "truncated", strconv.FormatBool(res.Truncated), "n", strconv.Itoa(len(res.Rows)))}
			for _, r := range res.Rows {
				row := Row{}
				for i, v := range r {
					row = append(row, Field{strconv.Itoa(i), v})
				}
				rows = append(rows, row)
			}
			return rows, nil
		})
	}
	surface := c.rowsCall("PublicQuerySurface", "", func(s *storage.FlatSQLStore) ([]Row, error) {
		ts, err := s.PublicQuerySurface()
		if err != nil {
			return nil, err
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
		rows := make([]Row, 0, len(ts))
		for _, t := range ts {
			rows = append(rows, ValueRow("name", t.Name, "kind", t.Kind, "source", t.Source, "columns", strings.Join(t.Columns, ","),
				"placeholders", strings.Join(t.PlaceholderColumns, ","), "records", i64(t.Records)))
		}
		return rows, nil
	})
	small := flatsqlrt.SandboxCaps{Timeout: time.Minute, MaxRows: 5, MaxBytes: 1 << 20}
	tiny := flatsqlrt.SandboxCaps{Timeout: time.Minute, MaxRows: 1000, MaxBytes: 512}
	calls := []Call{
		jsonRows("SELECT COUNT(*) FROM MPE", caps, false),
		jsonRows("SELECT COUNT(*) FROM IQC", caps, false),
		jsonRows("SELECT COUNT(*) FROM CAT", caps, false),
		jsonRows("SELECT NORAD_CAT_ID, OBJECT_NAME, _source, _rowid FROM CAT WHERE NORAD_CAT_ID = ?1", caps, true, int64(40463)),
		jsonRows("SELECT OBJECT_NAME FROM OMM WHERE OBJECT_NAME = ?1 LIMIT 3", caps, true, "OBJECT 25544"),
		jsonRows("SELECT NORAD_CAT_ID FROM OMM WHERE NORAD_CAT_ID < ?1 ORDER BY NORAD_CAT_ID LIMIT 5", caps, false, 3.5),
		jsonRows("SELECT _source, COUNT(*) FROM CAT GROUP BY _source", caps, true),
		jsonRows("SELECT _source, COUNT(*) FROM IQC GROUP BY _source", caps, true),
		jsonRows("SELECT MIN(_rowid), MAX(_rowid) FROM OMM", caps, false),
		jsonRows("SELECT NORAD_CAT_ID FROM OMM ORDER BY NORAD_CAT_ID LIMIT 50", small, false),
		jsonRows("SELECT _data FROM OMM WHERE NORAD_CAT_ID = 25544", tiny, false),
		jsonRows("DELETE FROM OMM", caps, false),
		jsonRows("SELECT 1; SELECT 2", caps, false),
		jsonRows("SELECT * FROM sdn_record_index LIMIT 1", caps, false),
		jsonRows("SELECT * FROM NO_SUCH_TABLE", caps, false),
		// C-39 S1: a "<TYPE>@<source>" relation for a source the node knows
		// (another type's feed, or local) and the type has no table for
		// answers empty; a source no type has stays "no such table".
		jsonRows("SELECT COUNT(*) FROM \"OMM@IQEngine\"", caps, false),
		jsonRows("SELECT COUNT(*) FROM \"OMM@no-such-source\"", caps, false),
		streamCall("SELECT _data FROM \"MPE@local\"", caps),
		raw("SELECT _data FROM \"CAT@celestrak-gp\""),
		streamCall("SELECT _data FROM MPE WHERE NORAD_CAT_ID = ?1", caps, int64(22528)),
		streamCall("SELECT _data FROM IQC LIMIT 10", caps),
		streamCall("SELECT _data FROM \"CAT@celestrak-satcat-csv\" WHERE NORAD_CAT_ID = 40463", caps),
		streamCall("SELECT _data FROM OMM", small),
		streamCall("SELECT NORAD_CAT_ID FROM OMM LIMIT 1", caps),
		raw("SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?1", int64(25544)),
		raw("SELECT _data FROM CAT LIMIT 20"),
		sel("SELECT NORAD_CAT_ID, OBJECT_NAME FROM CAT WHERE NORAD_CAT_ID BETWEEN 40460 AND 40470 ORDER BY NORAD_CAT_ID", storage.SandboxSelectCaps{MaxRows: 100, MaxBytes: 1 << 20, Timeout: time.Minute}),
		sel("SELECT NORAD_CAT_ID FROM OMM ORDER BY NORAD_CAT_ID LIMIT 100", storage.SandboxSelectCaps{MaxRows: 3, MaxBytes: 1 << 20, Timeout: time.Minute}),
		sel("SELECT COUNT(*) FROM IQC", storage.SandboxSelectCaps{}),
		sel("DROP TABLE OMM", storage.SandboxSelectCaps{}),
		surface,
	}
	sh := rule(covShape(class, "SQL surface", "", calls...), "SELECT MIN(_rowid), MAX(_rowid) FROM OMM", c9Rowid, "MIN(_rowid)", "MAX(_rowid)")
	sh.Policy.Surface = "PublicQuerySurface" // C-39 S1 listing and S2 (surfaceDiff)
	return []Shape{sh}
}

// V08: summaries and accounting not in R20, full-text state, the engine
// hooks, the lane ledger reads.
func (c *cov) v08() []Shape {
	const class = "V08"
	calls := []Call{
		c.valueCall("PeerStorageBytes source:celestrak", "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes(gpPeer)
			return ValueRow("bytes", i64(n)), err
		}),
		c.valueCall("PeerStorageBytes source:sigmf", "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes(iqcSigmfPeer)
			return ValueRow("bytes", i64(n)), err
		}),
		c.valueCall("PeerStorageBytes unknown", "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes("16Uiu2HAmNoSuchPeer")
			return ValueRow("bytes", i64(n)), err
		}),
		c.valueCall("PeerStorageBytes empty", "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes("")
			return ValueRow("bytes", i64(n)), err
		}),
		c.valueCall("LiveRecordBytesReconciled", "", func(s *storage.FlatSQLStore) (Row, error) {
			return ValueRow("reconciled", strconv.FormatBool(s.LiveRecordBytesReconciled())), nil
		}),
		c.valueCall("RecoverPoisonedEngine (healthy)", "", func(s *storage.FlatSQLStore) (Row, error) {
			_, err := s.RecoverPoisonedEngine()
			return ValueRow("ok", "1"), err
		}),
		c.valueCall("NewestServableSourceBatch OMM (no ledger)", "", func(s *storage.FlatSQLStore) (Row, error) {
			id, at, ok, pending, err := s.NewestServableSourceBatch("OMM.fbs", FixtureProvider, "celestrak-gp")
			return ValueRow("batch", id, "at", unixOf(at), "ok", strconv.FormatBool(ok), "pending", strconv.FormatBool(pending)), err
		}),
	}
	calls = append(calls, c.valueCall("CheckFullTextSearch empty search", "", func(s *storage.FlatSQLStore) (Row, error) {
		return ValueRow("ok", "1"), s.CheckFullTextSearch("CAT.fbs", "")
	}))
	return []Shape{covShape(class, "accounting, full text, hooks, ledger", "", calls...)}
}

// rebuildIndexBaseline is format 1's RebuildIndex answer without the
// rebuild: format 1 re-upserts one index row per record of each schema's
// read source (every registered schema, one per CID, GROUP BY cid) and
// returns that count per schema, which is Count(schema) on a store whose
// every record indexes (the fixture). Running it re-upserts 4.4 million
// index rows one statement at a time (days on this fixture).
func rebuildIndexBaseline(s *storage.FlatSQLStore) ([]Row, error) {
	v, err := sds.NewValidator(nil)
	if err != nil {
		return nil, err
	}
	schemas := v.Schemas()
	sort.Strings(schemas)
	var rows []Row
	for _, schema := range schemas {
		n, err := s.Count(schema)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ValueRow("schema", schema, "n", i64(n)))
	}
	return rows, nil
}
