// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3" // SQLite driver

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var log = logging.Logger("storage")

// FlatSQLStore provides SQLite storage with FlatBuffer virtual tables.
type FlatSQLStore struct {
	db        *sql.DB
	validator *sds.Validator
	dbPath    string
	mu        sync.RWMutex
}

// NewFlatSQLStore creates a new FlatSQL storage instance.
func NewFlatSQLStore(basePath string, validator *sds.Validator) (*FlatSQLStore, error) {
	// Ensure directory exists
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	dbPath := filepath.Join(basePath, "sdn.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &FlatSQLStore{
		db:        db,
		validator: validator,
		dbPath:    dbPath,
	}

	// Initialize tables for all schemas
	if err := store.initTables(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize tables: %w", err)
	}

	return store, nil
}

func (s *FlatSQLStore) initTables() error {
	// Create main metadata table
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_metadata (
			key TEXT PRIMARY KEY,
			value TEXT,
			updated_at INTEGER
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create metadata table: %w", err)
	}

	// Fast lookup index for API queries (schema/day/object filters).
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_record_index (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			norad_cat_id INTEGER,
			entity_id TEXT,
			epoch_unix INTEGER,
			epoch_day TEXT,
			source_timestamp INTEGER NOT NULL,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (schema_name, cid)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create index table: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_lookup
		ON sdn_record_index (schema_name, epoch_day, norad_cat_id, entity_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create composite index: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_norad
		ON sdn_record_index (schema_name, norad_cat_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create norad index: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_entity
		ON sdn_record_index (schema_name, entity_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create entity index: %w", err)
	}

	// Create tables for each schema
	for _, schemaName := range s.validator.Schemas() {
		tableName := sds.SchemaNameToTable(schemaName)

		// Main data table
		createSQL := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				cid TEXT PRIMARY KEY,
				peer_id TEXT NOT NULL,
				timestamp INTEGER NOT NULL,
				data BLOB NOT NULL,
				signature BLOB,
				created_at INTEGER DEFAULT (strftime('%%s', 'now')),
				UNIQUE(cid)
			)
		`, tableName)

		if _, err := s.db.Exec(createSQL); err != nil {
			return fmt.Errorf("failed to create table %s: %w", tableName, err)
		}

		// Create index on peer_id and timestamp
		indexSQL := fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS idx_%s_peer_time ON %s (peer_id, timestamp)
		`, tableName, tableName)

		if _, err := s.db.Exec(indexSQL); err != nil {
			log.Warnf("Failed to create index for %s: %v", tableName, err)
		}

		log.Debugf("Initialized table: %s", tableName)
	}

	return nil
}

// Store stores validated data in the appropriate table.
func (s *FlatSQLStore) Store(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableName := sds.SchemaNameToTable(schemaName)

	// Compute CID (content identifier)
	cid := computeCID(data)

	// Store the data
	insertSQL := fmt.Sprintf(`
		INSERT OR REPLACE INTO %s (cid, peer_id, timestamp, data, signature)
		VALUES (?, ?, ?, ?, ?)
	`, tableName)

	now := time.Now().Unix()
	_, err := s.db.Exec(insertSQL, cid, peerID, now, data, signature)
	if err != nil {
		return "", fmt.Errorf("failed to store data: %w", err)
	}

	if err := s.upsertRecordIndex(schemaName, cid, now, data); err != nil {
		// Do not fail writes if index extraction fails for a record.
		log.Warnf("Failed to index %s record %s: %v", schemaName, cid[:16]+"...", err)
	}

	log.Debugf("Stored %s record with CID: %s", schemaName, cid[:16]+"...")
	return cid, nil
}

// Get retrieves data by CID.
func (s *FlatSQLStore) Get(schemaName, cid string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName := sds.SchemaNameToTable(schemaName)

	querySQL := fmt.Sprintf(`SELECT data FROM %s WHERE cid = ?`, tableName)

	var data []byte
	err := s.db.QueryRow(querySQL, cid).Scan(&data)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("not found: %s", cid)
		}
		return nil, fmt.Errorf("failed to get data: %w", err)
	}

	return data, nil
}

