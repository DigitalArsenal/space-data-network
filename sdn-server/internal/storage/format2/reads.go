package format2

// Record reads on lanes (design §8, §9, A16, A17, A18, A20, A28; T6 scope 4).
// Every read here is a statement on a reader instance: it reads committed
// files only and waits on no writer and no store lock (reads-never-wait
// law). Counters come from partition and type heads, never from a scan.
// Trusted reads run on the interactive lanes and move to the bulk lanes only
// when the engine says the plan is unbounded (FLATSQL_NEEDS_BULK).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ErrNotFound: no live record with that CID.
var ErrNotFound = errors.New("format2: record not found")

// Rec is one record read back.
type Rec struct {
	CID       string
	CIDBin    []byte
	Gseq      int64 // the datasync v1 cursor (0 when the copy is not labeled yet)
	Pseq      int64
	PeerID    string
	Arrival   time.Time
	EpochMs   int64
	Data      []byte // the stored FlatBuffer (sealed bytes for an (encrypted) standard)
	Signature []byte
	Source    string // source_name of the copy's tag ("" untagged)
	Provider  string
	Batch     string
	Producer  string // the partition's producer token
	Length    int64  // record bytes (the legacy record_length)
}

const recColumns = `_cid_bin, _gseq, _pseq, _peer_id, _arrival, _epoch, _data, _signature, _source_name, _provider, _batch, _producer, _len`

func recFromRow(row []Cell) Rec {
	r := Rec{CIDBin: row[0].B, Gseq: row[1].Int64(), Pseq: row[2].Int64(), PeerID: string(row[3].B),
		Arrival: time.UnixMilli(row[4].Int64()), EpochMs: row[5].Int64(), Data: row[6].B, Signature: row[7].B,
		Source: row[8].String(), Provider: row[9].String(), Batch: row[10].String(), Producer: row[11].String()}
	r.CID = CIDText(r.CIDBin)
	if n := row[12].Int64(); n >= 4 {
		r.Length = n - 4
	}
	return r
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func typeName(schema string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(schema), ".")
	return strings.ToUpper(name)
}

// GetRecord returns the live record with this CID (its FIRST copy). A record
// acked but not yet labeled by its type owner is found in its partition
// (A20: read-your-writes at ack; partition-level visibility is the acked
// HWM).
func (s *Store) GetRecord(ctx context.Context, schema, cidText string) (*Rec, error) {
	c, err := CIDFromText(cidText)
	if err != nil {
		return nil, err
	}
	typ := typeName(schema)
	res, err := s.point(ctx, Request{SQL: fmt.Sprintf(`SELECT %s FROM %s WHERE _cid_bin = ?1`, recColumns, quoteIdent(typ)),
		Params: []Cell{Blob(c)}})
	if noSuchType(err, schema) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, cidText)
	}
	if err != nil {
		return nil, err
	}
	if len(res.Rows) > 0 {
		r := recFromRow(res.Rows[0])
		return &r, nil
	}
	// Not labeled yet: its partition holds it (the partitions from the
	// heads, no lane).
	parts, err := s.PartitionsOf(schema)
	if err != nil {
		return nil, err
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].SQLName < parts[j].SQLName })
	for _, p := range parts {
		res, err := s.point(ctx, Request{SQL: fmt.Sprintf(`SELECT %s FROM %s WHERE _cid_bin = ?1`, recColumns, quoteIdent(p.SQLName)),
			Params: []Cell{Blob(c)}})
		if err != nil {
			return nil, err
		}
		if len(res.Rows) > 0 {
			r := recFromRow(res.Rows[0])
			return &r, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, cidText)
}

// PartitionCounter is one partition's head counters (§4.4).
type PartitionCounter struct {
	PID                                       int64
	Producer, SQLName, Type                   string
	CommitSeq, PseqHi                         int64
	Total, TotalBytes, Live, LiveBytes, Tombs int64
	DiskBytes                                 int64
	MinEpoch, MaxEpoch, LatestArrival         int64
	Quarantined                               bool
}

