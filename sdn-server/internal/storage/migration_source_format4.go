package storage

// migration_source_format4.go: what `spacedatanetwork store-migrate --to 4`
// (store format 4, stack design docs/architecture/flatsql-sqlite-partitions.md
// §11) reads from a format-1 store beyond what the format-2 migration reads
// (format2_source.go):
//   - each held datasync index row WITH the structured columns format 1
//     extracted from its record. The copy walks these rows (their rowid is the
//     record's format-4 seq); the check compares the columns with what the
//     format-4 engine extracts from the same bytes;
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
)

// IndexEntry is one held sdn_record_index row: the datasync cursor position
// and the structured columns (NULL = the record does not carry the field).
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

// HeldIndexEntries is HeldIndexPage with the structured columns: up to limit
// index rows of schema with rowid > after, in rowid order, whose record one of
// tables holds. Orphans (no table holds the record) are left out.
func (m *MigrationSource) HeldIndexEntries(schema string, tables []LegacyTable, after int64, limit int) ([]IndexEntry, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	scan, err := m.indexScan()
	if err != nil {
		return nil, err
	}
	rows, err := m.s.db.Query(`SELECT idx.rowid, idx.cid, idx.norad_cat_id, idx.entity_id, idx.object_type, idx.ops_status_code,
		idx.epoch_unix, idx.epoch_day, idx.source_timestamp FROM sdn_record_index idx `+scan+`
		WHERE idx.schema_name = ? AND idx.rowid > ? AND `+migrateHeldSQL(tables, "idx.cid")+`
		ORDER BY idx.rowid LIMIT ?`, schema, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexEntry
	for rows.Next() {
		var e IndexEntry
		if err := rows.Scan(&e.RowID, &e.CID, &e.NoradCatID, &e.EntityID, &e.ObjectType, &e.OpsStatusCode,
			&e.EpochUnix, &e.EpochDay, &e.SourceTimestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
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
