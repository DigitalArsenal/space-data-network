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
//   - the IQC ingest identities format 1 holds, which the engine keeps so a
//     re-fetch of the same capture stays a duplicate after the migration.
//
// Everything is read through the format-1 engine (MigrationSource); nothing is
// written.

import (
	"database/sql"
	"encoding/hex"
	"fmt"
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

// copyJoinGroup is how many producer tables one page query joins (SQLite
// joins at most 64 tables); a schema with more is read in groups over the
// same rowid range.
const copyJoinGroup = 32

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
		var cols, joins strings.Builder
		for i, t := range group {
			fmt.Fprintf(&cols, ", t%d.cid IS NOT NULL", i)
			if withRecords {
				fmt.Fprintf(&cols, ", t%[1]d.peer_id, t%[1]d.timestamp, t%[1]d.data, t%[1]d.record_length, t%[1]d.signature_hex, t%[1]d.created_at", i)
			}
			fmt.Fprintf(&joins, " LEFT JOIN %s t%d ON t%d.cid = idx.cid", t.Name, i, i)
		}
		q := `SELECT idx.rowid, idx.cid, idx.norad_cat_id, idx.entity_id, idx.object_type, idx.ops_status_code, idx.epoch_unix,
			idx.epoch_day, idx.source_timestamp` + cols.String() + ` FROM sdn_record_index idx ` + scan + joins.String() + `
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
		for rows.Next() {
			var e IndexEntry
			dst := []any{&e.RowID, &e.CID, &e.NoradCatID, &e.EntityID, &e.ObjectType, &e.OpsStatusCode, &e.EpochUnix,
				&e.EpochDay, &e.SourceTimestamp}
			held := make([]bool, len(group))
			type cells struct {
				peer      sql.NullString
				ts, n, at sql.NullInt64
				data      []byte
				sig       sql.NullString
			}
			cs := make([]cells, len(group))
			for i := range group {
				dst = append(dst, &held[i])
				if withRecords {
					dst = append(dst, &cs[i].peer, &cs[i].ts, &cs[i].data, &cs[i].n, &cs[i].sig, &cs[i].at)
				}
			}
			if err := rows.Scan(dst...); err != nil {
				rows.Close()
				return nil, err
			}
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
				c.Held[g+i] = held[i]
				if !withRecords || !held[i] {
					continue
				}
				r := LegacyRecord{RowID: e.RowID, CID: e.CID, PeerID: cs[i].peer.String, Timestamp: cs[i].ts.Int64, Stored: cs[i].data,
					RecordLength: cs[i].n.Int64, SignatureHex: cs[i].sig.String, CreatedAt: cs[i].at.Int64}
				if r.Plain, err = m.s.openStoredRecordBytes(schema, r.Stored); err != nil {
					rows.Close()
					return nil, fmt.Errorf("record %s: %w", r.CID, err)
				}
				r.Sealed = encfield.IsSealed(r.Stored)
				c.Copies[g+i] = r
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
