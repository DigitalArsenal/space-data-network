package storage

// The duplicate reconcile's rework (host-02, 2026-09-28/29: a 9.3 s store
// write-lock hold; 13.5 s for 256 duplicates on host-02's $OMM shape) must
// evict EXACTLY what the old single-hold reconcile evicted, must never walk the
// schema's tags, residency ledger or index rows inside a hold, and must let a
// reader in between every two lock windows.

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// ── the pre-rework implementation, kept verbatim as the reference ──────────
//
// legacyReconcileSourceBatchIndexedDuplicates is
// ReconcileSourceBatchIndexedDuplicates as it shipped up to sdn 1480b98e5:
// one write hold for the whole call.
func legacyReconcileSourceBatchIndexedDuplicates(s *FlatSQLStore, schemaName, providerID, sourceName, batchID string, apply bool) (SourceBatchDuplicateReconcileResult, error) {
	if apply {
		if err := s.requireWritable("reconcile source batch duplicates"); err != nil {
			return SourceBatchDuplicateReconcileResult{}, err
		}
	}
	result := SourceBatchDuplicateReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		BatchID:    strings.TrimSpace(batchID),
		Apply:      apply,
	}
	if result.SchemaName == "" {
		return result, errors.New("schema name is required")
	}
	if result.ProviderID == "" {
		return result, errors.New("provider id is required")
	}
	if result.SourceName == "" {
		return result, errors.New("source name is required")
	}
	if result.BatchID == "" {
		return result, errors.New("batch id is required")
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	defer s.lockWrite("ReconcileSourceBatchIndexedDuplicates")()

	// Each tag row's record is read by CID from the producer tables that
	// hold it; joining the union read source materialised every record of the
	// standard on each call (plan guard).
	tables, err := s.recordTablesForSchema(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("record tables: %w", err)
	}
	recordTimestamp := recordColumnSQL(tables, "timestamp")
	recordJoins := recordColumnJoinsSQL(tables, "tags.cid")
	recordHeld := recordJoinedHeldSQL(tables)
	args := []interface{}{result.SchemaName, result.ProviderID, result.SourceName, result.BatchID}
	countSQL := fmt.Sprintf(`
		WITH ranked AS (
			SELECT
				tags.cid,
				ROW_NUMBER() OVER (
					PARTITION BY
						COALESCE(idx.norad_cat_id, -1),
						COALESCE(idx.entity_id, ''),
						COALESCE(idx.object_type, ''),
						COALESCE(idx.ops_status_code, ''),
						COALESCE(idx.epoch_unix, -1),
						COALESCE(idx.epoch_day, '')
					ORDER BY tags.created_at DESC, %s DESC, tags.cid DESC
				) AS rn
			FROM sdn_record_source_tags tags
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = tags.schema_name AND idx.cid = tags.cid%s
			WHERE tags.schema_name = ?
			  AND tags.provider_id = ?
			  AND tags.source_name = ?
			  AND tags.batch_id = ?
			  AND %s
			  -- AN EMPTY INDEX IS NOT AN IDENTITY.
			  --
			  -- The partition above is the SATELLITE index. A standard that
			  -- populates none of it — $TBS cell sites, $IRM marks, every
			  -- non-satellite record type — lands every row in the single
			  -- partition (-1, '', '', '', -1, '') and ROW_NUMBER() marks all
			  -- but one of them a duplicate. The duplicates mode is the DEFAULT,
			  -- so a batch of six distinct cell towers reconciled down to ONE
			  -- and reported success (graph:
			  -- sdn-cellular-ingest-lands-no-batch, measured: 6 sites in, 1
			  -- stored). Records that share an ACTUAL key still collapse; a row
			  -- with no key at all is no longer anyone's duplicate.
			  AND (
			    COALESCE(idx.norad_cat_id, -1) <> -1
			    OR COALESCE(idx.entity_id, '') <> ''
			    OR COALESCE(idx.object_type, '') <> ''
			    OR COALESCE(idx.ops_status_code, '') <> ''
			    OR COALESCE(idx.epoch_unix, -1) <> -1
			    OR COALESCE(idx.epoch_day, '') <> ''
			  )
		)
		SELECT COUNT(*)
		FROM ranked
		WHERE rn > 1
	`, recordTimestamp, recordJoins, recordHeld)
	if err := s.db.QueryRow(countSQL, args...).Scan(&result.Matched); err != nil {
		return result, fmt.Errorf("count source batch duplicate records: %w", err)
	}
	if !apply || result.Matched == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin source batch duplicate reconciliation: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_duplicate_cids (cid TEXT PRIMARY KEY)`); err != nil {
		return result, fmt.Errorf("create duplicate reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM temp_sdn_reconcile_duplicate_cids`); err != nil {
		return result, fmt.Errorf("clear duplicate reconcile cid table: %w", err)
	}
	stageSQL := fmt.Sprintf(`
		INSERT OR IGNORE INTO temp_sdn_reconcile_duplicate_cids (cid)
		WITH ranked AS (
			SELECT
				tags.cid,
				ROW_NUMBER() OVER (
					PARTITION BY
						COALESCE(idx.norad_cat_id, -1),
						COALESCE(idx.entity_id, ''),
						COALESCE(idx.object_type, ''),
						COALESCE(idx.ops_status_code, ''),
						COALESCE(idx.epoch_unix, -1),
						COALESCE(idx.epoch_day, '')
					ORDER BY tags.created_at DESC, %s DESC, tags.cid DESC
				) AS rn
			FROM sdn_record_source_tags tags
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = tags.schema_name AND idx.cid = tags.cid%s
			WHERE tags.schema_name = ?
			  AND tags.provider_id = ?
			  AND tags.source_name = ?
			  AND tags.batch_id = ?
			  AND %s
			  -- AN EMPTY INDEX IS NOT AN IDENTITY.
			  --
			  -- The partition above is the SATELLITE index. A standard that
			  -- populates none of it — $TBS cell sites, $IRM marks, every
			  -- non-satellite record type — lands every row in the single
			  -- partition (-1, '', '', '', -1, '') and ROW_NUMBER() marks all
			  -- but one of them a duplicate. The duplicates mode is the DEFAULT,
			  -- so a batch of six distinct cell towers reconciled down to ONE
			  -- and reported success (graph:
			  -- sdn-cellular-ingest-lands-no-batch, measured: 6 sites in, 1
			  -- stored). Records that share an ACTUAL key still collapse; a row
			  -- with no key at all is no longer anyone's duplicate.
			  AND (
			    COALESCE(idx.norad_cat_id, -1) <> -1
			    OR COALESCE(idx.entity_id, '') <> ''
			    OR COALESCE(idx.object_type, '') <> ''
			    OR COALESCE(idx.ops_status_code, '') <> ''
			    OR COALESCE(idx.epoch_unix, -1) <> -1
			    OR COALESCE(idx.epoch_day, '') <> ''
			  )
		)
		SELECT cid
		FROM ranked
		WHERE rn > 1
	`, recordTimestamp, recordJoins, recordHeld)
	if _, err := tx.Exec(stageSQL, args...); err != nil {
		return result, fmt.Errorf("stage source batch duplicate cids: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids)
	`, args...); err != nil {
		return result, fmt.Errorf("delete source batch duplicate tags: %w", err)
	}
	// Deleted counts LOGICAL records (per cid), independent of table layout.
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM temp_sdn_reconcile_duplicate_cids WHERE cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
		result.SchemaName,
	).Scan(&result.Deleted); err != nil {
		return result, fmt.Errorf("count orphaned duplicate records: %w", err)
	}
	s.deleteRoutedMirrorsWhere(tx, tableName,
		`cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
		result.SchemaName)
	if _, err := tx.Exec(`
		DELETE FROM sdn_record_index
		WHERE schema_name = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids)
		  AND NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = sdn_record_index.schema_name
			  AND tags.cid = sdn_record_index.cid
		  )
	`, result.SchemaName); err != nil {
		return result, fmt.Errorf("delete orphaned duplicate index rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit source batch duplicate reconciliation: %w", err)
	}
	if _, err := s.tombstoneOrphanedEngineRowsLocked(result.SchemaName); err != nil {
		return result, err
	}
	if err := s.rebuildSourceSummaryForSourceBatch(result.SchemaName, tableName, result.ProviderID, result.SourceName, result.BatchID); err != nil {
		return result, err
	}
	return result, nil
}

