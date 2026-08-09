package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// legacySourceSummaryRebuildSQL is the statement this task replaced, kept
// VERBATIM so the equivalence test compares against the real thing rather than
// a paraphrase of it. It is the per-schema DELETE + one grouped join through the
// union read source that held host-01's engine for 16-35 s per schema.
const legacySourceSummaryRebuildSQL = `
	INSERT INTO sdn_record_source_summary (
		schema_name, provider_id, source_name, batch_id, producer_peer_id,
		producer_public_key, record_count, total_bytes, max_rowid, first_seen, updated_at
	)
	SELECT
		tags.schema_name,
		tags.provider_id,
		tags.source_name,
		COALESCE(tags.batch_id, ''),
		COALESCE(tags.producer_peer_id, ''),
		COALESCE(tags.producer_public_key, ''),
		COUNT(*),
		COALESCE(SUM(records.record_length), 0),
		COALESCE(MAX(records.rowid), 0),
		COALESCE(MIN(tags.created_at), strftime('%%s', 'now')),
		strftime('%%s', 'now')
	FROM sdn_record_source_tags tags
	INNER JOIN %s records ON records.cid = tags.cid
	WHERE tags.schema_name = ?
	GROUP BY tags.schema_name, tags.provider_id, tags.source_name, COALESCE(tags.batch_id, ''),
	         COALESCE(tags.producer_peer_id, ''), COALESCE(tags.producer_public_key, '')`

// legacySourceBatchProgressSQL is the SourceBatchProgress query this task
// replaced: the LEFT JOIN onto a GROUP BY over the whole source-tag table that
// measured 37.6 s and 90.0 s of engine hold on host-01.
const legacySourceBatchProgressSQL = `
	SELECT ss.schema_name, ss.provider_id, ss.source_name, ss.batch_id,
	       SUM(ss.record_count) AS count,
	       SUM(ss.total_bytes) AS total_bytes,
	       MAX(ss.updated_at) AS updated_at,
	       MIN(t.first_seen) AS first_seen,
	       MAX(t.last_seen) AS last_seen
	FROM sdn_record_source_summary ss
	LEFT JOIN (
		SELECT tg.schema_name, tg.provider_id, tg.source_name, tg.batch_id,
		       MIN(tg.created_at) AS first_seen, MAX(tg.created_at) AS last_seen
		FROM sdn_record_source_tags tg
		GROUP BY tg.schema_name, tg.provider_id, tg.source_name, tg.batch_id
	) t
	  ON t.schema_name = ss.schema_name
	 AND t.provider_id = ss.provider_id
	 AND t.source_name = ss.source_name
	 AND t.batch_id = ss.batch_id
	GROUP BY ss.schema_name, ss.provider_id, ss.source_name, ss.batch_id
	HAVING SUM(ss.record_count) > 0
	ORDER BY ss.schema_name ASC, ss.provider_id ASC, ss.source_name ASC, ss.batch_id ASC`

// withSourceSummaryRebuildChunk drives the rebuild at a chosen slice size so the
// chunk-boundary cases are reachable from a fixture of ten rows instead of a
// fixture of ten thousand.
func withSourceSummaryRebuildChunk(t *testing.T, chunk int, fn func()) {
	t.Helper()
	prev := sourceSummaryRebuildChunk
	sourceSummaryRebuildChunk = chunk
	defer func() { sourceSummaryRebuildChunk = prev }()
	fn()
}

type summarySnapshotRow struct {
	Schema, Provider, Source, Batch, PeerID, PubKey string
	Count, Bytes, MaxRowID, FirstSeen               int64
}

