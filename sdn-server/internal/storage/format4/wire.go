package format4

// The request side of the wire (contract §3.1, §3.5, §3.7): TLVs
// [u16 tag][u32 len][value], little-endian, tags in ascending order (the
// canonical encoding the golden vectors pin), zero values left out.

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/internal/cidv1"
)

// Op codes (§3.5).
const (
	opPut       uint32 = 1
	opSupersede uint32 = 2
	opDelete    uint32 = 3
	opQuotaGC   uint32 = 4
	opRebuild   uint32 = 5
	opGet       uint32 = 10
	opTags      uint32 = 11
	opScan      uint32 = 12
	opHead      uint32 = 13
	opWindow    uint32 = 14
	opIndexPage uint32 = 15
	opEpoch     uint32 = 16
	opSummary   uint32 = 17
	opSQL       uint32 = 30
	opSurface   uint32 = 31
)

var opNames = map[uint32]string{opPut: "PUT", opSupersede: "SUPERSEDE", opDelete: "DELETE", opQuotaGC: "QUOTA_GC",
	opRebuild: "REBUILD", opGet: "GET", opTags: "TAGS", opScan: "SCAN", opHead: "HEAD", opWindow: "WINDOW",
	opIndexPage: "INDEX_PAGE", opEpoch: "EPOCH", opSummary: "SUMMARY", opSQL: "SQL", opSurface: "SURFACE"}

// Request tags (§3.5).
const (
	tagType       uint16 = 1
	tagHydrate    uint16 = 2
	tagLimit      uint16 = 3
	tagOffset     uint16 = 4
	tagOrder      uint16 = 5
	tagSeqAfter   uint16 = 6
	tagSeqThrough uint16 = 7
	tagCID        uint16 = 8
	tagPeer       uint16 = 9
	tagProducer   uint16 = 10
	tagLane       uint16 = 11 // 11-16: provider, source, batch, content_key_id, producer_peer, producer_pubkey
	tagPred       uint16 = 17
	tagSearch     uint16 = 18
	tagByteCap    uint16 = 19
	tagProfile    uint16 = 30
	tagAt         uint16 = 31
	tagMaxDelta   uint16 = 32
	tagCountOnly  uint16 = 33
	tagCIDs       uint16 = 40
	tagEveryCopy  uint16 = 41
	tagKind       uint16 = 45
	tagPutPeer    uint16 = 50
	tagPutTag     uint16 = 51
	tagMode       uint16 = 52
	tagRecords    uint16 = 53
	tagPutAt      uint16 = 54
	tagPutOwnTS   uint16 = 55
	tagKeepBatch  uint16 = 60
	tagApply      uint16 = 61
	tagMaxBytes   uint16 = 62
	tagWhat       uint16 = 63
	tagSQL        uint16 = 70
	tagParams     uint16 = 71
)

// tlv builds one request.
type tlv []byte

func (t tlv) raw(tag uint16, v []byte) tlv { return tlvBytes(t, tag, v) }

func (t tlv) text(tag uint16, s string) tlv {
	if s == "" {
		return t
	}
	return tlvText(t, tag, s)
}

func (t tlv) flag(tag uint16, v bool) tlv {
	if !v {
		return t
	}
	return tlvU8(t, tag, 1)
}

func (t tlv) u8(tag uint16, v uint8) tlv {
	if v == 0 {
		return t
	}
	return tlvU8(t, tag, v)
}

func (t tlv) u32(tag uint16, v uint32) tlv {
	if v == 0 {
		return t
	}
	return tlvU32(t, tag, v)
}

func (t tlv) u64(tag uint16, v uint64) tlv {
	if v == 0 {
		return t
	}
	return tlvU64(t, tag, v)
}

func (t tlv) i64(tag uint16, v int64) tlv {
	if v == 0 {
		return t
	}
	return tlvU64(t, tag, uint64(v))
}

func (t tlv) lane(l LaneFilter) tlv {
	for i, s := range []string{l.Provider, l.Source, l.Batch, l.ContentKeyID, l.ProducerPeer, l.ProducerPubkey} {
		t = t.text(tagLane+uint16(i), s)
	}
	return t
}

func (t tlv) preds(preds []Pred) (tlv, error) {
	for _, p := range preds {
		b, err := encodePred(p)
		if err != nil {
			return nil, err
		}
		t = t.raw(tagPred, b)
	}
	return t, nil
}

func (t tlv) cids(cids []string) (tlv, error) {
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(cids)))
	for _, c := range cids {
		k, err := cidv1.Parse(c)
		if err != nil {
			return nil, fmt.Errorf("CID %q: %w", c, err)
		}
		b = append(b, k[:]...)
	}
	return t.raw(tagCIDs, b), nil
}

