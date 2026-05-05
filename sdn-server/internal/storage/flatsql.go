// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	if err := os.MkdirAll(basePath, 0700); err != nil {
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
			object_type TEXT,
			ops_status_code TEXT,
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
	if err := s.ensureColumn("sdn_record_index", "object_type", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("sdn_record_index", "ops_status_code", "TEXT"); err != nil {
		return err
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

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_catalog_filters
		ON sdn_record_index (schema_name, object_type, ops_status_code, norad_cat_id)
	`); err != nil {
		return fmt.Errorf("failed to create catalog filter index: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_time_window
		ON sdn_record_index (schema_name, epoch_unix, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create time window index: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_record_source_tags (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			source_url TEXT,
			batch_id TEXT,
			content_key_id TEXT,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (schema_name, cid)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create source tags table: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_lookup
		ON sdn_record_source_tags (schema_name, provider_id, source_name, batch_id)
	`); err != nil {
		return fmt.Errorf("failed to create source tags lookup index: %w", err)
	}

	// Directory index for node/user EPM records.
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_directory (
			kind TEXT NOT NULL,
			peer_id TEXT NOT NULL,
			dn TEXT,
			legal_name TEXT,
			bitcoin_address TEXT,
			epm_cid TEXT,
			source TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			epm_json TEXT NOT NULL,
			PRIMARY KEY (kind, peer_id)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create directory table: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_directory_search
		ON sdn_directory (kind, dn, legal_name, bitcoin_address)
	`); err != nil {
		return fmt.Errorf("failed to create directory search index: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_directory_updated
		ON sdn_directory (kind, updated_at DESC)
	`); err != nil {
		return fmt.Errorf("failed to create directory updated index: %w", err)
	}

	// Publication log index for PLG hash-chained logs.
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_log_index (
			publisher_peer_id TEXT NOT NULL,
			schema_type       TEXT NOT NULL,
			sequence          INTEGER NOT NULL,
			entry_hash        TEXT NOT NULL,
			record_cid        TEXT NOT NULL,
			plg_cid           TEXT NOT NULL,
			epoch_day         TEXT,
			timestamp         INTEGER NOT NULL,
			PRIMARY KEY (publisher_peer_id, schema_type, sequence)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create log index table: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_log_index_head
		ON sdn_log_index (publisher_peer_id, schema_type, sequence DESC)
	`); err != nil {
		return fmt.Errorf("failed to create log head index: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_log_index_epoch
		ON sdn_log_index (schema_type, epoch_day, timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create log epoch index: %w", err)
	}

	// Create tables for each schema
	for _, schemaName := range s.validator.Schemas() {
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			return fmt.Errorf("invalid schema name %q: %w", schemaName, err)
		}

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

func (s *FlatSQLStore) ensureColumn(tableName, columnName, columnType string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return fmt.Errorf("failed to inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("failed to scan %s column metadata: %w", tableName, err)
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed iterating %s columns: %w", tableName, err)
	}

	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, tableName, columnName, columnType)); err != nil {
		return fmt.Errorf("failed to add %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

// Store stores validated data in the appropriate table.
func (s *FlatSQLStore) Store(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return "", fmt.Errorf("invalid schema name: %w", err)
	}

	// Compute CID (content identifier)
	cid := computeCID(data)

	// Use INSERT OR IGNORE: content-addressed records are immutable.
	// REPLACE would allow a different peer to overwrite the original
	// author's peer_id (attribution hijacking).
	insertSQL := fmt.Sprintf(`
		INSERT OR IGNORE INTO %s (cid, peer_id, timestamp, data, signature)
		VALUES (?, ?, ?, ?, ?)
	`, tableName)

	now := time.Now().Unix()
	_, err = s.db.Exec(insertSQL, cid, peerID, now, data, signature)
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

// SourceTags records source provenance needed for provider/batch-aware queries.
type SourceTags struct {
	ProviderID   string
	SourceName   string
	SourceURL    string
	BatchID      string
	ContentKeyID string
}

// SourceTagQuery filters stored records by source tags.
type SourceTagQuery struct {
	SchemaName string
	ProviderID string
	SourceName string
	BatchID    string
	Limit      int
}

// IndexedRecordQuery filters materialized FlatSQL record indexes.
type IndexedRecordQuery struct {
	SchemaName         string
	Day                string
	NoradCatID         *uint32
	EntityID           string
	ObjectType         string
	OpsStatusCode      string
	ActivePayloads     bool
	CAReadyResidentSet bool
	From               *time.Time
	To                 *time.Time
	ProviderID         string
	SourceName         string
	BatchID            string
	Limit              int
}

// StoreWithSourceTags stores a FlatBuffer record and attaches provider/source metadata.
func (s *FlatSQLStore) StoreWithSourceTags(schemaName string, data []byte, peerID string, signature []byte, tags SourceTags) (string, error) {
	cid, err := s.Store(schemaName, data, peerID, signature)
	if err != nil {
		return "", err
	}
	if err := s.UpsertSourceTags(schemaName, cid, tags); err != nil {
		return "", err
	}
	return cid, nil
}

// UpsertSourceTags attaches or updates source tags for an existing record.
func (s *FlatSQLStore) UpsertSourceTags(schemaName, cid string, tags SourceTags) error {
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}
	if strings.TrimSpace(cid) == "" {
		return errors.New("cid is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id, content_key_id
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, cid) DO UPDATE SET
			provider_id = excluded.provider_id,
			source_name = excluded.source_name,
			source_url = excluded.source_url,
			batch_id = excluded.batch_id,
			content_key_id = excluded.content_key_id
	`, schemaName, cid, strings.TrimSpace(tags.ProviderID), strings.TrimSpace(tags.SourceName), strings.TrimSpace(tags.SourceURL), strings.TrimSpace(tags.BatchID), strings.TrimSpace(tags.ContentKeyID))
	if err != nil {
		return fmt.Errorf("failed to upsert source tags: %w", err)
	}
	return nil
}

