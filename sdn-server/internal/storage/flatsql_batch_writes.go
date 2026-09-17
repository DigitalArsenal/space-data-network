package storage

// flatsql_batch_writes.go — the batch ingest path's PER-CHUNK statements.
//
// WHY THIS FILE EXISTS. Every SQL statement this store issues is one handoff
// to the engine's dedicated locked OS thread (wasmrt.Module.RunOnExecThread:
// "the DISPATCH was the work"), and database/sql's Exec is TWO of them — the
// statement, then the `SELECT last_insert_rowid(), changes()` the driver adds
// to build its Result. storeBatchChunk issued that per RECORD: an existence
// probe, an insert into the routed table, an upsert into the record index, and
// with source tags a tag insert and a summary increment. A 64-record window
// therefore paid ~320 handoffs to write 64 rows, and a CPU profile of the write
// path spent 48 % of its samples in pthread_cond_signal/pthread_cond_wait —
// thread handoff, not SQL and not I/O.
//
// Nothing here changes what the work MEANS. The same rows land, in the same
// order, with the same dedupe, supersede and index semantics; they just travel
// to the engine in a handful of multi-row statements per window instead of
// five per record. The buffer is flushed by storeBatchChunk before any
// statement that must SEE the rows written earlier in the same window (a
// repeat-CID mirror, a supersede probe), which is what keeps the batched path
// row-for-row identical to the per-record one.

import (
	"fmt"
	"strings"
)

// batchStatementRows bounds how many rows one multi-row statement carries.
// The engine accepts far more (measured: a 1024-row, 3072-parameter INSERT
// executes fine), but the parameter blob is materialized in guest memory in
// one piece, and a routed-table row carries the whole FlatBuffer record — so
// this is the knob that keeps ONE statement's allocation bounded while still
// collapsing a whole window into a couple of handoffs.
const batchStatementRows = 128

// batchProbeCIDs bounds how many CIDs one IN-list probe carries. Measured on a
// 100,000-row sdn_record_index: 128 per-record probes cost 7.8 ms, one IN-list
// probe of the same 128 CIDs cost 1.0 ms, and the miss case that ingest
// actually runs (128 CIDs that are absent) cost 0.34 ms against 6.5 ms. The
// engine seeks the (schema_name, cid) primary key for the IN list; it does not
// scan.
const batchProbeCIDs = 256

// pendingIndexRow is one queued sdn_record_index upsert. `data` is the
// PLAINTEXT record: index extraction and full-text indexing always run against
// plaintext, never the sealed bytes that land in the routed table.
type pendingIndexRow struct {
	cid  string
	data []byte
	ts   int64
}

// chunkWriteBuffer accumulates one storeBatchChunk window's new-record rows so
// they land as multi-row statements. It is NOT a write-behind cache: the
// caller flushes it before anything that reads these rows back, and always
// before the transaction commits.
type chunkWriteBuffer struct {
	schemaName  string
	routedTable string
	textIndex   *fullTextIndexState
	// tags is the window's source provenance, identical for every record in
	// the window (storeBatchChunk takes one *SourceTags for the whole call);
	// nil for the untagged StoreBatch path.
	tags   *SourceTags
	routed []storedRecord
	index  []pendingIndexRow
}

func (b *chunkWriteBuffer) add(rec storedRecord, plaintext []byte) {
	b.routed = append(b.routed, rec)
	b.index = append(b.index, pendingIndexRow{cid: rec.cid, data: plaintext, ts: rec.timestamp})
}

