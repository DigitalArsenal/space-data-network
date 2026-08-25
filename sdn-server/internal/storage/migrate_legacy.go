package storage

// migrate_legacy.go (loop B.7): one-shot migration of a v1 sqlite control
// database (sdn.db) into the FlatSQL-WASM engine store. The legacy database
// is read-only input; every control row is re-inserted through the engine
// driver WITH ITS LEGACY ROWID. sdn_record_index rowids are the datasync
// cursor space that deployed peers hold (docs/flatsql-store-v2.md §3), so
// sparse rowids left by GC must survive byte-for-byte: every insert is
// `INSERT ... (rowid, <cols>) VALUES (?, ...)`.
//
// Record payload bytes are NOT touched: they stay in the unchanged
// append-only <basePath>/flatsql-streams/*.flatsql files that the copied
// metadata rows keep pointing at.
//
// The migration is hot-window-bounded (docs/flatsql-store-v2.md §6): a
// full-history control DB does not fit one engine (the A.4 capacity ceiling —
// measured: a 9.66 GB sdn.db with ~3.2M index rows pins the 4 GiB wasm
// memory ceiling). With WindowLimit > 0 only the newest WindowLimit
// sdn_record_index rows — plus their source tags and per-record stream
// metadata — are migrated; rows below the window stay in the legacy
// sdn.db/streams as archive. Peers whose cursors point below the window page
// into the window start or trigger a snapshot resync (designed behavior, §3).

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// legacyMigrationSampleSize is how many evenly-spaced (schema_name, cid)
// pairs from the legacy sdn_record_index are verified (same rowid, same
// identity) in the migrated store.
const legacyMigrationSampleSize = 100

// legacyMigrationBatchRows bounds rows per destination transaction.
const legacyMigrationBatchRows = 5000

// LegacyMigrationOptions configures MigrateLegacyControl.
type LegacyMigrationOptions struct {
	// WindowLimit bounds the migration to the newest WindowLimit
	// sdn_record_index rows (the hot window, docs/flatsql-store-v2.md §6).
	// 0 migrates the full history (only safe for small stores: the engine's
	// wasm memory ceiling caps what one engine can hold).
	WindowLimit int
}

// LegacyTableMigration reports the outcome for one legacy user table.
type LegacyTableMigration struct {
	Name           string
	LegacyRows     int64
	CopiedRows     int64
	SkippedReason  string // empty when the table was copied
	WindowFiltered bool   // true when rows outside the hot window were dropped
}

// LegacyMigrationReport summarizes a MigrateLegacyControl run.
type LegacyMigrationReport struct {
	Tables []LegacyTableMigration

	// The datasync cursor head: MAX(rowid) over sdn_record_index. These two
	// MUST match or deployed peers' cursors point at a different space.
	LegacyRecordIndexMaxRowID int64
	NewRecordIndexMaxRowID    int64

	// Hot-window bounds. WindowMinRowID is 0 when the window did not
	// truncate (unlimited, or fewer legacy rows than the limit).
	WindowLimit                int
	WindowMinRowID             int64
	WindowRows                 int64 // index rows inside the window (== migrated)
	LegacyRecordIndexTotalRows int64 // all legacy index rows
	TruncatedRows              int64 // legacy index rows below the window (not migrated)

	// Tag rows: full legacy count, the count expected after window
	// filtering, and what actually landed in the new store.
	LegacyTagsCount   int64
	ExpectedTagsCount int64
	NewTagsCount      int64

	// Deterministic sample check: up to legacyMigrationSampleSize
	// evenly-spaced (rowid, schema_name, cid) triples from the legacy
	// sdn_record_index (within the window), each looked up identically in
	// the new store.
	SampleChecked    int
	SampleMatched    int
	SampleMismatches []string

	// Records resident in the engine's OMM hot window after the post-copy
	// rebuild.
	EngineRecordsIngested int64
}

// Ok reports whether the load-bearing invariants held: cursor head preserved,
// tag rows preserved (window-adjusted), and every sampled record found
// identically.
func (r *LegacyMigrationReport) Ok() bool {
	if r == nil {
		return false
	}
	return r.LegacyRecordIndexMaxRowID == r.NewRecordIndexMaxRowID &&
		r.ExpectedTagsCount == r.NewTagsCount &&
		r.SampleMatched == r.SampleChecked
}

type legacyTable struct {
	name string
	ddl  string
	// dest is the table the rows are copied INTO. It differs from name only
	// when the legacy name is reserved by an engine record vtab and the rows
	// are re-homed (legacyTableDestination).
	dest string
}

