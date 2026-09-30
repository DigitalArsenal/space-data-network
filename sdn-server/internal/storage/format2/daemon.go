package format2

// The surface the storage package's daemon wiring reads through (T6: every
// record read of the node on lanes). The storage package keeps the legacy
// API (storage.FlatSQLStore) and translates each record read into a
// statement on the reader instances: record vtabs, meta tables and heads.
// Everything here is a read on a lane or a head; nothing waits on a writer.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// RecColumns is the column list RecFromRow decodes.
const RecColumns = recColumns

// RecColumnsMeta is RecColumns without the payload (_data projects NULL).
var RecColumnsMeta = strings.Replace(recColumns, "_data", "NULL", 1)

// RecFromRow decodes one row selected with RecColumns (or RecColumnsMeta).
// The cells must own their bytes (RB1Decoder.Rows do).
func RecFromRow(row []Cell) Rec { return recFromRow(row) }

// QuoteIdent quotes an SQL identifier.
func QuoteIdent(s string) string { return quoteIdent(s) }

// TypeName is the engine type of a schema name ("OMM.fbs" -> "OMM").
func TypeName(schema string) string { return typeName(schema) }

// EpochSeconds floors epoch milliseconds to whole seconds.
func EpochSeconds(ms int64) int64 { return epochSeconds(ms) }

// TypeHasEpoch reports whether a type's extraction rules derive a record
// epoch (the legacy index's epoch_unix). A type without one keeps its
// arrival as _epoch, where the legacy index held NULL.
func TypeHasEpoch(schema string) bool { return strings.Contains(typeRules[typeName(schema)], "epoch ") }

// Query runs a trusted statement: interactive lanes, resubmitted to the bulk
// lanes when the engine says the plan is unbounded (§9 admission). A
// statement over a type no partition has registered answers ErrNoSuchType.
func (s *Store) Query(ctx context.Context, req Request) (*Result, error) {
	return s.query(ctx, req)
}

// NoSuchType reports the error of a statement over a type that holds no
// record yet (no partition registered it).
func NoSuchType(err error, schema string) bool { return noSuchType(err, schema) }

// LaneCounter is one lane (a tag tuple of one partition) of flatsql_lanes:
// the per-(provider, source, batch, producer peer, producer key) counters the
// legacy sdn_record_source_summary kept, maintained by the partition's writer
// on append (A2).
type LaneCounter struct {
	PID                                   int64
	Producer, Type                        string
	LaneID                                int64
	Provider, Source, Batch, Peer, PubKey string
	Count, Bytes, MaxPseq                 int64
	FirstSeenMs, UpdatedMs                int64 // 0: unknown
	Tuple                                 bool  // false: the lane's tuple is not readable
}

// Lanes returns every lane counter of every partition (a meta statement on a
// lane; counters come from the partitions' lane stores, never a record scan).
func (s *Store) Lanes(ctx context.Context) ([]LaneCounter, error) {
	// A meta statement over the partitions' lane stores: bounded, so it runs
	// on the point lanes and never queues behind a window.
	return s.lanes(ctx, "", nil)
}

// LaneFilter selects lanes: every set field must match. Lanes whose count
// fell to zero (a reconciled batch) are left out.
type LaneFilter struct {
	Type      string   // engine type ("OMM")
	Producers []string // partition producer tokens (any of them)
	PID       int64
	Provider  string
	Source    string
	Batch     string
}

// LanesWhere returns the live lanes (count > 0) a filter selects: one
// statement whose conditions the engine can serve from the named
// partitions alone (flatsql_lanes on pid, producer and type), rather than
// every lane of the store (terabyte audit B7).
func (s *Store) LanesWhere(ctx context.Context, f LaneFilter) ([]LaneCounter, error) {
	conds := []string{"count > 0"}
	var params []Cell
	eq := func(col, v string) {
		if v != "" {
			params = append(params, Text(v))
			conds = append(conds, fmt.Sprintf("%s = ?%d", col, len(params)))
		}
	}
	eq("type", f.Type)
	if f.PID > 0 {
		params = append(params, Int(f.PID))
		conds = append(conds, fmt.Sprintf("pid = ?%d", len(params)))
	}
	if len(f.Producers) > 0 {
		marks := make([]string, len(f.Producers))
		for i, p := range f.Producers {
			params = append(params, Text(p))
			marks[i] = fmt.Sprintf("?%d", len(params))
		}
		conds = append(conds, "producer IN ("+strings.Join(marks, ",")+")")
	}
	eq("provider", f.Provider)
	eq("source", f.Source)
	eq("batch", f.Batch)
	return s.lanes(ctx, " WHERE "+strings.Join(conds, " AND "), params)
}

