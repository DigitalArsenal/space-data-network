package storage

// format4_daemon_reads.go — the node's record reads on store format 4
// (design §5.4, §10; contract §3.5, §3.6, §5.4). Each legacy read is one or
// two engine ops on the read lanes; none takes s.mu or waits on a writer.
//
// SEMANTICS THAT CHANGE WITH THE FORMAT (accepted with format 2, C-10, C-12):
//   - a datasync page, a tag-filtered count or head, and an epoch coverage
//     with a lane filter carry each record once (format 1 repeated it once per
//     matching tag row, A16); RowID is the record's seq, which for a migrated
//     record IS format 1's sdn_record_index rowid;
//   - a record's projected tag is the matched tag (§3.6): with a tag filter,
//     an instance that meets it, else the record's earliest; format 1
//     projected one of its rows;
//   - a sync-filtered or searched raw page off the cursor is in arrival
//     order, paged by the engine (format 2's search order; format 1
//     ordered it by window_at, then CID, which the engine's windows give
//     newest first only and without a search), so an offset walk sees no
//     record twice while records arrive;
//   - GetRecord returns the lowest-pid copy (C-12);
//   - a supersede is per partition (A2): a copy loses its last tag and goes
//     while another producer's copy keeps the record live.
// Everything else is format 1's answer: the stored bytes, CIDs, signatures,
// source URLs, content keys and tag times; counts, windows (window_at DESC
// then CID, or CID order), index pages and epoch rankings.

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// ---- records and filters ---------------------------------------------------------------

// f4CIDStored reports a CID text of the one form the engine stores: the
// canonical text (lower-case base32, zero pad bits) of a CIDv1, raw,
// sha2-256 (bafkrei…). Any other text names no record, and a by-CID read
// answers format 1's "not found" for it, as format 1 does for a CID it does
// not hold (benchset R02: a text whose last character differs only in its
// pad bits decodes to a held CID's bytes, but is not that CID).
func f4CIDStored(text string) bool {
	c, err := cid.Decode(text)
	if err != nil || c.Version() != 1 || c.Type() != cid.Raw {
		return false
	}
	p := c.Prefix()
	return p.MhType == mh.SHA2_256 && p.MhLength == 32 && c.String() == text
}

// f4SourceTags is an engine tag as source tags.
func f4SourceTags(t format4.Tag) SourceTags {
	return SourceTags{ProviderID: t.Provider, SourceName: t.Source, SourceURL: t.SourceURL, BatchID: t.Batch,
		ContentKeyID: t.ContentKeyID, ProducerPeerID: t.ProducerPeer, ProducerPublicKey: t.ProducerPubkey}
}

// f4Record is an engine REC row as a Record: the seq as RowID, the source
// timestamp, the stored length, the matched tag and its time. open decrypts
// sealed fields (the stored bytes stay otherwise).
func (b format4Backend) f4Record(schemaName string, r format4.Rec, open bool) (*Record, error) {
	rec := &Record{CID: r.CID, RowID: r.Seq, PeerID: r.Peer, Timestamp: time.Unix(r.TS, 0).UTC(), RecordLength: r.Len}
	if len(r.Sig) > 0 {
		rec.Signature = append([]byte(nil), r.Sig...)
	}
	if r.Data != nil {
		rec.Data = r.Data
		if open {
			data, err := b.s.openStoredRecordBytes(schemaName, r.Data)
			if err != nil {
				return nil, err
			}
			rec.Data = data
		}
	}
	if r.Tag != nil {
		rec.SourceTags = f4SourceTags(r.Tag.Tag)
		if r.Tag.At > 0 {
			rec.MaterializedAt = time.Unix(r.Tag.At, 0).UTC()
		}
	}
	return rec, nil
}

func (b format4Backend) f4Records(schemaName string, recs []format4.Rec, open bool) ([]*Record, error) {
	out := make([]*Record, 0, len(recs))
	for _, r := range recs {
		rec, err := b.f4Record(schemaName, r, open)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func f4Lane(provider, source, batch, producerPeer, producerKey string) format4.LaneFilter {
	return format4.LaneFilter{Provider: strings.TrimSpace(provider), Source: strings.TrimSpace(source), Batch: strings.TrimSpace(batch),
		ProducerPeer: strings.TrimSpace(producerPeer), ProducerPubkey: strings.TrimSpace(producerKey)}
}

func f4LaneEmpty(l format4.LaneFilter) bool { return l == format4.LaneFilter{} }

// f4Pred is one predicate.
func f4Pred(field format4.Field, op format4.Op, values ...format2.Cell) format4.Pred {
	return format4.Pred{Field: field, Op: op, Values: values}
}

// f4SyncPreds compiles a sync_filter (raw_sync_filter.go's grammar) into
// engine predicates over the record's own fields, which are format 1's index
// columns: EPOCH is the epoch, SOURCE_TIMESTAMP the source timestamp,
// EPOCH_DAY its UTC day, NORAD_CAT_ID COL0, OBJECT_ID/ENTITY_ID/FILE_ID COL1,
// OBJECT_TYPE COL2 and OPS_STATUS_CODE COL3. A field a type does not carry is
// absent, and SQL NULL semantics fail every operator on it, as on format 1's
// NULL column.
func f4SyncPreds(text string) ([]format4.Pred, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	clauses, err := splitSyncFilterClauses(text)
	if err != nil {
		return nil, err
	}
	out := make([]format4.Pred, 0, len(clauses))
	for _, clause := range clauses {
		p, err := f4SyncPred(clause)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func f4SyncField(raw string) (format4.Field, rawSyncFilterFieldSpec, error) {
	spec, err := rawSyncFilterField(raw)
	if err != nil {
		return 0, spec, err
	}
	switch spec.column {
	case "idx.epoch_unix":
		return format4.FieldEpoch, spec, nil
	case "idx.source_timestamp":
		return format4.FieldTS, spec, nil
	case "idx.epoch_day":
		return format4.FieldEpochDay, spec, nil
	case "idx.norad_cat_id":
		return format4.FieldCol0, spec, nil
	case "idx.entity_id":
		return format4.FieldCol1, spec, nil
	case "idx.object_type":
		return format4.FieldCol2, spec, nil
	case "idx.ops_status_code":
		return format4.FieldCol3, spec, nil
	}
	return 0, spec, fmt.Errorf("unsupported sync_filter field %q", raw)
}

func f4SyncValue(spec rawSyncFilterFieldSpec, raw string) (format2.Cell, error) {
	v, err := rawSyncFilterValue(spec, raw)
	if err != nil {
		return format2.Cell{}, err
	}
	switch x := v.(type) {
	case int64:
		return format2.Int(x), nil
	case string:
		return format2.Text(x), nil
	}
	return format2.Cell{}, fmt.Errorf("sync_filter value %q", raw)
}

func f4SyncPred(clause string) (format4.Pred, error) {
	if m := syncFilterBetweenPattern.FindStringSubmatch(clause); len(m) == 4 {
		field, spec, err := f4SyncField(m[1])
		if err != nil {
			return format4.Pred{}, err
		}
		lo, err := f4SyncValue(spec, m[2])
		if err != nil {
			return format4.Pred{}, err
		}
		hi, err := f4SyncValue(spec, m[3])
		if err != nil {
			return format4.Pred{}, err
		}
		if spec.kind == "text" || spec.kind == "enum" {
			return format4.Pred{}, fmt.Errorf("sync_filter BETWEEN is not supported for %s", m[1])
		}
		return f4Pred(field, format4.OpBetween, lo, hi), nil
	}
	if m := syncFilterLikePattern.FindStringSubmatch(clause); len(m) == 3 {
		field, spec, err := f4SyncField(m[1])
		if err != nil {
			return format4.Pred{}, err
		}
		if spec.kind != "text" && spec.kind != "enum" && spec.kind != "day" {
			return format4.Pred{}, fmt.Errorf("sync_filter LIKE is not supported for %s", m[1])
		}
		value := unquoteSyncFilterValue(m[2])
		if spec.kind == "enum" {
			value = normalizeIndexEnum(value)
		}
		return f4Pred(field, format4.OpLike, format2.Text(value)), nil
	}
	if m := syncFilterComparePattern.FindStringSubmatch(clause); len(m) == 4 {
		field, spec, err := f4SyncField(m[1])
		if err != nil {
			return format4.Pred{}, err
		}
		op := normalizeSyncFilterOperator(m[2])
		if (spec.kind == "text" || spec.kind == "enum") && op != "=" && op != "!=" {
			return format4.Pred{}, fmt.Errorf("sync_filter operator %s is not supported for %s", op, m[1])
		}
		v, err := f4SyncValue(spec, m[3])
		if err != nil {
			return format4.Pred{}, err
		}
		ops := map[string]format4.Op{"=": format4.OpEq, "!=": format4.OpNe, "<": format4.OpLt, "<=": format4.OpLe, ">": format4.OpGt,
			">=": format4.OpGe}
		return f4Pred(field, ops[op], v), nil
	}
	return format4.Pred{}, fmt.Errorf("unsupported sync_filter clause %q", strings.TrimSpace(clause))
}

// f4RawQuery is a raw-record filter as an engine query (the tag filter, the
// copy's peer, the CID, the sync filter and the search; the cursor when
// asked). nothing reports a filter no record meets.
func f4RawQuery(typ string, filter RawRecordQuery, cursor bool) (q format4.Query, nothing bool, err error) {
	q = format4.Query{Type: typ, Peer: strings.TrimSpace(filter.PeerID),
		Lane: f4Lane(filter.ProviderID, filter.SourceName, filter.BatchID, filter.ProducerPeerID, filter.ProducerPublicKey)}
	if c := strings.TrimSpace(filter.CID); c != "" {
		if !f4CIDStored(c) {
			return q, true, nil
		}
		q.CID = c
	}
	if q.Preds, err = f4SyncPreds(filter.SyncFilter); err != nil {
		return q, false, err
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		if q.Search, err = fullTextMatchExpression(search); err != nil {
			return q, false, err
		}
		if filter.MaxRowID > 0 {
			q.SeqThrough = filter.MaxRowID
		}
	}
	if cursor {
		q.SeqAfter = max(filter.AfterRowID, 0)
		if filter.MaxRowID > 0 {
			q.SeqThrough = filter.MaxRowID
		}
	}
	return q, false, nil
}

// f4Indexed reports a raw filter format 1 ran over its record index (a sync
// filter or a search): its pages leave local EPMs out.
func f4Indexed(q format4.Query) bool { return len(q.Preds) > 0 || q.Search != "" }

// f4LocalEPMs reports a read local EPMs merge into (EPM.fbs, no index
// filter, a tag filter local EPMs meet).
func f4LocalEPMs(filter RawRecordQuery, q format4.Query) bool {
	return !f4Indexed(q) && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter)
}

// ---- point reads -----------------------------------------------------------------------

// GetRecord is the lowest-pid copy (C-12), its bytes opened.
func (b format4Backend) GetRecord(schemaName, cid string) (*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	if !f4CIDStored(cid) {
		return nil, fmt.Errorf("not found: %s", cid)
	}
	recs, err := b.d.api().Get(b.d.ctx, typ, []string{cid}, false, true)
	if err != nil && !errors.Is(err, format4.ErrNoType) {
		return nil, fmt.Errorf("failed to get record: %w", err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("not found: %s", cid)
	}
	r := recs[0]
	rec := &Record{CID: r.CID, PeerID: r.Peer, Timestamp: time.Unix(r.TS, 0), RecordLength: r.Len}
	if len(r.Sig) > 0 {
		rec.Signature = append([]byte(nil), r.Sig...)
	}
	if rec.Data, err = b.s.openStoredRecordBytes(schemaName, r.Data); err != nil {
		return nil, fmt.Errorf("failed to read record data: %w", err)
	}
	return rec, nil
}

// f4TagRows are a set of CIDs' tag rows (one per tag identity, merged over
// copies), newest first per CID.
func (b format4Backend) f4TagRows(schemaName string, cids []string) (map[string][]format4.TagRow, error) {
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	var stored []string
	for _, c := range dedupeStrings(cids) {
		if f4CIDStored(c) {
			stored = append(stored, c)
		}
	}
	out := make(map[string][]format4.TagRow, len(stored))
	for start := 0; start < len(stored); start += 1024 {
		rows, err := b.d.api().Tags(b.d.ctx, typ, stored[start:min(start+1024, len(stored))])
		if errors.Is(err, format4.ErrNoType) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.CID] = append(out[r.CID], r)
		}
	}
	for c := range out {
		rows := out[c]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].At > rows[j].At })
	}
	return out, nil
}

