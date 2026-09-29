package storage

// dataset_supersede.go — pinned-dataset supersede eviction (gateway loop G.4,
// docs/gateway-api.md §10).
//
// A gateway.pin replicates ONE dataset generation per (provider, standard):
// each new publication batch SUPERSEDES the previous pin — the old batch's
// records are evicted from the store rather than accumulated. Eviction is the
// batch-keyed inverse of the shard import path: source-tag rows for other
// batches of the (provider, source, schema) group are deleted, and any record
// whose CID no longer carries a source tag is removed from the routed
// (producer, standard) tables, the global record index and the engine hot
// window. Records SHARED with the kept batch (unchanged content, same CID,
// re-tagged by the newer import) survive untouched.
//
// The work is CHUNKED: each chunk evicts a bounded set of CIDs under one
// store write lock + one transaction, releasing the lock between chunks so
// readers interleave — the same lock-window discipline the 2026-07-06
// production blackout forced onto the import path (storeWriteChunkSize). Each
// chunk commits atomically; the operation is idempotent, so an interrupted
// supersede converges on the next evaluation.
//
// A CHUNK'S COST MUST BE ITS OWN, NEVER THE STORE'S. host-02, 2026-09-29: the
// first ReplaceCurrent of a replicated $OMM lane held the store lock 57-80 s
// per 2,048-CID chunk, 78 chunks in a row, and other writers queued behind it
// for up to 57 s. Each chunk asked "which of these CIDs still carry a tag?" as
// `cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`
// three times (a count, then one DELETE per partition mirror), and SQLite
// answers a non-correlated NOT IN by materializing the whole list: every $OMM
// tag on the node, ~15 s per statement. After its commit the chunk anti-joined
// the ENTIRE $OMM residency ledger (the 400,000-row engine hot window) against
// the record index, another 4 s. Now the orphan set is computed ONCE per chunk
// by a correlated NOT EXISTS that seeks the (schema_name, cid) prefix of the
// tag table's primary key, the mirror and index deletes reuse it, and the
// engine tombstones only that set's residency rows. Every statement in a chunk
// is proportional to the chunk.
//
// The chunk SIZE follows its measured lock hold (nextSupersedeChunk), because
// the same CID count costs an order of magnitude more on a small host with a
// multi-GB database than on a workstation: it starts at half the import
// path's window, grows while holds are cheap and shrinks in proportion when
// one runs long.
//
// Two whole-schema passes that the old code ran inside or next to every chunk
// now run once, after the chunks, in bounded lock windows of their own:
//   - the engine residency sweep (sweepOrphanedEngineRowsPaged): the same
//     anti-join the old chunk ran, in pages of the ledger, so a residency row
//     orphaned by any other path is still tombstoned exactly as before;
//   - the source summary: only the lanes this supersede evicted are rebuilt,
//     one lane per lock hold. No other lane's inputs changed — a lane's
//     summary is its tags joined to their records, the supersede deletes tags
//     of evicted lanes only, and it deletes a record only when NO tag of the
//     schema names it — so their incrementally maintained rows stay exact.
//     Rebuilding every lane of the schema under one hold was the next
//     unbounded hold behind the chunks (all of host-02's $OMM tags joined to
//     their records).
//
// NOT touched, deliberately:
//   - superseded publication METADATA rows (sdn_dataset_shard_publications):
//     they are the few-hundred-byte provenance record AND the trusted-peer
//     catch-up dedup key (datasetShardPublicationAlreadyCached) — deleting
//     them would make the next catch-up cycle re-materialize the very batch
//     the supersede just evicted. Only their cached shard/index FILES are
//     removed; a batch with rows but no files is simply not servable;
//   - the provider's OWN publication history (callers must not supersede
//     the node's own provider identity; the node-level hook skips self).

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const (
	// supersedeChunkStart is the first chunk, on hardware nothing is known
	// about yet: half the import path's window (storeWriteChunkSize), which
	// host-02 holds for ~0.5 s. Evicting a CID is the same order of work as
	// importing one — its tags, index row and producer rows, each with their
	// indexes and triggers — and host-02's old chunks spent 4-8 ms per CID in
	// the statements that did not scan the schema (per 2,048 CIDs: tag delete
	// 5-11 s, index delete 0.5-2.7 s, COMMIT 3.1-3.3 s).
	supersedeChunkStart = storeWriteChunkSize / 2
	// supersedeChunkMin is the floor the size may shrink to on a host slower
	// than that.
	supersedeChunkMin = 8
	// supersedeChunkMax is the fixed chunk this path used to run at on every
	// size of host.
	supersedeChunkMax = 2048
	// supersedeChunkTarget is the lock hold the chunk size steers toward:
	// half the one-second bound, so one chunk's variance (a WAL checkpoint
	// landing in its commit) stays inside it.
	supersedeChunkTarget = 250 * time.Millisecond
	// supersedeEngineSweepPage bounds the residency rows one sweep window
	// anti-joins against the record index: one index-range walk plus one
	// primary-key probe per row.
	supersedeEngineSweepPage = 2048
)

