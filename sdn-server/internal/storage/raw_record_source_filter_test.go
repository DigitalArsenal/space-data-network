package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const rawRecordSourceFilterFixtureRows = 20000

func newRawRecordSourceFilterStore(t *testing.T) (*FlatSQLStore, string) {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tableName, err := store.ensureProducerStandardTable("source-filter-fixture", "OMM.fbs")
	if err != nil {
		t.Fatalf("ensureProducerStandardTable: %v", err)
	}
	insertRecords := fmt.Sprintf(`
		WITH RECURSIVE seq(i) AS (
			SELECT 1
			UNION ALL
			SELECT i + 1 FROM seq WHERE i < %d
		)
		INSERT INTO %s (
			cid, peer_id, timestamp, stream_path, stream_offset,
			record_length, signature_hex, created_at
		)
		SELECT printf('source-filter-cid-%%05d', i), 'source:fixture', i,
		       'unused.stream', 0, 32, NULL, i
		FROM seq`, rawRecordSourceFilterFixtureRows, tableName)
	if _, err := store.db.Exec(flatsqldrv.WithoutJournal(insertRecords)); err != nil {
		t.Fatalf("insert fixture records: %v", err)
	}
	if _, err := store.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
		INSERT INTO sdn_record_index (schema_name, cid, source_timestamp, created_at)
		SELECT 'OMM.fbs', cid, timestamp, created_at FROM %s`, tableName))); err != nil {
		t.Fatalf("insert fixture index: %v", err)
	}
	if _, err := store.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key, created_at
		)
		SELECT 'OMM.fbs', cid,
		       CASE WHEN rowid <= %d THEN 'provider-alpha' ELSE 'provider-beta' END,
		       CASE WHEN rowid <= %d THEN 'source-alpha' ELSE 'source-beta' END,
		       '', 'batch-fixture', '', '', '', created_at
		FROM %s`, rawRecordSourceFilterFixtureRows/2, rawRecordSourceFilterFixtureRows/2, tableName))); err != nil {
		t.Fatalf("insert fixture source tags: %v", err)
	}
	if _, err := store.db.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_record_source_summary (
			schema_name, provider_id, source_name, batch_id, producer_peer_id,
			producer_public_key, record_count, total_bytes, max_rowid, first_seen, updated_at
		)
		VALUES
			('OMM.fbs', 'provider-alpha', 'source-alpha', 'batch-fixture', '', '', 10000, 320000, 10000, 1, 10000),
			('OMM.fbs', 'provider-beta', 'source-beta', 'batch-fixture', '', '', 10000, 320000, 20000, 10001, 20000)`)); err != nil {
		t.Fatalf("insert fixture source summary: %v", err)
	}
	return store, tableName
}

func TestRawRecordSourceNameFilterCountHeadAndPage(t *testing.T) {
	store, _ := newRawRecordSourceFilterStore(t)
	absent := RawRecordQuery{
		SchemaName:     "OMM.fbs",
		SourceName:     "source-absent",
		Limit:          73,
		UseRowIDCursor: true,
	}
	started := time.Now()
	count, head, err := store.RawRecordSnapshot(absent)
	if err != nil {
		t.Fatalf("absent RawRecordSnapshot: %v", err)
	}
	refs, err := store.QueryRawRecordRefs(absent)
	if err != nil {
		t.Fatalf("absent QueryRawRecordRefs: %v", err)
	}
	t.Logf("absent source snapshot + page over %d rows: %s", rawRecordSourceFilterFixtureRows, time.Since(started).Round(time.Millisecond))
	if count != 0 || head != (RawRecordHead{}) || len(refs) != 0 {
		t.Fatalf("absent source returned count=%d head=%+v refs=%d; want all zero", count, head, len(refs))
	}

	present := absent
	present.SourceName = "source-alpha"
	present.Limit = rawRecordSourceFilterFixtureRows
	count, head, err = store.RawRecordSnapshot(present)
	if err != nil {
		t.Fatalf("present RawRecordSnapshot: %v", err)
	}
	present.MaxRowID = head.MaxRowID
	refs, err = store.QueryRawRecordRefs(present)
	if err != nil {
		t.Fatalf("present QueryRawRecordRefs: %v", err)
	}
	if count != rawRecordSourceFilterFixtureRows/2 || len(refs) != rawRecordSourceFilterFixtureRows/2 {
		t.Fatalf("present source returned count=%d refs=%d; want %d", count, len(refs), rawRecordSourceFilterFixtureRows/2)
	}
	for i, ref := range refs {
		if ref.SourceTags.SourceName != "source-alpha" {
			t.Fatalf("present source ref %d has source %q", i, ref.SourceTags.SourceName)
		}
	}

	readSource, err := store.rawRecordReadSource(absent.SchemaName)
	if err != nil {
		t.Fatalf("rawRecordReadSource: %v", err)
	}
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ? AND tags.source_name = ?`, rawRecordSourceTagsReadSource(absent), readSource)
	assertRawRecordSourceNameSeek(t, store, "count", countQuery, []interface{}{absent.SchemaName, absent.SourceName}, 1)
	headQuery := fmt.Sprintf(`
		SELECT COALESCE(SUM(records.record_length), 0), COALESCE(MAX(records.timestamp), 0),
		       COALESCE(MAX(tags.created_at), 0), COALESCE(MAX(records.rowid), 0)
		FROM %s
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ? AND tags.source_name = ?`, rawRecordSourceTagsReadSource(absent), readSource)
	assertRawRecordSourceNameSeek(t, store, "head", headQuery, []interface{}{absent.SchemaName, absent.SourceName}, 1)
	pageQuery, pageArgs := rawRecordRowIDSourceQuery(readSource, absent)
	assertRawRecordSourceNameSeek(t, store, "page", pageQuery, pageArgs, 2)
}

func assertRawRecordSourceNameSeek(t *testing.T, store *FlatSQLStore, label, query string, args []interface{}, wantUses int) {
	t.Helper()
	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN %s: %v", label, err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan %s plan: %v", label, err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s plan: %v", label, err)
	}
	plan := strings.Join(details, "\n")
	t.Logf("%s source-name plan:\n%s", label, plan)
	if uses := strings.Count(plan, rawRecordSourceNameCIDIndex); uses < wantUses {
		t.Fatalf("%s plan uses %s %d times; want at least %d:\n%s", label, rawRecordSourceNameCIDIndex, uses, wantUses, plan)
	}
	if strings.Contains(plan, "SCAN tags") {
		t.Fatalf("%s plan scans source tags instead of seeking %s:\n%s", label, rawRecordSourceNameCIDIndex, plan)
	}
}
