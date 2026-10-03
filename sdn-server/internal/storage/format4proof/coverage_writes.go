package format4proof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	EPMfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	KMFfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	PNMfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The X coverage classes: one write scenario each, on its own fresh clone,
// followed by the reads that see what it left (record bytes, copies, tags
// with source_url and content key, cursors, counts, lanes, windows, index
// pages, epochs, SQL and summaries). The records are fixture records (OMM
// b052, MPE b051, the IQEngine and satcat-csv lanes), cloned to new CIDs where
// a scenario needs new ones (CloneOf, CloneKeepIdentity, RestampIQC: the
// write benchmarks' mutations), and the few standards the fixture lacks
// (PNM, KMF, EPM, PLOG) are built from their schemas with fixed values. A
// scenario writes a handful of records into its own lanes, so format 1 runs
// it in seconds; W01–W10 cover the same paths at scale.

// Peers the scenarios write as, beside the fixture's.
const (
	covPeer      = "16Uiu2HAmCoverageProducerA1111111111111111111111111"
	covPeer2     = "16Uiu2HAmCoverageProducerB2222222222222222222222222"
	covStorePeer = "16Uiu2HAmCoverageStorefrontC333333333333333333333333"
	covProvider  = "16Uiu2HAmCoverageImportProviderD44444444444444444444"
	covRelay     = "16Uiu2HAmCoverageRelayOriginE5555555555555555555555"
)

var covSig = []byte("coverage-signature-0123456789abcdef0123456789abcdef0123456789abcd")

// covTags are fully populated tags: url, content key, producer peer and
// public key, licence (the fields the fixture leaves empty).
func covTags(source, batch string) storage.SourceTags {
	return storage.SourceTags{ProviderID: FixtureProvider, SourceName: source, SourceURL: "https://celestrak.org/NORAD/elements/gp.php?GROUP=active&FORMAT=" + batch,
		BatchID: batch, ContentKeyID: "ckey-" + batch, ProducerPeerID: covPeer, ProducerPublicKey: "ed25519:" + covPeer,
		License: "CC-BY-4.0", LicenseURL: "https://creativecommons.org/licenses/by/4.0/", Citation: "CelesTrak GP (coverage)", ShareAlike: true}
}

// plainTags are the fixture's shape of tags: provider, source, batch.
func plainTags(source, batch string) storage.SourceTags {
	return storage.SourceTags{ProviderID: FixtureProvider, SourceName: source, BatchID: batch}
}

func cidsOf(recs [][]byte) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = storage.ComputeCID(r)
	}
	return out
}

// clonesOf is CloneOf(typ, seeds[from:to], c) for each seed.
func clonesOf(typ string, seeds [][]byte, from, to int, c int64) [][]byte {
	var out [][]byte
	for i := from; i < to && i < len(seeds); i++ {
		out = append(out, CloneOf(typ, seeds[i], c, nil))
	}
	return out
}

// ---- write calls -------------------------------------------------------------

func (c *cov) storeBatch(name, schema string, recs [][]byte, peer string, sig []byte, tags *storage.SourceTags) Call {
	return c.writeValue(name, schema, func(s *storage.FlatSQLStore) (Row, error) {
		var n int
		var err error
		if tags == nil {
			n, err = s.StoreBatch(schema, recs, peer, sig)
		} else {
			n, err = s.StoreBatchWithSourceTags(schema, recs, peer, sig, *tags)
		}
		return ValueRow("n", strconv.Itoa(n)), err
	})
}

func (c *cov) store(name, schema string, rec []byte, peer string, sig []byte, tags *storage.SourceTags) Call {
	return c.writeValue(name, schema, func(s *storage.FlatSQLStore) (Row, error) {
		var id string
		var err error
		if tags == nil {
			id, err = s.Store(schema, rec, peer, sig)
		} else {
			id, err = s.StoreWithSourceTags(schema, rec, peer, sig, *tags)
		}
		return ValueRow("cid", id), err
	})
}

// writeValue and writeRows are valueCall and rowsCall marked as writes.
func (c *cov) writeValue(name, schema string, fn func(s *storage.FlatSQLStore) (Row, error)) Call {
	return c.writing(c.valueCall(name, schema, fn))
}

func (c *cov) writeRows(name, schema string, fn func(s *storage.FlatSQLStore) ([]Row, error)) Call {
	return c.writing(c.rowsCall(name, schema, fn))
}

// ---- read-back calls ---------------------------------------------------------

// gets is GetRecord of each CID (a miss is an err row).
func (c *cov) gets(name, schema string, cids []string) Call {
	return c.rowsCall(name, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
		var rows []Row
		for _, id := range cids {
			r, err := s.GetRecord(schema, id)
			if err != nil {
				rows = append(rows, append(Row{{"cid", id}}, errRow(name, err)...))
				continue
			}
			rows = append(rows, getRow(r))
		}
		return rows, nil
	})
}

// tagsOf is GetSourceTags of each CID (the newest tag; a miss is an err row).
func (c *cov) tagsOf(name, schema string, cids []string) Call {
	return c.rowsCall(name, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
		var rows []Row
		for _, id := range cids {
			t, err := s.GetSourceTags(schema, id)
			if err != nil {
				rows = append(rows, append(Row{{"cid", id}}, errRow(name, err)...))
				continue
			}
			rows = append(rows, TagsRow(id, t))
		}
		return rows, nil
	})
}

// refsOf is QueryRawRecordRefsByRefs, one ref per CID (with mod applied):
// the records with cursor, tags and materialized time.
func (c *cov) refsOf(name, schema string, cids []string, mod func(*storage.RawRecordRef)) Call {
	return c.recordsCall(name, schema, func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
		refs := make([]storage.RawRecordRef, 0, len(cids))
		for _, id := range cids {
			r := storage.RawRecordRef{CID: id}
			if mod != nil {
				mod(&r)
			}
			refs = append(refs, r)
		}
		return s.QueryRawRecordRefsByRefs(schema, refs)
	})
}

// laneReads are the reads of one lane (any field may be empty): its
// datasync page, counts and head, its CID-ordered window, its index page,
// its tagged records and its byte probe.
// laneReadLimit is the row limit of a lane read's pages (laneReads).
const laneReadLimit = 200

func (c *cov) laneReads(schema, provider, source, batch string) []Call {
	tag := fmt.Sprintf("%s %s/%s/%s", schema, provider, source, batch)
	q := storage.RawRecordQuery{SchemaName: schema, ProviderID: provider, SourceName: source, BatchID: batch}
	cur := q
	cur.UseRowIDCursor, cur.Limit = true, laneReadLimit
	w := storage.IndexedRecordQuery{SchemaName: schema, ProviderID: provider, SourceName: source, BatchID: batch, Limit: laneReadLimit, OrderByCID: true}
	return []Call{
		c.rawQuery("lane page "+tag, cur, false),
		c.valueCall("lane snapshot "+tag, schema, func(s *storage.FlatSQLStore) (Row, error) {
			n, h, err := s.RawRecordSnapshot(q)
			return covHead(q, n, &h), err
		}),
		c.valueCall("lane count "+tag, schema, func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.CountRawRecords(q)
			return covHead(q, n, nil), err
		}),
		c.valueCall("lane head "+tag, schema, func(s *storage.FlatSQLStore) (Row, error) {
			h, err := s.RawRecordHead(q)
			return covHead(q, -1, &h), err
		}),
		c.recordsCall("lane window "+tag, schema, func(s *storage.FlatSQLStore) ([]*storage.Record, error) { return s.QueryIndexedRecords(w) }),
		c.rowsCall("lane index page "+tag, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
			rows, total, err := s.RecordIndexPage(storage.RecordIndexPageQuery{SchemaName: schema, ProviderID: provider, SourceName: source, BatchID: batch, Limit: 50})
			if err != nil {
				return nil, err
			}
			out := []Row{ValueRow("total", i64(total))}
			for _, r := range rows {
				out = append(out, ValueRow("cid", r.CID, "norad", i64p(r.NoradCatID), "epoch", i64p(r.EpochUnix)))
			}
			return out, nil
		}),
		c.recordsCall("lane tagged "+tag, schema, func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			return s.QuerySourceTaggedRecords(storage.SourceTagQuery{SchemaName: schema, ProviderID: provider, SourceName: source, BatchID: batch, Limit: laneReadLimit})
		}),
		c.valueCall("lane bytes probe "+tag, schema, func(s *storage.FlatSQLStore) (Row, error) {
			n, more, err := s.IndexedRecordWindowLimitForBytes(storage.IndexedRecordQuery{SchemaName: schema, ProviderID: provider, SourceName: source, BatchID: batch}, 1<<20)
			return ValueRow("n", strconv.Itoa(n), "more", strconv.FormatBool(more)), err
		}),
	}
}

// typeReads are a type's counts and head, its newest records and its first
// full-table page after the writes.
func (c *cov) typeReads(schema string) []Call {
	return []Call{
		c.valueCall("Count "+schema, schema, func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.Count(schema)
			return ValueRow("n", i64(n)), err
		}),
		c.valueCall("type head "+schema, schema, func(s *storage.FlatSQLStore) (Row, error) {
			q := storage.RawRecordQuery{SchemaName: schema}
			n, h, err := s.RawRecordSnapshot(q)
			return covHead(q, n, &h), err
		}),
		c.rawQuery("newest "+schema, storage.RawRecordQuery{SchemaName: schema, Limit: 10}, true),
		c.recordsCall("recent "+schema, schema, func(s *storage.FlatSQLStore) ([]*storage.Record, error) { return s.QueryRecentRecords(schema, 10) }),
		c.recordsCall("table page "+schema, schema, func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			p, err := s.FullTablePageWithCursor(storage.FullTablePageQuery{SchemaName: schema, Limit: 10})
			return p.Records, err
		}),
	}
}

// summaries are the store's summaries and accounting after the writes.
func (c *cov) summaries(peers ...string) []Call {
	var out []Call
	for _, fn := range []string{"DataSummary", "SourceBatchProgress", "ProducerSourceProgress", "SourceRecordCounts", "SchemaDateRanges", "LiveRecordBytes"} {
		fn := fn
		out = append(out, c.rowsCall(fn, "", func(s *storage.FlatSQLStore) ([]Row, error) {
			v, err := summaryFetch(s, fn, "")
			if err != nil {
				return nil, err
			}
			rows, err := summaryRows(v)
			if err != nil {
				return nil, err
			}
			return sortRows(rows), nil // a summary's rows compare as a set (R20)
		}))
	}
	for _, p := range peers {
		p := p
		out = append(out, c.valueCall("PeerStorageBytes "+p, "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes(p)
			return ValueRow("bytes", i64(n)), err
		}))
	}
	return out
}

// ---- records the fixture lacks ----------------------------------------------