// GetSourceTags is the record's newest tag (format 1: ORDER BY created_at
// DESC LIMIT 1).
func (b format4Backend) GetSourceTags(schemaName, cid string) (SourceTags, error) {
	if _, err := f4Type(schemaName); err != nil {
		return SourceTags{}, err
	}
	rows, err := b.f4TagRows(schemaName, []string{cid})
	if err != nil {
		return SourceTags{}, fmt.Errorf("failed to get source tags: %w", err)
	}
	if len(rows[cid]) == 0 {
		return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
	}
	return f4SourceTags(rows[cid][0].Tag), nil
}

// sourceTagsForCIDs is each record's newest tag.
func (b format4Backend) sourceTagsForCIDs(schemaName string, cids []string) (map[string]SourceTags, error) {
	rows, err := b.f4TagRows(schemaName, cids)
	if err != nil {
		return nil, fmt.Errorf("query source tags: %w", err)
	}
	out := make(map[string]SourceTags, len(rows))
	for c, rs := range rows {
		if len(rs) > 0 {
			out[c] = f4SourceTags(rs[0].Tag)
		}
	}
	return out, nil
}

// exportSourceTags is each exported record's tag: the newest that meets the
// export's own tag filter, else its newest.
func (b format4Backend) exportSourceTags(filter IndexedRecordQuery, cids []string) (map[string]SourceTags, error) {
	rows, err := b.f4TagRows(filter.SchemaName, cids)
	if err != nil {
		return nil, fmt.Errorf("query source tags: %w", err)
	}
	provider, source, batch := strings.TrimSpace(filter.ProviderID), strings.TrimSpace(filter.SourceName), strings.TrimSpace(filter.BatchID)
	out := make(map[string]SourceTags, len(rows))
	for c, rs := range rows {
		if len(rs) == 0 {
			continue
		}
		pick := rs[0]
		for _, r := range rs {
			if (provider == "" || r.Provider == provider) && (source == "" || r.Source == source) && (batch == "" || r.Batch == batch) {
				pick = r
				break
			}
		}
		out[c] = f4SourceTags(pick.Tag)
	}
	return out, nil
}

// QueryRawRecordRefsByRefs resolves scan-bound refs in order: each ref's
// copy (its peer) and a tag row meeting the ref's tag fields. The records
// carry their STORED bytes.
func (b format4Backend) QueryRawRecordRefsByRefs(schemaName string, refs []RawRecordRef) ([]*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return nil, errors.New("schema name is required")
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	normalized := make([]RawRecordRef, 0, len(refs))
	var cids []string
	for _, ref := range refs {
		ref = normalizeRawRecordRef(ref)
		if ref.CID == "" {
			return nil, errors.New("record cid is required")
		}
		normalized = append(normalized, ref)
		if f4CIDStored(ref.CID) {
			cids = append(cids, ref.CID)
		}
	}
	cids = dedupeStrings(cids)
	copies := map[string][]format4.Rec{}
	for start := 0; start < len(cids); start += 1024 {
		got, err := b.d.api().Get(b.d.ctx, typ, cids[start:min(start+1024, len(cids))], true, true)
		if err != nil && !errors.Is(err, format4.ErrNoType) {
			return nil, fmt.Errorf("raw record ref query failed: %w", err)
		}
		for _, r := range got {
			copies[r.CID] = append(copies[r.CID], r)
		}
	}
	tags, err := b.f4TagRows(schemaName, cids)
	if err != nil {
		return nil, fmt.Errorf("raw record ref query failed: %w", err)
	}
	ordered := make([]*Record, 0, len(normalized))
	for _, ref := range normalized {
		var matched *Record
		for _, c := range copies[ref.CID] {
			if ref.PeerID != "" && c.Peer != ref.PeerID {
				continue
			}
			rec, err := b.f4Record(schemaName, c, false)
			if err != nil {
				return nil, err
			}
			ts := tags[ref.CID]
			if len(ts) == 0 {
				if rawRecordMatchesRef(rec, ref) {
					matched = rec
				}
				break
			}
			for _, t := range ts {
				rec.SourceTags, rec.MaterializedAt = f4SourceTags(t.Tag), time.Unix(t.At, 0).UTC()
				if rawRecordMatchesRef(rec, ref) {
					matched = rec
					break
				}
			}
			if matched != nil {
				break
			}
		}
		if matched == nil && schemaName == "EPM.fbs" {
			b.s.mu.RLock()
			local, err := b.s.queryLocalEPMRecordsLocked(RawRecordQuery{SchemaName: schemaName, CID: ref.CID, ProviderID: ref.ProviderID,
				SourceName: ref.SourceName, BatchID: ref.BatchID, ProducerPeerID: ref.ProducerPeerID,
				ProducerPublicKey: ref.ProducerPublicKey, PeerID: ref.PeerID, Limit: 1}, 1)
			b.s.mu.RUnlock()
			if err == nil && len(local) == 1 {
				matched = local[0]
			}
		}
		if matched == nil {
			return nil, fmt.Errorf("raw record ref not found: %s", ref.CID)
		}
		ordered = append(ordered, matched)
	}
	return ordered, nil
}

// ---- raw records (datasync v1, /api/v1/data) -------------------------------------------