// ── the fixture ────────────────────────────────────────────────────────────

// duplicateFixtureSets holds the fixture's records, built once: the OMM
// builder stamps a creation date, so two builds of "the same" record are two
// CIDs. Every set is one record per object of a group, all at one epoch, so
// two sets of a group are duplicates of each other (same index key, other
// bytes).
type duplicateFixtureSets map[string][][]byte

func duplicateFixtureGroup(t *testing.T, sets duplicateFixtureSets, name string, norad uint32, n int, versions ...string) {
	t.Helper()
	for _, v := range versions {
		recs := make([][]byte, n)
		for i := range recs {
			recs[i] = buildEngineOMM(t, norad+uint32(i), fmt.Sprintf("%s %s %d", name, v, i), 1_700_000_000+int64(norad))
		}
		sets[name+v] = recs
	}
}

func newDuplicateFixtureSets(t *testing.T) duplicateFixtureSets {
	t.Helper()
	sets := duplicateFixtureSets{}
	duplicateFixtureGroup(t, sets, "G1", 1000, 40, "A", "B", "C")
	duplicateFixtureGroup(t, sets, "G2", 2000, 20, "A", "B")
	duplicateFixtureGroup(t, sets, "G2b", 2100, 20, "A", "B")
	duplicateFixtureGroup(t, sets, "G3", 3000, 20, "A", "B")
	duplicateFixtureGroup(t, sets, "G4", 4000, 10, "A", "B")
	duplicateFixtureGroup(t, sets, "G5", 5000, 20, "A")
	duplicateFixtureGroup(t, sets, "G6", 1000, 10, "D")
	duplicateFixtureGroup(t, sets, "G6b", 6000, 5, "D", "E")
	duplicateFixtureGroup(t, sets, "G7", 7000, 5, "A", "B")
	duplicateFixtureGroup(t, sets, "G8", 8000, 3, "A")
	return sets
}

