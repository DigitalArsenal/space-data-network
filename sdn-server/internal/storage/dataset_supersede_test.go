package storage

// The supersede's chunk rework (host-02, 2026-09-29: 57-80 s store-lock
// holds per chunk) must evict EXACTLY what the old chunk evicted, must never
// walk the schema's tags or residency ledger inside a chunk, and must let a
// reader in between every two lock windows.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// ── the pre-rework implementation, kept verbatim as the reference ──────────
//
// legacySupersedeSourceBatches is SupersedeSourceBatches as it shipped up to
// sdn 759d7dd94 (beta.77), minus the cached-file removal (untouched by the
// rework) and with its fixed 2,048-CID chunk as a parameter so a small
// fixture still takes several chunks.
func legacySupersedeSourceBatches(s *FlatSQLStore, schemaName, providerID, sourceName, keepBatch string, chunkSize int) (DatasetSupersedeResult, error) {
	result := DatasetSupersedeResult{SchemaName: schemaName, ProviderID: providerID, SourceName: sourceName, KeepBatch: keepBatch}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, err
	}
	evictedAny := false
	for {
		tags, records, err := legacySupersedeSourceBatchChunk(s, result, tableName, chunkSize)
		if err != nil {
			return result, err
		}
		if tags == 0 && records == 0 {
			break
		}
		evictedAny = true
		result.Chunks++
		result.TagsDeleted += tags
		result.RecordsDeleted += records
	}
	if evictedAny {
		release := s.lockWrite("dataset supersede: source summary")
		summaryErr := s.rebuildSourceSummaryForSchema(result.SchemaName, tableName)
		release()
		if summaryErr != nil {
			return result, summaryErr
		}
	}
	return result, nil
}

