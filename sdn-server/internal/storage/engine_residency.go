package storage

// engine_residency.go — the durable ledger of what the engine hot window holds.
//
// The FlatSQL record vtabs are a BOUNDED CACHE of the routed standards: every
// routed record written to the control tables is mirrored into its
// "<Table>@<source>" partition, and the oldest rows beyond the per-standard
// window are tombstoned (engine_records.go). The engine persists its record
// arena and index across restarts (flatsql_flush_index / flatsql_open_state)
// but NOT its tombstones, and it has no notion of a CID — its rows are keyed
// by a per-partition ingest sequence.
//
// sdn_engine_rows closes both gaps. One row per resident record —
// (schema_name, cid) -> (source, seq) — written in the SAME control
// transaction as the engine ingest, deleted on eviction, delete and
// supersede. It makes three things exact that used to be approximate:
//
//   - a DELETE or a CAT supersede reaches the engine row immediately
//     (tombstoneEngineRecordsLocked), so the public query surface never
//     answers with a record the store no longer holds;
//   - a warm boot re-applies exactly the tombstones the engine forgot and
//     re-ingests exactly the records whose arena bytes were never flushed
//     (reconcileEngineResidencyLocked), instead of guessing from counts;
//   - the residency count is read from a table, not drifted by deletes.
//
// The window itself remains a workaround for the engine's 4 GiB linear memory
// (flatsql-page-fsdata-not-slurp, Part B); it is not deleted here.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	engineRowsTableSQL = `
		CREATE TABLE IF NOT EXISTS sdn_engine_rows (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			source TEXT NOT NULL,
			seq INTEGER NOT NULL,
			PRIMARY KEY (schema_name, cid)
		)`
	engineRowsSeqIndexSQL = `
		CREATE INDEX IF NOT EXISTS idx_sdn_engine_rows_seq
		ON sdn_engine_rows (schema_name, source, seq)`

	// engineTombstoneChunk bounds one IN (...) list against sdn_engine_rows.
	engineTombstoneChunk = 500
)

// engineIngest is one committed record queued for the engine mirror.
type engineIngest struct {
	cid    string
	data   []byte
	source string
}

// engineLedgerSchema is the ledger's schema key: the canonical ".fbs" name.
// sdn_record_index stores the writer's spelling verbatim (a record stored as
// "OMM" and one stored as "OMM.fbs" are the same standard), so every ledger
// read and write normalises first; the index side of a join uses
// engineSchemaNameAliases.
func engineLedgerSchema(schemaName string) string {
	return normalizeSchemaNameForEpoch(schemaName)
}

// enginePartition is the full shadow-table name MarkDeleted keys on.
func enginePartition(table, source string) string { return table + "@" + source }

// engineSourceOfPartition recovers the bare source name from a partition name.
func engineSourceOfPartition(table, partition string) string {
	return strings.TrimPrefix(partition, table+"@")
}

// ingestEngineBatchLocked mirrors one source's records into the engine and
// records their residency, INSIDE ONE control transaction. Per-record ingest
// (flatsql_ingest_one_with_source) is what returns the engine-assigned
// sequence each residency row needs; the transaction is what keeps a standard
// with nested vectors ($TBS provenance junction rows) from paying a journal
// fsync per record. A rollback leaves the engine arena holding rows nothing
// tracks; the next checkpoint's flush persists them and the next boot's
// reconcile tombstones them. Caller holds s.mu for writing.
// ingestEngineBatchLocked mirrors a batch into the engine arena and records
// its residency. When join is non-nil the ledger rows are written INTO that
// transaction and this opens none of its own — see storeBatchChunk, which folds
// the ledger into the same commit as the control rows so a window costs ONE
// fsync instead of two.
func (s *FlatSQLStore) ingestEngineBatchLocked(schemaName, source string, payloads [][]byte, cids []string, join sqlQueryExecer) (int, []engineResidencyRow, error) {
	if len(payloads) == 0 {
		return 0, nil, nil
	}
	schemaName = engineLedgerSchema(schemaName)
	// exec is where the ledger statements go: the caller's transaction when it
	// gave us one, otherwise the database with a transaction of our own.
	var exec sqlQueryExecer = join
	inTxn := false
	if join == nil {
		exec = s.db
		if s.db != nil {
			if _, err := s.db.Exec("BEGIN"); err == nil {
				inTxn = true
			}
		}
	}
	ingested := 0
	// Sequences handed back by the arena this call. On a failure the caller's
	// transaction (or ours) takes the ledger rows away, and these arena rows
	// would be left behind with nothing pointing at them — rows the engine
	// would keep SERVING until the next warm open reconciled them away. So they
	// are tombstoned on the spot; the boot reconcile stays the backstop for a
	// hard crash, which is the case it was written for.
	var ingestedSeqs []uint64
	fail := func(err error) (int, []engineResidencyRow, error) {
		for _, seq := range ingestedSeqs {
			if mErr := s.engineDB.MarkDeleted(enginePartition(s.engineTableForLedger(schemaName), source), seq); mErr != nil {
				log.Warnf("FlatSQL engine: tombstone %s seq %d after a failed ingest: %v", source, seq, mErr)
				break
			}
		}
		if inTxn {
			_, _ = s.db.Exec("ROLLBACK")
		}
		return 0, nil, err
	}

	// ONE RESIDENT ROW PER RECORD. The ledger is keyed by (schema, cid); a
	// record that is already resident keeps its row and the row just appended
	// is tombstoned, so no caller can put a record in the window twice
	// (INSERT OR REPLACE used to move the ledger to the new row and leave the
	// old one visible: duplicate query results, found on the dev node
	// 2026-09-14 — 48 PRR rows written live while the cold rebuild was
	// ingesting the same records from the tables).
	//
	// WHICH rows are already resident is asked ONCE for the whole batch. The
	// per-record form was an Exec per record, and database/sql's Exec is TWO
	// engine statements — the insert, then the `last_insert_rowid()/changes()`
	// the driver adds to build its Result — each one a handoff to the engine's
	// locked exec thread. On a 64-record window that was 128 of the 147
	// statements the whole store path issued.
	resident, err := s.residentCIDSet(schemaName, cids, exec)
	if err != nil {
		return fail(fmt.Errorf("read engine residency for %s: %w", schemaName, err))
	}

	// EVERY PAYLOAD IN ONE DISPATCH. Each IngestOneWithSource is a handoff to
	// the engine's locked OS thread, so a 64-record window paid 64 of them here
	// — a fifth of the whole store write path — to run the same export the same
	// number of times. The arena sees an identical sequence of ingests; only the
	// thread-boundary crossings collapse, N to one. Partial failure leaves the
	// records before the failing one in the arena, exactly as the loop did, and
	// the transaction below rolls back the ledger so the boot reconcile removes
	// them.
	seqs, err := s.engineDB.IngestManyWithSource(payloads, source)
	if err != nil {
		return fail(err)
	}
	if len(seqs) != len(payloads) {
		return fail(fmt.Errorf("engine ingest batch for %s: %d sequences for %d payloads", schemaName, len(seqs), len(payloads)))
	}

	fresh := make([]engineResidencyRow, 0, len(payloads))
	var duplicateSeqs []uint64
	for i := range payloads {
		seq := seqs[i]
		cid := cids[i]
		if _, already := resident[cid]; already {
			duplicateSeqs = append(duplicateSeqs, uint64(seq))
			continue
		}
		// The same CID twice inside ONE batch is the second duplicate shape,
		// and the per-record INSERT OR IGNORE caught it because the first
		// insert had already landed. Held here in the set instead.
		resident[cid] = struct{}{}
		fresh = append(fresh, engineResidencyRow{cid: cid, source: source, seq: int64(seq)})
	}

	// Only the FRESH rows: a duplicate's arena row is tombstoned unconditionally
	// below, so the failure path must not tombstone it a second time.
	for _, row := range fresh {
		ingestedSeqs = append(ingestedSeqs, uint64(row.seq))
	}

	if len(fresh) > 0 {
		ingested = len(fresh)
		inserted, err := insertEngineResidencyBatch(exec, schemaName, fresh)
		if err != nil {
			return fail(fmt.Errorf("record engine residency for %s: %w", schemaName, err))
		}
		if inserted != int64(len(fresh)) {
			// OR IGNORE swallowed a row the probe said was absent. That must
			// never go unnoticed: the record's arena row would stay visible
			// with no ledger row pointing at it — the duplicate-results bug
			// above. Find the rows whose ledger seq is NOT the one just
			// ingested and tombstone those.
			log.Warnf("FlatSQL engine residency: %s — %d of %d rows were already present; reconciling", schemaName, int64(len(fresh))-inserted, len(fresh))
			ignored, err := s.residencySeqsNotStored(schemaName, fresh, exec)
			if err != nil {
				return fail(err)
			}
			duplicateSeqs = append(duplicateSeqs, ignored...)
			ingested -= len(ignored)
		}
	}

	duplicates := len(duplicateSeqs)
	for _, seq := range duplicateSeqs {
		if err := s.engineDB.MarkDeleted(enginePartition(s.engineTableForLedger(schemaName), source), seq); err != nil {
			return fail(fmt.Errorf("tombstone duplicate engine row in %s: %w", schemaName, err))
		}
	}
	if inTxn {
		if _, err := s.db.Exec("COMMIT"); err != nil {
			_, _ = s.db.Exec("ROLLBACK")
			return 0, nil, fmt.Errorf("commit engine ingest batch: %w", err)
		}
	}
	if duplicates > 0 {
		log.Infof("FlatSQL engine records: %s — %d record(s) were already resident; the duplicate rows were tombstoned", schemaName, duplicates)
	}
	s.engineUnflushed.Add(int64(ingested + duplicates))
	return ingested, fresh, nil
}

// engineTableForLedger is the engine base table of a ledger schema name.
func (s *FlatSQLStore) engineTableForLedger(schemaName string) string {
	if binding, ok := engineRoutedSchemaFor(schemaName); ok {
		return binding.Table
	}
	return strings.TrimSuffix(schemaName, ".fbs")
}

// engineResidencyRow is one sdn_engine_rows row.
type engineResidencyRow struct {
	cid    string
	source string
	seq    int64
}

// residentCIDSet is engineResidencyRowsForCIDs reduced to the question the
// ingest path asks: which of these CIDs already hold a place in the window.
func (s *FlatSQLStore) residentCIDSet(schemaName string, cids []string, exec sqlQueryExecer) (map[string]struct{}, error) {
	rows, err := s.engineResidencyRowsForCIDs(schemaName, cids, exec)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(cids))
	for _, row := range rows {
		out[row.cid] = struct{}{}
	}
	return out, nil
}

// residencySeqsNotStored returns the ingest sequences of `fresh` rows whose
// ledger row is NOT the one just written — the rows an OR IGNORE swallowed.
// Their arena rows must be tombstoned or the window serves the record twice.
func (s *FlatSQLStore) residencySeqsNotStored(schemaName string, fresh []engineResidencyRow, exec sqlQueryExecer) ([]uint64, error) {
	cids := make([]string, len(fresh))
	for i, row := range fresh {
		cids[i] = row.cid
	}
	stored, err := s.engineResidencyRowsForCIDs(schemaName, cids, exec)
	if err != nil {
		return nil, fmt.Errorf("reconcile engine residency for %s: %w", schemaName, err)
	}
	bySeq := make(map[string]int64, len(stored))
	for _, row := range stored {
		bySeq[row.cid] = row.seq
	}
	var ignored []uint64
	for _, row := range fresh {
		if seq, ok := bySeq[row.cid]; !ok || seq != row.seq {
			ignored = append(ignored, uint64(row.seq))
		}
	}
	return ignored, nil
}

// insertEngineResidencyBatch writes the window's ledger rows in multi-row
// statements and returns how many rows were actually inserted. OR IGNORE is
// kept from the per-record form: it is the backstop that makes a row already
// present a detectable no-op rather than a failed write.
func insertEngineResidencyBatch(exec sqlExecer, schemaName string, rows []engineResidencyRow) (int64, error) {
	var inserted int64
	for start := 0; start < len(rows); start += batchStatementRows {
		end := start + batchStatementRows
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		args := make([]any, 0, len(batch)*4)
		for _, row := range batch {
			args = append(args, schemaName, row.cid, row.source, row.seq)
		}
		res, err := exec.Exec(
			`INSERT OR IGNORE INTO sdn_engine_rows (schema_name, cid, source, seq) VALUES `+valueTupleList(len(batch), 4),
			args...)
		if err != nil {
			return inserted, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return inserted, err
		}
		inserted += affected
	}
	return inserted, nil
}

// engineResidencyRowsForCIDs reads the residency rows of the given CIDs.
func (s *FlatSQLStore) engineResidencyRowsForCIDs(schemaName string, cids []string, exec sqlQueryExecer) ([]engineResidencyRow, error) {
	// Read through the caller's transaction when it has one: the ledger rows it
	// is about to write are only visible inside it.
	if exec == nil {
		exec = s.db
	}
	schemaName = engineLedgerSchema(schemaName)
	var out []engineResidencyRow
	for start := 0; start < len(cids); start += engineTombstoneChunk {
		end := start + engineTombstoneChunk
		if end > len(cids) {
			end = len(cids)
		}
		chunk := cids[start:end]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, schemaName)
		for _, cid := range chunk {
			args = append(args, cid)
		}
		rows, err := exec.Query(fmt.Sprintf(
			`SELECT cid, source, seq FROM sdn_engine_rows WHERE schema_name = ? AND cid IN (%s)`,
			strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row engineResidencyRow
			if err := rows.Scan(&row.cid, &row.source, &row.seq); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// tombstoneEngineRecordsLocked removes the given records from the engine hot
// window: MarkDeleted on each resident row and the residency row deleted.
// Records that were never resident (never routed, evicted, skipped) cost one
// lookup and nothing else. Best-effort per row; only a poisoned runtime is
// returned as an error. Caller holds s.mu for writing.
// tombstoneEngineRecordsLocked removes records from the engine's hot window.
// join, when non-nil, is a transaction the ledger deletes are written into —
// without one each DELETE is its own autocommit, and under WAL + FULL that is
// an fsync per superseded record.
func (s *FlatSQLStore) tombstoneEngineRecordsLocked(schemaName string, cids []string, join sqlQueryExecer) (int, error) {
	if len(cids) == 0 || s.engineDB == nil {
		return 0, nil
	}
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return 0, nil
	}
	rows, err := s.engineResidencyRowsForCIDs(schemaName, cids, join)
	if err != nil {
		return 0, fmt.Errorf("read engine residency for %s: %w", schemaName, err)
	}
	return s.tombstoneResidencyRowsLocked(schemaName, binding.Table, rows, join)
}

// tombstoneResidencyRowsLocked applies MarkDeleted for each row and deletes it
// from the ledger. Caller holds s.mu for writing.
func (s *FlatSQLStore) tombstoneResidencyRowsLocked(schemaName, table string, rows []engineResidencyRow, join sqlQueryExecer) (int, error) {
	var exec sqlQueryExecer = s.db
	if join != nil {
		exec = join
	}
	ledgerSchema := engineLedgerSchema(schemaName)
	removed := 0
	for _, row := range rows {
		if err := s.engineDB.MarkDeleted(enginePartition(table, row.source), uint64(row.seq)); err != nil {
			log.Warnf("FlatSQL engine: tombstone %s %s (%s seq %d): %v", schemaName, row.cid, row.source, row.seq, err)
			if s.engine.Poisoned() {
				return removed, fmt.Errorf("FlatSQL engine poisoned tombstoning %s: %w", schemaName, err)
			}
			continue
		}
		if _, err := exec.Exec(`DELETE FROM sdn_engine_rows WHERE schema_name = ? AND cid = ?`, ledgerSchema, row.cid); err != nil {
			log.Warnf("FlatSQL engine: drop residency row %s/%s: %v", schemaName, row.cid, err)
		}
		removed++
	}
	s.engineResidentAdd(schemaName, -int64(removed))
	return removed, nil
}

// tombstoneOrphanedEngineRowsLocked is the bulk form for the delete paths that
// remove records by predicate (age GC, source-batch reconcile, dataset
// supersede): every residency row whose record no longer has an index row is
// tombstoned. The anti-join runs over the window, not the catalog. Caller
// holds s.mu for writing.
func (s *FlatSQLStore) tombstoneOrphanedEngineRowsLocked(schemaName string) (int, error) {
	if s.engineDB == nil {
		return 0, nil
	}
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return 0, nil
	}
	aliases := engineSchemaNameAliases(schemaName)
	args := make([]any, 0, len(aliases)+1)
	args = append(args, engineLedgerSchema(schemaName))
	for _, alias := range aliases {
		args = append(args, alias)
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT e.cid, e.source, e.seq
		FROM sdn_engine_rows e
		WHERE e.schema_name = ?
		  AND NOT EXISTS (
			SELECT 1 FROM sdn_record_index idx
			WHERE idx.schema_name IN (%s) AND idx.cid = e.cid
		  )`, strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")), args...)
	if err != nil {
		return 0, fmt.Errorf("find orphaned engine rows for %s: %w", schemaName, err)
	}
	var orphans []engineResidencyRow
	for rows.Next() {
		var row engineResidencyRow
		if err := rows.Scan(&row.cid, &row.source, &row.seq); err != nil {
			rows.Close()
			return 0, err
		}
		orphans = append(orphans, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return s.tombstoneResidencyRowsLocked(schemaName, binding.Table, orphans, nil)
}

// tombstoneOrphanedEngineRowsAllLocked runs the anti-join for every routed
// schema that has residency rows. Caller holds s.mu for writing.
func (s *FlatSQLStore) tombstoneOrphanedEngineRowsAllLocked() error {
	rows, err := s.db.Query(`SELECT DISTINCT schema_name FROM sdn_engine_rows`)
	if err != nil {
		return err
	}
	var schemas []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		schemas = append(schemas, name)
	}
	rows.Close()
	for _, name := range schemas {
		if _, err := s.tombstoneOrphanedEngineRowsLocked(name); err != nil {
			return err
		}
	}
	return nil
}