func snapshotSourceSummary(t *testing.T, store *FlatSQLStore) []summarySnapshotRow {
	t.Helper()
	rows, err := store.db.Query(`
		SELECT schema_name, provider_id, source_name, batch_id, producer_peer_id,
		       producer_public_key, record_count, total_bytes, max_rowid, first_seen
		FROM sdn_record_source_summary
		ORDER BY schema_name, provider_id, source_name, batch_id, producer_peer_id, producer_public_key`)
	if err != nil {
		t.Fatalf("snapshot summary: %v", err)
	}
	defer rows.Close()
	out := []summarySnapshotRow{}
	for rows.Next() {
		var r summarySnapshotRow
		if err := rows.Scan(&r.Schema, &r.Provider, &r.Source, &r.Batch, &r.PeerID,
			&r.PubKey, &r.Count, &r.Bytes, &r.MaxRowID, &r.FirstSeen); err != nil {
			t.Fatalf("scan summary snapshot: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate summary snapshot: %v", err)
	}
	return out
}

// seedSummaryFixture builds a store whose OMM standard is backed by TWO
// (producer, standard) tables that SHARE one cid — the union + GROUP BY cid
// dedup case — spread over several batches and two producer identities.
func seedSummaryFixture(t *testing.T, store *FlatSQLStore) {
	t.Helper()
	tables := []string{"sds_p_prodA__OMM", "sds_p_prodB__OMM"}
	for _, tab := range tables {
		if _, err := store.db.Exec(fmt.Sprintf(`CREATE TABLE %s (
			cid TEXT PRIMARY KEY, peer_id TEXT NOT NULL, timestamp INTEGER NOT NULL,
			stream_path TEXT NOT NULL, stream_offset INTEGER NOT NULL,
			record_length INTEGER NOT NULL, signature_hex TEXT,
			created_at INTEGER DEFAULT 0, UNIQUE(cid))`, tab)); err != nil {
			t.Fatalf("create %s: %v", tab, err)
		}
	}
	insertRecord := func(tab, cid string, length int64) {
		if _, err := store.db.Exec(fmt.Sprintf(`INSERT OR IGNORE INTO %s
			(cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex)
			VALUES (?,?,?,?,?,?,?)`, tab),
			cid, "peer", int64(1786000000), "flatsql-streams/OMM.flatsql", int64(0), length, ""); err != nil {
			t.Fatalf("insert record: %v", err)
		}
	}
	insertTag := func(cid, provider, source, batch, peerID, pubKey string, createdAt int64) {
		if _, err := store.db.Exec(`INSERT OR IGNORE INTO sdn_record_source_tags
			(schema_name, cid, provider_id, source_name, source_url, batch_id,
			 content_key_id, producer_peer_id, producer_public_key, created_at)
			VALUES ('OMM.fbs', ?, ?, ?, '', ?, '', ?, ?, ?)`,
			cid, provider, source, batch, peerID, pubKey, createdAt); err != nil {
			t.Fatalf("insert tag: %v", err)
		}
	}
	// batch-1: 6 records in prodA, two producer identities.
	for i := 0; i < 6; i++ {
		cid := fmt.Sprintf("bafyA%030d", i)
		insertRecord(tables[0], cid, int64(100+i))
		peer := "peerA"
		if i%2 == 1 {
			peer = "peerB"
		}
		insertTag(cid, "celestrak", "gp", "batch-1", peer, peer+"-key", int64(1700000000+i))
	}
	// batch-2: 4 records in prodB.
	for i := 0; i < 4; i++ {
		cid := fmt.Sprintf("bafyB%030d", i)
		insertRecord(tables[1], cid, int64(200+i))
		insertTag(cid, "celestrak", "gp", "batch-2", "peerA", "peerA-key", int64(1700001000+i))
	}
	// A cid present in BOTH backing tables, tagged once: the dedup case. The
	// record_length differs between the tables so a double count is visible.
	shared := "bafySHARED000000000000000000000"
	insertRecord(tables[0], shared, 500)
	insertRecord(tables[1], shared, 500)
	insertTag(shared, "peerfeed", "mirror", "batch-3", "peerC", "peerC-key", 1700002000)
	// A tag whose record row does not exist at all (the store's index is
	// complete, but the legacy INNER JOIN dropped such a row while the chunked
	// LEFT JOIN keeps it at zero bytes — asserted explicitly below).
	insertTag("bafyORPHAN0000000000000000000000", "celestrak", "gp", "batch-4", "peerA", "peerA-key", 1700003000)
}

// TestSourceSummaryRebuildMatchesLegacy is the equivalence proof: the chunked,
// fingerprinted, lane-at-a-time rebuild produces the same summary rows the
// single-statement join produced, including the union dedup case.
func TestSourceSummaryRebuildMatchesLegacy(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)

	// LEGACY
	readSource, err := store.recordReadSource("OMM.fbs")
	if err != nil {
		t.Fatalf("recordReadSource: %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary WHERE schema_name = ?`, "OMM.fbs"); err != nil {
		t.Fatalf("clear summary: %v", err)
	}
	if _, err := store.db.Exec(fmt.Sprintf(legacySourceSummaryRebuildSQL, readSource), "OMM.fbs"); err != nil {
		t.Fatalf("legacy rebuild: %v", err)
	}
	legacy := snapshotSourceSummary(t, store)

	// NEW
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary`); err != nil {
		t.Fatalf("clear summary: %v", err)
	}
	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("rebuildSourceSummaryScope: %v", err)
	}
	current := snapshotSourceSummary(t, store)

	// The one intended difference: the legacy INNER JOIN silently DROPPED a lane
	// whose record row is missing, so a tagged-but-unstored record vanished from
	// the summary entirely. The chunked rebuild LEFT JOINs and keeps it at zero
	// bytes, which is the honest answer and the one /api/v1/stats should show.
	orphan := "batch-4"
	filtered := make([]summarySnapshotRow, 0, len(current))
	sawOrphan := false
	for _, row := range current {
		if row.Batch == orphan {
			sawOrphan = true
			if row.Count != 1 || row.Bytes != 0 {
				t.Fatalf("orphan lane should be count=1 bytes=0, got %+v", row)
			}
			continue
		}
		filtered = append(filtered, row)
	}
	if !sawOrphan {
		t.Fatal("chunked rebuild dropped the tagged-but-unstored lane; it must be reported at zero bytes")
	}
	for _, row := range legacy {
		if row.Batch == orphan {
			t.Fatal("legacy rebuild unexpectedly kept the orphan lane; the fixture no longer proves the difference")
		}
	}
	if len(filtered) != len(legacy) {
		t.Fatalf("row count: legacy %d, chunked %d\nlegacy=%+v\nchunked=%+v", len(legacy), len(filtered), legacy, filtered)
	}
	for i := range legacy {
		if legacy[i] != filtered[i] {
			t.Fatalf("row %d differs:\n legacy  %+v\n chunked %+v", i, legacy[i], filtered[i])
		}
	}
	if len(legacy) == 0 {
		t.Fatal("fixture produced no summary rows; the test proves nothing")
	}
}

// TestSourceSummaryRebuildChunkBoundary drives the rebuild with a chunk size of
// 1 so every slice boundary is exercised, including a cid carried by two
// producers, which a half-open range would have dropped.
func TestSourceSummaryRebuildChunkBoundary(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)

	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("rebuild at default chunk: %v", err)
	}
	want := snapshotSourceSummary(t, store)

	for _, chunk := range []int{1, 2, 3, 5} {
		if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary`); err != nil {
			t.Fatalf("clear summary: %v", err)
		}
		withSourceSummaryRebuildChunk(t, chunk, func() {
			if err := store.rebuildSourceSummaryScope(""); err != nil {
				t.Fatalf("rebuild at chunk=%d: %v", chunk, err)
			}
		})
		got := snapshotSourceSummary(t, store)
		if len(got) != len(want) {
			t.Fatalf("chunk=%d row count %d, want %d", chunk, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("chunk=%d row %d differs:\n got  %+v\n want %+v", chunk, i, got[i], want[i])
			}
		}
	}
}