// Partitions returns every partition's counters, read from the heads
// without a lane (A28).
func (s *Store) Partitions(ctx context.Context) ([]PartitionCounter, error) {
	return s.heads.Partitions()
}

// LiveRecordBytes is Σ live_bytes over partitions: the bytes each partition
// holds, a record held by two producers counted once per partition (the
// legacy semantics, §16.2). From the heads: no lane, no scan.
func (s *Store) LiveRecordBytes(ctx context.Context) (int64, error) {
	parts, err := s.heads.Partitions()
	if err != nil {
		return 0, err
	}
	var n int64
	for _, p := range parts {
		n += p.LiveBytes
	}
	return n, nil
}

// DiskUsageBytes is Σ disk_bytes over partitions (§13: the quota's input;
// on-disk bytes shrink after compaction).
func (s *Store) DiskUsageBytes(ctx context.Context) (int64, error) {
	parts, err := s.heads.Partitions()
	if err != nil {
		return 0, err
	}
	var n int64
	for _, p := range parts {
		n += p.DiskBytes
	}
	return n, nil
}

// PeerStorageBytes is Σ live_bytes of a producer's partitions.
func (s *Store) PeerStorageBytes(ctx context.Context, producerToken string) (int64, error) {
	parts, err := s.heads.Partitions()
	if err != nil {
		return 0, err
	}
	var n int64
	for _, p := range parts {
		if p.Producer == producerToken {
			n += p.LiveBytes
		}
	}
	return n, nil
}

// TypeCounter is one type's head (A16: MaxRowID = gseq_hi, TotalCount =
// first_live_count, and the SnapshotID inputs).
type TypeCounter struct {
	Type, Schema                                               string
	CommitSeq, GseqHi, Arrivals, FirstLive, FirstLiveBytes, NP int64
}

// Types returns every type's head counters (from the heads, no lane).
func (s *Store) Types(ctx context.Context) ([]TypeCounter, error) {
	return s.heads.Types()
}

// WindowQuery is an indexed record window (storage.IndexedRecordQuery).
type WindowQuery struct {
	Schema     string
	NoradCatID *uint32
	EntityID   string
	Provider   string
	Source     string
	Batch      string
	From, To   *time.Time // on the record epoch in whole seconds, both inclusive (the legacy window_at)
	Limit      int
	Offset     int
	// Order: "" is the default order (epoch seconds DESC, text CID ASC,
	// A17/A19); "gseq" is arrival order; "cid" is text CID order.
	Order string
	// Conds are further conditions (the legacy index's object type, status,
	// epoch day, entity column per type, ...).
	Conds []Cond
	// Table reads one partition (its sql_name) instead of the type's
	// fan-out: every copy it holds, FIRST or REPEAT, with its own tags.
	Table string
	// EpochRanges are further half-open [lo, hi) ranges on the record epoch
	// in milliseconds (a UTC day): an EPOCH_CID range scan when nothing
	// else drives the plan.
	EpochRanges [][2]int64
	// ByBatch drives a (source, batch) window by its batch: the source
	// plan walks every posting of the source in epoch order, the batch plan
	// only the batch's. The provider and source conditions are checked on
	// each match (its own tag, else its live tag instances by CID). The
	// caller sets it when the batch's lanes are about the window's own.
	ByBatch bool
}

func (q WindowQuery) table() string {
	if q.Table != "" {
		return quoteIdent(q.Table)
	}
	return quoteIdent(typeName(q.Schema))
}

// Cond is one extra condition: SQL with unnumbered ? placeholders (numbered
// in order when the statement is built; the SQL holds no ? of its own) and
// its parameters. Indexed marks a condition an index drives (a COL or tag
// EQ): a window with one reads in epoch order from that index.
type Cond struct {
	SQL     string
	Params  []Cell
	Indexed bool
}