// engineResidencyCount reads the ledger's row count for a schema.
func (s *FlatSQLStore) engineResidencyCount(schemaName string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = ?`, engineLedgerSchema(schemaName)).Scan(&n)
	return n, err
}

// enginePartitionState is what one engine partition reports about itself.
type enginePartitionState struct {
	count  int64
	maxSeq int64
}

// enginePartitionStates asks the engine for (count, max seq) of every
// registered partition of a table in ONE statement.
func (s *FlatSQLStore) enginePartitionStates(table string, sources []string) (map[string]enginePartitionState, error) {
	out := make(map[string]enginePartitionState, len(sources))
	if len(sources) == 0 {
		return out, nil
	}
	var b strings.Builder
	for i, source := range sources {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		partition := enginePartition(table, source)
		b.WriteString("SELECT ")
		b.WriteString(quoteEngineSQLString(source))
		b.WriteString(" AS src, COUNT(*) AS n, COALESCE(MAX(_rowid), 0) AS mx FROM ")
		b.WriteString(quoteEngineRelation(partition))
	}
	res, err := s.engineDB.Query(b.String())
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if len(row) != 3 {
			continue
		}
		source, _ := row[0].(string)
		n, _ := row[1].(int64)
		mx, _ := row[2].(int64)
		out[source] = enginePartitionState{count: n, maxSeq: mx}
	}
	return out, nil
}

// reconcileEngineResidencyLocked makes the engine's persisted partitions of a
// routed schema agree with the residency ledger after a WARM open, and
// returns the residency rows whose records must be re-ingested.
//
// The engine persisted a PREFIX of each partition's ingest sequence (its
// flush is a prefix of the arena). So for each partition:
//
//   - ledger rows with seq > the engine's max seq were ingested after the last
//     flush and are gone from the arena: their rows are dropped and the
//     records re-ingested from the control tables;
//   - every ledger row with seq <= max IS in the engine (a prefix), so the
//     engine's row count minus those rows is exactly the number of rows the
//     engine holds that the ledger does not — evicted, deleted or superseded
//     before the flush, whose tombstones the engine forgot. Only when that
//     number is non-zero are the partition's sequences listed and the extras
//     tombstoned.
//
// Caller holds s.mu for writing.
func (s *FlatSQLStore) reconcileEngineResidencyLocked(schemaName string) ([]engineResidencyRow, error) {
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return nil, nil
	}
	ledgerSchema := engineLedgerSchema(schemaName)
	sources := make([]string, 0, len(s.engineSources))
	for source := range s.engineSources {
		sources = append(sources, source)
	}
	states, err := s.enginePartitionStates(binding.Table, sources)
	if err != nil {
		return nil, fmt.Errorf("engine partition states for %s: %w", schemaName, err)
	}
	var lost []engineResidencyRow
	for _, source := range sources {
		state := states[source]
		// Rows the engine never flushed.
		rows, err := s.db.Query(`SELECT cid, source, seq FROM sdn_engine_rows WHERE schema_name = ? AND source = ? AND seq > ?`,
			ledgerSchema, source, state.maxSeq)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row engineResidencyRow
			if err := rows.Scan(&row.cid, &row.source, &row.seq); err != nil {
				rows.Close()
				return nil, err
			}
			lost = append(lost, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		var tracked int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_engine_rows WHERE schema_name = ? AND source = ? AND seq <= ?`,
			ledgerSchema, source, state.maxSeq).Scan(&tracked); err != nil {
			return nil, err
		}
		extras := state.count - tracked
		if extras <= 0 {
			continue
		}
		// The engine forgot these tombstones. List the partition's sequences,
		// subtract the ledger's, and re-apply.
		partition := enginePartition(binding.Table, source)
		res, err := s.engineDB.Query("SELECT _rowid FROM " + quoteEngineRelation(partition))
		if err != nil {
			return nil, fmt.Errorf("list %s sequences: %w", partition, err)
		}
		ledger := make(map[int64]struct{}, tracked)
		seqRows, err := s.db.Query(`SELECT seq FROM sdn_engine_rows WHERE schema_name = ? AND source = ?`, ledgerSchema, source)
		if err != nil {
			return nil, err
		}
		for seqRows.Next() {
			var seq int64
			if err := seqRows.Scan(&seq); err != nil {
				seqRows.Close()
				return nil, err
			}
			ledger[seq] = struct{}{}
		}
		seqRows.Close()
		tombstoned := 0
		for _, row := range res.Rows {
			if len(row) != 1 {
				continue
			}
			seq, ok := row[0].(int64)
			if !ok {
				continue
			}
			if _, tracked := ledger[seq]; tracked {
				continue
			}
			if err := s.engineDB.MarkDeleted(partition, uint64(seq)); err != nil {
				log.Warnf("FlatSQL engine: re-apply tombstone %s seq %d: %v", partition, seq, err)
				if s.engine.Poisoned() {
					return nil, fmt.Errorf("FlatSQL engine poisoned re-applying tombstones: %w", err)
				}
				continue
			}
			tombstoned++
		}
		if tombstoned > 0 {
			log.Infof("FlatSQL engine records: %s — re-applied %d tombstone(s) the engine did not persist", partition, tombstoned)
		}
	}
	if len(lost) > 0 {
		for start := 0; start < len(lost); start += engineTombstoneChunk {
			end := start + engineTombstoneChunk
			if end > len(lost) {
				end = len(lost)
			}
			args := make([]any, 0, end-start+1)
			args = append(args, ledgerSchema)
			for _, row := range lost[start:end] {
				args = append(args, row.cid)
			}
			if _, err := s.db.Exec(fmt.Sprintf(`DELETE FROM sdn_engine_rows WHERE schema_name = ? AND cid IN (%s)`,
				strings.TrimSuffix(strings.Repeat("?, ", end-start), ", ")), args...); err != nil {
				return nil, err
			}
		}
	}
	return lost, nil
}