// buildDuplicateReconcileFixture lays out, in lane prov-g/gp, the cases the
// reconcile of batch b1 has to tell apart (created_at and record timestamps
// are set explicitly, so both builds rank alike):
//
//	G1  40 keys, A/B/C by peer-a, created 1000/2000/3000: C kept, A and B evicted
//	G2  20 keys, A/B created in one second, A's record newer: B evicted
//	G2b 20 keys, A/B equal in created_at and timestamp: the smaller CID evicted
//	G3  20 keys, A older, but A is also tagged in lane prov-h/mirror h1:
//	    its b1 tag goes, its record stays
//	G4  10 keys, A tagged in b1 by peer-a AND peer-b (two rows of one CID,
//	    two producer tables), B newer: both of A's rows lose
//	G5  20 keys with one row each: untouched
//	G6  G1's first 10 objects again in batch b2, and G6b 5 keys duplicated
//	    inside b2: another batch of the lane, untouched by b1's reconcile
//	G7  5 keys whose newest row (A) has lost its record row: A is not ranked,
//	    so B, alone, is no one's duplicate
//	G8  3 records in h1 whose index rows are gone before the reconcile: the
//	    old reconcile's whole-ledger sweep tombstoned their residency rows,
//	    and the new one must too
func buildDuplicateReconcileFixture(t *testing.T, dir string, sets duplicateFixtureSets) *FlatSQLStore {
	t.Helper()
	s := newLockWindowStore(t, dir)
	lane := func(provider, source, batch string) SourceTags {
		return SourceTags{ProviderID: provider, SourceName: source, BatchID: batch}
	}
	b1, b2, h1 := lane("prov-g", "gp", "b1"), lane("prov-g", "gp", "b2"), lane("prov-h", "mirror", "h1")
	for _, name := range []string{"G1A", "G1B", "G1C", "G2A", "G2B", "G2bA", "G2bB", "G3A", "G3B", "G4A", "G4B", "G5A", "G7A", "G7B"} {
		supersedeFixtureStore(t, s, sets[name], "peer-a", b1)
	}
	// A second tag row for each G4 A record: same batch, another producer.
	b1b := b1
	b1b.ProducerPeerID = "peer-b"
	supersedeFixtureStore(t, s, sets["G4A"], "peer-b", b1b)
	supersedeFixtureStore(t, s, concatRecords(sets["G6D"], sets["G6bD"], sets["G6bE"]), "peer-a", b2)
	supersedeFixtureStore(t, s, concatRecords(sets["G3A"], sets["G8A"]), "peer-h", h1)

	tables, err := s.listProducerStandardTables()
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(set, batch string, createdAt, timestamp int64) {
		for _, rec := range sets[set] {
			cid := ComputeCID(rec)
			if _, err := s.db.Exec(`UPDATE sdn_record_source_tags SET created_at = ? WHERE schema_name = 'OMM.fbs' AND cid = ? AND batch_id = ?`, createdAt, cid, batch); err != nil {
				t.Fatalf("stamp %s tag: %v", set, err)
			}
			for _, pt := range tables {
				if _, err := s.db.Exec(fmt.Sprintf(`UPDATE %s SET timestamp = ? WHERE cid = ?`, pt.TableName), timestamp, cid); err != nil {
					t.Fatalf("stamp %s record: %v", set, err)
				}
			}
		}
	}
	stamp("G1A", "b1", 1000, 5000)
	stamp("G1B", "b1", 2000, 5000)
	stamp("G1C", "b1", 3000, 5000)
	stamp("G2A", "b1", 4000, 6000)
	stamp("G2B", "b1", 4000, 5000)
	stamp("G2bA", "b1", 4000, 5000)
	stamp("G2bB", "b1", 4000, 5000)
	stamp("G3A", "b1", 1000, 5000)
	stamp("G3A", "h1", 1500, 5000)
	stamp("G3B", "b1", 2000, 5000)
	stamp("G4A", "b1", 1000, 5000)
	stamp("G4B", "b1", 2000, 5000)
	stamp("G5A", "b1", 1000, 5000)
	stamp("G6D", "b2", 1000, 5000)
	stamp("G6bD", "b2", 1000, 5000)
	stamp("G6bE", "b2", 2000, 5000)
	stamp("G7A", "b1", 3000, 5000)
	stamp("G7B", "b1", 1000, 5000)
	stamp("G8A", "h1", 1000, 5000)

	for _, rec := range sets["G7A"] {
		for _, pt := range tables {
			if _, err := s.db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE cid = ?`, pt.TableName), ComputeCID(rec)); err != nil {
				t.Fatalf("drop a G7 record row: %v", err)
			}
		}
	}
	for _, rec := range sets["G8A"] {
		if _, err := s.db.Exec(`DELETE FROM sdn_record_index WHERE schema_name = 'OMM.fbs' AND cid = ?`, ComputeCID(rec)); err != nil {
			t.Fatalf("orphan a residency row: %v", err)
		}
	}
	var resident int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' AND cid = ?`, ComputeCID(sets["G8A"][0])).Scan(&resident); err != nil || resident != 1 {
		t.Fatalf("fixture: G8 residency rows = %d (%v), want 1", resident, err)
	}
	if len(tables) != 3 {
		t.Fatalf("fixture holds OMM in %d producer tables, want 3", len(tables))
	}
	return s
}

// Evictions the fixture sets up: G1 80 + G2 20 + G2b 20 + G3 20 + G4 20 rows
// lose; G1 80 + G2 20 + G2b 20 + G4 10 CIDs are left with no tag.
const (
	duplicateFixtureMatched = 160
	duplicateFixtureDeleted = 130
)

// TestReconcileDuplicatesEvictsExactlyWhatTheOldReconcileEvicted runs the old
// and the new reconcile on two byte-identical stores and compares every table
// either can touch, plus the engine's visible rows. The rank slice is shrunk
// so the batch takes many slices, some of them splitting a key's rows.
func TestReconcileDuplicatesEvictsExactlyWhatTheOldReconcileEvicted(t *testing.T) {
	prev := duplicateRankSlice
	duplicateRankSlice = 7
	defer func() { duplicateRankSlice = prev }()

	root := t.TempDir()
	sets := newDuplicateFixtureSets(t)
	oldStore := buildDuplicateReconcileFixture(t, filepath.Join(root, "old"), sets)
	newStore := buildDuplicateReconcileFixture(t, filepath.Join(root, "new"), sets)
	sources := []string{"gp", "mirror"}

	before := captureSupersedeState(t, newStore, sources)
	beforeOld := captureSupersedeState(t, oldStore, sources)
	for name, pair := range map[string][2][]string{"tags": {before.Tags, beforeOld.Tags}, "index": {before.Index, beforeOld.Index}, "ledger": {before.Ledger, beforeOld.Ledger}} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			diffSupersedeState(t, name, pair[0], pair[1])
			t.Fatalf("the two fixtures differ in %s before any reconcile; the comparison would mean nothing", name)
		}
	}

	oldDry, err := legacyReconcileSourceBatchIndexedDuplicates(oldStore, "OMM.fbs", "prov-g", "gp", "b1", false)
	if err != nil {
		t.Fatalf("old dry run: %v", err)
	}
	newDry, err := newStore.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "prov-g", "gp", "b1", false)
	if err != nil {
		t.Fatalf("new dry run: %v", err)
	}
	if oldDry.Matched != duplicateFixtureMatched || newDry.Matched != oldDry.Matched || newDry.Deleted != 0 {
		t.Fatalf("dry runs matched old %d / new %d (deleted %d), want %d and nothing deleted", oldDry.Matched, newDry.Matched, newDry.Deleted, duplicateFixtureMatched)
	}
	if got := captureSupersedeState(t, newStore, sources); !reflect.DeepEqual(got.Tags, before.Tags) || !reflect.DeepEqual(got.Ledger, before.Ledger) {
		t.Fatal("the new dry run changed the store")
	}

	oldResult, err := legacyReconcileSourceBatchIndexedDuplicates(oldStore, "OMM.fbs", "prov-g", "gp", "b1", true)
	if err != nil {
		t.Fatalf("old reconcile: %v", err)
	}
	newResult, err := newStore.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "prov-g", "gp", "b1", true)
	if err != nil {
		t.Fatalf("new reconcile: %v", err)
	}
	if oldResult.Matched != duplicateFixtureMatched || oldResult.Deleted != duplicateFixtureDeleted {
		t.Fatalf("old reconcile matched %d / deleted %d, want %d / %d: the fixture is not what this test describes",
			oldResult.Matched, oldResult.Deleted, duplicateFixtureMatched, duplicateFixtureDeleted)
	}
	if newResult.Matched != oldResult.Matched || newResult.Deleted != oldResult.Deleted {
		t.Errorf("new reconcile matched %d / deleted %d, old %d / %d", newResult.Matched, newResult.Deleted, oldResult.Matched, oldResult.Deleted)
	}
	if newResult.Chunks < 2 {
		t.Errorf("new reconcile ran %d chunks; the fixture must cross a chunk boundary", newResult.Chunks)
	}

	got := captureSupersedeState(t, newStore, sources)
	want := captureSupersedeState(t, oldStore, sources)
	diffSupersedeState(t, "source tags", got.Tags, want.Tags)
	diffSupersedeState(t, "record index", got.Index, want.Index)
	diffSupersedeState(t, "residency ledger", got.Ledger, want.Ledger)
	diffSupersedeState(t, "source summary", got.Summary, want.Summary)
	diffSupersedeState(t, "partition counters", got.Counters, want.Counters)
	if !reflect.DeepEqual(keysOf(got.Records), keysOf(want.Records)) {
		t.Errorf("producer tables %v, old %v", keysOf(got.Records), keysOf(want.Records))
	}
	for table := range want.Records {
		diffSupersedeState(t, "producer table "+table, got.Records[table], want.Records[table])
	}
	for _, source := range sources {
		diffSupersedeState(t, "engine partition OMM@"+source, got.Engine[source], want.Engine[source])
	}

	// The case each group sets up, on the new store: the comparison above is
	// not two no-ops, and it agrees with the rule, not only with the old code.
	tagged := func(set, batch string) int {
		n := 0
		for _, rec := range sets[set] {
			var c int
			if err := newStore.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_source_tags WHERE schema_name = 'OMM.fbs' AND cid = ? AND batch_id = ?`, ComputeCID(rec), batch).Scan(&c); err != nil {
				t.Fatal(err)
			}
			n += c
		}
		return n
	}
	indexed := func(set string) int {
		n := 0
		for _, rec := range sets[set] {
			var c int
			if err := newStore.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'OMM.fbs' AND cid = ?`, ComputeCID(rec)).Scan(&c); err != nil {
				t.Fatal(err)
			}
			n += c
		}
		return n
	}
	larger := 0
	for i := range sets["G2bA"] {
		if ComputeCID(sets["G2bA"][i]) > ComputeCID(sets["G2bB"][i]) {
			larger++
		}
	}
	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"G1 A+B tags in b1", tagged("G1A", "b1") + tagged("G1B", "b1"), 0},
		{"G1 C tags in b1", tagged("G1C", "b1"), 40},
		{"G1 A+B index rows", indexed("G1A") + indexed("G1B"), 0},
		{"G2 A tags (newer record)", tagged("G2A", "b1"), 20},
		{"G2 B tags", tagged("G2B", "b1"), 0},
		{"G2b tags kept (the larger CID of each pair)", tagged("G2bA", "b1") + tagged("G2bB", "b1"), 20},
		{"G2b A tags kept", tagged("G2bA", "b1"), larger},
		{"G3 A tags in b1", tagged("G3A", "b1"), 0},
		{"G3 A tags in h1", tagged("G3A", "h1"), 20},
		{"G3 A index rows (still tagged in h1)", indexed("G3A"), 20},
		{"G4 A tags in b1 (both producers)", tagged("G4A", "b1"), 0},
		{"G4 A index rows", indexed("G4A"), 0},
		{"G4 B tags", tagged("G4B", "b1"), 10},
		{"G5 tags", tagged("G5A", "b1"), 20},
		{"G6 tags in b2", tagged("G6D", "b2") + tagged("G6bD", "b2") + tagged("G6bE", "b2"), 20},
		{"G7 A tags (no record row, never ranked)", tagged("G7A", "b1"), 5},
		{"G7 B tags", tagged("G7B", "b1"), 5},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.what, c.got, c.want)
		}
	}
	if containsPrefix(got.Ledger, "OMM.fbs|"+ComputeCID(sets["G8A"][0])+"|") {
		t.Errorf("the residency row orphaned before the reconcile is still in the ledger")
	}
	if len(got.Ledger) >= len(before.Ledger) {
		t.Errorf("ledger %d -> %d: the reconcile tombstoned nothing", len(before.Ledger), len(got.Ledger))
	}

	again, err := newStore.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "prov-g", "gp", "b1", true)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again.Matched != 0 || again.Deleted != 0 || again.Chunks != 0 {
		t.Errorf("second reconcile %+v, want nothing", again)
	}
}