// nextSupersedeChunk returns the next chunk size from the last chunk's size
// and lock hold. A hold past the target shrinks the chunk in proportion (a
// 4 s chunk of 2,048 becomes 128 at once, not after five halvings); a hold
// under half of it doubles the chunk; anything between keeps it.
//
// Growth is needed, not a nicety: every chunk pays a fixed COMMIT (a WAL
// checkpoint and its syncs once the chunk's pages pass wal_autocheckpoint) —
// ~100 ms on the owner's machine, where 64 CIDs of deletes are ~10 ms more —
// so a chunk stuck at its first size runs the supersede ten times longer than
// it needs to, and readers pay a lock window per chunk. Growing only from
// under half the target bounds a chunk that turns out costlier per CID than
// the one before it: a CID whose record is evicted writes about three times
// the b-tree entries of one whose record survives (its index row, producer
// rows and residency row go too, not only its tags), so the next hold stays
// near 3x half the target — inside the one-second bound.
func nextSupersedeChunk(current int, held time.Duration) int {
	if current <= 0 {
		return supersedeChunkStart
	}
	next := current
	switch {
	case held <= 0:
	case held > supersedeChunkTarget:
		next = int(int64(current) * int64(supersedeChunkTarget) / int64(held))
	case held < supersedeChunkTarget/2:
		next = current * 2
	}
	if next < supersedeChunkMin {
		next = supersedeChunkMin
	}
	if next > supersedeChunkMax {
		next = supersedeChunkMax
	}
	return next
}

// DatasetSupersedeResult reports one supersede eviction.
type DatasetSupersedeResult struct {
	SchemaName     string
	ProviderID     string
	SourceName     string
	KeepBatch      string
	TagsDeleted    int64
	RecordsDeleted int64
	FilesDeleted   int
	// Chunks is how many eviction chunks ran; MaxLockHold is the longest
	// store write-lock hold of the whole supersede (chunks, residency sweep
	// pages and summary lanes) — the longest any reader could have waited.
	// Both go to the supersede's log line, not to the admin API's JSON.
	Chunks      int           `json:"-"`
	MaxLockHold time.Duration `json:"-"`
}

func (r *DatasetSupersedeResult) noteHold(held time.Duration) {
	if held > r.MaxLockHold {
		r.MaxLockHold = held
	}
}

// holdWrite runs fn under the store write lock and reports how long it held
// the lock.
func (s *FlatSQLStore) holdWrite(site string, fn func() error) (time.Duration, error) {
	release := s.lockWrite(site)
	started := time.Now()
	err := fn()
	held := time.Since(started)
	release()
	return held, err
}

// supersedeChunkResult is one chunk's account.
type supersedeChunkResult struct {
	staged  int64
	tags    int64
	records int64
	held    time.Duration
}