// engineRecordSource is a record's bytes and the engine source it belongs to.
type engineRecordSource struct {
	cid    string
	data   []byte
	source string
}

// readEngineRecordsByCID reads record bytes (raw, sealed if the standard is
// field-encrypted) for a list of CIDs from the read source.
func (s *FlatSQLStore) readEngineRecordsByCID(schemaName string, rows []engineResidencyRow) ([]engineRecordSource, error) {
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return nil, err
	}
	out := make([]engineRecordSource, 0, len(rows))
	for _, row := range rows {
		var data []byte
		err := s.db.QueryRow(fmt.Sprintf(`SELECT data FROM %s WHERE cid = ?1`, readSource), row.cid).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			continue // deleted since; nothing to restore
		}
		if err != nil {
			return nil, err
		}
		out = append(out, engineRecordSource{cid: row.cid, data: data, source: row.source})
	}
	return out, nil
}

// ingestEngineRecordSourcesLocked ingests records grouped by source, through
// engineIngestablePayload, and returns how many landed. Caller holds s.mu.
func (s *FlatSQLStore) ingestEngineRecordSourcesLocked(schemaName string, records []engineRecordSource) (int, error) {
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return 0, nil
	}
	batch := &engineIngestBatch{store: s, schemaName: schemaName}
	skipped := 0
	for _, rec := range records {
		payload, reason, ok := engineIngestablePayload(binding, rec.data)
		if !ok {
			log.Warnf("FlatSQL engine: skip %s record %s: %s", schemaName, rec.cid, reason)
			skipped++
			continue
		}
		source := strings.TrimSpace(rec.source)
		if source == "" {
			source = engineDefaultSource
		}
		if err := s.ensureEngineSource(source); err != nil {
			log.Warnf("FlatSQL engine: register source %q: %v", source, err)
			if s.engine.Poisoned() {
				return int(batch.total), fmt.Errorf("FlatSQL engine poisoned registering source %q: %w", source, err)
			}
			skipped++
			continue
		}
		if err := batch.add(payload, rec.cid, source); err != nil {
			log.Warnf("FlatSQL engine: ingest %s records: %v", schemaName, err)
			if s.engine.Poisoned() {
				return int(batch.total), fmt.Errorf("FlatSQL engine poisoned during %s ingest: %w", schemaName, err)
			}
		}
	}
	if err := batch.flush(); err != nil {
		log.Warnf("FlatSQL engine: ingest %s records: %v", schemaName, err)
		if s.engine.Poisoned() {
			return int(batch.total), fmt.Errorf("FlatSQL engine poisoned during %s ingest: %w", schemaName, err)
		}
	}
	_ = skipped
	return int(batch.total), nil
}