func (b format4Backend) queryRawRecords(filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	if err := b.CheckFullTextSearch(filter.SchemaName, filter.Search); err != nil {
		return nil, err
	}
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return nil, errors.New("schema name is required")
	}
	typ, err := f4Type(filter.SchemaName)
	if err != nil {
		return nil, err
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > rawRecordMaxQueryLimit {
		filter.Limit = rawRecordMaxQueryLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	q, nothing, err := f4RawQuery(typ, filter, filter.UseRowIDCursor)
	if err != nil {
		return nil, err
	}
	var records []*Record
	if !nothing {
		var recs []format4.Rec
		q.Hydrate = true
		switch {
		case filter.UseRowIDCursor:
			// The datasync cursor: seq order, each record once (A16).
			q.Order, q.Limit = format4.OrderSeqAsc, int64(filter.Limit)
			recs, err = b.d.api().Scan(b.d.ctx, q)
		case f4Indexed(q):
			// Off the cursor: arrival order, paged by the engine.
			q.Order, q.Limit, q.Offset = format4.OrderSeqAsc, int64(filter.Limit), int64(filter.Offset)
			recs, err = b.d.api().Scan(b.d.ctx, q)
		default:
			// Newest first.
			q.Order, q.Limit, q.Offset = format4.OrderSeqDesc, int64(filter.Limit), int64(filter.Offset)
			recs, err = b.d.api().Scan(b.d.ctx, q)
		}
		if errors.Is(err, format4.ErrNoType) {
			recs, err = nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("raw record query failed: %w", err)
		}
		if records, err = b.f4Records(filter.SchemaName, recs, hydrate); err != nil {
			return nil, fmt.Errorf("failed reading raw record data: %w", err)
		}
	}
	if f4LocalEPMs(filter, q) && len(records) < filter.Limit {
		b.s.mu.RLock()
		local, err := b.s.queryLocalEPMRecordsLocked(filter, filter.Limit-len(records))
		b.s.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		records = append(records, local...)
	}
	return records, nil
}

// f4Head is a raw filter's HEAD (n, bytes, newest seq, ts and tag time).
func (b format4Backend) f4Head(filter RawRecordQuery) (format4.Head, format4.Query, bool, error) {
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return format4.Head{}, format4.Query{}, false, errors.New("schema name is required")
	}
	typ, err := f4Type(filter.SchemaName)
	if err != nil {
		return format4.Head{}, format4.Query{}, false, err
	}
	q, nothing, err := f4RawQuery(typ, filter, false)
	if err != nil || nothing {
		return format4.Head{}, q, nothing, err
	}
	h, err := b.d.api().Head(b.d.ctx, q)
	if errors.Is(err, format4.ErrNoType) {
		return format4.Head{}, q, false, nil
	}
	return h, q, false, err
}

// CountRawRecords is the filter's records, each once (C-10), plus the local
// EPMs an EPM read merges.
func (b format4Backend) CountRawRecords(filter RawRecordQuery) (int64, error) {
	n, _, err := b.RawRecordSnapshot(filter)
	return n, err
}

func (b format4Backend) RawRecordHead(filter RawRecordQuery) (RawRecordHead, error) {
	_, head, err := b.RawRecordSnapshot(filter)
	return head, err
}

// RawRecordSnapshot is the count and the head from one HEAD: one committed
// state (the seq bound and the count of what lies under it).
func (b format4Backend) RawRecordSnapshot(filter RawRecordQuery) (int64, RawRecordHead, error) {
	if err := b.closed(); err != nil {
		return 0, RawRecordHead{}, err
	}
	if err := b.CheckFullTextSearch(filter.SchemaName, filter.Search); err != nil {
		return 0, RawRecordHead{}, err
	}
	h, q, _, err := b.f4Head(filter)
	if err != nil {
		return 0, RawRecordHead{}, fmt.Errorf("raw record head failed: %w", err)
	}
	head := RawRecordHead{TotalBytes: h.Bytes, MaxRecordTimestampUnix: h.MaxTS, MaxRowID: h.MaxSeq, MaxCreatedAtUnix: h.MaxTS}
	if !f4LaneEmpty(q.Lane) {
		// A tag filter's times are its tag instances' (format 1's created_at).
		head.MaxCreatedAtUnix, head.MaxSourceUpdatedAtUnix = h.MaxAt, h.MaxAt
	}
	count := h.N
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if f4LocalEPMs(filter, q) {
		b.s.mu.RLock()
		localCount, localBytes, err := b.s.localEPMSummaryLocked()
		b.s.mu.RUnlock()
		if err != nil {
			return 0, RawRecordHead{}, err
		}
		count += localCount
		head.TotalBytes += localBytes
		if localCount > 0 {
			now := time.Now().Unix()
			head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, now)
			head.MaxCreatedAtUnix = max(head.MaxCreatedAtUnix, now)
		}
	}
	return count, head, nil
}

// QuerySourceTaggedRecords is the newest tagged records meeting the tag
// filter (any tag when none is given), their bytes opened.
func (b format4Backend) QuerySourceTaggedRecords(query SourceTagQuery) ([]*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	q := format4.Query{Type: typ, Lane: f4Lane(query.ProviderID, query.SourceName, query.BatchID, "", ""), Order: format4.OrderSeqDesc,
		Hydrate: true}
	tagged := !f4LaneEmpty(q.Lane)
	var out []*Record
	for offset := int64(0); len(out) < query.Limit; {
		// Untagged records have no tag to meet; skip them when no filter does.
		q.Limit, q.Offset = int64(query.Limit), offset
		recs, err := b.d.api().Scan(b.d.ctx, q)
		if errors.Is(err, format4.ErrNoType) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to query source tagged records: %w", err)
		}
		for _, r := range recs {
			if !tagged && r.Tag == nil {
				continue
			}
			rec, err := b.f4Record(query.SchemaName, r, true)
			if err != nil {
				return nil, err
			}
			// Format 1's row: no tag, no RowID, the local-time timestamp.
			rec.SourceTags, rec.RowID, rec.MaterializedAt, rec.Timestamp = SourceTags{}, 0, time.Time{}, time.Unix(r.TS, 0)
			out = append(out, rec)
			if len(out) == query.Limit {
				break
			}
		}
		if len(recs) < query.Limit {
			break
		}
		offset += int64(len(recs))
	}
	return out, nil
}

// QueryRecentRecords is the newest records first, their bytes opened and
// their tags.
func (b format4Backend) QueryRecentRecords(schemaName string, limit int) ([]*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 250000 {
		limit = 250000
	}
	recs, err := b.d.api().Scan(b.d.ctx, format4.Query{Type: typ, Order: format4.OrderSeqDesc, Limit: int64(limit), Hydrate: true})
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recent records query failed: %w", err)
	}
	out, err := b.f4Records(schemaName, recs, true)
	if err != nil {
		return nil, err
	}
	for _, r := range out {
		r.RowID = 0
	}
	return out, nil
}

// f4FullTableCursorKey names the one cursor a format-4 full-table page
// keeps: the seq it continues below (every copy of a type shares one seq).
const f4FullTableCursorKey = "seq"

// FullTablePageWithCursor is newest seqs first, below the cursor, optionally
// one source's records.
func (b format4Backend) FullTablePageWithCursor(query FullTablePageQuery) (FullTablePageResult, error) {
	if err := b.closed(); err != nil {
		return FullTablePageResult{}, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return FullTablePageResult{}, err
	}
	before := query.BeforeRowID
	if v, ok := query.Cursor[f4FullTableCursorKey]; ok && v > 0 {
		before = v
	}
	q := format4.Query{Type: typ, Lane: format4.LaneFilter{Source: strings.TrimSpace(query.SourceName)}, Order: format4.OrderSeqDesc,
		Limit: int64(query.Limit), Offset: int64(query.Offset), Hydrate: !query.metadataOnly}
	var recs []format4.Rec
	switch {
	case before == 1:
		// Nothing lies below the first seq (a SeqThrough of 0 is no bound).
	default:
		if before > 1 {
			q.SeqThrough = before - 1
		}
		recs, err = b.d.api().Scan(b.d.ctx, q)
		if errors.Is(err, format4.ErrNoType) {
			recs, err = nil, nil
		}
		if err != nil {
			return FullTablePageResult{}, fmt.Errorf("full table page failed: %w", err)
		}
	}
	out, err := b.f4Records(query.SchemaName, recs, true)
	if err != nil {
		return FullTablePageResult{}, err
	}
	next := cloneFullTableCursor(query.Cursor)
	if len(out) > 0 {
		next = FullTablePageCursor{f4FullTableCursorKey: out[len(out)-1].RowID}
	}
	for _, r := range out {
		name := r.SourceTags.SourceName
		r.SourceTags, r.MaterializedAt = SourceTags{}, time.Time{}
		switch {
		case strings.TrimSpace(query.SourceName) != "":
			r.SourceTags.SourceName = strings.TrimSpace(query.SourceName)
		case strings.TrimSpace(query.KnownSourceName) != "":
			r.SourceTags.SourceName = strings.TrimSpace(query.KnownSourceName)
		case query.IncludeSource:
			r.SourceTags.SourceName = name
		}
	}
	if out == nil {
		out = []*Record{}
	}
	return FullTablePageResult{Records: out, NextCursor: next}, nil
}

// f4QueryData is the newest records' opened bytes (Query, QueryAll,
// QueryAllBounded). A SQL WHERE over format 1's record table has no
// equivalent here.
func (b format4Backend) f4QueryData(schemaName, whereClause string, limit, maxTotalBytes int) ([][]byte, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(whereClause) != "" {
		return nil, fmt.Errorf("%w: a SQL WHERE over the legacy record table (%q)", ErrFormat4Unsupported, whereClause)
	}
	recs, err := b.d.api().Scan(b.d.ctx, format4.Query{Type: typ, Order: format4.OrderSeqDesc, Limit: int64(max(limit, 0)), Hydrate: true})
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	var out [][]byte
	total := 0
	for _, r := range recs {
		data, err := b.s.openStoredRecordBytes(schemaName, r.Data)
		if err != nil {
			log.Warnf("Failed to open stored record: %v", err)
			continue
		}
		if maxTotalBytes > 0 {
			if len(data) > maxTotalBytes {
				continue
			}
			if total+len(data) > maxTotalBytes {
				break
			}
			total += len(data)
		}
		out = append(out, data)
	}
	return out, nil
}

