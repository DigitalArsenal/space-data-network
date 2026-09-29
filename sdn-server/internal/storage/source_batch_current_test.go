package storage

// The current-batch reconcile's rework (host-02, beta.78: a 5.57 s store
// write-lock hold) must evict EXACTLY what the old single-hold reconcile
// evicted, must never walk the schema's tags, residency ledger or index rows
// inside a hold, must rebuild only the summary lanes it evicts, and must let
// a reader in between every two lock windows.

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// ── the pre-rework implementation, kept verbatim as the reference ──────────
//
// legacyReconcileSourceBatch is ReconcileSourceBatch as it shipped up to sdn
// 58a742f44 (beta.80): one write hold for the whole call.
func legacyReconcileSourceBatch(s *FlatSQLStore, schemaName, providerID, sourceName, keepBatch string, apply bool) (SourceBatchReconcileResult, error) {
	if apply {
		if err := s.requireWritable("reconcile source batch"); err != nil {
			return SourceBatchReconcileResult{}, err
		}
	}
	result := SourceBatchReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		KeepBatch:  strings.TrimSpace(keepBatch),
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
	if result.KeepBatch == "" {
		return result, errors.New("keep batch is required")
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}
	if s.ps != nil {
		return s.f2ReconcileSourceBatch(result)
	}

	defer s.lockWrite("ReconcileSourceBatch")()

	// Driven by the source's tag rows, each checked against the producer
	// tables by CID: joining the union read source materialised every record
	// of the standard, four times per module ingest (plan guard).
	tables, err := s.recordTablesForSchema(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("record tables: %w", err)
	}
	args := []interface{}{result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch}
	countSQL := `
		SELECT COUNT(*)
		FROM sdn_record_source_tags tags
		WHERE tags.schema_name = ?
		  AND tags.provider_id = ?
		  AND tags.source_name = ?
		  AND tags.batch_id <> ?
		  AND ` + recordHeldSQL(tables, "tags.cid")
	if err := s.db.QueryRow(countSQL, args...).Scan(&result.Matched); err != nil {
		return result, fmt.Errorf("count source batch reconciliation records: %w", err)
	}
	if !apply || result.Matched == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin source batch reconciliation: %w", err)
	}
	defer tx.Rollback()

	cidSubquery := `
		SELECT cid
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id <> ?
	`
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_cids (cid TEXT PRIMARY KEY)`)); err != nil {
		return result, fmt.Errorf("create reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`DELETE FROM temp_sdn_reconcile_cids`)); err != nil {
		return result, fmt.Errorf("clear reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`INSERT OR IGNORE INTO temp_sdn_reconcile_cids (cid) `+cidSubquery), args...); err != nil {
		return result, fmt.Errorf("stage source batch cids: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?`), args...); err != nil {
		return result, fmt.Errorf("delete source batch tags: %w", err)
	}
	// Delete orphaned records (staged cids with no surviving source tag) from
	// every (producer, standard) table. Deleted counts LOGICAL records (per
	// cid), independent of how many tables hold the row.
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM temp_sdn_reconcile_cids staged WHERE NOT EXISTS (SELECT 1 FROM sdn_record_source_tags keep WHERE keep.schema_name = ? AND keep.cid = staged.cid)`,
		result.SchemaName,
	).Scan(&result.Deleted); err != nil {
		return result, fmt.Errorf("count orphaned source batch records: %w", err)
	}
	s.deleteRoutedMirrorsWhere(tx, tableName,
		`cid IN (SELECT staged.cid FROM temp_sdn_reconcile_cids staged WHERE NOT EXISTS (SELECT 1 FROM sdn_record_source_tags keep WHERE keep.schema_name = ? AND keep.cid = staged.cid))`,
		result.SchemaName)
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_index
		WHERE schema_name = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_cids)
		  AND NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = sdn_record_index.schema_name
			  AND tags.cid = sdn_record_index.cid
		  )
	`), result.SchemaName); err != nil {
		return result, fmt.Errorf("delete orphaned source batch index rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit source batch reconciliation: %w", err)
	}
	if _, err := s.tombstoneOrphanedEngineRowsLocked(result.SchemaName); err != nil {
		return result, err
	}
	if err := s.rebuildSourceSummaryForSchema(result.SchemaName, tableName); err != nil {
		return result, err
	}
	return result, nil
}