func (s *Store) lanes(ctx context.Context, where string, params []Cell) ([]LaneCounter, error) {
	res, err := s.point(ctx, Request{SQL: `SELECT pid, producer, type, lane_id, provider, source, batch, peer, pubkey,
		count, bytes, max_pseq, first_seen, updated FROM flatsql_lanes` + where, Params: params})
	if err != nil {
		return nil, err
	}
	out := make([]LaneCounter, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, LaneCounter{PID: r[0].Int64(), Producer: r[1].String(), Type: r[2].String(), LaneID: r[3].Int64(),
			Provider: r[4].String(), Source: r[5].String(), Batch: r[6].String(), Peer: r[7].String(), PubKey: r[8].String(),
			Count: r[9].Int64(), Bytes: r[10].Int64(), MaxPseq: r[11].Int64(), FirstSeenMs: r[12].Int64(), UpdatedMs: r[13].Int64(),
			Tuple: r[4].Type != CellNull})
	}
	return out, nil
}

// Licence is one live LICENCE entry of a partition (flatsql_licences).
type Licence struct {
	PID                 int64
	Producer, Type, Key string
	Pseq, ArrivalMs     int64
	Body                []byte
}

// Licences returns every live licence entry.
func (s *Store) Licences(ctx context.Context) ([]Licence, error) {
	res, err := s.point(ctx, Request{SQL: `SELECT pid, producer, type, licence_key, pseq, arrival, data FROM flatsql_licences`})
	if err != nil {
		return nil, err
	}
	out := make([]Licence, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, Licence{PID: r[0].Int64(), Producer: r[1].String(), Type: r[2].String(), Key: r[3].String(),
			Pseq: r[4].Int64(), ArrivalMs: r[5].Int64(), Body: r[6].B})
	}
	return out, nil
}

// LabelsCaughtUp reports whether every partition of a type was labeled by
// its type owner through everything it had acked when the call was made: a
// type-level read then sees what the lane counters count.
func (s *Store) LabelsCaughtUp(schema string) (bool, error) {
	behind, err := s.behindLabels(schema)
	return len(behind) == 0, err
}

// PartitionsOf returns the counters of the partitions of one type (the head
// reader's, no lane; only the heads that moved are read).
func (s *Store) PartitionsOf(schema string) ([]PartitionCounter, error) {
	return s.heads.PartitionsOfType(typeName(schema))
}

// PartitionByPID returns one partition's counters (ok false when it is not
// registered): no other head is read.
func (s *Store) PartitionByPID(pid int64) (PartitionCounter, bool, error) {
	if pid <= 0 || pid > int64(^uint32(0)) {
		return PartitionCounter{}, false, nil
	}
	return s.heads.Partition(uint32(pid))
}

// TypeCounterOf returns one type's head counters (zero when the type holds
// nothing yet): one type head read.
func (s *Store) TypeCounterOf(schema string) (TypeCounter, error) {
	typ := typeName(schema)
	tc, ok, err := s.heads.TypeOf(typ)
	if err != nil {
		return TypeCounter{}, err
	}
	if !ok {
		return TypeCounter{Type: typ, Schema: typ + ".fbs"}, nil
	}
	return tc, nil
}

