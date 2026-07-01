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
