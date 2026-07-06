package storage

// dataset_supersede.go — pinned-dataset supersede eviction (gateway loop G.4,
// docs/gateway-api.md §10).
//
// A gateway.pin replicates ONE dataset generation per (provider, standard):
// each new publication batch SUPERSEDES the previous pin — the old batch's
// records are evicted from the store rather than accumulated. Eviction is the
// batch-keyed inverse of the shard import path: source-tag rows for other
// batches of the (provider, source, schema) group are deleted, and any record
// whose CID no longer carries a source tag is removed from the legacy
// per-standard table, the routed (producer, standard) tables, and the global
// record index. Records SHARED with the kept batch (unchanged content, same
// CID, re-tagged by the newer import) survive untouched.
//
// The work is CHUNKED: each chunk deletes at most supersedeChunkSize CIDs
// under one store write lock + one transaction, releasing the lock between
// chunks so readers interleave — the same lock-window discipline the
// 2026-07-06 production blackout forced onto the import path
// (storeWriteChunkSize). Each chunk commits atomically; the operation is
// idempotent, so an interrupted supersede converges on the next evaluation.
//
// NOT touched, deliberately:
//   - superseded publication METADATA rows (sdn_dataset_shard_publications):
//     they are the few-hundred-byte provenance record AND the trusted-peer
//     catch-up dedup key (datasetShardPublicationAlreadyCached) — deleting
//     them would make the next catch-up cycle re-materialize the very batch
//     the supersede just evicted. Only their cached shard/index FILES are
//     removed; a batch with rows but no files is simply not servable;
//   - append-only FlatSQL stream files (payload bytes are never rewritten;
//     evicted records merely lose their control rows — same rule as every
//     other delete path in this store);
//   - the in-memory engine hot window (a bounded cache rebuilt at boot;
//     tombstoning it record-by-record needs a cid->vtab-sequence mapping the
//     control tables do not keep — see docs/gateway-api.md §10);
//   - the provider's OWN publication history (callers must not supersede
//     the node's own provider identity; the node-level hook skips self).

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// supersedeChunkSize bounds the CIDs evicted per store-lock window. Larger
// than storeWriteChunkSize (128) because eviction rows are index-only work
// (no stream appends, no FlatBuffer verification), but still small enough
// that a lock hold stays in the low tens of milliseconds.
const supersedeChunkSize = 2048

// DatasetSupersedeResult reports one supersede eviction.
type DatasetSupersedeResult struct {
	SchemaName     string
	ProviderID     string
	SourceName     string
	KeepBatch      string
	TagsDeleted    int64
	RecordsDeleted int64
	FilesDeleted   int
}

// SupersedeSourceBatches evicts every batch EXCEPT keepBatch for one
// (schema, provider, source) group: superseded source-tag rows, orphaned
// records (all backing tables + the record index), and the superseded
// batches' cached shard/index files. Publication metadata rows are KEPT (see
// the package comment). Chunked per the package comment.
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

	evictedAny := false
	for {
		tags, records, err := s.supersedeSourceBatchChunk(result, tableName)
		if err != nil {
			return result, err
		}
		if tags == 0 && records == 0 {
			break
		}
		evictedAny = true
		result.TagsDeleted += tags
		result.RecordsDeleted += records
	}

	if evictedAny {
		s.mu.Lock()
		summaryErr := s.rebuildSourceSummaryForSchema(result.SchemaName, tableName)
		s.mu.Unlock()
		if summaryErr != nil {
			return result, summaryErr
		}
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

// supersedeSourceBatchChunk evicts up to supersedeChunkSize superseded CIDs
// under one lock hold + transaction. Returns (tagsDeleted, recordsDeleted).
func (s *FlatSQLStore) supersedeSourceBatchChunk(scope DatasetSupersedeResult, tableName string) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_supersede_cids (cid TEXT PRIMARY KEY)`); err != nil {
		return 0, 0, fmt.Errorf("create supersede cid table: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM temp_sdn_supersede_cids`); err != nil {
		return 0, 0, fmt.Errorf("clear supersede cid table: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO temp_sdn_supersede_cids (cid)
		SELECT cid FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		LIMIT ?
	`, scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch, supersedeChunkSize); err != nil {
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

	tagsResult, err := tx.Exec(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
		  AND cid IN (SELECT cid FROM temp_sdn_supersede_cids)
	`, scope.SchemaName, scope.ProviderID, scope.SourceName, scope.KeepBatch)
	if err != nil {
		return 0, 0, fmt.Errorf("delete superseded source tags: %w", err)
	}
	tagsDeleted, _ := tagsResult.RowsAffected()

	// Orphans: staged CIDs with no surviving source tag for the schema.
	var recordsDeleted int64
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM temp_sdn_supersede_cids
		WHERE cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)
	`, scope.SchemaName).Scan(&recordsDeleted); err != nil {
		return 0, 0, fmt.Errorf("count orphaned superseded records: %w", err)
	}
	if recordsDeleted > 0 {
		if legacyExists, exErr := s.tableExists(tableName); exErr == nil && legacyExists {
			if _, err := tx.Exec(fmt.Sprintf(`
				DELETE FROM %s
				WHERE cid IN (SELECT cid FROM temp_sdn_supersede_cids)
				  AND NOT EXISTS (
					SELECT 1 FROM sdn_record_source_tags tags
					WHERE tags.schema_name = ? AND tags.cid = %s.cid
				  )
			`, tableName, tableName), scope.SchemaName); err != nil {
				return 0, 0, fmt.Errorf("delete orphaned superseded records: %w", err)
			}
		}
		s.deleteRoutedMirrorsWhere(tx, tableName,
			`cid IN (SELECT cid FROM temp_sdn_supersede_cids) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
			scope.SchemaName)
		if _, err := tx.Exec(`
			DELETE FROM sdn_record_index
			WHERE schema_name = ?
			  AND cid IN (SELECT cid FROM temp_sdn_supersede_cids)
			  AND NOT EXISTS (
				SELECT 1 FROM sdn_record_source_tags tags
				WHERE tags.schema_name = sdn_record_index.schema_name
				  AND tags.cid = sdn_record_index.cid
			  )
		`, scope.SchemaName); err != nil {
			return 0, 0, fmt.Errorf("delete orphaned superseded index rows: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit supersede chunk: %w", err)
	}
	committed = true
	return tagsDeleted, recordsDeleted, nil
}
