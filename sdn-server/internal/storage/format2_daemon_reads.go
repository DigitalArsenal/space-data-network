package storage

// format2_daemon_reads.go — the node's record reads on store format 2
// (design §8, §9, A16, A17, A18, A20, A28; T6 scope 4). Each legacy read API
// becomes a statement on the partition store's reader lanes, or a read of the
// writer-maintained heads and lane counters; none takes s.mu or waits on a
// writer.
//
// SEMANTICS THAT CHANGE WITH THE FORMAT (A16, A2, recorded in the task):
//   - a datasync v1 page carries each record once, in gseq order (the legacy
//     page repeated a record once per tag row); RowID is the gseq, which for
//     migrated records IS the legacy sdn_record_index rowid;
//   - a record's projected source tag is its FIRST copy's (the PUT's) tag;
//     tag FILTERS match any live tag instance (flatsql 3.2.0 §31.1);
//   - the producer peer and key of a tag come from the partition's lane
//     tuple, and only when that tuple is unambiguous; the source URL and
//     content key of a tag are not kept per record;
//   - Timestamp is the arrival, floored to seconds (migrated records keep the
//     legacy timestamp: store-migrate wrote it as the arrival).

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// ---- lane tuples ---------------------------------------------------------------------

// f2LaneSnapshot is flatsql_lanes, cached for a moment: the producer peer
// and key of a record's tag come from its partition's lane tuple.
type f2LaneSnapshot struct {
	at    time.Time
	lanes []format2.LaneCounter
}

var f2LaneCacheTTL = time.Second

type f2LaneCache struct {
	mu   sync.Mutex
	snap *f2LaneSnapshot
}

// invalidate drops the snapshot: a write just moved the lanes, and this
// daemon's own reads must see its writes.
func (c *f2LaneCache) invalidate() {
	c.mu.Lock()
	c.snap = nil
	c.mu.Unlock()
}

func (s *FlatSQLStore) f2Lanes(ctx context.Context) ([]format2.LaneCounter, error) {
	c := &s.f2.lanes
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap != nil && time.Since(c.snap.at) < f2LaneCacheTTL {
		return c.snap.lanes, nil
	}
	lanes, err := s.ps.Lanes(ctx)
	if err != nil {
		return nil, err
	}
	c.snap = &f2LaneSnapshot{at: time.Now(), lanes: lanes}
	return lanes, nil
}

// f2Producers maps (producer token, type, provider, source, batch) to the
// lane tuple's producer peer and key, when exactly one tuple matches.
type f2TupleKey struct{ producer, typ, provider, source, batch string }

func f2TupleIndex(lanes []format2.LaneCounter) map[f2TupleKey][2]string {
	out := map[f2TupleKey][2]string{}
	ambiguous := map[f2TupleKey]bool{}
	for _, l := range lanes {
		if !l.Tuple || l.Count <= 0 {
			continue
		}
		k := f2TupleKey{l.Producer, l.Type, l.Provider, l.Source, l.Batch}
		v := [2]string{l.Peer, l.PubKey}
		if prev, ok := out[k]; ok && prev != v {
			ambiguous[k] = true
		}
		out[k] = v
	}
	for k := range ambiguous {
		delete(out, k)
	}
	return out
}

// ---- records -------------------------------------------------------------------------

// f2Record converts a lane row to a legacy Record. hydrate opens sealed
// fields (the stored bytes stay otherwise).
func (s *FlatSQLStore) f2Record(schemaName string, r format2.Rec, hydrate bool, tuples map[f2TupleKey][2]string) (*Record, error) {
	rec := &Record{CID: r.CID, RowID: r.Gseq, PeerID: r.PeerID, Timestamp: time.Unix(r.Arrival.Unix(), 0).UTC(),
		RecordLength: r.Length}
	if len(r.Signature) > 0 {
		rec.Signature = append([]byte(nil), r.Signature...)
	}
	if r.Data != nil {
		if hydrate {
			data, err := s.openStoredRecordBytes(schemaName, r.Data)
			if err != nil {
				return nil, err
			}
			rec.Data = data
		} else {
			rec.Data = r.Data
		}
	}
	if r.Provider != "" || r.Source != "" {
		rec.SourceTags = SourceTags{ProviderID: r.Provider, SourceName: r.Source, BatchID: r.Batch}
		if pk, ok := tuples[f2TupleKey{r.Producer, format2.TypeName(schemaName), r.Provider, r.Source, r.Batch}]; ok {
			rec.SourceTags.ProducerPeerID, rec.SourceTags.ProducerPublicKey = pk[0], pk[1]
		}
		rec.MaterializedAt = rec.Timestamp
	}
	return rec, nil
}

func (s *FlatSQLStore) f2Records(ctx context.Context, schemaName string, rows [][]format2.Cell, hydrate bool) ([]*Record, error) {
	var tuples map[f2TupleKey][2]string
	for _, row := range rows {
		if row[8].String() != "" || row[9].String() != "" {
			lanes, err := s.f2Lanes(ctx)
			if err != nil {
				return nil, err
			}
			tuples = f2TupleIndex(lanes)
			break
		}
	}
	out := make([]*Record, 0, len(rows))
	for _, row := range rows {
		rec, err := s.f2Record(schemaName, format2.RecFromRow(row), hydrate, tuples)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// f2Select runs a record statement and converts its rows.
func (s *FlatSQLStore) f2Select(schemaName, sql string, params []format2.Cell, hydrate bool) ([]*Record, error) {
	return s.f2SelectOn(s.ps.Query, schemaName, sql, params, hydrate)
}

// f2SelectPoint is f2Select for an O(1) statement (by CID or gseq): the
// point lanes.
func (s *FlatSQLStore) f2SelectPoint(schemaName, sql string, params []format2.Cell, hydrate bool) ([]*Record, error) {
	return s.f2SelectOn(s.ps.QueryPoint, schemaName, sql, params, hydrate)
}

func (s *FlatSQLStore) f2SelectOn(run func(context.Context, format2.Request) (*format2.Result, error), schemaName, sql string, params []format2.Cell, hydrate bool) ([]*Record, error) {
	ctx := s.f2ctx()
	res, err := run(ctx, format2.Request{SQL: sql, Params: params})
	if format2.NoSuchType(err, schemaName) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.f2Records(ctx, schemaName, res.Rows, hydrate)
}

func (s *FlatSQLStore) f2GetRecord(schemaName, cid string) (*Record, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if _, err := format2.CIDFromText(cid); err != nil {
		return nil, fmt.Errorf("not found: %s", cid)
	}
	r, err := s.ps.GetRecord(s.f2ctx(), schemaName, cid)
	if errors.Is(err, format2.ErrNotFound) {
		return nil, fmt.Errorf("not found: %s", cid)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get record: %w", err)
	}
	rec, err := s.f2Record(schemaName, *r, true, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read record data: %w", err)
	}
	rec.SourceTags = SourceTags{}
	rec.MaterializedAt = time.Time{}
	return rec, nil
}

// ---- per-type columns ------------------------------------------------------------------

// f2EntityColumn is the column the legacy index's entity_id came from.
func f2EntityColumn(schemaName string) (expr string, indexed bool, ok bool) {
	switch format2.TypeName(schemaName) {
	case "OMM", "CAT":
		return "OBJECT_ID", true, true
	case "MPE":
		return "ENTITY_ID", true, true
	case "PNM":
		return "FILE_ID", true, true
	case "RFB":
		return "COALESCE(NULLIF(ID_TRANSMITTER, ''), ID)", false, true
	}
	return "", false, false
}

// f2HasNorad reports a type whose legacy norad_cat_id is a root field.
func f2HasNorad(schemaName string) bool {
	switch format2.TypeName(schemaName) {
	case "OMM", "CAT", "RFB":
		return true
	}
	return false
}

// f2Never is a condition no row meets: a filter on a legacy index column the
// type never filled (the legacy answer was empty).
var f2Never = format2.Cond{SQL: "0"}

func f2CATEnum(column, name string, values map[string]int64) (format2.Cond, bool) {
	v, ok := values[name]
	if !ok {
		return f2Never, true
	}
	return format2.Cond{SQL: column + " = ?", Params: []format2.Cell{format2.Int(v)}}, true
}

var (
	f2CATObjectTypes = func() map[string]int64 {
		out := map[string]int64{}
		for k, v := range CAT.EnumValuesspaceObjectClass {
			out[k] = int64(v)
		}
		return out
	}()
	f2CATOpsStatus = func() map[string]int64 {
		out := map[string]int64{}
		for k, v := range CAT.EnumValuesoperationalState {
			out[k] = int64(v)
		}
		return out
	}()
)

// f2DayRange is one UTC day in epoch milliseconds.
func f2DayRange(day string) (int64, int64, error) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0, 0, err
	}
	return t.UTC().UnixMilli(), t.UTC().Add(24 * time.Hour).UnixMilli(), nil
}

// f2WindowQuery maps an indexed-record window onto the partition store's.
func f2WindowQuery(filter IndexedRecordQuery) (format2.WindowQuery, error) {
	q := format2.WindowQuery{Schema: filter.SchemaName, NoradCatID: filter.NoradCatID,
		Provider: strings.TrimSpace(filter.ProviderID), Source: strings.TrimSpace(filter.SourceName), Batch: strings.TrimSpace(filter.BatchID),
		From: filter.From, To: filter.To, Limit: filter.Limit, Offset: filter.Offset}
	if filter.OrderByCID {
		q.Order = "cid"
	}
	if filter.NoradCatID != nil && !f2HasNorad(filter.SchemaName) {
		q.NoradCatID = nil
		q.Conds = append(q.Conds, f2Never)
	}
	if filter.EntityID != "" {
		col, indexed, ok := f2EntityColumn(filter.SchemaName)
		if !ok {
			q.Conds = append(q.Conds, f2Never)
		} else {
			q.Conds = append(q.Conds, format2.Cond{SQL: col + " = ?", Params: []format2.Cell{format2.Text(filter.EntityID)}, Indexed: indexed})
		}
	}
	if filter.Day != "" {
		lo, hi, err := f2DayRange(filter.Day)
		if err != nil {
			return q, fmt.Errorf("invalid day %q (expected YYYY-MM-DD)", filter.Day)
		}
		if !format2.TypeHasEpoch(filter.SchemaName) {
			q.Conds = append(q.Conds, f2Never)
		} else {
			q.EpochRanges = append(q.EpochRanges, [2]int64{lo, hi})
		}
	}
	objectType := normalizeIndexEnum(filter.ObjectType)
	opsStatus := normalizeIndexEnum(filter.OpsStatusCode)
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		objectType = "PAYLOAD"
	}
	isCAT := format2.TypeName(filter.SchemaName) == "CAT"
	if objectType != "" {
		if !isCAT {
			q.Conds = append(q.Conds, f2Never)
		} else {
			c, _ := f2CATEnum("OBJECT_TYPE", objectType, f2CATObjectTypes)
			q.Conds = append(q.Conds, c)
		}
	}
	if opsStatus != "" {
		if !isCAT {
			q.Conds = append(q.Conds, f2Never)
		} else {
			c, _ := f2CATEnum("OPS_STATUS_CODE", opsStatus, f2CATOpsStatus)
			q.Conds = append(q.Conds, c)
		}
	}
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		if isCAT {
			var vals []string
			var params []format2.Cell
			for _, name := range []string{"OPERATIONAL", "PARTIALLY_OPERATIONAL", "BACKUP_STANDBY", "SPARE", "EXTENDED_MISSION"} {
				vals = append(vals, "?")
				params = append(params, format2.Int(f2CATOpsStatus[name]))
			}
			q.Conds = append(q.Conds, format2.Cond{SQL: "OPS_STATUS_CODE IN (" + strings.Join(vals, ",") + ")", Params: params})
		}
	}
	if filter.CAReadyResidentSet && isCAT {
		q.Conds = append(q.Conds, format2.Cond{SQL: "NORAD_CAT_ID > 0"})
	}
	return q, nil
}