// ── the fixture ────────────────────────────────────────────────────────────

// currentReconcileFixtureSets is the supersede fixture's records plus two sets
// of this fixture's own, built once: the OMM builder stamps a creation date,
// so two builds of "the same" record are two CIDs.
type currentReconcileFixtureSets struct {
	supersedeFixtureSets
	e, f [][]byte
}

func newCurrentReconcileFixtureSets(t *testing.T) currentReconcileFixtureSets {
	t.Helper()
	return currentReconcileFixtureSets{
		supersedeFixtureSets: newSupersedeFixtureSets(t),
		e:                    supersedeFixtureRecords(t, 70000, "E", 10),
		f:                    supersedeFixtureRecords(t, 80000, "F", 10),
	}
}

// buildCurrentReconcileFixture is the supersede fixture (lane prov-g/gp with
// batches g1 and g2 and the kept g3, lanes prov-h/mirror and prov-g/other,
// three producer tables, one residency row orphaned beforehand; see
// buildSupersedeEquivalenceFixture) plus:
//
//	g1 E[0,10)          as peer-g1, then its producer rows dropped: tags and
//	                    index rows whose record no table holds (not matched,
//	                    still evicted)
//	g2 B[0,20)          again as peer-h: a second tag row of each CID in one
//	                    batch, and a copy in a second producer table
//	g9 C[0,10) F[0,10)  as peer-g1: a batch that sorts after the kept one
//	                    (C[0,10) stays tagged in g3)
func buildCurrentReconcileFixture(t *testing.T, dir string, sets currentReconcileFixtureSets) *FlatSQLStore {
	t.Helper()
	s := buildSupersedeEquivalenceFixture(t, dir, sets.supersedeFixtureSets)
	supersedeFixtureStore(t, s, sets.e, "peer-g1", SourceTags{ProviderID: "prov-g", SourceName: "gp", BatchID: "g1"})
	supersedeFixtureStore(t, s, sets.b[:20], "peer-h", SourceTags{ProviderID: "prov-g", SourceName: "gp", BatchID: "g2", ProducerPeerID: "peer-h"})
	supersedeFixtureStore(t, s, concatRecords(sets.c[:10], sets.f), "peer-g1", SourceTags{ProviderID: "prov-g", SourceName: "gp", BatchID: "g9"})
	dropProducerRowsForTest(t, s, sets.e)
	return s
}

