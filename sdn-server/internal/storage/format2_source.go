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

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// MigrationSource reads a legacy store for store-migrate.
type MigrationSource struct {
	s    *FlatSQLStore
	base string
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

// IndexPage returns up to limit index rows of schema with rowid > after.
func (m *MigrationSource) IndexPage(schema string, after int64, limit int) ([]IndexRow, error) {
	rows, err := m.s.db.Query(`SELECT rowid, cid FROM sdn_record_index WHERE schema_name = ? AND rowid > ? ORDER BY rowid LIMIT ?`,
		schema, after, limit)
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
	Count, Bytes                                                              int64
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

// LaneRecount recounts the lanes of schema from the tag rows of live
// records: per tag tuple, the tag rows whose record's FIRST copy (the first
// table by name holding the CID) exists, and their record bytes. It is the
// ground truth sdn_record_source_summary is maintained to equal (the summary
// is incremental and can drift; store-migrate reports any difference).
func (m *MigrationSource) LaneRecount(schema string, tables []LegacyTable) ([]SourceSummaryRow, error) {
	acc := map[[5]string]*SourceSummaryRow{}
	for i, t := range tables {
		var notEarlier strings.Builder
		for _, e := range tables[:i] {
			fmt.Fprintf(&notEarlier, " AND NOT EXISTS (SELECT 1 FROM %s e WHERE e.cid = t.cid)", e.Name)
		}
		rows, err := m.s.db.Query(fmt.Sprintf(`SELECT t.provider_id, t.source_name, t.batch_id, t.producer_peer_id,
			t.producer_public_key, COUNT(*), COALESCE(SUM(r.record_length), 0)
			FROM sdn_record_source_tags t JOIN %s r ON r.cid = t.cid
			WHERE t.schema_name = ?%s
			GROUP BY t.provider_id, t.source_name, t.batch_id, t.producer_peer_id, t.producer_public_key`,
			t.Name, notEarlier.String()), schema)
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
			}
			r.Count += n
			r.Bytes += b
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]SourceSummaryRow, 0, len(acc))
	for _, r := range acc {
		out = append(out, *r)
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
	Bytes           int64
}

// CopyControl writes the interim control database (§14) as dstName inside
// the store: every control table of control.flatsqldb with its rows and
// indexes, and the interim FTS table (still keyed by the legacy rowid; A6
// gives FTS its own file later). The record tables are not copied, so the
// file is megabytes, not the record store. It runs on the legacy engine's
// own connection (ATTACH through its file root), holding the store lock.
func (m *MigrationSource) CopyControl(dstName string) (ControlCopyStats, error) {
	var st ControlCopyStats
	if strings.ContainsAny(dstName, "/\\'") || dstName == "" {
		return st, errors.New("control copy name must be a plain file name")
	}
	dst := filepath.Join(m.base, dstName)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(dst); err != nil { // a copy an interrupted run left
			return st, err
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
		case strings.HasPrefix(table, "sqlite_"), strings.HasPrefix(table, "sds_p_"), legacyRecordTables[table]:
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
	if exists, err := m.s.tableExists("sdn_record_fts_progress"); err == nil && exists {
		if _, err := m.s.db.Exec(`CREATE VIRTUAL TABLE migrate_control.sdn_record_fts USING fts5(text, tokenize='unicode61 remove_diacritics 2')`); err != nil {
			return st, fmt.Errorf("create the FTS table in the copy: %w", err)
		}
		res, err := m.s.db.Exec(`INSERT INTO migrate_control.sdn_record_fts(rowid, text) SELECT rowid, text FROM main.sdn_record_fts`)
		if err != nil {
			return st, fmt.Errorf("copy the FTS rows: %w", err)
		}
		st.FTSRows, _ = res.RowsAffected()
		if _, err := m.s.db.Exec(`CREATE TABLE migrate_control.sdn_record_fts_progress AS SELECT * FROM main.sdn_record_fts_progress`); err != nil {
			return st, fmt.Errorf("copy the FTS progress: %w", err)
		}
	}
	if fi, err := os.Stat(dst); err == nil {
		st.Bytes = fi.Size()
	}
	return st, nil
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
