package storage

// migration_source_format4.go: what `spacedatanetwork store-migrate --to 4`
// (store format 4, stack design docs/architecture/flatsql-sqlite-partitions.md
// §11) reads from a format-1 store beyond what the format-2 migration reads
// (format2_source.go):
//   - each datasync index row WITH the structured columns format 1 extracted
//     from its record and every producer table's copy of that record, in one
//     query per page (each table is probed once per row: the held check and
//     the record read are the same seek). The copy walks these rows (their
//     rowid is the record's format-4 seq); the check compares the columns
//     with what the format-4 engine extracts from the same bytes;
//   - a schema's tag rows in one ordered pass (SchemaTags);
//   - the IQC ingest identities format 1 holds, which the engine keeps so a
//     re-fetch of the same capture stays a duplicate after the migration.
//
// Everything is read through the format-1 engine (MigrationSource); nothing is
// written. Scalar columns come back packed in one JSON cell per row
// (packedRow): the engine's driver costs about 3 µs a cell, which made a
// nine-column tag row 31 µs and a packed one 6.6 µs on the host-02-sized
// fixture.

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
)

// IndexEntry is one sdn_record_index row: the datasync cursor position and
// the structured columns (NULL = the record does not carry the field).
type IndexEntry struct {
	RowID           int64
	CID             string
	NoradCatID      sql.NullInt64
	EntityID        sql.NullString
	ObjectType      sql.NullString
	OpsStatusCode   sql.NullString
	EpochUnix       sql.NullInt64
	EpochDay        sql.NullString
	SourceTimestamp int64
}

// IndexCopies is an index row with the producer tables holding its record.
// No table holds an orphan's record.
type IndexCopies struct {
	IndexEntry
	Held   []bool         // per table of the request: the table holds the record
	Copies []LegacyRecord // per table, where Held (with records only); SupersedeKey is not read
}

// Orphan reports whether no producer table holds the row's record.
func (c IndexCopies) Orphan() bool {
	for _, h := range c.Held {
		if h {
			return false
		}
	}
	return true
}

// copyJoinGroup is how many producer tables one page query joins: its packed
// row holds 9 + 6 values per table, and a SQLite function takes at most 127
// arguments. A schema with more tables is read in groups over the same rowid
// range.
const copyJoinGroup = 16