func (b format4Backend) Query(schemaName, whereClause string, _ ...interface{}) ([][]byte, error) {
	return b.f4QueryData(schemaName, whereClause, 0, 0)
}

func (b format4Backend) QueryAll(schemaName string, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = 1000
	}
	return b.f4QueryData(schemaName, "", limit, 0)
}

func (b format4Backend) QueryAllBounded(schemaName string, limit int, maxTotalBytes int) ([][]byte, error) {
	return b.f4QueryData(schemaName, "", limit, maxTotalBytes)
}

func (b format4Backend) QueryRoutedByStandard(string, int) ([]RoutedRecord, error) {
	return nil, f4Unsupported("routed-table listings (the (producer, standard) tables are partitions)")
}

func (b format4Backend) QueryRoutedByProducer(string, int) ([]RoutedRecord, error) {
	return nil, f4Unsupported("routed-table listings (the (producer, standard) tables are partitions)")
}

func (b format4Backend) QueryRoutedAll(int) ([]RoutedRecord, error) {
	return nil, f4Unsupported("routed-table listings (the (producer, standard) tables are partitions)")
}

// ---- indexed windows (/api/v1/data, exports) --------------------------------------------

// f4CATActiveStatus are the operational states format 1's active-payload
// window admits (its 'UNKNOWN' is a NULL column there, and absent here).
var f4CATActiveStatus = []string{"OPERATIONAL", "PARTIALLY_OPERATIONAL", "BACKUP_STANDBY", "SPARE", "EXTENDED_MISSION"}

// f4WindowQuery is an indexed-record window as an engine WINDOW: format 1's
// index filters as predicates (window_at is w), its tag filters as the lane
// filter (any tag instance), its order and paging.
func f4WindowQuery(typ string, filter IndexedRecordQuery) format4.Query {
	q := format4.Query{Type: typ, Lane: f4Lane(filter.ProviderID, filter.SourceName, filter.BatchID, "", ""), Order: format4.OrderWDesc,
		Limit: int64(filter.Limit), Offset: int64(max(filter.Offset, 0))}
	if filter.OrderByCID {
		q.Order = format4.OrderCID
	}
	if filter.Day != "" {
		q.Preds = append(q.Preds, f4Pred(format4.FieldEpochDay, format4.OpEq, format2.Text(filter.Day)))
	}
	if filter.NoradCatID != nil {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol0, format4.OpEq, format2.Int(int64(*filter.NoradCatID))))
	}
	if filter.EntityID != "" {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol1, format4.OpEq, format2.Text(filter.EntityID)))
	}
	objectType := normalizeIndexEnum(filter.ObjectType)
	opsStatus := normalizeIndexEnum(filter.OpsStatusCode)
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		objectType = "PAYLOAD"
	}
	if objectType != "" {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol2, format4.OpEq, format2.Text(objectType)))
	}
	if opsStatus != "" {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol3, format4.OpEq, format2.Text(opsStatus)))
	}
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		vals := make([]format2.Cell, len(f4CATActiveStatus))
		for i, v := range f4CATActiveStatus {
			vals[i] = format2.Text(v)
		}
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol3, format4.OpIn, vals...))
	}
	if filter.CAReadyResidentSet {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol0, format4.OpNotNull))
	}
	if filter.From != nil {
		q.Preds = append(q.Preds, f4Pred(format4.FieldW, format4.OpGe, format2.Int(filter.From.Unix())))
	}
	if filter.To != nil {
		q.Preds = append(q.Preds, f4Pred(format4.FieldW, format4.OpLe, format2.Int(filter.To.Unix())))
	}
	return q
}

func (b format4Backend) QueryIndexedRecords(filter IndexedRecordQuery) ([]*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(filter.SchemaName)
	if err != nil {
		return nil, err
	}
	if filter, err = normalizeIndexedRecordWindow(filter); err != nil {
		return nil, err
	}
	q := f4WindowQuery(typ, filter)
	q.Hydrate = true
	recs, err := b.d.api().Window(b.d.ctx, q)
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("indexed query failed: %w", err)
	}
	out, err := b.f4Records(filter.SchemaName, recs, true)
	if err != nil {
		return nil, fmt.Errorf("failed reading indexed record data: %w", err)
	}
	for _, r := range out {
		// Format 1's window projects (provider, source, batch) only.
		t := r.SourceTags
		r.SourceTags = SourceTags{ProviderID: t.ProviderID, SourceName: t.SourceName, BatchID: t.BatchID}
		r.RowID, r.MaterializedAt = 0, time.Time{}
	}
	return out, nil
}

// IndexedRecordWindowLimitForBytes is the shard byte probe: the longest
// prefix of the window whose frames (stored length plus the size prefix)
// fit maxBytes, always at least one record, and whether the budget cut it.
// The engine's HEAD walks the window with a byte cap over stored lengths
// (contract §3.6: no payload read, nothing in Go memory). The cap knows no
// frame overhead, so each step reserves it: one overhead for an upper bound
// on what fits, then every candidate's for a prefix that surely fits.
func (b format4Backend) IndexedRecordWindowLimitForBytes(filter IndexedRecordQuery, maxBytes int64) (int, bool, error) {
	if err := b.closed(); err != nil {
		return 0, false, err
	}
	typ, err := f4Type(filter.SchemaName)
	if err != nil {
		return 0, false, err
	}
	if filter, err = normalizeIndexedRecordWindow(filter); err != nil {
		return 0, false, err
	}
	n, cut, err := b.f4WindowFit(f4WindowQuery(typ, filter), maxBytes)
	if errors.Is(err, format4.ErrNoType) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("shard byte probe failed: %w", err)
	}
	return int(n), cut, nil
}

// f4WindowFit is IndexedRecordWindowLimitForBytes over the engine window q.
func (b format4Backend) f4WindowFit(q format4.Query, maxBytes int64) (int64, bool, error) {
	const frame = DatasetShardFrameOverheadBytes
	api, ctx := b.d.api(), b.d.ctx
	// head is HEAD over the window's records after the first skip, at most
	// limit of them (0 = to the window's end), their stored lengths capped
	// (0 = no cap).
	head := func(skip, limit, byteCap int64) (format4.Head, error) {
		h := q
		h.Offset, h.Limit, h.ByteCap = q.Offset+skip, limit, byteCap
		if q.Limit > 0 {
			if skip >= q.Limit {
				return format4.Head{}, nil
			}
			if limit == 0 || limit > q.Limit-skip {
				h.Limit = q.Limit - skip
			}
		}
		return api.Head(ctx, h)
	}
	var n, used int64 // records taken, their frame bytes
	for q.Limit == 0 || n < q.Limit {
		budget := maxBytes - used
		if budget <= frame {
			next, err := head(n, 1, 0)
			return b.f4FitAtLeastOne(head, n, next.N > 0, err)
		}
		up, err := head(n, 0, budget-frame)
		if err != nil {
			return 0, false, err
		}
		if up.N == 0 {
			// The next record does not fit (up.More), or none is left.
			return b.f4FitAtLeastOne(head, n, up.More, nil)
		}
		if up.Bytes+up.N*frame <= budget {
			// All fit; the next one's length alone passed budget-frame.
			return n + up.N, up.More, nil
		}
		var fit format4.Head
		if c := budget - up.N*frame; c > 0 {
			if fit, err = head(n, up.N, c); err != nil {
				return 0, false, err
			}
		}
		if fit.N == 0 {
			// The next record alone fits: up.N >= 1.
			if fit, err = head(n, 1, budget-frame); err != nil {
				return 0, false, err
			}
		}
		n, used = n+fit.N, used+fit.Bytes+fit.N*frame
	}
	return n, false, nil
}

// f4FitAtLeastOne ends a byte probe that took n records with a next one
// pending (more) or not: a window whose first record alone passes the budget
// is a one-record shard, cut when a second record follows.
func (b format4Backend) f4FitAtLeastOne(head func(skip, limit, byteCap int64) (format4.Head, error), n int64, more bool, err error) (int64, bool, error) {
	if err != nil || n > 0 || !more {
		return n, more, err
	}
	second, err := head(1, 1, 0)
	if err != nil {
		return 0, false, err
	}
	return 1, second.N > 0, nil
}