// Query executes a query against a schema table.
func (s *FlatSQLStore) Query(schemaName, whereClause string, args ...interface{}) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName := sds.SchemaNameToTable(schemaName)

	var querySQL string
	if whereClause != "" {
		querySQL = fmt.Sprintf(`SELECT data FROM %s WHERE %s`, tableName, whereClause)
	} else {
		querySQL = fmt.Sprintf(`SELECT data FROM %s`, tableName)
	}

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	defer rows.Close()

	var results [][]byte
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			log.Warnf("Failed to scan row: %v", err)
			continue
		}
		results = append(results, data)
	}

	return results, nil
}

// QueryWithPeerID queries records from a specific peer.
func (s *FlatSQLStore) QueryWithPeerID(schemaName, peerID string) ([][]byte, error) {
	return s.Query(schemaName, "peer_id = ?", peerID)
}

// QuerySince queries records since a given timestamp.
func (s *FlatSQLStore) QuerySince(schemaName string, since time.Time) ([][]byte, error) {
	return s.Query(schemaName, "timestamp > ?", since.Unix())
}

// Delete removes a record by CID.
func (s *FlatSQLStore) Delete(schemaName, cid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableName := sds.SchemaNameToTable(schemaName)

	deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE cid = ?`, tableName)

	result, err := s.db.Exec(deleteSQL, cid)
	if err != nil {
		return fmt.Errorf("failed to delete: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("not found: %s", cid)
	}

	if _, err := s.db.Exec(`DELETE FROM sdn_record_index WHERE schema_name = ? AND cid = ?`, schemaName, cid); err != nil {
		log.Warnf("Failed to delete index row for %s/%s: %v", schemaName, cid, err)
	}

	return nil
}

// Count returns the number of records in a schema table.
func (s *FlatSQLStore) Count(schemaName string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName := sds.SchemaNameToTable(schemaName)

	var count int64
	err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tableName)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count: %w", err)
	}

	return count, nil
}

// GarbageCollect removes old records based on age.
func (s *FlatSQLStore) GarbageCollect(maxAge time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge).Unix()
	var totalDeleted int64

	for _, schemaName := range s.validator.Schemas() {
		tableName := sds.SchemaNameToTable(schemaName)

		deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE timestamp < ?`, tableName)
		result, err := s.db.Exec(deleteSQL, cutoff)
		if err != nil {
			log.Warnf("GC failed for %s: %v", tableName, err)
			continue
		}

		affected, _ := result.RowsAffected()
		totalDeleted += affected

		// Keep index table in sync with GC deletes.
		if _, err := s.db.Exec(`
			DELETE FROM sdn_record_index
			WHERE schema_name = ? AND source_timestamp < ?
		`, schemaName, cutoff); err != nil {
			log.Warnf("GC index cleanup failed for %s: %v", schemaName, err)
		}
	}

	if totalDeleted > 0 {
		log.Infof("GC removed %d old records", totalDeleted)
	}

	return totalDeleted, nil
}