func dropProducerRowsForTest(t *testing.T, s *FlatSQLStore, records [][]byte) {
	t.Helper()
	tables, err := s.listProducerStandardTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		for _, pt := range tables {
			if _, err := s.db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE cid = ?`, pt.TableName), ComputeCID(rec)); err != nil {
				t.Fatalf("drop a producer row: %v", err)
			}
		}
	}
}

// What keeping g3 evicts: g1 310 + g2 470 + g9 20 tags, of which E's 10 hold
// no record; A[80,300) B[0,150) E F are left with no tag (A[0,60) survives by
// h1, A[60,80) by x1, B[150,300) and C by g3).
const (
	currentFixtureMatched = 790
	currentFixtureDeleted = 390
)

// TestReconcileCurrentEvictsExactlyWhatTheOldReconcileEvicted runs the old
// and the new reconcile on two byte-identical stores, dry run then apply, and
// compares every table either can touch, plus the engine's visible rows. The
// count slice is shrunk so each batch takes many slices.
func TestReconcileCurrentEvictsExactlyWhatTheOldReconcileEvicted(t *testing.T) {
	prev := currentReconcileCountSlice
	currentReconcileCountSlice = 7
	defer func() { currentReconcileCountSlice = prev }()

	root := t.TempDir()
	sets := newCurrentReconcileFixtureSets(t)
	oldStore := buildCurrentReconcileFixture(t, filepath.Join(root, "old"), sets)
	newStore := buildCurrentReconcileFixture(t, filepath.Join(root, "new"), sets)
	sources := []string{"gp", "mirror", "other"}

	before := captureSupersedeState(t, newStore, sources)
	beforeOld := captureSupersedeState(t, oldStore, sources)
	for name, pair := range map[string][2][]string{"tags": {before.Tags, beforeOld.Tags}, "index": {before.Index, beforeOld.Index}, "ledger": {before.Ledger, beforeOld.Ledger}, "summary": {before.Summary, beforeOld.Summary}} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			diffSupersedeState(t, name, pair[0], pair[1])
			t.Fatalf("the two fixtures differ in %s before any reconcile; the comparison would mean nothing", name)
		}
	}

	oldDry, err := legacyReconcileSourceBatch(oldStore, "OMM.fbs", "prov-g", "gp", "g3", false)
	if err != nil {
		t.Fatalf("old dry run: %v", err)
	}
	newDry, newDryStats, err := newStore.reconcileSourceBatch("OMM.fbs", "prov-g", "gp", "g3", false)
	if err != nil {
		t.Fatalf("new dry run: %v", err)
	}
	if oldDry.Matched != currentFixtureMatched || newDry.Matched != oldDry.Matched || newDry.Deleted != 0 || newDryStats.Chunks != 0 {
		t.Fatalf("dry runs matched old %d / new %d (deleted %d, %d chunks), want %d and nothing deleted", oldDry.Matched, newDry.Matched, newDry.Deleted, newDryStats.Chunks, currentFixtureMatched)
	}
	if got := captureSupersedeState(t, newStore, sources); !reflect.DeepEqual(got, before) {
		t.Fatal("the new dry run changed the store")
	}

	oldResult, err := legacyReconcileSourceBatch(oldStore, "OMM.fbs", "prov-g", "gp", "g3", true)
	if err != nil {
		t.Fatalf("old reconcile: %v", err)
	}
	newResult, newStats, err := newStore.reconcileSourceBatch("OMM.fbs", "prov-g", "gp", "g3", true)
	if err != nil {
		t.Fatalf("new reconcile: %v", err)
	}
	if oldResult.Matched != currentFixtureMatched || oldResult.Deleted != currentFixtureDeleted {
		t.Fatalf("old reconcile matched %d / deleted %d, want %d / %d: the fixture is not what this test describes",
			oldResult.Matched, oldResult.Deleted, currentFixtureMatched, currentFixtureDeleted)
	}
	if newResult.Matched != oldResult.Matched || newResult.Deleted != oldResult.Deleted {
		t.Errorf("new reconcile matched %d / deleted %d, old %d / %d", newResult.Matched, newResult.Deleted, oldResult.Matched, oldResult.Deleted)
	}
	if newStats.Chunks < 2 {
		t.Errorf("new reconcile ran %d chunks; the fixture must cross a chunk boundary", newStats.Chunks)
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

	// The reconcile did evict, by the rule and not only like the old code.
	if n := len(before.Tags) - len(got.Tags); n != currentFixtureMatched+len(sets.e) {
		t.Errorf("%d tags evicted, want %d (every other-batch tag, held or not)", n, currentFixtureMatched+len(sets.e))
	}
	for _, row := range got.Tags {
		if parts := strings.Split(row, "|"); parts[2] == "prov-g" && parts[3] == "gp" && parts[5] != "g3" {
			t.Fatalf("a tag of an evicted batch survived: %s", row)
		}
	}
	if len(got.Ledger) >= len(before.Ledger) {
		t.Errorf("ledger %d -> %d: the reconcile tombstoned nothing", len(before.Ledger), len(got.Ledger))
	}
	if orphan := ComputeCID(sets.d[0]); containsPrefix(got.Ledger, "OMM.fbs|"+orphan+"|") {
		t.Errorf("the residency row orphaned before the reconcile is still in the ledger")
	}
	for _, rec := range concatRecords(sets.e, sets.f) {
		if containsPrefix(got.Index, "OMM.fbs|"+ComputeCID(rec)+"|") {
			t.Fatalf("an evicted record kept its index row")
		}
	}

	again, againStats, err := newStore.reconcileSourceBatch("OMM.fbs", "prov-g", "gp", "g3", true)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again.Matched != 0 || again.Deleted != 0 || againStats.Chunks != 0 {
		t.Errorf("second reconcile %+v in %d chunks, want nothing", again, againStats.Chunks)
	}
}

// TestReconcileCurrentChangesNothingWhenNothingIsHeld: the old reconcile
// returned before it deleted anything when no other-batch tag had a record,
// even with such tags present. The new one must leave them too.
func TestReconcileCurrentChangesNothingWhenNothingIsHeld(t *testing.T) {
	root := t.TempDir()
	dangling := supersedeFixtureRecords(t, 90000, "Z", 12)
	kept := supersedeFixtureRecords(t, 91000, "K", 5)
	build := func(dir string) *FlatSQLStore {
		s := newLockWindowStore(t, dir)
		supersedeFixtureStore(t, s, dangling, "peer-z", SourceTags{ProviderID: "prov-z", SourceName: "zz", BatchID: "z1"})
		supersedeFixtureStore(t, s, kept, "peer-z", SourceTags{ProviderID: "prov-z", SourceName: "zz", BatchID: "z2"})
		dropProducerRowsForTest(t, s, dangling)
		return s
	}
	oldStore, newStore := build(filepath.Join(root, "old")), build(filepath.Join(root, "new"))
	sources := []string{"zz"}
	before := captureSupersedeState(t, newStore, sources)

	oldResult, err := legacyReconcileSourceBatch(oldStore, "OMM.fbs", "prov-z", "zz", "z2", true)
	if err != nil {
		t.Fatalf("old reconcile: %v", err)
	}
	newResult, newStats, err := newStore.reconcileSourceBatch("OMM.fbs", "prov-z", "zz", "z2", true)
	if err != nil {
		t.Fatalf("new reconcile: %v", err)
	}
	if oldResult.Matched != 0 || oldResult.Deleted != 0 {
		t.Fatalf("old reconcile %+v, want nothing matched: the fixture is not what this test describes", oldResult)
	}
	if newResult.Matched != 0 || newResult.Deleted != 0 || newStats.Chunks != 0 {
		t.Errorf("new reconcile %+v in %d chunks, want nothing matched and no chunk", newResult, newStats.Chunks)
	}
	got, want := captureSupersedeState(t, newStore, sources), captureSupersedeState(t, oldStore, sources)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, before) {
		diffSupersedeState(t, "source tags", got.Tags, want.Tags)
		t.Errorf("the reconcile changed the store (tags %d -> %d, old %d)", len(before.Tags), len(got.Tags), len(want.Tags))
	}
}

// ── only the evicted lanes' summaries are rebuilt ──────────────────────────

// TestReconcileCurrentRebuildsOnlyTheLanesItEvicts stamps every summary row
// updated_at = 0 and reconciles: a rebuilt lane is rewritten with the current
// time, so every row still at 0 was left alone. The old reconcile rebuilt
// every lane of the schema under its one hold (all of host-02's $OMM tags
// joined to their records), and the test must see that.
func TestReconcileCurrentRebuildsOnlyTheLanesItEvicts(t *testing.T) {
	root := t.TempDir()
	sets := newCurrentReconcileFixtureSets(t)
	rewritten := func(s *FlatSQLStore, reconcile func() (SourceBatchReconcileResult, error)) (rows []string, touched []string) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE sdn_record_source_summary SET updated_at = 0`); err != nil {
			t.Fatal(err)
		}
		if got, err := reconcile(); err != nil || got.Deleted != currentFixtureDeleted {
			t.Fatalf("reconcile: %+v, %v", got, err)
		}
		for _, row := range supersedeScanStrings(t, s, `SELECT provider_id, source_name, batch_id, updated_at FROM sdn_record_source_summary`) {
			rows = append(rows, row)
			if !strings.HasSuffix(row, "|0") {
				touched = append(touched, row)
			}
		}
		return rows, touched
	}

	s := buildCurrentReconcileFixture(t, filepath.Join(root, "new"), sets)
	rows, touched := rewritten(s, func() (SourceBatchReconcileResult, error) {
		return s.ReconcileSourceBatch("OMM.fbs", "prov-g", "gp", "g3", true)
	})
	if len(touched) > 0 {
		t.Errorf("the reconcile rewrote the summary of lanes it did not evict: %v", touched)
	}
	lanes := map[string]bool{}
	for _, row := range rows {
		parts := strings.Split(row, "|")
		lanes[strings.Join(parts[:3], "/")] = true
	}
	for _, lane := range []string{"prov-g/gp/g3", "prov-h/mirror/h1", "prov-g/other/x1"} {
		if !lanes[lane] {
			t.Errorf("lane %s lost its summary; lanes left: %v", lane, rows)
		}
	}
	for _, lane := range []string{"prov-g/gp/g1", "prov-g/gp/g2", "prov-g/gp/g9"} {
		if lanes[lane] {
			t.Errorf("evicted lane %s still has a summary row", lane)
		}
	}

	legacy := buildCurrentReconcileFixture(t, filepath.Join(root, "old"), sets)
	_, oldTouched := rewritten(legacy, func() (SourceBatchReconcileResult, error) {
		return legacyReconcileSourceBatch(legacy, "OMM.fbs", "prov-g", "gp", "g3", true)
	})
	if len(oldTouched) == 0 {
		t.Error("the pre-rework reconcile passes, and it rebuilt every lane of the schema: the test cannot see the defect it guards")
	}
}