// DatasetPublicationSetFingerprint is the sorted text CIDs of the records a
// (provider, source[, batch]) tag selects, hashed as format 1 hashes them.
func (b format4Backend) DatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error) {
	if err := b.closed(); err != nil {
		return "", 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return "", 0, err
	}
	recs, err := b.d.api().Window(b.d.ctx, format4.Query{Type: typ, Lane: f4Lane(providerID, sourceName, batchID, "", ""),
		Order: format4.OrderCID, Bulk: true})
	if err != nil && !errors.Is(err, format4.ErrNoType) {
		return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	h := newPublicationSetHash(schemaName, providerID, sourceName, batchID)
	for _, r := range recs {
		h.Write([]byte(r.CID))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), len(recs), nil
}

// QueryLogEntries is a publisher's log after sinceSequence: the log index (a
// control table) in sequence order, each entry's PLOG record from the
// engine; an entry whose record is gone is left out, as format 1's join
// leaves it.
func (b format4Backend) QueryLogEntries(publisherPeerID, schemaType string, sinceSequence uint64, limit int) ([][]byte, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	rows, err := b.s.db.Query(`
		SELECT plg_cid FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?`, publisherPeerID, schemaType, sinceSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	var cids []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to query log entries: %w", err)
		}
		cids = append(cids, cid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	var stored []string
	for _, c := range cids {
		if f4CIDStored(c) {
			stored = append(stored, c)
		}
	}
	recs, err := b.d.api().Get(b.d.ctx, "PLOG", dedupeStrings(stored), false, true)
	if err != nil && !errors.Is(err, format4.ErrNoType) {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	data := make(map[string][]byte, len(recs))
	for _, r := range recs {
		data[r.CID] = r.Data
	}
	var out [][]byte
	for _, c := range cids {
		d, ok := data[c]
		if !ok {
			continue
		}
		opened, err := b.s.openStoredRecordBytes("PLOG.fbs", d)
		if err != nil {
			log.Warnf("Failed to open log entry record: %v", err)
			continue
		}
		out = append(out, opened)
	}
	return out, nil
}

// ---- counts and index pages --------------------------------------------------------------

// f4TypeSummaries are the engine's per-type summaries by type name.
func (b format4Backend) f4TypeSummaries() (map[string]format4.TypeSummary, error) {
	types, err := b.d.api().Types(b.d.ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]format4.TypeSummary, len(types))
	for _, t := range types {
		out[t.Type] = t
	}
	return out, nil
}

// f4TypeRecords is a type's records (unique CIDs).
func (b format4Backend) f4TypeRecords(typ string) (int64, error) {
	types, err := b.f4TypeSummaries()
	if err != nil {
		return 0, err
	}
	return types[typ].Records, nil
}

func (b format4Backend) Count(schemaName string) (int64, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return 0, err
	}
	n, err := b.f4TypeRecords(typ)
	if err != nil {
		return 0, fmt.Errorf("failed to count: %w", err)
	}
	return n, nil
}

func (b format4Backend) EngineRecordCount(schemaName string) (int64, error) {
	return b.Count(schemaName)
}

// RecordIndexPage is the /api/v1/data/index page: newest epoch first (no
// epoch last), then CID; the total over the same filters.
func (b format4Backend) RecordIndexPage(q RecordIndexPageQuery) ([]RecordIndexRow, int64, error) {
	if err := b.closed(); err != nil {
		return nil, 0, err
	}
	typ, err := f4Type(q.SchemaName)
	if err != nil {
		return nil, 0, err
	}
	eq := format4.Query{Type: typ, Lane: f4Lane(q.ProviderID, q.SourceName, q.BatchID, "", ""), Limit: int64(q.Limit),
		Offset: int64(max(q.Offset, 0))}
	if eq.Limit <= 0 {
		eq.Limit = 50
	}
	if q.NoradLike != "" {
		eq.Preds = append(eq.Preds, f4Pred(format4.FieldCol0, format4.OpLike, format2.Text("%"+q.NoradLike+"%")))
	}
	api, ctx := b.d.api(), b.d.ctx
	head, err := api.Head(ctx, format4.Query{Type: eq.Type, Lane: eq.Lane, Preds: eq.Preds})
	if errors.Is(err, format4.ErrNoType) {
		return []RecordIndexRow{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("count record index page: %w", err)
	}
	rows, err := api.IndexPage(ctx, eq)
	if err != nil {
		return nil, 0, fmt.Errorf("query record index page: %w", err)
	}
	out := make([]RecordIndexRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, RecordIndexRow{NoradCatID: r.Col0, EpochUnix: r.Epoch, CID: r.CID})
	}
	return out, head.N, nil
}

// ---- epoch profiles (epoch_profiles.go) ------------------------------------------------

// f4EpochQuery is an epoch profile query's filters as an engine EPOCH op.
func f4EpochQuery(typ string, query EpochRecordQuery) format4.EpochQuery {
	q := format4.EpochQuery{Query: format4.Query{Type: typ, Lane: f4Lane(query.ProviderID, query.SourceName, query.BatchID, "", "")}}
	if query.Day != "" {
		q.Preds = append(q.Preds, f4Pred(format4.FieldEpochDay, format4.OpEq, format2.Text(query.Day)))
	}
	if query.From != nil {
		q.Preds = append(q.Preds, f4Pred(format4.FieldEpoch, format4.OpGe, format2.Int(query.From.UTC().Unix())))
	}
	if query.To != nil {
		q.Preds = append(q.Preds, f4Pred(format4.FieldEpoch, format4.OpLt, format2.Int(query.To.UTC().Unix())))
	}
	if query.NoradCatID != nil {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol0, format4.OpEq, format2.Int(int64(*query.NoradCatID))))
	}
	if query.EntityID != "" {
		q.Preds = append(q.Preds, f4Pred(format4.FieldCol1, format4.OpEq, format2.Text(query.EntityID)))
	}
	q.At = query.At.UTC().Unix()
	q.MaxDelta = max(query.MaxDeltaSeconds, 0)
	switch query.Profile {
	case EpochProfileAsOf:
		q.Profile = format4.EpochAsOf
	case EpochProfileForward:
		q.Profile = format4.EpochForward
	case EpochProfileNearest:
		q.Profile = format4.EpochNearest
	default:
		q.Profile = format4.EpochWindow
	}
	return q
}

// f4EpochRecord is an epoch row as format 1's epoch record (no tag, no
// RowID; the bytes opened).
func (b format4Backend) f4EpochRecord(schemaName string, r format4.Rec) (*Record, error) {
	rec, err := b.f4Record(schemaName, r, true)
	if err != nil {
		return nil, err
	}
	rec.SourceTags, rec.RowID, rec.MaterializedAt = SourceTags{}, 0, time.Time{}
	return rec, nil
}

// queryEpochIndexedRecords is epoch.window: records with an epoch in the
// window, epoch then CID ascending.
func (b format4Backend) queryEpochIndexedRecords(query EpochRecordQuery) ([]*Record, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return nil, err
	}
	q := f4EpochQuery(typ, query)
	q.Profile, q.Limit, q.Hydrate = format4.EpochWindow, int64(epochQueryLimit(query)), true
	recs, err := b.d.api().Epoch(b.d.ctx, q)
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("epoch indexed query failed: %w", err)
	}
	out := make([]*Record, 0, len(recs))
	for _, r := range recs {
		rec, err := b.f4EpochRecord(query.SchemaName, r)
		if err != nil {
			return nil, fmt.Errorf("hydrate epoch indexed record: %w", err)
		}
		out = append(out, rec)
	}
	return out, nil
}

func (b format4Backend) countEpochIndexedRows(query EpochRecordQuery) (int64, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return 0, err
	}
	q := f4EpochQuery(typ, query)
	q.Profile = format4.EpochWindow
	n, err := b.d.api().EpochCount(b.d.ctx, q)
	if errors.Is(err, format4.ErrNoType) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("epoch indexed count failed: %w", err)
	}
	return n, nil
}

// queryPointEpochRecords is one record per entity, ranked as format 1 ranks
// (as_of, forward, nearest), ordered by entity key.
func (b format4Backend) queryPointEpochRecords(query EpochRecordQuery) ([]EpochRecordMatch, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return nil, err
	}
	switch query.Profile {
	case EpochProfileAsOf, EpochProfileForward, EpochProfileNearest:
	default:
		return nil, fmt.Errorf("unsupported point epoch profile %q", query.Profile)
	}
	q := f4EpochQuery(typ, query)
	// Format 1 limits the ranked entities, then drops those past the max
	// delta: the engine filters by the delta first, so the delta is applied
	// here, after the limit.
	q.MaxDelta, q.Limit, q.Hydrate = 0, int64(epochQueryLimit(query)), true
	recs, err := b.d.api().Epoch(b.d.ctx, q)
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("epoch point query failed: %w", err)
	}
	var out []EpochRecordMatch
	for _, r := range recs {
		rec, err := b.f4EpochRecord(query.SchemaName, r)
		if err != nil {
			return nil, fmt.Errorf("hydrate epoch record: %w", err)
		}
		matched := time.Unix(r.Epoch, 0).UTC()
		m := EpochRecordMatch{Record: rec, EntityKey: r.Key, RequestedEpoch: query.At.UTC(), MatchedEpoch: matched,
			DeltaSeconds: absInt64(r.Epoch - query.At.UTC().Unix()), MatchType: epochMatchType(query.Profile, query.At.UTC(), matched)}
		if query.MaxDeltaSeconds > 0 && m.DeltaSeconds > query.MaxDeltaSeconds {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (b format4Backend) countPointEpochEntities(query EpochRecordQuery) (int64, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return 0, err
	}
	switch query.Profile {
	case EpochProfileAsOf, EpochProfileForward, EpochProfileNearest:
	default:
		return 0, fmt.Errorf("unsupported point epoch profile %q", query.Profile)
	}
	n, err := b.d.api().EpochCount(b.d.ctx, f4EpochQuery(typ, query))
	if errors.Is(err, format4.ErrNoType) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("epoch point count failed: %w", err)
	}
	return n, nil
}

