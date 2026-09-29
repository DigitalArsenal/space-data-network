package storage

// source_batch_current.go — the current-snapshot reconcile: every source
// ingest in reconcile mode "current" (storage.ingest_with_source) counts the
// lane's older batches before it stores a batch and evicts them after it, and
// the `reconcile-source-batch` command runs the same call.
//
// For one (schema, provider, source) group and a kept batch, the reconcile
// deletes the group's tags of every OTHER batch, then deletes a record only
// when no tag of the schema names its CID any more — from every (producer,
// standard) table, the record index and the engine hot window. Matched counts
// the other batches' tags whose record a producer table holds; Deleted counts
// the records deleted. When nothing is matched the call changes nothing, even
// if the other batches still carry tags of records no producer table holds.
//
// A CALL COSTS THE BATCHES IT EVICTS, NEVER THE SCHEMA, AND NO SINGLE LOCK
// HOLD PAYS FOR ALL OF THEM. host-02 (beta.78, 2026-09-29): this call held the
// store write lock 5.57 s. It ran under one write hold from start to end: the
// count of the other batches' tags, the staging, the tag, mirror and index
// deletes of every other batch in one transaction, then the anti-join of the
// whole residency ledger (the 400,000-row engine hot window) against the
// record index, then a summary rebuild of EVERY lane of the schema (all of
// host-02's $OMM tags joined to their records). sdn 1480b98e5 (dataset
// supersede) and b75d1e89b (duplicate reconcile) removed the same pattern;
// this is that pattern applied here:
//   - the count reads the other batches in slices of their tag index, one
//     READ hold per slice (a dry run takes no write hold at all);
//   - the other batches are evicted in chunks, one write hold + transaction
//     each, sized from the last hold by nextSupersedeChunk. A chunk stages
//     CIDs from the two index ranges either side of the kept batch, counts
//     its held tags, deletes the group's other-batch tags of those CIDs and
//     computes its orphan set ONCE (deleteStagedOrphansTx); the mirror
//     deletes, the index delete and the engine tombstones
//     (tombstoneStagedEngineRowsLocked) all use that set;
//   - the whole-ledger residency sweep the old call ran runs once after the
//     chunks, in pages (sweepOrphanedEngineRowsPaged), so a residency row
//     orphaned by any other path is still tombstoned exactly as before;
//   - only the evicted lanes' summaries are rebuilt, one lane per hold. No
//     other lane's inputs changed: a lane's summary is its tags joined to
//     their records, the reconcile deletes tags of the evicted lanes only,
//     and it deletes a record only when NO tag of the schema names it.
//
// Between holds the group can change. A tag stored into an older batch after
// a chunk passed its CIDs is evicted by a later chunk or the next call (every
// current-mode ingest runs one); a record another path evicted first is
// simply gone, and its deletes match nothing. Matched is the sum of the
// chunks' counts, taken as each chunk deletes, so it counts what this call
// evicted. The call always ends where some order of the old single-hold call
// and the other writers would have.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const (
	// currentReconcileSite names the reconcile's store-lock holds.
	currentReconcileSite = "ReconcileSourceBatch"
	// currentReconcileBatchSeeks bounds the batch-id seeks one read hold
	// runs while the call lists the group's batches.
	currentReconcileBatchSeeks = 256
)

// currentReconcileCountSlice bounds the tags one count slice reads under one
// read hold: a range of one batch's tag index plus one CID seek per producer
// table per tag. A variable so a test can cross slice boundaries with a small
// fixture.
var currentReconcileCountSlice = 2048

// currentReconcileStats is one call's lock account: how many eviction chunks
// ran, and the longest store-lock hold of the whole call (count slices,
// chunks, residency sweep pages and summary lanes) — the longest any reader
// could have waited. It goes to the reconcile's log line and not into
// SourceBatchReconcileResult, whose format-1 and format-2 values are the same
// answer and compare equal.
type currentReconcileStats struct {
	Chunks      int
	MaxLockHold time.Duration
}

func (st *currentReconcileStats) noteHold(held time.Duration) {
	if held > st.MaxLockHold {
		st.MaxLockHold = held
	}
}

// ReconcileSourceBatch deletes source-tagged records outside the accepted
// source batch. It is intended for DPM-series reconciliation after an operator
// has selected the latest accepted source hash/batch ID, and it is the
// "current" reconcile mode of a source ingest. See the file comment for how
// the work is split into bounded lock holds.
func (s *FlatSQLStore) ReconcileSourceBatch(schemaName, providerID, sourceName, keepBatch string, apply bool) (SourceBatchReconcileResult, error) {
	result, _, err := s.reconcileSourceBatch(schemaName, providerID, sourceName, keepBatch, apply)
	return result, err
}