// encodePred encodes a predicate: u8 field, u8 op, u16 n, n cells (§3.5).
func encodePred(p Pred) ([]byte, error) {
	if len(p.Values) > math.MaxUint16 {
		return nil, fmt.Errorf("predicate with %d values", len(p.Values))
	}
	b := []byte{byte(p.Field), byte(p.Op)}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(p.Values)))
	for _, c := range p.Values {
		b = appendCell(b, c)
	}
	return b, nil
}

// appendCell is format 2's RB1 cell encoding (the params and predicates
// share it).
func appendCell(b []byte, c format2.Cell) []byte {
	enc := format2.EncodeParams([]format2.Cell{c}) // u32 count + the cell
	return append(b, enc[4:]...)
}

// The ops' fields (§3.5 "Ops (codes)" column): a Query field an op does not
// take is refused, never silently dropped.
type queryFields uint32

const (
	qfCID queryFields = 1 << iota
	qfPeer
	qfProducer
	qfSeq
	qfSearch
	qfOrder
	qfLimit
	qfOffset
	qfHydrate
	qfByteCap
)

func (q Query) check(op uint32, allowed queryFields) error {
	set := map[queryFields]bool{qfCID: q.CID != "", qfPeer: q.Peer != "", qfProducer: q.Producer != "",
		qfSeq: q.SeqAfter != 0 || q.SeqThrough != 0, qfSearch: q.Search != "", qfOrder: q.Order != 0,
		qfLimit: q.Limit != 0, qfOffset: q.Offset != 0, qfHydrate: q.Hydrate, qfByteCap: q.ByteCap != 0}
	names := map[queryFields]string{qfCID: "CID", qfPeer: "Peer", qfProducer: "Producer", qfSeq: "SeqAfter/SeqThrough",
		qfSearch: "Search", qfOrder: "Order", qfLimit: "Limit", qfOffset: "Offset", qfHydrate: "Hydrate", qfByteCap: "ByteCap"}
	for f, on := range set {
		if on && allowed&f == 0 {
			return &StatusError{Op: opNames[op], Status: StatusArg, Msg: names[f] + " does not apply"}
		}
	}
	if q.Type == "" {
		return &StatusError{Op: opNames[op], Status: StatusArg, Msg: "no type"}
	}
	if q.Limit < 0 || q.Offset < 0 || q.ByteCap < 0 {
		return &StatusError{Op: opNames[op], Status: StatusArg, Msg: "negative limit, offset or byte cap"}
	}
	return nil
}

// encodeQuery encodes a SCAN, HEAD, WINDOW or INDEX_PAGE request.
func encodeQuery(op uint32, q Query) ([]byte, error) {
	allowed := map[uint32]queryFields{
		opScan:      qfCID | qfPeer | qfProducer | qfSeq | qfSearch | qfOrder | qfLimit | qfOffset | qfHydrate,
		opHead:      qfCID | qfPeer | qfProducer | qfSeq | qfSearch | qfOrder | qfLimit | qfOffset | qfByteCap,
		opWindow:    qfPeer | qfProducer | qfOrder | qfLimit | qfOffset | qfHydrate,
		opIndexPage: qfLimit | qfOffset,
	}[op]
	if err := q.check(op, allowed); err != nil {
		return nil, err
	}
	t := tlv(nil).text(tagType, q.Type).flag(tagHydrate, q.Hydrate).u64(tagLimit, uint64(q.Limit)).
		u64(tagOffset, uint64(q.Offset)).u8(tagOrder, uint8(q.Order)).i64(tagSeqAfter, q.SeqAfter).
		i64(tagSeqThrough, q.SeqThrough)
	if q.CID != "" {
		k, err := cidv1.Parse(q.CID)
		if err != nil {
			return nil, &StatusError{Op: opNames[op], Status: StatusArg, Msg: err.Error()}
		}
		t = t.raw(tagCID, k[:])
	}
	t = t.text(tagPeer, q.Peer).text(tagProducer, q.Producer).lane(q.Lane)
	t, err := t.preds(q.Preds)
	if err != nil {
		return nil, &StatusError{Op: opNames[op], Status: StatusArg, Msg: err.Error()}
	}
	return t.text(tagSearch, q.Search).u64(tagByteCap, uint64(q.ByteCap)), nil
}