func (b format4Backend) QueryEpochCoverage(query EpochRecordQuery) ([]EpochCoverageBucket, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(query.SchemaName)
	if err != nil {
		return nil, err
	}
	q := f4EpochQuery(typ, query)
	q.Profile = format4.EpochCoverage
	buckets, err := b.d.api().Coverage(b.d.ctx, q)
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("epoch coverage query failed: %w", err)
	}
	var out []EpochCoverageBucket
	for _, c := range buckets {
		out = append(out, EpochCoverageBucket{Day: c.Day, Count: c.N, OldestEpoch: time.Unix(c.MinEpoch, 0).UTC(),
			NewestEpoch: time.Unix(c.MaxEpoch, 0).UTC()})
	}
	return out, nil
}

// ---- summaries and counters --------------------------------------------------------------

// f4Lanes are the live lanes of every type (or one).
func (b format4Backend) f4Lanes(typ string) ([]format4.Lane, error) {
	lanes, err := b.d.api().Lanes(b.d.ctx, typ)
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	return lanes, err
}

// DataSummary is format 1's: per schema from its lanes (the source summary)
// when it has any, else from its records; per lane tuple (provider, source,
// batch, producer peer and key); plus the local EPMs.
func (b format4Backend) DataSummary() (*DataSummary, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	summary := &DataSummary{Schemas: make([]DataSchemaSummary, 0), Sources: make([]DataSourceSummary, 0)}
	lanes, err := b.f4Lanes("")
	if err != nil {
		return nil, fmt.Errorf("summarize source-backed producers: %w", err)
	}
	laneSchemas := map[string]*DataSchemaSummary{}
	sources := map[DataSourceSummary]*DataSourceSummary{}
	var sourceKeys []DataSourceSummary
	for _, l := range lanes {
		schema := l.Type + ".fbs"
		ls := laneSchemas[schema]
		if ls == nil {
			ls = &DataSchemaSummary{SchemaName: schema}
			laneSchemas[schema] = ls
		}
		ls.Count += l.Records
		ls.TotalBytes += l.Bytes
		k := DataSourceSummary{SchemaName: schema, ProviderID: l.Provider, SourceName: l.Source, BatchID: l.Batch,
			ProducerPeerID: l.ProducerPeer, ProducerPublicKey: l.ProducerPubkey}
		if sources[k] == nil {
			c := k
			sources[k] = &c
			sourceKeys = append(sourceKeys, k)
		}
		sources[k].Count += l.Records
		sources[k].TotalBytes += l.Bytes
	}
	types, err := b.f4TypeSummaries()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(types))
	seen := map[string]bool{}
	for n := range laneSchemas {
		names, seen[n] = append(names, n), true
	}
	for t := range types {
		if n := t + ".fbs"; !seen[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		src := laneSchemas[n]
		if src == nil || src.Count <= 0 {
			t := types[strings.TrimSuffix(n, ".fbs")]
			src = &DataSchemaSummary{SchemaName: n, Count: t.Records, TotalBytes: t.CopyBytes}
		}
		if src.Count <= 0 {
			continue
		}
		summary.Schemas = append(summary.Schemas, *src)
		summary.TotalRecords += src.Count
		summary.TotalBytes += src.TotalBytes
	}
	sort.Slice(sourceKeys, func(i, j int) bool {
		a, c := sourceKeys[i], sourceKeys[j]
		for _, p := range [][2]string{{a.SchemaName, c.SchemaName}, {a.ProviderID, c.ProviderID}, {a.SourceName, c.SourceName},
			{a.BatchID, c.BatchID}, {a.ProducerPeerID, c.ProducerPeerID}, {a.ProducerPublicKey, c.ProducerPublicKey}} {
			if p[0] != p[1] {
				return p[0] < p[1]
			}
		}
		return false
	})
	for _, k := range sourceKeys {
		if sources[k].Count > 0 {
			summary.Sources = append(summary.Sources, *sources[k])
		}
	}
	b.s.mu.RLock()
	localCount, localBytes, err := b.s.localEPMSummaryLocked()
	b.s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if localCount > 0 {
		summary.Schemas = appendOrAddSchemaSummary(summary.Schemas, DataSchemaSummary{SchemaName: "EPM.fbs", Count: localCount, TotalBytes: localBytes})
		summary.Sources = append(summary.Sources, DataSourceSummary{SchemaName: "EPM.fbs", ProviderID: "local-node", SourceName: "local-epm",
			BatchID: "local", ProducerPeerID: "local-node", ProducerPublicKey: "local-node", Count: localCount, TotalBytes: localBytes})
		summary.TotalRecords += localCount
		summary.TotalBytes += localBytes
	}
	return summary, nil
}

func (b format4Backend) SourceBatchProgress() ([]SourceBatchProgress, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	lanes, err := b.f4Lanes("")
	if err != nil {
		return nil, fmt.Errorf("query source batch progress: %w", err)
	}
	type key struct{ schema, provider, source, batch string }
	agg := map[key]*SourceBatchProgress{}
	var keys []key
	for _, l := range lanes {
		k := key{l.Type + ".fbs", l.Provider, l.Source, l.Batch}
		p := agg[k]
		if p == nil {
			p = &SourceBatchProgress{SchemaName: k.schema, ProviderID: k.provider, SourceName: k.source, BatchID: k.batch}
			agg[k] = p
			keys = append(keys, k)
		}
		p.Count += l.Records
		p.TotalBytes += l.Bytes
		if l.First > 0 && (p.FirstSeenUnix == 0 || l.First < p.FirstSeenUnix) {
			p.FirstSeenUnix = l.First
		}
		p.UpdatedAtUnix = max(p.UpdatedAtUnix, l.Updated)
		p.LastSeenUnix = p.UpdatedAtUnix
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.schema != c.schema {
			return a.schema < c.schema
		}
		if a.provider != c.provider {
			return a.provider < c.provider
		}
		if a.source != c.source {
			return a.source < c.source
		}
		return a.batch < c.batch
	})
	out := make([]SourceBatchProgress, 0, len(keys))
	for _, k := range keys {
		if agg[k].Count > 0 {
			out = append(out, *agg[k])
		}
	}
	return out, nil
}

func (b format4Backend) ProducerSourceProgress() ([]ProducerSourceProgress, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	lanes, err := b.f4Lanes("")
	if err != nil {
		return nil, fmt.Errorf("query producer source progress: %w", err)
	}
	type key struct{ peer, schema, provider, source string }
	type acc struct {
		p        ProducerSourceProgress
		batches  map[string]int64 // batch -> updated
		batchCnt map[string]int64
	}
	agg := map[key]*acc{}
	for _, l := range lanes {
		k := key{l.ProducerPeer, l.Type + ".fbs", l.Provider, l.Source}
		a := agg[k]
		if a == nil {
			a = &acc{p: ProducerSourceProgress{ProducerPeerID: k.peer, SchemaName: k.schema, ProviderID: k.provider, SourceName: k.source},
				batches: map[string]int64{}, batchCnt: map[string]int64{}}
			agg[k] = a
		}
		a.p.Count += l.Records
		a.p.TotalBytes += l.Bytes
		if l.First > 0 && (a.p.FirstSeenUnix == 0 || l.First < a.p.FirstSeenUnix) {
			a.p.FirstSeenUnix = l.First
		}
		a.p.UpdatedAtUnix = max(a.p.UpdatedAtUnix, l.Updated)
		a.p.LastSeenUnix = a.p.UpdatedAtUnix
		a.batches[l.Batch] = max(a.batches[l.Batch], l.Updated)
		a.batchCnt[l.Batch] += l.Records
	}
	out := make([]ProducerSourceProgress, 0, len(agg))
	for _, a := range agg {
		if a.p.Count <= 0 {
			continue
		}
		var best string
		var bestAt int64 = -1
		for batch, at := range a.batches {
			if a.batchCnt[batch] <= 0 {
				continue
			}
			a.p.BatchCount++
			if at > bestAt || (at == bestAt && batch > best) {
				best, bestAt = batch, at
			}
		}
		a.p.LastBatchID = best
		out = append(out, a.p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtUnix != out[j].UpdatedAtUnix {
			return out[i].UpdatedAtUnix > out[j].UpdatedAtUnix
		}
		if out[i].ProducerPeerID != out[j].ProducerPeerID {
			return out[i].ProducerPeerID < out[j].ProducerPeerID
		}
		return out[i].SchemaName < out[j].SchemaName
	})
	return out, nil
}

func (b format4Backend) SourceRecordCounts() (map[string]int64, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	lanes, err := b.f4Lanes("")
	if err != nil {
		return nil, fmt.Errorf("count records per source: %w", err)
	}
	counts := map[string]int64{}
	for _, l := range lanes {
		if l.Records <= 0 {
			continue
		}
		if key := sourceCountKey(l.Provider, l.Source); key != "" {
			counts[key] += l.Records
		}
	}
	return counts, nil
}