// SupersedeSourceBatches evicts every batch EXCEPT keepBatch for one
// (schema, provider, source) group: superseded source-tag rows, orphaned
// records (all backing tables, the record index and the engine hot window),
// and the superseded batches' cached shard/index files. Publication metadata
// rows are KEPT (see the package comment). Chunked per the package comment.
func (s *FlatSQLStore) SupersedeSourceBatches(schemaName, providerID, sourceName, keepBatch string) (DatasetSupersedeResult, error) {
	result := DatasetSupersedeResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		KeepBatch:  strings.TrimSpace(keepBatch),
	}
	if s == nil {
		return result, errors.New("store is required")
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
	if err := s.requireWritable("supersede source batches"); err != nil {
		return result, err
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	started := time.Now()
	chunk := nextSupersedeChunk(0, 0)
	for {
		got, err := s.supersedeSourceBatchChunk(result, tableName, chunk)
		if err != nil {
			return result, err
		}
		if got.staged == 0 {
			break
		}
		result.Chunks++
		result.TagsDeleted += got.tags
		result.RecordsDeleted += got.records
		result.noteHold(got.held)
		chunk = nextSupersedeChunk(chunk, got.held)
	}

	if result.Chunks > 0 {
		// The whole-schema residency sweep every old chunk ran after its
		// commit, once and in pages: it tombstones residency rows orphaned by
		// anything, not only by this supersede.
		if err := s.sweepOrphanedEngineRowsPaged("dataset supersede: engine residency sweep", result.SchemaName, result.noteHold); err != nil {
			return result, err
		}
		if err := s.rebuildSupersededSummaryLanes(&result); err != nil {
			return result, err
		}
		log.Infof("Dataset supersede %s %s/%s keep %s: evicted %d source tags and %d records in %d chunks in %s; longest store-lock hold %s",
			result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch,
			result.TagsDeleted, result.RecordsDeleted, result.Chunks,
			time.Since(started).Round(time.Millisecond), result.MaxLockHold.Round(time.Millisecond))
	}

	// Superseded batches' cached shard/index files. The publication metadata
	// rows are kept (catch-up dedup + provenance, see the package comment);
	// only the payload files go.
	stale, err := s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   result.SchemaName,
		ProviderID:   result.ProviderID,
		SourceName:   result.SourceName,
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		return result, err
	}
	keepFiles := make(map[string]bool, 4)
	for _, pub := range stale {
		if pub.BatchID != result.KeepBatch {
			continue
		}
		// A shard shared byte-identically across batches resolves to the
		// same file name (query/shard hash pair) — never delete a file the
		// kept batch still serves.
		if shardPath, err := s.DatasetPublicationShardPath(pub); err == nil {
			keepFiles[shardPath] = true
		}
		if indexPath, err := s.DatasetPublicationIndexPath(pub); err == nil {
			keepFiles[indexPath] = true
		}
	}
	for _, pub := range stale {
		if pub.BatchID == result.KeepBatch {
			continue
		}
		var files []string
		if shardPath, err := s.DatasetPublicationShardPath(pub); err == nil {
			files = append(files, shardPath)
		}
		if indexPath, err := s.DatasetPublicationIndexPath(pub); err == nil {
			files = append(files, indexPath)
		}
		for _, file := range files {
			if keepFiles[file] {
				continue
			}
			if err := os.Remove(file); err == nil {
				result.FilesDeleted++
			} else if !os.IsNotExist(err) {
				log.Warnf("supersede %s %s: remove cached publication file %s: %v", result.SchemaName, pub.BatchID, file, err)
			}
		}
	}
	return result, nil
}