// ── the rank's value rules are SQLite's ────────────────────────────────────

// TestDuplicateRankComparesLikeSQLite checks compareSQLiteValues (the rank's
// order) and duplicateKeyPart (its partition equality) against the engine
// itself, for every pair of values of every storage class.
func TestDuplicateRankComparesLikeSQLite(t *testing.T) {
	s := newLockWindowStore(t, filepath.Join(t.TempDir(), "store"))
	values := []any{nil, int64(-1), int64(0), int64(5), float64(5), float64(5.5), float64(-0.5),
		int64(1 << 60), float64(1 << 60), "", "5", "a", "b", []byte("a"), []byte("5")}
	for _, a := range values {
		for _, b := range values {
			var order, same int64
			if err := s.db.QueryRow(`
				SELECT
				  CASE WHEN ?1 IS ?2 THEN 0
				       WHEN ?1 IS NULL THEN -1
				       WHEN ?2 IS NULL THEN 1
				       WHEN ?1 < ?2 THEN -1
				       ELSE 1 END,
				  ?1 IS ?2`, a, b).Scan(&order, &same); err != nil {
				t.Fatalf("compare %#v, %#v: %v", a, b, err)
			}
			if got := compareSQLiteValues(a, b); got != int(order) {
				t.Errorf("compareSQLiteValues(%#v, %#v) = %d, SQLite orders them %d", a, b, got, order)
			}
			gotSame := string(duplicateKeyPart(nil, a)) == string(duplicateKeyPart(nil, b))
			if gotSame != (same == 1) {
				t.Errorf("duplicateKeyPart(%#v) == duplicateKeyPart(%#v) is %v, SQLite says %v", a, b, gotSame, same == 1)
			}
		}
	}
}

