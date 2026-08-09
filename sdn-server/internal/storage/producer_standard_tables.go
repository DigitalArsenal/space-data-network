package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var producerStandardIdentRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// sqlQueryExecer is satisfied by *sql.DB and *sql.Tx — the routed mirror runs
// inside either the plain write path or a batch transaction.
type sqlQueryExecer interface {
	sqlExecer
	QueryRow(query string, args ...any) *sql.Row
}

func isSafeIdentifier(s string) bool { return producerStandardIdentRe.MatchString(s) }

// sanitizeProducerID maps a producer identity (libp2p peer ID or hex public key)
// to a SQL-identifier-safe token. Peer IDs (base58) and public keys (hex) are
// already alphanumeric; any other byte is replaced with '_' so the resulting
// table name is always a valid identifier and never an injection vector.
func sanitizeProducerID(producerID string) string {
	trimmed := strings.TrimSpace(producerID)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ProducerStandardTableName derives the physical table name for records from a
// given producer and standard (schema). Records are stored per (producer,
// standard) rather than per standard alone, so a node keeps each publisher's
// records in a separate table while cross-table SQL can still span producers.
//
// The standard component reuses sds.SchemaNameToTable (validated, e.g.
// "OMM.fbs" -> "OMM"); the producer component is the sanitized peer ID / public
// key. Format:
//
//	sds_p_<producer>__<standard>
func ProducerStandardTableName(producerID, schemaName string) (string, error) {
	standard, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return "", err
	}
	producer := sanitizeProducerID(producerID)
	if producer == "" {
		return "", fmt.Errorf("producer id is required for (producer, standard) table routing")
	}
	return fmt.Sprintf("sds_p_%s__%s", producer, standard), nil
}

// ensureProducerStandardTable computes the (producer, standard) table name and
// creates the table on demand (idempotently) using the canonical schema-record
// layout. A new producer therefore materializes its own tables the first time
// it publishes, with no up-front schema registration.
func (s *FlatSQLStore) ensureProducerStandardTable(producerID, schemaName string) (string, error) {
	tableName, err := ProducerStandardTableName(producerID, schemaName)
	if err != nil {
		return "", err
	}
	exists, err := s.tableExists(tableName)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := s.createSchemaMetadataTable(tableName); err != nil {
			return "", fmt.Errorf("create (producer, standard) table %s: %w", tableName, err)
		}
	}
	return tableName, nil
}

// routedProducerID maps a record's peer identity to the producer token used
// for (producer, standard) routing. v1 stores are routed-only, so records
// without a peer identity land under the reserved "unattributed" producer
// rather than being dropped.
func routedProducerID(peerID string) string {
	if strings.TrimSpace(peerID) == "" {
		return "unattributed"
	}
	return peerID
}

// mirrorRoutedRecord writes the (producer, standard) metadata row for a record
// whose payload already lives in the shared FlatSQL stream. Used by the
// repeat-CID path (the content row already exists under some producer; this
// records that THIS producer also published it). Best-effort: a repeat-CID
// mirror failure must never fail the caller. Callers hold s.mu.
func (s *FlatSQLStore) mirrorRoutedRecord(exec sqlExecer, schemaName, cid, peerID string, timestamp int64, streamPath string, streamOffset, recordLength int64, signature []byte) {
	tableName, err := s.ensureProducerStandardTable(routedProducerID(peerID), schemaName)
	if err != nil {
		log.Warnf("Routed mirror: ensure (producer, standard) table for %s/%s: %v", peerID, schemaName, err)
		return
	}
	if err := insertSchemaMetadata(exec, tableName, cid, peerID, timestamp, streamPath, streamOffset, recordLength, signature, timestamp); err != nil {
		log.Warnf("Routed mirror: insert into %s for %s: %v", tableName, cid[:16]+"...", err)
	}
}