// GetSourceTags returns provider/source tags for a stored record.
func (s *FlatSQLStore) GetSourceTags(schemaName, cid string) (SourceTags, error) {
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return SourceTags{}, fmt.Errorf("invalid schema name: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	var tags SourceTags
	err := s.db.QueryRow(`
		SELECT provider_id, source_name, source_url, batch_id, content_key_id
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ?
	`, schemaName, cid).Scan(&tags.ProviderID, &tags.SourceName, &tags.SourceURL, &tags.BatchID, &tags.ContentKeyID)
	if err != nil {
		if err == sql.ErrNoRows {
			return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
		}
		return SourceTags{}, fmt.Errorf("failed to get source tags: %w", err)
	}
	return tags, nil
}

// QuerySourceTaggedRecords returns records matching provider/source/batch tags.
func (s *FlatSQLStore) QuerySourceTaggedRecords(query SourceTagQuery) ([]*Record, error) {
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	tableName, err := sds.SchemaNameToTable(query.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	conditions := []string{"tags.schema_name = ?"}
	args := []interface{}{query.SchemaName, query.SchemaName}
	if providerID := strings.TrimSpace(query.ProviderID); providerID != "" {
		conditions = append(conditions, "tags.provider_id = ?")
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(query.SourceName); sourceName != "" {
		conditions = append(conditions, "tags.source_name = ?")
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(query.BatchID); batchID != "" {
		conditions = append(conditions, "tags.batch_id = ?")
		args = append(args, batchID)
	}
	args = append(args, query.Limit)

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT records.cid, records.peer_id, records.timestamp, records.data, records.signature
		FROM %s records
		INNER JOIN sdn_record_source_tags tags
			ON tags.schema_name = ? AND tags.cid = records.cid
		WHERE %s
		ORDER BY records.timestamp DESC
		LIMIT ?
	`, tableName, strings.Join(conditions, " AND ")), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query source tagged records: %w", err)
	}
	defer rows.Close()

	records := make([]*Record, 0, query.Limit)
	for rows.Next() {
		var record Record
		var ts int64
		if err := rows.Scan(&record.CID, &record.PeerID, &ts, &record.Data, &record.Signature); err != nil {
			return nil, fmt.Errorf("failed to scan source tagged record: %w", err)
		}
		record.Timestamp = time.Unix(ts, 0)
		records = append(records, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source tagged record rows: %w", err)
	}
	return records, nil
}

// ValidateSourceTags checks required provider/source fields.
func ValidateSourceTags(tags SourceTags) error {
	if strings.TrimSpace(tags.ProviderID) == "" {
		return errors.New("provider_id is required")
	}
	if strings.TrimSpace(tags.SourceName) == "" {
		return errors.New("source_name is required")
	}
	return nil
}

// Get retrieves data by CID.
func (s *FlatSQLStore) Get(schemaName, cid string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	querySQL := fmt.Sprintf(`SELECT data FROM %s WHERE cid = ?`, tableName)

	var data []byte
	err = s.db.QueryRow(querySQL, cid).Scan(&data)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("not found: %s", cid)
		}
		return nil, fmt.Errorf("failed to get data: %w", err)
	}

	return data, nil
}

// Query executes a safe parameterized query against a schema table.
// The whereClause MUST use ? placeholders for all values.
// This method is only used internally with trusted where clauses.
func (s *FlatSQLStore) Query(schemaName, whereClause string, args ...interface{}) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

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

// QueryAll returns all records for a schema (no filtering). Safe for protocol use.
func (s *FlatSQLStore) QueryAll(schemaName string, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	return s.Query(schemaName, "1=1 ORDER BY timestamp DESC LIMIT ?", limit)
}

// QueryAllBounded returns recent records while enforcing both row and total-byte limits.
func (s *FlatSQLStore) QueryAllBounded(schemaName string, limit int, maxTotalBytes int) ([][]byte, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if maxTotalBytes <= 0 {
		maxTotalBytes = 2 * 1024 * 1024 // 2MB default response budget
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	querySQL := fmt.Sprintf(`SELECT data FROM %s ORDER BY timestamp DESC LIMIT ?`, tableName)
	rows, err := s.db.Query(querySQL, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query bounded records: %w", err)
	}
	defer rows.Close()

	results := make([][]byte, 0, limit)
	totalBytes := 0
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			log.Warnf("Failed to scan row: %v", err)
			continue
		}
		if len(data) > maxTotalBytes {
			continue
		}
		if totalBytes+len(data) > maxTotalBytes {
			break
		}
		totalBytes += len(data)
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

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}

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

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}

	var count int64
	err = s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tableName)).Scan(&count)
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
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			log.Warnf("GC skipping invalid schema %q: %v", schemaName, err)
			continue
		}

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

// Path returns the database file path.
func (s *FlatSQLStore) Path() string {
	return s.dbPath
}

// Record represents a stored record with metadata.
type Record struct {
	CID       string
	PeerID    string
	Timestamp time.Time
	Data      []byte
	Signature []byte
}

// DirectoryRecord represents a normalized EPM directory entry.
type DirectoryRecord struct {
	Kind           string
	PeerID         string
	DN             string
	LegalName      string
	BitcoinAddress string
	EPMCID         string
	Source         string
	EPMJSON        string
	UpdatedAt      int64
}

// DirectoryQuery filters directory records.
type DirectoryQuery struct {
	Kind           string
	PeerID         string
	Source         string
	ExcludePeerID  string
	ExcludeSources []string
	Search         string
	Limit          int
}

// UpsertDirectoryRecord inserts or updates a directory record.
func (s *FlatSQLStore) UpsertDirectoryRecord(record DirectoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	kind := strings.TrimSpace(strings.ToLower(record.Kind))
	peerID := strings.TrimSpace(record.PeerID)
	if kind == "" {
		return errors.New("directory record kind is required")
	}
	if peerID == "" {
		return errors.New("directory record peer_id is required")
	}

	source := strings.TrimSpace(record.Source)
	if source == "" {
		source = "unknown"
	}
	updatedAt := record.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}

	epmJSON := strings.TrimSpace(record.EPMJSON)
	if epmJSON == "" {
		epmJSON = "{}"
	} else {
		var canonical any
		if err := json.Unmarshal([]byte(epmJSON), &canonical); err == nil {
			if b, err := json.Marshal(canonical); err == nil {
				epmJSON = string(b)
			}
		}
	}

	_, err := s.db.Exec(`
		INSERT INTO sdn_directory (
			kind, peer_id, dn, legal_name, bitcoin_address, epm_cid, source, updated_at, epm_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, peer_id) DO UPDATE SET
			dn = excluded.dn,
			legal_name = excluded.legal_name,
			bitcoin_address = excluded.bitcoin_address,
			epm_cid = excluded.epm_cid,
			source = excluded.source,
			updated_at = excluded.updated_at,
			epm_json = excluded.epm_json
	`, kind, peerID, strings.TrimSpace(record.DN), strings.TrimSpace(record.LegalName), strings.TrimSpace(record.BitcoinAddress), strings.TrimSpace(record.EPMCID), source, updatedAt, epmJSON)
	if err != nil {
		return fmt.Errorf("failed to upsert directory record: %w", err)
	}

	return nil
}

// QueryDirectory queries directory records using indexed filters and free-text search.
func (s *FlatSQLStore) QueryDirectory(query DirectoryQuery) ([]DirectoryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	kind := strings.TrimSpace(strings.ToLower(query.Kind))
	peerID := strings.TrimSpace(query.PeerID)
	source := strings.TrimSpace(query.Source)
	excludePeerID := strings.TrimSpace(query.ExcludePeerID)
	search := strings.TrimSpace(query.Search)

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	sqlBuilder := strings.Builder{}
	sqlBuilder.WriteString(`
		SELECT kind, peer_id, dn, legal_name, bitcoin_address, epm_cid, source, updated_at, epm_json
		FROM sdn_directory
		WHERE 1=1
	`)
	args := make([]any, 0, 6)

	if kind != "" {
		sqlBuilder.WriteString(` AND kind = ?`)
		args = append(args, kind)
	}
	if peerID != "" {
		sqlBuilder.WriteString(` AND peer_id = ?`)
		args = append(args, peerID)
	}
	if excludePeerID != "" {
		sqlBuilder.WriteString(` AND peer_id <> ?`)
		args = append(args, excludePeerID)
	}
	if source != "" {
		sqlBuilder.WriteString(` AND source = ?`)
		args = append(args, source)
	}
	excludeSources := make([]string, 0, len(query.ExcludeSources))
	for _, excludeSource := range query.ExcludeSources {
		if trimmed := strings.TrimSpace(excludeSource); trimmed != "" {
			excludeSources = append(excludeSources, trimmed)
		}
	}
	if len(excludeSources) > 0 {
		sqlBuilder.WriteString(` AND source NOT IN (`)
		for i, excludeSource := range excludeSources {
			if i > 0 {
				sqlBuilder.WriteString(`, `)
			}
			sqlBuilder.WriteString(`?`)
			args = append(args, excludeSource)
		}
		sqlBuilder.WriteString(`)`)
	}
	if search != "" {
		needle := "%" + strings.ToLower(search) + "%"
		sqlBuilder.WriteString(` AND (
			lower(COALESCE(peer_id, '')) LIKE ? OR
			lower(COALESCE(dn, '')) LIKE ? OR
			lower(COALESCE(legal_name, '')) LIKE ? OR
			lower(COALESCE(bitcoin_address, '')) LIKE ? OR
			lower(COALESCE(epm_cid, '')) LIKE ? OR
			lower(COALESCE(source, '')) LIKE ? OR
			lower(COALESCE(epm_json, '')) LIKE ?
		)`)
		for i := 0; i < 7; i++ {
			args = append(args, needle)
		}
	}

	sqlBuilder.WriteString(` ORDER BY updated_at DESC, peer_id ASC LIMIT ?`)
	args = append(args, limit)

	rows, err := s.db.Query(sqlBuilder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query directory: %w", err)
	}
	defer rows.Close()

	results := make([]DirectoryRecord, 0, limit)
	for rows.Next() {
		var record DirectoryRecord
		if err := rows.Scan(
			&record.Kind,
			&record.PeerID,
			&record.DN,
			&record.LegalName,
			&record.BitcoinAddress,
			&record.EPMCID,
			&record.Source,
			&record.UpdatedAt,
			&record.EPMJSON,
		); err != nil {
			return nil, fmt.Errorf("failed to scan directory record: %w", err)
		}
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory query iteration failed: %w", err)
	}

	return results, nil
}

// RebuildIndex scans all schema tables and repopulates sdn_record_index.
func (s *FlatSQLStore) RebuildIndex() (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summary := make(map[string]int64)

	for _, schemaName := range s.validator.Schemas() {
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			return nil, fmt.Errorf("invalid schema name %q: %w", schemaName, err)
		}
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
	return s.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: schemaName,
		Day:        day,
		NoradCatID: noradCatID,
		EntityID:   entityID,
		Limit:      limit,
	})
}

// QueryIndexedRecords returns records using materialized catalog/source indexes.
func (s *FlatSQLStore) QueryIndexedRecords(filter IndexedRecordQuery) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := sds.SchemaNameToTable(filter.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	if filter.Day != "" {
		if _, err := time.Parse("2006-01-02", filter.Day); err != nil {
			return nil, fmt.Errorf("invalid day %q (expected YYYY-MM-DD)", filter.Day)
		}
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return nil, errors.New("from time must be before to time")
	}

	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}

	query := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp, d.data, d.signature
		FROM %s d
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = d.cid
	`, tableName)

	args := []interface{}{filter.SchemaName}
	if filter.ProviderID != "" || filter.SourceName != "" || filter.BatchID != "" {
		query += `
		INNER JOIN sdn_record_source_tags tags
		  ON tags.schema_name = idx.schema_name AND tags.cid = idx.cid
		`
	}
	query += `
		WHERE 1=1
	`

	if filter.Day != "" {
		query += ` AND idx.epoch_day = ?`
		args = append(args, filter.Day)
	}

	if filter.NoradCatID != nil {
		query += ` AND idx.norad_cat_id = ?`
		args = append(args, int64(*filter.NoradCatID))
	}

	if filter.EntityID != "" {
		query += ` AND idx.entity_id = ?`
		args = append(args, filter.EntityID)
	}

	objectType := normalizeIndexEnum(filter.ObjectType)
	opsStatus := normalizeIndexEnum(filter.OpsStatusCode)
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		objectType = "PAYLOAD"
	}
	if objectType != "" {
		query += ` AND idx.object_type = ?`
		args = append(args, objectType)
	}
	if opsStatus != "" {
		query += ` AND idx.ops_status_code = ?`
		args = append(args, opsStatus)
	}
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		query += ` AND idx.ops_status_code IN ('OPERATIONAL', 'PARTIALLY_OPERATIONAL', 'BACKUP_STANDBY', 'SPARE', 'EXTENDED_MISSION', 'UNKNOWN')`
	}
	if filter.CAReadyResidentSet {
		query += ` AND idx.norad_cat_id IS NOT NULL`
	}
	if filter.From != nil {
		query += ` AND COALESCE(idx.epoch_unix, idx.source_timestamp) >= ?`
		args = append(args, filter.From.Unix())
	}
	if filter.To != nil {
		query += ` AND COALESCE(idx.epoch_unix, idx.source_timestamp) <= ?`
		args = append(args, filter.To.Unix())
	}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += ` AND tags.provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += ` AND tags.source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += ` AND tags.batch_id = ?`
		args = append(args, batchID)
	}

	query += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) DESC, d.cid ASC LIMIT ?`
	args = append(args, filter.Limit)

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

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	querySQL := fmt.Sprintf(`
		SELECT cid, peer_id, timestamp, data, signature
		FROM %s WHERE cid = ?
	`, tableName)

	var record Record
	var timestamp int64
	err = s.db.QueryRow(querySQL, cid).Scan(
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
	noradCatID    *uint32
	entityID      string
	objectType    string
	opsStatusCode string
	epochUnix     *int64
	epochDay      string
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
	var objectType interface{}
	if fields.objectType != "" {
		objectType = fields.objectType
	}
	var opsStatusCode interface{}
	if fields.opsStatusCode != "" {
		opsStatusCode = fields.opsStatusCode
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
			schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, cid) DO UPDATE SET
			norad_cat_id = excluded.norad_cat_id,
			entity_id = excluded.entity_id,
			object_type = excluded.object_type,
			ops_status_code = excluded.ops_status_code,
			epoch_unix = excluded.epoch_unix,
			epoch_day = excluded.epoch_day,
			source_timestamp = excluded.source_timestamp
	`, schemaName, cid, norad, entity, objectType, opsStatusCode, epoch, day, sourceTimestamp)
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
		out.entityID = strings.TrimSpace(string(omm.OBJECT_ID()))

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
		if objectType := strings.TrimSpace(cat.OBJECT_TYPE().String()); objectType != "" && objectType != "UNKNOWN" {
			out.objectType = objectType
		}
		if opsStatusCode := strings.TrimSpace(cat.OPS_STATUS_CODE().String()); opsStatusCode != "" && opsStatusCode != "UNKNOWN" {
			out.opsStatusCode = opsStatusCode
		}

	default:
		// No structured extraction for this schema yet.
	}

	return out, nil
}