// ── reads never wait on the reconcile ──────────────────────────────────────

// TestReconcileCurrentReaderWaitsAtMostOneLockWindow runs readers through a
// reconcile that evicts three older batches of 1,000 records and holds every
// read's store-lock wait to the longest single lock hold the reconcile
// reports: the lock is released between count slices, chunks, sweep pages
// and summary lanes, and a reader queued behind one is admitted before the
// next starts.
func TestReconcileCurrentReaderWaitsAtMostOneLockWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	s := newLockWindowStore(t, filepath.Join(t.TempDir(), "store"))
	stale := supersedeFixtureRecords(t, 50000, "S", 3000)
	for start := 0; start < len(stale); start += 1000 {
		supersedeFixtureStore(t, s, stale[start:start+1000], "peer-s", SourceTags{ProviderID: "prov-s", SourceName: "gp", BatchID: fmt.Sprintf("old-%d", start/1000)})
	}
	supersedeFixtureStore(t, s, supersedeFixtureRecords(t, 60000, "K", 20), "peer-s", SourceTags{ProviderID: "prov-s", SourceName: "gp", BatchID: "keep"})

	var result SourceBatchReconcileResult
	var stats currentReconcileStats
	task := startLockWindowWriteTask(t, func() (int, error) {
		var err error
		result, stats, err = s.reconcileSourceBatch("OMM.fbs", "prov-s", "gp", "keep", true)
		return int(result.Deleted), err
	})
	started := time.Now()
	sample := sampleReadsResponsive(s, "OMM.fbs", task.Done())
	took := time.Since(started)
	written := task.Wait()
	if written.err != nil {
		t.Fatalf("ReconcileSourceBatch: %v", written.err)
	}
	if sample.err != nil {
		t.Fatal(sample.err)
	}
	if result.Matched != 3000 || result.Deleted != 3000 {
		t.Fatalf("matched %d, deleted %d; want 3000 each", result.Matched, result.Deleted)
	}
	for _, rec := range stale[:10] {
		if _, err := s.GetRecord("OMM.fbs", ComputeCID(rec)); err == nil {
			t.Fatalf("a record of an evicted batch is still readable")
		}
	}
	if stats.Chunks < 3 {
		t.Fatalf("the reconcile ran %d chunks; it must release the store lock between bounded chunks", stats.Chunks)
	}
	if sample.reads < 3 {
		t.Fatalf("%d reads completed during a %d-chunk reconcile (%s): readers are not getting in between holds", sample.reads, stats.Chunks, took)
	}
	// One lock window, plus scheduling slack short of a second window.
	slack := stats.MaxLockHold / 2
	if slack < 100*time.Millisecond {
		slack = 100 * time.Millisecond
	}
	if limit := stats.MaxLockHold + slack; sample.worstLockWait > limit {
		t.Fatalf("a reader waited %s for the store lock during the reconcile; the longest single hold was %s (%d chunks in %s): a reader waited on more than one lock window",
			sample.worstLockWait, stats.MaxLockHold, stats.Chunks, took)
	}
	t.Logf("%d reads during a %d-chunk reconcile in %s: worst lock wait %s, longest hold %s", sample.reads, stats.Chunks, took, sample.worstLockWait, stats.MaxLockHold)
}