// encodeEpoch encodes an EPOCH request.
func encodeEpoch(q EpochQuery, countOnly bool) ([]byte, error) {
	if err := q.Query.check(opEpoch, qfLimit|qfHydrate); err != nil {
		return nil, err
	}
	if q.Profile < EpochWindow || q.Profile > EpochCoverage {
		return nil, &StatusError{Op: "EPOCH", Status: StatusArg, Msg: fmt.Sprintf("profile %d", q.Profile)}
	}
	t := tlv(nil).text(tagType, q.Type).flag(tagHydrate, q.Hydrate).u64(tagLimit, uint64(q.Limit)).lane(q.Lane)
	t, err := t.preds(q.Preds)
	if err != nil {
		return nil, &StatusError{Op: "EPOCH", Status: StatusArg, Msg: err.Error()}
	}
	return t.u8(tagProfile, uint8(q.Profile)).i64(tagAt, q.At).i64(tagMaxDelta, q.MaxDelta).flag(tagCountOnly, countOnly), nil
}

// PUT record flags (§3.7).
const (
	recSealed uint16 = 1 << iota
	recIdent
	recSeq
	recPeer
	recTags
)

// putOverhead bounds a PUT request's bytes outside its records.
func putOverhead(b Batch) int {
	n := 64 + len(b.Type) + len(b.Peer)
	for _, tg := range b.Tags {
		n += 6 + 7*6 + len(tg.Provider) + len(tg.Source) + len(tg.SourceURL) + len(tg.Batch) +
			len(tg.ContentKeyID) + len(tg.ProducerPeer) + len(tg.ProducerPubkey)
	}
	return n
}

// encodeRecord appends one PUT record entry (§3.7).
func encodeRecord(out []byte, in In, batchPeer string) ([]byte, error) {
	k, err := cidv1.Parse(in.CID)
	if err != nil {
		return nil, err
	}
	var flags uint16
	if in.Sealed != nil {
		flags |= recSealed
	}
	if in.Ident != nil {
		flags |= recIdent
	}
	if in.Seq != 0 {
		flags |= recSeq
	}
	if in.Peer != "" && in.Peer != batchPeer {
		flags |= recPeer
	}
	if len(in.Tags) > 0 {
		flags |= recTags
	}
	if len(in.Sig) > math.MaxUint16 || len(in.Peer) > math.MaxUint16 || len(in.Tags) > math.MaxUint16 {
		return nil, fmt.Errorf("signature, peer or tag list too long")
	}
	out = binary.LittleEndian.AppendUint16(out, flags)
	out = binary.LittleEndian.AppendUint16(out, 0)
	out = append(out, k[:]...)
	out = binary.LittleEndian.AppendUint64(out, uint64(in.TS))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(in.Plain)+4))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(in.Plain)))
	out = append(out, in.Plain...)
	if flags&recSealed != 0 {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(in.Sealed)))
		out = append(out, in.Sealed...)
	}
	out = binary.LittleEndian.AppendUint16(out, uint16(len(in.Sig)))
	out = append(out, in.Sig...)
	if flags&recIdent != 0 {
		out = append(out, in.Ident[:]...)
	}
	if flags&recSeq != 0 {
		out = binary.LittleEndian.AppendUint64(out, uint64(in.Seq))
	}
	if flags&recPeer != 0 {
		out = binary.LittleEndian.AppendUint16(out, uint16(len(in.Peer)))
		out = append(out, in.Peer...)
	}
	if flags&recTags != 0 {
		out = binary.LittleEndian.AppendUint16(out, uint16(len(in.Tags)))
		for _, ta := range in.Tags {
			if ta.Tag < 0 || ta.Tag > math.MaxUint16 {
				return nil, fmt.Errorf("tag index %d", ta.Tag)
			}
			out = binary.LittleEndian.AppendUint16(out, uint16(ta.Tag))
			out = binary.LittleEndian.AppendUint64(out, uint64(ta.At))
		}
	}
	return out, nil
}

// encodeTag is a PUT tag's nested TLV (tags 1-7, empty fields left out).
func encodeTag(tg Tag) []byte {
	return tlv(nil).text(1, tg.Provider).text(2, tg.Source).text(3, tg.SourceURL).text(4, tg.Batch).
		text(5, tg.ContentKeyID).text(6, tg.ProducerPeer).text(7, tg.ProducerPubkey)
}

// encodePut encodes a PUT of the given (already encoded) record entries.
func encodePut(b Batch, n int, records []byte) []byte {
	t := tlv(nil).text(tagType, b.Type).text(tagPutPeer, b.Peer)
	for _, tg := range b.Tags {
		t = t.raw(tagPutTag, encodeTag(tg))
	}
	t = tlvU8(t, tagMode, uint8(b.Mode)) // always present (the golden vector carries mode 0)
	recs := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(records)), uint32(n))
	t = t.raw(tagRecords, append(recs, records...))
	return t.i64(tagPutAt, b.At).flag(tagPutOwnTS, b.OwnTS)
}