func legacySupersedeSourceBatchChunk(s *FlatSQLStore, scope DatasetSupersedeResult, tableName string, chunkSize int) (int64, int64, error) {
	defer s.lockWrite("supersedeSourceBatchChunk")()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin supersede chunk: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_supersede_cids (cid TEXT PRIMARY KEY)`)); err != nil {
		return 0, 0, fmt.Errorf("create supersede cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`DELETE FROM temp_sdn_supersede_cids`)); err != nil {
		return 0, 0, fmt.Errorf("clear supersede cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		INSERT OR IGNORE INTO temp_sdn_supersede_cids (cid)
		SELECT cid FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		LIMIT ?
	`), scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch, chunkSize); err != nil {
		return 0, 0, fmt.Errorf("stage superseded cids: %w", err)
	}
	var staged int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM temp_sdn_supersede_cids`).Scan(&staged); err != nil {
		return 0, 0, fmt.Errorf("count staged superseded cids: %w", err)
	}
	if staged == 0 {
		if err := tx.Commit(); err != nil {
			return 0, 0, fmt.Errorf("commit empty supersede chunk: %w", err)
		}
		committed = true
		return 0, 0, nil
	}

	tagsResult, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		  AND cid IN (SELECT cid FROM temp_sdn_supersede_cids)
	`), scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch)
	if err != nil {
		return 0, 0, fmt.Errorf("delete superseded source tags: %w", err)
	}
	tagsDeleted, _ := tagsResult.RowsAffected()

	var recordsDeleted int64
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM temp_sdn_supersede_cids
		WHERE cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)
	`, scope.SchemaName).Scan(&recordsDeleted); err != nil {
		return 0, 0, fmt.Errorf("count orphaned superseded records: %w", err)
	}
	if recordsDeleted > 0 {
		s.deleteRoutedMirrorsWhere(tx, tableName,
			`cid IN (SELECT cid FROM temp_sdn_supersede_cids) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
			scope.SchemaName)
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM sdn_record_index
			WHERE schema_name = ?
			  AND cid IN (SELECT cid FROM temp_sdn_supersede_cids)
			  AND NOT EXISTS (
				SELECT 1 FROM sdn_record_source_tags tags
				WHERE tags.schema_name = sdn_record_index.schema_name
				  AND tags.cid = sdn_record_index.cid
			  )
		`), scope.SchemaName); err != nil {
			return 0, 0, fmt.Errorf("delete orphaned superseded index rows: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit supersede chunk: %w", err)
	}
	committed = true
	if _, err := s.tombstoneOrphanedEngineRowsLocked(scope.SchemaName); err != nil {
		return tagsDeleted, recordsDeleted, err
	}
	return tagsDeleted, recordsDeleted, nil
}

// ── the fixture ────────────────────────────────────────────────────────────

func supersedeFixtureRecords(t *testing.T, base uint32, name string, n int) [][]byte {
	t.Helper()
	out := make([][]byte, n)
	for i := range out {
		out[i] = buildEngineOMM(t, base+uint32(i), name, 1_700_000_000+int64(base)+int64(i))
	}
	return out
}

func supersedeFixtureStore(t *testing.T, s *FlatSQLStore, records [][]byte, producer string, tags SourceTags) {
	t.Helper()
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", records, producer, nil, tags); err != nil {
		t.Fatalf("store %d records as %s %s/%s/%s: %v", len(records), producer, tags.ProviderID, tags.SourceName, tags.BatchID, err)
	}
}

func concatRecords(sets ...[][]byte) [][]byte {
	var out [][]byte
	for _, set := range sets {
		out = append(out, set...)
	}
	return out
}

// buildSupersedeEquivalenceFixture lays out the cases the supersede has to
// tell apart, in three producer tables:
//
//	lane prov-g/gp (superseded, keep g3):
//	  g1 A[0,300)            as peer-g1
//	  g2 A[150,300) B[0,300)  as peer-g2  (A[150,300) repeat: re-tagged, mirrored)
//	  g3 B[150,300) C[0,100)  as peer-g1  (B[150,300) shared with the kept batch)
//	lane prov-h/mirror h1: A[0,60) D[0,50)       as peer-h (A[0,60) survive by it)
//	lane prov-g/other  x1: A[60,80)              as peer-g2 (same provider, other source)
//
// and one residency row whose index row is already gone (D[0]): the old chunk
// swept the whole ledger after every commit, so it tombstoned rows orphaned by
// anything, and the new supersede must too.
type supersedeFixtureSets struct{ a, b, c, d [][]byte }

// newSupersedeFixtureSets builds the fixture's records once: the OMM builder
// stamps a creation date, so two builds of "the same" record are two CIDs.
func newSupersedeFixtureSets(t *testing.T) supersedeFixtureSets {
	t.Helper()
	return supersedeFixtureSets{
		a: supersedeFixtureRecords(t, 10000, "A", 300),
		b: supersedeFixtureRecords(t, 20000, "B", 300),
		c: supersedeFixtureRecords(t, 30000, "C", 100),
		d: supersedeFixtureRecords(t, 40000, "D", 50),
	}
}

func buildSupersedeEquivalenceFixture(t *testing.T, dir string, sets supersedeFixtureSets) *FlatSQLStore {
	t.Helper()
	s := newLockWindowStore(t, dir)
	a, b, c, d := sets.a, sets.b, sets.c, sets.d
	lane := func(provider, source, batch string) SourceTags {
		return SourceTags{ProviderID: provider, SourceName: source, BatchID: batch}
	}
	supersedeFixtureStore(t, s, a, "peer-g1", lane("prov-g", "gp", "g1"))
	supersedeFixtureStore(t, s, concatRecords(a[150:], b), "peer-g2", lane("prov-g", "gp", "g2"))
	supersedeFixtureStore(t, s, concatRecords(b[150:], c), "peer-g1", lane("prov-g", "gp", "g3"))
	supersedeFixtureStore(t, s, concatRecords(a[:60], d), "peer-h", lane("prov-h", "mirror", "h1"))
	supersedeFixtureStore(t, s, a[60:80], "peer-g2", lane("prov-g", "other", "x1"))

	orphan := ComputeCID(d[0])
	if _, err := s.db.Exec(`DELETE FROM sdn_record_index WHERE schema_name = 'OMM.fbs' AND cid = ?`, orphan); err != nil {
		t.Fatalf("orphan a residency row: %v", err)
	}
	var resident int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' AND cid = ?`, orphan).Scan(&resident); err != nil || resident != 1 {
		t.Fatalf("fixture: D[0] residency rows = %d (%v), want 1", resident, err)
	}
	return s
}