// engineHotWindowPage bounds ONE hot-window page in rows. Every page is its
// own bounded engine call: an engine call that outlives the per-call budget
// poisons the instance (measured on host-02's real store: a single-statement
// CAT.fbs window held the engine 5m0s). A var so a test can shrink it.
var engineHotWindowPage = 2000

// engineWindowPagesSQL is the paged read of a routed schema's records with
// their bytes and source name, ascending by index rowid from a cursor.
//
// A LEFT JOIN on the source tags (not a correlated LIMIT-1 subquery): the
// ORDER BY inside such a subquery made the planner take the created_at index
// and walk the schema's whole tag table per record — 5m0s and a poisoned
// engine on host-02. The join takes the (schema_name, cid) equality index;
// rows arrive oldest-tag-first and the newest wins in Go.
func engineWindowPagesSQL(readSource, placeholders, extraWhere string) string {
	// extraWhere may reference ?N placeholders only by position AFTER the
	// aliases, cursor and limit; the catch-up clause below binds the ledger
	// schema as the trailing argument.
	return fmt.Sprintf(`
		SELECT page.rid, page.cid, page.data, COALESCE(tags.source_name, '') AS source_name
		FROM (
			SELECT idx.rowid AS rid, idx.cid AS cid, idx.schema_name AS schema_name, rr.data AS data
			FROM sdn_record_index idx
			JOIN %s rr ON rr.cid = idx.cid
			WHERE idx.schema_name IN (%s) AND idx.rowid > ?%s
			ORDER BY idx.rowid ASC
			LIMIT ?
		) page
		LEFT JOIN sdn_record_source_tags tags
		  ON tags.schema_name = page.schema_name AND tags.cid = page.cid
		ORDER BY page.rid ASC, tags.created_at ASC
	`, readSource, placeholders, extraWhere)
}