// buildPNM is a publication notice for a fixture record, fixed values.
func buildPNM(i int, fileCID string) []byte {
	b := flatbuffers.NewBuilder(256)
	addr := b.CreateString("/ip4/127.0.0.1/tcp/4001/p2p/" + covStorePeer)
	ts := b.CreateString(fmt.Sprintf("2026-09-27T12:%02d:00Z", i))
	id := b.CreateString(fileCID)
	name := b.CreateString(fmt.Sprintf("coverage-%d.fbs", i))
	fid := b.CreateString("$OMM")
	PNMfb.PNMStart(b)
	PNMfb.PNMAddMULTIFORMAT_ADDRESS(b, addr)
	PNMfb.PNMAddPUBLISH_TIMESTAMP(b, ts)
	PNMfb.PNMAddCID(b, id)
	PNMfb.PNMAddFILE_NAME(b, name)
	PNMfb.PNMAddFILE_ID(b, fid)
	PNMfb.FinishPNMBuffer(b, PNMfb.PNMEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

// buildKMF is key material whose KEY_BYTES the store seals (field
// encryption, KMF.KEY_BYTES `(encrypted)`).
func buildKMF(i int) []byte {
	b := flatbuffers.NewBuilder(256)
	keyID := b.CreateString(fmt.Sprintf("coverage-key-%d", i))
	key := make([]byte, 32)
	for j := range key {
		key[j] = byte(i*31 + j)
	}
	kb := b.CreateByteVector(key)
	KMFfb.KMFStart(b)
	KMFfb.KMFAddKEY_ID(b, keyID)
	KMFfb.KMFAddROLE(b, 1)
	KMFfb.KMFAddALGORITHM(b, 6)
	KMFfb.KMFAddENCODING(b, 1)
	KMFfb.KMFAddKEY_BYTES(b, kb)
	KMFfb.KMFAddVERSION(b, uint32(i+1))
	KMFfb.KMFAddEXPIRES_AT(b, 1893456000)
	KMFfb.FinishKMFBuffer(b, KMFfb.KMFEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

// buildEPM is an entity profile, fixed values.
func buildEPM(i int) []byte {
	b := flatbuffers.NewBuilder(256)
	dn := b.CreateString(fmt.Sprintf("CN=coverage-%d,O=Space Data Network", i))
	legal := b.CreateString(fmt.Sprintf("Coverage Entity %d", i))
	email := b.CreateString(fmt.Sprintf("coverage-%d@example.invalid", i))
	EPMfb.EPMStart(b)
	EPMfb.EPMAddDN(b, dn)
	EPMfb.EPMAddLEGAL_NAME(b, legal)
	EPMfb.EPMAddEMAIL(b, email)
	EPMfb.FinishEPMBuffer(b, EPMfb.EPMEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

// buildPLOG is a publication-log entry as internal/logservice builds it
// (buildPLGFlatBuffer, size-prefixed, "PLOG"), with fixed values.
func buildPLOG(seq uint64, schemaType, publisher, recordCID, prevHash, entryHash string, ts uint64) []byte {
	b := flatbuffers.NewBuilder(512)
	st := b.CreateString(schemaType)
	pub := b.CreateString(publisher)
	rc := b.CreateString(recordCID)
	prev := b.CreateString(prevHash)
	eh := b.CreateString(entryHash)
	sigType := b.CreateString("Ed25519")
	day := b.CreateString("2026-09-27")
	b.StartObject(11)
	b.PrependUint64Slot(0, seq, 0)
	b.PrependUOffsetTSlot(1, st, 0)
	b.PrependUOffsetTSlot(2, pub, 0)
	b.PrependUOffsetTSlot(3, rc, 0)
	b.PrependUOffsetTSlot(4, prev, 0)
	b.PrependUOffsetTSlot(5, eh, 0)
	b.PrependUint64Slot(6, ts, 0)
	b.PrependUOffsetTSlot(8, sigType, 0)
	b.PrependUOffsetTSlot(10, day, 0)
	b.FinishSizePrefixedWithFileIdentifier(b.EndObject(), []byte("PLOG"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// ---- the scenarios -----------------------------------------------------------

// writeCoverage builds the X classes (nil when the inputs are missing).
func (c *cov) writeCoverage() []Shape {
	omm, mpe, iqc, cat := c.sets[InputOMMB052], c.sets[InputMPEB051], c.sets[InputIQC], c.sets[InputCATCSV]
	if len(omm) < 100 || len(mpe) < 100 || len(iqc) < 100 || len(cat) < 100 {
		return nil
	}
	var out []Shape
	out = append(out, c.x01(omm)...)
	out = append(out, c.x02(omm, mpe, iqc, cat)...)
	out = append(out, c.x03(omm, iqc)...)
	out = append(out, c.x04()...)
	out = append(out, c.x05(omm)...)
	out = append(out, c.x06(omm)...)
	out = append(out, c.x07(omm)...)
	out = append(out, c.x08()...)
	out = append(out, c.x09()...)
	out = append(out, c.x10(omm)...)
	out = append(out, c.x11(mpe)...)
	out = append(out, c.x12(omm)...)
	out = append(out, c.x13()...)
	out = append(out, c.x14()...)
	out = append(out, c.xm(omm, mpe, iqc, cat)...)
	return out
}

// ClassXM is the migration class: every X scenario but X13 (the
// maintenance verbs) on one format-1 clone, its writes first, then its
// reads; the clone is then migrated (store-migrate --to 4: --inventory, the
// migration, --verify-only), its record sets digested on both sides (W-op
// digests, oversized records aside, C-6) and the reads run again on the
// migrated store against format 1's answers (DriveCoverage, driveXM). It
// carries into a migration what the fixture lacks: untagged records,
// records with several tags, url and content keys, copies by a second
// producer, sealed records, an oversized record, standards the fixture
// holds none of, imported records, the publication log, the local EPM, the
// lane ledger.
const ClassXM = "XM"

// xmTypes are the types the XM digests cover.
var xmTypes = []string{"OMM.fbs", "MPE.fbs", "IQC.fbs", "CAT.fbs", "PNM.fbs", "KMF.fbs", "EPM.fbs", "PLOG.fbs"}

func (c *cov) xm(omm, mpe, iqc, cat [][]byte) []Shape {
	coverageNeedsSQL[ClassXM] = true
	scenarios := [][]Shape{c.x01(omm), c.x02(omm, mpe, iqc, cat), c.x03(omm, iqc), c.x04(), c.x05(omm), c.x06(omm), c.x07(omm),
		c.x08(), c.x09(), c.x10(omm), c.x11(mpe), c.x12(omm), c.x14()}
	// The writes in order; the reads after all of them, one shape per
	// policy of the shapes they came from (strict; C-6; C-12).
	var writes []Call
	reads := map[string][]Call{}
	policy := map[string]Policy{}
	var order []string
	for _, shapes := range scenarios {
		for _, sh := range shapes {
			for _, call := range sh.Calls {
				switch call.Name {
				case "remove the placed shard":
					continue // the migrated store serves the publication too
				case "RepairDatasetPublicationIndexFromShard batch a":
					continue // its argument is the publication X12's write returned; XM's format-4 reads run no writes
				}
				call.Name = sh.Class + ": " + call.Name
				if call.Write {
					writes = append(writes, call)
					continue
				}
				k := sh.Policy.Accepted
				if _, ok := policy[k]; !ok {
					policy[k], order = sh.Policy, append(order, k)
				} else {
					policy[k] = mergePolicy(policy[k], sh.Policy)
				}
				reads[k] = append(reads[k], call)
			}
		}
	}
	w := covShape(ClassXM, "XM writes", "", writes...)
	w.Arms = []string{ArmF1, ArmF2}
	out := []Shape{w}
	for _, k := range order {
		name := "XM reads (format 4: on the migrated store)"
		switch {
		case strings.Contains(k, "C-12"):
			name += " C-12"
		case strings.Contains(k, "C-6"):
			name += " C-6"
		}
		sh := covShape(ClassXM, name, "", reads[k]...)
		sh.Policy = policy[k]
		// Every XM read follows every scenario's writes, so each summary
		// read shows the C-39 items the scenarios' own reads scope to
		// theirs: the XYZ table (U1, X01), the dangling tags (U2, X08), the
		// KMF lane bytes (U5, X04) and the CAT lane bytes (U6, X02).
		sh = xmSetAside(laneBytes(laneBytes(u2Rows(u1Rows(sh, ""), ""), "", c39U5, "KMF.fbs"), "", c39U6, "CAT.fbs"))
		out = append(out, sh)
	}
	return out
}

// xmSetAside is C-6 on the migrated store: X05's 9 MiB record (lanes
// celestrak-gp OMM-cov-big and OMM-cov-big-single, stored by source:celestrak)
// is set aside by the migration, so format 4's summaries lack it: format 1's
// OMM-cov-big-single rows, the counts and bytes of the OMM-cov-big lane, of
// the OMM celestrak-gp producer lane, of the OMM type and of the store, and
// source:celestrak's partition bytes.
func xmSetAside(sh Shape) Shape {
	for _, call := range summaryCalls {
		sh.Policy.Calls = append(sh.Policy.Calls,
			CallRuling{Call: call, Why: c6SetAside, Absent: "OMM.fbs", Batch: "OMM-cov-big-single"},
			CallRuling{Call: call, Why: c6SetAside, Fields: []string{"Count", "TotalBytes"}, Standard: "OMM.fbs", Source: "celestrak-gp", Batch: "OMM-cov-big"})
	}
	sh.Policy.Calls = append(sh.Policy.Calls,
		CallRuling{Call: "ProducerSourceProgress", Why: c6SetAside, Fields: []string{"Count", "TotalBytes"}, Standard: "OMM.fbs", Source: "celestrak-gp"},
		CallRuling{Call: "DataSummary", Why: c6SetAside, Fields: []string{"n", "bytes", "total_records", "total_bytes"}, Standard: "OMM.fbs"},
		CallRuling{Call: "SchemaDateRanges", Why: c6SetAside, Fields: []string{"n", "bytes"}, Standard: "OMM.fbs"},
		CallRuling{Call: "LiveRecordBytes", Why: c6SetAside, Fields: []string{"bytes"}},
		CallRuling{Call: "PeerStorageBytes source:celestrak", Why: c6SetAside, Fields: []string{"bytes"}},
		// source_celestrak shares source:celestrak's token, so its partition (C-2).
		CallRuling{Call: "PeerStorageBytes the token-sharing peer", Why: c6SetAside, Fields: []string{"bytes"}})
	// The OMM type's count and head: the set-aside record is missing, and
	// X07's two records held by two feeds count once per feed (C-38 (5)).
	for _, call := range []string{"Count OMM.fbs", "type head OMM.fbs"} {
		sh.Policy.Calls = append(sh.Policy.Calls, CallRuling{Call: call, Why: c6SetAside + "; " + c38PerFeed, Fields: []string{"n", "bytes"}})
	}
	return sh
}

// mergePolicy is a's policy with b's call rulings, tie rules and CID
// aliases added (XM: one read shape takes the calls of several scenario
// shapes of one accepted difference, each with its own rulings).
func mergePolicy(a, b Policy) Policy {
	a.Calls = append(append([]CallRuling(nil), a.Calls...), b.Calls...)
	seen := map[TieRule]bool{}
	var ties []TieRule
	for _, t := range append(append([]TieRule(nil), a.TieOrdered...), b.TieOrdered...) {
		if !seen[t] {
			seen[t] = true
			ties = append(ties, t)
		}
	}
	a.TieOrdered = ties
	if len(b.CIDAliases) > 0 {
		m := map[string]string{}
		for k, v := range a.CIDAliases {
			m[k] = v
		}
		for k, v := range b.CIDAliases {
			m[k] = v
		}
		a.CIDAliases = m
	}
	return a
}

// X01: storeBatch untagged (api/publish, logservice PLG, archive imports):
// NEW into the type's partition, an untagged repeat (DUP), a held CID from a
// second producer with a signature (COPY: a new partition), NEW from that
// producer, empty batches, an unknown type; then the records, their copies
// and cursors read back.
func (c *cov) x01(omm [][]byte) []Shape {
	const class = "X01"
	fresh := clonesOf("OMM", omm, 0, 5, 901)
	held := omm[10:13]
	fresh2 := clonesOf("OMM", omm, 20, 23, 902)
	// Peers whose producer token is another peer's (C-2: format 1 keeps the
	// row's own peer_id; "source_celestrak" is source:celestrak's table) and
	// no peer at all (format 1's "unattributed" table).
	sameToken := clonesOf("OMM", omm, 25, 27, 914)
	noPeer := clonesOf("OMM", omm, 27, 29, 915)
	all := append(append(append(append(cidsOf(fresh), cidsOf(held)...), cidsOf(fresh2)...), cidsOf(sameToken)...), cidsOf(noPeer)...)
	writes := []Call{
		c.storeBatch("StoreBatch OMM 5 new", "OMM.fbs", fresh, gpPeer, nil, nil),
		c.storeBatch("StoreBatch OMM 5 again", "OMM.fbs", fresh, gpPeer, nil, nil),
		c.storeBatch("StoreBatch OMM 3 held, second producer, signed", "OMM.fbs", held, covPeer, covSig, nil),
		c.storeBatch("StoreBatch OMM 3 new, second producer, signed", "OMM.fbs", fresh2, covPeer, covSig, nil),
		c.storeBatch("StoreBatch OMM 2 new, a peer sharing source:celestrak's token", "OMM.fbs", sameToken, "source_celestrak", nil, nil),
		c.storeBatch("StoreBatch OMM 2 new, no peer", "OMM.fbs", noPeer, "", nil, nil),
		c.storeBatch("StoreBatch OMM empty", "OMM.fbs", nil, gpPeer, nil, nil),
		c.storeBatch("StoreBatchWithSourceTags OMM empty", "OMM.fbs", [][]byte{}, gpPeer, nil, &storage.SourceTags{ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: "OMM-cov-empty"}),
		c.storeBatch("StoreBatch unknown type", "XYZ.fbs", fresh[:1], gpPeer, nil, nil),
	}
	reads := []Call{
		c.gets("GetRecord written", "OMM.fbs", all),
		c.tagsOf("GetSourceTags written", "OMM.fbs", all),
		c.refsOf("refs untagged", "OMM.fbs", cidsOf(fresh), nil),
		c.refsOf("refs second producer's copies", "OMM.fbs", append(cidsOf(held), cidsOf(fresh2)...), func(r *storage.RawRecordRef) { r.PeerID = covPeer }),
		c.refsOf("refs the token-sharing peer's records", "OMM.fbs", cidsOf(sameToken), func(r *storage.RawRecordRef) { r.PeerID = "source_celestrak" }),
		c.refsOf("refs the records with no peer", "OMM.fbs", cidsOf(noPeer), nil),
		c.rawQuery("cursor the token-sharing peer", storage.RawRecordQuery{SchemaName: "OMM.fbs", PeerID: "source_celestrak", UseRowIDCursor: true, Limit: 20}, false),
		c.valueCall("PeerStorageBytes the token-sharing peer", "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes("source_celestrak")
			return ValueRow("bytes", i64(n)), err
		}),
		c.rawQuery("cursor second producer", storage.RawRecordQuery{SchemaName: "OMM.fbs", PeerID: covPeer, UseRowIDCursor: true, Limit: 50}, false),
		c.rawQuery("cursor after the fixture", storage.RawRecordQuery{SchemaName: "OMM.fbs", UseRowIDCursor: true, AfterRowID: 2115808, Limit: 50}, true),
		c.rawQuery("cursor norad of a new record", storage.RawRecordQuery{SchemaName: "OMM.fbs", UseRowIDCursor: true, AfterRowID: 2100000, Limit: 50,
			SyncFilter: "NORAD_CAT_ID = " + noradOf(fresh[0])}, false),
		c.valueCall("PeerStorageBytes second producer", "", func(s *storage.FlatSQLStore) (Row, error) {
			n, err := s.PeerStorageBytes(covPeer)
			return ValueRow("bytes", i64(n)), err
		}),
	}
	reads = append(reads, c.typeReads("OMM.fbs")...)
	reads = append(reads, c.summaries(gpPeer, covPeer)...)
	return []Shape{rule(covShape(class, "storeBatch untagged: writes", "OMM.fbs", writes...), "StoreBatch unknown type", c39U1, "n", "err"),
		u1Rows(covShape(class, "storeBatch untagged: read back", "OMM.fbs", reads...), ""),
		c12Shape(class, "storeBatch untagged: the copy format 1 does not serve", "OMM.fbs",
			c.refsOf("refs first producer's copies", "OMM.fbs", cidsOf(held), func(r *storage.RawRecordRef) { r.PeerID = gpPeer }))}
}

// noradOf reads an OMM record's NORAD_CAT_ID through the fixture's own index
// (the record bytes; CloneOf keeps it).
func noradOf(rec []byte) string {
	n, ok := ommNorad(rec)
	if !ok {
		return "0"
	}
	return strconv.FormatUint(uint64(n), 10)
}

// X02: StoreBatchWithSourceTags with every tag field (source_url, content
// key, producer peer and public key, licence) and a signature: NEW, the
// same identity with a new url (DUP + url, C-3), a new batch (RETAG: two
// tags, the newest wins), MPE NEW, CAT supersede-on-ingest, IQC IDENT_DUP
// (C-21, C-26); then every tag, lane, export and summary read back.
func (c *cov) x02(omm, mpe, iqc, cat [][]byte) []Shape {
	const class = "X02"
	o := clonesOf("OMM", omm, 30, 35, 903)
	m := clonesOf("MPE", mpe, 30, 33, 903)
	var catNew, catOld [][]byte
	for i := 0; i < 3; i++ {
		catOld = append(catOld, cat[100+i])
		catNew = append(catNew, CloneKeepIdentity(cat[100+i], 903))
	}
	var restamped, iqcHeld [][]byte
	for i := 0; i < 3; i++ {
		r, _ := RestampIQC(iqc[200+i], 903)
		restamped = append(restamped, r)
		iqcHeld = append(iqcHeld, iqc[200+i])
	}
	t1 := covTags("celestrak-gp", "OMM-cov-b053")
	t1url := t1
	t1url.SourceURL += "&v=2"
	t2 := covTags("celestrak-gp", "OMM-cov-b054")
	tm := covTags("celestrak-gp", "MPE-cov-b052")
	tc := plainTags("celestrak-satcat-csv", "CAT-cov-2")
	ti := plainTags("IQEngine", "iqc-cov-restamp")
	writes := []Call{
		c.storeBatch("OMM 5 new, every tag field, signed", "OMM.fbs", o, gpPeer, covSig, &t1),
		c.storeBatch("OMM 5 again, new url", "OMM.fbs", o, gpPeer, covSig, &t1url),
		c.storeBatch("OMM 5 again, new batch", "OMM.fbs", o, gpPeer, covSig, &t2),
		c.storeBatch("MPE 3 new", "MPE.fbs", m, gpPeer, nil, &tm),
		c.storeBatch("CAT 3 same objects, new bytes (supersede on ingest)", "CAT.fbs", catNew, gpPeer, nil, &tc),
		c.storeBatch("IQC 3 restamped captures (identity repeat)", "IQC.fbs", restamped, iqcSigmfPeer, nil, &ti),
	}
	oc := cidsOf(o)
	reads := []Call{
		c.tagsOf("tags OMM (newest of two)", "OMM.fbs", oc),
		c.refsOf("refs OMM batch b053", "OMM.fbs", oc, func(r *storage.RawRecordRef) {
			r.ProviderID, r.SourceName, r.BatchID = FixtureProvider, "celestrak-gp", t1.BatchID
		}),
		c.refsOf("refs OMM batch b054 with producer key", "OMM.fbs", oc, func(r *storage.RawRecordRef) {
			r.BatchID, r.ProducerPeerID, r.ProducerPublicKey = t2.BatchID, covPeer, t2.ProducerPublicKey
		}),
		c.refsOf("refs OMM no tag fields", "OMM.fbs", oc, nil),
		c.gets("GetRecord OMM", "OMM.fbs", oc),
		c.tagsOf("tags MPE", "MPE.fbs", cidsOf(m)),
		c.gets("GetRecord CAT superseded", "CAT.fbs", cidsOf(catOld)),
		c.gets("GetRecord CAT new", "CAT.fbs", cidsOf(catNew)),
		c.tagsOf("tags CAT new", "CAT.fbs", cidsOf(catNew)),
		c.gets("GetRecord IQC restamped", "IQC.fbs", cidsOf(restamped)),
		c.tagsOf("tags IQC held (the repeat's batch)", "IQC.fbs", cidsOf(iqcHeld)),
		c.refsOf("refs IQC held, the repeat's batch", "IQC.fbs", cidsOf(iqcHeld), func(r *storage.RawRecordRef) { r.BatchID = ti.BatchID }),
		c.rawQuery("cursor OMM producer public key", storage.RawRecordQuery{SchemaName: "OMM.fbs", ProducerPublicKey: t1.ProducerPublicKey, UseRowIDCursor: true, Limit: 50}, false),
		c.rawQuery("cursor OMM producer peer", storage.RawRecordQuery{SchemaName: "OMM.fbs", ProducerPeerID: covPeer, UseRowIDCursor: true, Limit: 50}, false),
		c.rowsCall("export OMM lane b053 (the lane's tag)", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			return exportOf(s, "x02-b053", storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: t1.BatchID, Limit: 100, OrderByCID: true})
		}),
		c.rowsCall("export OMM by object (the newest tag)", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			n, _ := strconv.ParseUint(noradOf(o[0]), 10, 32)
			return exportOf(s, "x02-norad", storage.IndexedRecordQuery{SchemaName: "OMM.fbs", NoradCatID: u32(uint32(n)), Limit: 100, OrderByCID: true})
		}),
		c.valueCall("fingerprint OMM b053", "OMM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			f, n, err := s.DatasetPublicationSetFingerprint("OMM.fbs", FixtureProvider, "celestrak-gp", t1.BatchID)
			return ValueRow("fingerprint", f, "n", strconv.Itoa(n)), err
		}),
		c.rowsCall("epoch nearest of a new record's object", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			n, _ := strconv.ParseUint(noradOf(o[0]), 10, 32)
			q := storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: time.Unix(1790600000, 0).UTC(),
				NoradCatID: u32(uint32(n)), SourceName: "celestrak-gp", Limit: 5}
			cnt, err := s.CountEpochRecords(q)
			if err != nil {
				return nil, err
			}
			ms, err := s.QueryEpochRecords(q)
			if err != nil {
				return nil, err
			}
			a, _ := matchesAnswer("", ms, cnt)
			return a.Rows, nil
		}),
	}
	reads = append(reads, c.laneReads("OMM.fbs", FixtureProvider, "celestrak-gp", t1.BatchID)...)
	reads = append(reads, c.laneReads("OMM.fbs", FixtureProvider, "celestrak-gp", t2.BatchID)...)
	reads = append(reads, c.laneReads("MPE.fbs", FixtureProvider, "celestrak-gp", tm.BatchID)...)
	reads = append(reads, c.laneReads("CAT.fbs", FixtureProvider, "celestrak-satcat-csv", tc.BatchID)...)
	reads = append(reads, c.laneReads("IQC.fbs", FixtureProvider, "IQEngine", ti.BatchID)...)
	reads = append(reads, c.summaries(gpPeer, iqcSigmfPeer)...)
	return []Shape{covShape(class, "storeBatch tagged: writes", "OMM.fbs", writes...),
		laneBytes(covShape(class, "storeBatch tagged: read back", "OMM.fbs", reads...), "", c39U6, "CAT.fbs")}
}

// exportOf exports a window into the class's scratch directory.
func exportOf(s *storage.FlatSQLStore, name string, q storage.IndexedRecordQuery) ([]Row, error) {
	dir := filepath.Join(scratchDir, "export-"+name)
	_ = os.RemoveAll(dir)
	exp, err := s.ExportDatasetWindow(dir, q)
	if err != nil {
		return nil, err
	}
	return exportRows(exp)
}

// X03: the first write of a type the store does not hold (PNM: its derived
// files are created then, C-32), through StoreWithSourceTags (NEW with a
// licence, DUP, RETAG), Store (storeOne untagged, and a COPY from a second
// peer) and StoreBatch; plus StoreWithSourceTags of an IQC capture fetched
// again (IDENT_DUP: the held CID is returned, f4CIDAt) and of an OMM held
// CID; every returned CID compared.
func (c *cov) x03(omm, iqc [][]byte) []Shape {
	const class = "X03"
	ommC := cidsOf(omm[:4])
	p := []([]byte){buildPNM(1, ommC[0]), buildPNM(2, ommC[1]), buildPNM(3, ommC[2]), buildPNM(4, ommC[3])}
	t1 := covTags("coverage-pnm", "pnm-1")
	t2 := covTags("coverage-pnm", "pnm-2")
	r1, _ := RestampIQC(iqc[300], 904)
	r2, _ := RestampIQC(iqc[300], 905)
	ti := plainTags("IQEngine", "iqc-cov-single")
	to := plainTags("celestrak-gp", "OMM-cov-single")
	writes := []Call{
		c.store("StoreWithSourceTags PNM new, licence", "PNM.fbs", p[0], covStorePeer, covSig, &t1),
		c.store("StoreWithSourceTags PNM again", "PNM.fbs", p[0], covStorePeer, covSig, &t1),
		c.store("StoreWithSourceTags PNM new batch", "PNM.fbs", p[0], covStorePeer, covSig, &t2),
		c.store("Store PNM untagged", "PNM.fbs", p[1], covStorePeer, nil, nil),
		c.store("Store PNM untagged again", "PNM.fbs", p[1], covStorePeer, nil, nil),
		c.store("Store PNM held, second peer", "PNM.fbs", p[1], covPeer2, covSig, nil),
		c.storeBatch("StoreBatch PNM", "PNM.fbs", p[2:], covStorePeer, nil, nil),
		c.store("StoreWithSourceTags IQC capture again", "IQC.fbs", r1, iqcSigmfPeer, nil, &ti),
		c.store("StoreWithSourceTags IQC capture again, other stamp", "IQC.fbs", r2, iqcSigmfPeer, nil, &ti),
		c.store("Store IQC capture again, untagged", "IQC.fbs", r2, iqcSigmfPeer, nil, nil),
		c.store("StoreWithSourceTags OMM held, new batch", "OMM.fbs", omm[40], gpPeer, nil, &to),
		c.store("StoreWithSourceTags OMM held, same batch", "OMM.fbs", omm[41], gpPeer, nil, &[]storage.SourceTags{b052Tags(c.in)}[0]),
	}
	pc := cidsOf(p)
	reads := []Call{
		c.gets("GetRecord PNM", "PNM.fbs", pc),
		c.tagsOf("tags PNM", "PNM.fbs", pc),
		c.refsOf("refs PNM", "PNM.fbs", pc, nil),
		c.refsOf("refs PNM second peer", "PNM.fbs", pc[1:2], func(r *storage.RawRecordRef) { r.PeerID = covPeer2 }),
		c.refsOf("refs PNM batch pnm-1", "PNM.fbs", pc[:1], func(r *storage.RawRecordRef) { r.BatchID = t1.BatchID }),
		c.rawQuery("cursor PNM", storage.RawRecordQuery{SchemaName: "PNM.fbs", UseRowIDCursor: true, Limit: 20}, true),
		c.rawQuery("cursor PNM file id", storage.RawRecordQuery{SchemaName: "PNM.fbs", UseRowIDCursor: true, Limit: 20, SyncFilter: "FILE_ID = '$OMM'"}, false),
		c.recordsCall("window PNM", "PNM.fbs", func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			return s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "PNM.fbs", Limit: 20})
		}),
		c.rowsCall("index page PNM", "PNM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			rows, total, err := s.RecordIndexPage(storage.RecordIndexPageQuery{SchemaName: "PNM.fbs", Limit: 20})
			if err != nil {
				return nil, err
			}
			out := []Row{ValueRow("total", i64(total))}
			for _, r := range rows {
				out = append(out, ValueRow("cid", r.CID, "norad", i64p(r.NoradCatID), "epoch", i64p(r.EpochUnix)))
			}
			return out, nil
		}),
		c.gets("GetRecord IQC captures again", "IQC.fbs", cidsOf([][]byte{r1, r2, iqc[300]})),
		c.tagsOf("tags IQC held", "IQC.fbs", cidsOf([][]byte{iqc[300]})),
		c.tagsOf("tags OMM held", "OMM.fbs", cidsOf([][]byte{omm[40], omm[41]})),
	}
	reads = append(reads, c.typeReads("PNM.fbs")...)
	reads = append(reads, c.laneReads("PNM.fbs", FixtureProvider, "coverage-pnm", "")...)
	reads = append(reads, c.laneReads("IQC.fbs", FixtureProvider, "IQEngine", ti.BatchID)...)
	reads = append(reads, c.summaries(covStorePeer, covPeer2)...)
	return []Shape{covShape(class, "first write of a type; single writes: writes", "PNM.fbs", writes...),
		covShape(class, "first write of a type; single writes: read back", "PNM.fbs", reads...)}
}