// f2WindowRecs reads an indexed window. A tag-filtered window on a type
// with live REPEAT copies reads each matching partition's top
// (offset + limit) in the window's order without payloads, merges them by
// CID (the tag targets comment above), and reads the window's payloads.
// meta leaves the payloads out.
func (s *FlatSQLStore) f2WindowRecs(ctx context.Context, q format2.WindowQuery, meta bool) ([]format2.Rec, error) {
	merged := false
	rows, err := s.f2WindowMerged(ctx, q, func(sq format2.WindowQuery) ([]format2.WindowRow, error) {
		var recs []format2.Rec
		var err error
		if meta || sq.Table != "" {
			merged = sq.Table != ""
			recs, err = s.ps.WindowMeta(ctx, sq)
		} else {
			recs, err = s.ps.Window(ctx, sq)
		}
		rows := make([]format2.WindowRow, len(recs))
		for i, r := range recs {
			rows[i] = format2.WindowRow{Rec: r, Table: sq.Table}
		}
		return rows, err
	})
	if err != nil {
		return nil, err
	}
	if merged && !meta {
		byTable := map[string][][]byte{}
		for _, r := range rows {
			byTable[r.Table] = append(byTable[r.Table], r.Rec.CIDBin)
		}
		data := map[string]map[string][]byte{}
		for table, cids := range byTable {
			d, err := s.ps.Payloads(ctx, q.Schema, table, cids)
			if err != nil {
				return nil, err
			}
			data[table] = d
		}
		kept := rows[:0]
		for _, r := range rows {
			d, ok := data[r.Table][string(r.Rec.CIDBin)]
			if !ok {
				continue // deleted since
			}
			r.Rec.Data = d
			kept = append(kept, r)
		}
		rows = kept
	}
	out := make([]format2.Rec, len(rows))
	for i, r := range rows {
		out[i] = r.Rec
	}
	return out, nil
}

// f2ByBatch drives a (source, batch) window by its batch when the lanes say
// the batch's records are about the window's own: at most twice the lanes
// that meet every tag condition, and fewer than the source's (the source
// plan walks every posting of the source). The lanes only choose the plan;
// each match is checked (format2.WindowQuery.ByBatch).
func (s *FlatSQLStore) f2ByBatch(ctx context.Context, q *format2.WindowQuery) error {
	if q.Batch == "" || q.Source == "" {
		return nil
	}
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return err
	}
	typ := format2.TypeName(q.Schema)
	var batch, match, source int64
	for _, l := range lanes {
		if l.Type != typ || !l.Tuple || l.Count <= 0 {
			continue
		}
		if l.Source == q.Source {
			source += l.Count
		}
		if l.Batch != q.Batch {
			continue
		}
		batch += l.Count
		if l.Source == q.Source && (q.Provider == "" || l.Provider == q.Provider) {
			match += l.Count
		}
	}
	q.ByBatch = batch <= 2*match+1024 && batch < source
	return nil
}