// engineLocker runs fn under the store write lock. hydrateEngineHotWindow
// hands one in that takes and releases s.mu per call (so readers interleave
// with a background pass, and the test hook fires between holds) or, for a
// caller that already holds the lock, one that just calls fn.
type engineLocker func(fn func() error) error

// ingestEngineWindowPages walks the index from `cursor` (exclusive) upward for
// one schema, reading and ingesting ONE PAGE PER LOCK HOLD, and returns the
// count landed. extraWhere lets the caller exclude already-resident records.
func (s *FlatSQLStore) ingestEngineWindowPages(schemaName string, cursor int64, extraWhere string, extraArgs []any, locked engineLocker) (int, error) {
	aliases := engineSchemaNameAliases(schemaName)
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")
	total := 0
	for {
		pageSize := 0
		if err := locked(func() error {
			readSource, err := s.recordReadSource(schemaName)
			if err != nil {
				return err
			}
			query := engineWindowPagesSQL(readSource, placeholders, extraWhere)
			args := make([]any, 0, len(aliases)+2+len(extraArgs))
			for _, alias := range aliases {
				args = append(args, alias)
			}
			args = append(args, cursor)
			args = append(args, extraArgs...)
			args = append(args, engineHotWindowPage)
			started := time.Now()
			rows, err := s.db.Query(query, args...)
			if err != nil {
				return err
			}
			page := make([]engineRecordSource, 0, engineHotWindowPage)
			var lastRID int64
			for rows.Next() {
				var rid int64
				var rec engineRecordSource
				if err := rows.Scan(&rid, &rec.cid, &rec.data, &rec.source); err != nil {
					rows.Close()
					return err
				}
				// One row per source tag: collapse on rowid, newest tag wins.
				if n := len(page); n > 0 && rid == lastRID {
					page[n-1].source = rec.source
					continue
				}
				lastRID = rid
				page = append(page, rec)
			}
			iterErr := rows.Err()
			rows.Close()
			if iterErr != nil {
				return iterErr
			}
			if held := time.Since(started); held*2 > s.engineExecBudget() {
				log.Warnf("FlatSQL engine rebuild: a %s hot-window page (%d rows) held the engine %s — over half the %s per-call budget. Lower the page size before this store grows again.",
					schemaName, len(page), held.Round(time.Millisecond), s.engineExecBudget())
			}
			pageSize = len(page)
			if pageSize == 0 {
				return nil
			}
			n, err := s.ingestEngineRecordSourcesLocked(schemaName, page)
			total += n
			cursor = lastRID
			return err
		}); err != nil {
			return total, err
		}
		if pageSize < engineHotWindowPage {
			return total, nil
		}
	}
}

