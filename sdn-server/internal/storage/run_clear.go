package storage

// run_clear.go — operator-initiated single-batch eviction ("clear this run",
// App 2 run controls). The batch-scoped INVERSE of dataset_supersede.go's
// SupersedeSourceBatches (which evicts every batch EXCEPT one): ClearSourceBatch
// evicts exactly ONE (schema, source, batch) group's source-tag rows plus any
// record thereby orphaned. It reuses the supersede semantics verbatim:
//
//   - a record whose CID is ALSO tagged by another (kept) batch SURVIVES —
//     only its tag rows for the cleared batch are removed;
//   - a record with no surviving source tag for the schema is deleted from the
//     legacy per-standard table, the routed (producer, standard) tables, and
//     the global record index;
//   - append-only FlatSQL stream files are never rewritten (payload bytes
//     merely lose their control rows — same rule as every delete path here);
//   - the work is CHUNKED under short store-lock windows (supersedeChunkSize)
//     so readers interleave, and each chunk commits atomically — an
//     interrupted clear converges on retry.
//
// provider_id is optional: when empty the clear spans every provider tag row
// of the (schema, source, batch) group (the stats surface groups runs by all
// four keys, but a batch id is already provider-scoped in practice).
//
// Record-catalog journal: NO event is appended — the per-record Delete() path
// sets that precedent (only the gateway supersede flow journals a SourceKeep,
// because pin catch-up replays depend on it). A hot-window replay may briefly
// resurrect cleared entries in the bounded in-memory engine cache; the SQL
// control tables — the source of truth for /api/v1/stats and the record
// index — are authoritative and converge immediately.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// ClearSourceBatchResult reports one run-clear eviction.
type ClearSourceBatchResult struct {
	SchemaName     string `json:"schema"`
	ProviderID     string `json:"provider_id,omitempty"`
	SourceName     string `json:"source_name"`
	BatchID        string `json:"batch_id"`
	TagsDeleted    int64  `json:"tags_deleted"`
	RecordsDeleted int64  `json:"records_deleted"`
}

// ClearSourceBatch evicts one (schema, [provider,] source, batch) group:
// its source-tag rows and any record orphaned by their removal. Records
// shared with other kept batches survive (see the file comment).
func (s *FlatSQLStore) ClearSourceBatch(schemaName, providerID, sourceName, batchID string) (ClearSourceBatchResult, error) {
	result := ClearSourceBatchResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		BatchID:    strings.TrimSpace(batchID),
	}
	if s == nil {
		return result, errors.New("store is required")
	}
	if result.SchemaName == "" {
		return result, errors.New("schema name is required")
	}
	if result.SourceName == "" {
		return result, errors.New("source name is required")
	}
	if result.BatchID == "" {
		return result, errors.New("batch id is required")
	}
	if err := s.requireWritable("clear source batch"); err != nil {
		return result, err
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	clearedAny := false
	for {
		tags, records, err := s.clearSourceBatchChunk(result, tableName)
		if err != nil {
			return result, err
		}
		if tags == 0 && records == 0 {
			break
		}
		clearedAny = true
		result.TagsDeleted += tags
		result.RecordsDeleted += records
	}

	if clearedAny {
		s.mu.Lock()
		summaryErr := s.rebuildSourceSummaryForSchema(result.SchemaName, tableName)
		s.mu.Unlock()
		if summaryErr != nil {
			return result, summaryErr
		}
	}
	return result, nil
}

// clearSourceBatchChunk evicts up to supersedeChunkSize CIDs of the cleared
// batch under one lock hold + transaction. Returns (tagsDeleted, recordsDeleted).
func (s *FlatSQLStore) clearSourceBatchChunk(scope ClearSourceBatchResult, tableName string) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Tag scope for the CLEARED batch (batch_id = ?, not <> like supersede).
	where := `schema_name = ? AND source_name = ? AND batch_id = ?`
	args := []interface{}{scope.SchemaName, scope.SourceName, scope.BatchID}
	if scope.ProviderID != "" {
		where += ` AND provider_id = ?`
		args = append(args, scope.ProviderID)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin clear chunk: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_runclear_cids (cid TEXT PRIMARY KEY)`)); err != nil {
		return 0, 0, fmt.Errorf("create clear cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`DELETE FROM temp_sdn_runclear_cids`)); err != nil {
		return 0, 0, fmt.Errorf("reset clear cid table: %w", err)
	}
	stageArgs := append(append([]interface{}{}, args...), supersedeChunkSize)
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		INSERT OR IGNORE INTO temp_sdn_runclear_cids (cid)
		SELECT cid FROM sdn_record_source_tags
		WHERE `+where+`
		LIMIT ?
	`), stageArgs...); err != nil {
		return 0, 0, fmt.Errorf("stage cleared cids: %w", err)
	}
	var staged int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM temp_sdn_runclear_cids`).Scan(&staged); err != nil {
		return 0, 0, fmt.Errorf("count staged cleared cids: %w", err)
	}
	if staged == 0 {
		if err := tx.Commit(); err != nil {
			return 0, 0, fmt.Errorf("commit empty clear chunk: %w", err)
		}
		committed = true
		return 0, 0, nil
	}

	tagsResult, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_tags
		WHERE `+where+`
		  AND cid IN (SELECT cid FROM temp_sdn_runclear_cids)
	`), args...)
	if err != nil {
		return 0, 0, fmt.Errorf("delete cleared source tags: %w", err)
	}
	tagsDeleted, _ := tagsResult.RowsAffected()

	// Orphans: staged CIDs with NO surviving source tag for the schema —
	// records shared with a kept batch keep a tag row and are skipped here.
	var recordsDeleted int64
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM temp_sdn_runclear_cids
		WHERE cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)
	`, scope.SchemaName).Scan(&recordsDeleted); err != nil {
		return 0, 0, fmt.Errorf("count orphaned cleared records: %w", err)
	}
	if recordsDeleted > 0 {
		if legacyExists, exErr := s.tableExists(tableName); exErr == nil && legacyExists {
			if _, err := tx.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
				DELETE FROM %s
				WHERE cid IN (SELECT cid FROM temp_sdn_runclear_cids)
				  AND NOT EXISTS (
					SELECT 1 FROM sdn_record_source_tags tags
					WHERE tags.schema_name = ? AND tags.cid = %s.cid
				  )
			`, tableName, tableName)), scope.SchemaName); err != nil {
				return 0, 0, fmt.Errorf("delete orphaned cleared records: %w", err)
			}
		}
		s.deleteRoutedMirrorsWhere(tx, tableName,
			`cid IN (SELECT cid FROM temp_sdn_runclear_cids) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
			scope.SchemaName)
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM sdn_record_index
			WHERE schema_name = ?
			  AND cid IN (SELECT cid FROM temp_sdn_runclear_cids)
			  AND NOT EXISTS (
				SELECT 1 FROM sdn_record_source_tags tags
				WHERE tags.schema_name = sdn_record_index.schema_name
				  AND tags.cid = sdn_record_index.cid
			  )
		`), scope.SchemaName); err != nil {
			return 0, 0, fmt.Errorf("delete orphaned cleared index rows: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit clear chunk: %w", err)
	}
	committed = true
	return tagsDeleted, recordsDeleted, nil
}