// MigrateLegacyControl copies every eligible control/metadata table from a
// READ-ONLY legacy sqlite database into this (freshly created) store,
// preserving rowids exactly — including sparse gaps left by GC — and then
// rebuilds the engine hot window so the OMM vtab reflects the imported
// records. The legacy handle is never written to.
//
// With opts.WindowLimit > 0 the copy is hot-window-bounded: sdn_record_index
// keeps only its newest WindowLimit rows (sparse rowids preserved WITHIN the
// window), and source tags plus per-record stream-metadata tables keep only
// rows whose (schema, cid) is in the window. All other control tables
// (metadata, directory, local EPMs, log index, publications, pin ledger,
// source summary) are copied in full.
func (s *FlatSQLStore) MigrateLegacyControl(legacy *sql.DB, opts LegacyMigrationOptions) (*LegacyMigrationReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tables, err := legacyUserTables(legacy)
	if err != nil {
		return nil, err
	}

	window, err := computeLegacyWindow(legacy, opts.WindowLimit)
	if err != nil {
		return nil, err
	}

	report := &LegacyMigrationReport{WindowLimit: opts.WindowLimit}

	// Copying preserves legacy row order per table but not cross-table
	// insertion order, so relax FK enforcement for the copy.
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return nil, fmt.Errorf("disable foreign keys for migration: %w", err)
	}
	defer func() {
		_, _ = s.db.Exec(`PRAGMA foreign_keys = ON`)
	}()

	for _, t := range tables {
		entry := LegacyTableMigration{Name: t.name}
		entry.LegacyRows, err = legacyCount(legacy, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quoteIdent(t.name)))
		if err != nil {
			return report, fmt.Errorf("count legacy rows in %s: %w", t.name, err)
		}
		if reason := s.legacyTableSkipReason(legacy, tables, t); reason != "" {
			entry.SkippedReason = reason
			log.Warnf("legacy migration: skipping table %s: %s", t.name, reason)
			report.Tables = append(report.Tables, entry)
			continue
		}
		t.dest = s.legacyTableDestination(t)
		if t.dest != t.name {
			log.Infof("legacy migration: re-homing table %s into %s (the canonical name is an engine record vtab here)", t.name, t.dest)
		}
		filter := s.legacyWindowFilterForTable(legacy, window, t.name)
		entry.WindowFiltered = filter != nil
		copied, err := s.copyLegacyTable(legacy, t, filter)
		if err != nil {
			return report, fmt.Errorf("copy legacy table %s: %w", t.name, err)
		}
		entry.CopiedRows = copied
		report.Tables = append(report.Tables, entry)
	}

	if err := s.fillLegacyMigrationStats(legacy, window, report); err != nil {
		return report, err
	}
	if err := s.sampleCheckRecordIndex(legacy, window, report); err != nil {
		return report, err
	}
	if err := s.appendMigratedRecordCatalogEvents(); err != nil {
		return report, fmt.Errorf("append compact record metadata after migration: %w", err)
	}

	// Rebuild the engine hot window from the (now migrated) control tables +
	// stream files so the OMM vtab reflects the imported records. Only a
	// poisoned runtime fails; per-record problems are logged and skipped.
	if err := s.rebuildEngineRecords(); err != nil {
		return report, fmt.Errorf("rebuild engine records after migration: %w", err)
	}
	report.EngineRecordsIngested = s.engineResidentOMMCountLocked()

	return report, nil
}