// reconcileSourceBatch is ReconcileSourceBatch with the call's lock account.
func (s *FlatSQLStore) reconcileSourceBatch(schemaName, providerID, sourceName, keepBatch string, apply bool) (SourceBatchReconcileResult, currentReconcileStats, error) {
	var stats currentReconcileStats
	if apply {
		if err := s.requireWritable("reconcile source batch"); err != nil {
			return SourceBatchReconcileResult{}, stats, err
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
		return result, stats, errors.New("schema name is required")
	}
	if result.ProviderID == "" {
		return result, stats, errors.New("provider id is required")
	}
	if result.SourceName == "" {
		return result, stats, errors.New("source name is required")
	}
	if result.KeepBatch == "" {
		return result, stats, errors.New("keep batch is required")
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, stats, fmt.Errorf("invalid schema name: %w", err)
	}
	if s.ps != nil {
		result, err := s.f2ReconcileSourceBatch(result)
		return result, stats, err
	}

	started := time.Now()
	noteHold := stats.noteHold
	batches, err := s.currentReconcileOtherBatches(result, noteHold)
	if err != nil {
		return result, stats, err
	}
	// A dry run counts every held tag of the other batches. The apply only
	// asks whether there is one (the old call changed nothing when its count
	// was zero) and takes its count from the chunks that evict them.
	held, err := s.countCurrentReconcileHeld(result, batches, apply, noteHold)
	if err != nil {
		return result, stats, err
	}
	if !apply {
		result.Matched = held
		return result, stats, nil
	}
	if held == 0 {
		return result, stats, nil
	}

	chunk := nextSupersedeChunk(0, 0)
	for {
		got, err := s.reconcileCurrentChunk(result, tableName, chunk)
		noteHold(got.held)
		if err != nil {
			return result, stats, err
		}
		if got.staged == 0 {
			break
		}
		stats.Chunks++
		result.Matched += got.matched
		result.Deleted += got.records
		chunk = nextSupersedeChunk(chunk, got.held)
	}

	// The whole-schema residency sweep the old call ran after its commit, once
	// and in pages: it tombstones residency rows orphaned by anything, not
	// only by this reconcile.
	if err := s.sweepOrphanedEngineRowsPaged(currentReconcileSite+": engine residency sweep", result.SchemaName, noteHold); err != nil {
		return result, stats, err
	}
	if err := s.rebuildCurrentReconcileLanes(result, batches, noteHold); err != nil {
		return result, stats, err
	}
	log.Infof("Source batch reconcile %s %s/%s keep %s: %d tags matched, %d records deleted in %d chunks in %s; longest store-lock hold %s",
		result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch,
		result.Matched, result.Deleted, stats.Chunks,
		time.Since(started).Round(time.Millisecond), stats.MaxLockHold.Round(time.Millisecond))
	return result, stats, nil
}

// currentReconcileOtherBatches lists the group's batches other than the kept
// one, in batch order: one seek of the batch tag index per batch
// (idx_sdn_record_source_tags_batch_cid, the next batch_id past the last),
// never a walk of the batches' rows, and up to currentReconcileBatchSeeks
// seeks per read hold.
func (s *FlatSQLStore) currentReconcileOtherBatches(scope SourceBatchReconcileResult, noteHold func(time.Duration)) ([]string, error) {
	const firstSQL = `
		SELECT batch_id FROM sdn_record_source_tags INDEXED BY idx_sdn_record_source_tags_batch_cid
		WHERE schema_name = ? AND provider_id = ? AND source_name = ?
		ORDER BY batch_id LIMIT 1`
	const nextSQL = `
		SELECT batch_id FROM sdn_record_source_tags INDEXED BY idx_sdn_record_source_tags_batch_cid
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id > ?
		ORDER BY batch_id LIMIT 1`
	group := []any{scope.SchemaName, scope.ProviderID, scope.SourceName}
	var batches []string
	last, started, done := "", false, false
	for seeks := 0; !done; {
		release := s.lockRead(currentReconcileSite + ": batches")
		holdStart := time.Now()
		err := func() error {
			if err := s.closedErr(); err != nil {
				return err
			}
			for n := 0; n < currentReconcileBatchSeeks; n++ {
				var next string
				var err error
				if started {
					err = s.db.QueryRow(nextSQL, append(group, last)...).Scan(&next)
				} else {
					err = s.db.QueryRow(firstSQL, group...).Scan(&next)
				}
				if errors.Is(err, sql.ErrNoRows) {
					done = true
					return nil
				}
				if err != nil {
					return fmt.Errorf("list source batches of %s/%s/%s: %w", scope.SchemaName, scope.ProviderID, scope.SourceName, err)
				}
				if started && next <= last {
					return fmt.Errorf("list source batches of %s/%s/%s: seek did not advance past %q", scope.SchemaName, scope.ProviderID, scope.SourceName, last)
				}
				started, last = true, next
				if next != scope.KeepBatch {
					batches = append(batches, next)
				}
				if seeks++; seeks > sourceSummaryLaneScanLimit {
					return fmt.Errorf("list source batches of %s/%s/%s: more than %d batches", scope.SchemaName, scope.ProviderID, scope.SourceName, sourceSummaryLaneScanLimit)
				}
			}
			return nil
		}()
		noteHold(time.Since(holdStart))
		release()
		if err != nil {
			return nil, err
		}
	}
	return batches, nil
}

// countCurrentReconcileHeld counts the tags of batches whose record a producer
// table holds, in slices of currentReconcileCountSlice tags, one read hold
// each. A slice is a CLOSED cid range of one batch's tag index, so every tag
// row of a CID in that batch lands in one slice. anyOnly stops at the first
// slice that holds one.
func (s *FlatSQLStore) countCurrentReconcileHeld(scope SourceBatchReconcileResult, batches []string, anyOnly bool, noteHold func(time.Duration)) (int64, error) {
	boundarySQL := fmt.Sprintf(`
		SELECT cid FROM sdn_record_source_tags INDEXED BY idx_sdn_record_source_tags_batch_cid
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ? AND cid > ?
		ORDER BY cid
		LIMIT 1 OFFSET %d`, currentReconcileCountSlice-1)
	var total int64
	for _, batch := range batches {
		lane := []any{scope.SchemaName, scope.ProviderID, scope.SourceName, batch}
		after := ""
		for {
			bound := ""
			var n int64
			release := s.lockRead(currentReconcileSite + ": count")
			started := time.Now()
			err := func() error {
				if err := s.closedErr(); err != nil {
					return err
				}
				if err := s.db.QueryRow(boundarySQL, append(lane, after)...).Scan(&bound); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("slice source batch reconciliation count: %w", err)
				}
				tables, err := s.recordTablesForSchema(scope.SchemaName)
				if err != nil {
					return fmt.Errorf("record tables: %w", err)
				}
				args := append(append([]any(nil), lane...), after)
				upper := ""
				if bound != "" {
					upper = " AND tags.cid <= ?"
					args = append(args, bound)
				}
				// Driven by the batch's tag rows, each checked against the
				// producer tables by CID: joining the union read source
				// materialised every record of the standard (plan guard).
				if err := s.db.QueryRow(`
					SELECT COUNT(*)
					FROM sdn_record_source_tags tags INDEXED BY idx_sdn_record_source_tags_batch_cid
					WHERE tags.schema_name = ?
					  AND tags.provider_id = ?
					  AND tags.source_name = ?
					  AND tags.batch_id = ?
					  AND tags.cid > ?`+upper+`
					  AND `+recordHeldSQL(tables, "tags.cid"), args...).Scan(&n); err != nil {
					return fmt.Errorf("count source batch reconciliation records: %w", err)
				}
				return nil
			}()
			noteHold(time.Since(started))
			release()
			if err != nil {
				return total, err
			}
			total += n
			if anyOnly && total > 0 {
				return total, nil
			}
			if bound == "" {
				break
			}
			if bound <= after {
				return total, fmt.Errorf("count source batch %s/%s/%s/%s: slice cursor did not advance past %q",
					scope.SchemaName, scope.ProviderID, scope.SourceName, batch, after)
			}
			after = bound
		}
	}
	return total, nil
}

