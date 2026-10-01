package storage

// format2_source.go: the legacy side of `spacedatanetwork store-migrate`
// (store format 2, stack design docs/architecture/flatsql-partition-store.md
// §16, T6). It opens a legacy record store the way the daemon does (the
// auxiliary journal replayed, the engine hot window NOT hydrated) and reads
// it: the (producer, standard) tables in rowid pages, the datasync index,
// source tags, licences, and the three oracles store-migrate verifies
// against (sdn_partition_record_bytes, sdn_record_source_summary, the v1 cid
// sequence per schema). It changes no record. The only write it makes is the
// control copy (§14 interim: VACUUM INTO, then the record tables dropped in
// the copy).

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// MigrationSource reads a legacy store for store-migrate.
type MigrationSource struct {
	s    *FlatSQLStore
	base string

	scanOnce sync.Once
	scan     string // HeldIndexPage's access path (indexScan)
	scanErr  error
}

// OpenMigrationSource opens the legacy store at basePath (it takes the
// store's single-writer lock, so the daemon must be stopped, or basePath is
// a copy).
func OpenMigrationSource(basePath string) (*MigrationSource, error) {
	v, err := sds.NewValidator(nil)
	if err != nil {
		return nil, err
	}
	s, err := NewFlatSQLStore(basePath, v, WithDeferredBootRebuilds())
	if err != nil {
		return nil, err
	}
	return &MigrationSource{s: s, base: basePath}, nil
}

// Close closes the legacy store.
func (m *MigrationSource) Close() error { return m.s.Close() }

// LegacyTable is one (producer, standard) table.
type LegacyTable struct {
	Name   string // sds_p_<token>__<STD>
	Token  string // the producer token (A3: the partition key)
	Schema string // "<STD>.fbs"
}