// X04: a sealed standard (KMF: KEY_BYTES encrypted, C-25): NEW tagged,
// storeOne untagged, RETAG, UpsertSourceTags of the sealed held record,
// Delete; the opened bytes (GetRecord, QueryRawRecords) and the sealed ones
// (refs, pages: "sealed") read back.
func (c *cov) x04() []Shape {
	const class = "X04"
	k := [][]byte{buildKMF(1), buildKMF(2), buildKMF(3)}
	kc := cidsOf(k)
	t1 := covTags("coverage-kmf", "kmf-1")
	t2 := covTags("coverage-kmf", "kmf-2")
	t3 := plainTags("coverage-kmf", "kmf-3")
	writes := []Call{
		c.storeBatch("StoreBatchWithSourceTags KMF 2", "KMF.fbs", k[:2], covPeer, nil, &t1),
		c.store("Store KMF untagged", "KMF.fbs", k[2], covPeer, covSig, nil),
		c.storeBatch("StoreBatchWithSourceTags KMF new batch", "KMF.fbs", k[:1], covPeer, nil, &t2),
		c.errOnly("UpsertSourceTags KMF sealed held", func(s *storage.FlatSQLStore) error { return s.UpsertSourceTags("KMF.fbs", kc[2], t3) }),
	}
	reads := []Call{
		c.gets("GetRecord KMF (opened)", "KMF.fbs", kc),
		c.tagsOf("tags KMF", "KMF.fbs", kc),
		c.refsOf("refs KMF (stored bytes)", "KMF.fbs", kc, nil),
		c.rawQuery("QueryRawRecords KMF (opened)", storage.RawRecordQuery{SchemaName: "KMF.fbs", UseRowIDCursor: true, Limit: 10}, true),
		c.rawQuery("QueryRawRecordRefs KMF", storage.RawRecordQuery{SchemaName: "KMF.fbs", UseRowIDCursor: true, Limit: 10}, false),
		c.rawQuery("QueryRawRecords KMF newest (opened)", storage.RawRecordQuery{SchemaName: "KMF.fbs", Limit: 10}, true),
		c.rawQuery("QueryRawRecordRefs KMF lane kmf-3", storage.RawRecordQuery{SchemaName: "KMF.fbs", BatchID: t3.BatchID, UseRowIDCursor: true, Limit: 10}, false),
		c.rowsCall("QueryAllBounded KMF", "KMF.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			l, err := s.QueryAllBounded("KMF.fbs", 10, 1<<20)
			return bytesRows(l), err
		}),
	}
	reads = append(reads, c.typeReads("KMF.fbs")...)
	reads = append(reads,
		c.errOnly("Delete KMF", func(s *storage.FlatSQLStore) error { return s.Delete("KMF.fbs", kc[1]) }),
		c.gets("GetRecord KMF after the delete", "KMF.fbs", kc))
	reads = append(reads, c.typeReads("KMF.fbs")...)
	reads = append(reads, c.summaries(covPeer)...)
	back := laneBytes(covShape(class, "sealed standard: read back", "KMF.fbs", reads...), "", c39U5, "KMF.fbs")
	// C-39 E7: the two records of one batch share a source timestamp, so
	// format 1's order between them is arbitrary; the rows are the same.
	back.Policy.Calls = append(back.Policy.Calls, CallRuling{Call: "QueryAllBounded KMF", Why: c39E7, Fields: []string{"frame"}, SameSet: true})
	return []Shape{covShape(class, "sealed standard: writes", "KMF.fbs", writes...), back}
}