// IndexPageCopies returns up to limit index rows of schema with rowid >
// after, in rowid order, held and orphan alike, each with which of tables
// hold its record and, withRecords, their copies. One query per group of
// tables: the index is walked in rowid order and each table probed once per
// row by its cid key.
func (m *MigrationSource) IndexPageCopies(schema string, tables []LegacyTable, after int64, limit int, withRecords bool) ([]IndexCopies, error) {
	scan, err := m.indexScan()
	if err != nil {
		return nil, err
	}
	var out []IndexCopies
	hi := int64(-1) // the page's last rowid, once the first group has read it
	for g := 0; g == 0 || g < len(tables); g += copyJoinGroup {
		group := tables[min(g, len(tables)):min(g+copyJoinGroup, len(tables))]
		var cols, data, joins strings.Builder
		for i, t := range group {
			fmt.Fprintf(&cols, ", t%d.cid IS NOT NULL", i)
			if withRecords {
				fmt.Fprintf(&cols, ", %s, t%[2]d.timestamp, t%[2]d.record_length, %s, t%[2]d.created_at",
					packText(fmt.Sprintf("t%d.peer_id", i)), i, packText(fmt.Sprintf("t%d.signature_hex", i)))
				fmt.Fprintf(&data, ", t%d.data", i)
			}
			fmt.Fprintf(&joins, " LEFT JOIN %s t%d ON t%d.cid = idx.cid", t.Name, i, i)
		}
		q := `SELECT json_array(idx.rowid, ` + packText("idx.cid") + `, idx.norad_cat_id, ` + packText("idx.entity_id") + `, ` +
			packText("idx.object_type") + `, ` + packText("idx.ops_status_code") + `, idx.epoch_unix, ` + packText("idx.epoch_day") +
			`, idx.source_timestamp` + cols.String() + `)` + data.String() + ` FROM sdn_record_index idx ` + scan + joins.String() + `
			WHERE idx.schema_name = ? AND idx.rowid > ?`
		args := []any{schema, after}
		if hi < 0 {
			q += ` ORDER BY idx.rowid LIMIT ?`
			args = append(args, limit)
		} else {
			q += ` AND idx.rowid <= ? ORDER BY idx.rowid`
			args = append(args, hi)
		}
		rows, err := m.s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		k := 0
		per := 1
		if withRecords {
			per = 6
		}
		for rows.Next() {
			var packed string
			blobs := make([][]byte, len(group))
			dst := []any{&packed}
			if withRecords {
				for i := range blobs {
					dst = append(dst, &blobs[i])
				}
			}
			if err := rows.Scan(dst...); err != nil {
				rows.Close()
				return nil, err
			}
			r, err := decodePacked(packed, 9+per*len(group))
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("%s index page: %w", schema, err)
			}
			var e IndexEntry
			var rowid, ts sql.NullInt64
			var cidText sql.NullString
			if err := errors.Join(r.num(0, &rowid), r.text(1, &cidText), r.num(2, &e.NoradCatID), r.text(3, &e.EntityID),
				r.text(4, &e.ObjectType), r.text(5, &e.OpsStatusCode), r.num(6, &e.EpochUnix), r.text(7, &e.EpochDay),
				r.num(8, &ts)); err != nil {
				rows.Close()
				return nil, fmt.Errorf("%s index page: %w", schema, err)
			}
			e.RowID, e.CID, e.SourceTimestamp = rowid.Int64, cidText.String, ts.Int64
			if g == 0 {
				out = append(out, IndexCopies{IndexEntry: e, Held: make([]bool, len(tables))})
				if withRecords {
					out[len(out)-1].Copies = make([]LegacyRecord, len(tables))
				}
			} else if k >= len(out) || out[k].RowID != e.RowID {
				rows.Close()
				return nil, fmt.Errorf("%s: index row %d moved between two reads of one page", schema, e.RowID)
			}
			c := &out[k]
			k++
			for i := range group {
				base := 9 + per*i
				var held sql.NullInt64
				if err := r.num(base, &held); err != nil {
					rows.Close()
					return nil, fmt.Errorf("%s index page: %w", schema, err)
				}
				c.Held[g+i] = held.Int64 != 0
				if !withRecords || !c.Held[g+i] {
					continue
				}
				var peer, sig sql.NullString
				var stamp, n, at sql.NullInt64
				if err := errors.Join(r.text(base+1, &peer), r.num(base+2, &stamp), r.num(base+3, &n), r.text(base+4, &sig),
					r.num(base+5, &at)); err != nil {
					rows.Close()
					return nil, fmt.Errorf("%s index page: %w", schema, err)
				}
				rec := LegacyRecord{RowID: e.RowID, CID: e.CID, PeerID: peer.String, Timestamp: stamp.Int64, Stored: blobs[i],
					RecordLength: n.Int64, SignatureHex: sig.String, CreatedAt: at.Int64}
				if rec.Plain, err = m.s.openStoredRecordBytes(schema, rec.Stored); err != nil {
					rows.Close()
					return nil, fmt.Errorf("record %s: %w", rec.CID, err)
				}
				rec.Sealed = encfield.IsSealed(rec.Stored)
				c.Copies[g+i] = rec
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if g == 0 {
			if len(out) == 0 {
				return nil, nil
			}
			hi = out[len(out)-1].RowID
		} else if k != len(out) {
			return nil, fmt.Errorf("%s: %d index rows in one read of a page, %d in another", schema, len(out), k)
		}
	}
	return out, nil
}

// SchemaTags reads in runs: a run of at least schemaTagRunMin sorted rowids
// whose span is at most twice its length is one rowid range (other schemas'
// rows inside it are skipped); the rest are named, schemaTagBatch at a time.
const (
	schemaTagRunMin = 64
	schemaTagRunMax = 8192
	schemaTagBatch  = 500
)

var schemaTagColumns = `json_array(` + packText("cid") + `, ` + packText("provider_id") + `, ` + packText("source_name") + `, ` +
	packText("source_url") + `, ` + packText("batch_id") + `, ` + packText("content_key_id") + `, ` + packText("producer_peer_id") +
	`, ` + packText("producer_public_key") + `, created_at)`

// SchemaTags calls fn for every sdn_record_source_tags row of schema, in no
// particular order, in one pass over the schema's rows: their rowids come
// from an index on schema_name (covering, a range scan), sorted, then the rows
// are read in rowid ranges and sorted batches, so table pages are visited in
// order. A probe per CID visits the CID-keyed index and the table at random
// instead, which on a cold store is most of a page-at-a-time copy's time.
func (m *MigrationSource) SchemaTags(schema string, fn func(cid string, t LegacyTag) error) error {
	ids, err := m.int64s(`SELECT rowid FROM sdn_record_source_tags WHERE schema_name = ?`, schema)
	if err != nil {
		return err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	read := func(where string, args ...any) error {
		rows, err := m.s.db.Query(`SELECT `+schemaTagColumns+` FROM sdn_record_source_tags WHERE `+where, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var packed string
			if err := rows.Scan(&packed); err != nil {
				return err
			}
			r, err := decodePacked(packed, 9)
			if err != nil {
				return fmt.Errorf("%s tags: %w", schema, err)
			}
			var v [8]sql.NullString
			var at sql.NullInt64
			for i := range v {
				if err := r.text(i, &v[i]); err != nil {
					return fmt.Errorf("%s tags: %w", schema, err)
				}
			}
			if err := r.num(8, &at); err != nil {
				return fmt.Errorf("%s tags: %w", schema, err)
			}
			t := LegacyTag{CreatedAt: at.Int64}
			t.ProviderID, t.SourceName, t.SourceURL, t.BatchID = v[1].String, v[2].String, v[3].String, v[4].String
			t.ContentKeyID, t.ProducerPeerID, t.ProducerPublicKey = v[5].String, v[6].String, v[7].String
			if err := fn(v[0].String, t); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	var named []any
	flush := func() error {
		if len(named) == 0 {
			return nil
		}
		err := read(`rowid IN (`+migratePlaceholders(len(named))+`)`, named...)
		named = named[:0]
		return err
	}
	for i := 0; i < len(ids); {
		j := i
		for j+1 < len(ids) && j+1-i < schemaTagRunMax && ids[j+1]-ids[i] < 2*int64(j+2-i) {
			j++
		}
		if j-i+1 >= schemaTagRunMin {
			if err := read(`rowid >= ? AND rowid <= ? AND schema_name = ?`, ids[i], ids[j], schema); err != nil {
				return err
			}
		} else {
			for _, id := range ids[i : j+1] {
				named = append(named, id)
				if len(named) == schemaTagBatch {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
		i = j + 1
	}
	return flush()
}

func (m *MigrationSource) int64s(q string, args ...any) ([]int64, error) {
	rows, err := m.s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// IngestIdentities returns the ingest identity format 1 holds for each CID of
// schema (sdn_record_ingest_identity, record_ingest_identity.go), as the
// SHA-256 digest the identity names. The table has no CID index, so the
// schema's rows are read once. An identity of another version is an error: its
// digest would be compared against a different mask.
func (m *MigrationSource) IngestIdentities(schema string) (map[string][32]byte, error) {
	exists, err := m.s.tableExists("sdn_record_ingest_identity")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := m.s.db.Query(`SELECT cid, identity FROM sdn_record_ingest_identity WHERE schema_name = ?`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][32]byte{}
	for rows.Next() {
		var cid, identity string
		if err := rows.Scan(&cid, &identity); err != nil {
			return nil, err
		}
		digest, err := ingestIdentityDigest(identity)
		if err != nil {
			return nil, fmt.Errorf("%s ingest identity of %s: %w", schema, cid, err)
		}
		if held, ok := out[cid]; ok && held != digest {
			return nil, fmt.Errorf("%s: %s holds two different ingest identities", schema, cid)
		}
		out[cid] = digest
	}
	return out, rows.Err()
}

// ingestIdentityDigest parses recordIngestIdentity's text form.
func ingestIdentityDigest(identity string) ([32]byte, error) {
	var d [32]byte
	h, ok := strings.CutPrefix(identity, ingestIdentityPrefix)
	if !ok {
		return d, fmt.Errorf("identity %q is not %s", identity, strings.TrimSuffix(ingestIdentityPrefix, ":"))
	}
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) != len(d) {
		return d, fmt.Errorf("identity %q is not a SHA-256 digest", identity)
	}
	copy(d[:], raw)
	return d, nil
}

// packedRow is one row's scalar columns, packed by SQLite into one JSON array
// cell: integers as numbers, text as the hex of its bytes (exact whatever its
// encoding; JSON text would pass through encoding/json's UTF-8 repair), NULL
// as null.
type packedRow []any

// packText packs a TEXT column for a packedRow.
func packText(expr string) string { return "iif(" + expr + " IS NULL, NULL, hex(" + expr + "))" }

func decodePacked(cell string, n int) (packedRow, error) {
	dec := json.NewDecoder(strings.NewReader(cell))
	dec.UseNumber()
	var r packedRow
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("packed row: %w", err)
	}
	if len(r) != n {
		return nil, fmt.Errorf("packed row of %d values, want %d", len(r), n)
	}
	return r, nil
}

func (r packedRow) num(i int, dst *sql.NullInt64) error {
	switch v := r[i].(type) {
	case nil:
		*dst = sql.NullInt64{}
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return fmt.Errorf("packed value %d: %w", i, err)
		}
		*dst = sql.NullInt64{Int64: n, Valid: true}
	default:
		return fmt.Errorf("packed value %d is %T, not an integer", i, r[i])
	}
	return nil
}

func (r packedRow) text(i int, dst *sql.NullString) error {
	switch v := r[i].(type) {
	case nil:
		*dst = sql.NullString{}
	case string:
		b, err := hex.DecodeString(v)
		if err != nil {
			return fmt.Errorf("packed value %d: %w", i, err)
		}
		*dst = sql.NullString{String: string(b), Valid: true}
	default:
		return fmt.Errorf("packed value %d is %T, not text", i, r[i])
	}
	return nil
}