// mirrorRoutedRecordFromExisting records a repeat CID (possibly from a
// different producer) in that producer's (producer, standard) table: the
// stream coordinates are read from the record read source (any table already
// holding the content row — routed, or legacy on pre-flip databases).
// Callers hold s.mu.
func (s *FlatSQLStore) mirrorRoutedRecordFromExisting(exec sqlQueryExecer, schemaName, cid, peerID string, signature []byte) {
	// Inlined cid predicate, not an outer one: this runs ONCE PER REPEAT RECORD
	// on the ingest path, inside the store's WRITE lock. With the predicate
	// outside the union's GROUP BY it full-scanned every (producer, standard)
	// table per record — measured on host-01 as 1,393 of 1,414 slow statements
	// in a 12-minute window at ~300 ms each, which is what made an ingest batch
	// hold the store lock for tens of seconds and put that wait in front of
	// every API read. See recordReadSourceFiltered.
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		log.Warnf("Routed mirror: read source for %s: %v", schemaName, err)
		return
	}
	var (
		timestamp    int64
		streamPath   string
		streamOffset int64
		recordLength int64
	)
	err = exec.QueryRow(
		fmt.Sprintf(`SELECT timestamp, stream_path, stream_offset, record_length FROM %s WHERE cid = ?1`, readSource),
		cid,
	).Scan(&timestamp, &streamPath, &streamOffset, &recordLength)
	if err != nil {
		log.Warnf("Routed mirror: read existing %s record %s: %v", schemaName, cid[:16]+"...", err)
		return
	}
	s.mirrorRoutedRecord(exec, schemaName, cid, peerID, timestamp, streamPath, streamOffset, recordLength, signature)
}