// rebuildEngineWindowForSchema fills a routed schema's window from the
// control tables: the newest `window` records by index rowid, ingested oldest
// first so the engine's per-partition order is ingest order. Records the
// ledger already holds — written live since the cold open, before this pass
// or between its pages — are kept, not ingested a second time.
func (s *FlatSQLStore) rebuildEngineWindowForSchema(schemaName string, locked engineLocker) (int, error) {
	if _, routed := s.engineRoutedSchemaFor(schemaName); !routed {
		s.engineResidentSet(schemaName, 0)
		return 0, nil
	}
	window := s.engineWindowFor(schemaName)
	if window <= 0 {
		s.engineResidentSet(schemaName, 0)
		return 0, nil
	}
	aliases := engineSchemaNameAliases(schemaName)
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")
	args := make([]any, 0, len(aliases)+1)
	for _, alias := range aliases {
		args = append(args, alias)
	}
	args = append(args, window-1)
	// The lower bound of the window: the rowid of the window-th newest record.
	cursor := int64(0)
	if err := locked(func() error {
		var floor sql.NullInt64
		if err := s.db.QueryRow(fmt.Sprintf(`
			SELECT idx.rowid FROM sdn_record_index idx
			WHERE idx.schema_name IN (%s)
			ORDER BY idx.rowid DESC LIMIT 1 OFFSET ?`, placeholders), args...).Scan(&floor); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("locate %s hot-window floor: %w", schemaName, err)
		}
		if floor.Valid && floor.Int64 > 0 {
			cursor = floor.Int64 - 1
		}
		return nil
	}); err != nil {
		return 0, err
	}
	started := time.Now()
	notInLedger := ` AND NOT EXISTS (SELECT 1 FROM sdn_engine_rows e WHERE e.schema_name = ? AND e.cid = idx.cid)`
	n, err := s.ingestEngineWindowPages(schemaName, cursor, notInLedger, []any{engineLedgerSchema(schemaName)}, locked)
	if err != nil {
		return 0, err
	}
	return n, locked(func() error {
		resident, cErr := s.engineResidencyCount(schemaName)
		if cErr != nil {
			resident = int64(n)
		}
		s.engineResidentSet(schemaName, resident)
		s.engineSchemaLoadedSet(schemaName)
		if n > 0 {
			log.Infof("FlatSQL engine rebuild: loaded %d %s records into the hot window (window %d) in %s",
				n, schemaName, window, time.Since(started).Round(time.Millisecond))
		}
		return nil
	})
}

