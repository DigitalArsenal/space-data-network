package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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

// mirrorRoutedRecord writes the (producer, standard) metadata row for a record
// whose payload already lives in the shared FlatSQL stream — the phased WS7.3
// write flip: every legacy write also lands in the producer's own table, so
// producer-scoped queries (QueryRouted*) see all new data while the hydrated
// readers keep using the legacy per-standard tables until they migrate.
//
// Best-effort by design: a routed-mirror failure must never fail the primary
// write. Records with no producer identity (empty peerID) are skipped — those
// call sites gain identities in the reader-flip phase. Callers hold s.mu.
func (s *FlatSQLStore) mirrorRoutedRecord(exec sqlExecer, schemaName, cid, peerID string, timestamp int64, streamPath string, streamOffset, recordLength int64, signature []byte) {
	if strings.TrimSpace(peerID) == "" {
		return
	}
	tableName, err := s.ensureProducerStandardTable(peerID, schemaName)
	if err != nil {
		log.Warnf("Routed mirror: ensure (producer, standard) table for %s/%s: %v", peerID, schemaName, err)
		return
	}
	if err := insertSchemaMetadata(exec, tableName, cid, peerID, timestamp, streamPath, streamOffset, recordLength, signature, timestamp); err != nil {
		log.Warnf("Routed mirror: insert into %s for %s: %v", tableName, cid[:16]+"...", err)
	}
}

// mirrorRoutedRecordFromExisting mirrors a record that deduplicated against an
// existing legacy row (repeat CID, possibly from a different producer): the
// stream coordinates are read from the legacy table so the producer's table
// still records that this producer published the content. Callers hold s.mu.
func (s *FlatSQLStore) mirrorRoutedRecordFromExisting(exec sqlQueryExecer, legacyTable, schemaName, cid, peerID string, signature []byte) {
	if strings.TrimSpace(peerID) == "" {
		return
	}
	var (
		timestamp    int64
		streamPath   string
		streamOffset int64
		recordLength int64
	)
	err := exec.QueryRow(
		fmt.Sprintf(`SELECT timestamp, stream_path, stream_offset, record_length FROM %s WHERE cid = ?`, legacyTable),
		cid,
	).Scan(&timestamp, &streamPath, &streamOffset, &recordLength)
	if err != nil {
		log.Warnf("Routed mirror: read existing %s record %s: %v", legacyTable, cid[:16]+"...", err)
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

// recordReadSource returns a SQL FROM/JOIN target spanning the legacy
// per-standard table and every (producer, standard) table for the standard,
// deduplicated by cid (dual-written rows appear once; rowid is carried as an
// ordering tiebreaker only). When no producer tables exist for the standard it
// returns the legacy table name unchanged, keeping pre-flip query plans
// byte-identical. Callers hold s.mu.
func (s *FlatSQLStore) recordReadSource(schemaName string) (string, error) {
	legacy, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return "", err
	}
	producerTables, err := s.listProducerStandardTables()
	if err != nil {
		log.Warnf("recordReadSource: list (producer, standard) tables: %v", err)
		return legacy, nil
	}
	selects := []string{fmt.Sprintf("SELECT rowid AS rowid, %s FROM %s", recordReadColumns, legacy)}
	for _, t := range producerTables {
		if t.Standard == legacy {
			selects = append(selects, fmt.Sprintf("SELECT rowid AS rowid, %s FROM %s", recordReadColumns, t.TableName))
		}
	}
	if len(selects) == 1 {
		return legacy, nil
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
// records via their routed mirrors. Best-effort per table; callers hold s.mu.
func (s *FlatSQLStore) deleteRoutedMirrorsWhere(exec sqlExecer, standardTable, whereClause string, args ...any) {
	producerTables, err := s.listProducerStandardTables()
	if err != nil {
		log.Warnf("routed mirror delete: list (producer, standard) tables: %v", err)
		return
	}
	for _, pt := range producerTables {
		if pt.Standard != standardTable {
			continue
		}
		if _, err := exec.Exec(fmt.Sprintf(`DELETE FROM %s WHERE %s`, pt.TableName, whereClause), args...); err != nil {
			log.Warnf("routed mirror delete %s: %v", pt.TableName, err)
		}
	}
}