// currentReconcileChunk is one chunk's account.
type currentReconcileChunk struct {
	staged  int64
	matched int64
	records int64
	held    time.Duration
}

// reconcileCurrentChunk evicts up to limit CIDs of the group's other batches
// under one write hold + transaction, and tombstones the evicted records'
// engine rows before it releases the lock. staged == 0 means there was
// nothing left to evict.
func (s *FlatSQLStore) reconcileCurrentChunk(scope SourceBatchReconcileResult, tableName string, limit int) (out currentReconcileChunk, err error) {
	out.held, err = s.holdWrite(currentReconcileSite, func() error {
		if err := s.closedErr(); err != nil {
			return err
		}
		if limit < 1 {
			limit = 1
		}
		tables, err := s.recordTablesForSchema(scope.SchemaName)
		if err != nil {
			return fmt.Errorf("record tables: %w", err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin source batch reconciliation: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		for _, stmt := range []string{
			`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_cids (cid TEXT PRIMARY KEY)`,
			`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_orphans (cid TEXT PRIMARY KEY)`,
			`DELETE FROM temp_sdn_reconcile_cids`,
			`DELETE FROM temp_sdn_reconcile_orphans`,
		} {
			if _, err := tx.Exec(flatsqldrv.WithoutJournal(stmt)); err != nil {
				return fmt.Errorf("prepare source batch reconciliation tables: %w", err)
			}
		}

		// Stage from the two index ranges either side of the kept batch
		// (idx_sdn_record_source_tags_batch_cid), as the supersede chunk does:
		// `batch_id <> ?` is not a range, and SQLite would step over every row
		// of the kept batch whenever its id sorts first. batch_id is TEXT NOT
		// NULL, so the two ranges are exactly `<>`. The chunk deletes every
		// other-batch tag of the CIDs it stages, so the next chunk's ranges
		// start past them.
		group := []any{scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch}
		for _, cmp := range []string{"<", ">"} {
			if out.staged >= int64(limit) {
				break
			}
			if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
				INSERT OR IGNORE INTO temp_sdn_reconcile_cids (cid)
				SELECT cid FROM sdn_record_source_tags INDEXED BY idx_sdn_record_source_tags_batch_cid
				WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id `+cmp+` ?
				LIMIT ?
			`), append(group, int64(limit)-out.staged)...); err != nil {
				return fmt.Errorf("stage source batch cids: %w", err)
			}
			if err := tx.QueryRow(`SELECT COUNT(*) FROM temp_sdn_reconcile_cids`).Scan(&out.staged); err != nil {
				return fmt.Errorf("count staged source batch cids: %w", err)
			}
		}
		if out.staged == 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit empty source batch reconciliation chunk: %w", err)
			}
			committed = true
			return nil
		}

		// Matched: the staged CIDs' other-batch tags whose record a producer
		// table holds, counted before they go. One seek per staged CID on the
		// (schema_name, cid, provider_id, source_name) prefix of the tag key,
		// and one CID seek per producer table.
		if err := tx.QueryRow(`
			SELECT COUNT(*)
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = ? AND tags.provider_id = ? AND tags.source_name = ? AND tags.batch_id <> ?
			  AND tags.cid IN (SELECT cid FROM temp_sdn_reconcile_cids)
			  AND `+recordHeldSQL(tables, "tags.cid"), group...).Scan(&out.matched); err != nil {
			return fmt.Errorf("count source batch reconciliation records: %w", err)
		}
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM sdn_record_source_tags
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
			  AND cid IN (SELECT cid FROM temp_sdn_reconcile_cids)
		`), group...); err != nil {
			return fmt.Errorf("delete source batch tags: %w", err)
		}
		// Deleted counts LOGICAL records (per cid), independent of how many
		// tables hold the row.
		if out.records, err = s.deleteStagedOrphansTx(tx, scope.SchemaName, tableName,
			"temp_sdn_reconcile_cids", "temp_sdn_reconcile_orphans"); err != nil {
			return fmt.Errorf("source batch records: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit source batch reconciliation: %w", err)
		}
		committed = true
		if out.records > 0 {
			if _, err := s.tombstoneStagedEngineRowsLocked(scope.SchemaName, "temp_sdn_reconcile_orphans"); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// rebuildCurrentReconcileLanes rebuilds the summary rows of the lanes the
// reconcile evicted — the batches it listed, and any other batch of the group
// the summary names — one lane per store-lock hold. See the file comment for
// why no other lane is touched.
func (s *FlatSQLStore) rebuildCurrentReconcileLanes(scope SourceBatchReconcileResult, batches []string, noteHold func(time.Duration)) error {
	seen := make(map[string]bool, len(batches))
	lanes := make([]string, 0, len(batches))
	for _, batch := range batches {
		if !seen[batch] {
			seen[batch] = true
			lanes = append(lanes, batch)
		}
	}
	err := func() error {
		defer s.lockRead(currentReconcileSite + ": summary lanes")()
		rows, err := s.db.Query(`
			SELECT DISTINCT batch_id FROM sdn_record_source_summary
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		`, scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch)
		if err != nil {
			return fmt.Errorf("list reconciled summary lanes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var batch string
			if err := rows.Scan(&batch); err != nil {
				return fmt.Errorf("scan reconciled summary lane: %w", err)
			}
			if !seen[batch] {
				seen[batch] = true
				lanes = append(lanes, batch)
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return err
	}
	for _, batch := range lanes {
		lane := sourceSummaryLane{SchemaName: scope.SchemaName, ProviderID: scope.ProviderID, SourceName: scope.SourceName, BatchID: batch}
		held, err := s.holdWrite(currentReconcileSite+": source summary", func() error {
			if err := s.closedErr(); err != nil {
				return err
			}
			return s.rebuildSourceSummaryLane(lane)
		})
		noteHold(held)
		if err != nil {
			return err
		}
	}
	return nil
}