// warmEngineSchema brings one routed schema's window current after a WARM
// open: reconcile the persisted partitions against the ledger, restore the
// records the engine never flushed, ingest the tail past the coverage mark
// (the records the ledger does not hold), and re-apply the window bound.
func (s *FlatSQLStore) warmEngineSchema(schemaName string, fromRowID int64, locked engineLocker) (int, error) {
	restored := 0
	if err := locked(func() error {
		lost, err := s.reconcileEngineResidencyLocked(schemaName)
		if err != nil {
			return err
		}
		if len(lost) == 0 {
			return nil
		}
		records, err := s.readEngineRecordsByCID(schemaName, lost)
		if err != nil {
			return fmt.Errorf("read %s records lost from the engine arena: %w", schemaName, err)
		}
		restored, err = s.ingestEngineRecordSourcesLocked(schemaName, records)
		if err != nil {
			return err
		}
		log.Infof("FlatSQL engine records: %s — re-ingested %d record(s) the engine had not flushed", schemaName, restored)
		return nil
	}); err != nil {
		return restored, err
	}
	notInLedger := ` AND NOT EXISTS (SELECT 1 FROM sdn_engine_rows e WHERE e.schema_name = ? AND e.cid = idx.cid)`
	ledgerArgs := []any{engineLedgerSchema(schemaName)}
	tail, err := s.ingestEngineWindowPages(schemaName, fromRowID, notInLedger, ledgerArgs, locked)
	if err != nil {
		return restored + tail, err
	}
	// THE WINDOW IS AN ENGINE BOUND, NOT DATA LOSS. A window widened since
	// the last run (or one emptied by deletes) is refilled with the newest
	// records the ledger does not hold, so a wider window sees the history
	// again. A full window makes this one cheap COUNT.
	backfilled := 0
	if err := locked(func() error {
		resident, err := s.engineResidencyCount(schemaName)
		if err != nil {
			return err
		}
		deficit := int64(s.engineWindowFor(schemaName)) - resident
		if deficit <= 0 {
			return nil
		}
		aliases := engineSchemaNameAliases(schemaName)
		args := make([]any, 0, len(aliases)+2)
		for _, alias := range aliases {
			args = append(args, alias)
		}
		args = append(args, ledgerArgs...)
		args = append(args, deficit-1)
		var floor sql.NullInt64
		err = s.db.QueryRow(fmt.Sprintf(`
			SELECT idx.rowid FROM sdn_record_index idx
			WHERE idx.schema_name IN (%s)%s
			ORDER BY idx.rowid DESC LIMIT 1 OFFSET ?`,
			strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", "), notInLedger), args...).Scan(&floor)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Fewer untracked records than the deficit: take them all.
			floor = sql.NullInt64{Int64: 1, Valid: true}
		case err != nil:
			return fmt.Errorf("locate %s backfill floor: %w", schemaName, err)
		}
		n, err := s.ingestEngineWindowPages(schemaName, floor.Int64-1, notInLedger, ledgerArgs, func(fn func() error) error { return fn() })
		backfilled += n
		return err
	}); err != nil {
		return restored + tail + backfilled, err
	}
	err = locked(func() error {
		resident, err := s.engineResidencyCount(schemaName)
		if err != nil {
			return err
		}
		s.engineResidentSet(schemaName, resident)
		s.engineSchemaLoadedSet(schemaName)
		if err := s.enforceEngineHotWindowLocked(schemaName); err != nil {
			return err
		}
		if tail > 0 || backfilled > 0 {
			log.Infof("FlatSQL engine records: %s — ingested %d record(s) written past the coverage mark and backfilled %d into the window", schemaName, tail, backfilled)
		}
		return nil
	})
	return restored + tail + backfilled, err
}