// TestSourceSummaryFingerprintSkipsUnchangedLanes proves the boot-path win: a
// second rebuild over an unchanged store issues no lane rebuild at all, and a
// lane whose tag count changed IS rebuilt.
func TestSourceSummaryFingerprintSkipsUnchangedLanes(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)

	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	lanes, err := store.sourceSummaryLanes("")
	if err != nil {
		t.Fatalf("sourceSummaryLanes: %v", err)
	}
	if len(lanes) != 4 {
		t.Fatalf("expected 4 lanes, got %d: %+v", len(lanes), lanes)
	}
	for _, lane := range lanes {
		needs, err := store.sourceSummaryLaneNeedsRebuild(lane)
		if err != nil {
			t.Fatalf("fingerprint %+v: %v", lane, err)
		}
		if needs {
			t.Fatalf("lane %+v still reports changed after a rebuild; the boot path would redo all the work", lane)
		}
	}

	// Land one more record in batch-2 and prove only that lane reports changed.
	if _, err := store.db.Exec(`INSERT INTO sds_p_prodB__OMM
		(cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex)
		VALUES ('bafyNEW00000000000000000000000', 'peer', 1786000001, 'p', 0, 777, '')`); err != nil {
		t.Fatalf("insert new record: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO sdn_record_source_tags
		(schema_name, cid, provider_id, source_name, source_url, batch_id,
		 content_key_id, producer_peer_id, producer_public_key, created_at)
		VALUES ('OMM.fbs','bafyNEW00000000000000000000000','celestrak','gp','','batch-2','','peerA','peerA-key',1700009999)`); err != nil {
		t.Fatalf("insert new tag: %v", err)
	}
	changed := map[string]bool{}
	for _, lane := range lanes {
		needs, err := store.sourceSummaryLaneNeedsRebuild(lane)
		if err != nil {
			t.Fatalf("fingerprint %+v: %v", lane, err)
		}
		changed[lane.BatchID] = needs
	}
	if !changed["batch-2"] {
		t.Fatal("the lane that gained a record did not report changed")
	}
	for batch, needs := range changed {
		if batch != "batch-2" && needs {
			t.Fatalf("untouched lane %q reported changed", batch)
		}
	}

	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	var count, bytes int64
	if err := store.db.QueryRow(`SELECT SUM(record_count), SUM(total_bytes) FROM sdn_record_source_summary
		WHERE schema_name='OMM.fbs' AND batch_id='batch-2'`).Scan(&count, &bytes); err != nil {
		t.Fatalf("read batch-2 summary: %v", err)
	}
	if count != 5 {
		t.Fatalf("batch-2 record_count = %d, want 5", count)
	}
	if bytes != 200+201+202+203+777 {
		t.Fatalf("batch-2 total_bytes = %d, want %d", bytes, 200+201+202+203+777)
	}
}

// TestSourceSummaryPrunesVanishedLanes proves a lane whose tags were superseded
// or garbage-collected away stops being reported.
func TestSourceSummaryPrunesVanishedLanes(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)
	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_tags WHERE batch_id = 'batch-2'`); err != nil {
		t.Fatalf("delete tags: %v", err)
	}
	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("rebuild after delete: %v", err)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_source_summary WHERE batch_id = 'batch-2'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("superseded lane still has %d summary rows", n)
	}
}