// legacyUserTables enumerates the legacy database's plain user tables
// (deterministically, by name): no sqlite internals, no virtual tables.
func legacyUserTables(legacy *sql.DB) ([]legacyTable, error) {
	rows, err := legacy.Query(`
		SELECT name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		  AND COALESCE(sql, '') NOT LIKE 'CREATE VIRTUAL%'
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("enumerate legacy tables: %w", err)
	}
	defer rows.Close()

	var tables []legacyTable
	for rows.Next() {
		var t legacyTable
		if err := rows.Scan(&t.name, &t.ddl); err != nil {
			return nil, fmt.Errorf("scan legacy table row: %w", err)
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy tables: %w", err)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].name < tables[j].name })
	return tables, nil
}

// legacyWindow describes the hot-window bound over the legacy
// sdn_record_index: only rows with rowid >= minRowID (the WindowLimit-th
// newest row) — and their (schema_name, cid) pairs — are migrated.
type legacyWindow struct {
	limit      int
	minRowID   int64 // 0 = no truncation
	totalRows  int64 // all legacy index rows
	windowRows int64 // index rows inside the window
	// cids: schema_name -> set of cids inside the window. Nil when the
	// window does not truncate.
	cids map[string]map[string]struct{}
}

func (w *legacyWindow) active() bool { return w != nil && w.minRowID > 0 }

func (w *legacyWindow) contains(schemaName, cid string) bool {
	if !w.active() {
		return true
	}
	set, ok := w.cids[schemaName]
	if !ok {
		return false
	}
	_, ok = set[cid]
	return ok
}

func (w *legacyWindow) containsAnySchema(cid string) bool {
	if !w.active() {
		return true
	}
	for _, set := range w.cids {
		if _, ok := set[cid]; ok {
			return true
		}
	}
	return false
}

// computeLegacyWindow resolves the hot-window bound: the rowid of the
// limit-th newest sdn_record_index row, plus the in-memory (schema, cid) set
// of every row inside the window (~WindowLimit entries).
func computeLegacyWindow(legacy *sql.DB, limit int) (*legacyWindow, error) {
	w := &legacyWindow{limit: limit}
	var err error
	w.totalRows, err = legacyCount(legacy, `SELECT COUNT(*) FROM sdn_record_index`)
	if err != nil {
		return nil, fmt.Errorf("count legacy sdn_record_index rows: %w", err)
	}
	w.windowRows = w.totalRows
	if limit <= 0 || w.totalRows <= int64(limit) {
		return w, nil
	}

	if err := legacy.QueryRow(`
		SELECT rowid FROM sdn_record_index
		ORDER BY rowid DESC LIMIT 1 OFFSET ?
	`, limit-1).Scan(&w.minRowID); err != nil {
		return nil, fmt.Errorf("resolve hot-window min rowid: %w", err)
	}

	rows, err := legacy.Query(`
		SELECT schema_name, cid FROM sdn_record_index WHERE rowid >= ?
	`, w.minRowID)
	if err != nil {
		return nil, fmt.Errorf("read hot-window (schema, cid) set: %w", err)
	}
	defer rows.Close()

	w.cids = map[string]map[string]struct{}{}
	w.windowRows = 0
	for rows.Next() {
		var schemaName, cid string
		if err := rows.Scan(&schemaName, &cid); err != nil {
			return nil, fmt.Errorf("scan hot-window row: %w", err)
		}
		set, ok := w.cids[schemaName]
		if !ok {
			set = map[string]struct{}{}
			w.cids[schemaName] = set
		}
		set[cid] = struct{}{}
		w.windowRows++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hot-window rows: %w", err)
	}
	return w, nil
}

// legacyCopyFilter bounds one table's copy to the hot window: either a rowid
// floor (sdn_record_index itself) or a per-row keep decision over the scanned
// column values.
type legacyCopyFilter struct {
	minRowID int64                                        // >0: WHERE rowid >= minRowID
	keep     func(colIdx map[string]int, vals []any) bool // nil = keep every row
}

// legacyWindowFilterForTable decides how the hot window applies to a table:
//   - sdn_record_index: rowid floor;
//   - sdn_record_source_tags: keep rows whose (schema_name, cid) is in the
//     window;
//   - per-record stream-metadata tables (canonical per-schema and routed
//     sds_p_* tables): keep rows whose cid is in the window
//     (schema-qualified when the table name implies the schema);
//   - everything else (metadata, directory, local EPMs, log index,
//     publications, pin ledger, source summary): full copy.
//
// Returns nil (no filtering) when the window is inactive.
func (s *FlatSQLStore) legacyWindowFilterForTable(legacy *sql.DB, window *legacyWindow, tableName string) *legacyCopyFilter {
	if !window.active() {
		return nil
	}
	switch tableName {
	case "sdn_record_index":
		return &legacyCopyFilter{minRowID: window.minRowID}
	case "sdn_record_source_tags":
		return &legacyCopyFilter{keep: func(colIdx map[string]int, vals []any) bool {
			si, okS := colIdx["schema_name"]
			ci, okC := colIdx["cid"]
			if !okS || !okC {
				return true
			}
			return window.contains(legacyScanString(vals[si]), legacyScanString(vals[ci]))
		}}
	case "sdn_record_source_summary":
		return nil // small; copied as-is
	}
	if !legacyTableHasStreamColumns(legacy, tableName) {
		return nil
	}
	schemaName := s.schemaForRecordTable(tableName)
	return &legacyCopyFilter{keep: func(colIdx map[string]int, vals []any) bool {
		ci, ok := colIdx["cid"]
		if !ok {
			return true
		}
		cid := legacyScanString(vals[ci])
		if schemaName != "" {
			return window.contains(schemaName, cid)
		}
		return window.containsAnySchema(cid)
	}}
}

// schemaForRecordTable maps a record-metadata table name back to its SDS
// schema: the canonical per-schema table ("CAT" -> "CAT.fbs") or a routed
// (producer, standard) table ("sds_p_x__OMM" -> "OMM.fbs"). "" when the
// table does not correspond to a known schema.
func (s *FlatSQLStore) schemaForRecordTable(tableName string) string {
	for _, schemaName := range s.validator.Schemas() {
		canonical, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			continue
		}
		if tableName == canonical {
			return schemaName
		}
		if strings.HasPrefix(tableName, "sds_p_") && strings.HasSuffix(tableName, "__"+canonical) {
			return schemaName
		}
	}
	return ""
}

// legacyScanString renders a scanned legacy value as a string for window-set
// lookups (TEXT columns scan as string; be tolerant of []byte).
func legacyScanString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// legacyTableSkipReason decides whether a legacy table must not be copied.
// Returns "" when the table should be copied.
func (s *FlatSQLStore) legacyTableSkipReason(legacy *sql.DB, all []legacyTable, t legacyTable) string {
	lowerName := strings.ToLower(t.name)
	if strings.Contains(lowerName, "_fts") {
		return "FTS shadow table"
	}
	if s.engineOwnsTableName(t.name) {
		// RE-HOME, DO NOT DROP. Every embedded standard is engine-routed now,
		// so this used to be a two-name carve-out ($OMM, $TBS) and is 227
		// names today: a blanket skip would silently discard a pre-flip
		// store's canonical rows at import. Rows in the v2 stream-metadata
		// layout are copied into the (producer, standard) table instead
		// (legacyTableDestination), where recordReadSourceFiltered still
		// unions them. Only the BLOB-era layout is skipped, and only because
		// the blob->stream migration is what owns those bytes.
		if legacyTableHasStreamColumns(legacy, t.name) {
			return ""
		}
		return "table name is reserved by an engine record vtab and the rows are in the pre-stream BLOB layout; run the blob->stream migration first"
	}
	if !legacyDDLHasDataBlob(t.ddl) {
		return ""
	}
	// BLOB-era per-standard table (pre-stream payload bytes inline). Skip it
	// ONLY when a stream-metadata twin exists in the legacy database (the v1
	// blob→stream migration already re-homed these rows); otherwise copy it
	// verbatim so no data is dropped.
	if twin := s.legacyBlobStreamTwin(legacy, all, t.name); twin != "" {
		return fmt.Sprintf("legacy BLOB-era table; stream-metadata twin %s exists", twin)
	}
	return ""
}

// legacyTableDestination is the table legacy rows are copied INTO: normally
// the legacy name itself, and the reserved standard's (producer, standard)
// table when that name belongs to an engine record vtab. The "legacy" producer
// token keeps the rows inside the routed namespace, which is what
// listProducerStandardTables (and therefore every read) enumerates.
func (s *FlatSQLStore) legacyTableDestination(t legacyTable) string {
	if !s.engineOwnsTableName(t.name) {
		return t.name
	}
	dest, err := ProducerStandardTableName("legacy", t.name+".fbs")
	if err != nil {
		return t.name
	}
	return dest
}

// legacyDDLHasDataBlob reports whether a table's original DDL declares a
// `data BLOB` payload column (the pre-stream v1 layout).
func legacyDDLHasDataBlob(ddl string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(ddl)), " ")
	normalized = strings.ReplaceAll(normalized, `"`, "")
	normalized = strings.ReplaceAll(normalized, "`", "")
	normalized = strings.ReplaceAll(normalized, "[", "")
	normalized = strings.ReplaceAll(normalized, "]", "")
	return strings.Contains(normalized, "data blob")
}

// legacyBlobStreamTwin returns the name of a legacy stream-metadata table
// holding the same standard's records as blob table blobName ("" when none):
// either the canonical per-schema metadata table (with stream_path columns)
// or any routed (producer, standard) table.
func (s *FlatSQLStore) legacyBlobStreamTwin(legacy *sql.DB, all []legacyTable, blobName string) string {
	for _, schemaName := range s.validator.Schemas() {
		if legacySchemaTableName(schemaName) != blobName {
			continue
		}
		canonical, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			continue
		}
		for _, other := range all {
			if other.name == blobName {
				continue
			}
			if other.name == canonical && legacyTableHasStreamColumns(legacy, other.name) {
				return other.name
			}
			if strings.HasPrefix(other.name, "sds_p_") && strings.HasSuffix(other.name, "__"+canonical) {
				return other.name
			}
		}
		return ""
	}
	return ""
}