// X05: a record above the largest storable size (C-6: write request area −
// 64 KiB; SDN accepts records up to max_record_bytes, 10 MB) in a batch and
// alone. Format 4 rejects it per record (contract C-6, an intended
// difference); the batch's other records must land as on format 1.
func (c *cov) x05(omm [][]byte) []Shape {
	const class = "X05"
	a := CloneOf("OMM", omm[50], 905, nil)
	b := CloneOf("OMM", omm[51], 905, nil)
	big := append(CloneOf("OMM", omm[52], 905, nil), make([]byte, 9<<20)...)
	t := plainTags("celestrak-gp", "OMM-cov-big")
	tb := plainTags("celestrak-gp", "OMM-cov-big-single")
	bigCall := func(name string, fn func(s *storage.FlatSQLStore) (string, error)) Call {
		return c.writeValue(name, "OMM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			v, err := fn(s)
			kind := ""
			if err != nil {
				kind = errKind(err)
				fmt.Printf("coverage: %s: %v\n", name, err)
			}
			return ValueRow("big_value", v, "big_err", kind), nil
		})
	}
	calls := []Call{
		bigCall("StoreBatchWithSourceTags OMM with an oversized record", func(s *storage.FlatSQLStore) (string, error) {
			n, err := s.StoreBatchWithSourceTags("OMM.fbs", [][]byte{a, big, b}, gpPeer, nil, t)
			return strconv.Itoa(n), err
		}),
		bigCall("StoreWithSourceTags OMM oversized", func(s *storage.FlatSQLStore) (string, error) {
			return s.StoreWithSourceTags("OMM.fbs", big, gpPeer, nil, tb)
		}),
		bigCall("GetRecord oversized", func(s *storage.FlatSQLStore) (string, error) {
			r, err := s.GetRecord("OMM.fbs", storage.ComputeCID(big))
			if err != nil {
				return "", err
			}
			return digest(r.Data), nil
		}),
		c.gets("GetRecord the batch's other records", "OMM.fbs", cidsOf([][]byte{a, b})),
		c.tagsOf("tags the batch's other records", "OMM.fbs", cidsOf([][]byte{a, b})),
		c.refsOf("refs the batch's other records", "OMM.fbs", cidsOf([][]byte{a, b}), nil),
	}
	sh := covShape(class, "oversized record (C-6)", "OMM.fbs", calls...)
	accept(&sh, "C-6: format 4 rejects a record above the write slot's request area − 64 KiB (P4_REJ_TOO_LARGE); format 1 stores it",
		"big_value", "big_err")
	return []Shape{sh}
}