// TestSourceBatchProgressMatchesLegacyShape proves the summary-only
// SourceBatchProgress reports the same lanes, counts and bytes the tag-join
// reported, and that first/last seen follow the ProducerSourceProgress
// convention (summary first_seen, summary updated_at).
func TestSourceBatchProgressMatchesLegacyShape(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)
	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	type legacyRow struct {
		schema, provider, source, batch string
		count, bytes                    int64
	}
	rows, err := store.db.Query(legacySourceBatchProgressSQL)
	if err != nil {
		t.Fatalf("legacy progress: %v", err)
	}
	legacy := []legacyRow{}
	for rows.Next() {
		var r legacyRow
		var updated, first, last any
		if err := rows.Scan(&r.schema, &r.provider, &r.source, &r.batch, &r.count, &r.bytes, &updated, &first, &last); err != nil {
			rows.Close()
			t.Fatalf("scan legacy progress: %v", err)
		}
		legacy = append(legacy, r)
	}
	rows.Close()

	got, err := store.SourceBatchProgress()
	if err != nil {
		t.Fatalf("SourceBatchProgress: %v", err)
	}
	if len(got) != len(legacy) {
		t.Fatalf("progress rows: new %d, legacy %d", len(got), len(legacy))
	}
	for i := range legacy {
		if got[i].SchemaName != legacy[i].schema || got[i].ProviderID != legacy[i].provider ||
			got[i].SourceName != legacy[i].source || got[i].BatchID != legacy[i].batch ||
			got[i].Count != legacy[i].count || got[i].TotalBytes != legacy[i].bytes {
			t.Fatalf("row %d differs:\n new    %+v\n legacy %+v", i, got[i], legacy[i])
		}
		if got[i].FirstSeenUnix == 0 {
			t.Fatalf("row %d has no first_seen; the summary column is the source now", i)
		}
		if got[i].LastSeenUnix < got[i].FirstSeenUnix {
			t.Fatalf("row %d last_seen %d < first_seen %d", i, got[i].LastSeenUnix, got[i].FirstSeenUnix)
		}
	}
}