// flush writes every queued row and returns how many routed rows LANDED, so
// the caller's `inserted` counts rows on disk rather than rows queued. Ordering
// is the per-record path's ordering held exactly: routed rows first, then index
// rows, then the full-text rows that JOIN the index rows, then the source tags
// that need the routed rowids.
func (b *chunkWriteBuffer) flush(exec sqlQueryExecer) (int, error) {
	if len(b.routed) == 0 {
		return 0, nil
	}
	routed := b.routed
	index := b.index
	// Cleared BEFORE the writes: a flush that fails must not leave rows queued
	// for a second attempt. The caller's transaction rolls back the whole
	// window anyway, and re-issuing an insert that already applied would turn
	// one failure into a duplicate-CID constraint error that hides it.
	b.routed = nil
	b.index = nil

	if err := insertSchemaMetadataBatch(exec, b.routedTable, routed); err != nil {
		return 0, fmt.Errorf("store %s batch of %d record(s): %w", b.schemaName, len(routed), err)
	}

	// Index failures NEVER fail a write: the index is derived state and a
	// record that cannot be indexed is still a record (see
	// upsertRecordIndexExec). One bad row must not cost the whole window its
	// index rows either, so a failed batch falls back to the per-record
	// statement that names the record it could not index.
	if err := upsertRecordIndexBatch(exec, b.schemaName, index); err != nil {
		log.Warnf("Failed to index %s batch of %d record(s) in one statement (%v); falling back per record", b.schemaName, len(index), err)
		for _, row := range index {
			if err := upsertRecordIndexExec(exec, b.schemaName, row.cid, row.ts, row.data, b.textIndex); err != nil {
				log.Warnf("Failed to index batch %s record %s: %v", b.schemaName, row.cid[:16]+"...", err)
			}
		}
	} else if b.textIndex != nil {
		// The full-text statement reads the row it indexes out of
		// sdn_record_index, so it can only run once those rows exist. Per
		// record because the indexed text is per record; a no-op when the
		// schema has no initialized full-text index (the default).
		for _, row := range index {
			if err := upsertFullTextExec(exec, b.textIndex, b.schemaName, row.cid, row.data); err != nil {
				log.Warnf("Failed to index batch %s record %s: %v", b.schemaName, row.cid[:16]+"...", err)
			}
		}
	}

	if b.tags != nil {
		if err := b.writeSourceTags(exec, routed); err != nil {
			return 0, err
		}
	}
	return len(routed), nil
}

// writeSourceTags writes the window's provenance rows and ONE aggregated
// summary increment. The per-record path wrote one tag row and one summary
// increment per record; both are arithmetic that aggregates exactly
// (count += n, bytes += sum, max_rowid = MAX over the window), so the derived
// summary lands on the same values it would have reached one record at a time.
func (b *chunkWriteBuffer) writeSourceTags(exec sqlQueryExecer, routed []storedRecord) error {
	tags := normalizeSourceTags(*b.tags)
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	cids := make([]string, len(routed))
	var totalBytes int64
	for i, rec := range routed {
		if strings.TrimSpace(rec.cid) == "" {
			return fmt.Errorf("cid is required")
		}
		cids[i] = rec.cid
		totalBytes += int64(len(rec.data))
	}

	// The summary's max_rowid is the datasync high-water mark, so it must be a
	// REAL rowid. The per-record path read it back from the insert's
	// LastInsertId; one lookup for the whole window replaces those.
	rowIDs, err := routedRowIDsByCID(exec, b.routedTable, cids)
	if err != nil {
		return err
	}
	var maxRowID int64
	for _, cid := range cids {
		rowID, ok := rowIDs[cid]
		if !ok {
			// The routed insert just succeeded for this CID, so a missing
			// rowid means the row is not where this code thinks it is —
			// silently writing a summary with a wrong high-water mark would
			// strand every peer syncing from this node past that cursor.
			return fmt.Errorf("source tags: no rowid for %s record %s in %s", b.schemaName, cid, b.routedTable)
		}
		if rowID > maxRowID {
			maxRowID = rowID
		}
	}

	if err := insertSourceTagsBatch(exec, b.schemaName, cids, tags); err != nil {
		return err
	}
	return incrementSourceSummaryBy(exec, b.schemaName, tags, int64(len(cids)), totalBytes, maxRowID)
}