// ── reads never wait on the reconcile ──────────────────────────────────────

// TestReconcileDuplicatesReaderWaitsAtMostOneLockWindow runs readers through
// a reconcile that evicts 3,000 duplicates and holds every read's store-lock
// wait to the longest single lock hold the reconcile reports: the lock is
// released between rank slices and chunks, and a reader queued behind one is
// admitted before the next starts.
func TestReconcileDuplicatesReaderWaitsAtMostOneLockWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	prev := duplicateRankSlice
	duplicateRankSlice = 512
	defer func() { duplicateRankSlice = prev }()

	s := newLockWindowStore(t, filepath.Join(t.TempDir(), "store"))
	const n = 3000
	older := make([][]byte, n)
	newer := make([][]byte, n)
	for i := range older {
		older[i] = buildEngineOMM(t, uint32(50000+i), fmt.Sprintf("OLD %d", i), 1_700_000_000+int64(i))
		newer[i] = buildEngineOMM(t, uint32(50000+i), fmt.Sprintf("NEW %d", i), 1_700_000_000+int64(i))
	}
	tags := SourceTags{ProviderID: "prov-s", SourceName: "gp", BatchID: "b1"}
	supersedeFixtureStore(t, s, older, "peer-s", tags)
	if _, err := s.db.Exec(`UPDATE sdn_record_source_tags SET created_at = created_at - 10 WHERE schema_name = 'OMM.fbs'`); err != nil {
		t.Fatal(err)
	}
	supersedeFixtureStore(t, s, newer, "peer-s", tags)

	var result SourceBatchDuplicateReconcileResult
	task := startLockWindowWriteTask(t, func() (int, error) {
		var err error
		result, err = s.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", tags.ProviderID, tags.SourceName, tags.BatchID, true)
		return int(result.Deleted), err
	})
	started := time.Now()
	sample := sampleReadsResponsive(s, "OMM.fbs", task.Done())
	took := time.Since(started)
	written := task.Wait()
	if written.err != nil {
		t.Fatalf("ReconcileSourceBatchIndexedDuplicates: %v", written.err)
	}
	if sample.err != nil {
		t.Fatal(sample.err)
	}
	if result.Matched != n || result.Deleted != n {
		t.Fatalf("matched %d, deleted %d; want %d each", result.Matched, result.Deleted, n)
	}
	for _, rec := range older[:10] {
		if _, err := s.GetRecord("OMM.fbs", ComputeCID(rec)); err == nil {
			t.Fatalf("an older duplicate is still readable")
		}
	}
	if result.Chunks < 3 {
		t.Fatalf("the reconcile ran %d chunks; it must release the store lock between bounded chunks", result.Chunks)
	}
	if sample.reads < 3 {
		t.Fatalf("%d reads completed during a %d-chunk reconcile (%s): readers are not getting in between holds", sample.reads, result.Chunks, took)
	}
	// One lock window, plus scheduling slack short of a second window.
	slack := result.MaxLockHold / 2
	if slack < 100*time.Millisecond {
		slack = 100 * time.Millisecond
	}
	if limit := result.MaxLockHold + slack; sample.worstLockWait > limit {
		t.Fatalf("a reader waited %s for the store lock during the reconcile; the longest single hold was %s (%d chunks in %s): a reader waited on more than one lock window",
			sample.worstLockWait, result.MaxLockHold, result.Chunks, took)
	}
	t.Logf("%d reads during a %d-chunk reconcile in %s: worst lock wait %s, longest hold %s", sample.reads, result.Chunks, took, sample.worstLockWait, result.MaxLockHold)
}

