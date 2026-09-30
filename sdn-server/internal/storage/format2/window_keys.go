package format2

// Two-phase windows. The engine emits a window in the default order (epoch
// seconds DESC, text CID ASC: EPOCH_CID), in epoch order on the source plan
// and in arrival order; for any other order SQLite sorts every match, payload
// included, before the first row (a publication shard in text CID order over
// a type of millions of records: seconds, then the reader's memory budget).
// Such a window reads its matches' keys first (binary CID, epoch; no
// payload), keeps the window's rows in order here, and reads those rows by
// CID.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// errStop ends a scanned statement early: the caller has what it needs.
var errStop = errors.New("format2: stop")

// scan runs a trusted statement and hands each row to fn; the cells are
// valid during the call only. The interactive lanes run it, the bulk lanes
// when the engine says the plan is unbounded. fn returning errStop cancels
// the statement and scan returns nil.
func (s *Store) scan(ctx context.Context, req Request, fn func(row []Cell) error) error {
	run := func(r *Reader) (bool, error) {
		delivered := false
		st, err := r.Submit(ctx, req)
		if err != nil {
			return false, err
		}
		dec := RB1Decoder{OnRow: func(row []Cell) error {
			delivered = true
			return fn(row)
		}}
		buf := make([]byte, 256<<10)
		for {
			n, err := st.Read(ctx, buf)
			if n > 0 {
				if ferr := dec.Feed(buf[:n]); ferr != nil {
					st.Close()
					if ferr == errStop {
						return delivered, nil
					}
					return delivered, ferr
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				st.Close()
				return delivered, err
			}
		}
		if o := st.Finish(); o.Status != 0 {
			return delivered, &StatusError{Status: o.Status, Msg: o.Err}
		}
		return delivered, nil
	}
	delivered, err := run(s.ri)
	if IsStatus(err, StatusNeedsBulk) && !delivered {
		_, err = run(s.rb)
	}
	return err
}

// byBatch reports a window its batch drives (WindowQuery.ByBatch).
func (q WindowQuery) byBatch() bool {
	return q.ByBatch && q.Batch != "" && q.Source != ""
}

// twoPhase reports a window the engine cannot emit in its order without
// sorting every match: text CID order under conditions (the engine emits
// text CID order from the type's cid catalog, or a partition's CID
// postings, only for the whole table: flatsql PARTITION-STORE.md §39), and
// the default order on a tag plan (provider or batch postings are in
// arrival order).
func (q WindowQuery) twoPhase() bool {
	switch q.Order {
	case "cid":
		where, _, _ := q.where()
		return where != ""
	case "":
	default:
		return false
	}
	if q.byBatch() {
		return true
	}
	if q.Source != "" || q.NoradCatID != nil || q.EntityID != "" {
		return false
	}
	for _, c := range q.Conds {
		if c.Indexed {
			return false
		}
	}
	return q.Provider != "" || q.Batch != ""
}

// matches streams a window's matches (its conditions; no order, no limit)
// as (binary CID, epoch ms). A ByBatch window reads the batch's postings and
// checks the provider and source on each match: a record whose own tag (the
// projected one, its PUT's) meets every condition matches; any other is
// checked by CID against every live instance (A2: one instance meets all).
func (s *Store) matches(ctx context.Context, q WindowQuery, fn func(cid []byte, epochMs int64) error) error {
	if !q.byBatch() {
		where, params, _ := q.where()
		err := s.scan(ctx, Request{SQL: fmt.Sprintf("SELECT _cid_bin, _epoch FROM %s%s", q.table(), where), Params: params},
			func(row []Cell) error { return fn(row[0].B, row[1].Int64()) })
		if noSuchType(err, q.Schema) {
			return nil
		}
		return err
	}
	nq := q
	nq.Provider, nq.Source, nq.ByBatch = "", "", false
	where, params, _ := nq.where()
	var pending [][]byte
	err := s.scan(ctx, Request{SQL: fmt.Sprintf("SELECT _cid_bin, _epoch, _provider, _source_name, _batch FROM %s%s", q.table(), where),
		Params: params},
		func(row []Cell) error {
			if row[3].String() == q.Source && row[4].String() == q.Batch && (q.Provider == "" || row[2].String() == q.Provider) {
				return fn(row[0].B, row[1].Int64())
			}
			pending = append(pending, append([]byte(nil), row[0].B...))
			return nil
		})
	if noSuchType(err, q.Schema) {
		return nil
	}
	if err != nil {
		return err
	}
	// The rest matched the batch on another tag instance.
	fq := q
	fq.ByBatch = false
	fwhere, fparams, _ := fq.where()
	for start := 0; start < len(pending); start += 256 {
		end := min(start+256, len(pending))
		params := append([]Cell(nil), fparams...)
		marks := make([]string, 0, end-start)
		for _, c := range pending[start:end] {
			params = append(params, Blob(c))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		res, err := s.query(ctx, Request{SQL: fmt.Sprintf("SELECT _cid_bin, _epoch FROM %s%s AND _cid_bin IN (%s)", q.table(), fwhere,
			strings.Join(marks, ",")), Params: params})
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			if err := fn(r[0].B, r[1].Int64()); err != nil {
				return err
			}
		}
	}
	return nil
}

// cidKeyLen is the length of a cidOrderKey: 58 base32 digits of 5 bits.
const cidKeyLen = 37

// cidOrderKey maps a binary CID to a key whose byte order is the order of
// its text form. The text of a CIDv1 is "b" and the RFC 4648 base32 digits
// (lower case, no padding) of the binary, and the digits '2'..'7' (values
// 26..31) sort before 'a'..'z' (0..25): each 5-bit group g becomes
// (g + 6) mod 32, packed big-endian.
func cidOrderKey(bin []byte) (k [cidKeyLen]byte, ok bool) {
	if len(bin) != cidBytes {
		return k, false
	}
	var acc, out uint64
	nacc, nout, oi := 0, 0, 0
	emit := func(g uint64) {
		out = out<<5 | (g+6)&31
		nout += 5
		for nout >= 8 {
			k[oi] = byte(out >> (nout - 8))
			oi++
			nout -= 8
		}
	}
	for _, b := range bin {
		acc = acc<<8 | uint64(b)
		nacc += 8
		for nacc >= 5 {
			emit(acc >> (nacc - 5) & 31)
			nacc -= 5
		}
	}
	if nacc > 0 {
		emit(acc << (5 - nacc) & 31)
	}
	if nout > 0 {
		k[oi] = byte(out << (8 - nout))
	}
	return k, true
}

type windowKey struct {
	key [cidKeyLen]byte
	sec int64
	cid [cidBytes]byte
}

// windowTwoPhase serves a twoPhase window: phase 1 keeps the first
// (offset + limit) matches in the window's order from their keys, phase 2
// reads the window's rows by CID.
func (s *Store) windowTwoPhase(ctx context.Context, q WindowQuery, cols string) ([][]Cell, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	need := q.Offset + limit
	byCID := q.Order == "cid"
	less := func(a, b *windowKey) bool {
		if !byCID && a.sec != b.sec {
			return a.sec > b.sec
		}
		return bytes.Compare(a.key[:], b.key[:]) < 0
	}
	var keys []windowKey
	full := false // keys holds need keys and keys[need-1] bounds the window
	trim := func() {
		sort.Slice(keys, func(i, j int) bool { return less(&keys[i], &keys[j]) })
		// A record matched through two tag instances comes twice.
		w := 0
		for i := range keys {
			if w > 0 && keys[i].cid == keys[w-1].cid {
				continue
			}
			keys[w] = keys[i]
			w++
		}
		keys = keys[:w]
		if len(keys) >= need {
			keys = keys[:need]
			full = true
		}
	}
	batch := max(2*need, need+65536)
	err := s.matches(ctx, q, func(cid []byte, epochMs int64) error {
		k, ok := cidOrderKey(cid)
		if !ok {
			return fmt.Errorf("format2: a CID of %d bytes", len(cid))
		}
		wk := windowKey{key: k, sec: epochSeconds(epochMs)}
		copy(wk.cid[:], cid)
		if full && !less(&wk, &keys[need-1]) {
			return nil
		}
		keys = append(keys, wk)
		if len(keys) >= batch {
			trim()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	trim()
	if q.Offset >= len(keys) {
		return nil, nil
	}
	keys = keys[q.Offset:]
	rows := make([][]Cell, len(keys))
	at := make(map[[cidBytes]byte]int, len(keys))
	for i := range keys {
		at[keys[i].cid] = i
	}
	for start := 0; start < len(keys); start += 512 {
		end := min(start+512, len(keys))
		params := make([]Cell, 0, end-start)
		marks := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			params = append(params, Blob(append([]byte(nil), keys[i].cid[:]...)))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		res, err := s.query(ctx, Request{SQL: fmt.Sprintf("SELECT _cid_bin AS _k, %s FROM %s WHERE _cid_bin IN (%s)", cols, q.table(),
			strings.Join(marks, ",")), Params: params})
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			var c [cidBytes]byte
			copy(c[:], r[0].B)
			if i, ok := at[c]; ok && rows[i] == nil {
				rows[i] = r[1:]
			}
		}
	}
	// A record deleted between the phases leaves the window.
	out := rows[:0]
	for _, r := range rows {
		if r != nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// Payloads reads the payloads of records by CID in one table (a
// partition's sql_name; "" the type's fan-out), 512 per statement, keyed by
// the binary CID.
func (s *Store) Payloads(ctx context.Context, schema, table string, cids [][]byte) (map[string][]byte, error) {
	q := WindowQuery{Schema: schema, Table: table}
	out := make(map[string][]byte, len(cids))
	for start := 0; start < len(cids); start += 512 {
		end := min(start+512, len(cids))
		params := make([]Cell, 0, end-start)
		marks := make([]string, 0, end-start)
		for _, c := range cids[start:end] {
			params = append(params, Blob(c))
			marks = append(marks, fmt.Sprintf("?%d", len(params)))
		}
		res, err := s.query(ctx, Request{SQL: fmt.Sprintf("SELECT _cid_bin, _data FROM %s WHERE _cid_bin IN (%s)", q.table(),
			strings.Join(marks, ",")), Params: params})
		if noSuchType(err, schema) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			out[string(r[0].B)] = r[1].B
		}
	}
	return out, nil
}