// supersedeStoreState is everything a supersede can change, without the
// wall-clock columns two fixture builds cannot share.
type supersedeStoreState struct {
	Tags     []string
	Records  map[string][]string
	Index    []string
	Ledger   []string
	Engine   map[string][]string
	Summary  []string
	Counters []string
}

func supersedeScanStrings(t *testing.T, s *FlatSQLStore, query string, args ...any) []string {
	t.Helper()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s: %v", query, err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = v.String
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	sort.Strings(out)
	return out
}

func captureSupersedeState(t *testing.T, s *FlatSQLStore, engineSources []string) supersedeStoreState {
	t.Helper()
	state := supersedeStoreState{Records: map[string][]string{}, Engine: map[string][]string{}}
	state.Tags = supersedeScanStrings(t, s, `SELECT schema_name, cid, provider_id, source_name, COALESCE(source_url, ''), batch_id,
		content_key_id, producer_peer_id, producer_public_key FROM sdn_record_source_tags`)
	tables, err := s.listProducerStandardTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range tables {
		rows := supersedeScanStrings(t, s, fmt.Sprintf(`SELECT rowid, cid, peer_id, record_length, COALESCE(supersede_key, ''), hex(data) FROM %s`, pt.TableName))
		for i, row := range rows {
			// The payload is compared by digest to keep a mismatch readable.
			cut := strings.LastIndex(row, "|")
			sum := sha256.Sum256([]byte(row[cut+1:]))
			rows[i] = row[:cut+1] + hex.EncodeToString(sum[:8])
		}
		state.Records[pt.TableName] = rows
	}
	state.Index = supersedeScanStrings(t, s, `SELECT schema_name, cid, rowid, COALESCE(norad_cat_id, ''), COALESCE(entity_id, ''),
		COALESCE(epoch_unix, ''), COALESCE(epoch_day, '') FROM sdn_record_index`)
	state.Ledger = supersedeScanStrings(t, s, `SELECT schema_name, cid, source, seq FROM sdn_engine_rows`)
	state.Summary = supersedeScanStrings(t, s, `SELECT schema_name, provider_id, source_name, batch_id, producer_peer_id,
		producer_public_key, record_count, total_bytes, max_rowid FROM sdn_record_source_summary`)
	state.Counters = supersedeScanStrings(t, s, `SELECT table_name, record_count, record_bytes FROM sdn_partition_record_bytes`)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, source := range engineSources {
		res, err := s.engineDB.Query(`SELECT _rowid, NORAD_CAT_ID FROM "OMM@` + source + `" ORDER BY _rowid`)
		if err != nil {
			state.Engine[source] = []string{"error: " + err.Error()}
			continue
		}
		for _, row := range res.Rows {
			state.Engine[source] = append(state.Engine[source], fmt.Sprint(row...))
		}
	}
	return state
}

func diffSupersedeState(t *testing.T, what string, got, want []string) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
	}
	var extra, missing []string
	for _, g := range got {
		if !wantSet[g] {
			extra = append(extra, g)
		}
	}
	for _, w := range want {
		if !gotSet[w] {
			missing = append(missing, w)
		}
	}
	clip := func(v []string) []string {
		if len(v) > 5 {
			return append(v[:5:5], fmt.Sprintf("… %d more", len(v)-5))
		}
		return v
	}
	t.Errorf("%s differs from the old supersede: %d rows vs %d; only new: %v; only old: %v", what, len(got), len(want), clip(extra), clip(missing))
}