// Close closes the database connection.
func (s *FlatSQLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Stats returns storage statistics.
func (s *FlatSQLStore) Stats() (map[string]int64, error) {
	stats := make(map[string]int64)

	for _, schemaName := range s.validator.Schemas() {
		count, err := s.Count(schemaName)
		if err != nil {
			log.Warnf("Failed to get count for %s: %v", schemaName, err)
			continue
		}
		stats[schemaName] = count
	}

	return stats, nil
}

// computeCID computes a content identifier for data.
func computeCID(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// Record represents a stored record with metadata.
type Record struct {
	CID       string
	PeerID    string
	Timestamp time.Time
	Data      []byte
	Signature []byte
}

// RebuildIndex scans all schema tables and repopulates sdn_record_index.
func (s *FlatSQLStore) RebuildIndex() (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summary := make(map[string]int64)

	for _, schemaName := range s.validator.Schemas() {
		tableName := sds.SchemaNameToTable(schemaName)
		rows, err := s.db.Query(fmt.Sprintf(`SELECT cid, timestamp, data FROM %s`, tableName))
		if err != nil {
			return nil, fmt.Errorf("failed to query %s for reindex: %w", tableName, err)
		}

		var indexed int64
		for rows.Next() {
			var cid string
			var ts int64
			var data []byte
			if err := rows.Scan(&cid, &ts, &data); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to scan %s row: %w", tableName, err)
			}
			if err := s.upsertRecordIndex(schemaName, cid, ts, data); err != nil {
				log.Debugf("Skipping index row for %s/%s: %v", schemaName, cid, err)
				continue
			}
			indexed++
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("failed closing rows for %s: %w", tableName, err)
		}
		summary[schemaName] = indexed
	}

	return summary, nil
}

// QueryByIndexedFields returns records for schema/day/object filters.
// day uses YYYY-MM-DD in UTC and is optional.
func (s *FlatSQLStore) QueryByIndexedFields(schemaName, day string, noradCatID *uint32, entityID string, limit int) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := sds.ValidateSchemaName(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	if day != "" {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return nil, fmt.Errorf("invalid day %q (expected YYYY-MM-DD)", day)
		}
	}

	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	tableName := sds.SchemaNameToTable(schemaName)

	query := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp, d.data, d.signature
		FROM %s d
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = d.cid
		WHERE 1=1
	`, tableName)

	args := []interface{}{schemaName}

	if day != "" {
		query += ` AND idx.epoch_day = ?`
		args = append(args, day)
	}

	if noradCatID != nil {
		query += ` AND idx.norad_cat_id = ?`
		args = append(args, int64(*noradCatID))
	}

	if entityID != "" {
		query += ` AND idx.entity_id = ?`
		args = append(args, entityID)
	}

	query += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("indexed query failed: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		rec := &Record{}
		var ts int64
		if err := rows.Scan(&rec.CID, &rec.PeerID, &ts, &rec.Data, &rec.Signature); err != nil {
			return nil, fmt.Errorf("failed scanning indexed row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		records = append(records, rec)
	}

	return records, nil
}

// GetRecord retrieves a full record by CID.
func (s *FlatSQLStore) GetRecord(schemaName, cid string) (*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName := sds.SchemaNameToTable(schemaName)

	querySQL := fmt.Sprintf(`
		SELECT cid, peer_id, timestamp, data, signature
		FROM %s WHERE cid = ?
	`, tableName)

	var record Record
	var timestamp int64
	err := s.db.QueryRow(querySQL, cid).Scan(
		&record.CID,
		&record.PeerID,
		&timestamp,
		&record.Data,
		&record.Signature,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("not found: %s", cid)
		}
		return nil, fmt.Errorf("failed to get record: %w", err)
	}

	record.Timestamp = time.Unix(timestamp, 0)
	return &record, nil
}

type indexedFields struct {
	noradCatID *uint32
	entityID   string
	epochUnix  *int64
	epochDay   string
}

func (s *FlatSQLStore) upsertRecordIndex(schemaName, cid string, sourceTimestamp int64, data []byte) error {
	fields, err := extractIndexedFields(schemaName, data)
	if err != nil {
		return err
	}

	var norad interface{}
	if fields.noradCatID != nil {
		norad = int64(*fields.noradCatID)
	}
	var entity interface{}
	if fields.entityID != "" {
		entity = fields.entityID
	}
	var epoch interface{}
	if fields.epochUnix != nil {
		epoch = *fields.epochUnix
	}
	var day interface{}
	if fields.epochDay != "" {
		day = fields.epochDay
	}

	_, err = s.db.Exec(`
		INSERT INTO sdn_record_index (
			schema_name, cid, norad_cat_id, entity_id, epoch_unix, epoch_day, source_timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, cid) DO UPDATE SET
			norad_cat_id = excluded.norad_cat_id,
			entity_id = excluded.entity_id,
			epoch_unix = excluded.epoch_unix,
			epoch_day = excluded.epoch_day,
			source_timestamp = excluded.source_timestamp
	`, schemaName, cid, norad, entity, epoch, day, sourceTimestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert index row: %w", err)
	}

	return nil
}

func extractIndexedFields(schemaName string, data []byte) (*indexedFields, error) {
	out := &indexedFields{}

	switch schemaName {
	case "OMM.fbs":
		omm, err := parseOMM(data)
		if err != nil {
			return nil, err
		}
		if id := omm.NORAD_CAT_ID(); id > 0 {
			idCopy := id
			out.noradCatID = &idCopy
		}

		epochStr := strings.TrimSpace(string(omm.EPOCH()))
		if epochStr == "" {
			epochStr = strings.TrimSpace(string(omm.CREATION_DATE()))
		}
		if epochStr != "" {
			epochUnix, err := parseEpochString(epochStr)
			if err == nil {
				out.epochUnix = &epochUnix
				out.epochDay = time.Unix(epochUnix, 0).UTC().Format("2006-01-02")
			}
		}

	case "MPE.fbs":
		mpe, err := parseMPE(data)
		if err != nil {
			return nil, err
		}
		out.entityID = strings.TrimSpace(string(mpe.ENTITY_ID()))
		if epoch := int64(mpe.EPOCH()); epoch > 0 {
			out.epochUnix = &epoch
			out.epochDay = time.Unix(epoch, 0).UTC().Format("2006-01-02")
		}

	case "CAT.fbs":
		cat, err := parseCAT(data)
		if err != nil {
			return nil, err
		}
		if id := cat.NORAD_CAT_ID(); id > 0 {
			idCopy := id
			out.noradCatID = &idCopy
		}

	default:
		// No structured extraction for this schema yet.
	}

	return out, nil
}

func parseOMM(data []byte) (*OMM.OMM, error) {
	switch {
	case OMM.SizePrefixedOMMBufferHasIdentifier(data):
		return OMM.GetSizePrefixedRootAsOMM(data, 0), nil
	case OMM.OMMBufferHasIdentifier(data):
		return OMM.GetRootAsOMM(data, 0), nil
	default:
		return nil, errors.New("invalid OMM buffer")
	}
}

func parseMPE(data []byte) (*MPE.MPE, error) {
	switch {
	case MPE.SizePrefixedMPEBufferHasIdentifier(data):
		return MPE.GetSizePrefixedRootAsMPE(data, 0), nil
	case MPE.MPEBufferHasIdentifier(data):
		return MPE.GetRootAsMPE(data, 0), nil
	default:
		return nil, errors.New("invalid MPE buffer")
	}
}

func parseCAT(data []byte) (*CAT.CAT, error) {
	switch {
	case CAT.SizePrefixedCATBufferHasIdentifier(data):
		return CAT.GetSizePrefixedRootAsCAT(data, 0), nil
	case CAT.CATBufferHasIdentifier(data):
		return CAT.GetRootAsCAT(data, 0), nil
	default:
		return nil, errors.New("invalid CAT buffer")
	}
}

func parseEpochString(raw string) (int64, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return 0, errors.New("empty epoch")
	}

	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, normalized); err == nil {
			return t.UTC().Unix(), nil
		}
	}

	if floatEpoch, err := strconv.ParseFloat(normalized, 64); err == nil && floatEpoch > 0 {
		return int64(floatEpoch), nil
	}

	return 0, fmt.Errorf("unsupported epoch format: %q", raw)
}