// X06: StoreRoutedByProducer (the storefront's DPM/PNM publication path: a
// raw peer id, the producer token derived from it, C-13; no tags; a
// signature): NEW, again (DUP), a held CID (COPY) and a type it holds none
// of; the copies by that peer read back.
func (c *cov) x06(omm [][]byte) []Shape {
	const class = "X06"
	n := CloneOf("OMM", omm[60], 906, nil)
	pnm := buildPNM(9, storage.ComputeCID(omm[60]))
	ids := cidsOf([][]byte{n, omm[61], pnm})
	routed := func(name, schema string, rec []byte) Call {
		return c.writeValue(name, schema, func(s *storage.FlatSQLStore) (Row, error) {
			id, err := s.StoreRoutedByProducer(schema, rec, covStorePeer, covSig)
			return ValueRow("cid", id), err
		})
	}
	calls := []Call{
		routed("StoreRoutedByProducer OMM new", "OMM.fbs", n),
		routed("StoreRoutedByProducer OMM again", "OMM.fbs", n),
		routed("StoreRoutedByProducer OMM held", "OMM.fbs", omm[61]),
		routed("StoreRoutedByProducer PNM new type", "PNM.fbs", pnm),
		c.gets("GetRecord OMM", "OMM.fbs", ids[:2]),
		c.gets("GetRecord PNM", "PNM.fbs", ids[2:]),
		c.refsOf("refs OMM by the storefront peer", "OMM.fbs", ids[:2], func(r *storage.RawRecordRef) { r.PeerID = covStorePeer }),
		c.refsOf("refs PNM by the storefront peer", "PNM.fbs", ids[2:], func(r *storage.RawRecordRef) { r.PeerID = covStorePeer }),
		c.tagsOf("tags OMM", "OMM.fbs", ids[:2]),
		c.rawQuery("cursor OMM storefront peer", storage.RawRecordQuery{SchemaName: "OMM.fbs", PeerID: covStorePeer, UseRowIDCursor: true, Limit: 20}, false),
		c.rowsCall("QueryRoutedByProducer storefront peer", "", func(s *storage.FlatSQLStore) ([]Row, error) {
			list, err := s.QueryRoutedByProducer(covStorePeer, 20)
			if err != nil {
				return nil, err
			}
			var rows []Row
			for _, r := range list {
				rows = append(rows, c.sc.canon(ValueRow("cid", r.CID, "producer", r.ProducerID, "standard", r.Standard, "peer", r.PeerID, "ts", i64(r.Timestamp)), ""))
			}
			return sortRows(rows), nil
		}),
	}
	calls = append(calls, c.summaries(covStorePeer)...)
	return []Shape{covShape(class, "StoreRoutedByProducer", "OMM.fbs", calls...)}
}

// X07: importDatasetShardChunk (ImportDatasetShardFromFilesContext,
// ImportDatasetShard): index records with provider and source, with a
// source and no provider (the serving peer substituted), with a relayed
// producer peer, untagged (the nil group), held CIDs (DUP/COPY), a
// sha256-hex CID; the import repeated; an index whose CID does not match
// its bytes.
func (c *cov) x07(omm [][]byte) []Shape {
	const class = "X07"
	a := clonesOf("OMM", omm, 70, 73, 907)
	bb := clonesOf("OMM", omm, 73, 75, 907)
	u := clonesOf("OMM", omm, 75, 77, 907)
	held := omm[77:79]
	hexRec := CloneOf("OMM", omm[79], 907, nil)
	ta := plainTags("celestrak-gp", "OMM-cov-import-a")
	tb := storage.SourceTags{SourceName: "celestrak-gp", BatchID: "OMM-cov-import-b", ProducerPeerID: covRelay}
	// The sha256-hex record has a batch of its own: format 4 keeps it under
	// its CIDv1 (§3.8 (1)), so the lane reads of batch a stay free of it.
	th := plainTags("celestrak-gp", "OMM-cov-import-hex")
	var recs []storage.DatasetExportRecord
	add := func(list [][]byte, t storage.SourceTags) {
		for _, r := range list {
			recs = append(recs, storage.DatasetExportRecord{CID: storage.ComputeCID(r), Data: r, SourceTags: t})
		}
	}
	add(a, ta)
	add(bb, tb)
	add(u, storage.SourceTags{})
	add(held, b052Tags(c.in))
	add([][]byte{hexRec}, th)
	hexCID := sha256.Sum256(hexRec)
	var shard, index, badIndex string
	build := c.errOnly("build the shard", func(s *storage.FlatSQLStore) error {
		var err error
		shard, index, badIndex, err = buildImportShard(recs, storage.ComputeCID(hexRec), hex.EncodeToString(hexCID[:]))
		return err
	})
	imp := func(name, provider string, idx *string) Call {
		return c.writeRows(name, "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			n, ix, err := s.ImportDatasetShardFromFilesContext(context.Background(), shard, *idx, provider)
			row := ValueRow("n", strconv.Itoa(n), "index_records", strconv.Itoa(func() int {
				if ix == nil {
					return -1
				}
				return len(ix.Records)
			}()))
			if err != nil {
				return []Row{row, errRow(name, err)}, nil
			}
			return []Row{row}, nil
		})
	}
	impBytes := c.writeRows("ImportDatasetShard (bytes) again", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		sb, err := os.ReadFile(shard)
		if err != nil {
			return nil, err
		}
		ib, err := os.ReadFile(index)
		if err != nil {
			return nil, err
		}
		n, _, err := s.ImportDatasetShard(sb, ib, covPeer2)
		return []Row{ValueRow("n", strconv.Itoa(n))}, err
	})
	all := append(append(append(cidsOf(a), cidsOf(bb)...), cidsOf(u)...), cidsOf(held)...)
	// The sha256-hex record keeps the identity its index names (format 1
	// stores it under that text, manifest.go): read by both names.
	hexIDs := []string{hex.EncodeToString(hexCID[:]), storage.ComputeCID(hexRec)}
	calls := []Call{
		build,
		imp("import", covProvider, &index),
		imp("import again", covProvider, &index),
		impBytes,
		c.gets("GetRecord imported", "OMM.fbs", all),
		c.tagsOf("tags imported", "OMM.fbs", all),
		c.refsOf("refs imported, the provider's copies", "OMM.fbs", all, func(r *storage.RawRecordRef) { r.PeerID = covProvider }),
		c.refsOf("refs imported, by their tags", "OMM.fbs", cidsOf(bb), func(r *storage.RawRecordRef) { r.BatchID, r.ProducerPeerID = tb.BatchID, covRelay }),
		c.gets("GetRecord the sha256-hex record", "OMM.fbs", hexIDs),
		c.tagsOf("tags the sha256-hex record", "OMM.fbs", hexIDs),
		c.refsOf("refs the sha256-hex record", "OMM.fbs", hexIDs[:1], nil),
		c.rawQuery("cursor provider peer", storage.RawRecordQuery{SchemaName: "OMM.fbs", PeerID: covProvider, UseRowIDCursor: true, Limit: 50}, false),
		imp("import with a CID that does not match its bytes", covProvider, &badIndex),
	}
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, "celestrak-gp", ta.BatchID)...)
	calls = append(calls, c.laneReads("OMM.fbs", covProvider, "celestrak-gp", tb.BatchID)...)
	calls = append(calls, c.summaries(covProvider, covPeer2)...)
	sh := covShape(class, "dataset shard import", "OMM.fbs", calls...)
	// A read naming the record by either text compares its CIDv1 (format
	// 1's rows read the hex text as its alias); the reads by each name answer
	// on the other format by the other name (§3.8 (1)).
	sh.Policy.CIDAliases = map[string]string{hexIDs[0]: hexIDs[1]}
	for _, call := range []string{"GetRecord the sha256-hex record", "tags the sha256-hex record", "refs the sha256-hex record"} {
		sh = rule(sh, call, c38Hex)
	}
	// The second provider's import is new to its feed (C-38 (3)); batch b's
	// records are then held by both providers' feeds, and a window of the
	// first provider's lane projects that feed's tag (C-38 (5)), where format
	// 1 projects the second import's, the record's newest.
	sh = rule(sh, "ImportDatasetShard (bytes) again", c38PerFeed, "n")
	sh = rule(sh, "SchemaDateRanges", c38PerFeed, "n") // batch b's two records, once per feed
	sh = rule(sh, "lane window OMM.fbs "+covProvider+"/celestrak-gp/"+tb.BatchID, c38OwnFeed, "provider")
	return []Shape{sh,
		c12Shape(class, "dataset shard import: the copy format 1 does not serve", "OMM.fbs",
			c.refsOf("refs imported, the second import's copies", "OMM.fbs", all, func(r *storage.RawRecordRef) { r.PeerID = covPeer2 }))}
}

// buildImportShard writes the shard and its index (ExportDatasetRecords) in
// the class's scratch directory, with one record's CID in the legacy
// sha256-hex form, and a second index whose first record names another CID.
func buildImportShard(recs []storage.DatasetExportRecord, hexOf, hexCID string) (shard, index, bad string, err error) {
	dir := filepath.Join(scratchDir, "import")
	_ = os.RemoveAll(dir)
	exp, err := storage.ExportDatasetRecords(dir, storage.IndexedRecordQuery{SchemaName: "OMM.fbs"}, recs)
	if err != nil {
		return "", "", "", err
	}
	b, err := os.ReadFile(exp.IndexPath)
	if err != nil {
		return "", "", "", err
	}
	var idx storage.DatasetExportIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return "", "", "", err
	}
	for i := range idx.Records {
		if idx.Records[i].CID == hexOf {
			idx.Records[i].CID = hexCID
		}
	}
	index = filepath.Join(dir, "coverage.index.json")
	if err := writeJSONFile(index, idx); err != nil {
		return "", "", "", err
	}
	idx.Records[0].CID = idx.Records[1].CID
	bad = filepath.Join(dir, "coverage-bad.index.json")
	if err := writeJSONFile(bad, idx); err != nil {
		return "", "", "", err
	}
	return exp.ShardPath, index, bad, nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// X08: UpsertSourceTags: a new tag on a held CID (RETAG), the same tag with a