func normalizeIndexEnum(value string) string {
	normalized := strings.TrimSpace(strings.ToUpper(value))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	normalized = strings.ReplaceAll(normalized, "-", "_")
	if normalized == "UNKNOWN" {
		return ""
	}
	return normalized
}

func parseOMM(data []byte) (omm *OMM.OMM, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed OMM buffer: %v", r)
		}
	}()
	switch {
	case OMM.SizePrefixedOMMBufferHasIdentifier(data):
		return OMM.GetSizePrefixedRootAsOMM(data, 0), nil
	case OMM.OMMBufferHasIdentifier(data):
		return OMM.GetRootAsOMM(data, 0), nil
	default:
		return nil, errors.New("invalid OMM buffer")
	}
}

func parseMPE(data []byte) (mpe *MPE.MPE, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed MPE buffer: %v", r)
		}
	}()
	switch {
	case MPE.SizePrefixedMPEBufferHasIdentifier(data):
		return MPE.GetSizePrefixedRootAsMPE(data, 0), nil
	case MPE.MPEBufferHasIdentifier(data):
		return MPE.GetRootAsMPE(data, 0), nil
	default:
		return nil, errors.New("invalid MPE buffer")
	}
}

func parseCAT(data []byte) (cat *CAT.CAT, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed CAT buffer: %v", r)
		}
	}()
	switch {
	case CAT.SizePrefixedCATBufferHasIdentifier(data):
		return CAT.GetSizePrefixedRootAsCAT(data, 0), nil
	case CAT.CATBufferHasIdentifier(data):
		return CAT.GetRootAsCAT(data, 0), nil
	default:
		return nil, errors.New("invalid CAT buffer")
	}
}