// legacyWindowOrder is the legacy window order: whole epoch seconds DESC
// (floored), then text CID ASC (A17, A19). The engine's default plan emits
// exactly this order (EPOCH_CID); a plan driven by another index does not,
// and sorts its matches.
const legacyWindowOrder = ` ORDER BY (CASE WHEN _epoch >= 0 THEN _epoch / 1000 ELSE -((999 - _epoch) / 1000) END) DESC, _cid ASC`

func (q WindowQuery) where() (string, []Cell, bool) {
	var conds []string
	var params []Cell
	indexed := false
	add := func(cond string, c Cell) {
		params = append(params, c)
		conds = append(conds, fmt.Sprintf(cond, len(params)))
	}
	if q.NoradCatID != nil {
		add("NORAD_CAT_ID = ?%d", Int(int64(*q.NoradCatID)))
		indexed = true
	}
	if q.EntityID != "" {
		col := "OBJECT_ID"
		if typeName(q.Schema) == "MPE" {
			col = "ENTITY_ID"
		}
		add(col+" = ?%d", Text(q.EntityID))
		indexed = true
	}
	if q.Provider != "" {
		add("_provider = ?%d", Text(q.Provider))
		indexed = true
	}
	if q.Source != "" {
		add("_source_name = ?%d", Text(q.Source))
		indexed = true
	}
	if q.Batch != "" {
		add("_batch = ?%d", Text(q.Batch))
		indexed = true
	}
	for _, c := range q.Conds {
		var b strings.Builder
		pi := 0
		for i := 0; i < len(c.SQL); i++ {
			if c.SQL[i] != '?' || pi >= len(c.Params) {
				b.WriteByte(c.SQL[i])
				continue
			}
			params = append(params, c.Params[pi])
			pi++
			fmt.Fprintf(&b, "?%d", len(params))
		}
		conds = append(conds, "("+b.String()+")")
		if c.Indexed {
			indexed = true
		}
	}
	// A time range alone is an EPOCH_CID range scan: the default plan's
	// order (the legacy order), second-granular keys, the exact millisecond
	// bounds applied by SQLite. With an index-driven plan it narrows the
	// matches.
	if q.From != nil {
		add("_epoch >= ?%d", Int(q.From.Unix()*1000))
	}
	if q.To != nil {
		add("_epoch < ?%d", Int((q.To.Unix()+1)*1000))
	}
	for _, r := range q.EpochRanges {
		add("_epoch >= ?%d", Int(r[0]))
		add("_epoch < ?%d", Int(r[1]))
	}
	if len(conds) == 0 {
		return "", nil, false
	}
	return " WHERE " + strings.Join(conds, " AND "), params, indexed
}

func (q WindowQuery) sql(cols string) (string, []Cell) {
	where, params, indexed := q.where()
	order := ""
	switch {
	case q.Order == "gseq":
		order = " ORDER BY _gseq"
	case q.Order == "cid":
		order = " ORDER BY _cid"
	case indexed:
		order = legacyWindowOrder
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	sql := fmt.Sprintf("SELECT %s FROM %s%s%s LIMIT %d", cols, q.table(), where, order, limit)
	if q.Offset > 0 {
		sql += fmt.Sprintf(" OFFSET %d", q.Offset)
	}
	return sql, params
}

// noSuchType reports the error of a statement over a type no partition
// has registered yet: that type holds no record.
func noSuchType(err error, schema string) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == StatusSQLError && strings.Contains(se.Msg, "no such table: "+typeName(schema))
}

// Window returns an indexed record window.
func (s *Store) Window(ctx context.Context, q WindowQuery) ([]Rec, error) {
	rows, err := s.windowRows(ctx, q, recColumns)
	if err != nil {
		return nil, err
	}
	out := make([]Rec, len(rows))
	for i, row := range rows {
		out[i] = recFromRow(row)
	}
	return out, nil
}