// f2WindowMerged runs a window read on its targets and merges partition
// results by the window's order, keeping each CID once.
func (s *FlatSQLStore) f2WindowMerged(ctx context.Context, q format2.WindowQuery, read func(format2.WindowQuery) ([]format2.WindowRow, error)) ([]format2.WindowRow, error) {
	if err := s.f2ByBatch(ctx, &q); err != nil {
		return nil, err
	}
	tag := f2TagSpec{provider: q.Provider, source: q.Source, batch: q.Batch}
	targets, err := s.f2Targets(ctx, q.Schema, tag, false)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	if len(targets) == 1 && targets[0].table == "" {
		return read(q)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	var all []format2.WindowRow
	for _, t := range targets {
		sq := q
		sq.Table, sq.Offset, sq.Limit = t.table, 0, q.Offset+limit
		rows, err := read(sq)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].Rec, all[j].Rec
		switch q.Order {
		case "cid":
			return a.CID < b.CID
		case "gseq":
			return a.Gseq < b.Gseq
		}
		sa, sb := format2.EpochSeconds(a.EpochMs), format2.EpochSeconds(b.EpochMs)
		if sa != sb {
			return sa > sb
		}
		return a.CID < b.CID
	})
	seen := map[string]bool{}
	out := make([]format2.WindowRow, 0, limit)
	skip := q.Offset
	for _, r := range all {
		if seen[r.Rec.CID] {
			continue
		}
		seen[r.Rec.CID] = true
		if skip > 0 {
			skip--
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// f2WindowCount counts a window's records (its conditions; distinct CIDs
// over the partitions of a REPEAT-holding type).
func (s *FlatSQLStore) f2WindowCount(ctx context.Context, q format2.WindowQuery) (int64, error) {
	if err := s.f2ByBatch(ctx, &q); err != nil {
		return 0, err
	}
	tag := f2TagSpec{provider: q.Provider, source: q.Source, batch: q.Batch}
	targets, err := s.f2Targets(ctx, q.Schema, tag, false)
	if err != nil {
		return 0, err
	}
	if len(targets) == 1 && targets[0].table == "" {
		return s.ps.Count(ctx, q)
	}
	seen := map[string]bool{}
	for _, t := range targets {
		sq := q
		sq.Table = t.table
		cids, err := s.ps.WindowCIDs(ctx, sq)
		if err != nil {
			return 0, err
		}
		for _, c := range cids {
			seen[c] = true
		}
	}
	return int64(len(seen)), nil
}

func (s *FlatSQLStore) f2QueryIndexedRecords(filter IndexedRecordQuery) ([]*Record, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	if _, err := sds.SchemaNameToTable(filter.SchemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	filter, err := normalizeIndexedRecordWindow(filter)
	if err != nil {
		return nil, err
	}
	q, err := f2WindowQuery(filter)
	if err != nil {
		return nil, err
	}
	ctx := s.f2ctx()
	recs, err := s.f2WindowRecs(ctx, q, false)
	if err != nil {
		return nil, fmt.Errorf("indexed query failed: %w", err)
	}
	out := make([]*Record, 0, len(recs))
	for _, r := range recs {
		rec, err := s.f2Record(filter.SchemaName, r, true, nil)
		if err != nil {
			return nil, fmt.Errorf("failed reading indexed record data: %w", err)
		}
		// The legacy window projected (provider, source, batch) only.
		rec.SourceTags = SourceTags{ProviderID: r.Provider, SourceName: r.Source, BatchID: r.Batch}
		rec.RowID = 0
		rec.RecordLength = 0
		rec.MaterializedAt = time.Time{}
		out = append(out, rec)
	}
	return out, nil
}

// f2IndexedRecordWindowLimitForBytes is the shard byte probe: the window's
// record lengths only (no payload byte read).
func (s *FlatSQLStore) f2IndexedRecordWindowLimitForBytes(filter IndexedRecordQuery, maxBytes int64) (int, bool, error) {
	if err := s.f2Closed(); err != nil {
		return 0, false, err
	}
	if _, err := sds.SchemaNameToTable(filter.SchemaName); err != nil {
		return 0, false, fmt.Errorf("invalid schema name: %w", err)
	}
	filter, err := normalizeIndexedRecordWindow(filter)
	if err != nil {
		return 0, false, err
	}
	q, err := f2WindowQuery(filter)
	if err != nil {
		return 0, false, err
	}
	recs, err := s.f2WindowRecs(s.f2ctx(), q, true)
	if err != nil {
		return 0, false, fmt.Errorf("shard byte probe failed: %w", err)
	}
	var total int64
	count, truncated := 0, false
	for _, r := range recs {
		frame := r.Length + DatasetShardFrameOverheadBytes
		if count > 0 && total+frame > maxBytes {
			truncated = true
			break
		}
		total += frame
		count++
	}
	return count, truncated, nil
}

// ---- tag targets ----------------------------------------------------------------------
//
// A tag condition (provider, source, batch, producer peer) matches a record
// when ONE live tag instance meets all of them (flatsql 3.2.0 §31.1). The
// engine's type-level fan-out evaluates it on the FIRST copy's instances;
// the legacy tag table held every producer's tags per record. So while a
// type holds live REPEAT copies (a record stored by two producers), a tag
// read runs per partition — each partition with the instances of its own
// copies — and the results merge by CID (A2: any live tag instance of a live
// copy). A type without REPEAT copies reads at type level.

// f2TagSpec is a filter's tag conditions.
type f2TagSpec struct{ provider, source, batch, peer, key string }

func (t f2TagSpec) empty() bool {
	return t.provider == "" && t.source == "" && t.batch == "" && t.peer == "" && t.key == ""
}

func (t f2TagSpec) matches(l format2.LaneCounter) bool {
	return l.Tuple && l.Count > 0 && (t.provider == "" || l.Provider == t.provider) && (t.source == "" || l.Source == t.source) &&
		(t.batch == "" || l.Batch == t.batch) && (t.peer == "" || l.Peer == t.peer) && (t.key == "" || l.PubKey == t.key)
}

// conds are the SQL tag conditions (the producer key has no column: a key
// filter is resolved to lane tuples first).
func (t f2TagSpec) conds() []format2.Cond {
	var out []format2.Cond
	for _, c := range [][2]string{{"_provider", t.provider}, {"_source_name", t.source}, {"_batch", t.batch}, {"_peer_id", t.peer}} {
		if c[1] != "" {
			out = append(out, format2.Cond{SQL: c[0] + " = ?", Params: []format2.Cell{format2.Text(c[1])}, Indexed: true})
		}
	}
	return out
}

// f2Target is one statement target of a read: the type (table "") or one
// partition, with the tag conditions it is read with.
type f2Target struct {
	table string
	tag   f2TagSpec
}

// f2TypeHasRepeats reports live REPEAT copies in a type: its partitions hold
// more live copies than it has live records (heads only).
func (s *FlatSQLStore) f2TypeHasRepeats(schema string) (bool, error) {
	parts, err := s.ps.PartitionsOf(schema)
	if err != nil {
		return false, err
	}
	if len(parts) < 2 {
		return false, nil
	}
	var live int64
	for _, p := range parts {
		live += p.Live
	}
	tc, err := s.ps.TypeCounterOf(schema)
	if err != nil {
		return false, err
	}
	return live > tc.FirstLive, nil
}

// f2Targets resolves where a tag-filtered read runs. perTuple splits the
// targets by lane tuple (a legacy count counts tag rows). No target: nothing
// can match.
func (s *FlatSQLStore) f2Targets(ctx context.Context, schema string, tag f2TagSpec, perTuple bool) ([]f2Target, error) {
	if tag.empty() {
		return []f2Target{{}}, nil
	}
	repeats, err := s.f2TypeHasRepeats(schema)
	if err != nil {
		return nil, err
	}
	if !repeats && !perTuple && tag.key == "" {
		return []f2Target{{tag: tag}}, nil
	}
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := s.ps.PartitionsOf(schema)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, p := range parts {
		names[p.PID] = p.SQLName
	}
	typ := format2.TypeName(schema)
	var out []f2Target
	seen := map[f2Target]bool{}
	for _, l := range lanes {
		if l.Type != typ || !tag.matches(l) {
			continue
		}
		t := f2Target{tag: tag}
		if repeats {
			t.table = names[l.PID]
			if t.table == "" {
				continue
			}
		}
		if perTuple || tag.key != "" {
			t.tag = f2TagSpec{provider: l.Provider, source: l.Source, batch: l.Batch, peer: l.Peer}
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// ---- raw records (datasync v1, /api/v1/data) -------------------------------------------

// f2RawFilter is a raw-record filter: its record conditions and its tag
// conditions (resolved to targets when it runs).
type f2RawFilter struct {
	conds   []string
	params  []format2.Cell
	tag     f2TagSpec
	index   bool // sync filter conditions
	nothing bool
}

func (f *f2RawFilter) add(sql string, params ...format2.Cell) {
	var b strings.Builder
	pi := 0
	for i := 0; i < len(sql); i++ {
		if sql[i] != '?' || pi >= len(params) {
			b.WriteByte(sql[i])
			continue
		}
		f.params = append(f.params, params[pi])
		pi++
		fmt.Fprintf(&b, "?%d", len(f.params))
	}
	f.conds = append(f.conds, "("+b.String()+")")
}

func (f *f2RawFilter) where() string {
	if len(f.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(f.conds, " AND ")
}

// on is the filter for one target: its conditions plus the target's tag
// conditions.
func (f *f2RawFilter) on(t f2Target) *f2RawFilter {
	g := &f2RawFilter{conds: append([]string(nil), f.conds...), params: append([]format2.Cell(nil), f.params...), index: f.index}
	for _, c := range t.tag.conds() {
		g.add(c.SQL, c.Params...)
	}
	return g
}

func (t f2Target) from(schema string) string {
	if t.table != "" {
		return format2.QuoteIdent(t.table)
	}
	return format2.QuoteIdent(format2.TypeName(schema))
}

func (s *FlatSQLStore) f2CompileRawFilter(ctx context.Context, filter RawRecordQuery, cursor bool) (*f2RawFilter, error) {
	f := &f2RawFilter{tag: f2TagSpec{provider: strings.TrimSpace(filter.ProviderID), source: strings.TrimSpace(filter.SourceName),
		batch: strings.TrimSpace(filter.BatchID), peer: strings.TrimSpace(filter.ProducerPeerID), key: strings.TrimSpace(filter.ProducerPublicKey)}}
	if peer := strings.TrimSpace(filter.PeerID); peer != "" {
		// The storing call's peer (RecordAttr.peer_id), compared as a value:
		// an _peer_id constraint the vtab consumes is a TAG condition (the
		// tag's producer peer), so the unary + keeps this one a row filter.
		f.add("+_peer_id = ?", format2.Blob([]byte(peer)))
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		bin, err := format2.CIDFromText(cid)
		if err != nil {
			f.nothing = true
			return f, nil
		}
		f.add("_cid_bin = ?", format2.Blob(bin))
	}
	if strings.TrimSpace(filter.SyncFilter) != "" {
		clauses, err := splitSyncFilterClauses(strings.TrimSpace(filter.SyncFilter))
		if err != nil {
			return nil, err
		}
		for _, clause := range clauses {
			sql, params, err := f2CompileSyncFilterClause(filter.SchemaName, clause)
			if err != nil {
				return nil, err
			}
			f.add(sql, params...)
		}
		f.index = true
	}
	if cursor {
		if filter.AfterRowID > 0 {
			f.add("_gseq > ?", format2.Int(filter.AfterRowID))
		} else {
			f.add("_gseq > 0") // a copy not labeled yet has no cursor position
		}
		if filter.MaxRowID > 0 {
			f.add("_gseq <= ?", format2.Int(filter.MaxRowID))
		}
	}
	return f, nil
}

// f2CompileSyncFilterClause is compileSyncFilterClause over record columns:
// the legacy index columns become the record's own fields, the epoch its
// _epoch (milliseconds) and the store time its _arrival.
func f2CompileSyncFilterClause(schemaName, clause string) (string, []format2.Cell, error) {
	type field struct {
		expr string // a column expression
		kind string // time (ms), int, text, enum, day
		enum map[string]int64
		none bool // the legacy column is NULL for every record of this type
	}
	resolve := func(raw string) (field, error) {
		spec, err := rawSyncFilterField(raw)
		if err != nil {
			return field{}, err
		}
		switch spec.column {
		case "idx.epoch_unix":
			return field{expr: "_epoch", kind: "time", none: !format2.TypeHasEpoch(schemaName)}, nil
		case "idx.source_timestamp":
			return field{expr: "_arrival", kind: "time"}, nil
		case "idx.epoch_day":
			return field{expr: "strftime('%Y-%m-%d', _epoch / 1000, 'unixepoch')", kind: "day", none: !format2.TypeHasEpoch(schemaName)}, nil
		case "idx.norad_cat_id":
			return field{expr: "NORAD_CAT_ID", kind: "int", none: !f2HasNorad(schemaName)}, nil
		case "idx.entity_id":
			col, _, ok := f2EntityColumn(schemaName)
			return field{expr: col, kind: "text", none: !ok}, nil
		case "idx.object_type":
			return field{expr: "OBJECT_TYPE", kind: "enum", enum: f2CATObjectTypes, none: format2.TypeName(schemaName) != "CAT"}, nil
		case "idx.ops_status_code":
			return field{expr: "OPS_STATUS_CODE", kind: "enum", enum: f2CATOpsStatus, none: format2.TypeName(schemaName) != "CAT"}, nil
		}
		return field{}, fmt.Errorf("unsupported sync_filter field %q", raw)
	}
	value := func(f field, raw string) (format2.Cell, error) {
		v, err := rawSyncFilterValue(rawSyncFilterFieldSpec{kind: f.kind}, raw)
		if err != nil {
			return format2.Cell{}, err
		}
		switch x := v.(type) {
		case int64:
			return format2.Int(x), nil
		case string:
			if f.kind == "enum" {
				n, ok := f.enum[x]
				if !ok {
					return format2.Int(-1), nil
				}
				return format2.Int(n), nil
			}
			return format2.Text(x), nil
		}
		return format2.Cell{}, fmt.Errorf("sync_filter value %q", raw)
	}
	if m := syncFilterBetweenPattern.FindStringSubmatch(clause); len(m) == 4 {
		f, err := resolve(m[1])
		if err != nil {
			return "", nil, err
		}
		if f.kind == "text" || f.kind == "enum" {
			return "", nil, fmt.Errorf("sync_filter BETWEEN is not supported for %s", m[1])
		}
		lo, err := value(f, m[2])
		if err != nil {
			return "", nil, err
		}
		hi, err := value(f, m[3])
		if err != nil {
			return "", nil, err
		}
		if f.none {
			return "0", nil, nil
		}
		if f.kind == "time" {
			return f.expr + " >= ? AND " + f.expr + " < ?", []format2.Cell{format2.Int(lo.I * 1000), format2.Int((hi.I + 1) * 1000)}, nil
		}
		if f.kind == "day" {
			// UTC days are an epoch range (an EPOCH_CID range scan).
			a, _, err := f2DayRange(lo.String())
			if err != nil {
				return "", nil, err
			}
			_, b, err := f2DayRange(hi.String())
			if err != nil {
				return "", nil, err
			}
			return "_epoch >= ? AND _epoch < ?", []format2.Cell{format2.Int(a), format2.Int(b)}, nil
		}
		return f.expr + " BETWEEN ? AND ?", []format2.Cell{lo, hi}, nil
	}
	if m := syncFilterLikePattern.FindStringSubmatch(clause); len(m) == 3 {
		f, err := resolve(m[1])
		if err != nil {
			return "", nil, err
		}
		if f.kind != "text" && f.kind != "enum" && f.kind != "day" {
			return "", nil, fmt.Errorf("sync_filter LIKE is not supported for %s", m[1])
		}
		v := unquoteSyncFilterValue(m[2])
		if f.none {
			return "0", nil, nil
		}
		if f.kind == "enum" {
			// LIKE over enum names: every name that matches, by value.
			pattern := normalizeIndexEnum(v)
			var params []format2.Cell
			var marks []string
			for name, n := range f.enum {
				if name != "UNKNOWN" && sqlLikeMatch(pattern, name) {
					params = append(params, format2.Int(n))
					marks = append(marks, "?")
				}
			}
			if len(marks) == 0 {
				return "0", nil, nil
			}
			return f.expr + " IN (" + strings.Join(marks, ",") + ")", params, nil
		}
		return f.expr + " LIKE ?", []format2.Cell{format2.Text(v)}, nil
	}
	if m := syncFilterComparePattern.FindStringSubmatch(clause); len(m) == 4 {
		f, err := resolve(m[1])
		if err != nil {
			return "", nil, err
		}
		op := normalizeSyncFilterOperator(m[2])
		if (f.kind == "text" || f.kind == "enum") && op != "=" && op != "!=" {
			return "", nil, fmt.Errorf("sync_filter operator %s is not supported for %s", op, m[1])
		}
		v, err := value(f, m[3])
		if err != nil {
			return "", nil, err
		}
		if f.none {
			return "0", nil, nil
		}
		if f.kind == "time" {
			// Whole seconds (the legacy column) over milliseconds.
			lo, hi := v.I*1000, (v.I+1)*1000
			switch op {
			case "=":
				return f.expr + " >= ? AND " + f.expr + " < ?", []format2.Cell{format2.Int(lo), format2.Int(hi)}, nil
			case "!=":
				return "(" + f.expr + " < ? OR " + f.expr + " >= ?)", []format2.Cell{format2.Int(lo), format2.Int(hi)}, nil
			case ">":
				return f.expr + " >= ?", []format2.Cell{format2.Int(hi)}, nil
			case ">=":
				return f.expr + " >= ?", []format2.Cell{format2.Int(lo)}, nil
			case "<":
				return f.expr + " < ?", []format2.Cell{format2.Int(lo)}, nil
			case "<=":
				return f.expr + " < ?", []format2.Cell{format2.Int(hi)}, nil
			}
		}
		if f.kind == "day" {
			// A UTC day is an epoch range; the legacy day text compares in
			// day order.
			lo, hi, err := f2DayRange(v.String())
			if err != nil {
				return "", nil, err
			}
			switch op {
			case "=":
				return "_epoch >= ? AND _epoch < ?", []format2.Cell{format2.Int(lo), format2.Int(hi)}, nil
			case "!=":
				return "(_epoch < ? OR _epoch >= ?)", []format2.Cell{format2.Int(lo), format2.Int(hi)}, nil
			case ">":
				return "_epoch >= ?", []format2.Cell{format2.Int(hi)}, nil
			case ">=":
				return "_epoch >= ?", []format2.Cell{format2.Int(lo)}, nil
			case "<":
				return "_epoch < ?", []format2.Cell{format2.Int(lo)}, nil
			case "<=":
				return "_epoch < ?", []format2.Cell{format2.Int(hi)}, nil
			}
		}
		if f.kind == "enum" && v.I < 0 {
			if op == "=" {
				return "0", nil, nil
			}
			return "1", nil, nil
		}
		if f.kind == "enum" && op == "!=" {
			// The legacy column was NULL for UNKNOWN, and NULL != x is not true.
			return f.expr + " != ? AND " + f.expr + " != ?", []format2.Cell{v, format2.Int(f.enum["UNKNOWN"])}, nil
		}
		return f.expr + " " + op + " ?", []format2.Cell{v}, nil
	}
	return "", nil, fmt.Errorf("unsupported sync_filter clause %q", strings.TrimSpace(clause))
}

// sqlLikeMatch is SQL LIKE (ASCII case-insensitive, % and _).
func sqlLikeMatch(pattern, s string) bool {
	p, t := strings.ToUpper(pattern), strings.ToUpper(s)
	var match func(i, j int) bool
	match = func(i, j int) bool {
		for i < len(p) {
			switch p[i] {
			case '%':
				for k := j; k <= len(t); k++ {
					if match(i+1, k) {
						return true
					}
				}
				return false
			case '_':
				if j >= len(t) {
					return false
				}
			default:
				if j >= len(t) || p[i] != t[j] {
					return false
				}
			}
			i++
			j++
		}
		return j == len(t)
	}
	return match(0, 0)
}

// f2RawOrder is a raw page's order: "gseq" (the datasync cursor), "gseq
// desc" (newest first) or "epoch" (a sync-filtered page).
func f2RawOrderSQL(order string) string {
	switch order {
	case "gseq":
		return " ORDER BY _gseq"
	case "epoch":
		return " ORDER BY " + f2EpochSecSQL + ", _cid"
	}
	return " ORDER BY _gseq DESC"
}

// f2RawPage runs a raw page over its targets. One type-level target is one
// statement; several targets (or partition targets) run in two phases: each
// target's page of keys (gseq, epoch, CID; no payload), merged, deduplicated
// by CID and windowed in Go, then the records at type level by gseq.
func (s *FlatSQLStore) f2RawPage(ctx context.Context, schema string, f *f2RawFilter, targets []f2Target, order string, limit, offset int, hydrate bool) ([]*Record, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	if len(targets) == 1 && targets[0].table == "" {
		g := f.on(targets[0])
		sql := fmt.Sprintf("SELECT %s FROM %s%s%s LIMIT %d", format2.RecColumns, targets[0].from(schema), g.where(), f2RawOrderSQL(order), limit)
		if offset > 0 {
			sql += fmt.Sprintf(" OFFSET %d", offset)
		}
		return s.f2Select(schema, sql, g.params, hydrate)
	}
	type key struct {
		gseq, epoch int64
		cid         string
	}
	var keys []key
	for _, t := range targets {
		g := f.on(t)
		if t.table != "" {
			g.add("_gseq > 0")
		}
		res, err := s.ps.Query(ctx, format2.Request{SQL: fmt.Sprintf("SELECT _gseq, _epoch, _cid FROM %s%s%s LIMIT %d",
			t.from(schema), g.where(), f2RawOrderSQL(order), limit+offset), Params: g.params})
		if format2.NoSuchType(err, schema) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			keys = append(keys, key{r[0].Int64(), r[1].Int64(), r[2].String()})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		switch order {
		case "gseq":
			return a.gseq < b.gseq
		case "epoch":
			if sa, sb := format2.EpochSeconds(a.epoch), format2.EpochSeconds(b.epoch); sa != sb {
				return sa < sb
			}
			return a.cid < b.cid
		}
		return a.gseq > b.gseq
	})
	seen := map[string]bool{}
	var gseqs []int64
	for _, k := range keys {
		if seen[k.cid] {
			continue
		}
		seen[k.cid] = true
		if offset > 0 {
			offset--
			continue
		}
		gseqs = append(gseqs, k.gseq)
		if len(gseqs) >= limit {
			break
		}
	}
	if len(gseqs) == 0 {
		return nil, nil
	}
	recs, err := s.f2RecordsAtGseqs(schema, gseqs, &f2RawFilter{}, hydrate)
	if err != nil {
		return nil, err
	}
	pos := make(map[int64]int, len(gseqs))
	for i, g := range gseqs {
		pos[g] = i
	}
	sort.Slice(recs, func(i, j int) bool { return pos[recs[i].RowID] < pos[recs[j].RowID] })
	return recs, nil
}

func (s *FlatSQLStore) f2QueryRawRecords(filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return nil, errors.New("schema name is required")
	}
	if _, err := sds.SchemaNameToTable(filter.SchemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
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
	ctx := s.f2ctx()
	if strings.TrimSpace(filter.Search) != "" {
		return s.f2SearchRawRecords(ctx, filter, hydrate)
	}
	f, err := s.f2CompileRawFilter(ctx, filter, filter.UseRowIDCursor)
	if err != nil {
		return nil, err
	}
	var records []*Record
	if !f.nothing {
		targets, err := s.f2Targets(ctx, filter.SchemaName, f.tag, false)
		if err != nil {
			return nil, err
		}
		order, offset := "gseq desc", filter.Offset
		switch {
		case filter.UseRowIDCursor:
			order, offset = "gseq", 0
		case f.index:
			order = "epoch"
		}
		if records, err = s.f2RawPage(ctx, filter.SchemaName, f, targets, order, filter.Limit, offset, hydrate); err != nil {
			return nil, fmt.Errorf("raw record query failed: %w", err)
		}
	}
	if !f.index && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) && len(records) < filter.Limit {
		s.mu.RLock()
		local, err := s.queryLocalEPMRecordsLocked(filter, filter.Limit-len(records))
		s.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		records = append(records, local...)
	}
	return records, nil
}

// f2RawAggregate sums per-target aggregates of a filter that has record
// conditions: per lane tuple when it has tag conditions (the legacy count and
// head summed its tag rows). isMax marks the columns combined by MAX.
func (s *FlatSQLStore) f2RawAggregate(ctx context.Context, schema string, f *f2RawFilter, exprs []string, isMax []bool) ([]int64, error) {
	targets, err := s.f2Targets(ctx, schema, f.tag, true)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(exprs))
	for _, t := range targets {
		g := f.on(t)
		res, err := s.ps.Query(ctx, format2.Request{SQL: fmt.Sprintf("SELECT %s FROM %s%s", strings.Join(exprs, ", "), t.from(schema), g.where()),
			Params: g.params})
		if format2.NoSuchType(err, schema) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i, c := range res.Rows[0] {
			if isMax[i] {
				out[i] = max(out[i], c.Int64())
			} else {
				out[i] += c.Int64()
			}
		}
	}
	return out, nil
}

// f2CountRawRecords counts without a payload byte: the type head, the lane
// counters, or COUNTs over the conditions.
func (s *FlatSQLStore) f2CountRawRecords(filter RawRecordQuery) (int64, error) {
	if err := s.f2Closed(); err != nil {
		return 0, err
	}
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return 0, errors.New("schema name is required")
	}
	if _, err := sds.SchemaNameToTable(filter.SchemaName); err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}
	ctx := s.f2ctx()
	if strings.TrimSpace(filter.Search) != "" {
		return s.f2SearchCount(ctx, filter)
	}
	f, err := s.f2CompileRawFilter(ctx, filter, false)
	if err != nil {
		return 0, err
	}
	var total int64
	switch {
	case f.nothing:
	case len(f.conds) == 0 && f.tag.empty():
		tc, err := s.ps.TypeCounterOf(filter.SchemaName)
		if err != nil {
			return 0, err
		}
		total = tc.FirstLive
	case len(f.conds) == 0:
		// Tag conditions only: the lane counters (the legacy source summary).
		total, err = s.f2LaneSum(ctx, filter, func(l format2.LaneCounter) int64 { return l.Count })
		if err != nil {
			return 0, err
		}
	default:
		sum, err := s.f2RawAggregate(ctx, filter.SchemaName, f, []string{"COUNT(*)"}, []bool{false})
		if err != nil {
			return 0, fmt.Errorf("raw record count failed: %w", err)
		}
		total = sum[0]
	}
	if !f.index && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
		s.mu.RLock()
		local, err := s.countLocalEPMRecordsLocked(filter)
		s.mu.RUnlock()
		if err != nil {
			return 0, err
		}
		total += local
	}
	return total, nil
}

// f2LaneSum sums one lane counter over the lanes a tag filter selects.
func (s *FlatSQLStore) f2LaneSum(ctx context.Context, filter RawRecordQuery, v func(format2.LaneCounter) int64) (int64, error) {
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, l := range s.f2FilterLanes(lanes, filter) {
		total += v(l)
	}
	return total, nil
}

func (s *FlatSQLStore) f2FilterLanes(lanes []format2.LaneCounter, filter RawRecordQuery) []format2.LaneCounter {
	typ := format2.TypeName(filter.SchemaName)
	spec := f2TagSpec{provider: strings.TrimSpace(filter.ProviderID), source: strings.TrimSpace(filter.SourceName),
		batch: strings.TrimSpace(filter.BatchID), peer: strings.TrimSpace(filter.ProducerPeerID), key: strings.TrimSpace(filter.ProducerPublicKey)}
	var out []format2.LaneCounter
	for _, l := range lanes {
		if l.Type == typ && spec.matches(l) {
			out = append(out, l)
		}
	}
	return out
}

func (s *FlatSQLStore) f2RawRecordHead(filter RawRecordQuery) (RawRecordHead, error) {
	if err := s.f2Closed(); err != nil {
		return RawRecordHead{}, err
	}
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return RawRecordHead{}, errors.New("schema name is required")
	}
	if _, err := sds.SchemaNameToTable(filter.SchemaName); err != nil {
		return RawRecordHead{}, fmt.Errorf("invalid schema name: %w", err)
	}
	ctx := s.f2ctx()
	tc, err := s.ps.TypeCounterOf(filter.SchemaName)
	if err != nil {
		return RawRecordHead{}, err
	}
	// The cursor boundary is the type's gseq_hi (A16 MaxRowID): pages filter
	// by their own conditions below it.
	head := RawRecordHead{MaxRowID: tc.GseqHi}
	if strings.TrimSpace(filter.Search) != "" {
		if filter.MaxRowID > 0 {
			head.MaxRowID = filter.MaxRowID
		}
		return head, nil
	}
	f, err := s.f2CompileRawFilter(ctx, filter, false)
	if err != nil {
		return RawRecordHead{}, err
	}
	switch {
	case f.nothing:
	case len(f.conds) == 0 && f.tag.empty():
		head.TotalBytes = tc.FirstLiveBytes
		parts, err := s.ps.PartitionsOf(filter.SchemaName)
		if err != nil {
			return RawRecordHead{}, err
		}
		for _, p := range parts {
			head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, format2.EpochSeconds(p.LatestArrival))
		}
		head.MaxCreatedAtUnix = head.MaxRecordTimestampUnix
	case len(f.conds) == 0:
		lanes, err := s.f2Lanes(ctx)
		if err != nil {
			return RawRecordHead{}, err
		}
		for _, l := range s.f2FilterLanes(lanes, filter) {
			head.TotalBytes += l.Bytes
			head.MaxSourceUpdatedAtUnix = max(head.MaxSourceUpdatedAtUnix, format2.EpochSeconds(l.UpdatedMs))
		}
		head.MaxCreatedAtUnix = head.MaxSourceUpdatedAtUnix
		head.MaxRecordTimestampUnix = head.MaxSourceUpdatedAtUnix
	default:
		sum, err := s.f2RawAggregate(ctx, filter.SchemaName, f, []string{"COALESCE(SUM(_len - 4), 0)", "COALESCE(MAX(_arrival), 0)"}, []bool{false, true})
		if err != nil {
			return RawRecordHead{}, fmt.Errorf("raw record head failed: %w", err)
		}
		head.TotalBytes = sum[0]
		head.MaxRecordTimestampUnix = format2.EpochSeconds(sum[1])
		head.MaxCreatedAtUnix = head.MaxRecordTimestampUnix
		if !f.tag.empty() {
			head.MaxSourceUpdatedAtUnix = head.MaxCreatedAtUnix
		}
	}
	if !f.index && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
		s.mu.RLock()
		localCount, localBytes, err := s.localEPMSummaryLocked()
		s.mu.RUnlock()
		if err != nil {
			return RawRecordHead{}, err
		}
		head.TotalBytes += localBytes
		if localCount > 0 {
			now := time.Now().Unix()
			head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, now)
			head.MaxCreatedAtUnix = max(head.MaxCreatedAtUnix, now)
		}
	}
	return head, nil
}