// supersedeSourceBatchChunk evicts up to limit superseded CIDs under one lock
// hold + transaction, and tombstones the evicted records' engine rows before
// it releases the lock. staged == 0 means there was nothing left to evict.
func (s *FlatSQLStore) supersedeSourceBatchChunk(scope DatasetSupersedeResult, tableName string, limit int) (out supersedeChunkResult, err error) {
	release := s.lockWrite("supersedeSourceBatchChunk")
	held := time.Now()
	defer func() {
		out.held = time.Since(held)
		release()
	}()
	if err := s.closedErr(); err != nil {
		return out, err
	}
	if limit < 1 {
		limit = 1
	}

	tx, err := s.db.Begin()
	if err != nil {
		return out, fmt.Errorf("begin supersede chunk: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, stmt := range []string{
		`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_supersede_cids (cid TEXT PRIMARY KEY)`,
		`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_supersede_orphans (cid TEXT PRIMARY KEY)`,
		`DELETE FROM temp_sdn_supersede_cids`,
		`DELETE FROM temp_sdn_supersede_orphans`,
	} {
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(stmt)); err != nil {
			return out, fmt.Errorf("prepare supersede chunk tables: %w", err)
		}
	}

	// Stage from the two index ranges either side of the kept batch
	// (idx_sdn_record_source_tags_batch_cid). `batch_id <> ?` is not a range:
	// SQLite walks the group from its first batch and steps over every row of
	// the kept one — tens of thousands of index entries per chunk whenever the
	// kept batch id sorts first. batch_id is TEXT NOT NULL, so the two ranges
	// are exactly `<>`.
	for _, cmp := range []string{"<", ">"} {
		if out.staged >= int64(limit) {
			break
		}
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
			INSERT OR IGNORE INTO temp_sdn_supersede_cids (cid)
			SELECT cid FROM sdn_record_source_tags
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id `+cmp+` ?
			LIMIT ?
		`), scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch, int64(limit)-out.staged); err != nil {
			return out, fmt.Errorf("stage superseded cids: %w", err)
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM temp_sdn_supersede_cids`).Scan(&out.staged); err != nil {
			return out, fmt.Errorf("count staged superseded cids: %w", err)
		}
	}
	if out.staged == 0 {
		if err := tx.Commit(); err != nil {
			return out, fmt.Errorf("commit empty supersede chunk: %w", err)
		}
		committed = true
		return out, nil
	}

	// Every tag of the group except the kept batch's, for the staged CIDs: a
	// seek per CID on the (schema_name, cid, provider_id, source_name) prefix
	// of the tag table's key.
	tagsResult, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		  AND cid IN (SELECT cid FROM temp_sdn_supersede_cids)
	`), scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch)
	if err != nil {
		return out, fmt.Errorf("delete superseded source tags: %w", err)
	}
	out.tags, _ = tagsResult.RowsAffected()

	// Orphans: staged CIDs no tag of the schema names any more.
	if out.records, err = s.deleteStagedOrphansTx(tx, scope.SchemaName, tableName,
		"temp_sdn_supersede_cids", "temp_sdn_supersede_orphans"); err != nil {
		return out, fmt.Errorf("superseded records: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("commit supersede chunk: %w", err)
	}
	committed = true
	if out.records > 0 {
		if _, err := s.tombstoneStagedEngineRowsLocked(scope.SchemaName, "temp_sdn_supersede_orphans"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// deleteStagedOrphansTx is a chunk's orphan set, computed ONCE and used by
// every delete that needs it. It stages into the orphans table the CIDs of the
// staged table that no tag of the schema names any more — correlated, so each
// staged CID is one seek on the (schema_name, cid) prefix of the tag table's
// key (the primary key's, or idx_sdn_record_source_tags_unique on a migrated
// table), never the NOT IN list of every tag of the schema — then deletes
// those records from every (producer, standard) table and from the record
// index, and returns how many there were. Both tables are (cid TEXT PRIMARY
// KEY), the orphans table empty. Runs in the caller's transaction, under the
// store write lock; the caller tombstones the orphans' engine rows
// (tombstoneStagedEngineRowsLocked) after its commit.
func (s *FlatSQLStore) deleteStagedOrphansTx(tx *sql.Tx, schemaName, tableName, staged, orphans string) (int64, error) {
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		INSERT OR IGNORE INTO `+orphans+` (cid)
		SELECT staged.cid FROM `+staged+` staged
		WHERE NOT EXISTS (
			SELECT 1 FROM sdn_record_source_tags tags
			WHERE tags.schema_name = ? AND tags.cid = staged.cid
		)
	`), schemaName); err != nil {
		return 0, fmt.Errorf("stage orphans: %w", err)
	}
	var n int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + orphans).Scan(&n); err != nil {
		return 0, fmt.Errorf("count orphans: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	s.deleteRoutedMirrorsWhere(tx, tableName, `cid IN (SELECT cid FROM `+orphans+`)`)
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_index
		WHERE schema_name = ? AND cid IN (SELECT cid FROM `+orphans+`)
	`), schemaName); err != nil {
		return n, fmt.Errorf("delete orphaned index rows: %w", err)
	}
	return n, nil
}

// engineOrphanPredicate is the anti-join tombstoneOrphanedEngineRowsLocked
// runs, over residency alias `e`: the record has no index row under ANY
// spelling of the schema.
func engineOrphanPredicate(schemaName string) (string, []any) {
	aliases := engineSchemaNameAliases(schemaName)
	args := make([]any, 0, len(aliases))
	for _, alias := range aliases {
		args = append(args, alias)
	}
	return fmt.Sprintf(`NOT EXISTS (
			SELECT 1 FROM sdn_record_index idx
			WHERE idx.schema_name IN (%s) AND idx.cid = e.cid
		)`, strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")), args
}

// tombstoneStagedEngineRowsLocked is tombstoneOrphanedEngineRowsLocked
// restricted to a chunk's orphans (the orphans table of
// deleteStagedOrphansTx), whose index rows the chunk has just deleted. Caller
// holds s.mu for writing.
func (s *FlatSQLStore) tombstoneStagedEngineRowsLocked(schemaName, orphans string) (int, error) {
	if s.engineDB == nil {
		return 0, nil
	}
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return 0, nil
	}
	orphaned, orphanArgs := engineOrphanPredicate(schemaName)
	return s.tombstoneEngineResidencyWhereLocked(schemaName, binding.Table,
		`e.cid IN (SELECT cid FROM `+orphans+`) AND `+orphaned, orphanArgs)
}

// tombstoneEngineResidencyWhereLocked tombstones the residency rows of
// schemaName matching where (over alias `e`), with the ledger deletes in one
// transaction rather than one commit per row. The transaction commits even
// when a tombstone fails part way: rows tombstoned so far leave the ledger,
// rows that failed stay in it for the next sweep — tombstoneResidencyRowsLocked's
// contract, kept the same way by the autocommit-per-row sweep it replaces.
// Caller holds s.mu for writing.
func (s *FlatSQLStore) tombstoneEngineResidencyWhereLocked(schemaName, table, where string, whereArgs []any) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin engine tombstones for %s: %w", schemaName, err)
	}
	args := append([]any{engineLedgerSchema(schemaName)}, whereArgs...)
	rows, err := tx.Query(`
		SELECT e.cid, e.source, e.seq FROM sdn_engine_rows e
		WHERE e.schema_name = ? AND `+where, args...)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("find orphaned engine rows for %s: %w", schemaName, err)
	}
	var orphans []engineResidencyRow
	for rows.Next() {
		var row engineResidencyRow
		if err := rows.Scan(&row.cid, &row.source, &row.seq); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return 0, err
		}
		orphans = append(orphans, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if len(orphans) == 0 {
		_ = tx.Rollback()
		return 0, nil
	}
	removed, tombErr := s.tombstoneResidencyRowsLocked(schemaName, table, orphans, tx)
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return removed, fmt.Errorf("commit engine tombstones for %s: %w", schemaName, err)
	}
	return removed, tombErr
}

// sweepOrphanedEngineRowsPaged is tombstoneOrphanedEngineRowsLocked in pages
// of supersedeEngineSweepPage residency rows, one store-lock hold per page,
// named site; noteHold sees every hold. The residency ledger's key is
// (schema_name, cid), so a page is one index range and the next page starts
// after the last cid of this one.
func (s *FlatSQLStore) sweepOrphanedEngineRowsPaged(site, schemaName string, noteHold func(time.Duration)) error {
	if s.engineDB == nil {
		return nil
	}
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return nil
	}
	ledgerSchema := engineLedgerSchema(schemaName)
	orphaned, orphanArgs := engineOrphanPredicate(schemaName)
	after := ""
	for {
		last := true
		bound := ""
		held, err := s.holdWrite(site, func() error {
			if err := s.closedErr(); err != nil {
				return err
			}
			// The page's closed upper bound: the cid supersedeEngineSweepPage
			// rows on. None means this is the last page.
			err := s.db.QueryRow(fmt.Sprintf(`
				SELECT cid FROM sdn_engine_rows
				WHERE schema_name = ? AND cid > ?
				ORDER BY cid LIMIT 1 OFFSET %d`, supersedeEngineSweepPage-1), ledgerSchema, after).Scan(&bound)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("page engine residency of %s: %w", schemaName, err)
			}
			where := `e.cid > ? AND `
			args := []any{after}
			if bound != "" {
				where += `e.cid <= ? AND `
				args = append(args, bound)
			}
			if _, err := s.tombstoneEngineResidencyWhereLocked(schemaName, binding.Table, where+orphaned, append(args, orphanArgs...)); err != nil {
				return err
			}
			last = bound == ""
			return nil
		})
		noteHold(held)
		if err != nil {
			return err
		}
		if last {
			return nil
		}
		if bound <= after {
			return fmt.Errorf("engine residency sweep of %s: page cursor did not advance past %q", schemaName, after)
		}
		after = bound
	}
}

// rebuildSupersededSummaryLanes rebuilds the summary rows of the lanes a
// supersede evicted — every batch of its (schema, provider, source) group
// except the kept one — one lane per store-lock hold. See the package comment
// for why no other lane is touched.
func (s *FlatSQLStore) rebuildSupersededSummaryLanes(scope *DatasetSupersedeResult) error {
	var lanes []sourceSummaryLane
	err := func() error {
		defer s.lockRead("dataset supersede: summary lanes")()
		rows, err := s.db.Query(`
			SELECT DISTINCT batch_id FROM sdn_record_source_summary
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		`, scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch)
		if err != nil {
			return fmt.Errorf("list superseded summary lanes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			lane := sourceSummaryLane{SchemaName: scope.SchemaName, ProviderID: scope.ProviderID, SourceName: scope.SourceName}
			if err := rows.Scan(&lane.BatchID); err != nil {
				return fmt.Errorf("scan superseded summary lane: %w", err)
			}
			lanes = append(lanes, lane)
		}
		return rows.Err()
	}()
	if err != nil {
		return err
	}
	for _, lane := range lanes {
		held, err := s.holdWrite("dataset supersede: source summary", func() error {
			return s.rebuildSourceSummaryLane(lane)
		})
		scope.noteHold(held)
		if err != nil {
			return err
		}
	}
	return nil
}