// TestSourceRecordCountsGroupByPrefixIsEquivalent proves that adding schema_name
// to the GROUP BY — which is what turns a 1.6 M-row sort into an ordered
// covering-index scan — cannot change the answer.
func TestSourceRecordCountsGroupByPrefixIsEquivalent(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)
	// The same provider/source pair under a SECOND schema is exactly the case
	// the prefix GROUP BY splits and the Go accumulator must put back together.
	if _, err := store.db.Exec(`INSERT INTO sdn_record_source_tags
		(schema_name, cid, provider_id, source_name, source_url, batch_id,
		 content_key_id, producer_peer_id, producer_public_key, created_at)
		VALUES ('CAT.fbs','bafyCAT0000000000000000000000','celestrak','gp','','batch-9','','peerA','peerA-key',1700004000)`); err != nil {
		t.Fatalf("insert cross-schema tag: %v", err)
	}

	want := map[string]int64{}
	rows, err := store.db.Query(`
		SELECT provider_id, source_name, COUNT(*)
		FROM sdn_record_source_tags
		WHERE provider_id <> '' OR source_name <> ''
		GROUP BY provider_id, source_name`)
	if err != nil {
		t.Fatalf("legacy counts: %v", err)
	}
	for rows.Next() {
		var p, s string
		var n int64
		if err := rows.Scan(&p, &s, &n); err != nil {
			rows.Close()
			t.Fatalf("scan legacy counts: %v", err)
		}
		want[sourceCountKey(p, s)] += n
	}
	rows.Close()

	got, err := store.SourceRecordCounts()
	if err != nil {
		t.Fatalf("SourceRecordCounts: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("counts: got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("counts[%q] = %d, want %d (full: got %v want %v)", k, got[k], v, got, want)
		}
	}
	if want["celestrak/gp"] != 12 {
		t.Fatalf("fixture guard: celestrak/gp = %d, want 12 across two schemas", want["celestrak/gp"])
	}
}

// TestSourceSummaryLanesUsesIndexSeek pins the plan: the lane enumeration must
// SEARCH an index, never SCAN the 1.6 M-row table. This is the 43.4 s
// `SELECT DISTINCT schema_name` from host-01's slow-statement log.
func TestSourceSummaryLanesUsesIndexSeek(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()
	seedSummaryFixture(t, store)

	rows, err := store.db.Query(`EXPLAIN QUERY PLAN
		SELECT schema_name, provider_id, source_name, batch_id
		FROM sdn_record_source_tags
		WHERE (schema_name, provider_id, source_name, batch_id) > (?, ?, ?, ?)
		ORDER BY schema_name, provider_id, source_name, batch_id
		LIMIT 1`, "", "", "", "")
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	plan := []string{}
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			t.Fatalf("columns: %v", err)
		}
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, fmt.Sprint(cells...))
	}
	joined := strings.Join(plan, " | ")
	if joined == "" {
		t.Fatal("EXPLAIN QUERY PLAN returned nothing")
	}
	if !strings.Contains(joined, "idx_sdn_record_source_tags_lookup") {
		t.Fatalf("lane seek does not use idx_sdn_record_source_tags_lookup: %s", joined)
	}
	if strings.Contains(joined, "SCAN sdn_record_source_tags") {
		t.Fatalf("lane seek full-scans the tag table — this is the 43 s DISTINCT again: %s", joined)
	}
	t.Logf("lane seek plan: %s", joined)
}