func (s *FlatSQLStore) f2QueryRawRecordRefsByRefs(schemaName string, refs []RawRecordRef) ([]*Record, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return nil, errors.New("schema name is required")
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
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
		cids = append(cids, ref.CID)
	}
	byCID := map[string]*Record{}
	typ := format2.QuoteIdent(format2.TypeName(schemaName))
	unique := dedupeStrings(cids)
	for start := 0; start < len(unique); start += 256 {
		end := min(start+256, len(unique))
		var params []format2.Cell
		var marks []string
		for _, c := range unique[start:end] {
			bin, err := format2.CIDFromText(c)
			if err != nil {
				continue
			}
			params = append(params, format2.Blob(bin))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		if len(params) == 0 {
			continue
		}
		recs, err := s.f2SelectPoint(schemaName, fmt.Sprintf("SELECT %s FROM %s WHERE _cid_bin IN (%s)", format2.RecColumns, typ, strings.Join(marks, ",")),
			params, false)
		if err != nil {
			return nil, fmt.Errorf("raw record ref query failed: %w", err)
		}
		for _, r := range recs {
			byCID[r.CID] = r
		}
	}
	ordered := make([]*Record, 0, len(normalized))
	for _, ref := range normalized {
		matched := byCID[ref.CID]
		if matched != nil && ref.PeerID != "" && matched.PeerID != ref.PeerID {
			matched = nil
		}
		if matched == nil && schemaName == "EPM.fbs" {
			s.mu.RLock()
			local, err := s.queryLocalEPMRecordsLocked(RawRecordQuery{SchemaName: schemaName, CID: ref.CID, ProviderID: ref.ProviderID,
				SourceName: ref.SourceName, BatchID: ref.BatchID, ProducerPeerID: ref.ProducerPeerID,
				ProducerPublicKey: ref.ProducerPublicKey, PeerID: ref.PeerID, Limit: 1}, 1)
			s.mu.RUnlock()
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

// f2RecordIndexPage is the /api/v1/data/index page: newest epoch first
// (text CID order for a type without an epoch, whose legacy epoch was NULL),
// no payload read.
func (s *FlatSQLStore) f2RecordIndexPage(q RecordIndexPageQuery) ([]RecordIndexRow, int64, error) {
	if err := s.f2Closed(); err != nil {
		return nil, 0, err
	}
	ctx := s.f2ctx()
	hasEpoch := format2.TypeHasEpoch(q.SchemaName)
	hasNorad := f2HasNorad(q.SchemaName)
	wq := format2.WindowQuery{Schema: q.SchemaName, Provider: strings.TrimSpace(q.ProviderID), Source: strings.TrimSpace(q.SourceName),
		Batch: strings.TrimSpace(q.BatchID), Limit: q.Limit, Offset: max(q.Offset, 0)}
	if wq.Limit <= 0 {
		wq.Limit = 50
	}
	if !hasEpoch {
		wq.Order = "cid"
	}
	if q.NoradLike != "" {
		if !hasNorad {
			wq.Conds = append(wq.Conds, f2Never)
		} else {
			wq.Conds = append(wq.Conds, format2.Cond{SQL: "CAST(NORAD_CAT_ID AS TEXT) LIKE ? AND NORAD_CAT_ID > 0",
				Params: []format2.Cell{format2.Text("%" + q.NoradLike + "%")}})
		}
	}
	// The total: the type head when unfiltered, else a COUNT over the same
	// conditions (a lane statement, no payload).
	var total int64
	if wq.Provider == "" && wq.Source == "" && wq.Batch == "" && len(wq.Conds) == 0 {
		tc, err := s.ps.TypeCounterOf(q.SchemaName)
		if err != nil {
			return nil, 0, err
		}
		total = tc.FirstLive
	} else {
		n, err := s.f2WindowCount(ctx, format2.WindowQuery{Schema: wq.Schema, Provider: wq.Provider, Source: wq.Source, Batch: wq.Batch,
			Conds: wq.Conds})
		if err != nil {
			return nil, 0, fmt.Errorf("count record index page: %w", err)
		}
		total = n
	}
	extra := "NULL"
	if hasNorad {
		extra = "NORAD_CAT_ID"
	}
	recs, err := s.f2WindowMerged(ctx, wq, func(q format2.WindowQuery) ([]format2.WindowRow, error) {
		return s.ps.WindowColumns(ctx, q, extra)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("query record index page: %w", err)
	}
	out := make([]RecordIndexRow, 0, len(recs))
	for _, r := range recs {
		row := RecordIndexRow{CID: r.Rec.CID}
		if hasNorad && r.Extra.Type == format2.CellInt && r.Extra.I > 0 {
			v := r.Extra.I
			row.NoradCatID = &v
		}
		if hasEpoch {
			v := format2.EpochSeconds(r.Rec.EpochMs)
			row.EpochUnix = &v
		}
		out = append(out, row)
	}
	return out, total, nil
}

// f2EpochSecSQL floors _epoch to whole seconds (the legacy epoch_unix).
const f2EpochSecSQL = "(CASE WHEN _epoch >= 0 THEN _epoch / 1000 ELSE -((999 - _epoch) / 1000) END)"

// ---- epoch profiles (epoch_profiles.go) ------------------------------------------------

// f2EpochFilter compiles an epoch profile query's filters.
func f2EpochFilter(query EpochRecordQuery) (*f2RawFilter, error) {
	f := &f2RawFilter{tag: f2TagSpec{provider: query.ProviderID, source: query.SourceName, batch: query.BatchID}}
	if query.Day != "" {
		lo, hi, err := f2DayRange(query.Day)
		if err != nil {
			return nil, fmt.Errorf("invalid day %q (expected YYYY-MM-DD)", query.Day)
		}
		f.add("_epoch >= ? AND _epoch < ?", format2.Int(lo), format2.Int(hi))
	}
	if query.From != nil {
		f.add("_epoch >= ?", format2.Int(query.From.UTC().Unix()*1000))
	}
	if query.To != nil {
		f.add("_epoch < ?", format2.Int(query.To.UTC().Unix()*1000))
	}
	if query.NoradCatID != nil {
		if f2HasNorad(query.SchemaName) {
			f.add("NORAD_CAT_ID = ?", format2.Int(int64(*query.NoradCatID)))
		} else {
			f.add("0")
		}
	}
	if query.EntityID != "" {
		if col, _, ok := f2EntityColumn(query.SchemaName); ok {
			f.add(col+" = ?", format2.Text(query.EntityID))
		} else {
			f.add("0")
		}
	}
	return f, nil
}

func f2EpochEntitySQL(schema string) string {
	if format2.TypeName(schema) == "OMM" {
		return "COALESCE(CASE WHEN NORAD_CAT_ID > 0 THEN CAST(NORAD_CAT_ID AS TEXT) END, NULLIF(OBJECT_ID, ''), _cid)"
	}
	col, _, ok := f2EntityColumn(schema)
	if !ok {
		return "_cid"
	}
	return "COALESCE(NULLIF(" + col + ", ''), _cid)"
}

// f2QueryEpochIndexedRecords is epoch.window: records in the window, epoch
// seconds then text CID ascending.
func (s *FlatSQLStore) f2QueryEpochIndexedRecords(query EpochRecordQuery) ([]*Record, error) {
	ctx := s.f2ctx()
	f, err := f2EpochFilter(query)
	if err != nil {
		return nil, err
	}
	targets, err := s.f2Targets(ctx, query.SchemaName, f.tag, false)
	if err != nil {
		return nil, err
	}
	recs, err := s.f2RawPage(ctx, query.SchemaName, f, targets, "epoch", epochQueryLimit(query), 0, true)
	if err != nil {
		return nil, fmt.Errorf("epoch indexed query failed: %w", err)
	}
	for _, r := range recs {
		r.SourceTags, r.RowID, r.RecordLength, r.MaterializedAt = SourceTags{}, 0, 0, time.Time{}
	}
	return recs, nil
}

// f2EpochPick is one entity's chosen record of a point profile.
type f2EpochPick struct {
	gseq, sec int64
	cid, key  string
}

// f2PointEpochPicks ranks each entity's candidates on each target (SQL
// window function on the lane) and keeps, per entity, the best of the
// targets' bests (the rank is a total order, so that is the global best).
func (s *FlatSQLStore) f2PointEpochPicks(ctx context.Context, query EpochRecordQuery) ([]f2EpochPick, error) {
	f, err := f2EpochFilter(query)
	if err != nil {
		return nil, err
	}
	target := query.At.UTC().Unix()
	switch query.Profile {
	case EpochProfileAsOf:
		f.add("_epoch < ?", format2.Int((target+1)*1000))
	case EpochProfileForward:
		f.add("_epoch >= ?", format2.Int(target*1000))
	case EpochProfileNearest:
	default:
		return nil, fmt.Errorf("unsupported point epoch profile %q", query.Profile)
	}
	rank := "matched DESC, cid ASC"
	switch query.Profile {
	case EpochProfileForward:
		rank = "matched ASC, cid ASC"
	case EpochProfileNearest:
		rank = fmt.Sprintf("ABS(matched - %d) ASC, CASE WHEN matched <= %d THEN 0 ELSE 1 END ASC, matched DESC, cid ASC", target, target)
	}
	better := func(a, b f2EpochPick) bool {
		switch query.Profile {
		case EpochProfileForward:
			if a.sec != b.sec {
				return a.sec < b.sec
			}
		case EpochProfileNearest:
			da, db := absInt64(a.sec-target), absInt64(b.sec-target)
			if da != db {
				return da < db
			}
			if (a.sec <= target) != (b.sec <= target) {
				return a.sec <= target
			}
			if a.sec != b.sec {
				return a.sec > b.sec
			}
		default:
			if a.sec != b.sec {
				return a.sec > b.sec
			}
		}
		return a.cid < b.cid
	}
	targets, err := s.f2Targets(ctx, query.SchemaName, f.tag, false)
	if err != nil {
		return nil, err
	}
	best := map[string]f2EpochPick{}
	for _, t := range targets {
		g := f.on(t)
		sql := fmt.Sprintf(`SELECT g, cid, entity_key, matched FROM (
			SELECT g, cid, entity_key, matched, ROW_NUMBER() OVER (PARTITION BY entity_key ORDER BY %s) AS rn FROM (
				SELECT _gseq AS g, _cid AS cid, %s AS entity_key, %s AS matched FROM %s%s)) WHERE rn = 1`,
			rank, f2EpochEntitySQL(query.SchemaName), f2EpochSecSQL, t.from(query.SchemaName), g.where())
		res, err := s.ps.Query(ctx, format2.Request{SQL: sql, Params: g.params})
		if format2.NoSuchType(err, query.SchemaName) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("epoch point query failed: %w", err)
		}
		for _, r := range res.Rows {
			p := f2EpochPick{gseq: r[0].Int64(), cid: r[1].String(), key: r[2].String(), sec: r[3].Int64()}
			if cur, ok := best[p.key]; !ok || better(p, cur) {
				best[p.key] = p
			}
		}
	}
	out := make([]f2EpochPick, 0, len(best))
	for _, p := range best {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

func (s *FlatSQLStore) f2QueryPointEpochRecords(query EpochRecordQuery) ([]EpochRecordMatch, error) {
	ctx := s.f2ctx()
	picks, err := s.f2PointEpochPicks(ctx, query)
	if err != nil {
		return nil, err
	}
	if lim := epochQueryLimit(query); len(picks) > lim {
		picks = picks[:lim]
	}
	var out []EpochRecordMatch
	for start := 0; start < len(picks); start += 256 {
		end := min(start+256, len(picks))
		gseqs := make([]int64, 0, end-start)
		for _, p := range picks[start:end] {
			gseqs = append(gseqs, p.gseq)
		}
		recs, err := s.f2RecordsAtGseqs(query.SchemaName, gseqs, &f2RawFilter{}, true)
		if err != nil {
			return nil, err
		}
		byGseq := map[int64]*Record{}
		for _, r := range recs {
			byGseq[r.RowID] = r
		}
		for _, p := range picks[start:end] {
			rec := byGseq[p.gseq]
			if rec == nil {
				continue // gone since the ranking
			}
			rec.SourceTags, rec.RowID, rec.RecordLength, rec.MaterializedAt = SourceTags{}, 0, 0, time.Time{}
			matched := time.Unix(p.sec, 0).UTC()
			m := EpochRecordMatch{Record: rec, EntityKey: p.key, RequestedEpoch: query.At.UTC(), MatchedEpoch: matched,
				DeltaSeconds: absInt64(p.sec - query.At.UTC().Unix()), MatchType: epochMatchType(query.Profile, query.At.UTC(), matched)}
			if query.MaxDeltaSeconds > 0 && m.DeltaSeconds > query.MaxDeltaSeconds {
				continue
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *FlatSQLStore) f2CountPointEpochEntities(query EpochRecordQuery) (int64, error) {
	picks, err := s.f2PointEpochPicks(s.f2ctx(), query)
	if err != nil {
		return 0, err
	}
	var n int64
	target := query.At.UTC().Unix()
	for _, p := range picks {
		if p.key == "" || (query.MaxDeltaSeconds > 0 && absInt64(p.sec-target) > query.MaxDeltaSeconds) {
			continue
		}
		n++
	}
	return n, nil
}

func (s *FlatSQLStore) f2CountEpochIndexedRows(query EpochRecordQuery) (int64, error) {
	ctx := s.f2ctx()
	f, err := f2EpochFilter(query)
	if err != nil {
		return 0, err
	}
	if len(f.conds) == 0 && f.tag.empty() {
		tc, err := s.ps.TypeCounterOf(query.SchemaName)
		return tc.FirstLive, err
	}
	sum, err := s.f2RawAggregate(ctx, query.SchemaName, f, []string{"COUNT(*)"}, []bool{false})
	if err != nil {
		return 0, fmt.Errorf("epoch indexed count failed: %w", err)
	}
	return sum[0], nil
}

func (s *FlatSQLStore) f2QueryEpochCoverage(query EpochRecordQuery) ([]EpochCoverageBucket, error) {
	ctx := s.f2ctx()
	if !format2.TypeHasEpoch(query.SchemaName) {
		return nil, nil
	}
	f, err := f2EpochFilter(query)
	if err != nil {
		return nil, err
	}
	targets, err := s.f2Targets(ctx, query.SchemaName, f.tag, false)
	if err != nil {
		return nil, err
	}
	type day struct {
		n        int64
		min, max int64
	}
	days := map[string]*day{}
	add := func(d string, n, lo, hi int64) {
		b := days[d]
		if b == nil {
			days[d] = &day{n: n, min: lo, max: hi}
			return
		}
		b.n += n
		if lo < b.min {
			b.min = lo
		}
		if hi > b.max {
			b.max = hi
		}
	}
	dayExpr := "strftime('%Y-%m-%d', " + f2EpochSecSQL + ", 'unixepoch')"
	if len(targets) == 1 && targets[0].table == "" {
		g := f.on(targets[0])
		res, err := s.ps.Query(ctx, format2.Request{SQL: fmt.Sprintf("SELECT %s AS d, COUNT(*), MIN(%s), MAX(%s) FROM %s%s GROUP BY d",
			dayExpr, f2EpochSecSQL, f2EpochSecSQL, targets[0].from(query.SchemaName), g.where()), Params: g.params})
		if err != nil && !format2.NoSuchType(err, query.SchemaName) {
			return nil, fmt.Errorf("epoch coverage query failed: %w", err)
		}
		if err == nil {
			for _, r := range res.Rows {
				add(r[0].String(), r[1].Int64(), r[2].Int64(), r[3].Int64())
			}
		}
	} else {
		// Partitions of a REPEAT-holding type: each record once.
		seen := map[string]bool{}
		for _, t := range targets {
			g := f.on(t)
			res, err := s.ps.Query(ctx, format2.Request{SQL: fmt.Sprintf("SELECT %s, %s, _cid FROM %s%s", dayExpr, f2EpochSecSQL,
				t.from(query.SchemaName), g.where()), Params: g.params})
			if err != nil {
				return nil, fmt.Errorf("epoch coverage query failed: %w", err)
			}
			for _, r := range res.Rows {
				if seen[r[2].String()] {
					continue
				}
				seen[r[2].String()] = true
				add(r[0].String(), 1, r[1].Int64(), r[1].Int64())
			}
		}
	}
	names := make([]string, 0, len(days))
	for d := range days {
		names = append(names, d)
	}
	sort.Strings(names)
	out := make([]EpochCoverageBucket, 0, len(names))
	for _, d := range names {
		b := days[d]
		out = append(out, EpochCoverageBucket{Day: d, Count: b.n, OldestEpoch: time.Unix(b.min, 0).UTC(), NewestEpoch: time.Unix(b.max, 0).UTC()})
	}
	return out, nil
}

// ---- summaries and counters ------------------------------------------------------------

func (s *FlatSQLStore) f2DataSummary() (*DataSummary, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	ctx := s.f2ctx()
	summary := &DataSummary{Schemas: make([]DataSchemaSummary, 0), Sources: make([]DataSourceSummary, 0)}
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return nil, fmt.Errorf("summarize source-backed producers: %w", err)
	}
	laneSchemas := map[string]*DataSchemaSummary{}
	sources := map[DataSourceSummary]*DataSourceSummary{}
	var sourceKeys []DataSourceSummary
	for _, l := range lanes {
		if !l.Tuple {
			continue
		}
		schema := l.Type + ".fbs"
		ls := laneSchemas[schema]
		if ls == nil {
			ls = &DataSchemaSummary{SchemaName: schema}
			laneSchemas[schema] = ls
		}
		ls.Count += l.Count
		ls.TotalBytes += l.Bytes
		k := DataSourceSummary{SchemaName: schema, ProviderID: l.Provider, SourceName: l.Source, BatchID: l.Batch,
			ProducerPeerID: l.Peer, ProducerPublicKey: l.PubKey}
		if sources[k] == nil {
			c := k
			sources[k] = &c
			sourceKeys = append(sourceKeys, k)
		}
		sources[k].Count += l.Count
		sources[k].TotalBytes += l.Bytes
	}
	parts, err := s.ps.Partitions(ctx)
	if err != nil {
		return nil, err
	}
	partSchemas := map[string]*DataSchemaSummary{}
	for _, p := range parts {
		schema := p.Type + ".fbs"
		ps := partSchemas[schema]
		if ps == nil {
			ps = &DataSchemaSummary{SchemaName: schema}
			partSchemas[schema] = ps
		}
		ps.Count += p.Live
		ps.TotalBytes += p.LiveBytes
	}
	var names []string
	for n := range laneSchemas {
		names = append(names, n)
	}
	for n := range partSchemas {
		if laneSchemas[n] == nil {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		// A schema with lanes is summarized from them (the legacy source
		// summary); a schema without, from its partitions' counters.
		src := laneSchemas[n]
		if src == nil || src.Count <= 0 {
			src = partSchemas[n]
		}
		if src == nil || src.Count <= 0 {
			continue
		}
		summary.Schemas = append(summary.Schemas, *src)
		summary.TotalRecords += src.Count
		summary.TotalBytes += src.TotalBytes
	}
	sort.Slice(sourceKeys, func(i, j int) bool {
		a, b := sourceKeys[i], sourceKeys[j]
		for _, c := range [][2]string{{a.SchemaName, b.SchemaName}, {a.ProviderID, b.ProviderID}, {a.SourceName, b.SourceName},
			{a.BatchID, b.BatchID}, {a.ProducerPeerID, b.ProducerPeerID}, {a.ProducerPublicKey, b.ProducerPublicKey}} {
			if c[0] != c[1] {
				return c[0] < c[1]
			}
		}
		return false
	})
	for _, k := range sourceKeys {
		if sources[k].Count > 0 {
			summary.Sources = append(summary.Sources, *sources[k])
		}
	}
	s.mu.RLock()
	localCount, localBytes, err := s.localEPMSummaryLocked()
	s.mu.RUnlock()
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

func (s *FlatSQLStore) f2SourceBatchProgress() ([]SourceBatchProgress, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	lanes, err := s.f2Lanes(s.f2ctx())
	if err != nil {
		return nil, fmt.Errorf("query source batch progress: %w", err)
	}
	type key struct{ schema, provider, source, batch string }
	agg := map[key]*SourceBatchProgress{}
	var keys []key
	for _, l := range lanes {
		if !l.Tuple {
			continue
		}
		k := key{l.Type + ".fbs", l.Provider, l.Source, l.Batch}
		p := agg[k]
		if p == nil {
			p = &SourceBatchProgress{SchemaName: k.schema, ProviderID: k.provider, SourceName: k.source, BatchID: k.batch}
			agg[k] = p
			keys = append(keys, k)
		}
		p.Count += l.Count
		p.TotalBytes += l.Bytes
		if l.FirstSeenMs > 0 {
			fs := format2.EpochSeconds(l.FirstSeenMs)
			if p.FirstSeenUnix == 0 || fs < p.FirstSeenUnix {
				p.FirstSeenUnix = fs
			}
		}
		p.UpdatedAtUnix = max(p.UpdatedAtUnix, format2.EpochSeconds(l.UpdatedMs))
		p.LastSeenUnix = p.UpdatedAtUnix
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.schema != b.schema {
			return a.schema < b.schema
		}
		if a.provider != b.provider {
			return a.provider < b.provider
		}
		if a.source != b.source {
			return a.source < b.source
		}
		return a.batch < b.batch
	})
	out := make([]SourceBatchProgress, 0, len(keys))
	for _, k := range keys {
		if agg[k].Count > 0 {
			out = append(out, *agg[k])
		}
	}
	return out, nil
}

func (s *FlatSQLStore) f2ProducerSourceProgress() ([]ProducerSourceProgress, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	lanes, err := s.f2Lanes(s.f2ctx())
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
		if !l.Tuple {
			continue
		}
		k := key{l.Peer, l.Type + ".fbs", l.Provider, l.Source}
		a := agg[k]
		if a == nil {
			a = &acc{p: ProducerSourceProgress{ProducerPeerID: k.peer, SchemaName: k.schema, ProviderID: k.provider, SourceName: k.source},
				batches: map[string]int64{}, batchCnt: map[string]int64{}}
			agg[k] = a
		}
		a.p.Count += l.Count
		a.p.TotalBytes += l.Bytes
		if l.FirstSeenMs > 0 {
			fs := format2.EpochSeconds(l.FirstSeenMs)
			if a.p.FirstSeenUnix == 0 || fs < a.p.FirstSeenUnix {
				a.p.FirstSeenUnix = fs
			}
		}
		up := format2.EpochSeconds(l.UpdatedMs)
		a.p.UpdatedAtUnix = max(a.p.UpdatedAtUnix, up)
		a.p.LastSeenUnix = a.p.UpdatedAtUnix
		a.batches[l.Batch] = max(a.batches[l.Batch], up)
		a.batchCnt[l.Batch] += l.Count
	}
	out := make([]ProducerSourceProgress, 0, len(agg))
	for _, a := range agg {
		if a.p.Count <= 0 {
			continue
		}
		var best string
		var bestAt int64 = -1
		for b, at := range a.batches {
			if a.batchCnt[b] <= 0 {
				continue
			}
			a.p.BatchCount++
			if at > bestAt || (at == bestAt && b > best) {
				best, bestAt = b, at
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

func (s *FlatSQLStore) f2SourceRecordCounts() (map[string]int64, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	lanes, err := s.f2Lanes(s.f2ctx())
	if err != nil {
		return nil, fmt.Errorf("count records per source: %w", err)
	}
	counts := map[string]int64{}
	for _, l := range lanes {
		if !l.Tuple || l.Count <= 0 {
			continue
		}
		if key := sourceCountKey(l.Provider, l.Source); key != "" {
			counts[key] += l.Count
		}
	}
	return counts, nil
}

func (s *FlatSQLStore) f2LiveRecordBytes() (int64, error) {
	if err := s.f2Closed(); err != nil {
		return 0, err
	}
	return s.ps.LiveRecordBytes(s.f2ctx())
}

// f2DiskUsageBytes is §13's quota input: Σ disk_bytes of the partitions, plus
// the control instance's files and the auxiliary journal.
func (s *FlatSQLStore) f2DiskUsageBytes() (int64, error) {
	if err := s.f2Closed(); err != nil {
		return 0, err
	}
	total, err := s.ps.DiskUsageBytes(s.f2ctx())
	if err != nil {
		return 0, err
	}
	for _, path := range []string{s.controlDBPath, s.controlDBPath + "-journal", filepath.Join(s.basePath, auxiliaryMetadataFileName)} {
		if fi, err := statSize(path); err == nil {
			total += fi
		}
	}
	return total, nil
}

func (s *FlatSQLStore) f2PeerStorageBytes(peerID string) (int64, error) {
	if err := s.f2Closed(); err != nil {
		return 0, err
	}
	return s.ps.PeerStorageBytes(s.f2ctx(), sanitizeProducerID(routedProducerID(peerID)))
}

func (s *FlatSQLStore) f2SchemaDateRanges() ([]SchemaDateRange, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	ranges, err := s.ps.SchemaDateRanges(s.f2ctx())
	if err != nil {
		return nil, err
	}
	out := make([]SchemaDateRange, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, SchemaDateRange{Schema: r.Schema, RecordCount: r.RecordCount, OldestEpoch: r.OldestEpoch,
			NewestEpoch: r.NewestEpoch, TotalBytes: r.TotalBytes})
	}
	return out, nil
}

func (s *FlatSQLStore) f2Count(schemaName string) (int64, error) {
	if err := s.f2Closed(); err != nil {
		return 0, err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}
	tc, err := s.ps.TypeCounterOf(schemaName)
	if err != nil {
		return 0, fmt.Errorf("failed to count: %w", err)
	}
	return tc.FirstLive, nil
}

// ---- simple record lists ---------------------------------------------------------------

func (s *FlatSQLStore) f2QueryData(schemaName, whereClause string, limit int, maxTotalBytes int) ([][]byte, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if strings.TrimSpace(whereClause) != "" {
		return nil, fmt.Errorf("%w: a SQL WHERE over the legacy record table (%q)", ErrFormat2Unsupported, whereClause)
	}
	sql := fmt.Sprintf("SELECT _data FROM %s ORDER BY _gseq DESC", format2.QuoteIdent(format2.TypeName(schemaName)))
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	res, err := s.ps.Query(s.f2ctx(), format2.Request{SQL: sql})
	if format2.NoSuchType(err, schemaName) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	var out [][]byte
	total := 0
	for _, row := range res.Rows {
		data, err := s.openStoredRecordBytes(schemaName, row[0].B)
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

// f2QueryRecentRecords is the newest records first (arrival order).
func (s *FlatSQLStore) f2QueryRecentRecords(schemaName string, limit int) ([]*Record, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 250000 {
		limit = 250000
	}
	return s.f2Select(schemaName, fmt.Sprintf("SELECT %s FROM %s ORDER BY _gseq DESC LIMIT %d", format2.RecColumns,
		format2.QuoteIdent(format2.TypeName(schemaName)), limit), nil, true)
}

func (s *FlatSQLStore) f2QuerySourceTaggedRecords(query SourceTagQuery) ([]*Record, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	if _, err := sds.SchemaNameToTable(query.SchemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	ctx := s.f2ctx()
	f := &f2RawFilter{tag: f2TagSpec{provider: strings.TrimSpace(query.ProviderID), source: strings.TrimSpace(query.SourceName),
		batch: strings.TrimSpace(query.BatchID)}}
	var targets []f2Target
	var err error
	if f.tag.empty() {
		// Every tagged record: one target per lane tuple of the type.
		targets, err = s.f2TaggedTargets(ctx, query.SchemaName)
	} else {
		targets, err = s.f2Targets(ctx, query.SchemaName, f.tag, false)
	}
	if err != nil {
		return nil, err
	}
	recs, err := s.f2RawPage(ctx, query.SchemaName, &f2RawFilter{}, targets, "gseq desc", query.Limit, 0, true)
	if err != nil {
		return nil, fmt.Errorf("failed to query source tagged records: %w", err)
	}
	for _, r := range recs {
		r.SourceTags, r.RowID, r.RecordLength, r.MaterializedAt = SourceTags{}, 0, 0, time.Time{}
	}
	return recs, nil
}

// f2TaggedTargets is one target per lane tuple of a type (every tagged
// record), per partition while the type holds REPEAT copies.
func (s *FlatSQLStore) f2TaggedTargets(ctx context.Context, schema string) ([]f2Target, error) {
	repeats, err := s.f2TypeHasRepeats(schema)
	if err != nil {
		return nil, err
	}
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := s.ps.PartitionsOf(schema)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, p := range parts {
		names[p.PID] = p.SQLName
	}
	typ := format2.TypeName(schema)
	var out []f2Target
	seen := map[f2Target]bool{}
	for _, l := range lanes {
		if l.Type != typ || !l.Tuple || l.Count <= 0 {
			continue
		}
		t := f2Target{tag: f2TagSpec{provider: l.Provider, source: l.Source, batch: l.Batch, peer: l.Peer}}
		if repeats {
			if t.table = names[l.PID]; t.table == "" {
				continue
			}
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// f2GetSourceTags returns a record's most recently updated live tag (the
// legacy answer is its newest tag row): the lane tuples of its FIRST copy's
// partition, newest first, each probed as a tag condition on that copy.
func (s *FlatSQLStore) f2GetSourceTags(schemaName, cid string) (SourceTags, error) {
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return SourceTags{}, fmt.Errorf("invalid schema name: %w", err)
	}
	bin, err := format2.CIDFromText(cid)
	if err != nil {
		return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
	}
	ctx := s.f2ctx()
	r, err := s.ps.GetRecord(ctx, schemaName, cid)
	if errors.Is(err, format2.ErrNotFound) {
		return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
	}
	if err != nil {
		return SourceTags{}, fmt.Errorf("failed to get source tags: %w", err)
	}
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return SourceTags{}, err
	}
	parts, err := s.ps.PartitionsOf(schemaName)
	if err != nil {
		return SourceTags{}, err
	}
	var sqlName string
	var pid int64 = -1
	for _, p := range parts {
		if p.Producer == r.Producer {
			sqlName, pid = p.SQLName, p.PID
		}
	}
	var mine []format2.LaneCounter
	for _, l := range lanes {
		if l.PID == pid && l.Tuple && l.Count > 0 {
			mine = append(mine, l)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].UpdatedMs > mine[j].UpdatedMs })
	for _, l := range mine {
		if sqlName == "" {
			break
		}
		res, err := s.ps.QueryPoint(ctx, format2.Request{SQL: fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE _cid_bin = ?1 AND _provider = ?2 AND _source_name = ?3 AND _batch = ?4 AND _peer_id = ?5`,
			format2.QuoteIdent(sqlName)), Params: []format2.Cell{format2.Blob(bin), format2.Text(l.Provider), format2.Text(l.Source), format2.Text(l.Batch), format2.Text(l.Peer)}})
		if err != nil {
			return SourceTags{}, fmt.Errorf("failed to get source tags: %w", err)
		}
		if res.Rows[0][0].Int64() > 0 {
			return SourceTags{ProviderID: l.Provider, SourceName: l.Source, BatchID: l.Batch, ProducerPeerID: l.Peer, ProducerPublicKey: l.PubKey}, nil
		}
	}
	if r.Provider == "" && r.Source == "" {
		return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
	}
	tags := SourceTags{ProviderID: r.Provider, SourceName: r.Source, BatchID: r.Batch}
	if pk, ok := f2TupleIndex(lanes)[f2TupleKey{r.Producer, format2.TypeName(schemaName), r.Provider, r.Source, r.Batch}]; ok {
		tags.ProducerPeerID, tags.ProducerPublicKey = pk[0], pk[1]
	}
	return tags, nil
}

// f2DistinctBatches lists the live batches of a (provider, source) lane of a
// schema (from the lane counters).
func (s *FlatSQLStore) f2DistinctBatches(schemaName, providerID, sourceName string) ([]string, error) {
	lanes, err := s.f2Lanes(s.f2ctx())
	if err != nil {
		return nil, err
	}
	typ := format2.TypeName(schemaName)
	seen := map[string]bool{}
	var out []string
	for _, l := range lanes {
		if l.Type != typ || !l.Tuple || l.Count <= 0 || l.Provider != providerID || l.Source != sourceName || seen[l.Batch] {
			continue
		}
		seen[l.Batch] = true
		out = append(out, l.Batch)
	}
	sort.Strings(out)
	return out, nil
}

// f2PublicationSetFingerprint is DatasetPublicationSetFingerprint: the
// sorted text CIDs of the records a (provider, source[, batch]) tag
// selects, hashed as format 1 hashes them.
func (s *FlatSQLStore) f2PublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error) {
	ctx := s.f2ctx()
	q := format2.WindowQuery{Schema: schemaName, Provider: providerID, Source: sourceName, Batch: batchID}
	if err := s.f2ByBatch(ctx, &q); err != nil {
		return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	tag := f2TagSpec{provider: providerID, source: sourceName, batch: batchID}
	targets, err := s.f2Targets(ctx, schemaName, tag, false)
	if err != nil {
		return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	seen := map[string]bool{}
	for _, t := range targets {
		sq := q
		sq.Table = t.table
		cids, err := s.ps.WindowCIDs(ctx, sq)
		if err != nil {
			return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
		}
		for _, c := range cids {
			seen[c] = true
		}
	}
	sorted := make([]string, 0, len(seen))
	for c := range seen {
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)
	hash := sha256.New()
	fmt.Fprintf(hash, "sdn-dataset-publication-set-v1\x00%s\x00%s\x00%s\x00%s\n", schemaName, providerID, sourceName, batchID)
	for _, c := range sorted {
		hash.Write([]byte(c))
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil)), len(sorted), nil
}

// f2SourceTagsForCIDs is sourceTagsForCIDs for the export: the records are
// read in batches (no payload) and each takes its FIRST copy's own tag, its
// producer peer and key from the lane tuple. When the export selected one
// lane tuple (prefer), every record it exported carries that tuple, and
// that is the tag it reports. A record whose only tags are RETAGs reports
// none (format 1 reported its newest tag row).
func (s *FlatSQLStore) f2SourceTagsForCIDs(schemaName string, cids []string, prefer f2TagSpec) (map[string]SourceTags, error) {
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	ctx := s.f2ctx()
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return nil, err
	}
	typ := format2.TypeName(schemaName)
	var only *SourceTags
	if !prefer.empty() {
		uniq := map[SourceTags]bool{}
		for _, l := range lanes {
			if l.Type == typ && prefer.matches(l) {
				uniq[SourceTags{ProviderID: l.Provider, SourceName: l.Source, BatchID: l.Batch, ProducerPeerID: l.Peer, ProducerPublicKey: l.PubKey}] = true
			}
		}
		if len(uniq) == 1 {
			for t := range uniq {
				t := t
				only = &t
			}
		}
	}
	tuples := f2TupleIndex(lanes)
	out := make(map[string]SourceTags, len(cids))
	unique := dedupeStrings(cids)
	for start := 0; start < len(unique); start += 256 {
		end := min(start+256, len(unique))
		var params []format2.Cell
		var marks []string
		for _, c := range unique[start:end] {
			bin, err := format2.CIDFromText(c)
			if err != nil {
				continue
			}
			params = append(params, format2.Blob(bin))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		if len(params) == 0 {
			continue
		}
		res, err := s.ps.QueryPoint(ctx, format2.Request{SQL: fmt.Sprintf("SELECT %s FROM %s WHERE _cid_bin IN (%s)", format2.RecColumnsMeta,
			format2.QuoteIdent(typ), strings.Join(marks, ",")), Params: params})
		if format2.NoSuchType(err, schemaName) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("query source tags: %w", err)
		}
		for _, row := range res.Rows {
			r := format2.RecFromRow(row)
			if only != nil {
				out[r.CID] = *only
				continue
			}
			if r.Provider == "" && r.Source == "" {
				continue
			}
			t := SourceTags{ProviderID: r.Provider, SourceName: r.Source, BatchID: r.Batch}
			if pk, ok := tuples[f2TupleKey{r.Producer, typ, r.Provider, r.Source, r.Batch}]; ok {
				t.ProducerPeerID, t.ProducerPublicKey = pk[0], pk[1]
			}
			out[r.CID] = t
		}
	}
	return out, nil
}

// f2FullTableCursorKey names the one cursor a format-2 full-table page
// keeps: the gseq it continues below (every copy of a type shares one
// arrivals order).
const f2FullTableCursorKey = "gseq"

// f2FullTablePage is FullTablePageWithCursor: newest arrivals first, below
// the cursor's gseq, optionally one source's records.
func (s *FlatSQLStore) f2FullTablePage(query FullTablePageQuery) (FullTablePageResult, error) {
	if err := s.f2Closed(); err != nil {
		return FullTablePageResult{}, err
	}
	if _, err := sds.SchemaNameToTable(query.SchemaName); err != nil {
		return FullTablePageResult{}, fmt.Errorf("invalid schema name: %w", err)
	}
	ctx := s.f2ctx()
	before := query.BeforeRowID
	if v, ok := query.Cursor[f2FullTableCursorKey]; ok && v > 0 {
		before = v
	}
	f := &f2RawFilter{tag: f2TagSpec{source: strings.TrimSpace(query.SourceName)}}
	if before > 0 {
		f.add("_gseq < ?", format2.Int(before))
	}
	f.add("_gseq > 0")
	targets, err := s.f2Targets(ctx, query.SchemaName, f.tag, false)
	if err != nil {
		return FullTablePageResult{}, err
	}
	recs, err := s.f2RawPage(ctx, query.SchemaName, f, targets, "gseq desc", query.Limit, query.Offset, !query.metadataOnly)
	if err != nil {
		return FullTablePageResult{}, fmt.Errorf("full table page failed: %w", err)
	}
	next := cloneFullTableCursor(query.Cursor)
	if len(recs) > 0 {
		next = FullTablePageCursor{f2FullTableCursorKey: recs[len(recs)-1].RowID}
	}
	for _, r := range recs {
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
	if recs == nil {
		recs = []*Record{}
	}
	return FullTablePageResult{Records: recs, NextCursor: next}, nil
}

// ---- module and public SQL (engine_query.go, sandbox_select.go) ------------------------
//
// Trusted module SQL (storage.flatsql_query_stream) runs on the interactive
// lanes, moved to the bulk lanes when unbounded; untrusted SQL (/api/v1/query,
// the table API, the public query flow) runs under the sandbox flag: the
// authorizer, one SELECT, the work budget (A28), on an interactive lane when
// its plan is bounded, else on the sandbox lanes. The record relations keep
// their names (<TYPE>, <TYPE>@<source>, _source = '<TYPE>@<source_name>',
// A18); the result framing is the legacy engine's.

// f2Cells converts driver-style parameters to RB1 cells.
func f2Cells(params []interface{}) ([]format2.Cell, error) {
	out := make([]format2.Cell, 0, len(params))
	for i, p := range params {
		switch v := p.(type) {
		case nil:
			out = append(out, format2.Null())
		case int:
			out = append(out, format2.Int(int64(v)))
		case int32:
			out = append(out, format2.Int(int64(v)))
		case int64:
			out = append(out, format2.Int(v))
		case uint32:
			out = append(out, format2.Int(int64(v)))
		case uint64:
			if v > 1<<63-1 {
				return nil, fmt.Errorf("parameter %d: %d does not fit an SQL integer", i+1, v)
			}
			out = append(out, format2.Int(int64(v)))
		case bool:
			if v {
				out = append(out, format2.Int(1))
			} else {
				out = append(out, format2.Int(0))
			}
		case float32:
			out = append(out, format2.Real(float64(v)))
		case float64:
			out = append(out, format2.Real(v))
		case string:
			out = append(out, format2.Text(v))
		case []byte:
			out = append(out, format2.Blob(v))
		default:
			return nil, fmt.Errorf("parameter %d: unsupported type %T", i+1, p)
		}
	}
	return out, nil
}

// f2SandboxError maps a sandboxed statement's status to the legacy typed
// rejection (the flows map its code to an HTTP status).
func f2SandboxError(err error) error {
	var se *format2.StatusError
	if !errors.As(err, &se) {
		if errors.Is(err, context.DeadlineExceeded) {
			return &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeTimeout, Message: "sandbox: timeout: statement exceeded its deadline"}
		}
		return err
	}
	code := ""
	switch se.Status {
	case format2.StatusNotAuthorized:
		code = flatsqlrt.SandboxCodeNotAuthorized
	case format2.StatusTimeout, format2.StatusCancelled:
		code = flatsqlrt.SandboxCodeTimeout
	case format2.StatusTooLarge:
		code = flatsqlrt.SandboxCodeByteCap
	}
	if strings.HasPrefix(se.Msg, "sandbox: ") {
		rest := strings.TrimPrefix(se.Msg, "sandbox: ")
		if i := strings.IndexByte(rest, ':'); i > 0 {
			code = strings.TrimSpace(rest[:i])
		}
		return &flatsqlrt.SandboxError{Code: code, Message: se.Msg}
	}
	if code != "" {
		return &flatsqlrt.SandboxError{Code: code, Message: "sandbox: " + code + ": " + se.Msg}
	}
	return fmt.Errorf("SQL error: %s", se.Msg)
}

// f2Frames assembles a record stream from rows whose every cell is a BLOB
// ([u32le size][bytes] per cell, the legacy engine's framing).
func f2Frames(rows [][]format2.Cell) ([]byte, int, error) {
	var out []byte
	frames := 0
	for _, row := range rows {
		for _, c := range row {
			if c.Type != format2.CellBlob {
				return nil, 0, &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeNotRecordStream,
					Message: "sandbox: not-a-record-stream: raw response stream queries must return only BLOB cells (projection results are JSON-only — request format=json)"}
			}
			out = binary.LittleEndian.AppendUint32(out, uint32(len(c.B)))
			out = append(out, c.B...)
			if len(c.B) > 0 {
				frames++
			}
		}
	}
	return out, frames, nil
}

func f2RawStream(res *format2.Result) (*flatsqlrt.RawStream, error) {
	payload, frames, err := f2Frames(res.Rows)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		payload = []byte{}
	}
	return &flatsqlrt.RawStream{Bytes: payload, Rows: len(res.Rows), Columns: len(res.Names),
		FNV1a64: flatsqlrt.FNV1a64WordFolded(payload), FrameCount: frames}, nil
}

// f2QueryRawStream is QueryRawStream: trusted module SQL.
func (s *FlatSQLStore) f2QueryRawStream(sql string, params ...interface{}) (*flatsqlrt.RawStream, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	cells, err := f2Cells(params)
	if err != nil {
		return nil, err
	}
	res, err := s.ps.Query(s.f2ctx(), format2.Request{SQL: sql, Params: cells})
	if err != nil {
		var se *format2.StatusError
		if errors.As(err, &se) && strings.Contains(se.Msg, "no such table") {
			return &flatsqlrt.RawStream{Bytes: []byte{}}, nil // a type nothing was stored under yet
		}
		return nil, err
	}
	return f2RawStream(res)
}

func f2SandboxRequest(ctx context.Context, sql string, caps flatsqlrt.SandboxCaps, params []interface{}) (context.Context, context.CancelFunc, format2.Request, error) {
	cells, err := f2Cells(params)
	if err != nil {
		return nil, nil, format2.Request{}, err
	}
	req := format2.Request{SQL: sql, Params: cells, MaxResultBytes: caps.MaxBytes}
	if caps.MaxRows > 0 {
		req.MaxResultRows = caps.MaxRows + 1 // the extra row detects the cap (reject, never truncate)
	}
	cancel := func() {}
	if caps.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, caps.Timeout)
	}
	return ctx, cancel, req, nil
}

func f2RowCap(res *format2.Result, caps flatsqlrt.SandboxCaps) error {
	if caps.MaxRows > 0 && uint64(len(res.Rows)) > caps.MaxRows {
		return &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeRowCap,
			Message: fmt.Sprintf("sandbox: row-cap: result exceeds %d rows", caps.MaxRows)}
	}
	return nil
}

// f2QuerySandboxedStream is QuerySandboxedStream: untrusted SQL whose every
// cell is a BLOB.
func (s *FlatSQLStore) f2QuerySandboxedStream(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (*flatsqlrt.RawStream, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	ctx, cancel, req, err := f2SandboxRequest(s.f2ctx(), sql, caps, params)
	if err != nil {
		return nil, err
	}
	defer cancel()
	res, err := s.ps.SandboxQuery(ctx, req)
	if err != nil {
		return nil, f2SandboxError(err)
	}
	if err := f2RowCap(res, caps); err != nil {
		return nil, err
	}
	stream, err := f2RawStream(res)
	if err != nil {
		return nil, err
	}
	if caps.MaxBytes > 0 && uint64(len(stream.Bytes)) > caps.MaxBytes {
		return nil, &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeByteCap,
			Message: fmt.Sprintf("sandbox: byte-cap: result exceeds %d bytes", caps.MaxBytes)}
	}
	return stream, nil
}

// f2JSONEscape is the legacy engine's JSON string escape.
func f2JSONEscape(out []byte, s []byte) []byte {
	const hexdigits = "0123456789abcdef"
	for _, c := range s {
		switch {
		case c == '"' || c == '\\':
			out = append(out, '\\', c)
		case c == '\n':
			out = append(out, '\\', 'n')
		case c == '\r':
			out = append(out, '\\', 'r')
		case c == '\t':
			out = append(out, '\\', 't')
		case c < 0x20:
			out = append(out, '\\', 'u', '0', '0', hexdigits[c>>4], hexdigits[c&0xF])
		default:
			out = append(out, c)
		}
	}
	return out
}

// f2QuerySandboxedJSON is QuerySandboxedJSON: the rows as the legacy
// engine's bare JSON array of {"<column>": value} objects.
func (s *FlatSQLStore) f2QuerySandboxedJSON(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) ([]byte, int, int, error) {
	if err := s.f2Closed(); err != nil {
		return nil, 0, 0, err
	}
	ctx, cancel, req, err := f2SandboxRequest(s.f2ctx(), sql, caps, params)
	if err != nil {
		return nil, 0, 0, err
	}
	defer cancel()
	res, err := s.ps.SandboxQuery(ctx, req)
	if err != nil {
		return nil, 0, 0, f2SandboxError(err)
	}
	if err := f2RowCap(res, caps); err != nil {
		return nil, len(res.Rows), len(res.Names), err
	}
	keys := make([][]byte, len(res.Names))
	for i, n := range res.Names {
		k := []byte{','}
		if i == 0 {
			k[0] = '{'
		}
		k = append(k, '"')
		k = f2JSONEscape(k, []byte(n))
		keys[i] = append(k, '"', ':')
	}
	out := []byte{'['}
	for r, row := range res.Rows {
		if r > 0 {
			out = append(out, ',')
		}
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
			return nil, len(res.Rows), len(res.Names), &flatsqlrt.SandboxError{Code: flatsqlrt.SandboxCodeByteCap,
				Message: fmt.Sprintf("sandbox: byte-cap: result exceeds %d bytes", caps.MaxBytes)}
		}
	}
	out = append(out, ']')
	return validUTF8JSON(out), len(res.Rows), len(res.Names), nil
}

// f2PublicQuerySurface lists the queryable record relations: every routed
// standard, its records from the type head, and one <TYPE>@<source> relation
// per live source of a standard that holds records (from the lanes).
func (s *FlatSQLStore) f2PublicQuerySurface() ([]QuerySurfaceTable, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	ctx := s.f2ctx()
	types, err := s.ps.Types(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]int64{}
	for _, t := range types {
		live[t.Type] = t.FirstLive
	}
	lanes, err := s.f2Lanes(ctx)
	if err != nil {
		return nil, err
	}
	sources := map[string]map[string]int64{}
	for _, l := range lanes {
		if !l.Tuple || l.Count <= 0 || l.Source == "" {
			continue
		}
		if sources[l.Type] == nil {
			sources[l.Type] = map[string]int64{}
		}
		sources[l.Type][l.Source] += l.Count
	}
	routed := engineRoutedSchemaNames()
	surface := make([]QuerySurfaceTable, 0, len(routed))
	for _, schemaName := range routed {
		base := engineRoutedSchemas[schemaName].Table
		columns, ok := engineRelationColumns(base)
		if !ok {
			return nil, fmt.Errorf("engine schema declares no table %q for routed standard %s", base, schemaName)
		}
		var placeholders []string
		if field, junk := engineUnprojectableFirstFields[schemaName]; junk {
			placeholders = []string{field}
		}
		typ := format2.TypeName(schemaName)
		kind := "table"
		if len(sources[typ]) > 0 {
			kind = "view"
		}
		surface = append(surface, QuerySurfaceTable{Name: base, Kind: kind, Columns: columns, PlaceholderColumns: placeholders, Records: live[typ]})
		if live[typ] == 0 {
			continue
		}
		var names []string
		for src := range sources[typ] {
			names = append(names, src)
		}
		sort.Strings(names)
		for _, src := range names {
			surface = append(surface, QuerySurfaceTable{Name: base + "@" + src, Kind: "table", Source: src, Columns: columns,
				PlaceholderColumns: placeholders, Records: sources[typ][src]})
		}
	}
	return surface, nil
}

// f2SandboxedSelect is SandboxedSelect's statement on the sandbox path; the
// caller validated the statement and resolved the caps.
func (s *FlatSQLStore) f2SandboxedSelect(ctx context.Context, stmt string, maxRows, maxBytes int, timeout time.Duration) (*SandboxSelectResult, error) {
	if err := s.f2Closed(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := s.ps.SandboxQuery(ctx, format2.Request{SQL: stmt, MaxResultRows: uint64(maxRows) + 1})
	if err != nil {
		return nil, fmt.Errorf("sandboxed select: %w", f2SandboxError(err))
	}
	out := &SandboxSelectResult{Columns: res.Names}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	bytesUsed := 0
	for _, r := range res.Rows {
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			break
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
			break
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// f2Unsupported is the answer of a record API the format-2 store does not
// serve yet: an error that names it, never an empty result.
func f2Unsupported(what string) error {
	return fmt.Errorf("%w: %s", ErrFormat2Unsupported, what)
}

func statSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