// CopiesOf returns every live copy of a CID in the type's partitions. Used by
// the write paths that act on every copy (delete) and by read-your-writes
// before a copy is labeled.
//
// The catalog seek at type level shows the FIRST copy only; a labeled REPEAT
// copy (the record held by a second producer) is found only in its
// partition. So every partition is asked, one CID seek each, unless the type
// provably holds no REPEAT copy and nothing acked but unlabeled
// (copyTargets): then the catalog names the only partition that can hold a
// copy, and that one partition is asked (terabyte audit M7).
func (s *Store) CopiesOf(ctx context.Context, schema, cidText string) ([]Rec, error) {
	c, err := CIDFromText(cidText)
	if err != nil {
		return nil, err
	}
	parts, typeLevel, err := s.copyTargets(schema)
	if err != nil {
		return nil, err
	}
	if typeLevel {
		r, err := s.typeCopy(ctx, schema, c)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, nil
		}
		// The catalog's copy is confirmed in its partition: a kill acked
		// since the proof is not reported as a live copy.
		var own []PartitionCounter
		for _, p := range parts {
			if p.Producer == r.Producer {
				own = append(own, p)
			}
		}
		if len(own) == 0 {
			if parts, err = s.PartitionsOf(schema); err != nil {
				return nil, err
			}
		} else {
			parts = own
		}
	}
	var out []Rec
	seen := map[string]bool{}
	for _, p := range parts {
		rows, err := s.partitionSeek(ctx, p.SQLName, c)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			r := recFromRow(row)
			if k := fmt.Sprintf("%s/%d", r.Producer, r.Pseq); !seen[k] {
				seen[k] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// copyTargets returns the partitions CopiesOf asks: every partition of the
// type, unless the type provably holds no REPEAT copy and nothing acked but
// unlabeled (typeLevel; parts are then the type's partitions as just read).
// The proof holds one type head (commit_seq c, first_live F) across:
//   - its labels, read after it;
//   - every partition head of the type, read after that;
//   - each partition labeled through exactly the pseq_hi its head gave
//     (nothing unlabeled: a partition's live count is its labeled state,
//     including every kill, which F already counts);
//   - Σ live over the partitions equal to F: every live copy is a FIRST
//     copy, the one the catalog names.
//
// Anything else asks every partition: a partition behind or past its labels,
// a quarantined one, a type commit during the reads, unreadable labels, or
// published counters that already show a REPEAT copy or a partition behind
// (checked first, without reading a head).
func (s *Store) copyTargets(schema string) ([]PartitionCounter, bool, error) {
	typ := typeName(schema)
	all := func() ([]PartitionCounter, bool, error) {
		parts, err := s.PartitionsOf(schema)
		return parts, false, err
	}
	before, ok, err := s.heads.TypeOf(typ)
	if err != nil || !ok {
		return nil, false, err
	}
	if totals, err := s.TypeTotalsOf(schema); err != nil || totals.Live > before.FirstLive {
		if err != nil {
			return nil, false, err
		}
		return all()
	}
	if behind, err := s.behindLabels(schema); err != nil || len(behind) > 0 {
		if err != nil {
			return nil, false, err
		}
		return all()
	}
	fid, ok := s.heads.fidOfPublished(typ)
	if !ok {
		return all()
	}
	var labels map[uint32]uint64
	known := false
	if err := s.heads.withLabels(fid, func(l map[uint32]uint64, k bool) {
		if known = k; k {
			labels = maps.Clone(l)
		}
	}); err != nil {
		return nil, false, err
	}
	if !known {
		return all()
	}
	fresh, err := s.heads.PartitionsOfTypeFresh(typ)
	if err != nil {
		return nil, false, err
	}
	after, _, err := s.heads.TypeOf(typ)
	if err != nil {
		return nil, false, err
	}
	if after.CommitSeq != before.CommitSeq {
		return all()
	}
	var live int64
	for _, p := range fresh {
		if p.Quarantined || labels[uint32(p.PID)] != uint64(max(p.PseqHi, 0)) {
			return all()
		}
		live += p.Live
	}
	if live != before.FirstLive {
		return all()
	}
	return fresh, true, nil
}

// Present returns which of the CIDs have a live copy at type level (the cid
// catalog), in chunks of 256 per statement.
func (s *Store) Present(ctx context.Context, schema string, cidTexts []string) (map[string]bool, error) {
	out := make(map[string]bool, len(cidTexts))
	typ := quoteIdent(typeName(schema))
	for start := 0; start < len(cidTexts); start += 256 {
		end := min(start+256, len(cidTexts))
		var params []Cell
		var marks []string
		for _, t := range cidTexts[start:end] {
			c, err := CIDFromText(t)
			if err != nil {
				continue
			}
			params = append(params, Blob(c))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		if len(params) == 0 {
			continue
		}
		res, err := s.point(ctx, Request{SQL: fmt.Sprintf(`SELECT _cid_bin FROM %s WHERE _cid_bin IN (%s)`, typ, strings.Join(marks, ",")),
			Params: params})
		if noSuchType(err, schema) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			out[CIDText(r[0].B)] = true
		}
	}
	return out, nil
}

// PresentInPartition returns which of the CIDs have a live copy in the
// partition the peer id routes to (registering it on first use).
func (s *Store) PresentInPartition(ctx context.Context, schema, peerID string, cidTexts []string) (map[string]bool, error) {
	spec, err := s.spec(schema)
	if err != nil {
		return nil, err
	}
	p, err := s.w.Partition([]byte(peerID), spec.FID)
	if err != nil {
		return nil, err
	}
	pc, ok, err := s.heads.Partition(p.PID)
	if err != nil {
		return nil, err
	}
	name := pc.SQLName
	out := make(map[string]bool, len(cidTexts))
	if !ok || name == "" {
		return out, nil // registered, nothing committed yet
	}
	for start := 0; start < len(cidTexts); start += 256 {
		end := min(start+256, len(cidTexts))
		var params []Cell
		var marks []string
		for _, t := range cidTexts[start:end] {
			c, err := CIDFromText(t)
			if err != nil {
				continue
			}
			params = append(params, Blob(c))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		if len(params) == 0 {
			continue
		}
		res, err := s.point(ctx, Request{SQL: fmt.Sprintf(`SELECT _cid_bin FROM %s WHERE _cid_bin IN (%s)`, quoteIdent(name), strings.Join(marks, ",")),
			Params: params})
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			out[CIDText(r[0].B)] = true
		}
	}
	return out, nil
}

// Sandbox is the sandbox reader instance (untrusted SQL, A28).
func (s *Store) Sandbox() *Reader { return s.rs }

// SandboxQuery runs untrusted SQL (ReqSandbox: the authorizer, one SELECT,
// the work budget): on an interactive lane when its plan is bounded, else on
// the sandbox lanes under the budget; never on the shared bulk lanes.
func (s *Store) SandboxQuery(ctx context.Context, req Request) (*Result, error) {
	req.Flags |= ReqSandbox
	res, err := s.ri.Query(ctx, req)
	if IsStatus(err, StatusNeedsBulk) {
		return s.rs.Query(ctx, req)
	}
	return res, err
}

// SandboxStream is SandboxQuery in raw-stream mode: fn gets each BLOB
// cell's frame ([u32le size][bytes] removed).
func (s *Store) SandboxStream(ctx context.Context, req Request, fn func(frame []byte) error) (Outcome, error) {
	req.Flags |= ReqSandbox | ReqRawStream
	run := func(r *Reader) (Outcome, error) {
		var carry []byte
		return r.Stream(ctx, req, func(chunk []byte) error {
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
	o, err := run(s.ri)
	if IsStatus(err, StatusNeedsBulk) {
		return run(s.rs)
	}
	return o, err
}

// StreamTrusted runs a trusted statement in raw-stream mode on the
// interactive lanes, moved to the bulk lanes when its plan is unbounded.
func (s *Store) StreamTrusted(ctx context.Context, sql string, params []Cell, fn func(frame []byte) error) (Outcome, error) {
	run := func(r *Reader) (Outcome, error) {
		var carry []byte
		return r.Stream(ctx, Request{SQL: sql, Params: params, Flags: ReqRawStream}, func(chunk []byte) error {
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
	o, err := run(s.ri)
	if IsStatus(err, StatusNeedsBulk) {
		return run(s.rb)
	}
	return o, err
}

// Scan runs a trusted statement and hands each row to fn as it arrives
// (the cells are valid during the call only): the interactive lanes, the
// bulk lanes when the plan is unbounded. A statement over a type that holds
// no record yet answers ErrNoSuchType-shaped errors (NoSuchType).
func (s *Store) Scan(ctx context.Context, req Request, fn func(row []Cell) error) error {
	return s.scan(ctx, req, fn)
}

// InstanceHealth is one instance's state, its poison flag and its host I/O
// lock (the handle table: a shared critical section of T6 #3).
type InstanceHealth struct {
	Name, State      string
	Poisoned         bool
	Restarts         int64 // replacements (a fenced or recycled reader)
	Fenced           int64 // of which a fenced (trapped) instance
	Pages            uint64
	LockAcquisitions int64
	LockHoldMax      time.Duration
}

// Health reports every instance and the store-wide path registry lock (A29).
func (s *Store) Health() (flatsqlrt.NativeStoreStats, []InstanceHealth) {
	var out []InstanceHealth
	add := func(name string, inst *flatsqlrt.PSInstance, restarts, fenced int64) {
		if inst == nil {
			return
		}
		st := inst.Stats()
		out = append(out, InstanceHealth{Name: name, State: st.State, Poisoned: st.Poisoned, Restarts: restarts, Fenced: fenced,
			Pages: inst.Memory().Pages(), LockAcquisitions: st.IO.LockAcquisitions, LockHoldMax: st.IO.LockHoldMax})
	}
	add("writer", s.w.Instance(), 0, 0)
	for _, r := range []struct {
		name string
		r    *Reader
	}{{"interactive", s.ri}, {"bulk", s.rb}, {"sandbox", s.rs}, {"point", s.rp}} {
		if r.r != nil {
			add(r.name, r.r.Instance(), r.r.Restarts(), r.r.Fenced())
		}
	}
	return s.native.Stats(), out
}

// QueryPoint runs an O(1) statement (a lookup by CID or gseq) on the point
// lanes.
func (s *Store) QueryPoint(ctx context.Context, req Request) (*Result, error) {
	return s.point(ctx, req)
}