// f4LaneBatches are the live batches of a (provider, source) lane.
func (b format4Backend) f4LaneBatches(schemaName, providerID, sourceName string) ([]string, error) {
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	lanes, err := b.f4Lanes(typ)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range lanes {
		if l.Records > 0 && l.Provider == providerID && l.Source == sourceName {
			out = append(out, l.Batch)
		}
	}
	out = dedupeStrings(out)
	sort.Strings(out)
	return out, nil
}

func (b format4Backend) laneBatchHoldsRecords(lane DatasetPublicationLane, batchID string) (bool, error) {
	batches, err := b.f4LaneBatches(lane.SchemaName, lane.ProviderID, lane.SourceName)
	if err != nil {
		return false, fmt.Errorf("probe %s batch %s: %w", lane.SchemaName, batchID, err)
	}
	for _, id := range batches {
		if id == strings.TrimSpace(batchID) {
			return true, nil
		}
	}
	return false, nil
}

func (b format4Backend) laneHasOtherUnledgeredBatch(schemaName, providerID, sourceName string, known map[string]bool) (bool, error) {
	batches, err := b.f4LaneBatches(strings.TrimSpace(schemaName), strings.TrimSpace(providerID), strings.TrimSpace(sourceName))
	if err != nil {
		return false, fmt.Errorf("list lane batches: %w", err)
	}
	for _, id := range batches {
		if !known[strings.TrimSpace(id)] {
			return true, nil
		}
	}
	return false, nil
}

// LiveRecordBytes is the stored bytes of every copy (format 1's partition
// counters).
func (b format4Backend) LiveRecordBytes() (int64, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	types, err := b.f4TypeSummaries()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, t := range types {
		total += t.CopyBytes
	}
	return total, nil
}

// liveRecordBytesReconciled: the counters are the writer's from the first
// append.
func (b format4Backend) liveRecordBytesReconciled() bool { return true }

// SchemaDateRanges is per schema with records: its records, epoch range and
// the stored bytes of its copies.
func (b format4Backend) SchemaDateRanges() ([]SchemaDateRange, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	types, err := b.f4TypeSummaries()
	if err != nil {
		return nil, err
	}
	var out []SchemaDateRange
	for typ, t := range types {
		if t.Records <= 0 {
			continue
		}
		r := SchemaDateRange{Schema: typ + ".fbs", RecordCount: t.Records, TotalBytes: t.CopyBytes}
		if t.MinEpoch != nil && *t.MinEpoch > 0 {
			v := time.Unix(*t.MinEpoch, 0).UTC()
			r.OldestEpoch = &v
		}
		if t.MaxEpoch != nil && *t.MaxEpoch > 0 {
			v := time.Unix(*t.MaxEpoch, 0).UTC()
			r.NewestEpoch = &v
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Schema < out[j].Schema })
	return out, nil
}

// PeerStorageBytes is the stored bytes of the peer's partitions (the engine
// derives a partition's token from the peer as format 1 named its tables).
func (b format4Backend) PeerStorageBytes(peerID string) (int64, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	parts, err := b.d.api().Partitions(b.d.ctx)
	if err != nil {
		return 0, err
	}
	token := sanitizeProducerID(routedProducerID(peerID))
	var total int64
	for _, p := range parts {
		if p.Producer == token {
			total += p.Bytes
		}
	}
	return total, nil
}

// DiskUsageBytes is the engine's files plus the control instance's and the
// auxiliary journal.
func (b format4Backend) DiskUsageBytes() (int64, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	disk, err := b.d.api().Disk(b.d.ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, d := range disk {
		total += d.DBBytes + d.WALBytes + d.JournalBytes + d.IndexBytes + d.FTSBytes
	}
	for _, path := range []string{b.s.controlDBPath, b.s.controlDBPath + "-journal", filepath.Join(b.s.basePath, auxiliaryMetadataFileName)} {
		if n, err := statSize(path); err == nil {
			total += n
		}
	}
	return total, nil
}

// ---- full-text search --------------------------------------------------------------------

// f4FTSStates are the engine's full-text states by type.
func (b format4Backend) f4FTSStates() (map[string]format4.FTSState, error) {
	states, err := b.d.api().FTS(b.d.ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]format4.FTSState, len(states))
	for _, st := range states {
		out[st.Type] = st
	}
	return out, nil
}

// CheckFullTextSearch answers ErrSearchIndexBuilding until the engine's
// index of the type is ready (the engine builds it in the background).
func (b format4Backend) CheckFullTextSearch(schema, search string) error {
	if strings.TrimSpace(search) == "" {
		return nil
	}
	if _, err := fullTextMatchExpression(search); err != nil {
		return err
	}
	schema = normalizeSchemaNameForEpoch(schema)
	if _, _, _, ok := EngineRelationSchemaText(schema); !ok {
		return errors.New("Full-text search is unavailable for this schema")
	}
	if _, ok := sds.SearchSchema(schema); !ok {
		return errors.New("Complete search schema is unavailable")
	}
	if err := b.closed(); err != nil {
		return errors.New("Record store is closing")
	}
	states, err := b.f4FTSStates()
	if err != nil {
		return fmt.Errorf("Search index unavailable: %w", err)
	}
	switch states[strings.TrimSuffix(schema, ".fbs")].State {
	case "ready":
		return nil
	case "building":
		return ErrSearchIndexBuilding
	}
	return errors.New("Full-text search is unavailable for this schema")
}

func (b format4Backend) FullTextIndexState(schema string) string {
	states, err := b.f4FTSStates()
	if err != nil {
		return "unavailable"
	}
	switch st := states[strings.TrimSuffix(normalizeSchemaNameForEpoch(schema), ".fbs")].State; st {
	case "ready", "building":
		return st
	}
	return "cold"
}

// WarmFullTextIndexes: the engine builds every type's index itself; this
// reports which schemas holding records it covers.
func (b format4Backend) WarmFullTextIndexes() (scheduled, skipped []string, err error) {
	types, err := b.f4TypeSummaries()
	if err != nil {
		return nil, nil, err
	}
	states, err := b.f4FTSStates()
	if err != nil {
		return nil, nil, err
	}
	var schemas []string
	for typ, t := range types {
		if t.Records > 0 {
			schemas = append(schemas, typ+".fbs")
		}
	}
	sort.Strings(schemas)
	for _, schema := range schemas {
		if _, ok := sds.SearchSchema(schema); !ok {
			skipped = append(skipped, schema)
			continue
		}
		switch states[strings.TrimSuffix(schema, ".fbs")].State {
		case "ready", "building":
			scheduled = append(scheduled, schema)
		default:
			skipped = append(skipped, schema+": full-text search is off for this schema")
		}
	}
	return scheduled, skipped, nil
}

// ---- module and public SQL (engine_query.go, sandbox_select.go) ------------------------
//
// Trusted module SQL (storage.flatsql_query_stream) runs on the interactive
// lanes; untrusted SQL (/api/v1/query, the table API, the public query flow)
// runs under the sandbox flag on the sandbox lanes (the authorizer, one
// SELECT, the work budget and the lane heap cap). The record relations keep
// format 1's names (<TYPE>, <TYPE>@<source>; _source, _rowid, _offset,
// _data, C-9) and the result framing is the legacy engine's.

// f4SQL runs one statement and hands each row to onRow (cells valid during
// the call only); it returns the column names.
func (b format4Backend) f4SQL(ctx context.Context, req format4.SQLRequest, onRow func([]format2.Cell) error) ([]string, error) {
	dec := format2.RB1Decoder{OnRow: onRow}
	_, err := b.d.api().SQL(ctx, req, func(chunk []byte) error { return dec.Feed(chunk) })
	return dec.Names, err
}

// f4SandboxError maps a sandboxed statement's failure to the legacy typed
// rejection (the flows map its code to an HTTP status).
func f4SandboxError(err error) error {
	var se *format4.StatusError
	if !errors.As(err, &se) {
		if errors.Is(err, context.DeadlineExceeded) {
			return &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeTimeout, Message: "sandbox: timeout: statement exceeded its deadline"}
		}
		return err
	}
	if strings.HasPrefix(se.Msg, "sandbox: ") {
		code := ""
		rest := strings.TrimPrefix(se.Msg, "sandbox: ")
		if i := strings.IndexByte(rest, ':'); i > 0 {
			code = strings.TrimSpace(rest[:i])
		}
		return &flatsqlrt.SandboxError{Code: code, Message: se.Msg}
	}
	code := ""
	switch se.Status {
	case format4.StatusCancelled:
		code = flatsqlrt.SandboxCodeTimeout
	case format4.StatusBudget:
		code = flatsqlrt.SandboxCodeByteCap
	}
	if strings.Contains(se.Msg, "not authorized") {
		code = flatsqlrt.SandboxCodeNotAuthorized
	}
	if code != "" {
		return &flatsqlrt.SandboxError{Code: code, Message: "sandbox: " + code + ": " + se.Msg}
	}
	return fmt.Errorf("SQL error: %s", se.Msg)
}

var errF4NotStream = &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeNotRecordStream,
	Message: "sandbox: not-a-record-stream: raw response stream queries must return only BLOB cells (projection results are JSON-only — request format=json)"}