// SchemaDateRange holds catalog metadata for a single schema.
type SchemaDateRange struct {
	Schema      string
	RecordCount int64
	OldestEpoch *time.Time
	NewestEpoch *time.Time
	TotalBytes  int64
}

// SchemaDateRanges returns catalog metadata for all schemas with stored data.
func (s *FlatSQLStore) SchemaDateRanges() ([]SchemaDateRange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT schema_name, COUNT(*) as cnt,
		       MIN(epoch_unix) as min_epoch,
		       MAX(epoch_unix) as max_epoch
		FROM sdn_record_index
		GROUP BY schema_name
		ORDER BY schema_name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query schema date ranges: %w", err)
	}
	defer rows.Close()

	var ranges []SchemaDateRange
	for rows.Next() {
		var r SchemaDateRange
		var minEpoch, maxEpoch sql.NullInt64
		if err := rows.Scan(&r.Schema, &r.RecordCount, &minEpoch, &maxEpoch); err != nil {
			return nil, fmt.Errorf("failed to scan schema date range: %w", err)
		}
		if minEpoch.Valid && minEpoch.Int64 > 0 {
			t := time.Unix(minEpoch.Int64, 0).UTC()
			r.OldestEpoch = &t
		}
		if maxEpoch.Valid && maxEpoch.Int64 > 0 {
			t := time.Unix(maxEpoch.Int64, 0).UTC()
			r.NewestEpoch = &t
		}
		ranges = append(ranges, r)
	}

	// Compute total bytes from per-schema tables.
	for i := range ranges {
		tableName, err := sds.SchemaNameToTable(ranges[i].Schema)
		if err != nil {
			continue
		}
		var totalBytes sql.NullInt64
		err = s.db.QueryRow(fmt.Sprintf(`SELECT SUM(LENGTH(data)) FROM %s`, tableName)).Scan(&totalBytes)
		if err == nil && totalBytes.Valid {
			ranges[i].TotalBytes = totalBytes.Int64
		}
	}

	return ranges, nil
}