// new url (DUP + url), a CID two producers hold (the lowest-pid copy's
// bytes and peer), a tag without a source, a miss, a non-canonical CID, an
// unknown type; the tags and lanes read back.
func (c *cov) x08() []Shape {
	const class = "X08"
	b052 := c.in.B052CIDs
	iqcHit := c.hits["IQC.fbs"][5]
	t1 := covTags("celestrak-gp", "OMM-cov-retag")
	t1.ProducerPeerID = ""
	t1url := t1
	t1url.SourceURL += "&v=2"
	ti := plainTags("IQEngine", "iqc-cov-retag")
	up := func(name, schema, id string, t storage.SourceTags) Call {
		return c.errOnly(name, func(s *storage.FlatSQLStore) error { return s.UpsertSourceTags(schema, id, t) })
	}
	calls := []Call{
		up("UpsertSourceTags OMM held, new batch", "OMM.fbs", b052[0], t1),
		up("UpsertSourceTags OMM held, new url", "OMM.fbs", b052[0], t1url),
		up("UpsertSourceTags OMM held, second record", "OMM.fbs", b052[1], t1),
		up("UpsertSourceTags IQC held twice", "IQC.fbs", iqcHit, ti),
		up("UpsertSourceTags OMM no source", "OMM.fbs", b052[2], storage.SourceTags{ProviderID: FixtureProvider, BatchID: "x"}),
		up("UpsertSourceTags OMM fixture's own tag", "OMM.fbs", b052[3], b052Tags(c.in)),
		c.tagsOf("tags OMM", "OMM.fbs", b052[:5]),
		c.tagsOf("tags IQC", "IQC.fbs", []string{iqcHit}),
		c.refsOf("refs OMM new batch", "OMM.fbs", b052[:2], func(r *storage.RawRecordRef) { r.BatchID = t1.BatchID }),
		c.refsOf("refs OMM fixture batch", "OMM.fbs", b052[:2], func(r *storage.RawRecordRef) { r.BatchID = OMMLatestBatch }),
		c.refsOf("refs IQC new batch, each copy", "IQC.fbs", []string{iqcHit, iqcHit}, nil),
		c.refsOf("refs IQC new batch", "IQC.fbs", []string{iqcHit}, func(r *storage.RawRecordRef) { r.BatchID = ti.BatchID }),
		c.gets("GetRecord IQC", "IQC.fbs", []string{iqcHit}),
	}
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, "celestrak-gp", t1.BatchID)...)
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, "celestrak-gp", OMMLatestBatch)...)
	calls = append(calls, c.laneReads("IQC.fbs", FixtureProvider, "IQEngine", ti.BatchID)...)
	calls = append(calls, c.summaries()...)
	// Then CIDs not held (C-39 U2: format 4 answers not found; format 1
	// writes a dangling tag): the reads above compare strictly, these after
	// them (" #2") show only the dangling tags' effect.
	calls = append(calls,
		up("UpsertSourceTags OMM miss", "OMM.fbs", c.miss["OMM.fbs"][0], t1),
		up("UpsertSourceTags OMM non-canonical", "OMM.fbs", strings.ToUpper(b052[4]), t1),
		up("UpsertSourceTags unknown type", "XYZ.fbs", b052[4], t1))
	calls = append(calls, c.tagsOf("tags OMM", "OMM.fbs", b052[:5]))
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, "celestrak-gp", t1.BatchID)...)
	calls = append(calls, c.summaries()...)
	sh := covShape(class, "UpsertSourceTags", "OMM.fbs", calls...)
	for _, call := range []string{"UpsertSourceTags OMM miss", "UpsertSourceTags OMM non-canonical", "UpsertSourceTags unknown type"} {
		sh = rule(sh, call, c39U2, "ok", "err")
	}
	return []Shape{u2Rows(sh, " #2")}
}

// X09: Delete: a CID two producers hold (every copy goes), a single-copy
// CAT record, an OMM record an EPOCH read answers (then the next nearest),
// a miss, a non-canonical CID, an unknown type; then counts, lanes, windows,
// index pages, epochs, SQL and summaries.
func (c *cov) x09() []Shape {
	const class = "X09"
	coverageNeedsSQL[class] = true
	iqcHit := c.hits["IQC.fbs"][1]
	catHit := c.hits["CAT.fbs"][2]
	var nearest string
	at := time.Unix(r16At, 0).UTC()
	nq := storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: at, NoradCatID: u32(25544), Limit: 5}
	epochCall := func(name string, write bool) Call {
		call := c.rowsCall(name, "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			n, err := s.CountEpochRecords(nq)
			if err != nil {
				return nil, err
			}
			ms, err := s.QueryEpochRecords(nq)
			if err != nil {
				return nil, err
			}
			if nearest == "" && len(ms) > 0 && ms[0].Record != nil {
				nearest = ms[0].Record.CID
			}
			a, _ := matchesAnswer(name, ms, n)
			return a.Rows, nil
		})
		call.Write = write
		return call
	}
	del := func(name, schema string, id func() string) Call {
		return c.errOnly(name, func(s *storage.FlatSQLStore) error { return s.Delete(schema, id()) })
	}
	fixed := func(v string) func() string { return func() string { return v } }
	sql := func(q string, params ...any) Call {
		return c.rowsCall("SQL "+q, "", func(s *storage.FlatSQLStore) ([]Row, error) {
			payload, _, _, err := s.QuerySandboxedJSON(q, flatsqlrt5m, params...)
			if err != nil {
				return nil, err
			}
			return JSONRows(payload)
		})
	}
	stream := func(q string, params ...any) Call {
		return c.rowsCall("SQL stream "+q, "", func(s *storage.FlatSQLStore) ([]Row, error) {
			st, err := s.QuerySandboxedStream(q, flatsqlrt5m, params...)
			if err != nil {
				return nil, err
			}
			rows, err := FrameRows(st.Bytes)
			return sortRows(rows), err
		})
	}
	calls := []Call{
		epochCall("epoch nearest norad 25544 before", true),
		del("Delete IQC held by two producers", "IQC.fbs", fixed(iqcHit)),
		del("Delete CAT", "CAT.fbs", fixed(catHit)),
		del("Delete OMM the nearest record", "OMM.fbs", func() string { return nearest }),
		del("Delete OMM miss", "OMM.fbs", fixed(c.miss["OMM.fbs"][0])),
		del("Delete OMM non-canonical", "OMM.fbs", fixed(strings.ToUpper(c.hits["OMM.fbs"][9]))),
		del("Delete unknown type", "XYZ.fbs", fixed(c.hits["OMM.fbs"][9])),
		del("Delete IQC again", "IQC.fbs", fixed(iqcHit)),
		c.gets("GetRecord deleted", "IQC.fbs", []string{iqcHit}),
		c.gets("GetRecord deleted CAT", "CAT.fbs", []string{catHit}),
		c.tagsOf("tags deleted", "IQC.fbs", []string{iqcHit}),
		c.refsOf("refs deleted", "IQC.fbs", []string{iqcHit}, nil),
		c.rawQuery("cursor IQC cid", storage.RawRecordQuery{SchemaName: "IQC.fbs", CID: iqcHit, UseRowIDCursor: true, Limit: 5}, false),
		epochCall("epoch nearest norad 25544 after", false),
		c.rowsCall("epoch coverage norad 25544", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			bs, err := s.QueryEpochCoverage(storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileCoverage, NoradCatID: u32(25544)})
			if err != nil {
				return nil, err
			}
			var rows []Row
			for _, b := range bs {
				rows = append(rows, ValueRow("day", b.Day, "n", i64(b.Count), "oldest", unixOf(b.OldestEpoch), "newest", unixOf(b.NewestEpoch)))
			}
			return rows, nil
		}),
		c.recordsCall("window IQC by cid", "IQC.fbs", func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			return s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "IQC.fbs", OrderByCID: true, Limit: 10})
		}),
		c.recordsCall("window CAT object", "CAT.fbs", func(s *storage.FlatSQLStore) ([]*storage.Record, error) {
			return s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", NoradCatID: catNoradOf(c.sets, catHit), Limit: 10})
		}),
		sql("SELECT COUNT(*) FROM IQC"),
		sql("SELECT COUNT(*) FROM CAT"),
		stream("SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?1", int64(25544)),
	}
	calls = append(calls, c.laneReads("IQC.fbs", FixtureProvider, "IQEngine", iqcBatch)...)
	calls = append(calls, c.typeReads("IQC.fbs")...)
	calls = append(calls, c.typeReads("CAT.fbs")...)
	calls = append(calls, c.summaries(iqcPeer, iqcSigmfPeer, gpPeer)...)
	return []Shape{covShape(class, "Delete", "IQC.fbs", calls...)}
}

// catNoradOf is the NORAD id of a CAT CID among the inputs (nil when absent).
func catNoradOf(sets map[string][][]byte, id string) *uint32 {
	for _, name := range []string{InputCATCSV, InputCATSatcat} {
		for _, r := range sets[name] {
			if storage.ComputeCID(r) == id {
				n := catNorad(r)
				return &n
			}
		}
	}
	return u32(0)
}

// X10: ReconcileSourceBatch (count only, apply, again, an empty lane, no
// keep) on a lane two producers write, one record also tagged in another
// lane; the lane and its records read back.
func (c *cov) x10(omm [][]byte) []Shape {
	const class = "X10"
	src := "celestrak-gp-cov-reconcile"
	recs := clonesOf("OMM", omm, 100, 106, 910)
	ids := cidsOf(recs)
	ta, tb := plainTags(src, "OMM-cov-rec-a"), plainTags(src, "OMM-cov-rec-b")
	other := plainTags("celestrak-gp", "OMM-cov-rec-other")
	rec := func(name, source, keep string, apply bool) Call {
		return c.writeValue(name, "OMM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			r, err := s.ReconcileSourceBatch("OMM.fbs", FixtureProvider, source, keep, apply)
			b, _ := json.Marshal(r)
			return flatJSON(jsonObject(b)), err
		})
	}
	calls := []Call{
		c.storeBatch("lane batch a, first producer", "OMM.fbs", recs[:4], gpPeer, nil, &ta),
		c.storeBatch("lane batch b, second producer", "OMM.fbs", recs[3:6], covPeer, nil, &tb),
		c.storeBatch("another lane", "OMM.fbs", recs[:1], gpPeer, nil, &other),
		rec("ReconcileSourceBatch count only", src, tb.BatchID, false),
		rec("ReconcileSourceBatch apply", src, tb.BatchID, true),
		rec("ReconcileSourceBatch apply again", src, tb.BatchID, true),
		rec("ReconcileSourceBatch empty lane", "no-such-source", "x", true),
		rec("ReconcileSourceBatch no keep", src, "", true),
		c.gets("GetRecord lane records", "OMM.fbs", ids),
		c.tagsOf("tags lane records", "OMM.fbs", ids),
		c.refsOf("refs lane records", "OMM.fbs", ids[:1], nil),
	}
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, src, ta.BatchID)...)
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, src, tb.BatchID)...)
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, src, "")...)
	calls = append(calls, c.summaries(gpPeer, covPeer)...)
	// recs[0], also tagged in celestrak-gp, is new to that feed (C-38 (3)),
	// and leaves the reconciled feed while celestrak-gp keeps it.
	sh := rule(covShape(class, "ReconcileSourceBatch", "OMM.fbs", calls...), "another lane", c38PerFeed, "n")
	return []Shape{rule(sh, "ReconcileSourceBatch apply", c38PerFeed, "deleted")}
}