// WindowMeta is Window without the payloads (_data projects NULL): the byte
// probes read the lengths only.
func (s *Store) WindowMeta(ctx context.Context, q WindowQuery) ([]Rec, error) {
	rows, err := s.windowRows(ctx, q, RecColumnsMeta)
	if err != nil {
		return nil, err
	}
	out := make([]Rec, len(rows))
	for i, row := range rows {
		out[i] = recFromRow(row)
	}
	return out, nil
}

// WindowRow is a window row with one extra projected expression.
type WindowRow struct {
	Rec   Rec
	Extra Cell
	Table string // the statement target the row came from ("" the type)
}

// WindowColumns returns a window without payloads and with one extra column
// expression per row (a root field, e.g. NORAD_CAT_ID).
func (s *Store) WindowColumns(ctx context.Context, q WindowQuery, extra string) ([]WindowRow, error) {
	rows, err := s.windowRows(ctx, q, RecColumnsMeta+", "+extra)
	if err != nil {
		return nil, err
	}
	out := make([]WindowRow, len(rows))
	for i, row := range rows {
		out[i] = WindowRow{Rec: recFromRow(row), Extra: row[13]}
	}
	return out, nil
}

// Count counts a window's matches (its conditions, no order, no limit).
func (s *Store) Count(ctx context.Context, q WindowQuery) (int64, error) {
	if q.byBatch() {
		var n int64
		err := s.matches(ctx, q, func([]byte, int64) error { n++; return nil })
		return n, err
	}
	where, params, _ := q.where()
	res, err := s.query(ctx, Request{SQL: fmt.Sprintf("SELECT COUNT(*) FROM %s%s", q.table(), where), Params: params})
	if noSuchType(err, q.Schema) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return res.Rows[0][0].Int64(), nil
}

// WindowCIDs returns the CIDs a window's conditions match (no order, no
// limit, no payload): the distinct-record count of a union of partitions.
func (s *Store) WindowCIDs(ctx context.Context, q WindowQuery) ([]string, error) {
	if q.byBatch() {
		var out []string
		err := s.matches(ctx, q, func(cid []byte, _ int64) error { out = append(out, CIDText(cid)); return nil })
		return out, err
	}
	where, params, _ := q.where()
	res, err := s.query(ctx, Request{SQL: fmt.Sprintf("SELECT _cid_bin FROM %s%s", q.table(), where), Params: params})
	if noSuchType(err, q.Schema) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, len(res.Rows))
	for i, r := range res.Rows {
		out[i] = CIDText(r[0].B)
	}
	return out, nil
}