// StoreRoutedByProducer stores a record into the (producer, standard) table for
// its producer (peerID) and schema — the (producer, standard) counterpart of
// Store. It appends the payload to the shared FlatSQL stream and updates the
// record index exactly like Store, but the metadata row lands in the producer's
// own table, keeping each publisher's records separated. Content-addressed
// records are immutable, so a repeat CID is a no-op.
//
// This adds the routed write path without changing the existing per-standard
// Store; readers migrate to the (producer, standard) tables in WS7.3.
func (s *FlatSQLStore) StoreRoutedByProducer(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableName, err := s.ensureProducerStandardTable(peerID, schemaName)
	if err != nil {
		return "", fmt.Errorf("ensure (producer, standard) table: %w", err)
	}

	cid := computeCID(data)
	var existing int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT 1 FROM %s WHERE cid = ?`, tableName), cid).Scan(&existing); err == nil {
		return cid, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("failed to check existing record: %w", err)
	}

	now := time.Now().Unix()
	streamPath, streamOffset, recordLength, err := s.appendFlatSQLStreamRecord(schemaName, data)
	if err != nil {
		return "", fmt.Errorf("failed to append FlatSQL stream record: %w", err)
	}
	if err := insertSchemaMetadata(s.db, tableName, cid, peerID, now, streamPath, streamOffset, recordLength, signature, now); err != nil {
		return "", fmt.Errorf("failed to store data: %w", err)
	}
	if err := s.upsertRecordIndex(schemaName, cid, now, data); err != nil {
		// Do not fail writes if index extraction fails for a record.
		log.Warnf("Failed to index %s record %s: %v", schemaName, cid[:16]+"...", err)
	}
	event, err := s.recordCatalogUpsertEvent(s.db, schemaName, cid, peerID, now, streamPath, streamOffset, recordLength, signature, now, data)
	if err != nil {
		return "", fmt.Errorf("record catalog event: %w", err)
	}
	if err := s.appendCatalogEvent(event); err != nil {
		return "", fmt.Errorf("append record catalog event: %w", err)
	}
	return cid, nil
}

// ProducerStandardTable identifies a physical (producer, standard) record table.
type ProducerStandardTable struct {
	TableName  string
	ProducerID string
	Standard   string
}

// parseProducerStandardTable splits a physical table name of the form
// sds_p_<producer>__<standard> back into its parts. The standard component (an
// SDS file id like OMM/OEM/EPM) never contains "__", so the split is on the last
// "__" — robust even if a sanitized producer happens to contain one. It returns
// ok=false for names that are not valid (producer, standard) tables.
func parseProducerStandardTable(name string) (producer, standard string, ok bool) {
	const prefix = "sds_p_"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	idx := strings.LastIndex(rest, "__")
	if idx <= 0 || idx+2 >= len(rest) {
		return "", "", false
	}
	producer, standard = rest[:idx], rest[idx+2:]
	if !isSafeIdentifier(producer) || !isSafeIdentifier(standard) {
		return "", "", false
	}
	return producer, standard, true
}

// listProducerStandardTables enumerates the (producer, standard) record tables.
// The caller must hold s.mu.
func (s *FlatSQLStore) listProducerStandardTables() ([]ProducerStandardTable, error) {
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'sds\_p\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProducerStandardTable
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		producer, standard, ok := parseProducerStandardTable(name)
		if !ok {
			continue
		}
		out = append(out, ProducerStandardTable{TableName: name, ProducerID: producer, Standard: standard})
	}
	return out, rows.Err()
}

// RoutedRecord is one record's provenance drawn from a (producer, standard)
// table by a cross-table query.
type RoutedRecord struct {
	CID        string
	ProducerID string
	Standard   string
	PeerID     string
	Timestamp  int64
}

// QueryRoutedByStandard returns records for a standard across ALL producers,
// newest first — e.g. "all OMM across producers". A zero/negative limit means no
// limit. This is the cross-table query the (producer, standard) layout enables.
func (s *FlatSQLStore) QueryRoutedByStandard(schemaName string, limit int) ([]RoutedRecord, error) {
	standard, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, err
	}
	if !isSafeIdentifier(standard) {
		return nil, fmt.Errorf("unsupported standard name %q", standard)
	}
	return s.queryRoutedTables(func(t ProducerStandardTable) bool { return t.Standard == standard }, limit)
}

// QueryRoutedByProducer returns records from one producer across ALL standards,
// newest first — e.g. "all records from producer X".
func (s *FlatSQLStore) QueryRoutedByProducer(producerID string, limit int) ([]RoutedRecord, error) {
	producer := sanitizeProducerID(producerID)
	if producer == "" {
		return nil, fmt.Errorf("producer id is required")
	}
	return s.queryRoutedTables(func(t ProducerStandardTable) bool { return t.ProducerID == producer }, limit)
}

// QueryRoutedAll returns records across every (producer, standard) table,
// newest first.
func (s *FlatSQLStore) QueryRoutedAll(limit int) ([]RoutedRecord, error) {
	return s.queryRoutedTables(func(ProducerStandardTable) bool { return true }, limit)
}

// queryRoutedTables runs a UNION ALL over the matching (producer, standard)
// tables, carrying each row's producer and standard alongside its metadata.
func (s *FlatSQLStore) queryRoutedTables(match func(ProducerStandardTable) bool, limit int) ([]RoutedRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tables, err := s.listProducerStandardTables()
	if err != nil {
		return nil, err
	}

	parts := make([]string, 0, len(tables))
	for _, t := range tables {
		if !match(t) {
			continue
		}
		// TableName, ProducerID and Standard are validated identifiers (parsed
		// from our own table names), so this cross-table UNION carries
		// provenance without an injection vector.
		parts = append(parts, fmt.Sprintf(
			"SELECT cid, peer_id, timestamp, '%s' AS producer_id, '%s' AS standard FROM %s",
			t.ProducerID, t.Standard, t.TableName))
	}
	if len(parts) == 0 {
		return nil, nil
	}

	query := strings.Join(parts, " UNION ALL ") + " ORDER BY timestamp DESC"
	var args []any
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoutedRecord
	for rows.Next() {
		var r RoutedRecord
		if err := rows.Scan(&r.CID, &r.PeerID, &r.Timestamp, &r.ProducerID, &r.Standard); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// recordReadColumns is the shared column list of every record metadata table
// (legacy per-standard and (producer, standard) alike).
const recordReadColumns = "cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at"

// emptyRecordReadSource is a valid, always-empty FROM target with the shared
// record-metadata column set — used when NO table (legacy or routed) exists
// yet for a standard (v1 databases never pre-create legacy tables).
const emptyRecordReadSource = "(SELECT 0 AS rowid, '' AS cid, '' AS peer_id, 0 AS timestamp, " +
	"'' AS stream_path, 0 AS stream_offset, 0 AS record_length, '' AS signature_hex, 0 AS created_at WHERE 0)"

// recordReadSource returns a SQL FROM/JOIN target spanning every table that
// holds records for the standard: the (producer, standard) tables, plus the
// legacy per-standard table when it exists (pre-flip databases; v1 stores are
// routed-only and never create it). Rows are deduplicated by cid; rowid is
// carried as an ordering tiebreaker only. With exactly one backing table the
// bare table name is returned, keeping single-table query plans unchanged.
// Callers hold s.mu.
func (s *FlatSQLStore) recordReadSource(schemaName string) (string, error) {
	return s.recordReadSourceFiltered(schemaName, "")
}

// recordReadSourceFiltered is recordReadSource with branchWhere inlined into
// EVERY union branch. Use it whenever the caller has a predicate that each
// backing table can answer from an index — above all `cid = ?1`.
//
// WHY THIS EXISTS (measured on host-01, 2026-08-09):
//
// The union read source wraps its branches in `GROUP BY cid` to deduplicate a
// cid that two producers both published. SQLite cannot push an OUTER predicate
// through that GROUP BY into the branches, so
//
//	SELECT ... FROM (SELECT ... FROM (t1 UNION ALL t2) GROUP BY cid) WHERE cid = ?
//
// plans as a FULL SCAN of t1 and t2 — verified with EXPLAIN QUERY PLAN against
// the live 2.8 GB control database, which answered `SCAN sds_p_…__OMM` and
// `SCAN sds_p_source_celestrak__OMM` for exactly this query. On host-01 that is
// 10,457 + ~50 pages per record read, and because the engine is one
// single-threaded WASM instance whose VFS turns every page into a host call and
// a pread64, the daemon was measured doing **27,466 preads/s over the same
// 10,534 pages, forever** — 96 % CPU, 45 % of it system time, ~48,800 voluntary
// context switches/s. Every OTHER store read, including a 30-row indexed probe
// against sdn_dataset_shard_publications, then queued behind those scans and
// cost 0.34–31.7 s. That queue is graph task
// sdn-flatsql-engine-read-queue-seconds-per-call; the scan is its cause, and
// sdn-record-by-cid-read-12-to-29-seconds is the same defect seen from the
// other end.
//
// Inlining the predicate restores `SEARCH … USING INDEX (cid=?)` on every
// branch. Measured at prod scale (250,318 + 1,088-row OMM producer tables, the
// live counts): record-by-cid p50 123.6 ms -> 1.05 ms, and the 30-row control
// probe's p95 measured WHILE that lane runs continuously fell 253.9 ms -> 3.2 ms
// while the lane itself completed 23x more reads.
//
// branchWhere is repeated once per branch, so it MUST use numbered parameters
// (?1, ?2 …), never positional `?`. Callers therefore pass their arguments
// once. Semantics are unchanged: the GROUP BY still runs, so a cid present in
// several tables still collapses to the same single row (covered by
// TestRecordReadSourceFilteredMatchesUnfiltered).
//
// Callers hold s.mu.
func (s *FlatSQLStore) recordReadSourceFiltered(schemaName, branchWhere string) (string, error) {
	legacy, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return "", err
	}
	tables := []string{}
	if exists, err := s.tableExists(legacy); err == nil && exists {
		tables = append(tables, legacy)
	}
	producerTables, err := s.listProducerStandardTables()
	if err != nil {
		log.Warnf("recordReadSource: list (producer, standard) tables: %v", err)
	} else {
		for _, t := range producerTables {
			if t.Standard == legacy {
				tables = append(tables, t.TableName)
			}
		}
	}
	switch len(tables) {
	case 0:
		return emptyRecordReadSource, nil
	case 1:
		// One table needs no union and no GROUP BY, so the caller's own outer
		// predicate already reaches the index. Returning the bare name keeps
		// single-table plans byte-identical to what they always were.
		return tables[0], nil
	}
	where := ""
	if strings.TrimSpace(branchWhere) != "" {
		where = " WHERE " + branchWhere
	}
	selects := make([]string, 0, len(tables))
	for _, t := range tables {
		selects = append(selects, fmt.Sprintf("SELECT rowid AS rowid, %s FROM %s%s", recordReadColumns, t, where))
	}
	return "(SELECT rowid, " + recordReadColumns + " FROM (" + strings.Join(selects, " UNION ALL ") + ") GROUP BY cid)", nil
}

// rawRecordReadSource returns a FROM/JOIN target for the raw-record family and
// its head/count that exposes the GLOBAL sync cursor as `rowid` (WS7.3d):
// sdn_record_index.rowid — a single shared, monotonic table (the store never
// VACUUMs, so rowids are stable and never reused mid-life) — joined to the
// record payload columns from recordReadSource. Every stored record has an
// index row (upsertRecordIndexExec inserts a bare row even on field-extraction
// failure), so the join is complete. The subquery aliases as `records`, so the
// existing `records.rowid` cursor predicates / ORDER BY / MAX(records.rowid)
// keep working — now over the global index rowid instead of a per-table one.
// Callers hold s.mu.
func (s *FlatSQLStore) rawRecordReadSource(schemaName string) (string, error) {
	payload, err := s.recordReadSource(schemaName)
	if err != nil {
		return "", err
	}
	// schema_name is stored verbatim (e.g. "OMM.fbs"); validated by
	// recordReadSource above. Escape single quotes defensively for the literal.
	schemaLiteral := strings.ReplaceAll(schemaName, "'", "''")
	return fmt.Sprintf(
		`(SELECT idx.rowid AS rowid, rr.cid AS cid, rr.peer_id AS peer_id, `+
			`rr.timestamp AS timestamp, rr.stream_path AS stream_path, `+
			`rr.stream_offset AS stream_offset, rr.record_length AS record_length, `+
			`rr.signature_hex AS signature_hex, rr.created_at AS created_at `+
			`FROM sdn_record_index idx JOIN %s rr ON rr.cid = idx.cid `+
			`WHERE idx.schema_name = '%s')`,
		payload, schemaLiteral,
	), nil
}

// deleteRoutedMirrorsWhere deletes rows matching whereClause from every
// (producer, standard) table of the given standard — used by delete/GC/
// reconcile paths so the union read surface cannot resurrect removed
// records via their routed mirrors. Best-effort per table; returns the total
// rows deleted across the producer tables. Callers hold s.mu.
func (s *FlatSQLStore) deleteRoutedMirrorsWhere(exec sqlExecer, standardTable, whereClause string, args ...any) int64 {
	producerTables, err := s.listProducerStandardTables()
	if err != nil {
		log.Warnf("routed mirror delete: list (producer, standard) tables: %v", err)
		return 0
	}
	var total int64
	for _, pt := range producerTables {
		if pt.Standard != standardTable {
			continue
		}
		result, err := exec.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`DELETE FROM %s WHERE %s`, pt.TableName, whereClause)), args...)
		if err != nil {
			log.Warnf("routed mirror delete %s: %v", pt.TableName, err)
			continue
		}
		n, _ := result.RowsAffected()
		total += n
	}
	return total
}