// legacyTableHasStreamColumns reports whether a legacy table carries the v2
// stream-metadata layout (stream_path/stream_offset/record_length).
func legacyTableHasStreamColumns(legacy *sql.DB, tableName string) bool {
	cols, err := legacyTableColumns(legacy, tableName)
	if err != nil {
		return false
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[strings.ToLower(c.name)] = true
	}
	return have["stream_path"] && have["stream_offset"] && have["record_length"]
}

type legacyColumn struct {
	name    string
	declTyp string
	pk      int
}

func legacyTableColumns(legacy *sql.DB, tableName string) ([]legacyColumn, error) {
	rows, err := legacy.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, quoteIdent(tableName)))
	if err != nil {
		return nil, fmt.Errorf("inspect legacy columns of %s: %w", tableName, err)
	}
	defer rows.Close()

	var cols []legacyColumn
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan legacy column of %s: %w", tableName, err)
		}
		cols = append(cols, legacyColumn{name: name, declTyp: typ, pk: pk})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy columns of %s: %w", tableName, err)
	}
	return cols, nil
}

// copyLegacyTable copies rows of one legacy table into the store, preserving
// rowids. The destination table is created from the legacy DDL when the
// store's initTables did not already provide it; destination-missing columns
// are added so no legacy value is dropped. INSERT OR REPLACE lets legacy rows
// win over initTables-seeded defaults (e.g. sdn_metadata keys). A non-nil
// filter bounds the copy to the hot window.
func (s *FlatSQLStore) copyLegacyTable(legacy *sql.DB, t legacyTable, filter *legacyCopyFilter) (int64, error) {
	dest := t.dest
	if dest == "" {
		dest = t.name
	}
	exists, err := s.tableExists(dest)
	if err != nil {
		return 0, err
	}
	if !exists {
		if dest != t.name {
			// A re-homed table is created from the CANONICAL layout, never
			// from the legacy DDL: that DDL names the reserved table.
			if err := s.createSchemaMetadataTable(dest); err != nil {
				return 0, fmt.Errorf("create re-home target %s: %w", dest, err)
			}
		} else {
			if strings.TrimSpace(t.ddl) == "" {
				return 0, fmt.Errorf("legacy table %s has no DDL in sqlite_master", t.name)
			}
			if _, err := s.db.Exec(t.ddl); err != nil {
				return 0, fmt.Errorf("recreate legacy table %s from its DDL: %w", t.name, err)
			}
		}
	}

	cols, err := legacyTableColumns(legacy, t.name)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("legacy table %s reports no columns", t.name)
	}

	// Make sure every legacy column exists in the destination (older/newer
	// deployments may diverge; initTables' table wins its extra columns).
	destCols, err := s.tableColumnSet(dest)
	if err != nil {
		return 0, err
	}
	for _, c := range cols {
		if destCols[c.name] {
			continue
		}
		typ := strings.TrimSpace(c.declTyp)
		if typ == "" {
			typ = "TEXT"
		}
		if err := s.ensureColumn(dest, c.name, typ); err != nil {
			return 0, err
		}
	}

	// rowid handling: WITHOUT ROWID tables have none; a single INTEGER
	// PRIMARY KEY column IS the rowid (inserting both names would conflict).
	withoutRowid := strings.Contains(strings.ToUpper(t.ddl), "WITHOUT ROWID")
	rowidAlias := false
	if !withoutRowid {
		pkCount := 0
		var pkCol legacyColumn
		for _, c := range cols {
			if c.pk > 0 {
				pkCount++
				pkCol = c
			}
		}
		rowidAlias = pkCount == 1 && strings.EqualFold(strings.TrimSpace(pkCol.declTyp), "INTEGER")
	}
	explicitRowid := !withoutRowid && !rowidAlias

	quotedCols := make([]string, 0, len(cols)+1)
	selectCols := make([]string, 0, len(cols)+1)
	if explicitRowid {
		quotedCols = append(quotedCols, "rowid")
		selectCols = append(selectCols, "rowid")
	}
	for _, c := range cols {
		quotedCols = append(quotedCols, quoteIdent(c.name))
		selectCols = append(selectCols, quoteIdent(c.name))
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(quotedCols)), ", ")
	insertSQL := fmt.Sprintf(
		`INSERT OR REPLACE INTO %s (%s) VALUES (%s)`,
		quoteIdent(dest), strings.Join(quotedCols, ", "), placeholders,
	)
	selectSQL := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selectCols, ", "), quoteIdent(t.name))
	var selectArgs []any
	if filter != nil && filter.minRowID > 0 && !withoutRowid {
		selectSQL += ` WHERE rowid >= ?`
		selectArgs = append(selectArgs, filter.minRowID)
	}
	if !withoutRowid {
		selectSQL += ` ORDER BY rowid ASC`
	}

	// Column index of the scanned values that the keep filter sees: aligned
	// with cols (i.e. excluding the leading explicit rowid, when present).
	var keep func(vals []any) bool
	if filter != nil && filter.keep != nil {
		colIdx := make(map[string]int, len(cols))
		for i, c := range cols {
			colIdx[c.name] = i
		}
		valueOffset := 0
		if explicitRowid {
			valueOffset = 1
		}
		keep = func(vals []any) bool {
			return filter.keep(colIdx, vals[valueOffset:])
		}
	}

	rows, err := legacy.Query(selectSQL, selectArgs...)
	if err != nil {
		return 0, fmt.Errorf("read legacy rows: %w", err)
	}
	defer rows.Close()

	var copied int64
	var tx *sql.Tx
	var stmt *sql.Stmt
	closeBatch := func(commit bool) error {
		if stmt != nil {
			_ = stmt.Close()
			stmt = nil
		}
		if tx == nil {
			return nil
		}
		var err error
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		tx = nil
		return err
	}
	defer func() { _ = closeBatch(false) }()

	scan := make([]any, len(quotedCols))
	ptrs := make([]any, len(quotedCols))
	args := make([]any, len(quotedCols))
	for i := range scan {
		ptrs[i] = &scan[i]
	}
	for rows.Next() {
		if tx == nil {
			tx, err = s.db.Begin()
			if err != nil {
				return copied, fmt.Errorf("begin destination batch: %w", err)
			}
			stmt, err = tx.Prepare(insertSQL)
			if err != nil {
				return copied, fmt.Errorf("prepare destination insert: %w", err)
			}
		}
		for i := range scan {
			scan[i] = nil
		}
		if err := rows.Scan(ptrs...); err != nil {
			return copied, fmt.Errorf("scan legacy row: %w", err)
		}
		if keep != nil && !keep(scan) {
			continue
		}
		for i := range scan {
			args[i] = legacyValueForInsert(scan[i])
		}
		if _, err := stmt.Exec(args...); err != nil {
			return copied, fmt.Errorf("insert row (rowid-preserving): %w", err)
		}
		copied++
		if copied%legacyMigrationBatchRows == 0 {
			if err := closeBatch(true); err != nil {
				return copied, fmt.Errorf("commit destination batch: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, fmt.Errorf("iterate legacy rows: %w", err)
	}
	if err := closeBatch(true); err != nil {
		return copied, fmt.Errorf("commit destination batch: %w", err)
	}
	return copied, nil
}

func (s *FlatSQLStore) appendMigratedRecordCatalogEvents() error {
	if s.recordCatalog == nil {
		return nil
	}

	appendBatch := func(events *[]recordCatalogEvent, force bool) error {
		if len(*events) == 0 || (!force && len(*events) < recordCatalogReplayBatchSize) {
			return nil
		}
		if err := s.appendCatalogEvents(*events); err != nil {
			return err
		}
		*events = (*events)[:0]
		return nil
	}

	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	schemaRows, err := s.db.Query(`SELECT DISTINCT schema_name FROM sdn_record_index ORDER BY schema_name`)
	if err != nil {
		return fmt.Errorf("list migrated schemas: %w", err)
	}
	var schemas []string
	for schemaRows.Next() {
		var schemaName string
		if err := schemaRows.Scan(&schemaName); err != nil {
			schemaRows.Close()
			return fmt.Errorf("scan migrated schema: %w", err)
		}
		schemas = append(schemas, schemaName)
	}
	if err := schemaRows.Err(); err != nil {
		schemaRows.Close()
		return fmt.Errorf("iterate migrated schemas: %w", err)
	}
	schemaRows.Close()

	events := make([]recordCatalogEvent, 0, recordCatalogReplayBatchSize)
	for _, schemaName := range schemas {
		readSource, err := s.recordReadSource(schemaName)
		if err != nil {
			return fmt.Errorf("record read source for %s: %w", schemaName, err)
		}
		rows, err := s.db.Query(fmt.Sprintf(`
			SELECT idx.cid,
			       idx.source_timestamp,
			       COALESCE(rr.peer_id, ''),
			       rr.stream_path,
			       rr.stream_offset,
			       rr.record_length,
			       COALESCE(rr.signature_hex, ''),
			       COALESCE(rr.created_at, idx.created_at, idx.source_timestamp)
			FROM sdn_record_index idx
			JOIN %s rr ON rr.cid = idx.cid
			WHERE idx.schema_name = ?
			ORDER BY idx.rowid ASC
		`, readSource), schemaName)
		if err != nil {
			return fmt.Errorf("read migrated records for %s: %w", schemaName, err)
		}
		for rows.Next() {
			var cid, peerID, streamPath, signatureHex string
			var sourceTimestamp, streamOffset, recordLength, createdAt int64
			if err := rows.Scan(&cid, &sourceTimestamp, &peerID, &streamPath, &streamOffset, &recordLength, &signatureHex, &createdAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migrated record for %s: %w", schemaName, err)
			}
			data, err := s.readStreamRecordCached(files, streamPath, streamOffset, recordLength)
			if err != nil {
				rows.Close()
				return fmt.Errorf("read migrated record stream %s/%s: %w", schemaName, cid, err)
			}
			index, err := recordCatalogIndexFromStoredRow(s.db, schemaName, cid, sourceTimestamp, data)
			if err != nil {
				rows.Close()
				return fmt.Errorf("build migrated record index %s/%s: %w", schemaName, cid, err)
			}
			events = append(events, recordCatalogEvent{
				Kind:         recordCatalogEventRecordUpsert,
				SchemaName:   schemaName,
				CID:          cid,
				PeerID:       peerID,
				StreamPath:   streamPath,
				StreamOffset: streamOffset,
				RecordLength: recordLength,
				SignatureHex: signatureHex,
				Timestamp:    sourceTimestamp,
				CreatedAt:    createdAt,
				Index:        index,
			})
			if err := appendBatch(&events, false); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate migrated records for %s: %w", schemaName, err)
		}
		rows.Close()
	}
	if err := appendBatch(&events, true); err != nil {
		return err
	}

	tagRows, err := s.db.Query(`
		SELECT schema_name, cid,
		       COALESCE(provider_id, ''),
		       COALESCE(source_name, ''),
		       COALESCE(source_url, ''),
		       COALESCE(batch_id, ''),
		       COALESCE(content_key_id, ''),
		       COALESCE(producer_peer_id, ''),
		       COALESCE(producer_public_key, ''),
		       created_at
		FROM sdn_record_source_tags
		ORDER BY rowid ASC
	`)
	if err != nil {
		return fmt.Errorf("read migrated source tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var event recordCatalogEvent
		event.Kind = recordCatalogEventTagUpsert
		var tags SourceTags
		if err := tagRows.Scan(
			&event.SchemaName,
			&event.CID,
			&tags.ProviderID,
			&tags.SourceName,
			&tags.SourceURL,
			&tags.BatchID,
			&tags.ContentKeyID,
			&tags.ProducerPeerID,
			&tags.ProducerPublicKey,
			&event.CreatedAt,
		); err != nil {
			return fmt.Errorf("scan migrated source tag: %w", err)
		}
		event.Tags = normalizeSourceTags(tags)
		events = append(events, event)
		if err := appendBatch(&events, false); err != nil {
			return err
		}
	}
	if err := tagRows.Err(); err != nil {
		return fmt.Errorf("iterate migrated source tags: %w", err)
	}
	return appendBatch(&events, true)
}

// legacyValueForInsert maps a scanned legacy value onto the engine driver's
// supported parameter types (nil/bool/int64/float64/string/[]byte); anything
// else is stringified.
func legacyValueForInsert(v any) any {
	switch x := v.(type) {
	case nil, bool, int64, float64, string, []byte:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float32:
		return float64(x)
	default:
		return fmt.Sprint(x)
	}
}

func (s *FlatSQLStore) fillLegacyMigrationStats(legacy *sql.DB, window *legacyWindow, report *LegacyMigrationReport) error {
	report.WindowMinRowID = window.minRowID
	report.WindowRows = window.windowRows
	report.LegacyRecordIndexTotalRows = window.totalRows
	report.TruncatedRows = window.totalRows - window.windowRows

	var err error
	report.LegacyRecordIndexMaxRowID, err = legacyCount(legacy, `SELECT COALESCE(MAX(rowid), 0) FROM sdn_record_index`)
	if err != nil {
		return fmt.Errorf("legacy sdn_record_index max rowid: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(rowid), 0) FROM sdn_record_index`).Scan(&report.NewRecordIndexMaxRowID); err != nil {
		return fmt.Errorf("new sdn_record_index max rowid: %w", err)
	}
	report.LegacyTagsCount, err = legacyCount(legacy, `SELECT COUNT(*) FROM sdn_record_source_tags`)
	if err != nil {
		return fmt.Errorf("legacy sdn_record_source_tags count: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_source_tags`).Scan(&report.NewTagsCount); err != nil {
		return fmt.Errorf("new sdn_record_source_tags count: %w", err)
	}
	// Expected tags after window filtering == what the copy kept (the tags
	// table's PK guarantees legacy rows are unique, so kept == landed).
	report.ExpectedTagsCount = report.LegacyTagsCount
	for _, entry := range report.Tables {
		if entry.Name == "sdn_record_source_tags" {
			if entry.SkippedReason == "" {
				report.ExpectedTagsCount = entry.CopiedRows
			} else {
				report.ExpectedTagsCount = 0
			}
			break
		}
	}
	return nil
}

// legacyCount runs a single-value COUNT/MAX query against the legacy
// database, treating a missing table as 0 (older deployments).
func legacyCount(legacy *sql.DB, query string) (int64, error) {
	var n int64
	if err := legacy.QueryRow(query).Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

// sampleCheckRecordIndex verifies up to legacyMigrationSampleSize
// evenly-spaced legacy (rowid, schema_name, cid) triples exist identically in
// the migrated store: same identity at the SAME rowid (the cursor space).
// With an active window, samples are drawn from within the window only —
// rows below it were deliberately not migrated.
func (s *FlatSQLStore) sampleCheckRecordIndex(legacy *sql.DB, window *legacyWindow, report *LegacyMigrationReport) error {
	total := window.windowRows
	minRowID := window.minRowID
	if total == 0 {
		return nil
	}
	samples := int64(legacyMigrationSampleSize)
	if samples > total {
		samples = total
	}
	seen := map[int64]bool{}
	offsets := make([]int64, 0, samples)
	for i := int64(0); i < samples; i++ {
		var offset int64
		if samples == 1 {
			offset = 0
		} else {
			offset = i * (total - 1) / (samples - 1)
		}
		if seen[offset] {
			continue
		}
		seen[offset] = true
		offsets = append(offsets, offset)
	}

	for _, offset := range offsets {
		var rowID int64
		var schemaName, cid string
		err := legacy.QueryRow(`
			SELECT rowid, schema_name, cid FROM sdn_record_index
			WHERE rowid >= ?
			ORDER BY rowid ASC LIMIT 1 OFFSET ?
		`, minRowID, offset).Scan(&rowID, &schemaName, &cid)
		if err != nil {
			return fmt.Errorf("read legacy sample at offset %d: %w", offset, err)
		}
		report.SampleChecked++
		var found int64
		if err := s.db.QueryRow(`
			SELECT COUNT(*) FROM sdn_record_index
			WHERE rowid = ? AND schema_name = ? AND cid = ?
		`, rowID, schemaName, cid).Scan(&found); err != nil {
			return fmt.Errorf("check migrated sample (rowid=%d): %w", rowID, err)
		}
		if found == 1 {
			report.SampleMatched++
		} else {
			report.SampleMismatches = append(report.SampleMismatches,
				fmt.Sprintf("rowid=%d schema=%s cid=%s not found identically in migrated store", rowID, schemaName, cid))
		}
	}
	return nil
}

// engineResidentOMMCountLocked counts records resident in the engine OMM hot
// window. Caller holds s.mu (EngineRecordCount takes the lock itself and
// cannot be used here).
func (s *FlatSQLStore) engineResidentOMMCountLocked() int64 {
	if s.engineDB == nil {
		return 0
	}
	res, err := s.engineDB.Query(`SELECT COUNT(*) FROM OMM`)
	if err != nil {
		return 0
	}
	if len(res.Rows) == 1 && len(res.Rows[0]) == 1 {
		if n, ok := res.Rows[0][0].(int64); ok {
			return n
		}
	}
	return 0
}

// quoteIdent double-quotes a SQL identifier.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
