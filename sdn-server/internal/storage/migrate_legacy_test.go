package storage

// migrate_legacy_test.go (loop B.7): fabricates a v1 legacy sdn.db (via the
// pure-Go modernc.org/sqlite driver) plus REAL stream files, migrates it into
// a fresh FlatSQL store, and proves the load-bearing invariant: the
// sdn_record_index rowid space — the datasync cursor deployed peers hold —
// survives EXACTLY, including sparse rowids left by GC, across the migration
// AND a store reopen (statement-journal replay). Covers both full-history
// mode (WindowLimit=0) and the hot-window-bounded mode (only the newest N
// index rows + their tags/metadata migrate; rows below the window stay in
// the legacy archive).

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// appendLegacyStreamFrame appends one size-prefixed payload frame (the
// store's exact framing: 4-byte little-endian length + bytes, see
// appendFlatSQLStreamRecord / readFlatSQLStreamRecord) and returns its
// offset/length for the metadata row.
func appendLegacyStreamFrame(t *testing.T, path string, data []byte) (offset, length int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open stream file %s: %v", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat stream file %s: %v", path, err)
	}
	offset = info.Size()
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := f.Write(prefix[:]); err != nil {
		t.Fatalf("write frame prefix: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write frame payload: %v", err)
	}
	return offset, int64(len(data))
}

func mustExecLegacy(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("legacy exec failed: %v\nquery: %s", err, query)
	}
}

type legacyIndexRow struct {
	rowID      int64
	schemaName string
	cid        string
}

const legacyRoutedOMMTable = "sds_p_peerlegacy__OMM"

type legacyFixture struct {
	base        string
	legacy      *sql.DB
	indexRows   []legacyIndexRow // ascending rowid, SPARSE (GC gaps)
	ommPayloads map[string][]byte
	catPayloads map[string][]byte
}

// payloadFor returns the stream payload bytes for a fixture cid.
func (f *legacyFixture) payloadFor(schemaName, cid string) []byte {
	if schemaName == "OMM.fbs" {
		return f.ommPayloads[cid]
	}
	return f.catPayloads[cid]
}