// recordIndexPresence returns which of cids already have a row in the global
// record index — the chunk-wide form of the per-record
// "SELECT 1 FROM sdn_record_index WHERE schema_name=? AND cid=?" probe.
func recordIndexPresence(exec sqlQueryExecer, schemaName string, cids []string) (map[string]struct{}, error) {
	present := make(map[string]struct{}, len(cids))
	if len(cids) == 0 {
		return present, nil
	}
	// Duplicate CIDs within one window are legal (the same bytes twice) and
	// must be asked about once.
	unique := make([]string, 0, len(cids))
	seen := make(map[string]struct{}, len(cids))
	for _, cid := range cids {
		if _, dup := seen[cid]; dup {
			continue
		}
		seen[cid] = struct{}{}
		unique = append(unique, cid)
	}

	for start := 0; start < len(unique); start += batchProbeCIDs {
		end := start + batchProbeCIDs
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]
		args := make([]any, 0, len(batch)+1)
		args = append(args, schemaName)
		for _, cid := range batch {
			args = append(args, cid)
		}
		query := `SELECT cid FROM sdn_record_index WHERE schema_name = ? AND cid IN (` + placeholderList(len(batch)) + `)`
		rows, err := exec.Query(query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			present[cid] = struct{}{}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return present, nil
}

// routedRowIDsByCID reads the rowids of just-inserted routed rows, one
// statement per batchProbeCIDs CIDs.
func routedRowIDsByCID(exec sqlQueryExecer, tableName string, cids []string) (map[string]int64, error) {
	out := make(map[string]int64, len(cids))
	for start := 0; start < len(cids); start += batchProbeCIDs {
		end := start + batchProbeCIDs
		if end > len(cids) {
			end = len(cids)
		}
		batch := cids[start:end]
		args := make([]any, 0, len(batch))
		for _, cid := range batch {
			args = append(args, cid)
		}
		query := fmt.Sprintf(`SELECT cid, rowid FROM %s WHERE cid IN (%s)`, tableName, placeholderList(len(batch)))
		rows, err := exec.Query(query, args...)
		if err != nil {
			return nil, fmt.Errorf("read rowids from %s: %w", tableName, err)
		}
		for rows.Next() {
			var cid string
			var rowID int64
			if err := rows.Scan(&cid, &rowID); err != nil {
				rows.Close()
				return nil, err
			}
			out[cid] = rowID
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// insertSchemaMetadataBatch is insertSchemaMetadataReturningRowID for many
// records: the same columns, the same values, the same plain INSERT (a
// duplicate CID is a bug the caller's existence probe already ruled out, and
// must stay an error rather than a silently dropped record).
func insertSchemaMetadataBatch(exec sqlExecer, tableName string, recs []storedRecord) error {
	for start := 0; start < len(recs); start += batchStatementRows {
		end := start + batchStatementRows
		if end > len(recs) {
			end = len(recs)
		}
		batch := recs[start:end]
		args := make([]any, 0, len(batch)*8)
		for _, rec := range batch {
			args = append(args, schemaMetadataArgs(rec)...)
		}
		insertSQL := fmt.Sprintf(`
		INSERT INTO %s (
			cid, peer_id, timestamp, data, record_length, signature_hex, supersede_key, created_at
		)
		VALUES %s
	`, tableName, valueTupleList(len(batch), 8))
		if _, err := exec.Exec(insertSQL, args...); err != nil {
			return err
		}
	}
	return nil
}

// upsertRecordIndexBatch is upsertRecordIndexExec's index write for many
// records, with the identical ON CONFLICT clause — a repeat CID still keeps
// its rowid, which is the wire-visible datasync cursor deployed peers hold.
// Full-text indexing is NOT done here; it reads back the rows this statement
// writes, so the caller runs it after.
func upsertRecordIndexBatch(exec sqlExecer, schemaName string, rows []pendingIndexRow) error {
	for start := 0; start < len(rows); start += batchStatementRows {
		end := start + batchStatementRows
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		args := make([]any, 0, len(batch)*9)
		for _, row := range batch {
			args = append(args, recordIndexArgs(schemaName, row.cid, row.ts, row.data)...)
		}
		sqlText := `
		INSERT INTO sdn_record_index (
			schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
		)
		VALUES ` + valueTupleList(len(batch), 9) + recordIndexConflictClause
		if _, err := exec.Exec(sqlText, args...); err != nil {
			return fmt.Errorf("failed to upsert index rows: %w", err)
		}
	}
	return nil
}

// insertSourceTagsBatch writes one provenance row per CID. Every row carries
// the SAME tags — one storeBatchChunk window is one (provider, source, batch).
func insertSourceTagsBatch(exec sqlExecer, schemaName string, cids []string, tags SourceTags) error {
	for start := 0; start < len(cids); start += batchStatementRows {
		end := start + batchStatementRows
		if end > len(cids) {
			end = len(cids)
		}
		batch := cids[start:end]
		args := make([]any, 0, len(batch)*9)
		for _, cid := range batch {
			args = append(args, schemaName, cid, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey)
		}
		insertSQL := `
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
		VALUES ` + valueTupleList(len(batch), 9)
		if _, err := exec.Exec(insertSQL, args...); err != nil {
			return fmt.Errorf("failed to insert source tags: %w", err)
		}
	}
	return nil
}

// placeholderList returns "?,?,?" for n parameters.
func placeholderList(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// valueTupleList returns "(?,?),(?,?)" — rows tuples of width columns each.
func valueTupleList(rows, width int) string {
	if rows <= 0 || width <= 0 {
		return ""
	}
	tuple := "(" + placeholderList(width) + ")"
	return tuple + strings.Repeat(","+tuple, rows-1)
}

// forgetPresence drops CIDs whose sdn_record_index row a supersede just
// DELETED (supersedeInProducerTableTx -> removeRecordIfOrphanedTx).
//
// The window's presence map is probed ONCE and then maintained in Go, so a
// delete performed INSIDE the window has to be reflected there. The per-record
// path re-probed the index for every record and so saw the row go away; a map
// that only ever grows keeps claiming the CID exists. The next record carrying
// that CID would then take the repeat branch, whose mirror reads the row back
// out of the read source (producer_standard_tables.go), finds nothing, warns
// and returns nil — dropping the record with no error, leaving an orphan
// source-tag row and double-counting the source summary.
//
// Only $CAT has a supersede rule (recordSupersedeKey returns "" for every
// other standard), so this can only ever fire there — but there it is a
// last-writer-wins violation in the satellite catalog, which no later pass
// recovers because the control tables are the source of truth.
func forgetPresence(present map[string]struct{}, gone []string) {
	for _, cid := range gone {
		delete(present, cid)
	}
}

// dropEnginePending removes queued engine-mirror rows whose CID a supersede
// just took away.
//
// The engine mirror is applied AFTER the control transaction commits, and in a
// fixed order: tombstone everything superseded, then ingest everything queued.
// A CID superseded while it was still only QUEUED is therefore tombstoned
// against an engine that has never seen it — a no-op — and then ingested
// anyway, leaving the engine vtab holding a row the control tables superseded
// away. (Pre-dates the batched writes: storeBatchChunk has always queued the
// mirror for the whole window.) Dropping the entry at the moment of the
// supersede reproduces the per-record path, where each record's mirror and its
// tombstone happen at that record's own turn.
func dropEnginePending(pending []engineIngest, gone []string) []engineIngest {
	if len(pending) == 0 || len(gone) == 0 {
		return pending
	}
	drop := make(map[string]struct{}, len(gone))
	for _, cid := range gone {
		drop[cid] = struct{}{}
	}
	kept := pending[:0]
	for _, p := range pending {
		if _, superseded := drop[p.cid]; superseded {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}