// PeerStorageBytes returns the total stored bytes for a given peer across all schemas.
func (s *FlatSQLStore) PeerStorageBytes(peerID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int64
	for _, schemaName := range s.validator.Schemas() {
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			continue
		}
		var bytes sql.NullInt64
		err = s.db.QueryRow(fmt.Sprintf(`SELECT SUM(LENGTH(data)) FROM %s WHERE peer_id = ?`, tableName), peerID).Scan(&bytes)
		if err == nil && bytes.Valid {
			total += bytes.Int64
		}
	}

	return total, nil
}

// LogHeadInfo holds the latest log state for a (publisher, schema) pair.
type LogHeadInfo struct {
	PublisherPeerID string
	SchemaType      string
	Sequence        uint64
	EntryHash       string
	RecordCID       string
	Timestamp       int64
}

// UpsertLogIndex inserts or updates a publication log index entry.
func (s *FlatSQLStore) UpsertLogIndex(publisherPeerID, schemaType string, sequence uint64, entryHash, recordCID, plgCID, epochDay string, timestamp int64) error {
	_, err := s.db.Exec(`
		INSERT INTO sdn_log_index (
			publisher_peer_id, schema_type, sequence, entry_hash, record_cid, plg_cid, epoch_day, timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(publisher_peer_id, schema_type, sequence) DO UPDATE SET
			entry_hash = excluded.entry_hash,
			record_cid = excluded.record_cid,
			plg_cid = excluded.plg_cid,
			epoch_day = excluded.epoch_day,
			timestamp = excluded.timestamp
	`, publisherPeerID, schemaType, sequence, entryHash, recordCID, plgCID, epochDay, timestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert log index: %w", err)
	}
	return nil
}