// buildLegacyFixture fabricates a v1 basePath: real stream files plus a
// legacy sdn.db with 12 sparse-rowid index rows (6 OMM interleaved with
// 6 CAT), tags for every record, a canonical CAT metadata table, a routed
// (producer, standard) OMM table, and decoy tables that must be skipped.
func buildLegacyFixture(t *testing.T) *legacyFixture {
	t.Helper()
	base := t.TempDir()
	streamDir := filepath.Join(base, flatSQLStreamDirName)
	if err := os.MkdirAll(streamDir, 0o700); err != nil {
		t.Fatalf("mkdir stream dir: %v", err)
	}

	f := &legacyFixture{
		base:        base,
		ommPayloads: map[string][]byte{},
		catPayloads: map[string][]byte{},
		// Sparse rowids, interleaved schemas. Newest 5: 55, 89, 144, 233, 377.
		indexRows: []legacyIndexRow{
			{2, "OMM.fbs", "cid-omm-1"},
			{3, "CAT.fbs", "cid-cat-1"},
			{5, "OMM.fbs", "cid-omm-2"},
			{8, "CAT.fbs", "cid-cat-2"},
			{13, "OMM.fbs", "cid-omm-3"},
			{21, "CAT.fbs", "cid-cat-3"},
			{34, "OMM.fbs", "cid-omm-4"},
			{55, "CAT.fbs", "cid-cat-4"},
			{89, "OMM.fbs", "cid-omm-5"},
			{144, "CAT.fbs", "cid-cat-5"},
			{233, "OMM.fbs", "cid-omm-6"},
			{377, "CAT.fbs", "cid-cat-6"},
		},
	}

	// --- Payloads + real stream files (byte-unchanged across migration). ---
	for i := 1; i <= 6; i++ {
		norad := uint32(30000 + i)
		f.ommPayloads[fmt.Sprintf("cid-omm-%d", i)] = sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectID(fmt.Sprintf("2026-%03d", norad%1000)).
			WithObjectName(fmt.Sprintf("LEGACY-SAT-%d", norad)).
			WithEpoch("2026-05-12T12:00:00Z").
			Build()
		f.catPayloads[fmt.Sprintf("cid-cat-%d", i)] = []byte(fmt.Sprintf("legacy-cat-payload-%d", i))
	}

	ommStreamRel := filepath.Join(flatSQLStreamDirName, "OMM.flatsql")
	catStreamRel := filepath.Join(flatSQLStreamDirName, "CAT.flatsql")
	type frameRef struct{ offset, length int64 }
	ommFrames := map[string]frameRef{}
	catFrames := map[string]frameRef{}
	for i := 1; i <= 6; i++ {
		ommCID := fmt.Sprintf("cid-omm-%d", i)
		off, ln := appendLegacyStreamFrame(t, filepath.Join(base, ommStreamRel), f.ommPayloads[ommCID])
		ommFrames[ommCID] = frameRef{off, ln}
		catCID := fmt.Sprintf("cid-cat-%d", i)
		off, ln = appendLegacyStreamFrame(t, filepath.Join(base, catStreamRel), f.catPayloads[catCID])
		catFrames[catCID] = frameRef{off, ln}
	}

	// --- Fabricate the legacy sdn.db (v1 control tables, pure-Go driver). ---
	legacy, err := sql.Open("sqlite", "file:"+filepath.Join(base, "sdn.db"))
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	f.legacy = legacy

	mustExecLegacy(t, legacy, `
		CREATE TABLE sdn_metadata (
			key TEXT PRIMARY KEY,
			value TEXT,
			updated_at INTEGER
		)
	`)
	mustExecLegacy(t, legacy, `INSERT INTO sdn_metadata (key, value, updated_at) VALUES ('legacy_marker', 'legacy_value', 111)`)

	mustExecLegacy(t, legacy, `
		CREATE TABLE sdn_record_index (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			norad_cat_id INTEGER,
			entity_id TEXT,
			object_type TEXT,
			ops_status_code TEXT,
			epoch_unix INTEGER,
			epoch_day TEXT,
			source_timestamp INTEGER NOT NULL,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (schema_name, cid)
		)
	`)
	for i, row := range f.indexRows {
		mustExecLegacy(t, legacy, `
			INSERT INTO sdn_record_index (rowid, schema_name, cid, source_timestamp, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, row.rowID, row.schemaName, row.cid, 1700000000+int64(i), 1700000100+int64(i))
	}

	mustExecLegacy(t, legacy, sourceTagsTableSQL("sdn_record_source_tags"))
	for _, row := range f.indexRows {
		mustExecLegacy(t, legacy, `
			INSERT INTO sdn_record_source_tags (
				schema_name, cid, provider_id, source_name, source_url, batch_id,
				content_key_id, producer_peer_id, producer_public_key, created_at
			) VALUES (?, ?, 'space-data-network-02', 'celestrak-gp', NULL, 'batch-legacy',
			          'public', 'peerlegacy', 'pk-legacy', 1700000200)
		`, row.schemaName, row.cid)
	}

	// Canonical per-schema stream-metadata table (CAT) — recreated in the v2
	// store from its legacy DDL because CAT is not engine-reserved.
	mustExecLegacy(t, legacy, schemaMetadataTableSQL("CAT"))
	for cid, frame := range catFrames {
		mustExecLegacy(t, legacy, `
			INSERT INTO CAT (cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at)
			VALUES (?, 'peerlegacy', 1700000000, ?, ?, ?, NULL, 1700000100)
		`, cid, catStreamRel, frame.offset, frame.length)
	}

	// Routed (producer, standard) table holding the OMM stream metadata.
	mustExecLegacy(t, legacy, schemaMetadataTableSQL(legacyRoutedOMMTable))
	for cid, frame := range ommFrames {
		mustExecLegacy(t, legacy, fmt.Sprintf(`
			INSERT INTO %s (cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at)
			VALUES (?, 'peerlegacy', 1700000000, ?, ?, ?, NULL, 1700000100)
		`, legacyRoutedOMMTable), cid, ommStreamRel, frame.offset, frame.length)
	}

	// Canonical OMM table: engine-reserved name in v2 — must be skipped.
	mustExecLegacy(t, legacy, schemaMetadataTableSQL("OMM"))

	// BLOB-era table with a stream-metadata twin — must be skipped, its rows
	// were already re-homed by the v1 blob→stream migration.
	mustExecLegacy(t, legacy, `
		CREATE TABLE sds_omm (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL
		)
	`)
	mustExecLegacy(t, legacy, `INSERT INTO sds_omm (cid, peer_id, timestamp, data) VALUES ('cid-omm-1', 'peerlegacy', 1700000000, X'00')`)

	// FTS shadow table — must be skipped.
	mustExecLegacy(t, legacy, `CREATE TABLE sdn_search_fts_data (id INTEGER PRIMARY KEY, block BLOB)`)
	mustExecLegacy(t, legacy, `INSERT INTO sdn_search_fts_data (id, block) VALUES (1, X'FF')`)

	return f
}

// assertMigratedWindow asserts the migrated store holds EXACTLY wantRows of
// the fixture (exact sparse rowids, identities, hydrated payload bytes, tag
// rows, cursor head, and legacy metadata) — used both right after migration
// and after a close+reopen (journal replay).
func assertMigratedWindow(t *testing.T, store *FlatSQLStore, f *legacyFixture, wantRows []legacyIndexRow, wantOMMHead int64, phase string) {
	t.Helper()

	rows, err := store.db.Query(`SELECT rowid, schema_name, cid FROM sdn_record_index ORDER BY rowid ASC`)
	if err != nil {
		t.Fatalf("[%s] query migrated index: %v", phase, err)
	}
	var got []legacyIndexRow
	for rows.Next() {
		var r legacyIndexRow
		if err := rows.Scan(&r.rowID, &r.schemaName, &r.cid); err != nil {
			t.Fatalf("[%s] scan migrated index row: %v", phase, err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("[%s] iterate migrated index: %v", phase, err)
	}
	rows.Close()
	if len(got) != len(wantRows) {
		t.Fatalf("[%s] migrated index rows = %d, want %d (%v)", phase, len(got), len(wantRows), got)
	}
	for i, want := range wantRows {
		if got[i] != want {
			t.Fatalf("[%s] migrated index row %d = %+v, want %+v (rowid space corrupted)", phase, i, got[i], want)
		}
	}

	wantMax := wantRows[len(wantRows)-1].rowID
	var maxRowID int64
	if err := store.db.QueryRow(`SELECT COALESCE(MAX(rowid), 0) FROM sdn_record_index`).Scan(&maxRowID); err != nil {
		t.Fatalf("[%s] max rowid: %v", phase, err)
	}
	if maxRowID != wantMax {
		t.Fatalf("[%s] MAX(rowid) = %d, want %d", phase, maxRowID, wantMax)
	}

	// Exactly the window's tag rows landed.
	var tagCount int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_source_tags`).Scan(&tagCount); err != nil {
		t.Fatalf("[%s] tag count: %v", phase, err)
	}
	if tagCount != int64(len(wantRows)) {
		t.Fatalf("[%s] migrated tag rows = %d, want %d", phase, tagCount, len(wantRows))
	}
	inWindow := map[string]bool{}
	for _, row := range wantRows {
		inWindow[row.schemaName+"\x00"+row.cid] = true
		var n int64
		if err := store.db.QueryRow(`
			SELECT COUNT(*) FROM sdn_record_source_tags WHERE schema_name = ? AND cid = ?
		`, row.schemaName, row.cid).Scan(&n); err != nil {
			t.Fatalf("[%s] window tag lookup: %v", phase, err)
		}
		if n != 1 {
			t.Fatalf("[%s] tag row for %s/%s = %d, want 1", phase, row.schemaName, row.cid, n)
		}
	}

	// Point reads hydrate the ORIGINAL stream bytes through the migrated
	// metadata rows — and below-window records are NOT present.
	for _, row := range f.indexRows {
		data, err := store.Get(row.schemaName, row.cid)
		if inWindow[row.schemaName+"\x00"+row.cid] {
			if err != nil {
				t.Fatalf("[%s] Get(%s, %s): %v", phase, row.schemaName, row.cid, err)
			}
			if !bytes.Equal(data, f.payloadFor(row.schemaName, row.cid)) {
				t.Fatalf("[%s] Get(%s, %s) payload bytes diverged", phase, row.schemaName, row.cid)
			}
		} else if err == nil {
			t.Fatalf("[%s] Get(%s, %s) succeeded for a below-window record (must stay in the legacy archive)", phase, row.schemaName, row.cid)
		}
	}

	// The datasync cursor head over OMM equals the newest migrated OMM rowid.
	head, err := store.RawRecordHead(RawRecordQuery{SchemaName: "OMM.fbs", UseRowIDCursor: true})
	if err != nil {
		t.Fatalf("[%s] RawRecordHead: %v", phase, err)
	}
	if head.MaxRowID != wantOMMHead {
		t.Fatalf("[%s] cursor head MaxRowID = %d, want %d", phase, head.MaxRowID, wantOMMHead)
	}

	// Seeded/overwritten metadata carries the legacy value (full copy).
	var marker string
	if err := store.db.QueryRow(`SELECT value FROM sdn_metadata WHERE key = 'legacy_marker'`).Scan(&marker); err != nil {
		t.Fatalf("[%s] read legacy_marker: %v", phase, err)
	}
	if marker != "legacy_value" {
		t.Fatalf("[%s] legacy_marker = %q, want legacy_value", phase, marker)
	}
}

func reportTableEntries(report *LegacyMigrationReport) (skipReasons map[string]string, copiedRows map[string]int64) {
	skipReasons = map[string]string{}
	copiedRows = map[string]int64{}
	for _, tab := range report.Tables {
		skipReasons[tab.Name] = tab.SkippedReason
		copiedRows[tab.Name] = tab.CopiedRows
	}
	return skipReasons, copiedRows
}

func logMigrationReport(t *testing.T, report *LegacyMigrationReport) {
	t.Helper()
	t.Logf("migration report: indexMaxRowID legacy=%d new=%d, window limit=%d minRowID=%d rows=%d/%d truncated=%d, tags legacy=%d expected=%d new=%d, sample %d/%d, engine ingested=%d",
		report.LegacyRecordIndexMaxRowID, report.NewRecordIndexMaxRowID,
		report.WindowLimit, report.WindowMinRowID, report.WindowRows,
		report.LegacyRecordIndexTotalRows, report.TruncatedRows,
		report.LegacyTagsCount, report.ExpectedTagsCount, report.NewTagsCount,
		report.SampleMatched, report.SampleChecked, report.EngineRecordsIngested)
	for _, tab := range report.Tables {
		t.Logf("  table %s: legacy=%d copied=%d windowFiltered=%v skipped=%q",
			tab.Name, tab.LegacyRows, tab.CopiedRows, tab.WindowFiltered, tab.SkippedReason)
	}
}

func TestMigrateLegacyControlUnlimitedPreservesCursorSpace(t *testing.T) {
	f := buildLegacyFixture(t)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(f.base, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	report, err := store.MigrateLegacyControl(f.legacy, LegacyMigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateLegacyControl: %v", err)
	}
	logMigrationReport(t, report)

	// Full history: all 12 rows, newest OMM rowid is 233.
	assertMigratedWindow(t, store, f, f.indexRows, 233, "unlimited")

	if !report.Ok() {
		t.Fatalf("report.Ok() = false: %+v", report)
	}
	if report.LegacyRecordIndexMaxRowID != 377 || report.NewRecordIndexMaxRowID != 377 {
		t.Fatalf("report MAX(rowid): legacy=%d new=%d, want 377/377",
			report.LegacyRecordIndexMaxRowID, report.NewRecordIndexMaxRowID)
	}
	if report.WindowMinRowID != 0 || report.TruncatedRows != 0 || report.WindowRows != 12 {
		t.Fatalf("unlimited mode window fields: minRowID=%d truncated=%d rows=%d, want 0/0/12",
			report.WindowMinRowID, report.TruncatedRows, report.WindowRows)
	}
	if report.LegacyTagsCount != 12 || report.ExpectedTagsCount != 12 || report.NewTagsCount != 12 {
		t.Fatalf("report tags: legacy=%d expected=%d new=%d, want 12/12/12",
			report.LegacyTagsCount, report.ExpectedTagsCount, report.NewTagsCount)
	}
	if report.SampleChecked != 12 || report.SampleMatched != 12 {
		t.Fatalf("report sample: %d/%d, want 12/12", report.SampleMatched, report.SampleChecked)
	}
	if report.EngineRecordsIngested != 6 {
		t.Fatalf("engine hot window ingested %d records, want 6", report.EngineRecordsIngested)
	}

	// Skip decisions.
	skipReasons, copiedRows := reportTableEntries(report)
	for _, name := range []string{"OMM", "sds_omm", "sdn_search_fts_data"} {
		if skipReasons[name] == "" {
			t.Fatalf("expected table %s to be skipped, report: %+v", name, report.Tables)
		}
	}
	for name, want := range map[string]int64{
		"sdn_record_index":       12,
		"sdn_record_source_tags": 12,
		"CAT":                    6,
		legacyRoutedOMMTable:     6,
		"sdn_metadata":           1,
	} {
		if skipReasons[name] != "" {
			t.Fatalf("table %s unexpectedly skipped: %s", name, skipReasons[name])
		}
		if copiedRows[name] != want {
			t.Fatalf("table %s copied %d rows, want %d", name, copiedRows[name], want)
		}
	}
}

func TestMigrateLegacyControlWindowLimitBoundsMigration(t *testing.T) {
	f := buildLegacyFixture(t)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(f.base, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}

	report, err := store.MigrateLegacyControl(f.legacy, LegacyMigrationOptions{WindowLimit: 5})
	if err != nil {
		t.Fatalf("MigrateLegacyControl(WindowLimit=5): %v", err)
	}
	logMigrationReport(t, report)

	// The newest 5 index rows (sparse rowids preserved WITHIN the window):
	// 55, 89, 144, 233, 377. Newest migrated OMM rowid is 233.
	windowRows := f.indexRows[len(f.indexRows)-5:]
	assertMigratedWindow(t, store, f, windowRows, 233, "windowed")

	if !report.Ok() {
		t.Fatalf("report.Ok() = false: %+v", report)
	}
	if report.WindowMinRowID != 55 {
		t.Fatalf("WindowMinRowID = %d, want 55", report.WindowMinRowID)
	}
	if report.WindowRows != 5 || report.LegacyRecordIndexTotalRows != 12 || report.TruncatedRows != 7 {
		t.Fatalf("window rows=%d total=%d truncated=%d, want 5/12/7",
			report.WindowRows, report.LegacyRecordIndexTotalRows, report.TruncatedRows)
	}
	// Head preserved: MAX(rowid) is inside the window by construction.
	if report.LegacyRecordIndexMaxRowID != 377 || report.NewRecordIndexMaxRowID != 377 {
		t.Fatalf("report MAX(rowid): legacy=%d new=%d, want 377/377",
			report.LegacyRecordIndexMaxRowID, report.NewRecordIndexMaxRowID)
	}
	if report.LegacyTagsCount != 12 || report.ExpectedTagsCount != 5 || report.NewTagsCount != 5 {
		t.Fatalf("report tags: legacy=%d expected=%d new=%d, want 12/5/5",
			report.LegacyTagsCount, report.ExpectedTagsCount, report.NewTagsCount)
	}
	if report.SampleChecked != 5 || report.SampleMatched != 5 {
		t.Fatalf("report sample: %d/%d, want 5/5 (window-scoped)", report.SampleMatched, report.SampleChecked)
	}
	// Window holds 2 OMM records (rowids 89, 233).
	if report.EngineRecordsIngested != 2 {
		t.Fatalf("engine hot window ingested %d records, want 2", report.EngineRecordsIngested)
	}

	// Per-table filtered counts: 3 window CATs, 2 window OMMs.
	skipReasons, copiedRows := reportTableEntries(report)
	for name, want := range map[string]int64{
		"sdn_record_index":       5,
		"sdn_record_source_tags": 5,
		"CAT":                    3,
		legacyRoutedOMMTable:     2,
		"sdn_metadata":           1, // full copy: not a record table
	} {
		if skipReasons[name] != "" {
			t.Fatalf("table %s unexpectedly skipped: %s", name, skipReasons[name])
		}
		if copiedRows[name] != want {
			t.Fatalf("table %s copied %d rows, want %d", name, copiedRows[name], want)
		}
	}

	// --- Reopen: the window must be reproduced by journal replay alone. ---
	if err := store.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}
	reopened, err := NewFlatSQLStore(f.base, validator)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer reopened.Close()
	assertMigratedWindow(t, reopened, f, windowRows, 233, "windowed-reopen")

	engineCount, err := reopened.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount after reopen: %v", err)
	}
	if engineCount != 2 {
		t.Fatalf("engine record count after reopen = %d, want 2", engineCount)
	}
}