// settleEngineResidencyAtOpen makes the residency ledger describe the engine
// that was just opened, BEFORE any write can land: a warm engine's ledger is
// read back into the resident counts; a cold engine holds nothing, so its
// ledger (rows of an arena that is gone) is emptied here rather than at
// hydration time — a write that lands in between is resident and tracked,
// and the rebuild leaves it alone. Caller holds the store exclusively.
func (s *FlatSQLStore) settleEngineResidencyAtOpen() {
	if s.engineStateWarm {
		if resident, err := s.restoreEngineResidencyFromLedger(); err != nil {
			log.Warnf("FlatSQL engine records: residency ledger not readable at open (%v); counts are restored by the hot-window hydration", err)
		} else {
			log.Infof("FlatSQL engine records: %d resident record(s) tracked by the ledger across routed standards", resident)
		}
		return
	}
	if _, err := s.db.Exec(`DELETE FROM sdn_engine_rows`); err != nil {
		log.Warnf("FlatSQL engine records: could not reset the residency ledger for a cold engine (%v); the hot-window hydration reconciles it", err)
	}
}

// restoreEngineResidencyFromLedger sets the Go-side resident counts from the
// residency ledger at open, so a write that lands before the background
// hydration has reconciled the window still evicts against the real count.
// Called from NewFlatSQLStore before the store is shared, so no locking.
func (s *FlatSQLStore) restoreEngineResidencyFromLedger() (int64, error) {
	rows, err := s.db.Query(`SELECT schema_name, COUNT(*) FROM sdn_engine_rows GROUP BY schema_name`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var schemaName string
		var n int64
		if err := rows.Scan(&schemaName, &n); err != nil {
			return total, err
		}
		s.engineResidentSet(schemaName, n)
		total += n
	}
	return total, rows.Err()
}

// hydrateEngineHotWindow brings the engine hot window current for every
// routed schema that has records. lockPerSchema=false means the caller already
// holds the store write lock (a synchronous open, engine recovery); true takes
// and releases it per PAGE so readers interleave with a background pass.
func (s *FlatSQLStore) hydrateEngineHotWindow(ctx context.Context, lockPerSchema bool) (int, error) {
	var locked engineLocker = func(fn func() error) error {
		if !lockPerSchema {
			return fn()
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if hook := s.engineHydrateBatchHook; hook != nil {
			hook()
		}
		defer s.lockWrite("engine hot window: hydrate page")()
		return fn()
	}

	warm := s.engineStateWarm
	fromRowID := s.engineMarkRowID.Load()
	var present map[string]bool
	if err := locked(func() error {
		s.preregisterEngineSources()
		var err error
		present, err = s.schemasWithRecordTables()
		if err != nil {
			log.Warnf("FlatSQL engine hydrate: enumerate schemas with record tables: %v", err)
			present = nil
		}
		// A cold engine's ledger was emptied at OPEN (settleEngineResidencyAtOpen),
		// before any write could land; whatever it holds now is resident and
		// the rebuild below keeps it.
		return nil
	}); err != nil {
		return 0, err
	}

	total := 0
	for _, schemaName := range s.engineRoutedSchemaNames() {
		if present != nil && !present[schemaName] {
			s.engineResidentSet(schemaName, 0)
			s.engineSchemaLoadedSet(schemaName)
			continue
		}
		if warm {
			n, err := s.warmEngineSchema(schemaName, fromRowID, locked)
			total += n
			if err != nil {
				return total, err
			}
			continue
		}
		n, err := s.rebuildEngineWindowForSchema(schemaName, locked)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// HydrateEngineHotWindowContext brings the engine hot window current in the
// background, locking per schema so every reader lane keeps answering. On a
// warm open it reconciles and ingests only the tail; on a cold one it fills
// each window from the control tables. It ends by flushing the engine's
// record state and writing the coverage mark, so a restart before the next
// checkpoint still opens warm. A cancelled pass is not an error and does not
// mark the window hydrated.
func (s *FlatSQLStore) HydrateEngineHotWindowContext(ctx context.Context) (int, error) {
	if s.engineHotHydrated.Load() {
		return 0, nil
	}
	s.engineHotHydrating.Store(true)
	defer s.engineHotHydrating.Store(false)

	count, err := s.hydrateEngineHotWindow(ctx, true)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Infof("FlatSQL engine hot-window hydration cancelled after %d records — the next boot resumes it", count)
			return count, nil
		}
		return count, err
	}
	s.engineHotHydrated.Store(true)
	func() {
		defer s.lockWrite("engine hot window: flush record state")()
		if err := s.checkpointEngineLocked(); err != nil {
			log.Warnf("FlatSQL engine record state not flushed after hydration (the next boot rebuilds the tail): %v", err)
			return
		}
		log.Infof("FlatSQL engine record state flushed to disk after hydration (%d records ingested, coverage rowid %d)", count, s.engineMarkRowID.Load())
	}()
	return count, nil
}

// HydrateEngineHotWindow is HydrateEngineHotWindowContext without a context.
func (s *FlatSQLStore) HydrateEngineHotWindow() (int, error) {
	return s.HydrateEngineHotWindowContext(context.Background())
}

// rebuildEngineRecordsLocked is the synchronous form for a caller that holds
// the store write lock: the non-deferred open and engine recovery.
func (s *FlatSQLStore) rebuildEngineRecordsLocked() error {
	_, err := s.hydrateEngineHotWindow(context.Background(), false)
	if err != nil {
		return err
	}
	s.engineHotHydrated.Store(true)
	return nil
}