// GetLogHead returns the latest sequence and entry hash for a (publisher, schema) log.
func (s *FlatSQLStore) GetLogHead(publisherPeerID, schemaType string) (uint64, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sequence uint64
	var entryHash string
	err := s.db.QueryRow(`
		SELECT sequence, entry_hash
		FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ?
		ORDER BY sequence DESC
		LIMIT 1
	`, publisherPeerID, schemaType).Scan(&sequence, &entryHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, "", nil
		}
		return 0, "", fmt.Errorf("failed to get log head: %w", err)
	}
	return sequence, entryHash, nil
}

// QueryLogEntries returns PLG FlatBuffer data for entries after sinceSequence.
func (s *FlatSQLStore) QueryLogEntries(publisherPeerID, schemaType string, sinceSequence uint64, limit int) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := s.db.Query(`
		SELECT p.data
		FROM sdn_log_index li
		INNER JOIN sds_plg p ON p.cid = li.plg_cid
		WHERE li.publisher_peer_id = ?
		  AND li.schema_type = ?
		  AND li.sequence > ?
		ORDER BY li.sequence ASC
		LIMIT ?
	`, publisherPeerID, schemaType, sinceSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	defer rows.Close()

	var results [][]byte
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			log.Warnf("Failed to scan log entry: %v", err)
			continue
		}
		results = append(results, data)
	}
	return results, nil
}