// f4Stream runs a statement whose every cell is a BLOB and frames it
// ([u32le size][bytes] per cell, the legacy engine's record stream).
func (b format4Backend) f4Stream(ctx context.Context, req format4.SQLRequest) (*flatsqlrt.RawStream, error) {
	payload := []byte{}
	rows, frames := 0, 0
	names, err := b.f4SQL(ctx, req, func(row []format2.Cell) error {
		for _, c := range row {
			if c.Type != format2.CellBlob {
				return errF4NotStream
			}
			payload = binary.LittleEndian.AppendUint32(payload, uint32(len(c.B)))
			payload = append(payload, c.B...)
			if len(c.B) > 0 {
				frames++
			}
		}
		rows++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &flatsqlrt.RawStream{Bytes: payload, Rows: rows, Columns: len(names), FNV1a64: flatsqlrt.FNV1a64WordFolded(payload),
		FrameCount: frames}, nil
}

// QueryRawStream is trusted module SQL as a record stream.
func (b format4Backend) QueryRawStream(sql string, params ...interface{}) (*flatsqlrt.RawStream, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	cells, err := f2Cells(params)
	if err != nil {
		return nil, err
	}
	st, err := b.f4Stream(b.d.ctx, format4.SQLRequest{SQL: sql, Params: cells, Class: format4.ClassInteractive})
	if err != nil {
		var se *format4.StatusError
		if errors.As(err, &se) && strings.Contains(se.Msg, "no such table") {
			return &flatsqlrt.RawStream{Bytes: []byte{}}, nil // a type nothing was stored under yet
		}
		return nil, err
	}
	return st, nil
}

// f4SandboxRequest is untrusted SQL's request: the sandbox flag, the caps
// (one row past the row cap detects it: reject, never truncate) and the
// deadline.
func (b format4Backend) f4SandboxRequest(sql string, caps flatsqlrt.SandboxCaps, params []interface{}) (context.Context, context.CancelFunc, format4.SQLRequest, error) {
	cells, err := f2Cells(params)
	if err != nil {
		return nil, nil, format4.SQLRequest{}, err
	}
	req := format4.SQLRequest{SQL: sql, Params: cells, Class: format4.ClassSandbox, Sandbox: true,
		Caps: format4.Caps{MaxResultBytes: caps.MaxBytes}}
	if caps.MaxRows > 0 {
		req.Caps.MaxResultRows = caps.MaxRows + 1
	}
	ctx, cancel := b.d.ctx, context.CancelFunc(func() {})
	if caps.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, caps.Timeout)
	}
	return ctx, cancel, req, nil
}

func (b format4Backend) QuerySandboxedStream(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (*flatsqlrt.RawStream, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	ctx, cancel, req, err := b.f4SandboxRequest(sql, caps, params)
	if err != nil {
		return nil, err
	}
	defer cancel()
	st, err := b.f4Stream(ctx, req)
	if errors.Is(err, errF4NotStream) {
		return nil, errF4NotStream
	}
	if err != nil {
		return nil, f4SandboxError(err)
	}
	if caps.MaxRows > 0 && uint64(st.Rows) > caps.MaxRows {
		return nil, &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeRowCap, Message: fmt.Sprintf("sandbox: row-cap: result exceeds %d rows", caps.MaxRows)}
	}
	if caps.MaxBytes > 0 && uint64(len(st.Bytes)) > caps.MaxBytes {
		return nil, &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeByteCap, Message: fmt.Sprintf("sandbox: byte-cap: result exceeds %d bytes", caps.MaxBytes)}
	}
	return st, nil
}

// QuerySandboxedJSON is the rows as the legacy engine's bare JSON array of
// {"<column>": value} objects.
func (b format4Backend) QuerySandboxedJSON(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) ([]byte, int, int, error) {
	if err := b.closed(); err != nil {
		return nil, 0, 0, err
	}
	ctx, cancel, req, err := b.f4SandboxRequest(sql, caps, params)
	if err != nil {
		return nil, 0, 0, err
	}
	defer cancel()
	out := []byte{'['}
	var keys [][]byte
	rows := 0
	errByteCap := &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeByteCap, Message: fmt.Sprintf("sandbox: byte-cap: result exceeds %d bytes", caps.MaxBytes)}
	var dec format2.RB1Decoder
	dec.OnRow = func(row []format2.Cell) error {
		if keys == nil {
			keys = make([][]byte, len(dec.Names))
			for i, n := range dec.Names {
				k := []byte{','}
				if i == 0 {
					k[0] = '{'
				}
				k = append(k, '"')
				k = f2JSONEscape(k, []byte(n))
				keys[i] = append(k, '"', ':')
			}
		}
		if rows > 0 {
			out = append(out, ',')
		}
		rows++
		for i, c := range row {
			out = append(out, keys[i]...)
			switch c.Type {
			case format2.CellInt:
				out = strconv.AppendInt(out, c.I, 10)
			case format2.CellReal:
				if math.IsNaN(c.F) || math.IsInf(c.F, 0) {
					out = append(out, "null"...)
				} else {
					out = strconv.AppendFloat(out, c.F, 'g', 17, 64)
				}
			case format2.CellText:
				out = append(out, '"')
				out = f2JSONEscape(out, c.B)
				out = append(out, '"')
			case format2.CellBlob:
				out = append(out, '"')
				out = append(out, base64.StdEncoding.EncodeToString(c.B)...)
				out = append(out, '"')
			default:
				out = append(out, "null"...)
			}
		}
		if len(row) > 0 {
			out = append(out, '}')
		}
		if caps.MaxBytes > 0 && uint64(len(out)) > caps.MaxBytes {
			return errByteCap
		}
		return nil
	}
	_, err = b.d.api().SQL(ctx, req, func(chunk []byte) error { return dec.Feed(chunk) })
	cols := len(dec.Names)
	if errors.Is(err, errByteCap) {
		return nil, rows, cols, errByteCap
	}
	if err != nil {
		return nil, rows, cols, f4SandboxError(err)
	}
	if caps.MaxRows > 0 && uint64(rows) > caps.MaxRows {
		return nil, rows, cols, &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeRowCap, Message: fmt.Sprintf("sandbox: row-cap: result exceeds %d rows", caps.MaxRows)}
	}
	out = append(out, ']')
	return validUTF8JSON(out), rows, cols, nil
}

// sandboxedSelect is SandboxedSelect's statement on the sandbox path; the
// caller validated it and resolved the caps.
func (b format4Backend) sandboxedSelect(ctx context.Context, stmt string, maxRows, maxBytes int, timeout time.Duration) (*SandboxSelectResult, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &SandboxSelectResult{Columns: []string{}}
	bytesUsed := 0
	errDone := errors.New("format4: sandboxed select is full")
	names, err := b.f4SQL(ctx, format4.SQLRequest{SQL: stmt, Class: format4.ClassSandbox, Sandbox: true,
		Caps: format4.Caps{MaxResultRows: uint64(maxRows) + 1}}, func(r []format2.Cell) error {
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			return errDone
		}
		row := make([]string, len(r))
		for i, c := range r {
			var cell string
			switch c.Type {
			case format2.CellNull:
			case format2.CellBlob:
				cell = fmt.Sprintf("<%d bytes>", len(c.B))
			case format2.CellInt:
				cell = strconv.FormatInt(c.I, 10)
			case format2.CellReal:
				cell = fmt.Sprint(c.F)
			default:
				cell = string(c.B)
			}
			bytesUsed += len(cell)
			row[i] = cell
		}
		if bytesUsed > maxBytes {
			out.Truncated = true
			return errDone
		}
		out.Rows = append(out.Rows, row)
		return nil
	})
	if names != nil {
		out.Columns = names
	}
	if err != nil && !errors.Is(err, errDone) {
		return nil, fmt.Errorf("sandboxed select: %w", f4SandboxError(err))
	}
	return out, nil
}

// PublicQuerySurface is the engine's SQL surface (SURFACE: the relations,
// their columns and placeholder columns, as format 1 lists them), each with
// the records it serves: its type's or source's records, at most its A18
// bound.
func (b format4Backend) PublicQuerySurface() ([]QuerySurfaceTable, error) {
	if err := b.closed(); err != nil {
		return nil, err
	}
	rels, err := b.d.api().Surface(b.d.ctx)
	if err != nil {
		return nil, err
	}
	types, err := b.f4TypeSummaries()
	if err != nil {
		return nil, err
	}
	lanes, err := b.f4Lanes("")
	if err != nil {
		return nil, err
	}
	sources := map[[2]string]int64{}
	for _, l := range lanes {
		if l.Records > 0 {
			sources[[2]string{l.Type, l.Source}] += l.Records
		}
	}
	surface := make([]QuerySurfaceTable, 0, len(rels))
	for _, r := range rels {
		typ, _, _ := strings.Cut(r.Name, "@")
		records := types[typ].Records
		if r.Source != "" {
			records = sources[[2]string{typ, r.Source}]
		}
		if r.Bound > 0 && records > r.Bound {
			records = r.Bound
		}
		surface = append(surface, QuerySurfaceTable{Name: r.Name, Kind: r.Kind, Source: r.Source, Columns: r.Columns,
			PlaceholderColumns: r.Placeholder, Records: records})
	}
	return surface, nil
}