func (s *Store) windowRows(ctx context.Context, q WindowQuery, cols string) ([][]Cell, error) {
	if q.twoPhase() {
		return s.windowTwoPhase(ctx, q, cols)
	}
	if _, _, indexed := q.where(); indexed && q.Order == "" {
		return s.windowByEpoch(ctx, q, cols)
	}
	sql, params := q.sql(cols)
	res, err := s.query(ctx, Request{SQL: sql, Params: params})
	if noSuchType(err, q.Schema) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// windowByEpoch serves an index-driven window (a source, provider, batch or
// object filter) in the legacy order without sorting every match: it reads
// the plan's epoch order (ORDER BY _epoch DESC, which the source plan emits
// from its SOURCE_EPOCH postings: design §9, the IQC incident query) only
// until the window is full and the epoch second of the last kept row is
// complete, then orders that prefix by (whole seconds DESC, text CID ASC).
// The statement is cancelled once it has given enough rows.
func (s *Store) windowByEpoch(ctx context.Context, q WindowQuery, cols string) ([][]Cell, error) {
	where, params, _ := q.where()
	sql := fmt.Sprintf("SELECT %s, _epoch FROM %s%s ORDER BY _epoch DESC", cols, q.table(), where)
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	need := q.Offset + limit
	var rows [][]Cell
	var epochs []int64
	var cids []string
	errEnough := errors.New("enough")
	run := func(r *Reader) error {
		rows, epochs, cids = rows[:0], epochs[:0], cids[:0]
		st, err := r.Submit(ctx, Request{SQL: sql, Params: params})
		if err != nil {
			return err
		}
		dec := RB1Decoder{OnRow: func(row []Cell) error {
			ep := row[len(row)-1].Int64()
			if len(rows) >= need && epochSeconds(ep) < epochSeconds(epochs[need-1]) {
				return errEnough
			}
			own := make([]Cell, len(row)-1)
			for i := range own {
				own[i] = row[i]
				if row[i].B != nil {
					own[i].B = append([]byte(nil), row[i].B...)
				}
			}
			rows = append(rows, own)
			epochs = append(epochs, ep)
			cids = append(cids, CIDText(own[0].B))
			return nil
		}}
		buf := make([]byte, 256<<10)
		for {
			n, err := st.Read(ctx, buf)
			if n > 0 {
				if ferr := dec.Feed(buf[:n]); ferr != nil {
					st.Close()
					if ferr == errEnough {
						return nil
					}
					return ferr
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				st.Close()
				return err
			}
		}
		if o := st.Finish(); o.Status != 0 {
			return &StatusError{Status: o.Status, Msg: o.Err}
		}
		return nil
	}
	err := run(s.ri)
	if IsStatus(err, StatusNeedsBulk) {
		err = run(s.rb)
	}
	if noSuchType(err, q.Schema) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := epochSeconds(epochs[idx[a]]), epochSeconds(epochs[idx[b]])
		if sa != sb {
			return sa > sb
		}
		return cids[idx[a]] < cids[idx[b]]
	})
	out := make([][]Cell, 0, limit)
	for i := q.Offset; i < len(idx) && len(out) < limit; i++ {
		out = append(out, rows[idx[i]])
	}
	return out, nil
}

// ByteProbe counts a window's records and record bytes without reading one
// payload byte (§9: the probe sums rows' lengths).
func (s *Store) ByteProbe(ctx context.Context, q WindowQuery) (count, bytes int64, err error) {
	if _, _, indexed := q.where(); q.twoPhase() || (indexed && q.Order == "") {
		recs, err := s.WindowMeta(ctx, q)
		if err != nil {
			return 0, 0, err
		}
		for _, r := range recs {
			bytes += r.Length
		}
		return int64(len(recs)), bytes, nil
	}
	inner, params := q.sql("_len")
	res, err := s.query(ctx, Request{SQL: "SELECT COUNT(*), COALESCE(SUM(_len - 4), 0) FROM (" + inner + ")", Params: params})
	if noSuchType(err, q.Schema) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return res.Rows[0][0].Int64(), res.Rows[0][1].Int64(), nil
}

// SyncQuery is one datasync v1 page over the type's arrivals (§8, A16).
type SyncQuery struct {
	Schema    string
	AfterGseq int64
	MaxGseq   int64 // 0: the type head's gseq_hi now (the first page's MaxRowID)
	Limit     int
	Provider  string
	Source    string
	Batch     string
	PeerID    string
	MetaOnly  bool // no payload bytes (refs)
}