// X11: SupersedeSourceBatches (MPE, two producers, one record surviving
// under another lane's tag, again as a no-op, an empty lane, no keep) and
// RetainNewestSourceBatch without a ledger (CAT); result counts compared;
// RefreshSourceBatchSummary of the kept batch.
func (c *cov) x11(mpe [][]byte) []Shape {
	const class = "X11"
	src := "celestrak-gp-cov-supersede"
	recs := clonesOf("MPE", mpe, 100, 107, 911)
	ids := cidsOf(recs)
	ta, tb, tc := plainTags(src, "MPE-cov-sup-a"), plainTags(src, "MPE-cov-sup-b"), plainTags(src, "MPE-cov-sup-c")
	other := plainTags("celestrak-gp", "MPE-cov-sup-other")
	catSrc := "celestrak-satcat-cov"
	catA := clonesOf("CAT", c.sets[InputCATSatcat], 0, 3, 911)
	catB := clonesOf("CAT", c.sets[InputCATSatcat], 0, 3, 912)
	sup := func(name, schema, source, keep string) Call {
		return c.writeValue(name, schema, func(s *storage.FlatSQLStore) (Row, error) {
			r, err := s.SupersedeSourceBatches(schema, FixtureProvider, source, keep)
			return ValueRow("tags", i64(r.TagsDeleted), "records", i64(r.RecordsDeleted), "files", strconv.Itoa(r.FilesDeleted), "keep", r.KeepBatch), err
		})
	}
	calls := []Call{
		c.storeBatch("lane batch a, first producer", "MPE.fbs", recs[:4], gpPeer, nil, &ta),
		c.storeBatch("lane batch b, second producer", "MPE.fbs", recs[2:6], covPeer, nil, &tb),
		c.storeBatch("lane batch c", "MPE.fbs", recs[6:7], gpPeer, nil, &tc),
		c.storeBatch("another lane", "MPE.fbs", recs[1:2], gpPeer, nil, &other),
		sup("SupersedeSourceBatches keep b", "MPE.fbs", src, tb.BatchID),
		sup("SupersedeSourceBatches keep b again", "MPE.fbs", src, tb.BatchID),
		sup("SupersedeSourceBatches empty lane", "MPE.fbs", "no-such-source", "x"),
		sup("SupersedeSourceBatches no keep", "MPE.fbs", src, ""),
		c.errOnly("RefreshSourceBatchSummary kept batch", func(s *storage.FlatSQLStore) error {
			return s.RefreshSourceBatchSummary("MPE.fbs", FixtureProvider, src, tb.BatchID)
		}),
		c.errOnly("RefreshSourceBatchSummary no batch", func(s *storage.FlatSQLStore) error {
			return s.RefreshSourceBatchSummary("MPE.fbs", FixtureProvider, src, "")
		}),
		c.gets("GetRecord lane records", "MPE.fbs", ids),
		c.tagsOf("tags lane records", "MPE.fbs", ids),
		c.storeBatch("CAT lane batch a", "CAT.fbs", catA, gpPeer, nil, &[]storage.SourceTags{plainTags(catSrc, "CAT-cov-a")}[0]),
		c.storeBatch("CAT lane batch b", "CAT.fbs", catB, gpPeer, nil, &[]storage.SourceTags{plainTags(catSrc, "CAT-cov-b")}[0]),
		c.writeValue("RetainNewestSourceBatch CAT import b (no ledger)", "CAT.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			r, keep, err := s.RetainNewestSourceBatch("CAT.fbs", FixtureProvider, catSrc, storage.RetainCandidate{BatchID: "CAT-cov-b", PublishedAt: time.Unix(1790000000, 0)})
			return ValueRow("tags", i64(r.TagsDeleted), "records", i64(r.RecordsDeleted), "files", strconv.Itoa(r.FilesDeleted), "kept", keep), err
		}),
		c.gets("GetRecord CAT lane", "CAT.fbs", append(cidsOf(catA), cidsOf(catB)...)),
	}
	calls = append(calls, c.laneReads("MPE.fbs", FixtureProvider, src, "")...)
	calls = append(calls, c.laneReads("MPE.fbs", FixtureProvider, src, tb.BatchID)...)
	calls = append(calls, c.laneReads("MPE.fbs", FixtureProvider, "celestrak-gp", other.BatchID)...)
	calls = append(calls, c.laneReads("CAT.fbs", FixtureProvider, catSrc, "")...)
	calls = append(calls, c.summaries(gpPeer, covPeer)...)
	// recs[1], also tagged in celestrak-gp, is new to that feed (C-38 (3)),
	// and leaves the superseded feed while celestrak-gp keeps it.
	sh := rule(covShape(class, "SupersedeSourceBatches and RetainNewestSourceBatch", "MPE.fbs", calls...), "another lane", c38PerFeed, "n")
	sh = rule(sh, "SupersedeSourceBatches keep b", c38PerFeed, "records")
	// C-39 U3: format 1's RefreshSourceBatchSummary of the kept batch
	// restamps the supersede lane's times; format 4's verb is a no-op.
	for _, read := range []string{"lane snapshot ", "lane head "} {
		sh = rule(sh, read+"MPE.fbs "+FixtureProvider+"/"+src+"/", c39U3, "max_updated", "max_created")
	}
	for _, call := range []string{"SourceBatchProgress", "ProducerSourceProgress"} {
		sh.Policy.Calls = append(sh.Policy.Calls,
			CallRuling{Call: call, Why: c39U3, Fields: []string{"LastSeenUnix", "UpdatedAtUnix"}, Standard: "MPE.fbs", Source: src})
	}
	return []Shape{sh}
}