// TestSupersedeEvictsExactlyWhatTheOldChunkEvicted runs the old and the new
// supersede on two byte-identical stores and compares every table either can
// touch, plus the engine's visible rows.
func TestSupersedeEvictsExactlyWhatTheOldChunkEvicted(t *testing.T) {
	root := t.TempDir()
	sets := newSupersedeFixtureSets(t)
	oldStore := buildSupersedeEquivalenceFixture(t, filepath.Join(root, "old"), sets)
	newStore := buildSupersedeEquivalenceFixture(t, filepath.Join(root, "new"), sets)
	sources := []string{"gp", "mirror", "other"}

	before := captureSupersedeState(t, newStore, sources)
	beforeOld := captureSupersedeState(t, oldStore, sources)
	for name, pair := range map[string][2][]string{"tags": {before.Tags, beforeOld.Tags}, "index": {before.Index, beforeOld.Index}, "ledger": {before.Ledger, beforeOld.Ledger}} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			diffSupersedeState(t, name, pair[0], pair[1])
			t.Fatalf("the two fixtures differ in %s before any supersede; the comparison would mean nothing", name)
		}
	}

	oldResult, err := legacySupersedeSourceBatches(oldStore, "OMM.fbs", "prov-g", "gp", "g3", 128)
	if err != nil {
		t.Fatalf("old supersede: %v", err)
	}
	newResult, err := newStore.SupersedeSourceBatches("OMM.fbs", "prov-g", "gp", "g3")
	if err != nil {
		t.Fatalf("new supersede: %v", err)
	}
	// g1 300 + g2 450 tags; orphans A[80,300) and B[0,150).
	if oldResult.TagsDeleted != 750 || oldResult.RecordsDeleted != 370 || oldResult.Chunks < 3 {
		t.Fatalf("old supersede evicted %d tags / %d records in %d chunks, want 750 / 370 in several: the fixture is not what this test describes",
			oldResult.TagsDeleted, oldResult.RecordsDeleted, oldResult.Chunks)
	}
	if newResult.TagsDeleted != oldResult.TagsDeleted || newResult.RecordsDeleted != oldResult.RecordsDeleted {
		t.Errorf("new supersede evicted %d tags / %d records, old %d / %d",
			newResult.TagsDeleted, newResult.RecordsDeleted, oldResult.TagsDeleted, oldResult.RecordsDeleted)
	}
	if newResult.Chunks < 2 {
		t.Errorf("new supersede ran %d chunks; the fixture must cross a chunk boundary", newResult.Chunks)
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

	// The supersede did evict: the comparison above is not two no-ops.
	if len(got.Tags) != len(before.Tags)-750 || len(got.Ledger) >= len(before.Ledger) {
		t.Errorf("tags %d -> %d, ledger %d -> %d: the supersede did not evict what the fixture sets up",
			len(before.Tags), len(got.Tags), len(before.Ledger), len(got.Ledger))
	}
	if orphan := ComputeCID(sets.d[0]); containsPrefix(got.Ledger, "OMM.fbs|"+orphan+"|") {
		t.Errorf("the residency row orphaned before the supersede is still in the ledger")
	}

	again, err := newStore.SupersedeSourceBatches("OMM.fbs", "prov-g", "gp", "g3")
	if err != nil {
		t.Fatalf("second supersede: %v", err)
	}
	if again.TagsDeleted != 0 || again.RecordsDeleted != 0 || again.Chunks != 0 {
		t.Errorf("second supersede evicted %+v, want nothing", again)
	}
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsPrefix(rows []string, prefix string) bool {
	for _, r := range rows {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

// ── reads never wait on the supersede ──────────────────────────────────────

// TestSupersedeReaderWaitsAtMostOneLockWindow runs readers through a
// supersede of 3,000 records and holds every read's store-lock wait to the
// longest single lock hold the supersede reports: the lock is released
// between chunks, and a reader queued behind one chunk is admitted before the
// next one starts.
func TestSupersedeReaderWaitsAtMostOneLockWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	s := newLockWindowStore(t, filepath.Join(t.TempDir(), "store"))
	stale := supersedeFixtureRecords(t, 50000, "S", 3000)
	for start := 0; start < len(stale); start += 1000 {
		supersedeFixtureStore(t, s, stale[start:start+1000], "peer-s", SourceTags{ProviderID: "prov-s", SourceName: "gp", BatchID: fmt.Sprintf("old-%d", start/1000)})
	}
	supersedeFixtureStore(t, s, supersedeFixtureRecords(t, 60000, "K", 20), "peer-s", SourceTags{ProviderID: "prov-s", SourceName: "gp", BatchID: "keep"})

	var result DatasetSupersedeResult
	task := startLockWindowWriteTask(t, func() (int, error) {
		var err error
		result, err = s.SupersedeSourceBatches("OMM.fbs", "prov-s", "gp", "keep")
		return int(result.RecordsDeleted), err
	})
	started := time.Now()
	sample := sampleReadsResponsive(s, "OMM.fbs", task.Done())
	took := time.Since(started)
	written := task.Wait()
	if written.err != nil {
		t.Fatalf("SupersedeSourceBatches: %v", written.err)
	}
	if sample.err != nil {
		t.Fatal(sample.err)
	}
	if result.RecordsDeleted != 3000 {
		t.Fatalf("evicted %d records, want 3000", result.RecordsDeleted)
	}
	if result.Chunks < 3 {
		t.Fatalf("the supersede ran %d chunks; it must release the store lock between bounded chunks", result.Chunks)
	}
	if sample.reads < 3 {
		t.Fatalf("%d reads completed during a %d-chunk supersede (%s): readers are not getting in between chunks", sample.reads, result.Chunks, took)
	}
	// One lock window, plus scheduling slack short of a second window.
	slack := result.MaxLockHold / 2
	if slack < 100*time.Millisecond {
		slack = 100 * time.Millisecond
	}
	if limit := result.MaxLockHold + slack; sample.worstLockWait > limit {
		t.Fatalf("a reader waited %s for the store lock during the supersede; the longest single hold was %s (%d chunks in %s): a reader waited on more than one lock window",
			sample.worstLockWait, result.MaxLockHold, result.Chunks, took)
	}
	t.Logf("%d reads during a %d-chunk supersede in %s: worst lock wait %s, longest hold %s", sample.reads, result.Chunks, took, sample.worstLockWait, result.MaxLockHold)
}

// ── every chunk statement is bounded by the chunk ──────────────────────────

// wholeSchemaWalk matches a plan step whose only constraint on the tag table,
// the residency ledger or the record index is the schema: it reads every row
// the schema has (host-02: ~1.9 M $OMM tags, 400,000 residency rows).
var wholeSchemaWalk = regexp.MustCompile(`^(SCAN|SEARCH) (sdn_record_source_tags|tags|t|sdn_engine_rows|e|sdn_record_index|idx)\b.*?(\(schema_name=\?\))?$`)

func supersedePlanViolations(t *testing.T, s *FlatSQLStore, call func()) (violations []string, statements int) {
	t.Helper()
	s.mu.RLock()
	engineDB := s.engineDB
	s.mu.RUnlock()
	var mu sync.Mutex
	restore := flatsqldrv.SetStatementHook(func(query string, params []interface{}) {
		head := strings.ToUpper(strings.TrimSpace(query))
		if !strings.HasPrefix(head, "SELECT") && !strings.HasPrefix(head, "INSERT") && !strings.HasPrefix(head, "DELETE") && !strings.HasPrefix(head, "UPDATE") {
			return
		}
		res, err := engineDB.Query("EXPLAIN QUERY PLAN "+query, params...)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		statements++
		var plan []string
		for _, row := range res.Rows {
			if len(row) > 0 {
				if detail, ok := row[len(row)-1].(string); ok {
					plan = append(plan, detail)
				}
			}
		}
		for _, line := range plan {
			m := wholeSchemaWalk.FindStringSubmatch(line)
			if m == nil || (m[1] == "SEARCH" && m[3] == "") {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s\n    SQL: %s", line, strings.Join(strings.Fields(query), " ")))
		}
		if line, bad := planWalksPartitions(query, plan); bad {
			violations = append(violations, fmt.Sprintf("%s\n    SQL: %s", line, strings.Join(strings.Fields(query), " ")))
		}
	})
	defer restore()
	call()
	return violations, statements
}

// TestSupersedeChunkNeverWalksTheSchema EXPLAINs every statement a supersede
// runs. None may read the schema's tags, residency ledger or index rows
// beyond the chunk — the old orphan test (NOT IN over every tag of the
// schema) and the old whole-ledger residency sweep both did, and the guard
// must say so about them.
func TestSupersedeChunkNeverWalksTheSchema(t *testing.T) {
	root := t.TempDir()
	sets := newSupersedeFixtureSets(t)
	s := buildSupersedeEquivalenceFixture(t, filepath.Join(root, "new"), sets)
	violations, statements := supersedePlanViolations(t, s, func() {
		if _, err := s.SupersedeSourceBatches("OMM.fbs", "prov-g", "gp", "g3"); err != nil {
			t.Fatal(err)
		}
	})
	if statements == 0 {
		t.Fatal("the guard saw no statement")
	}
	if len(violations) > 0 {
		t.Errorf("%d supersede statement(s) walk the schema:\n  %s", len(violations), strings.Join(violations, "\n  "))
	}

	legacy := buildSupersedeEquivalenceFixture(t, filepath.Join(root, "old"), sets)
	oldViolations, _ := supersedePlanViolations(t, legacy, func() {
		if _, err := legacySupersedeSourceBatches(legacy, "OMM.fbs", "prov-g", "gp", "g3", 2048); err != nil {
			t.Fatal(err)
		}
	})
	if len(oldViolations) == 0 {
		t.Error("the guard passes the pre-rework supersede, whose NOT IN walked every tag of the schema: it cannot see the defect it guards")
	}
}

// ── the chunk size follows the hold ────────────────────────────────────────

func TestNextSupersedeChunkFollowsTheLockHold(t *testing.T) {
	if got := nextSupersedeChunk(0, 0); got != supersedeChunkStart {
		t.Fatalf("first chunk = %d, want %d", got, supersedeChunkStart)
	}
	if supersedeChunkStart > storeWriteChunkSize {
		t.Fatalf("the first chunk (%d CIDs) is larger than the import window (%d records) host-02 already holds for ~0.5 s", supersedeChunkStart, storeWriteChunkSize)
	}
	// host-02's old chunk: 2,048 CIDs held 57 s. One step must bring the next
	// chunk inside the target, not five halvings later.
	if got := nextSupersedeChunk(2048, 57*time.Second); got != supersedeChunkMin {
		t.Fatalf("after a 57 s chunk of 2048 the next is %d, want the floor %d", got, supersedeChunkMin)
	}
	// A first chunk that ran twice the target halves.
	if got := nextSupersedeChunk(supersedeChunkStart, 2*supersedeChunkTarget); got != supersedeChunkStart/2 {
		t.Fatalf("a first chunk at twice the target moved to %d, want %d", got, supersedeChunkStart/2)
	}
	if got := nextSupersedeChunk(1024, 4*supersedeChunkTarget); got != 256 {
		t.Fatalf("a chunk at 4x the target shrinks to %d, want 256 (in proportion)", got)
	}
	// Cheap chunks grow to the old fixed size and no further.
	chunk := supersedeChunkStart
	for i := 0; i < 10; i++ {
		chunk = nextSupersedeChunk(chunk, supersedeChunkTarget/8)
	}
	if chunk != supersedeChunkMax {
		t.Fatalf("after cheap chunks the size is %d, want the %d cap", chunk, supersedeChunkMax)
	}
	// A chunk whose hold is mostly the fixed COMMIT (~100 ms on the owner's
	// machine) still grows.
	if got := nextSupersedeChunk(64, 110*time.Millisecond); got != 128 {
		t.Fatalf("a 110 ms chunk of 64 moved to %d, want 128", got)
	}
	// Between half the target and the target, the size holds.
	if got := nextSupersedeChunk(512, supersedeChunkTarget*3/4); got != 512 {
		t.Fatalf("a chunk at 3/4 of the target moved to %d, want it kept at 512", got)
	}
	if got := nextSupersedeChunk(512, 0); got != 512 {
		t.Fatalf("a chunk with no measurement moved to %d", got)
	}
}
