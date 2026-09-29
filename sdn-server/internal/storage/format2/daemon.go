package format2

// The surface the storage package's daemon wiring reads through (T6: every
// record read of the node on lanes). The storage package keeps the legacy
// API (storage.FlatSQLStore) and translates each record read into a
// statement on the reader instances: record vtabs, meta tables and heads.
// Everything here is a read on a lane or a head; nothing waits on a writer.

import (
	"context"
	"fmt"
	"strings"
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
	res, err := s.query(ctx, Request{SQL: `SELECT pid, producer, type, lane_id, provider, source, batch, peer, pubkey,
		count, bytes, max_pseq, first_seen, updated FROM flatsql_lanes`})
	if err != nil {
		return nil, err
	}
	out := make([]LaneCounter, 0, len(res.Rows))
	for _, r := range res.Rows {
		lc := LaneCounter{PID: r[0].Int64(), Producer: r[1].String(), Type: r[2].String(), LaneID: r[3].Int64(),
			Provider: r[4].String(), Source: r[5].String(), Batch: r[6].String(), Peer: r[7].String(), PubKey: r[8].String(),
			Count: r[9].Int64(), Bytes: r[10].Int64(), MaxPseq: r[11].Int64(), FirstSeenMs: r[12].Int64(), UpdatedMs: r[13].Int64(),
			Tuple: r[4].Type != CellNull}
		out = append(out, lc)
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
	res, err := s.query(ctx, Request{SQL: `SELECT pid, producer, type, licence_key, pseq, arrival, data FROM flatsql_licences`})
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

// PartitionsOf returns the counters of the partitions of one type (from the
// heads, no lane).
func (s *Store) PartitionsOf(schema string) ([]PartitionCounter, error) {
	parts, err := s.heads.Partitions()
	if err != nil {
		return nil, err
	}
	typ := typeName(schema)
	out := parts[:0]
	for _, p := range parts {
		if p.Type == typ {
			out = append(out, p)
		}
	}
	return out, nil
}

// TypeCounterOf returns one type's head counters (zero when the type holds
// nothing yet).
func (s *Store) TypeCounterOf(schema string) (TypeCounter, error) {
	types, err := s.heads.Types()
	if err != nil {
		return TypeCounter{}, err
	}
	typ := typeName(schema)
	for _, t := range types {
		if t.Type == typ {
			return t, nil
		}
	}
	return TypeCounter{Type: typ, Schema: typ + ".fbs"}, nil
}

// CopiesOf returns every live copy of a CID in the type's partitions: one
// statement per partition of the type (a CID seek each). Used by the write
// paths that act on every copy (delete, retag) and by read-your-writes before
// a copy is labeled.
func (s *Store) CopiesOf(ctx context.Context, schema, cidText string) ([]Rec, error) {
	c, err := CIDFromText(cidText)
	if err != nil {
		return nil, err
	}
	parts, err := s.PartitionsOf(schema)
	if err != nil {
		return nil, err
	}
	var out []Rec
	for _, p := range parts {
		res, err := s.query(ctx, Request{SQL: fmt.Sprintf(`SELECT %s FROM %s WHERE _cid_bin = ?1`, recColumns, quoteIdent(p.SQLName)),
			Params: []Cell{Blob(c)}})
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			out = append(out, recFromRow(row))
		}
	}
	return out, nil
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
		res, err := s.query(ctx, Request{SQL: fmt.Sprintf(`SELECT _cid_bin FROM %s WHERE _cid_bin IN (%s)`, typ, strings.Join(marks, ",")),
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
	name := ""
	parts, err := s.heads.Partitions()
	if err != nil {
		return nil, err
	}
	for _, pc := range parts {
		if uint32(pc.PID) == p.PID {
			name = pc.SQLName
		}
	}
	out := make(map[string]bool, len(cidTexts))
	if name == "" {
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
		res, err := s.query(ctx, Request{SQL: fmt.Sprintf(`SELECT _cid_bin FROM %s WHERE _cid_bin IN (%s)`, quoteIdent(name), strings.Join(marks, ",")),
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