// QueryLogHeads returns the latest log head info for all publishers of a schema type.
func (s *FlatSQLStore) QueryLogHeads(schemaType string) ([]LogHeadInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT li.publisher_peer_id, li.schema_type, li.sequence, li.entry_hash, li.record_cid, li.timestamp
		FROM sdn_log_index li
		INNER JOIN (
			SELECT publisher_peer_id, schema_type, MAX(sequence) as max_seq
			FROM sdn_log_index
			WHERE schema_type = ?
			GROUP BY publisher_peer_id, schema_type
		) latest ON li.publisher_peer_id = latest.publisher_peer_id
		       AND li.schema_type = latest.schema_type
		       AND li.sequence = latest.max_seq
		ORDER BY li.publisher_peer_id
	`, schemaType)
	if err != nil {
		return nil, fmt.Errorf("failed to query log heads: %w", err)
	}
	defer rows.Close()

	var heads []LogHeadInfo
	for rows.Next() {
		var h LogHeadInfo
		if err := rows.Scan(&h.PublisherPeerID, &h.SchemaType, &h.Sequence, &h.EntryHash, &h.RecordCID, &h.Timestamp); err != nil {
			log.Warnf("Failed to scan log head: %v", err)
			continue
		}
		heads = append(heads, h)
	}
	return heads, nil
}

// LogRecordCount returns the total number of log entries for a (publisher, schema) pair.
func (s *FlatSQLStore) LogRecordCount(publisherPeerID, schemaType string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count uint64
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ?
	`, publisherPeerID, schemaType).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count log entries: %w", err)
	}
	return count, nil
}

// LogEpochRange returns the oldest and newest epoch days for a (publisher, schema) log.
func (s *FlatSQLStore) LogEpochRange(publisherPeerID, schemaType string) (oldest, newest string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	err = s.db.QueryRow(`
		SELECT COALESCE(MIN(epoch_day), ''), COALESCE(MAX(epoch_day), '')
		FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ?
		  AND epoch_day IS NOT NULL AND epoch_day != ''
	`, publisherPeerID, schemaType).Scan(&oldest, &newest)
	if err != nil {
		return "", "", fmt.Errorf("failed to get log epoch range: %w", err)
	}
	return oldest, newest, nil
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