// SyncPage returns up to Limit records with AfterGseq < gseq <= MaxGseq in
// gseq order, and the MaxGseq it used. No entry at or below MaxGseq can
// appear later: entries publish only once durable, in gseq order.
func (s *Store) SyncPage(ctx context.Context, q SyncQuery) ([]Rec, int64, error) {
	typ := typeName(q.Schema)
	max := q.MaxGseq
	if max <= 0 {
		types, err := s.heads.Types()
		if err != nil {
			return nil, 0, err
		}
		for _, ty := range types {
			if ty.Type == typ {
				max = ty.GseqHi
			}
		}
		if max <= 0 {
			return nil, 0, nil
		}
	}
	params := []Cell{Int(q.AfterGseq), Int(max)}
	conds := []string{"_gseq > ?1", "_gseq <= ?2"}
	add := func(col, v string) {
		if v != "" {
			params = append(params, Text(v))
			conds = append(conds, fmt.Sprintf("%s = ?%d", col, len(params)))
		}
	}
	add("_provider", q.Provider)
	add("_source_name", q.Source)
	add("_batch", q.Batch)
	cols := recColumns
	if q.MetaOnly {
		cols = strings.Replace(recColumns, "_data", "NULL", 1)
	}
	if q.PeerID != "" {
		params = append(params, Blob([]byte(q.PeerID)))
		conds = append(conds, fmt.Sprintf("_peer_id = ?%d", len(params)))
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	res, err := s.query(ctx, Request{SQL: fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY _gseq LIMIT %d`,
		cols, quoteIdent(typ), strings.Join(conds, " AND "), limit), Params: params})
	if noSuchType(err, q.Schema) {
		return nil, max, nil
	}
	if err != nil {
		return nil, max, err
	}
	out := make([]Rec, len(res.Rows))
	for i, row := range res.Rows {
		out[i] = recFromRow(row)
	}
	return out, max, nil
}

// StreamRaw runs a statement on the bulk lanes in raw-stream mode: fn gets
// every BLOB cell as it arrives ([u32le size][bytes] framing removed), so a
// large export is bounded by the slot's ring, never materialized.
func (s *Store) StreamRaw(ctx context.Context, sql string, params []Cell, fn func(frame []byte) error) (Outcome, error) {
	var carry []byte
	return s.rb.Stream(ctx, Request{SQL: sql, Params: params, Flags: ReqRawStream}, func(chunk []byte) error {
		carry = append(carry, chunk...)
		frames, rest := SplitRaw(carry)
		for _, f := range frames {
			if err := fn(f); err != nil {
				return err
			}
		}
		carry = append(carry[:0], rest...)
		return nil
	})
}

// SchemaRange is one type's catalog summary (storage.SchemaDateRange).
type SchemaRange struct {
	Schema      string
	RecordCount int64
	OldestEpoch *time.Time
	NewestEpoch *time.Time
	TotalBytes  int64
}

// SchemaDateRanges returns every type's record count (the type head's
// first_live_count), its oldest and newest record epoch (two bounded index
// seeks; none for a type without an epoch rule, as the legacy index held
// none), and its bytes (Σ live_bytes of its partitions).
func (s *Store) SchemaDateRanges(ctx context.Context) ([]SchemaRange, error) {
	types, err := s.Types(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := s.Partitions(ctx)
	if err != nil {
		return nil, err
	}
	bytes := map[string]int64{}
	for _, p := range parts {
		bytes[p.Type] += p.LiveBytes
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Schema < types[j].Schema })
	var out []SchemaRange
	for _, ty := range types {
		if ty.FirstLive == 0 {
			continue
		}
		r := SchemaRange{Schema: ty.Schema, RecordCount: ty.FirstLive, TotalBytes: bytes[ty.Type]}
		if strings.Contains(typeRules[ty.Type], "epoch ") {
			for _, dir := range []string{"ASC", "DESC"} {
				res, err := s.query(ctx, Request{SQL: fmt.Sprintf(`SELECT _epoch FROM %s ORDER BY _epoch %s LIMIT 1`, quoteIdent(ty.Type), dir)})
				if err != nil {
					return nil, err
				}
				if len(res.Rows) == 1 {
					t := time.Unix(epochSeconds(res.Rows[0][0].Int64()), 0).UTC()
					if dir == "ASC" {
						r.OldestEpoch = &t
					} else {
						r.NewestEpoch = &t
					}
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// epochSeconds floors epoch milliseconds to seconds (the legacy epoch_unix).
func epochSeconds(ms int64) int64 {
	if ms >= 0 {
		return ms / 1000
	}
	return -((999 - ms) / 1000)
}