// X12: the lane ledger: a servable publication (its shard at the canonical
// path), NewestServableSourceBatch, RetainNewestSourceBatch with a ledger
// (laneHasOtherUnledgeredBatch: not stuck, then stuck), the retention plan
// (laneBatchHoldsRecords, before and after the supersede), and the index
// repair from the shard (sourceTagsForCIDs).
func (c *cov) x12(omm [][]byte) []Shape {
	const class = "X12"
	src := "celestrak-gp-cov-ledger"
	recs := clonesOf("OMM", omm, 120, 127, 912)
	ta, tb, tc := plainTags(src, "OMM-cov-led-a"), plainTags(src, "OMM-cov-led-b"), plainTags(src, "OMM-cov-led-c")
	lane := storage.DatasetPublicationLane{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: src}
	t1 := time.Unix(1790000000, 0).UTC()
	var pubA storage.DatasetShardPublication
	var placed string
	publish := c.writeRows("publish batch a (ledger row, shard at its path)", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		dir := filepath.Join(scratchDir, "ledger-a")
		_ = os.RemoveAll(dir)
		exp, err := s.ExportDatasetWindow(dir, storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: src, BatchID: ta.BatchID,
			Limit: 50000, OrderByCID: true, AllowLargeResultSet: true})
		if err != nil {
			return nil, err
		}
		pubA = storage.DatasetShardPublication{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: src, BatchID: ta.BatchID,
			QueryProfile: storage.DatasetPublicationQueryProfile, Offset: 0, Limit: 50000, RecordCount: exp.RecordCount, ByteCount: exp.ShardBytes,
			ShardCID: exp.ShardCID, IndexCID: exp.IndexCID, ShardSHA256: exp.ShardSHA256, IndexSHA256: exp.IndexSHA256, QuerySHA256: exp.QuerySHA256,
			ResultSHA256: exp.ResultSHA256, FeedSequence: 1, PublishedAt: t1}
		if placed, err = s.DatasetPublicationShardPath(pubA); err != nil {
			return nil, err
		}
		if err := copyFile(exp.ShardPath, placed); err != nil {
			return nil, err
		}
		if err := s.UpsertDatasetShardPublication(pubA); err != nil {
			return nil, err
		}
		return exportRows(exp)
	})
	servable := c.valueCall("NewestServableSourceBatch", "OMM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
		id, at, ok, pending, err := s.NewestServableSourceBatch("OMM.fbs", FixtureProvider, src)
		return ValueRow("batch", id, "at", unixOf(at), "ok", strconv.FormatBool(ok), "pending", strconv.FormatBool(pending)), err
	})
	retain := func(name string) Call {
		return c.writeValue(name, "OMM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
			r, keep, err := s.RetainNewestSourceBatch("OMM.fbs", FixtureProvider, src, storage.RetainCandidate{BatchID: tb.BatchID, PublishedAt: t1.Add(time.Hour)})
			return ValueRow("tags", i64(r.TagsDeleted), "records", i64(r.RecordsDeleted), "files", strconv.Itoa(r.FilesDeleted), "kept", keep), err
		})
	}
	plan := func(name string) Call {
		return c.rowsCall(name, "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			p, err := s.PlanDatasetPublicationRetention(lane, covProvider, storage.DatasetPublicationRetentionPolicy{KeepSeries: 1})
			if err != nil {
				return nil, err
			}
			return []Row{ValueRow("keep", strconv.Itoa(p.Keep), "kept", strings.Join(p.KeptSeries, ","), "retired", strings.Join(p.RetiredSeries, ","),
				"unpin", strconv.Itoa(len(p.Unpin)), "shared", strconv.Itoa(len(p.Shared)), "retire_rows", strconv.Itoa(len(p.RetireRows)))}, nil
		})
	}
	series := c.errOnly("record two publication series", func(s *storage.FlatSQLStore) error {
		for i, b := range []string{ta.BatchID, tb.BatchID} {
			if err := s.RecordDatasetPublicationSeries(storage.DatasetPublicationSeries{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: src,
				SeriesID: "cov-series-" + b, BatchID: b, RecordCount: 3, PublishedAt: t1.Add(time.Duration(i) * time.Minute)}, covProvider); err != nil {
				return err
			}
		}
		return nil
	})
	repair := c.rowsCall("RepairDatasetPublicationIndexFromShard batch a", "OMM.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
		dir := filepath.Join(scratchDir, "repair-a")
		_ = os.RemoveAll(dir)
		exp, err := s.RepairDatasetPublicationIndexFromShard(dir, pubA)
		if err != nil {
			return nil, err
		}
		return exportRows(exp)
	})
	calls := []Call{
		c.storeBatch("lane batch a", "OMM.fbs", recs[:3], gpPeer, nil, &ta),
		c.storeBatch("lane batch b", "OMM.fbs", recs[3:6], gpPeer, nil, &tb),
		c.storeBatch("batch a's records also in batch b", "OMM.fbs", recs[:1], gpPeer, nil, &tb),
		publish,
		servable,
		repair,
		series,
		plan("retention plan, batch a held"),
		retain("RetainNewestSourceBatch import b (ledger a; not stuck)"),
		c.storeBatch("lane batch c (unledgered)", "OMM.fbs", recs[6:7], gpPeer, nil, &tc),
		retain("RetainNewestSourceBatch import b (stuck: c unledgered)"),
		plan("retention plan, batch a gone"),
		servable,
		c.gets("GetRecord lane records", "OMM.fbs", cidsOf(recs)),
		c.tagsOf("tags lane records", "OMM.fbs", cidsOf(recs)),
		c.errOnly("remove the placed shard", func(s *storage.FlatSQLStore) error {
			if err := os.Remove(placed); placed != "" && err != nil && !os.IsNotExist(err) {
				return err // the supersede removes it with batch a (FilesDeleted)
			}
			return nil
		}),
	}
	calls = append(calls, c.laneReads("OMM.fbs", FixtureProvider, src, "")...)
	calls = append(calls, c.summaries()...)
	return []Shape{covShape(class, "lane ledger", "OMM.fbs", calls...)}
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// X13: the maintenance verbs: GarbageCollect (format 4: unsupported, C-11),
// GarbageCollectToQuota as a no-op and with no quota, RefreshSourceBatchSummary
// of a fixture lane, RebuildSourceSummaries, RebuildDerivedState,
// RebuildIndex; the summaries after them.
func (c *cov) x13() []Shape {
	const class = "X13"
	gc := covShape(class, "GarbageCollect (C-11)", "", c.writeValue("GarbageCollect 200 years", "", func(s *storage.FlatSQLStore) (Row, error) {
		n, err := s.GarbageCollect(200 * 365 * 24 * time.Hour)
		return ValueRow("n", i64(n)), err
	}))
	accept(&gc, "C-11: GarbageCollect(maxAge) returns an unsupported error on format 4, as on format 2", "n", "err")
	quota := func(name string, of func(live int64) int64) Call {
		return c.writeValue(name, "", func(s *storage.FlatSQLStore) (Row, error) {
			live, err := s.LiveRecordBytes()
			if err != nil {
				return nil, err
			}
			n, err := s.GarbageCollectToQuota(of(live))
			if err != nil {
				return nil, err
			}
			after, err := s.LiveRecordBytes()
			return ValueRow("evicted", i64(n), "unchanged", strconv.FormatBool(after == live)), err
		})
	}
	quotaCalls := []Call{
		quota("GarbageCollectToQuota above the store's bytes", func(live int64) int64 { return live * 8 }),
		quota("GarbageCollectToQuota no quota", func(int64) int64 { return 0 }),
	}
	quotaCalls = append(quotaCalls, c.summaries(gpPeer, iqcPeer, iqcSigmfPeer)...)
	for _, schema := range pointSchemas {
		quotaCalls = append(quotaCalls, c.typeReads(schema)[:2]...)
	}
	// The summary verbs, then the summaries. Format 1 recomputes a lane's
	// summary row and stamps its last-seen and updated times with the clock
	// (all lanes for RebuildSourceSummaries); format 4's counters are
	// transactional and the verbs are no-ops, so its times stay the lanes'
	// own. These shapes report it as format 1 answers it.
	refresh := []Call{
		c.errOnly("RefreshSourceBatchSummary OMM b052", func(s *storage.FlatSQLStore) error {
			return s.RefreshSourceBatchSummary("OMM.fbs", FixtureProvider, "celestrak-gp", OMMLatestBatch)
		}),
		c.errOnly("RefreshSourceBatchSummary unknown type", func(s *storage.FlatSQLStore) error {
			return s.RefreshSourceBatchSummary("XYZ.fbs", FixtureProvider, "celestrak-gp", OMMLatestBatch)
		}),
	}
	refresh = append(refresh, c.summaries()...)
	rebuild := []Call{
		c.errOnly("RebuildSourceSummaries", func(s *storage.FlatSQLStore) error { return s.RebuildSourceSummaries() }),
		c.errOnly("RebuildDerivedState", func(s *storage.FlatSQLStore) error { return s.RebuildDerivedState() }),
	}
	rebuild = append(rebuild, c.summaries(gpPeer, iqcPeer, iqcSigmfPeer)...)
	for _, schema := range pointSchemas {
		rebuild = append(rebuild, c.typeReads(schema)[:2]...)
	}
	// RebuildIndex: format 1's answer is taken by its definition
	// (rebuildIndexBaseline); every other arm runs it.
	ri := covShape(class, "RebuildIndex", "", c.rowsCall("RebuildIndex", "", func(s *storage.FlatSQLStore) ([]Row, error) {
		m, err := s.RebuildIndex()
		if err != nil {
			return nil, err
		}
		var rows []Row
		for k, v := range m {
			rows = append(rows, ValueRow("schema", k, "n", i64(v)))
		}
		return sortRows(rows), nil
	}))
	ri.Arms = []string{ArmS, ArmF2}
	riF1 := covShape(class, "RebuildIndex", "", c.rowsCall("RebuildIndex", "", rebuildIndexBaseline))
	riF1.Arms = []string{ArmF1}
	// C-39 U3: the verbs restamp format 1's lane times (one OMM lane;
	// every lane), not format 4's; every other field compares.
	refreshShape := covShape(class, "RefreshSourceBatchSummary", "", refresh...)
	rebuildShape := covShape(class, "RebuildSourceSummaries and RebuildDerivedState", "", rebuild...)
	for _, call := range []string{"SourceBatchProgress", "ProducerSourceProgress"} {
		refreshShape.Policy.Calls = append(refreshShape.Policy.Calls,
			CallRuling{Call: call, Why: c39U3, Fields: []string{"LastSeenUnix", "UpdatedAtUnix"}, Standard: "OMM.fbs"})
		rebuildShape = rule(rebuildShape, call, c39U3, "LastSeenUnix", "UpdatedAtUnix")
	}
	return []Shape{gc, covShape(class, "GarbageCollectToQuota no-op", "", quotaCalls...), refreshShape, rebuildShape, ri, riF1}
}

// X14: the publication log (logservice: PLOG entries by untagged StoreBatch,
// the log index, QueryLogEntries with its since, limit default and cap) and
// the node's local EPM merged into EPM reads (SaveLocalEPM; QueryRawRecords,
// QueryRawRecordRefs with the local lane, QueryRawRecordRefsByRefs'
// fallback, counts).
func (c *cov) x14() []Shape {
	const class = "X14"
	hits := c.hits["OMM.fbs"]
	var entries [][]byte
	var rows []storage.LogIndexRow
	prev := ""
	for i := 0; i < 3; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("coverage-entry-%d", i)))
		eh := hex.EncodeToString(h[:])
		e := buildPLOG(uint64(i+1), "OMM", covPeer, hits[i], prev, eh, uint64(1790000000+i))
		entries = append(entries, e)
		rows = append(rows, storage.LogIndexRow{Sequence: uint64(i + 1), EntryHash: eh, RecordCID: hits[i], PLGCID: storage.ComputeCID(e),
			EpochDay: "2026-09-27", Timestamp: int64(1790000000 + i)})
		prev = eh
	}
	logq := func(peer, typ string, since uint64, limit int) Call {
		return c.rowsCall(fmt.Sprintf("QueryLogEntries %s %s since=%d limit=%d", peer, typ, since, limit), "PLOG.fbs", func(s *storage.FlatSQLStore) ([]Row, error) {
			l, err := s.QueryLogEntries(peer, typ, since, limit)
			return bytesRows(l), err
		})
	}
	epm := buildEPM(1)
	stored := buildEPM(2)
	ts := covTags("coverage-epm", "epm-1")
	calls := []Call{
		c.storeBatch("StoreBatch PLOG (logservice)", "PLOG.fbs", entries, covPeer, nil, nil),
		c.errOnly("UpsertLogIndexBatch", func(s *storage.FlatSQLStore) error { return s.UpsertLogIndexBatch(covPeer, "OMM", rows) }),
		c.errOnly("UpsertLogIndex an entry not stored", func(s *storage.FlatSQLStore) error {
			return s.UpsertLogIndex(covPeer, "OMM", 4, "beef", hits[3], c.miss["OMM.fbs"][0], "2026-09-27", 1790000003)
		}),
		logq(covPeer, "OMM", 0, 10),
		logq(covPeer, "OMM", 1, 10),
		logq(covPeer, "OMM", 0, 0),
		logq(covPeer, "OMM", 0, 5000),
		logq(covPeer, "MPE", 0, 10),
		logq("16Uiu2HAmNoSuchPeer", "OMM", 0, 10),
		c.gets("GetRecord PLOG", "PLOG.fbs", cidsOf(entries)),
		c.errOnly("SaveLocalEPM", func(s *storage.FlatSQLStore) error { return s.SaveLocalEPM(covPeer, epm) }),
		c.storeBatch("StoreBatchWithSourceTags EPM", "EPM.fbs", [][]byte{stored}, covPeer2, nil, &ts),
		c.rawQuery("QueryRawRecords EPM newest", storage.RawRecordQuery{SchemaName: "EPM.fbs", Limit: 10}, true),
		c.rawQuery("QueryRawRecordRefs EPM cursor", storage.RawRecordQuery{SchemaName: "EPM.fbs", UseRowIDCursor: true, Limit: 10}, false),
		c.rawQuery("QueryRawRecordRefs EPM local lane", storage.RawRecordQuery{SchemaName: "EPM.fbs", ProviderID: "local-node", SourceName: "local-epm", BatchID: "local", Limit: 10}, false),
		c.rawQuery("QueryRawRecordRefs EPM local peer", storage.RawRecordQuery{SchemaName: "EPM.fbs", PeerID: covPeer, Limit: 10}, false),
		c.rawQuery("QueryRawRecordRefs EPM stored lane", storage.RawRecordQuery{SchemaName: "EPM.fbs", SourceName: "coverage-epm", Limit: 10}, false),
		c.rawQuery("QueryRawRecordRefs EPM filtered", storage.RawRecordQuery{SchemaName: "EPM.fbs", SyncFilter: "ENTITY_ID != ''", Limit: 10}, false),
		c.refsOf("refs EPM local", "EPM.fbs", []string{covPeer}, nil),
		c.refsOf("refs EPM stored", "EPM.fbs", cidsOf([][]byte{stored}), nil),
	}
	calls = append(calls, c.typeReads("EPM.fbs")...)
	calls = append(calls, c.valueCall("CountRawRecords EPM local lane", "EPM.fbs", func(s *storage.FlatSQLStore) (Row, error) {
		n, err := s.CountRawRecords(storage.RawRecordQuery{SchemaName: "EPM.fbs", ProviderID: "local-node"})
		return ValueRow("n", i64(n)), err
	}))
	calls = append(calls, c.summaries(covPeer, covPeer2)...)
	return []Shape{covShape(class, "publication log and local EPM", "PLOG.fbs", calls...)}
}
