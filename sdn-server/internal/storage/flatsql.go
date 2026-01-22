// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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

	_, err := s.db.Exec(insertSQL, cid, peerID, time.Now().Unix(), data, signature)
	if err != nil {
		return "", fmt.Errorf("failed to store data: %w", err)
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