// ── every statement is bounded by its slice or chunk ───────────────────────

// TestReconcileCurrentNeverWalksTheSchema EXPLAINs every statement a dry run
// and an applied reconcile run. None may read the schema's tags, residency
// ledger or index rows beyond a slice or a chunk — the old reconcile's
// anti-join of the whole residency ledger did, and the guard must say so
// about it.
func TestReconcileCurrentNeverWalksTheSchema(t *testing.T) {
	root := t.TempDir()
	sets := newCurrentReconcileFixtureSets(t)
	s := buildCurrentReconcileFixture(t, filepath.Join(root, "new"), sets)
	violations, statements := supersedePlanViolations(t, s, func() {
		if got, err := s.ReconcileSourceBatch("OMM.fbs", "prov-g", "gp", "g3", false); err != nil || got.Matched != currentFixtureMatched {
			t.Fatalf("dry run: %+v, %v", got, err)
		}
		if got, err := s.ReconcileSourceBatch("OMM.fbs", "prov-g", "gp", "g3", true); err != nil || got.Deleted != currentFixtureDeleted {
			t.Fatalf("reconcile: %+v, %v", got, err)
		}
	})
	if statements == 0 {
		t.Fatal("the guard saw no statement")
	}
	if len(violations) > 0 {
		t.Errorf("%d reconcile statement(s) walk the schema:\n  %s", len(violations), strings.Join(violations, "\n  "))
	}

	legacy := buildCurrentReconcileFixture(t, filepath.Join(root, "old"), sets)
	oldViolations, _ := supersedePlanViolations(t, legacy, func() {
		if _, err := legacyReconcileSourceBatch(legacy, "OMM.fbs", "prov-g", "gp", "g3", true); err != nil {
			t.Fatal(err)
		}
	})
	if len(oldViolations) == 0 {
		t.Error("the guard passes the pre-rework reconcile, whose residency anti-join walked the schema's whole ledger: it cannot see the defect it guards")
	}
	sort.Strings(oldViolations)
	t.Logf("the pre-rework reconcile's schema walks:\n  %s", strings.Join(oldViolations, "\n  "))
}