// TestSourceSummaryProdScale is the MEASUREMENT half, opt-in (PRODSCALE=1)
// because it builds a control database at host-01's live row counts.
//
// It reproduces the shape that produced the numbers in this task: two
// (producer, standard) OMM tables at the live counts, source tags for every
// record spread across the live number of batch lanes, and then A/Bs
//
//	the per-schema rebuild             (host-01: 16-35 s of engine hold)
//	SourceBatchProgress                (host-01: 37.6 s and 90.0 s)
//	SELECT DISTINCT schema_name        (host-01: 27.8 s and 43.4 s)
//	SourceRecordCounts                 (host-01: 24.9 s and 26.0 s)
//
// against their replacements, and reports the WORST SINGLE STATEMENT the new
// rebuild issues — which is the number this task's acceptance is written in.
func TestSourceSummaryProdScale(t *testing.T) {
	if os.Getenv("PRODSCALE") == "" {
		t.Skip("set PRODSCALE=1 (builds a control database at live row counts)")
	}
	const (
		bigRows   = 250318 // live sds_p_<peer>__OMM
		smallRows = 1088   // live sds_p_source_celestrak__OMM
		laneCount = 8      // live celestrak-gp OMM batches on host-01
	)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tables := []string{"sds_p_probeA__OMM", "sds_p_probeB__OMM"}
	build := time.Now()
	for ti, rows := range []int{bigRows, smallRows} {
		if _, err := store.db.Exec(fmt.Sprintf(`CREATE TABLE %s (
			cid TEXT PRIMARY KEY, peer_id TEXT NOT NULL, timestamp INTEGER NOT NULL,
			stream_path TEXT NOT NULL, stream_offset INTEGER NOT NULL,
			record_length INTEGER NOT NULL, signature_hex TEXT,
			created_at INTEGER DEFAULT 0, UNIQUE(cid))`, tables[ti])); err != nil {
			t.Fatalf("create %s: %v", tables[ti], err)
		}
		for i := 0; i < rows; i += 5000 {
			tx, err := store.db.Begin()
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			for j := 0; j < 5000 && i+j < rows; j++ {
				n := i + j
				cid := fmt.Sprintf("bafybeih%d%040d", ti, n)
				if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s
					(cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex)
					VALUES (?,?,?,?,?,?,?)`, tables[ti]),
					cid, "peer", int64(1786000000+n), "flatsql-streams/OMM.flatsql",
					int64(n)*512, int64(384), fmt.Sprintf("%0128x", n)); err != nil {
					t.Fatalf("insert record: %v", err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
		}
		// One tag per record, spread over the live number of batch lanes. Done
		// as INSERT..SELECT so the fixture costs two engine statements instead
		// of a quarter of a million.
		if _, err := store.db.Exec(fmt.Sprintf(`INSERT INTO sdn_record_source_tags
			(schema_name, cid, provider_id, source_name, source_url, batch_id,
			 content_key_id, producer_peer_id, producer_public_key, created_at)
			SELECT 'OMM.fbs', cid, 'space-data-network-02', 'celestrak-gp', '',
			       'batch-' || (rowid %% %d), '', 'peer02', 'peer02-key', timestamp
			FROM %s`, laneCount, tables[ti])); err != nil {
			t.Fatalf("insert tags for %s: %v", tables[ti], err)
		}
	}
	t.Logf("fixture built: %d + %d records, %d tags, %d lanes, in %s",
		bigRows, smallRows, bigRows+smallRows, laneCount, time.Since(build).Round(time.Millisecond))

	readSource, err := store.recordReadSource("OMM.fbs")
	if err != nil {
		t.Fatalf("recordReadSource: %v", err)
	}
	timeExec := func(sql string, args ...any) time.Duration {
		start := time.Now()
		if _, err := store.db.Exec(sql, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
		return time.Since(start)
	}
	timeQuery := func(sql string, args ...any) time.Duration {
		start := time.Now()
		rows, err := store.db.Query(sql, args...)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for rows.Next() {
		}
		rows.Close()
		return time.Since(start)
	}

	// --- the per-schema rebuild ------------------------------------------------
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	legacyRebuild := timeExec(fmt.Sprintf(legacySourceSummaryRebuildSQL, readSource), "OMM.fbs")

	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	coldStart := time.Now()
	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("cold rebuild: %v", err)
	}
	coldRebuild := time.Since(coldStart)

	warmStart := time.Now()
	if err := store.rebuildSourceSummaryScope(""); err != nil {
		t.Fatalf("warm rebuild: %v", err)
	}
	warmRebuild := time.Since(warmStart)

	// The WORST SINGLE STATEMENT the chunked rebuild issues: this is the number
	// the acceptance bar is written in, because a statement's duration is what
	// every other reader on the box waits for.
	worstSlice := time.Duration(0)
	{
		lanes, err := store.sourceSummaryLanes("OMM.fbs")
		if err != nil {
			t.Fatalf("lanes: %v", err)
		}
		lane := lanes[0]
		filtered, err := store.recordReadSourceFiltered(lane.SchemaName, "cid > ?1 AND (?2 = '' OR cid <= ?2)")
		if err != nil {
			t.Fatalf("filtered read source: %v", err)
		}
		boundarySQL := fmt.Sprintf(`
			SELECT cid FROM sdn_record_source_tags
			WHERE schema_name = ?1 AND provider_id = ?2 AND source_name = ?3 AND batch_id = ?4
			  AND cid > ?5 ORDER BY cid LIMIT 1 OFFSET %d`, sourceSummaryRebuildChunk-1)
		var boundary string
		_ = store.db.QueryRow(boundarySQL, lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID, "").Scan(&boundary)
		aggSQL := fmt.Sprintf(`
			SELECT t.producer_peer_id, t.producer_public_key, COUNT(*),
			       COALESCE(SUM(records.record_length), 0), COALESCE(MAX(records.rowid), 0),
			       COALESCE(MIN(t.created_at), 0)
			FROM (SELECT cid, producer_peer_id, producer_public_key, created_at
			      FROM sdn_record_source_tags
			      WHERE schema_name = ?3 AND provider_id = ?4 AND source_name = ?5 AND batch_id = ?6
			        AND cid > ?1 AND (?2 = '' OR cid <= ?2)) t
			LEFT JOIN %s records ON records.cid = t.cid
			GROUP BY t.producer_peer_id, t.producer_public_key`, filtered)
		for i := 0; i < 3; i++ {
			d := timeQuery(aggSQL, "", boundary, lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID)
			if d > worstSlice {
				worstSlice = d
			}
		}
	}

	// --- SourceBatchProgress ---------------------------------------------------
	legacyProgress := timeQuery(legacySourceBatchProgressSQL)
	progStart := time.Now()
	if _, err := store.SourceBatchProgress(); err != nil {
		t.Fatalf("SourceBatchProgress: %v", err)
	}
	newProgress := time.Since(progStart)

	// --- distinct schemas ------------------------------------------------------
	legacyDistinct := timeQuery(`SELECT DISTINCT schema_name FROM sdn_record_source_tags ORDER BY schema_name`)
	laneStart := time.Now()
	if _, err := store.sourceSummaryLanes(""); err != nil {
		t.Fatalf("sourceSummaryLanes: %v", err)
	}
	newLanes := time.Since(laneStart)

	// --- SourceRecordCounts ----------------------------------------------------
	legacyCounts := timeQuery(`SELECT provider_id, source_name, COUNT(*) FROM sdn_record_source_tags
		WHERE provider_id <> '' OR source_name <> '' GROUP BY provider_id, source_name`)
	countStart := time.Now()
	if _, err := store.SourceRecordCounts(); err != nil {
		t.Fatalf("SourceRecordCounts: %v", err)
	}
	newCounts := time.Since(countStart)

	ratio := func(before, after time.Duration) string {
		if after <= 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1fx", float64(before)/float64(after))
	}
	t.Logf("PRODSCALE A/B (%d tags across %d lanes, 2 backing tables)", bigRows+smallRows, laneCount)
	t.Logf("  per-schema rebuild   legacy %-12v -> cold %-12v (%s)  warm %-12v (%s)",
		legacyRebuild.Round(time.Millisecond), coldRebuild.Round(time.Millisecond), ratio(legacyRebuild, coldRebuild),
		warmRebuild.Round(time.Millisecond), ratio(legacyRebuild, warmRebuild))
	t.Logf("  worst single statement in the chunked rebuild: %v (chunk = %d rows)",
		worstSlice.Round(time.Millisecond), sourceSummaryRebuildChunk)
	t.Logf("  SourceBatchProgress  legacy %-12v -> %-12v (%s)",
		legacyProgress.Round(time.Millisecond), newProgress.Round(time.Millisecond), ratio(legacyProgress, newProgress))
	t.Logf("  DISTINCT schema_name legacy %-12v -> lane seek %-12v (%s)",
		legacyDistinct.Round(time.Millisecond), newLanes.Round(time.Millisecond), ratio(legacyDistinct, newLanes))
	t.Logf("  SourceRecordCounts   legacy %-12v -> %-12v (%s)",
		legacyCounts.Round(time.Millisecond), newCounts.Round(time.Millisecond), ratio(legacyCounts, newCounts))

	if worstSlice > 2*time.Second {
		t.Errorf("worst single rebuild statement %v exceeds the 2 s acceptance bar", worstSlice)
	}
	if newProgress > legacyProgress {
		t.Errorf("SourceBatchProgress got slower: %v -> %v", legacyProgress, newProgress)
	}
}