// ── every statement is bounded by its slice or chunk ───────────────────────

// TestReconcileDuplicatesNeverWalksTheSchema EXPLAINs every statement a
// reconcile runs. None may read the schema's tags, residency ledger or index
// rows beyond the batch slice or the chunk — the old orphan test (NOT IN over
// every tag of the schema) and its whole-ledger residency anti-join both did,
// and the guard must say so about them.
func TestReconcileDuplicatesNeverWalksTheSchema(t *testing.T) {
	root := t.TempDir()
	sets := newDuplicateFixtureSets(t)
	s := buildDuplicateReconcileFixture(t, filepath.Join(root, "new"), sets)
	violations, statements := supersedePlanViolations(t, s, func() {
		if got, err := s.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "prov-g", "gp", "b1", true); err != nil || got.Deleted != duplicateFixtureDeleted {
			t.Fatalf("reconcile: %+v, %v", got, err)
		}
	})
	if statements == 0 {
		t.Fatal("the guard saw no statement")
	}
	if len(violations) > 0 {
		t.Errorf("%d reconcile statement(s) walk the schema:\n  %s", len(violations), strings.Join(violations, "\n  "))
	}

	legacy := buildDuplicateReconcileFixture(t, filepath.Join(root, "old"), sets)
	oldViolations, _ := supersedePlanViolations(t, legacy, func() {
		if _, err := legacyReconcileSourceBatchIndexedDuplicates(legacy, "OMM.fbs", "prov-g", "gp", "b1", true); err != nil {
			t.Fatal(err)
		}
	})
	if len(oldViolations) == 0 {
		t.Error("the guard passes the pre-rework reconcile, whose NOT IN walked every tag of the schema: it cannot see the defect it guards")
	}
	sort.Strings(oldViolations)
	t.Logf("the pre-rework reconcile's schema walks:\n  %s", strings.Join(oldViolations, "\n  "))
}