// ProducerTables lists every (producer, standard) table, sorted by name
// (the order that picks a record's FIRST copy, §16.1 step 5).
func (m *MigrationSource) ProducerTables() ([]LegacyTable, error) {
	rows, err := m.s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'sds\_p\_%' ESCAPE '\'
		AND COALESCE(sql, '') NOT LIKE 'CREATE VIRTUAL%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LegacyTable
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		body := strings.TrimPrefix(name, "sds_p_")
		i := strings.LastIndex(body, "__")
		if i <= 0 || i+2 >= len(body) || !isSafeIdentifier(name) {
			continue // not a routed table name
		}
		out = append(out, LegacyTable{Name: name, Token: body[:i], Schema: body[i+2:] + ".fbs"})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LegacyRecord is one row of a (producer, standard) table.
type LegacyRecord struct {
	RowID        int64
	CID          string
	PeerID       string
	Timestamp    int64
	Stored       []byte // the bytes the row holds (sealed for (encrypted) standards)
	Plain        []byte // the plaintext record (== Stored unless sealed)
	Sealed       bool
	RecordLength int64
	SignatureHex string
	SupersedeKey string
	CreatedAt    int64
}

// supersede_key is read as hex: a source-scoped key is "src:<source>\x00<identity>"
// (record_supersede.go), and a TEXT cell reaches Go through the engine as a
// C string, cut at the NUL.
const legacyRecordColumns = `rowid, cid, peer_id, timestamp, data, record_length,
	COALESCE(signature_hex, ''), COALESCE(hex(supersede_key), ''), COALESCE(created_at, 0)`

func (m *MigrationSource) scanRecords(schema string, rows *sql.Rows) ([]LegacyRecord, error) {
	defer rows.Close()
	var out []LegacyRecord
	for rows.Next() {
		var r LegacyRecord
		var keyHex string
		if err := rows.Scan(&r.RowID, &r.CID, &r.PeerID, &r.Timestamp, &r.Stored, &r.RecordLength,
			&r.SignatureHex, &keyHex, &r.CreatedAt); err != nil {
			return nil, err
		}
		key, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("record %s: supersede key: %w", r.CID, err)
		}
		r.SupersedeKey = string(key)
		plain, err := m.s.openStoredRecordBytes(schema, r.Stored)
		if err != nil {
			return nil, fmt.Errorf("record %s: %w", r.CID, err)
		}
		r.Plain = plain
		r.Sealed = encfield.IsSealed(r.Stored)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScanRecords returns up to limit rows of table with rowid > after, in
// rowid order, stopping early once maxBytes of record bytes are read.
func (m *MigrationSource) ScanRecords(t LegacyTable, after int64, limit int) ([]LegacyRecord, error) {
	rows, err := m.s.db.Query(fmt.Sprintf(`SELECT %s FROM %s WHERE rowid > ? ORDER BY rowid LIMIT ?`,
		legacyRecordColumns, t.Name), after, limit)
	if err != nil {
		return nil, err
	}
	return m.scanRecords(t.Schema, rows)
}

// RecordsByCID returns table's rows for the given CIDs (absent CIDs are
// missing from the map).
func (m *MigrationSource) RecordsByCID(t LegacyTable, cids []string) (map[string]LegacyRecord, error) {
	out := map[string]LegacyRecord{}
	for _, chunk := range migrateChunks(cids, 400) {
		args := make([]any, len(chunk))
		for i, c := range chunk {
			args[i] = c
		}
		rows, err := m.s.db.Query(fmt.Sprintf(`SELECT %s FROM %s WHERE cid IN (%s)`, legacyRecordColumns, t.Name,
			migratePlaceholders(len(chunk))), args...)
		if err != nil {
			return nil, err
		}
		recs, err := m.scanRecords(t.Schema, rows)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			out[r.CID] = r
		}
	}
	return out, nil
}

// HasCIDs reports which of cids the table holds.
func (m *MigrationSource) HasCIDs(t LegacyTable, cids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, chunk := range migrateChunks(cids, 400) {
		args := make([]any, len(chunk))
		for i, c := range chunk {
			args[i] = c
		}
		rows, err := m.s.db.Query(fmt.Sprintf(`SELECT cid FROM %s WHERE cid IN (%s)`, t.Name, migratePlaceholders(len(chunk))), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return nil, err
			}
			out[c] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// IndexRow is one sdn_record_index row: the datasync v1 cursor position.
type IndexRow struct {
	RowID int64
	CID   string
}

// HeldIndexPage returns, in rowid order, up to limit index rows of schema
// with rowid > after whose record one of tables (the schema's producer
// tables) holds. These are the rows the legacy datasync serves
// (recordHeldSQL) and the rows store-migrate copies. An orphan is an index
// row no producer table holds (its record was deleted and the index row
// stayed); it is never copied. With no tables every row is an orphan.
func (m *MigrationSource) HeldIndexPage(schema string, tables []LegacyTable, after int64, limit int) ([]IndexRow, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	scan, err := m.indexScan()
	if err != nil {
		return nil, err
	}
	rows, err := m.s.db.Query(`SELECT idx.rowid, idx.cid FROM sdn_record_index idx `+scan+`
		WHERE idx.schema_name = ? AND idx.rowid > ? AND `+migrateHeldSQL(tables, "idx.cid")+`
		ORDER BY idx.rowid LIMIT ?`, schema, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexRow
	for rows.Next() {
		var r IndexRow
		if err := rows.Scan(&r.RowID, &r.CID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// indexScan is how HeldIndexPage walks one schema's index rows in rowid
// order, a page at a time, without sorting. The full-text scan index
// (schema_name, rowid) serves it when the store has one; otherwise the table
// itself is walked in rowid order (NOT INDEXED: other schemas' rows are
// skipped). Left to the planner, the (schema_name, cid) primary key can win
// and every page then sorts every row of the schema, and probes every
// producer table for each.
func (m *MigrationSource) indexScan() (string, error) {
	m.scanOnce.Do(func() {
		var n int
		m.scanErr = m.s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sdn_record_fts_scan'
			AND tbl_name = 'sdn_record_index'`).Scan(&n)
		m.scan = "NOT INDEXED"
		if n > 0 {
			m.scan = "INDEXED BY idx_sdn_record_fts_scan"
		}
	})
	return m.scan, m.scanErr
}

// OrphanIndexRows counts schema's index rows that none of tables holds.
func (m *MigrationSource) OrphanIndexRows(schema string, tables []LegacyTable) (int64, error) {
	q := `SELECT COUNT(*) FROM sdn_record_index idx WHERE idx.schema_name = ?`
	if len(tables) > 0 {
		q += ` AND NOT ` + migrateHeldSQL(tables, "idx.cid")
	}
	var n int64
	err := m.s.db.QueryRow(q, schema).Scan(&n)
	return n, err
}

// migrateHeldGroup is how many EXISTS terms migrateHeldSQL ORs flat.
const migrateHeldGroup = 64

// migrateHeldSQL is true when cidExpr is held by one of tables: an EXISTS
// seek per table (recordHeldSQL's terms), OR'ed flat in parenthesised groups
// of migrateHeldGroup and the groups again, so the expression's depth grows
// with the logarithm of the table count. recordHeldSQL's single flat chain
// passes SQLite's expression-depth limit (1000) at about 1,000 producer
// tables of one standard. With no tables nothing is held.
func migrateHeldSQL(tables []LegacyTable, cidExpr string) string {
	if len(tables) == 0 {
		return "0"
	}
	terms := make([]string, len(tables))
	for i, t := range tables {
		terms[i] = fmt.Sprintf("EXISTS (SELECT 1 FROM %s h WHERE h.cid = %s)", t.Name, cidExpr)
	}
	for {
		next := make([]string, 0, (len(terms)+migrateHeldGroup-1)/migrateHeldGroup)
		for i := 0; i < len(terms); i += migrateHeldGroup {
			j := i + migrateHeldGroup
			if j > len(terms) {
				j = len(terms)
			}
			next = append(next, "("+strings.Join(terms[i:j], " OR ")+")")
		}
		if len(next) == 1 {
			return next[0]
		}
		terms = next
	}
}

// IndexStats is one schema's datasync index: its row count and MaxRowID.
type IndexStats struct {
	Schema   string
	Rows     int64
	MaxRowID int64
}

// IndexSchemas lists every schema_name in sdn_record_index with its count
// and max rowid (A16: the distinct schema_name strings, so aliases merge
// deliberately).
func (m *MigrationSource) IndexSchemas() ([]IndexStats, error) {
	rows, err := m.s.db.Query(`SELECT schema_name, COUNT(*), MAX(rowid) FROM sdn_record_index GROUP BY schema_name ORDER BY schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexStats
	for rows.Next() {
		var s IndexStats
		if err := rows.Scan(&s.Schema, &s.Rows, &s.MaxRowID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MaxIndexRowID is max(rowid) over sdn_record_index (0 when empty).
func (m *MigrationSource) MaxIndexRowID() (int64, error) {
	var v sql.NullInt64
	err := m.s.db.QueryRow(`SELECT MAX(rowid) FROM sdn_record_index`).Scan(&v)
	return v.Int64, err
}

// LegacyTag is one sdn_record_source_tags row.
type LegacyTag struct {
	SourceTags
	CreatedAt int64
}

// TagsFor returns the tag rows of each CID, in insertion (rowid) order.
func (m *MigrationSource) TagsFor(schema string, cids []string) (map[string][]LegacyTag, error) {
	out := map[string][]LegacyTag{}
	for _, chunk := range migrateChunks(cids, 400) {
		args := make([]any, 0, len(chunk)+1)
		args = append(args, schema)
		for _, c := range chunk {
			args = append(args, c)
		}
		rows, err := m.s.db.Query(fmt.Sprintf(`SELECT cid, provider_id, source_name, COALESCE(source_url, ''), batch_id,
			content_key_id, producer_peer_id, producer_public_key, COALESCE(created_at, 0)
			FROM sdn_record_source_tags WHERE schema_name = ? AND cid IN (%s) ORDER BY rowid`, migratePlaceholders(len(chunk))), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid string
			var t LegacyTag
			if err := rows.Scan(&cid, &t.ProviderID, &t.SourceName, &t.SourceURL, &t.BatchID, &t.ContentKeyID,
				&t.ProducerPeerID, &t.ProducerPublicKey, &t.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			out[cid] = append(out[cid], t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MaxTagRowID is max(rowid) over sdn_record_source_tags (0 when empty).
func (m *MigrationSource) MaxTagRowID() (int64, error) {
	var v sql.NullInt64
	err := m.s.db.QueryRow(`SELECT MAX(rowid) FROM sdn_record_source_tags`).Scan(&v)
	return v.Int64, err
}

// TagRow is one sdn_record_source_tags row with its rowid.
type TagRow struct {
	RowID  int64
	Schema string
	CID    string
	LegacyTag
}

// TagsAfter returns up to limit tag rows with rowid > after, in rowid order
// (the tags a store gained after a snapshot).
func (m *MigrationSource) TagsAfter(after int64, limit int) ([]TagRow, error) {
	rows, err := m.s.db.Query(`SELECT rowid, schema_name, cid, provider_id, source_name, COALESCE(source_url, ''), batch_id,
		content_key_id, producer_peer_id, producer_public_key, COALESCE(created_at, 0)
		FROM sdn_record_source_tags WHERE rowid > ? ORDER BY rowid LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagRow
	for rows.Next() {
		var r TagRow
		if err := rows.Scan(&r.RowID, &r.Schema, &r.CID, &r.ProviderID, &r.SourceName, &r.SourceURL, &r.BatchID,
			&r.ContentKeyID, &r.ProducerPeerID, &r.ProducerPublicKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PartitionCounter is one row of the sdn_partition_record_bytes oracle.
type PartitionCounter struct {
	Count int64
	Bytes int64
}

// PartitionCounters returns the per-table counters the legacy triggers keep
// (the §16.1 step 7 oracle), keyed by table name.
func (m *MigrationSource) PartitionCounters() (map[string]PartitionCounter, error) {
	rows, err := m.s.db.Query(`SELECT table_name, record_count, record_bytes FROM sdn_partition_record_bytes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PartitionCounter{}
	for rows.Next() {
		var name string
		var c PartitionCounter
		if err := rows.Scan(&name, &c.Count, &c.Bytes); err != nil {
			return nil, err
		}
		out[name] = c
	}
	return out, rows.Err()
}

// TableCounter recounts one table (count, Σ record_length): the oracle for
// a table the trigger counter does not cover.
func (m *MigrationSource) TableCounter(t LegacyTable) (PartitionCounter, error) {
	var c PartitionCounter
	err := m.s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(record_length), 0) FROM %s`, t.Name)).Scan(&c.Count, &c.Bytes)
	return c, err
}

// SourceSummaryRow is one sdn_record_source_summary lane.
type SourceSummaryRow struct {
	Schema, ProviderID, SourceName, BatchID, ProducerPeerID, ProducerPublicKey string
	Count, Bytes                                                               int64
}

// SourceSummaries returns the lane oracle.
func (m *MigrationSource) SourceSummaries() ([]SourceSummaryRow, error) {
	rows, err := m.s.db.Query(`SELECT schema_name, provider_id, source_name, batch_id, producer_peer_id, producer_public_key,
		record_count, total_bytes FROM sdn_record_source_summary`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceSummaryRow
	for rows.Next() {
		var r SourceSummaryRow
		if err := rows.Scan(&r.Schema, &r.ProviderID, &r.SourceName, &r.BatchID, &r.ProducerPeerID, &r.ProducerPublicKey,
			&r.Count, &r.Bytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// laneRecountChunk bounds one LaneRecount pass: about this many tag rows,
// and so (CIDs are hashes, spread evenly) a proportionate share of each
// table's records, sit in the engine's temp store at a time.
var laneRecountChunk = 100000

// LaneRecount recounts the lanes of schema from the tag rows of live
// records: per tag tuple, the tag rows whose record's FIRST copy (the first
// table by name holding the CID) exists, and their record bytes. It is the
// ground truth sdn_record_source_summary is maintained to equal (the summary
// is incremental and can drift; store-migrate reports any difference). Tag
// rows of orphans (no table holds the CID) count nowhere.
//
// Linear at any table count. The tag rows are taken in CID ranges of about
// laneRecountChunk rows; for each range every table is read once, in name
// order, into a TEMP table of each CID's FIRST copy length (INSERT OR IGNORE
// keeps the first table's), which the range's tag rows join. A statement per
// table excluding the CIDs of every earlier table (a chain of NOT EXISTS) hit
// SQLite's expression-depth limit (1000) at about 1,000 tables, and cost a
// quadratic number of probes and compiled subqueries below it. The engine's
// temp store is memory (TEMP_STORE=3), hence the ranges: it holds one range,
// never the schema, and nothing is written to the store file.
func (m *MigrationSource) LaneRecount(schema string, tables []LegacyTable) ([]SourceSummaryRow, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	const first = "temp.migrate_first_copy"
	if _, err := m.s.db.Exec(`DROP TABLE IF EXISTS ` + first); err != nil {
		return nil, err
	}
	if _, err := m.s.db.Exec(`CREATE TEMP TABLE migrate_first_copy (cid TEXT PRIMARY KEY, len INTEGER NOT NULL) WITHOUT ROWID`); err != nil {
		return nil, fmt.Errorf("lane recount: %w", err)
	}
	defer func() { _, _ = m.s.db.Exec(`DROP TABLE IF EXISTS ` + first) }()
	acc := map[[5]string]*SourceSummaryRow{}
	var order [][5]string
	for lo, last := "", false; !last; {
		// The range is (lo, hi]: hi is the CID of the chunk's last tag row
		// (every tag row of that CID included); the last range is open.
		var hi string
		err := m.s.db.QueryRow(`SELECT cid FROM sdn_record_source_tags WHERE schema_name = ? AND cid > ? ORDER BY cid LIMIT 1 OFFSET ?`,
			schema, lo, laneRecountChunk-1).Scan(&hi)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			last = true
		case err != nil:
			return nil, fmt.Errorf("lane recount %s range after %q: %w", schema, lo, err)
		}
		inRange, args := func(col string) string { return col + " > ?" }, []any{lo}
		if !last {
			inRange, args = func(col string) string { return col + " > ? AND " + col + " <= ?" }, []any{lo, hi}
		}
		if _, err := m.s.db.Exec(`DELETE FROM ` + first); err != nil {
			return nil, err
		}
		for _, t := range tables {
			if _, err := m.s.db.Exec(fmt.Sprintf(`INSERT OR IGNORE INTO %s (cid, len) SELECT cid, record_length FROM %s WHERE %s`,
				first, t.Name, inRange("cid")), args...); err != nil {
				return nil, fmt.Errorf("lane recount %s: %w", t.Name, err)
			}
		}
		rows, err := m.s.db.Query(`SELECT t.provider_id, t.source_name, t.batch_id, t.producer_peer_id, t.producer_public_key,
			COUNT(*), COALESCE(SUM(f.len), 0)
			FROM sdn_record_source_tags t JOIN `+first+` f ON f.cid = t.cid
			WHERE t.schema_name = ? AND `+inRange("t.cid")+`
			GROUP BY t.provider_id, t.source_name, t.batch_id, t.producer_peer_id, t.producer_public_key`, append([]any{schema}, args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k [5]string
			var n, b int64
			if err := rows.Scan(&k[0], &k[1], &k[2], &k[3], &k[4], &n, &b); err != nil {
				rows.Close()
				return nil, err
			}
			r := acc[k]
			if r == nil {
				r = &SourceSummaryRow{Schema: schema, ProviderID: k[0], SourceName: k[1], BatchID: k[2],
					ProducerPeerID: k[3], ProducerPublicKey: k[4]}
				acc[k] = r
				order = append(order, k)
			}
			r.Count += n
			r.Bytes += b
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		lo = hi
	}
	out := make([]SourceSummaryRow, 0, len(order))
	for _, k := range order {
		out = append(out, *acc[k])
	}
	return out, nil
}

// Licences returns every source-batch licence.
func (m *MigrationSource) Licences() ([]SourceBatchLicense, error) {
	rows, err := m.s.db.Query(`SELECT schema_name, provider_id, source_name, batch_id, license, license_url, citation,
		share_alike, updated_at FROM sdn_source_batch_license`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceBatchLicense
	for rows.Next() {
		var l SourceBatchLicense
		var share int64
		if err := rows.Scan(&l.SchemaName, &l.ProviderID, &l.SourceName, &l.BatchID, &l.License, &l.LicenseURL,
			&l.Citation, &share, &l.UpdatedAt); err != nil {
			return nil, err
		}
		l.ShareAlike = share != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// TableSizes returns count, Σ record_length and the p50/p99/max record
// length of one table (the --inventory distribution, §16.1 step 1).
type TableSizes struct {
	Rows, Bytes, P50, P99, Max int64
	MaxRowID                   int64
}

// TableSizes measures a table. It reads every row's record_length once.
func (m *MigrationSource) TableSizes(t LegacyTable) (TableSizes, error) {
	var ts TableSizes
	rows, err := m.s.db.Query(fmt.Sprintf(`SELECT record_length, rowid FROM %s`, t.Name))
	if err != nil {
		return ts, err
	}
	defer rows.Close()
	var lens []int64
	for rows.Next() {
		var n, rowid int64
		if err := rows.Scan(&n, &rowid); err != nil {
			return ts, err
		}
		lens = append(lens, n)
		ts.Bytes += n
		if rowid > ts.MaxRowID {
			ts.MaxRowID = rowid
		}
	}
	if err := rows.Err(); err != nil {
		return ts, err
	}
	ts.Rows = int64(len(lens))
	if len(lens) > 0 {
		sort.Slice(lens, func(i, j int) bool { return lens[i] < lens[j] })
		ts.P50 = lens[len(lens)/2]
		ts.P99 = lens[(len(lens)*99)/100]
		ts.Max = lens[len(lens)-1]
	}
	return ts, nil
}

// FullTextProgress returns sdn_record_fts_progress (schema -> last_rowid),
// or nil when the store has no FTS (22.3a-2: part of the gseq floor).
func (m *MigrationSource) FullTextProgress() (map[string]int64, error) {
	exists, err := m.s.tableExists("sdn_record_fts_progress")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := m.s.db.Query(`SELECT schema_name, last_rowid FROM sdn_record_fts_progress`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// legacyRecordTables are the record tables the control copy leaves out
// (§14): with the producer tables they are the record store T7b deletes (A1).
var legacyRecordTables = map[string]bool{
	"sdn_record_index": true, "sdn_record_source_tags": true, "sdn_record_source_summary": true,
	"sdn_partition_record_bytes": true, "sdn_engine_rows": true,
}

var createTableName = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?("[^"]+"|\S+?)(\s*\()`)
var createIndexName = regexp.MustCompile(`(?is)^\s*CREATE\s+(UNIQUE\s+)?INDEX\s+(IF\s+NOT\s+EXISTS\s+)?("[^"]+"|\S+)`)

// ControlCopyStats reports a control copy.
type ControlCopyStats struct {
	Tables, Indexes int
	Rows            int64
	FTSRows         int64
	FTSOrphans      int64 // FTS rows of orphan index rows, not copied
	Bytes           int64
}

// CopyControl writes the interim control database (§14) as dstName inside
// the store: every control table of control.flatsqldb with its rows and
// indexes. The interim full-text index goes to its own file, ftsName (A6:
// its own instance, so a control read never waits on an index window),
// keyed as it was by the legacy rowid, which is each record's gseq. The
// record tables are not copied, so the files are megabytes, not the record
// store. It runs on the legacy engine's own connection (ATTACH through its
// file root), holding the store lock.
func (m *MigrationSource) CopyControl(dstName, ftsName string) (ControlCopyStats, error) {
	if ftsName == "" {
		return ControlCopyStats{}, errors.New("control copy names must be plain file names")
	}
	return m.copyControl(dstName, ftsName, nil)
}

// format4EngineTables are the control tables format 4 keeps as engine data:
// the IQC ingest identities (C-21). A copy of them in fsql4/control.db
// would be a second, dead representation.
var format4EngineTables = map[string]bool{"sdn_record_ingest_identity": true}

// CopyControlFormat4 writes format 4's control database as dstName inside
// the store: the control tables, as CopyControl, less the ingest identities
// (engine data on format 4). It writes no full-text file: format 4 builds
// its full-text indexes itself after activation.
func (m *MigrationSource) CopyControlFormat4(dstName string) (ControlCopyStats, error) {
	return m.copyControl(dstName, "", format4EngineTables)
}

// copyControl is the control copy: the control tables less leaveOut, and,
// when ftsName is set, the interim full-text index into it.
func (m *MigrationSource) copyControl(dstName, ftsName string, leaveOut map[string]bool) (ControlCopyStats, error) {
	var st ControlCopyStats
	names := []string{dstName}
	if ftsName != "" {
		names = append(names, ftsName)
	}
	for _, name := range names {
		if strings.ContainsAny(name, "/\\'") || name == "" {
			return st, errors.New("control copy names must be plain file names")
		}
		if p := filepath.Join(m.base, name); fileExistsAt(p) {
			if err := os.Remove(p); err != nil { // a copy an interrupted run left
				return st, err
			}
		}
	}
	type obj struct{ typ, name, tbl, sql string }
	rows, err := m.s.db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE type IN ('table', 'index') ORDER BY type DESC, name`)
	if err != nil {
		return st, err
	}
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.typ, &o.name, &o.tbl, &o.sql); err != nil {
			rows.Close()
			return st, err
		}
		objs = append(objs, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	isControl := func(table string) bool {
		switch {
		case strings.HasPrefix(table, "sqlite_"), strings.HasPrefix(table, "sds_p_"), legacyRecordTables[table], leaveOut[table]:
			return false
		case strings.HasPrefix(table, "sdn_record_fts"):
			return false // copied as the FTS virtual table below
		}
		return true
	}
	defer m.s.lockWrite("store-migrate: control copy")()
	if _, err := m.s.db.Exec(fmt.Sprintf(`ATTACH DATABASE '%s' AS migrate_control`, dstName)); err != nil {
		return st, fmt.Errorf("attach %s: %w", dstName, err)
	}
	defer func() { _, _ = m.s.db.Exec(`DETACH DATABASE migrate_control`) }()
	tables := map[string]bool{}
	for _, o := range objs {
		if o.typ != "table" || !isControl(o.name) || o.sql == "" || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(o.sql)), "CREATE VIRTUAL") {
			continue
		}
		ddl := createTableName.ReplaceAllString(o.sql, `CREATE TABLE migrate_control."`+o.name+`"$3`)
		if ddl == o.sql {
			return st, fmt.Errorf("cannot rewrite the DDL of %s", o.name)
		}
		if _, err := m.s.db.Exec(ddl); err != nil {
			return st, fmt.Errorf("create %s in the copy: %w", o.name, err)
		}
		res, err := m.s.db.Exec(fmt.Sprintf(`INSERT INTO migrate_control."%s" SELECT * FROM main."%s"`, o.name, o.name))
		if err != nil {
			return st, fmt.Errorf("copy %s: %w", o.name, err)
		}
		n, _ := res.RowsAffected()
		st.Rows += n
		st.Tables++
		tables[o.name] = true
	}
	for _, o := range objs {
		if o.typ != "index" || !tables[o.tbl] || o.sql == "" {
			continue // automatic (PRIMARY KEY/UNIQUE) indexes come with their table
		}
		ddl := createIndexName.ReplaceAllString(o.sql, `CREATE ${1}INDEX migrate_control."`+o.name+`"`)
		if _, err := m.s.db.Exec(ddl); err != nil {
			return st, fmt.Errorf("create index %s in the copy: %w", o.name, err)
		}
		st.Indexes++
	}
	if exists, err := m.s.tableExists("sdn_record_fts_progress"); err == nil && exists && ftsName != "" {
		if _, err := m.s.db.Exec(fmt.Sprintf(`ATTACH DATABASE '%s' AS migrate_fts`, ftsName)); err != nil {
			return st, fmt.Errorf("attach %s: %w", ftsName, err)
		}
		defer func() { _, _ = m.s.db.Exec(`DETACH DATABASE migrate_fts`) }()
		if _, err := m.s.db.Exec(`CREATE VIRTUAL TABLE migrate_fts.sdn_record_fts USING fts5(text, tokenize='unicode61 remove_diacritics 2')`); err != nil {
			return st, fmt.Errorf("create the FTS table in the copy: %w", err)
		}
		// Only the rows of held records: an orphan's index row is not
		// migrated, so its FTS row would name a gseq no record has.
		producers, err := m.ProducerTables()
		if err != nil {
			return st, err
		}
		bySchema := map[string][]LegacyTable{}
		var schemas []string
		for _, t := range producers {
			if bySchema[t.Schema] == nil {
				schemas = append(schemas, t.Schema)
			}
			bySchema[t.Schema] = append(bySchema[t.Schema], t)
		}
		sort.Strings(schemas)
		for _, schema := range schemas {
			// CROSS JOIN keeps the index as the outer loop: one FTS rowid
			// seek per held record, never an FTS scan per standard.
			res, err := m.s.db.Exec(`INSERT INTO migrate_fts.sdn_record_fts(rowid, text)
				SELECT f.rowid, f.text FROM main.sdn_record_index idx CROSS JOIN main.sdn_record_fts f ON f.rowid = idx.rowid
				WHERE idx.schema_name = ? AND `+migrateHeldSQL(bySchema[schema], "idx.cid"), schema)
			if err != nil {
				return st, fmt.Errorf("copy the %s FTS rows: %w", schema, err)
			}
			n, _ := res.RowsAffected()
			st.FTSRows += n
		}
		var all int64
		if err := m.s.db.QueryRow(`SELECT COUNT(*) FROM main.sdn_record_fts`).Scan(&all); err != nil {
			return st, fmt.Errorf("count the FTS rows: %w", err)
		}
		st.FTSOrphans = all - st.FTSRows
		if _, err := m.s.db.Exec(`CREATE TABLE migrate_fts.sdn_record_fts_progress (schema_name TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, last_rowid INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return st, fmt.Errorf("create the FTS progress in the copy: %w", err)
		}
		if _, err := m.s.db.Exec(`INSERT INTO migrate_fts.sdn_record_fts_progress SELECT schema_name, fingerprint, last_rowid FROM main.sdn_record_fts_progress`); err != nil {
			return st, fmt.Errorf("copy the FTS progress: %w", err)
		}
	}
	for _, name := range names {
		if fi, err := os.Stat(filepath.Join(m.base, name)); err == nil {
			st.Bytes += fi.Size()
		}
	}
	return st, nil
}

func fileExistsAt(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Store exposes the opened legacy store (tests).
func (m *MigrationSource) Store() *FlatSQLStore { return m.s }

func migratePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func migrateChunks(in []string, n int) [][]string {
	var out [][]string
	for len(in) > n {
		out = append(out, in[:n])
		in = in[n:]
	}
	if len(in) > 0 {
		out = append(out, in)
	}
	return out
}
