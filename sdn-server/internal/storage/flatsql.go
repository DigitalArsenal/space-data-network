// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3" // SQLite driver
	"golang.org/x/crypto/scrypt"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var log = logging.Logger("storage")

const (
	flatSQLStreamDirName         = "flatsql-streams"
	legacyBlobMigrationBatchSize = 50000
	localEPMStoreSalt            = "space-data-network-local-epm-store-v1"
)

// FlatSQLStore provides SQLite storage with FlatBuffer virtual tables.
type FlatSQLStore struct {
	db        *sql.DB
	validator *sds.Validator
	dbPath    string
	basePath  string
	streamDir string
	mu        sync.RWMutex
}

// NewFlatSQLStore creates a new FlatSQL storage instance.
func NewFlatSQLStore(basePath string, validator *sds.Validator) (*FlatSQLStore, error) {
	// Ensure directory exists
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}
	streamDir := filepath.Join(basePath, flatSQLStreamDirName)
	if err := os.MkdirAll(streamDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create FlatSQL stream directory: %w", err)
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
		basePath:  basePath,
		streamDir: streamDir,
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
	recordIndexExisted, err := s.tableExists("sdn_record_index")
	if err != nil {
		return err
	}
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

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_lookup", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_lookup
		ON sdn_record_index (schema_name, epoch_day, norad_cat_id, entity_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create composite index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_norad", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_norad
		ON sdn_record_index (schema_name, norad_cat_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create norad index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_entity", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_entity
		ON sdn_record_index (schema_name, entity_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create entity index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_catalog_filters", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_catalog_filters
		ON sdn_record_index (schema_name, object_type, ops_status_code, norad_cat_id)
	`); err != nil {
		return fmt.Errorf("failed to create catalog filter index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_time_window", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_time_window
		ON sdn_record_index (schema_name, epoch_unix, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create time window index: %w", err)
	}

	sourceTagsExisted, err := s.initSourceTagsTable()
	if err != nil {
		return fmt.Errorf("failed to create source tags table: %w", err)
	}

	if !sourceTagsExisted {
		log.Infof("Building FlatSQL source tag producer uniqueness index")
	}
	if err := s.createStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_unique", sourceTagsExisted, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sdn_record_source_tags_unique
		ON sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
	`); err != nil {
		return fmt.Errorf("failed to create source tags producer uniqueness index: %w", err)
	}
	if err := s.createStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_lookup", sourceTagsExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_lookup
		ON sdn_record_source_tags (schema_name, provider_id, source_name, batch_id)
	`); err != nil {
		return fmt.Errorf("failed to create source tags lookup index: %w", err)
	}
	if err := s.createRequiredStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_source_cid", `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_source_cid
		ON sdn_record_source_tags (schema_name, provider_id, source_name, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags source/cid index: %w", err)
	}
	if err := s.createRequiredStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_batch_cid", `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_batch_cid
		ON sdn_record_source_tags (schema_name, provider_id, source_name, batch_id, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags batch/cid index: %w", err)
	}
	if err := s.createStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_recent", sourceTagsExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_recent
		ON sdn_record_source_tags (schema_name, created_at DESC, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags recent index: %w", err)
	}
	if err := s.initSourceSummaryTable(); err != nil {
		return fmt.Errorf("failed to create source summary table: %w", err)
	}
	if err := s.initDatasetShardPublicationTable(); err != nil {
		return fmt.Errorf("failed to create dataset shard publication table: %w", err)
	}

	// Directory index for node/user EPM records.
	directoryExisted, err := s.tableExists("sdn_directory")
	if err != nil {
		return err
	}
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

	if err := s.createStartupIndex("sdn_directory", "idx_sdn_directory_search", directoryExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_directory_search
		ON sdn_directory (kind, dn, legal_name, bitcoin_address)
	`); err != nil {
		return fmt.Errorf("failed to create directory search index: %w", err)
	}

	if err := s.createStartupIndex("sdn_directory", "idx_sdn_directory_updated", directoryExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_directory_updated
		ON sdn_directory (kind, updated_at DESC)
	`); err != nil {
		return fmt.Errorf("failed to create directory updated index: %w", err)
	}

	// Local EPM source-of-truth records. Only the size-prefixed EPM FlatBuffer
	// bytes are persisted; editable forms and JSON views are derived from the
	// FlatBuffer at the API edge.
	if err := s.initLocalEPMTable(); err != nil {
		return fmt.Errorf("failed to create local EPM table: %w", err)
	}

	// Publication log index for PLG hash-chained logs.
	logIndexExisted, err := s.tableExists("sdn_log_index")
	if err != nil {
		return err
	}
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

	if err := s.createStartupIndex("sdn_log_index", "idx_sdn_log_index_head", logIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_log_index_head
		ON sdn_log_index (publisher_peer_id, schema_type, sequence DESC)
	`); err != nil {
		return fmt.Errorf("failed to create log head index: %w", err)
	}

	if err := s.createStartupIndex("sdn_log_index", "idx_sdn_log_index_epoch", logIndexExisted, `
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
		if err := s.migrateCanonicalSchemaTableIfNeeded(schemaName, tableName); err != nil {
			return err
		}
		tableExisted, err := s.tableExists(tableName)
		if err != nil {
			return err
		}
		if err := s.createSchemaMetadataTable(tableName); err != nil {
			return fmt.Errorf("failed to create table %s: %w", tableName, err)
		}
		if err := s.migrateLegacySchemaTable(schemaName, tableName); err != nil {
			return err
		}

		// Create index on peer_id and timestamp
		indexName, indexSQL := schemaPeerTimeIndexSQL(tableName)
		if err := s.createStartupIndex(tableName, indexName, tableExisted, indexSQL); err != nil {
			if tableExisted {
				return err
			}
			log.Warnf("Failed to create index for %s: %v", tableName, err)
		}

		log.Debugf("Initialized table: %s", tableName)
		if err := s.ensureSourceSummaryForSchema(schemaName, tableName); err != nil {
			return err
		}
	}

	return nil
}

func (s *FlatSQLStore) createStartupIndex(tableName, indexName string, tableExisted bool, createSQL string) error {
	indexExists, err := s.indexExists(indexName)
	if err != nil {
		return err
	}
	if indexExists {
		return nil
	}
	if tableExisted {
		log.Warnf("Skipping synchronous startup creation of missing index %s on existing table %s; rebuild indexes during maintenance", indexName, tableName)
		return nil
	}
	if _, err := s.db.Exec(createSQL); err != nil {
		return err
	}
	return nil
}

func (s *FlatSQLStore) createRequiredStartupIndex(tableName, indexName string, createSQL string) error {
	indexExists, err := s.indexExists(indexName)
	if err != nil {
		return err
	}
	if indexExists {
		return nil
	}
	log.Infof("Building required FlatSQL index %s on %s", indexName, tableName)
	if _, err := s.db.Exec(createSQL); err != nil {
		return err
	}
	return nil
}

func (s *FlatSQLStore) initSourceTagsTable() (bool, error) {
	existed, err := s.tableExists("sdn_record_source_tags")
	if err != nil {
		return false, err
	}
	if !existed {
		_, err := s.db.Exec(sourceTagsTableSQL("sdn_record_source_tags"))
		return false, err
	}
	needsMigration, err := s.sourceTagsTableNeedsMigration()
	if err != nil {
		return true, err
	}
	if !needsMigration {
		return true, nil
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS sdn_record_source_tags_next`); err != nil {
		return true, err
	}
	if _, err := s.db.Exec(sourceTagsMigrationTableSQL("sdn_record_source_tags_next")); err != nil {
		return true, err
	}
	columns, err := s.tableColumnSet("sdn_record_source_tags")
	if err != nil {
		return true, err
	}
	producerPeerExpr := "TRIM(provider_id)"
	if columns["producer_peer_id"] {
		producerPeerExpr = "COALESCE(NULLIF(producer_peer_id, ''), TRIM(provider_id))"
	}
	producerPublicKeyExpr := "COALESCE(content_key_id, '')"
	if columns["producer_public_key"] {
		producerPublicKeyExpr = "COALESCE(NULLIF(producer_public_key, ''), COALESCE(content_key_id, ''))"
	}
	log.Infof("Migrating FlatSQL source tags to producer-aware provenance keys")
	if _, err := s.db.Exec(fmt.Sprintf(`
		INSERT OR IGNORE INTO sdn_record_source_tags_next (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key, created_at
		)
		SELECT
			schema_name,
			cid,
			TRIM(provider_id),
			TRIM(source_name),
			source_url,
			COALESCE(batch_id, ''),
			COALESCE(content_key_id, ''),
			%s,
			%s,
			created_at
		FROM sdn_record_source_tags
	`, producerPeerExpr, producerPublicKeyExpr)); err != nil {
		return true, err
	}
	if _, err := s.db.Exec(`DROP TABLE sdn_record_source_tags`); err != nil {
		return true, err
	}
	if _, err := s.db.Exec(`ALTER TABLE sdn_record_source_tags_next RENAME TO sdn_record_source_tags`); err != nil {
		return true, err
	}
	log.Infof("Migrated FlatSQL source tags to producer-aware provenance keys")
	return false, nil
}

func sourceTagsTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			source_url TEXT,
			batch_id TEXT NOT NULL DEFAULT '',
			content_key_id TEXT NOT NULL DEFAULT '',
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%%s', 'now')),
			PRIMARY KEY (
				schema_name, cid, provider_id, source_name, batch_id,
				content_key_id, producer_peer_id, producer_public_key
			)
		)
	`, tableName)
}

func sourceTagsMigrationTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			source_url TEXT,
			batch_id TEXT NOT NULL DEFAULT '',
			content_key_id TEXT NOT NULL DEFAULT '',
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%%s', 'now'))
		)
	`, tableName)
}

func (s *FlatSQLStore) sourceTagsTableNeedsMigration() (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(sdn_record_source_tags)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	columns := map[string]int{}
	present := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		columns[name] = pk
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	required := []string{
		"schema_name", "cid", "provider_id", "source_name", "batch_id",
		"content_key_id", "producer_peer_id", "producer_public_key",
	}
	for _, name := range required {
		if !present[name] {
			return true, nil
		}
	}
	hasPrimaryKey := true
	for _, name := range required {
		if columns[name] == 0 {
			hasPrimaryKey = false
			break
		}
	}
	if hasPrimaryKey {
		return false, nil
	}
	hasUniqueIndex, err := s.indexHasColumns("idx_sdn_record_source_tags_unique", required)
	if err != nil {
		return false, err
	}
	return !hasUniqueIndex, nil
}

func (s *FlatSQLStore) indexHasColumns(indexName string, expected []string) (bool, error) {
	exists, err := s.indexExists(indexName)
	if err != nil || !exists {
		return exists, err
	}
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA index_info(%s)`, indexName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	actual := make([]string, 0, len(expected))
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return false, err
		}
		actual = append(actual, name)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(actual) != len(expected) {
		return false, nil
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return false, nil
		}
	}
	return true, nil
}

func (s *FlatSQLStore) initSourceSummaryTable() error {
	sourceSummaryExisted, err := s.tableExists("sdn_record_source_summary")
	if err != nil {
		return err
	}
	if sourceSummaryExisted {
		columns, err := s.tableColumnSet("sdn_record_source_summary")
		if err != nil {
			return err
		}
		if !columns["producer_peer_id"] || !columns["producer_public_key"] {
			if _, err := s.db.Exec(`DROP TABLE sdn_record_source_summary`); err != nil {
				return err
			}
			sourceSummaryExisted = false
		}
	}
	_, err = s.db.Exec(sourceSummaryTableSQL("sdn_record_source_summary"))
	if err != nil {
		return fmt.Errorf("failed to create source summary table: %w", err)
	}
	if err := s.createStartupIndex("sdn_record_source_summary", "idx_sdn_record_source_summary_schema", sourceSummaryExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_summary_schema
		ON sdn_record_source_summary (schema_name)
	`); err != nil {
		return fmt.Errorf("failed to create source summary schema index: %w", err)
	}
	return nil
}

func sourceSummaryTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			batch_id TEXT NOT NULL,
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT '',
			record_count INTEGER NOT NULL,
			total_bytes INTEGER NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT (strftime('%%s', 'now')),
			PRIMARY KEY (
				schema_name, provider_id, source_name, batch_id,
				producer_peer_id, producer_public_key
			)
		)
	`, tableName)
}

func (s *FlatSQLStore) createSchemaMetadataTable(tableName string) error {
	_, err := s.db.Exec(schemaMetadataTableSQL(tableName))
	return err
}

func schemaMetadataTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			stream_path TEXT NOT NULL,
			stream_offset INTEGER NOT NULL,
			record_length INTEGER NOT NULL,
			signature_hex TEXT,
			created_at INTEGER DEFAULT (strftime('%%s', 'now')),
			UNIQUE(cid)
		)
	`, tableName)
}

func (s *FlatSQLStore) migrateCanonicalSchemaTableIfNeeded(schemaName, tableName string) error {
	exists, err := s.tableExists(tableName)
	if err != nil || !exists {
		return err
	}
	columns, err := s.tableColumnSet(tableName)
	if err != nil {
		return err
	}
	if !columns["data"] && columns["stream_path"] && columns["stream_offset"] && columns["record_length"] {
		return nil
	}
	if !columns["data"] {
		return fmt.Errorf("schema table %s has unsupported FlatSQL metadata layout", tableName)
	}
	return s.migrateBlobSchemaTableInPlace(schemaName, tableName)
}

func (s *FlatSQLStore) migrateLegacySchemaTable(schemaName, tableName string) error {
	legacyTableName := legacySchemaTableName(schemaName)
	if legacyTableName == tableName {
		return nil
	}
	legacyExists, err := s.tableExists(legacyTableName)
	if err != nil {
		return err
	}
	if !legacyExists {
		return nil
	}
	if err := s.createSchemaMetadataTable(tableName); err != nil {
		return fmt.Errorf("create canonical schema table %s before legacy migration: %w", tableName, err)
	}
	hasCanonicalRows, err := s.schemaMetadataTableHasRows(tableName)
	if err != nil {
		return err
	}
	if hasCanonicalRows {
		log.Warnf(
			"Deferring legacy FlatSQL table %s migration into %s because canonical stream metadata already exists; run maintenance migration before dropping legacy bytes",
			legacyTableName,
			tableName,
		)
		return nil
	}
	if err := s.copyBlobSchemaRowsToMetadataTable(schemaName, legacyTableName, tableName); err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf(`DROP TABLE %s`, legacyTableName)); err != nil {
		return fmt.Errorf("drop migrated legacy schema table %s: %w", legacyTableName, err)
	}
	log.Infof("Merged legacy FlatSQL table %s into %s", legacyTableName, tableName)
	return nil
}

func (s *FlatSQLStore) schemaMetadataTableHasRows(tableName string) (bool, error) {
	var cid string
	err := s.db.QueryRow(fmt.Sprintf(`SELECT cid FROM %s LIMIT 1`, tableName)).Scan(&cid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect schema metadata table %s: %w", tableName, err)
	}
	return true, nil
}

func (s *FlatSQLStore) migrateBlobSchemaTableInPlace(schemaName, tableName string) error {
	nextTable := tableName + "_stream_migration"
	if _, err := s.db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, nextTable)); err != nil {
		return fmt.Errorf("drop stale schema migration table %s: %w", nextTable, err)
	}
	if _, err := s.db.Exec(schemaMetadataTableSQL(nextTable)); err != nil {
		return fmt.Errorf("create schema migration table %s: %w", nextTable, err)
	}
	if err := s.copyBlobSchemaRowsToMetadataTable(schemaName, tableName, nextTable); err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf(`DROP TABLE %s`, tableName)); err != nil {
		return fmt.Errorf("drop migrated schema table %s: %w", tableName, err)
	}
	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, nextTable, tableName)); err != nil {
		return fmt.Errorf("rename schema migration table %s to %s: %w", nextTable, tableName, err)
	}
	indexName, indexSQL := schemaPeerTimeIndexSQL(tableName)
	if _, err := s.db.Exec(indexSQL); err != nil {
		return fmt.Errorf("create migrated schema index %s: %w", indexName, err)
	}
	log.Infof("Migrated FlatSQL schema table %s from SQLite BLOB rows to stream metadata", tableName)
	return nil
}

func schemaPeerTimeIndexSQL(tableName string) (string, string) {
	indexName := fmt.Sprintf("idx_%s_peer_time", tableName)
	indexSQL := fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS %s ON %s (peer_id, timestamp)
	`, indexName, tableName)
	return indexName, indexSQL
}

func (s *FlatSQLStore) copyBlobSchemaRowsToMetadataTable(schemaName, sourceTable, targetTable string) error {
	columns, err := s.tableColumnSet(sourceTable)
	if err != nil {
		return err
	}
	if !columns["data"] {
		return fmt.Errorf("legacy schema table %s has no data column", sourceTable)
	}
	signatureExpr := "NULL"
	if columns["signature"] {
		signatureExpr = "src.signature"
	}
	createdAtExpr := "src.timestamp"
	if columns["created_at"] {
		createdAtExpr = "src.created_at"
	}

	log.Infof("Migrating FlatSQL %s records from SQLite BLOB table %s to stream metadata", schemaName, sourceTable)

	appender, err := s.newFlatSQLStreamAppender(schemaName)
	if err != nil {
		return fmt.Errorf("open migrated %s FlatSQL stream: %w", schemaName, err)
	}
	defer appender.Close()

	var migratedRows int64
	var lastRowID int64
	for {
		batchRows, err := func() (int64, error) {
			tx, err := s.db.Begin()
			if err != nil {
				return 0, fmt.Errorf("begin schema table migration %s: %w", sourceTable, err)
			}
			committed := false
			defer func() {
				if !committed {
					_ = tx.Rollback()
				}
			}()

			rows, err := tx.Query(fmt.Sprintf(`
			SELECT src.rowid, src.cid, src.peer_id, src.timestamp, src.data, %s, %s
			FROM %s AS src
			LEFT JOIN %s AS dst ON dst.cid = src.cid
			WHERE src.rowid > ? AND dst.cid IS NULL
			ORDER BY src.rowid ASC
			LIMIT ?
		`, signatureExpr, createdAtExpr, sourceTable, targetTable), lastRowID, legacyBlobMigrationBatchSize)
			if err != nil {
				return 0, fmt.Errorf("query legacy schema table %s: %w", sourceTable, err)
			}

			stmt, err := tx.Prepare(fmt.Sprintf(`
			INSERT OR IGNORE INTO %s (
				cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, targetTable))
			if err != nil {
				_ = rows.Close()
				return 0, fmt.Errorf("prepare migrated schema metadata insert %s: %w", targetTable, err)
			}

			var batchRows int64
			for rows.Next() {
				var rowID int64
				var cid, peerID string
				var timestamp, createdAt int64
				var data []byte
				var signature []byte
				if err := rows.Scan(&rowID, &cid, &peerID, &timestamp, &data, &signature, &createdAt); err != nil {
					_ = rows.Close()
					_ = stmt.Close()
					return 0, fmt.Errorf("scan legacy schema table %s: %w", sourceTable, err)
				}
				lastRowID = rowID
				streamPath, streamOffset, recordLength, err := appender.Append(data)
				if err != nil {
					_ = rows.Close()
					_ = stmt.Close()
					return 0, fmt.Errorf("append migrated %s record %s to FlatSQL stream: %w", schemaName, cid, err)
				}
				var signatureHex any
				if len(signature) > 0 {
					signatureHex = hex.EncodeToString(signature)
				}
				if createdAt <= 0 {
					createdAt = timestamp
				}
				if _, err := stmt.Exec(cid, peerID, timestamp, streamPath, streamOffset, recordLength, signatureHex, createdAt); err != nil {
					_ = rows.Close()
					_ = stmt.Close()
					return 0, fmt.Errorf("insert migrated %s metadata row %s: %w", schemaName, cid, err)
				}
				batchRows++
				migratedRows++
				if migratedRows%100000 == 0 {
					log.Infof("Migrated %d FlatSQL %s records to stream metadata", migratedRows, schemaName)
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				_ = stmt.Close()
				return 0, fmt.Errorf("iterate legacy schema table %s: %w", sourceTable, err)
			}
			if err := rows.Close(); err != nil {
				_ = stmt.Close()
				return 0, fmt.Errorf("close legacy schema rows %s: %w", sourceTable, err)
			}
			if err := stmt.Close(); err != nil {
				return 0, fmt.Errorf("close migrated schema metadata insert %s: %w", targetTable, err)
			}
			if err := tx.Commit(); err != nil {
				return 0, fmt.Errorf("commit migrated schema table %s: %w", sourceTable, err)
			}
			committed = true
			return batchRows, nil
		}()
		if err != nil {
			return err
		}
		if batchRows == 0 {
			break
		}
	}
	log.Infof("Migrated %d FlatSQL %s records from SQLite BLOB table %s to stream metadata", migratedRows, schemaName, sourceTable)
	return nil
}

func legacySchemaTableName(schemaName string) string {
	name := strings.TrimSuffix(schemaName, ".fbs")
	name = strings.ToLower(name)
	return "sds_" + name
}

func (s *FlatSQLStore) tableExists(tableName string) (bool, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, tableName).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to inspect table %s: %w", tableName, err)
	}
	return true, nil
}

func (s *FlatSQLStore) indexExists(indexName string) (bool, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to inspect index %s: %w", indexName, err)
	}
	return true, nil
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

func (s *FlatSQLStore) initLocalEPMTable() error {
	const createSQL = `
		CREATE TABLE IF NOT EXISTS sdn_local_epms (
			peer_id TEXT PRIMARY KEY,
			schema_name TEXT NOT NULL DEFAULT 'EPM.fbs',
			encrypted_epm_bytes TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`

	exists, err := s.tableExists("sdn_local_epms")
	if err != nil {
		return err
	}
	if !exists {
		_, err := s.db.Exec(createSQL)
		return err
	}

	columns, err := s.tableColumnSet("sdn_local_epms")
	if err != nil {
		return err
	}
	if !columns["encrypted_profile_json"] && !columns["encrypted_epm_json"] {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE sdn_local_epms_next (
			peer_id TEXT PRIMARY KEY,
			schema_name TEXT NOT NULL DEFAULT 'EPM.fbs',
			encrypted_epm_bytes TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO sdn_local_epms_next (
			peer_id, schema_name, encrypted_epm_bytes, updated_at
		)
		SELECT
			peer_id,
			COALESCE(NULLIF(schema_name, ''), 'EPM.fbs'),
			encrypted_epm_bytes,
			updated_at
		FROM sdn_local_epms
		WHERE encrypted_epm_bytes IS NOT NULL AND encrypted_epm_bytes <> ''
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE sdn_local_epms`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE sdn_local_epms_next RENAME TO sdn_local_epms`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FlatSQLStore) tableColumnSet(tableName string) (map[string]bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return nil, fmt.Errorf("failed to inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("failed to scan %s column metadata: %w", tableName, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed iterating %s columns: %w", tableName, err)
	}
	return columns, nil
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertSchemaMetadata(exec sqlExecer, tableName, cid, peerID string, timestamp int64, streamPath string, streamOffset, recordLength int64, signature []byte, createdAt int64) error {
	var signatureHex any
	if len(signature) > 0 {
		signatureHex = hex.EncodeToString(signature)
	}
	if createdAt <= 0 {
		createdAt = timestamp
	}
	_, err := exec.Exec(fmt.Sprintf(`
		INSERT OR IGNORE INTO %s (
			cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, tableName), cid, peerID, timestamp, streamPath, streamOffset, recordLength, signatureHex, createdAt)
	return err
}

type flatSQLStreamAppender struct {
	file         *os.File
	relativePath string
	offset       int64
}

func (s *FlatSQLStore) newFlatSQLStreamAppender(schemaName string) (*flatSQLStreamAppender, error) {
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, err
	}
	relativePath := filepath.Join(flatSQLStreamDirName, tableName+".flatsql")
	absolutePath := filepath.Join(s.basePath, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &flatSQLStreamAppender{
		file:         file,
		relativePath: relativePath,
		offset:       info.Size(),
	}, nil
}

func (a *flatSQLStreamAppender) Append(data []byte) (string, int64, int64, error) {
	if len(data) > int(^uint32(0)) {
		return "", 0, 0, fmt.Errorf("record exceeds uint32 FlatSQL stream frame length")
	}
	offset := a.offset
	var sizePrefix [4]byte
	binary.LittleEndian.PutUint32(sizePrefix[:], uint32(len(data)))
	if _, err := a.file.Write(sizePrefix[:]); err != nil {
		return "", 0, 0, err
	}
	if _, err := a.file.Write(data); err != nil {
		return "", 0, 0, err
	}
	a.offset += int64(len(sizePrefix) + len(data))
	return a.relativePath, offset, int64(len(data)), nil
}

func (a *flatSQLStreamAppender) Close() error {
	return a.file.Close()
}

func (s *FlatSQLStore) appendFlatSQLStreamRecord(schemaName string, data []byte) (string, int64, int64, error) {
	appender, err := s.newFlatSQLStreamAppender(schemaName)
	if err != nil {
		return "", 0, 0, err
	}
	defer appender.Close()
	return appender.Append(data)
}

func (s *FlatSQLStore) readFlatSQLStreamRecord(streamPath string, streamOffset, recordLength int64) ([]byte, error) {
	if streamOffset < 0 {
		return nil, fmt.Errorf("negative FlatSQL stream offset %d", streamOffset)
	}
	if recordLength < 0 || recordLength > int64(^uint32(0)) {
		return nil, fmt.Errorf("invalid FlatSQL record length %d", recordLength)
	}
	clean := filepath.Clean(streamPath)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid FlatSQL stream path %q", streamPath)
	}
	absolutePath := filepath.Join(s.basePath, clean)
	file, err := os.Open(absolutePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var sizePrefix [4]byte
	if _, err := file.ReadAt(sizePrefix[:], streamOffset); err != nil {
		return nil, err
	}
	length := int64(binary.LittleEndian.Uint32(sizePrefix[:]))
	if length != recordLength {
		return nil, fmt.Errorf("FlatSQL stream frame length = %d, want %d", length, recordLength)
	}
	data := make([]byte, recordLength)
	if _, err := file.ReadAt(data, streamOffset+4); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *FlatSQLStore) hydrateRecordData(record *Record, streamPath string, streamOffset, recordLength int64, signatureHex sql.NullString) error {
	record.StreamPath = streamPath
	record.StreamOffset = streamOffset
	record.RecordLength = recordLength
	data, err := s.readFlatSQLStreamRecord(streamPath, streamOffset, recordLength)
	if err != nil {
		return err
	}
	record.Data = data
	if signatureHex.Valid && strings.TrimSpace(signatureHex.String) != "" {
		signature, err := hex.DecodeString(strings.TrimSpace(signatureHex.String))
		if err != nil {
			return fmt.Errorf("decode signature_hex for %s: %w", record.CID, err)
		}
		record.Signature = signature
	}
	return nil
}

func (s *FlatSQLStore) ensureSourceSummaryForSchema(schemaName, tableName string) error {
	var existing int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM sdn_record_source_summary
		WHERE schema_name = ?
	`, schemaName).Scan(&existing); err != nil {
		return fmt.Errorf("inspect source summary for %s: %w", schemaName, err)
	}
	if existing > 0 {
		return nil
	}

	var hasTag int
	if err := s.db.QueryRow(`
		SELECT 1
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		LIMIT 1
	`, schemaName).Scan(&hasTag); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("inspect source tags for %s: %w", schemaName, err)
	}
	return s.rebuildSourceSummaryForSchema(schemaName, tableName)
}

func (s *FlatSQLStore) rebuildSourceSummaryForSchema(schemaName, tableName string) error {
	if _, err := s.db.Exec(`DELETE FROM sdn_record_source_summary WHERE schema_name = ?`, schemaName); err != nil {
		return fmt.Errorf("clear source summary for %s: %w", schemaName, err)
	}
	if _, err := s.db.Exec(fmt.Sprintf(`
		INSERT INTO sdn_record_source_summary (
			schema_name, provider_id, source_name, batch_id, producer_peer_id,
			producer_public_key, record_count, total_bytes, updated_at
		)
		SELECT
			tags.schema_name,
			tags.provider_id,
			tags.source_name,
			COALESCE(tags.batch_id, ''),
			COALESCE(tags.producer_peer_id, ''),
			COALESCE(tags.producer_public_key, ''),
			COUNT(*),
			COALESCE(SUM(records.record_length), 0),
			strftime('%%s', 'now')
		FROM sdn_record_source_tags tags
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ?
		GROUP BY tags.schema_name, tags.provider_id, tags.source_name, COALESCE(tags.batch_id, ''),
		         COALESCE(tags.producer_peer_id, ''), COALESCE(tags.producer_public_key, '')
	`, tableName), schemaName); err != nil {
		return fmt.Errorf("rebuild source summary for %s: %w", schemaName, err)
	}
	return nil
}

func incrementSourceSummary(tx *sql.Tx, schemaName string, tags SourceTags, recordBytes int64) error {
	tags = normalizeSourceTags(tags)
	_, err := tx.Exec(`
		INSERT INTO sdn_record_source_summary (
			schema_name, provider_id, source_name, batch_id, producer_peer_id,
			producer_public_key, record_count, total_bytes, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, strftime('%s', 'now'))
		ON CONFLICT(schema_name, provider_id, source_name, batch_id, producer_peer_id, producer_public_key) DO UPDATE SET
			record_count = record_count + 1,
			total_bytes = total_bytes + excluded.total_bytes,
			updated_at = excluded.updated_at
	`, schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ProducerPeerID, tags.ProducerPublicKey, recordBytes)
	if err != nil {
		return fmt.Errorf("increment source summary: %w", err)
	}
	return nil
}

func decrementSourceSummary(tx *sql.Tx, schemaName string, tags SourceTags, recordBytes int64) error {
	tags = normalizeSourceTags(tags)
	_, err := tx.Exec(`
		UPDATE sdn_record_source_summary
		SET
			record_count = CASE WHEN record_count > 0 THEN record_count - 1 ELSE 0 END,
			total_bytes = CASE WHEN total_bytes > ? THEN total_bytes - ? ELSE 0 END,
			updated_at = strftime('%s', 'now')
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
	`, recordBytes, recordBytes, schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ProducerPeerID, tags.ProducerPublicKey)
	if err != nil {
		return fmt.Errorf("decrement source summary: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM sdn_record_source_summary
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
		  AND record_count <= 0
	`, schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ProducerPeerID, tags.ProducerPublicKey); err != nil {
		return fmt.Errorf("delete empty source summary: %w", err)
	}
	return nil
}

func sameSourceSummaryKey(left, right SourceTags) bool {
	left = normalizeSourceTags(left)
	right = normalizeSourceTags(right)
	return strings.TrimSpace(left.ProviderID) == strings.TrimSpace(right.ProviderID) &&
		strings.TrimSpace(left.SourceName) == strings.TrimSpace(right.SourceName) &&
		strings.TrimSpace(left.BatchID) == strings.TrimSpace(right.BatchID) &&
		strings.TrimSpace(left.ProducerPeerID) == strings.TrimSpace(right.ProducerPeerID) &&
		strings.TrimSpace(left.ProducerPublicKey) == strings.TrimSpace(right.ProducerPublicKey)
}

func normalizeSourceTags(tags SourceTags) SourceTags {
	tags.ProviderID = strings.TrimSpace(tags.ProviderID)
	tags.SourceName = strings.TrimSpace(tags.SourceName)
	tags.SourceURL = strings.TrimSpace(tags.SourceURL)
	tags.BatchID = strings.TrimSpace(tags.BatchID)
	tags.ContentKeyID = strings.TrimSpace(tags.ContentKeyID)
	tags.ProducerPeerID = strings.TrimSpace(tags.ProducerPeerID)
	tags.ProducerPublicKey = strings.TrimSpace(tags.ProducerPublicKey)
	if tags.ProducerPeerID == "" {
		tags.ProducerPeerID = tags.ProviderID
	}
	if tags.ProducerPublicKey == "" {
		tags.ProducerPublicKey = tags.ContentKeyID
	}
	return tags
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
	var existing int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT 1 FROM %s WHERE cid = ?`, tableName), cid).Scan(&existing); err == nil {
		return cid, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("failed to check existing record: %w", err)
	}

	// Use INSERT OR IGNORE: content-addressed records are immutable.
	// REPLACE would allow a different peer to overwrite the original
	// author's peer_id (attribution hijacking).
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

	log.Debugf("Stored %s record with CID: %s", schemaName, cid[:16]+"...")
	return cid, nil
}

// SourceTags records source provenance needed for provider/batch-aware queries.
type SourceTags struct {
	ProviderID        string
	SourceName        string
	SourceURL         string
	BatchID           string
	ContentKeyID      string
	ProducerPeerID    string
	ProducerPublicKey string
}

// SourceTagQuery filters stored records by source tags.
type SourceTagQuery struct {
	SchemaName string
	ProviderID string
	SourceName string
	BatchID    string
	Limit      int
}

// SourceBatchReconcileResult summarizes a source-batch reconciliation.
type SourceBatchReconcileResult struct {
	SchemaName string `json:"schemaName"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	KeepBatch  string `json:"keepBatch"`
	Apply      bool   `json:"apply"`
	Matched    int64  `json:"matched"`
	Deleted    int64  `json:"deleted"`
}

// DataSummary describes local FlatSQL record volume by schema and source.
type DataSummary struct {
	TotalRecords int64
	TotalBytes   int64
	Schemas      []DataSchemaSummary
	Sources      []DataSourceSummary
}

// DataSchemaSummary is a per-schema FlatSQL record count and byte total.
type DataSchemaSummary struct {
	SchemaName string
	Count      int64
	TotalBytes int64
}

// DataSourceSummary is a per-producer/source FlatSQL record count and byte total.
type DataSourceSummary struct {
	SchemaName        string
	ProviderID        string
	SourceName        string
	BatchID           string
	ProducerPeerID    string
	ProducerPublicKey string
	Count             int64
	TotalBytes        int64
}

// RawRecordQuery filters raw FlatBuffer records for UI and node-to-node reads.
type RawRecordQuery struct {
	SchemaName        string
	CID               string
	ProviderID        string
	SourceName        string
	BatchID           string
	ProducerPeerID    string
	ProducerPublicKey string
	PeerID            string
	Limit             int
	Offset            int
}

// RawRecordHead summarizes a raw-record result set without hydrating any
// FlatBuffer payload bytes.
type RawRecordHead struct {
	TotalBytes             int64
	MaxRecordTimestampUnix int64
	MaxSourceUpdatedAtUnix int64
	MaxCreatedAtUnix       int64
	MaxRowID               int64
}

// RawRecordRef identifies one scan-bound record for ordered raw-byte sync.
type RawRecordRef struct {
	CID               string
	ProviderID        string
	SourceName        string
	BatchID           string
	ProducerPeerID    string
	ProducerPublicKey string
	PeerID            string
}

// IndexedRecordQuery filters materialized FlatSQL record indexes.
type IndexedRecordQuery struct {
	SchemaName          string
	Day                 string
	NoradCatID          *uint32
	EntityID            string
	ObjectType          string
	OpsStatusCode       string
	ActivePayloads      bool
	CAReadyResidentSet  bool
	From                *time.Time
	To                  *time.Time
	ProviderID          string
	SourceName          string
	BatchID             string
	Limit               int
	Offset              int
	AllowLargeResultSet bool
	OrderByCID          bool
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
	tags = normalizeSourceTags(tags)
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}
	if strings.TrimSpace(cid) == "" {
		return errors.New("cid is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin source tag upsert: %w", err)
	}
	defer tx.Rollback()

	var recordBytes sql.NullInt64
	if err := tx.QueryRow(fmt.Sprintf(`SELECT record_length FROM %s WHERE cid = ?`, tableName), cid).Scan(&recordBytes); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lookup source-tagged record bytes: %w", err)
		}
	}

	var existingTag int
	err = tx.QueryRow(`
		SELECT 1
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND cid = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND content_key_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
	`, schemaName, cid, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey).Scan(&existingTag)
	if errors.Is(err, sql.ErrNoRows) {
		existingTag = 0
	} else if err != nil {
		return fmt.Errorf("lookup existing source tags: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, cid, provider_id, source_name, batch_id, content_key_id, producer_peer_id, producer_public_key) DO UPDATE SET
			source_url = excluded.source_url,
			created_at = strftime('%s', 'now')
	`, schemaName, cid, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey)
	if err != nil {
		return fmt.Errorf("failed to upsert source tags: %w", err)
	}

	bytesTotal := recordBytes.Int64
	if existingTag == 0 {
		if err := incrementSourceSummary(tx, schemaName, tags, bytesTotal); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source tag upsert: %w", err)
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
		SELECT provider_id, source_name, source_url, batch_id, content_key_id,
		       producer_peer_id, producer_public_key
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, schemaName, cid).Scan(
		&tags.ProviderID,
		&tags.SourceName,
		&tags.SourceURL,
		&tags.BatchID,
		&tags.ContentKeyID,
		&tags.ProducerPeerID,
		&tags.ProducerPublicKey,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
		}
		return SourceTags{}, fmt.Errorf("failed to get source tags: %w", err)
	}
	return tags, nil
}

func (s *FlatSQLStore) sourceTagsForCIDs(schemaName string, cids []string) (map[string]SourceTags, error) {
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	tagsByCID := make(map[string]SourceTags, len(cids))
	if len(cids) == 0 {
		return tagsByCID, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	const chunkSize = 500
	for start := 0; start < len(cids); start += chunkSize {
		end := start + chunkSize
		if end > len(cids) {
			end = len(cids)
		}
		chunk := cids[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, schemaName)
		for i, cid := range chunk {
			placeholders[i] = "?"
			args = append(args, cid)
		}

		rows, err := s.db.Query(`
			SELECT cid, provider_id, source_name, source_url, batch_id, content_key_id,
			       producer_peer_id, producer_public_key
			FROM sdn_record_source_tags
			WHERE schema_name = ? AND cid IN (`+strings.Join(placeholders, ",")+`)
			ORDER BY cid ASC, created_at DESC
		`, args...)
		if err != nil {
			return nil, fmt.Errorf("query source tags: %w", err)
		}
		for rows.Next() {
			var cid string
			var tags SourceTags
			if err := rows.Scan(
				&cid,
				&tags.ProviderID,
				&tags.SourceName,
				&tags.SourceURL,
				&tags.BatchID,
				&tags.ContentKeyID,
				&tags.ProducerPeerID,
				&tags.ProducerPublicKey,
			); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan source tags: %w", err)
			}
			if _, exists := tagsByCID[cid]; !exists {
				tagsByCID[cid] = tags
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read source tags: %w", err)
		}
		rows.Close()
	}
	return tagsByCID, nil
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
		SELECT records.cid, records.peer_id, records.timestamp,
		       records.stream_path, records.stream_offset, records.record_length, records.signature_hex
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
		var streamPath string
		var streamOffset, recordLength int64
		var signatureHex sql.NullString
		if err := rows.Scan(&record.CID, &record.PeerID, &ts, &streamPath, &streamOffset, &recordLength, &signatureHex); err != nil {
			return nil, fmt.Errorf("failed to scan source tagged record: %w", err)
		}
		record.Timestamp = time.Unix(ts, 0)
		if err := s.hydrateRecordData(&record, streamPath, streamOffset, recordLength, signatureHex); err != nil {
			return nil, fmt.Errorf("failed to read source tagged record data: %w", err)
		}
		records = append(records, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source tagged record rows: %w", err)
	}
	return records, nil
}

// ReconcileSourceBatch deletes source-tagged records outside the accepted
// source batch. It is intended for DPM-series reconciliation after an operator
// has selected the latest accepted source hash/batch ID.
func (s *FlatSQLStore) ReconcileSourceBatch(schemaName, providerID, sourceName, keepBatch string, apply bool) (SourceBatchReconcileResult, error) {
	result := SourceBatchReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		KeepBatch:  strings.TrimSpace(keepBatch),
		Apply:      apply,
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
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	args := []interface{}{result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch}
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s records
		INNER JOIN sdn_record_source_tags tags
		  ON tags.schema_name = ? AND tags.cid = records.cid
		WHERE tags.provider_id = ?
		  AND tags.source_name = ?
		  AND tags.batch_id <> ?
	`, tableName)
	if err := s.db.QueryRow(countSQL, args...).Scan(&result.Matched); err != nil {
		return result, fmt.Errorf("count source batch reconciliation records: %w", err)
	}
	if !apply || result.Matched == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin source batch reconciliation: %w", err)
	}
	defer tx.Rollback()

	cidSubquery := `
		SELECT cid
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id <> ?
	`
	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_cids (cid TEXT PRIMARY KEY)`); err != nil {
		return result, fmt.Errorf("create reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM temp_sdn_reconcile_cids`); err != nil {
		return result, fmt.Errorf("clear reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO temp_sdn_reconcile_cids (cid) `+cidSubquery, args...); err != nil {
		return result, fmt.Errorf("stage source batch cids: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?`, args...); err != nil {
		return result, fmt.Errorf("delete source batch tags: %w", err)
	}
	deleteRecordsSQL := fmt.Sprintf(`
		DELETE FROM %s
		WHERE cid IN (SELECT cid FROM temp_sdn_reconcile_cids)
		  AND NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = ? AND tags.cid = %s.cid
		  )
	`, tableName, tableName)
	recordDelete, err := tx.Exec(deleteRecordsSQL, result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("delete orphaned source batch records: %w", err)
	}
	result.Deleted, _ = recordDelete.RowsAffected()
	if _, err := tx.Exec(`
		DELETE FROM sdn_record_index
		WHERE schema_name = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_cids)
		  AND NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = sdn_record_index.schema_name
			  AND tags.cid = sdn_record_index.cid
		  )
	`, result.SchemaName); err != nil {
		return result, fmt.Errorf("delete orphaned source batch index rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit source batch reconciliation: %w", err)
	}
	if err := s.rebuildSourceSummaryForSchema(result.SchemaName, tableName); err != nil {
		return result, err
	}
	return result, nil
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
	record, err := s.GetRecord(schemaName, cid)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), record.Data...), nil
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
		querySQL = fmt.Sprintf(`SELECT stream_path, stream_offset, record_length FROM %s WHERE %s`, tableName, whereClause)
	} else {
		querySQL = fmt.Sprintf(`SELECT stream_path, stream_offset, record_length FROM %s`, tableName)
	}

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	defer rows.Close()

	var results [][]byte
	for rows.Next() {
		var streamPath string
		var streamOffset, recordLength int64
		if err := rows.Scan(&streamPath, &streamOffset, &recordLength); err != nil {
			log.Warnf("Failed to scan row: %v", err)
			continue
		}
		data, err := s.readFlatSQLStreamRecord(streamPath, streamOffset, recordLength)
		if err != nil {
			log.Warnf("Failed to read FlatSQL stream record: %v", err)
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
	querySQL := fmt.Sprintf(`SELECT stream_path, stream_offset, record_length FROM %s ORDER BY timestamp DESC LIMIT ?`, tableName)
	rows, err := s.db.Query(querySQL, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query bounded records: %w", err)
	}
	defer rows.Close()

	results := make([][]byte, 0, limit)
	totalBytes := 0
	for rows.Next() {
		var streamPath string
		var streamOffset, recordLength int64
		if err := rows.Scan(&streamPath, &streamOffset, &recordLength); err != nil {
			log.Warnf("Failed to scan row: %v", err)
			continue
		}
		data, err := s.readFlatSQLStreamRecord(streamPath, streamOffset, recordLength)
		if err != nil {
			log.Warnf("Failed to read FlatSQL stream record: %v", err)
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

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete: %w", err)
	}
	defer tx.Rollback()

	var recordBytes sql.NullInt64
	if err := tx.QueryRow(fmt.Sprintf(`SELECT record_length FROM %s WHERE cid = ?`, tableName), cid).Scan(&recordBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("not found: %s", cid)
		}
		return fmt.Errorf("lookup deleted record bytes: %w", err)
	}
	tagRows, err := tx.Query(`
		SELECT provider_id, source_name, source_url, batch_id, content_key_id,
		       producer_peer_id, producer_public_key
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ?
	`, schemaName, cid)
	if err != nil {
		return fmt.Errorf("lookup deleted source tags: %w", err)
	}
	var deletedTags []SourceTags
	for tagRows.Next() {
		var tags SourceTags
		if err := tagRows.Scan(
			&tags.ProviderID,
			&tags.SourceName,
			&tags.SourceURL,
			&tags.BatchID,
			&tags.ContentKeyID,
			&tags.ProducerPeerID,
			&tags.ProducerPublicKey,
		); err != nil {
			tagRows.Close()
			return fmt.Errorf("scan deleted source tags: %w", err)
		}
		deletedTags = append(deletedTags, tags)
	}
	if err := tagRows.Close(); err != nil {
		return fmt.Errorf("close deleted source tags: %w", err)
	}

	deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE cid = ?`, tableName)
	result, err := tx.Exec(deleteSQL, cid)
	if err != nil {
		return fmt.Errorf("failed to delete: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("not found: %s", cid)
	}

	if _, err := tx.Exec(`DELETE FROM sdn_record_index WHERE schema_name = ? AND cid = ?`, schemaName, cid); err != nil {
		log.Warnf("Failed to delete index row for %s/%s: %v", schemaName, cid, err)
	}
	if _, err := tx.Exec(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND cid = ?`, schemaName, cid); err != nil {
		log.Warnf("Failed to delete source tags for %s/%s: %v", schemaName, cid, err)
	}
	for _, tags := range deletedTags {
		if err := decrementSourceSummary(tx, schemaName, tags, recordBytes.Int64); err != nil {
			return err
		}
	}

	return tx.Commit()
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
		if affected > 0 {
			if _, err := s.db.Exec(fmt.Sprintf(`
				DELETE FROM sdn_record_source_tags
				WHERE schema_name = ?
				  AND cid NOT IN (SELECT cid FROM %s)
			`, tableName), schemaName); err != nil {
				log.Warnf("GC source tag cleanup failed for %s: %v", schemaName, err)
			}
			if err := s.rebuildSourceSummaryForSchema(schemaName, tableName); err != nil {
				log.Warnf("GC source summary rebuild failed for %s: %v", schemaName, err)
			}
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
	CID            string
	PeerID         string
	Timestamp      time.Time
	Data           []byte
	Signature      []byte
	SourceTags     SourceTags
	MaterializedAt time.Time
	StreamPath     string
	StreamOffset   int64
	RecordLength   int64
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

// LocalEPMRecord is the decrypted local EPM source-of-truth record. The
// corresponding on-disk SQLite row stores only EPM.fbs bytes encrypted.
type LocalEPMRecord struct {
	PeerID    string
	EPMBytes  []byte
	UpdatedAt int64
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

// SaveLocalEPM stores a local node EPM FlatBuffer encrypted in the FlatSQL
// database. JSON profile and JSON EPM projections are intentionally not stored.
func (s *FlatSQLStore) SaveLocalEPM(peerID string, epmBytes []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return errors.New("local EPM peer_id is required")
	}
	if len(epmBytes) == 0 {
		return errors.New("local EPM bytes are required")
	}

	encryptedBytes, err := s.encryptLocalEPMPayload(epmBytes)
	if err != nil {
		return fmt.Errorf("encrypt local EPM bytes: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO sdn_local_epms (
			peer_id, schema_name, encrypted_epm_bytes, updated_at
		)
		VALUES (?, 'EPM.fbs', ?, ?)
		ON CONFLICT(peer_id) DO UPDATE SET
			schema_name = 'EPM.fbs',
			encrypted_epm_bytes = excluded.encrypted_epm_bytes,
			updated_at = excluded.updated_at
	`, peerID, encryptedBytes, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save local EPM record: %w", err)
	}

	return nil
}

// LoadLocalEPM decrypts the local node EPM FlatBuffer bytes.
func (s *FlatSQLStore) LoadLocalEPM(peerID string) ([]byte, error) {
	record, err := s.GetLocalEPMRecord(peerID)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), record.EPMBytes...), nil
}

// GetLocalEPMRecord decrypts the local EPM record for a peer ID.
func (s *FlatSQLStore) GetLocalEPMRecord(peerID string) (*LocalEPMRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, errors.New("local EPM peer_id is required")
	}

	var encryptedBytes string
	var updatedAt int64
	err := s.db.QueryRow(`
		SELECT encrypted_epm_bytes, updated_at
		FROM sdn_local_epms
		WHERE peer_id = ?
	`, peerID).Scan(&encryptedBytes, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("local EPM profile not found for %s", peerID)
		}
		return nil, fmt.Errorf("read local EPM profile: %w", err)
	}

	epmBytes, err := s.decryptLocalEPMPayload(encryptedBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt local EPM bytes: %w", err)
	}

	return &LocalEPMRecord{
		PeerID:    peerID,
		EPMBytes:  epmBytes,
		UpdatedAt: updatedAt,
	}, nil
}

type localEPMEnvelope struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	KDF        string `json:"kdf"`
	IV         string `json:"iv"`
	Ciphertext string `json:"ciphertext"`
}

func (s *FlatSQLStore) encryptLocalEPMPayload(plaintext []byte) (string, error) {
	gcm, err := s.localEPMGCM()
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, iv, plaintext, []byte("EPM.fbs"))
	envelope := localEPMEnvelope{
		Version:    1,
		Algorithm:  "aes-256-gcm",
		KDF:        "scrypt-system-derived",
		IV:         base64.StdEncoding.EncodeToString(iv),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *FlatSQLStore) decryptLocalEPMPayload(rawEnvelope string) ([]byte, error) {
	var envelope localEPMEnvelope
	if err := json.Unmarshal([]byte(rawEnvelope), &envelope); err != nil {
		return nil, err
	}
	iv, err := base64.StdEncoding.DecodeString(envelope.IV)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, err
	}
	gcm, err := s.localEPMGCM()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, iv, ciphertext, []byte("EPM.fbs"))
}

func (s *FlatSQLStore) localEPMGCM() (cipher.AEAD, error) {
	key, err := s.localEPMKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *FlatSQLStore) localEPMKey() ([]byte, error) {
	password := strings.TrimSpace(os.Getenv("SDN_EPM_STORE_PASSWORD"))
	if password == "" {
		password = strings.TrimSpace(os.Getenv("SDN_KEY_PASSWORD"))
	}
	if password == "" {
		hostname, _ := os.Hostname()
		homeDir, _ := os.UserHomeDir()
		password = strings.Join([]string{
			"sdn-local-epm",
			hostname,
			runtime.GOOS,
			runtime.GOARCH,
			homeDir,
			filepath.Dir(s.dbPath),
		}, "|")
	}

	salt := sha256.Sum256([]byte(localEPMStoreSalt + "|" + s.dbPath))
	return scrypt.Key([]byte(password), salt[:], 32768, 8, 1, 32)
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
		rows, err := s.db.Query(fmt.Sprintf(`SELECT cid, timestamp, stream_path, stream_offset, record_length FROM %s`, tableName))
		if err != nil {
			return nil, fmt.Errorf("failed to query %s for reindex: %w", tableName, err)
		}

		var indexed int64
		for rows.Next() {
			var cid string
			var ts int64
			var streamPath string
			var streamOffset, recordLength int64
			if err := rows.Scan(&cid, &ts, &streamPath, &streamOffset, &recordLength); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to scan %s row: %w", tableName, err)
			}
			data, err := s.readFlatSQLStreamRecord(streamPath, streamOffset, recordLength)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to read %s/%s stream record: %w", schemaName, cid, err)
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

// QueryRecentRecords returns recent records directly from the schema table.
// It avoids the materialized index join for unfiltered bulk consumers that need
// the latest catalog stream and do not require day/object predicates.
func (s *FlatSQLStore) QueryRecentRecords(schemaName string, limit int) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 250000 {
		limit = 250000
	}

	taggedQuery := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp,
		       d.stream_path, d.stream_offset, d.record_length, d.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM sdn_record_source_tags tags
		INNER JOIN %s d ON d.cid = tags.cid
		WHERE tags.schema_name = ?
		ORDER BY tags.created_at DESC, d.rowid DESC
		LIMIT ?
	`, tableName)
	rows, err := s.db.Query(taggedQuery, schemaName, limit)
	if err != nil {
		return nil, fmt.Errorf("recent records query failed: %w", err)
	}

	records, err := s.scanRecentRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(records) >= limit {
		return records, nil
	}

	untaggedLimit := limit - len(records)
	untaggedQuery := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp,
		       d.stream_path, d.stream_offset, d.record_length, d.signature_hex,
		       '', '', '', '', '', '', '', NULL
		FROM %s d
		WHERE NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = ? AND tags.cid = d.cid
		)
		ORDER BY d.rowid DESC
		LIMIT ?
	`, tableName)
	rows, err = s.db.Query(untaggedQuery, schemaName, untaggedLimit)
	if err != nil {
		return nil, fmt.Errorf("recent untagged records query failed: %w", err)
	}
	untagged, err := s.scanRecentRecords(rows)
	if err != nil {
		return nil, err
	}
	records = append(records, untagged...)
	return records, nil
}

// DataSummary returns aggregate FlatSQL record counts and raw byte totals.
func (s *FlatSQLStore) DataSummary() (*DataSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := &DataSummary{
		Schemas: make([]DataSchemaSummary, 0),
		Sources: make([]DataSourceSummary, 0),
	}
	summarizedSchemas := map[string]bool{}

	schemaRows, err := s.db.Query(`
		SELECT schema_name, COALESCE(SUM(record_count), 0), COALESCE(SUM(total_bytes), 0)
		FROM sdn_record_source_summary
		GROUP BY schema_name
		ORDER BY schema_name
	`)
	if err != nil {
		return nil, fmt.Errorf("summarize source-backed schemas: %w", err)
	}
	for schemaRows.Next() {
		var schema DataSchemaSummary
		if err := schemaRows.Scan(&schema.SchemaName, &schema.Count, &schema.TotalBytes); err != nil {
			schemaRows.Close()
			return nil, fmt.Errorf("scan source-backed schema summary: %w", err)
		}
		if schema.Count > 0 {
			summary.Schemas = append(summary.Schemas, schema)
			summary.TotalRecords += schema.Count
			summary.TotalBytes += schema.TotalBytes
			summarizedSchemas[schema.SchemaName] = true
		}
	}
	if err := schemaRows.Close(); err != nil {
		return nil, fmt.Errorf("close source-backed schema summary rows: %w", err)
	}

	sourceRows, err := s.db.Query(`
		SELECT schema_name, provider_id, source_name, batch_id, producer_peer_id,
		       producer_public_key, record_count, total_bytes
		FROM sdn_record_source_summary
		WHERE record_count > 0
		ORDER BY schema_name ASC, provider_id ASC, source_name ASC, batch_id ASC,
		         producer_peer_id ASC, producer_public_key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("summarize source-backed producers: %w", err)
	}
	for sourceRows.Next() {
		var source DataSourceSummary
		if err := sourceRows.Scan(
			&source.SchemaName,
			&source.ProviderID,
			&source.SourceName,
			&source.BatchID,
			&source.ProducerPeerID,
			&source.ProducerPublicKey,
			&source.Count,
			&source.TotalBytes,
		); err != nil {
			sourceRows.Close()
			return nil, fmt.Errorf("scan source-backed producer summary: %w", err)
		}
		summary.Sources = append(summary.Sources, source)
	}
	if err := sourceRows.Close(); err != nil {
		return nil, fmt.Errorf("close source-backed producer summary rows: %w", err)
	}

	for _, schemaName := range s.validator.Schemas() {
		if summarizedSchemas[schemaName] {
			continue
		}
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			return nil, fmt.Errorf("invalid schema name %q: %w", schemaName, err)
		}
		var count int64
		var totalBytes sql.NullInt64
		if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(record_length), 0) FROM %s`, tableName)).Scan(&count, &totalBytes); err != nil {
			return nil, fmt.Errorf("summarize %s: %w", schemaName, err)
		}
		if count > 0 {
			bytesTotal := totalBytes.Int64
			summary.Schemas = append(summary.Schemas, DataSchemaSummary{
				SchemaName: schemaName,
				Count:      count,
				TotalBytes: bytesTotal,
			})
			summary.TotalRecords += count
			summary.TotalBytes += bytesTotal
		}
	}

	localCount, localBytes, err := s.localEPMSummaryLocked()
	if err != nil {
		return nil, err
	}
	if localCount > 0 {
		summary.Schemas = appendOrAddSchemaSummary(summary.Schemas, DataSchemaSummary{
			SchemaName: "EPM.fbs",
			Count:      localCount,
			TotalBytes: localBytes,
		})
		summary.Sources = append(summary.Sources, DataSourceSummary{
			SchemaName:        "EPM.fbs",
			ProviderID:        "local-node",
			SourceName:        "local-epm",
			BatchID:           "local",
			ProducerPeerID:    "local-node",
			ProducerPublicKey: "local-node",
			Count:             localCount,
			TotalBytes:        localBytes,
		})
		summary.TotalRecords += localCount
		summary.TotalBytes += localBytes
	}

	return summary, nil
}

// CountRawRecords returns a filtered raw-record count without hydrating
// FlatBuffer payloads from stream files.
func (s *FlatSQLStore) CountRawRecords(filter RawRecordQuery) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return 0, errors.New("schema name is required")
	}
	tableName, err := sds.SchemaNameToTable(filter.SchemaName)
	if err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}

	sourceFiltered := strings.TrimSpace(filter.ProviderID) != "" ||
		strings.TrimSpace(filter.SourceName) != "" ||
		strings.TrimSpace(filter.BatchID) != "" ||
		strings.TrimSpace(filter.ProducerPeerID) != "" ||
		strings.TrimSpace(filter.ProducerPublicKey) != ""

	if !sourceFiltered {
		total, err := s.countSchemaRecordsLocked(tableName, filter)
		if err != nil {
			return 0, err
		}
		if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
			localCount, err := s.countLocalEPMRecordsLocked(filter)
			if err != nil {
				return 0, err
			}
			total += localCount
		}
		return total, nil
	}

	if strings.TrimSpace(filter.PeerID) == "" && strings.TrimSpace(filter.CID) == "" {
		total, ok, err := s.countSourceSummaryRecordsLocked(filter)
		if err != nil {
			return 0, err
		}
		if ok {
			if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
				localCount, err := s.countLocalEPMRecordsLocked(filter)
				if err != nil {
					return 0, err
				}
				total += localCount
			}
			return total, nil
		}
	}

	taggedQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM sdn_record_source_tags tags
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ?
	`, tableName)
	args := []interface{}{filter.SchemaName}

	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		taggedQuery += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		taggedQuery += ` AND records.cid = ?`
		args = append(args, cid)
	}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		taggedQuery += ` AND tags.provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		taggedQuery += ` AND tags.source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		taggedQuery += ` AND tags.batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		taggedQuery += ` AND tags.producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		taggedQuery += ` AND tags.producer_public_key = ?`
		args = append(args, producerPublicKey)
	}

	var total int64
	if err := s.db.QueryRow(taggedQuery, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("raw tagged record count failed: %w", err)
	}

	if !sourceFiltered {
		untaggedQuery := fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s records
			WHERE NOT EXISTS (
				SELECT 1
				FROM sdn_record_source_tags tags
				WHERE tags.schema_name = ? AND tags.cid = records.cid
			)
		`, tableName)
		untaggedArgs := []interface{}{filter.SchemaName}
		if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
			untaggedQuery += ` AND records.peer_id = ?`
			untaggedArgs = append(untaggedArgs, peerID)
		}
		if cid := strings.TrimSpace(filter.CID); cid != "" {
			untaggedQuery += ` AND records.cid = ?`
			untaggedArgs = append(untaggedArgs, cid)
		}
		var untagged int64
		if err := s.db.QueryRow(untaggedQuery, untaggedArgs...).Scan(&untagged); err != nil {
			return 0, fmt.Errorf("raw untagged record count failed: %w", err)
		}
		total += untagged
	}

	if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
		localCount, err := s.countLocalEPMRecordsLocked(filter)
		if err != nil {
			return 0, err
		}
		total += localCount
	}

	return total, nil
}

// RawRecordHead returns cursor/snapshot metadata for a raw-record result set
// without opening the FlatSQL backing stream files.
func (s *FlatSQLStore) RawRecordHead(filter RawRecordQuery) (RawRecordHead, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return RawRecordHead{}, errors.New("schema name is required")
	}
	tableName, err := sds.SchemaNameToTable(filter.SchemaName)
	if err != nil {
		return RawRecordHead{}, fmt.Errorf("invalid schema name: %w", err)
	}

	sourceFiltered := strings.TrimSpace(filter.ProviderID) != "" ||
		strings.TrimSpace(filter.SourceName) != "" ||
		strings.TrimSpace(filter.BatchID) != "" ||
		strings.TrimSpace(filter.ProducerPeerID) != "" ||
		strings.TrimSpace(filter.ProducerPublicKey) != ""

	var head RawRecordHead
	if sourceFiltered && strings.TrimSpace(filter.PeerID) == "" && strings.TrimSpace(filter.CID) == "" {
		summary, ok, err := s.rawSourceSummaryHeadLocked(filter)
		if err != nil {
			return RawRecordHead{}, err
		}
		if ok {
			head = summary
			if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
				localCount, localBytes, err := s.localEPMSummaryLocked()
				if err != nil {
					return RawRecordHead{}, err
				}
				head.TotalBytes += localBytes
				if localCount > 0 {
					now := time.Now().Unix()
					head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, now)
					head.MaxCreatedAtUnix = max(head.MaxCreatedAtUnix, now)
				}
			}
			return head, nil
		}
	}

	if sourceFiltered {
		head, err = s.rawTaggedRecordHeadLocked(tableName, filter)
	} else {
		head, err = s.rawSchemaRecordHeadLocked(tableName, filter)
	}
	if err != nil {
		return RawRecordHead{}, err
	}

	if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
		localCount, localBytes, err := s.localEPMSummaryLocked()
		if err != nil {
			return RawRecordHead{}, err
		}
		head.TotalBytes += localBytes
		if localCount > 0 {
			now := time.Now().Unix()
			head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, now)
			head.MaxCreatedAtUnix = max(head.MaxCreatedAtUnix, now)
		}
	}

	return head, nil
}

func (s *FlatSQLStore) rawSourceSummaryHeadLocked(filter RawRecordQuery) (RawRecordHead, bool, error) {
	query := `
		SELECT COUNT(*), COALESCE(SUM(total_bytes), 0), COALESCE(MAX(updated_at), 0)
		FROM sdn_record_source_summary
		WHERE schema_name = ?
	`
	args := []interface{}{filter.SchemaName}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += ` AND provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += ` AND source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += ` AND producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += ` AND producer_public_key = ?`
		args = append(args, producerPublicKey)
	}

	var summaryRows int64
	var totalBytes int64
	var maxUpdated int64
	if err := s.db.QueryRow(query, args...).Scan(&summaryRows, &totalBytes, &maxUpdated); err != nil {
		return RawRecordHead{}, false, fmt.Errorf("raw source summary head failed: %w", err)
	}
	return RawRecordHead{
		TotalBytes:             totalBytes,
		MaxSourceUpdatedAtUnix: maxUpdated,
		MaxCreatedAtUnix:       maxUpdated,
	}, summaryRows > 0, nil
}

func (s *FlatSQLStore) countSchemaRecordsLocked(tableName string, filter RawRecordQuery) (int64, error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE 1=1`, tableName)
	args := make([]interface{}, 0, 2)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND cid = ?`
		args = append(args, cid)
	}

	var total int64
	if err := s.db.QueryRow(query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("raw schema record count failed: %w", err)
	}
	return total, nil
}

func (s *FlatSQLStore) rawSchemaRecordHeadLocked(tableName string, filter RawRecordQuery) (RawRecordHead, error) {
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(record_length), 0), COALESCE(MAX(timestamp), 0),
		       COALESCE(MAX(created_at), 0), COALESCE(MAX(rowid), 0)
		FROM %s
		WHERE 1=1
	`, tableName)
	args := make([]interface{}, 0, 2)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND cid = ?`
		args = append(args, cid)
	}

	var head RawRecordHead
	if err := s.db.QueryRow(query, args...).Scan(
		&head.TotalBytes,
		&head.MaxRecordTimestampUnix,
		&head.MaxCreatedAtUnix,
		&head.MaxRowID,
	); err != nil {
		return RawRecordHead{}, fmt.Errorf("raw schema record head failed: %w", err)
	}
	return head, nil
}

func (s *FlatSQLStore) rawTaggedRecordHeadLocked(tableName string, filter RawRecordQuery) (RawRecordHead, error) {
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(records.record_length), 0), COALESCE(MAX(records.timestamp), 0),
		       COALESCE(MAX(tags.created_at), 0), COALESCE(MAX(records.rowid), 0)
		FROM sdn_record_source_tags tags
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ?
	`, tableName)
	args := []interface{}{filter.SchemaName}

	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND records.cid = ?`
		args = append(args, cid)
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
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += ` AND tags.producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += ` AND tags.producer_public_key = ?`
		args = append(args, producerPublicKey)
	}

	var head RawRecordHead
	if err := s.db.QueryRow(query, args...).Scan(
		&head.TotalBytes,
		&head.MaxRecordTimestampUnix,
		&head.MaxCreatedAtUnix,
		&head.MaxRowID,
	); err != nil {
		return RawRecordHead{}, fmt.Errorf("raw tagged record head failed: %w", err)
	}
	head.MaxSourceUpdatedAtUnix = head.MaxCreatedAtUnix
	return head, nil
}

func (s *FlatSQLStore) countSourceSummaryRecordsLocked(filter RawRecordQuery) (int64, bool, error) {
	query := `
		SELECT COUNT(*), COALESCE(SUM(record_count), 0)
		FROM sdn_record_source_summary
		WHERE schema_name = ?
	`
	args := []interface{}{filter.SchemaName}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += ` AND provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += ` AND source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += ` AND producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += ` AND producer_public_key = ?`
		args = append(args, producerPublicKey)
	}

	var summaryRows int64
	var total int64
	if err := s.db.QueryRow(query, args...).Scan(&summaryRows, &total); err != nil {
		return 0, false, fmt.Errorf("raw source summary count failed: %w", err)
	}
	return total, summaryRows > 0, nil
}

const rawRecordMaxQueryLimit = 50000
const rawRecordRefLookupBatchSize = 500

// QueryRawRecords returns raw FlatBuffer records with metadata and source tags.
func (s *FlatSQLStore) QueryRawRecords(filter RawRecordQuery) ([]*Record, error) {
	return s.queryRawRecords(filter, true)
}

// QueryRawRecordRefs returns metadata refs without opening FlatSQL backing
// files. Provider scan paths use this to keep counts/pages cheap.
func (s *FlatSQLStore) QueryRawRecordRefs(filter RawRecordQuery) ([]*Record, error) {
	return s.queryRawRecords(filter, false)
}

func (s *FlatSQLStore) queryRawRecords(filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return nil, errors.New("schema name is required")
	}
	tableName, err := sds.SchemaNameToTable(filter.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > rawRecordMaxQueryLimit {
		filter.Limit = rawRecordMaxQueryLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	sourceFiltered := strings.TrimSpace(filter.ProviderID) != "" ||
		strings.TrimSpace(filter.SourceName) != "" ||
		strings.TrimSpace(filter.BatchID) != "" ||
		strings.TrimSpace(filter.ProducerPeerID) != "" ||
		strings.TrimSpace(filter.ProducerPublicKey) != ""

	taggedQuery := fmt.Sprintf(`
		SELECT records.cid, records.peer_id, records.timestamp,
		       records.stream_path, records.stream_offset, records.record_length, records.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM sdn_record_source_tags tags
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ?
	`, tableName)
	args := []interface{}{filter.SchemaName}

	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		taggedQuery += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		taggedQuery += ` AND records.cid = ?`
		args = append(args, cid)
	}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		taggedQuery += ` AND tags.provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		taggedQuery += ` AND tags.source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		taggedQuery += ` AND tags.batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		taggedQuery += ` AND tags.producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		taggedQuery += ` AND tags.producer_public_key = ?`
		args = append(args, producerPublicKey)
	}
	taggedQuery += ` ORDER BY tags.created_at DESC, tags.cid ASC LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.Query(taggedQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("raw record query failed: %w", err)
	}
	records, err := s.scanRawRecordRows(rows, hydrate)
	if err != nil {
		return nil, err
	}

	if !sourceFiltered && len(records) < filter.Limit {
		untaggedLimit := filter.Limit - len(records)
		untaggedQuery := fmt.Sprintf(`
			SELECT records.cid, records.peer_id, records.timestamp,
			       records.stream_path, records.stream_offset, records.record_length, records.signature_hex,
			       '', '', '', '', '', '', '', NULL
			FROM %s records
			WHERE NOT EXISTS (
				SELECT 1
				FROM sdn_record_source_tags tags
				WHERE tags.schema_name = ? AND tags.cid = records.cid
			)
		`, tableName)
		untaggedArgs := []interface{}{filter.SchemaName}
		if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
			untaggedQuery += ` AND records.peer_id = ?`
			untaggedArgs = append(untaggedArgs, peerID)
		}
		if cid := strings.TrimSpace(filter.CID); cid != "" {
			untaggedQuery += ` AND records.cid = ?`
			untaggedArgs = append(untaggedArgs, cid)
		}
		untaggedQuery += ` ORDER BY records.timestamp DESC, records.cid ASC LIMIT ?`
		untaggedArgs = append(untaggedArgs, untaggedLimit)
		untaggedRows, err := s.db.Query(untaggedQuery, untaggedArgs...)
		if err != nil {
			return nil, fmt.Errorf("raw untagged record query failed: %w", err)
		}
		untagged, err := s.scanRawRecordRows(untaggedRows, hydrate)
		if err != nil {
			return nil, err
		}
		records = append(records, untagged...)
	}

	if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) && len(records) < filter.Limit {
		localLimit := filter.Limit - len(records)
		localRecords, err := s.queryLocalEPMRecordsLocked(filter, localLimit)
		if err != nil {
			return nil, err
		}
		records = append(records, localRecords...)
	}

	return records, nil
}

// QueryRawRecordRefsByRefs resolves scan-bound refs in batches and preserves
// the requested order. Returned records include FlatSQL stream offsets but do
// not hydrate Data.
func (s *FlatSQLStore) QueryRawRecordRefsByRefs(schemaName string, refs []RawRecordRef) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return nil, errors.New("schema name is required")
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if len(refs) == 0 {
		return nil, nil
	}

	normalizedRefs := make([]RawRecordRef, 0, len(refs))
	for _, ref := range refs {
		ref = normalizeRawRecordRef(ref)
		if ref.CID == "" {
			return nil, errors.New("record cid is required")
		}
		normalizedRefs = append(normalizedRefs, ref)
	}

	candidates := make(map[string][]*Record, len(normalizedRefs))
	for start := 0; start < len(normalizedRefs); start += rawRecordRefLookupBatchSize {
		end := start + rawRecordRefLookupBatchSize
		if end > len(normalizedRefs) {
			end = len(normalizedRefs)
		}
		if err := s.loadRawRecordRefCandidatesLocked(schemaName, tableName, normalizedRefs[start:end], candidates); err != nil {
			return nil, err
		}
	}

	ordered := make([]*Record, 0, len(normalizedRefs))
	for _, ref := range normalizedRefs {
		var matched *Record
		for _, candidate := range candidates[ref.CID] {
			if rawRecordMatchesRef(candidate, ref) {
				matched = candidate
				break
			}
		}
		if matched == nil && schemaName == "EPM.fbs" {
			local, err := s.queryLocalEPMRecordsLocked(RawRecordQuery{
				SchemaName:        schemaName,
				CID:               ref.CID,
				ProviderID:        ref.ProviderID,
				SourceName:        ref.SourceName,
				BatchID:           ref.BatchID,
				ProducerPeerID:    ref.ProducerPeerID,
				ProducerPublicKey: ref.ProducerPublicKey,
				PeerID:            ref.PeerID,
				Limit:             1,
			}, 1)
			if err == nil && len(local) == 1 {
				matched = local[0]
			}
		}
		if matched == nil {
			return nil, fmt.Errorf("raw record ref not found: %s", ref.CID)
		}
		ordered = append(ordered, matched)
	}

	return ordered, nil
}

func (s *FlatSQLStore) loadRawRecordRefCandidatesLocked(schemaName, tableName string, refs []RawRecordRef, candidates map[string][]*Record) error {
	cids := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref.CID]; ok {
			continue
		}
		seen[ref.CID] = struct{}{}
		cids = append(cids, ref.CID)
	}
	if len(cids) == 0 {
		return nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(cids)), ",")
	taggedQuery := fmt.Sprintf(`
		SELECT records.cid, records.peer_id, records.timestamp,
		       records.stream_path, records.stream_offset, records.record_length, records.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM sdn_record_source_tags tags
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ? AND records.cid IN (%s)
	`, tableName, placeholders)
	taggedArgs := make([]interface{}, 0, len(cids)+1)
	taggedArgs = append(taggedArgs, schemaName)
	for _, cid := range cids {
		taggedArgs = append(taggedArgs, cid)
	}
	taggedRows, err := s.db.Query(taggedQuery, taggedArgs...)
	if err != nil {
		return fmt.Errorf("raw record ref query failed: %w", err)
	}
	tagged, err := s.scanRawRecordRows(taggedRows, false)
	if err != nil {
		return err
	}
	for _, record := range tagged {
		candidates[record.CID] = append(candidates[record.CID], record)
	}

	untaggedQuery := fmt.Sprintf(`
		SELECT records.cid, records.peer_id, records.timestamp,
		       records.stream_path, records.stream_offset, records.record_length, records.signature_hex,
		       '', '', '', '', '', '', '', NULL
		FROM %s records
		WHERE records.cid IN (%s)
		  AND NOT EXISTS (
			  SELECT 1
			  FROM sdn_record_source_tags tags
			  WHERE tags.schema_name = ? AND tags.cid = records.cid
		  )
	`, tableName, placeholders)
	untaggedArgs := make([]interface{}, 0, len(cids)+1)
	for _, cid := range cids {
		untaggedArgs = append(untaggedArgs, cid)
	}
	untaggedArgs = append(untaggedArgs, schemaName)
	untaggedRows, err := s.db.Query(untaggedQuery, untaggedArgs...)
	if err != nil {
		return fmt.Errorf("raw untagged record ref query failed: %w", err)
	}
	untagged, err := s.scanRawRecordRows(untaggedRows, false)
	if err != nil {
		return err
	}
	for _, record := range untagged {
		candidates[record.CID] = append(candidates[record.CID], record)
	}
	return nil
}

func normalizeRawRecordRef(ref RawRecordRef) RawRecordRef {
	ref.CID = strings.TrimSpace(ref.CID)
	ref.ProviderID = strings.TrimSpace(ref.ProviderID)
	ref.SourceName = strings.TrimSpace(ref.SourceName)
	ref.BatchID = strings.TrimSpace(ref.BatchID)
	ref.ProducerPeerID = strings.TrimSpace(ref.ProducerPeerID)
	ref.ProducerPublicKey = strings.TrimSpace(ref.ProducerPublicKey)
	ref.PeerID = strings.TrimSpace(ref.PeerID)
	return ref
}

func rawRecordMatchesRef(record *Record, ref RawRecordRef) bool {
	if ref.CID != "" && record.CID != ref.CID {
		return false
	}
	if ref.PeerID != "" && record.PeerID != ref.PeerID {
		return false
	}
	if ref.ProviderID != "" && record.SourceTags.ProviderID != ref.ProviderID {
		return false
	}
	if ref.SourceName != "" && record.SourceTags.SourceName != ref.SourceName {
		return false
	}
	if ref.BatchID != "" && record.SourceTags.BatchID != ref.BatchID {
		return false
	}
	if ref.ProducerPeerID != "" && record.SourceTags.ProducerPeerID != ref.ProducerPeerID {
		return false
	}
	if ref.ProducerPublicKey != "" && record.SourceTags.ProducerPublicKey != ref.ProducerPublicKey {
		return false
	}
	return true
}

// WriteRawRecordFrames writes native FlatSQL size-prefixed FlatBuffer streams
// directly from FlatSQL backing files. FlatSQL stream frame lengths are
// little-endian uint32 values.
func (s *FlatSQLStore) WriteRawRecordFrames(writer io.Writer, records []*Record) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	openFiles := make(map[string]*os.File)
	defer func() {
		for _, file := range openFiles {
			_ = file.Close()
		}
	}()

	var diskLength [4]byte
	for _, record := range records {
		if len(record.Data) > 0 && strings.TrimSpace(record.StreamPath) == "" {
			if len(record.Data) > int(^uint32(0)) {
				return fmt.Errorf("record %s exceeds uint32 stream frame length", record.CID)
			}
			binary.LittleEndian.PutUint32(diskLength[:], uint32(len(record.Data)))
			if _, err := writer.Write(diskLength[:]); err != nil {
				return err
			}
			if _, err := writer.Write(record.Data); err != nil {
				return err
			}
			continue
		}
		if record.StreamOffset < 0 {
			return fmt.Errorf("record %s has negative FlatSQL stream offset %d", record.CID, record.StreamOffset)
		}
		if record.RecordLength < 0 || record.RecordLength > int64(^uint32(0)) {
			return fmt.Errorf("record %s has invalid FlatSQL record length %d", record.CID, record.RecordLength)
		}
		file, err := s.openCachedFlatSQLStreamFile(openFiles, record.StreamPath)
		if err != nil {
			return fmt.Errorf("open FlatSQL stream for %s: %w", record.CID, err)
		}
		if _, err := file.ReadAt(diskLength[:], record.StreamOffset); err != nil {
			return fmt.Errorf("read FlatSQL stream length for %s: %w", record.CID, err)
		}
		if got := int64(binary.LittleEndian.Uint32(diskLength[:])); got != record.RecordLength {
			return fmt.Errorf("FlatSQL stream frame length for %s = %d, want %d", record.CID, got, record.RecordLength)
		}
		if _, err := writer.Write(diskLength[:]); err != nil {
			return err
		}
		section := io.NewSectionReader(file, record.StreamOffset+4, record.RecordLength)
		if _, err := io.Copy(writer, section); err != nil {
			return err
		}
	}
	return nil
}

func (s *FlatSQLStore) openCachedFlatSQLStreamFile(openFiles map[string]*os.File, streamPath string) (*os.File, error) {
	clean := filepath.Clean(streamPath)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid FlatSQL stream path %q", streamPath)
	}
	if file := openFiles[clean]; file != nil {
		return file, nil
	}
	file, err := os.Open(filepath.Join(s.basePath, clean))
	if err != nil {
		return nil, err
	}
	openFiles[clean] = file
	return file, nil
}

// GetRawRecord returns one raw FlatBuffer record by schema and CID. Local EPM
// records use the peer ID as the stable local record identifier.
func (s *FlatSQLStore) GetRawRecord(schemaName, cid string) (*Record, error) {
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return nil, errors.New("record id is required")
	}
	record, err := s.GetRecord(schemaName, cid)
	if err == nil {
		return record, nil
	}
	if schemaName == "EPM.fbs" {
		local, localErr := s.GetLocalEPMRecord(cid)
		if localErr == nil {
			return &Record{
				CID:       local.PeerID,
				PeerID:    local.PeerID,
				Timestamp: time.Unix(local.UpdatedAt, 0).UTC(),
				Data:      append([]byte(nil), local.EPMBytes...),
				SourceTags: SourceTags{
					ProviderID: "local-node",
					SourceName: "local-epm",
					BatchID:    "local",
				},
			}, nil
		}
	}
	return nil, err
}

func (s *FlatSQLStore) scanRecentRecords(rows *sql.Rows) ([]*Record, error) {
	defer rows.Close()

	records := make([]*Record, 0)
	for rows.Next() {
		rec := &Record{}
		var ts int64
		var materializedAt sql.NullInt64
		var streamPath string
		var streamOffset, recordLength int64
		var signatureHex sql.NullString
		if err := rows.Scan(
			&rec.CID,
			&rec.PeerID,
			&ts,
			&streamPath,
			&streamOffset,
			&recordLength,
			&signatureHex,
			&rec.SourceTags.ProviderID,
			&rec.SourceTags.SourceName,
			&rec.SourceTags.SourceURL,
			&rec.SourceTags.BatchID,
			&rec.SourceTags.ContentKeyID,
			&rec.SourceTags.ProducerPeerID,
			&rec.SourceTags.ProducerPublicKey,
			&materializedAt,
		); err != nil {
			return nil, fmt.Errorf("failed scanning recent row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		if err := s.hydrateRecordData(rec, streamPath, streamOffset, recordLength, signatureHex); err != nil {
			return nil, fmt.Errorf("failed reading recent record data: %w", err)
		}
		if materializedAt.Valid && materializedAt.Int64 > 0 {
			rec.MaterializedAt = time.Unix(materializedAt.Int64, 0).UTC()
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent records rows failed: %w", err)
	}
	return records, nil
}

func (s *FlatSQLStore) scanRawRecordRows(rows *sql.Rows, hydrate bool) ([]*Record, error) {
	defer rows.Close()

	records := make([]*Record, 0)
	for rows.Next() {
		rec := &Record{}
		var ts int64
		var materializedAt sql.NullInt64
		var streamPath string
		var streamOffset, recordLength int64
		var signatureHex sql.NullString
		if err := rows.Scan(
			&rec.CID,
			&rec.PeerID,
			&ts,
			&streamPath,
			&streamOffset,
			&recordLength,
			&signatureHex,
			&rec.SourceTags.ProviderID,
			&rec.SourceTags.SourceName,
			&rec.SourceTags.SourceURL,
			&rec.SourceTags.BatchID,
			&rec.SourceTags.ContentKeyID,
			&rec.SourceTags.ProducerPeerID,
			&rec.SourceTags.ProducerPublicKey,
			&materializedAt,
		); err != nil {
			return nil, fmt.Errorf("failed scanning raw record row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		rec.StreamPath = streamPath
		rec.StreamOffset = streamOffset
		rec.RecordLength = recordLength
		if hydrate {
			if err := s.hydrateRecordData(rec, streamPath, streamOffset, recordLength, signatureHex); err != nil {
				return nil, fmt.Errorf("failed reading raw record data: %w", err)
			}
		} else if signatureHex.Valid && strings.TrimSpace(signatureHex.String) != "" {
			signature, err := hex.DecodeString(strings.TrimSpace(signatureHex.String))
			if err != nil {
				return nil, fmt.Errorf("decode signature_hex for %s: %w", rec.CID, err)
			}
			rec.Signature = signature
		}
		if materializedAt.Valid && materializedAt.Int64 > 0 {
			rec.MaterializedAt = time.Unix(materializedAt.Int64, 0).UTC()
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("raw record rows failed: %w", err)
	}
	return records, nil
}

func appendOrAddSchemaSummary(schemas []DataSchemaSummary, add DataSchemaSummary) []DataSchemaSummary {
	for i := range schemas {
		if schemas[i].SchemaName == add.SchemaName {
			schemas[i].Count += add.Count
			schemas[i].TotalBytes += add.TotalBytes
			return schemas
		}
	}
	return append(schemas, add)
}

func localEPMFilterMatches(filter RawRecordQuery) bool {
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" && providerID != "local-node" {
		return false
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" && sourceName != "local-epm" {
		return false
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" && batchID != "local" {
		return false
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" && producerPeerID != "local-node" {
		return false
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" && producerPublicKey != "local-node" {
		return false
	}
	return true
}

func (s *FlatSQLStore) localEPMSummaryLocked() (int64, int64, error) {
	rows, err := s.db.Query(`
		SELECT encrypted_epm_bytes
		FROM sdn_local_epms
		WHERE schema_name = 'EPM.fbs'
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("summarize local EPMs: %w", err)
	}
	defer rows.Close()

	var count int64
	var totalBytes int64
	for rows.Next() {
		var encrypted string
		if err := rows.Scan(&encrypted); err != nil {
			return 0, 0, fmt.Errorf("scan local EPM summary: %w", err)
		}
		raw, err := s.decryptLocalEPMPayload(encrypted)
		if err != nil {
			return 0, 0, fmt.Errorf("decrypt local EPM summary payload: %w", err)
		}
		count++
		totalBytes += int64(len(raw))
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("local EPM summary rows failed: %w", err)
	}
	return count, totalBytes, nil
}

func (s *FlatSQLStore) queryLocalEPMRecordsLocked(filter RawRecordQuery, limit int) ([]*Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	query := `
		SELECT peer_id, encrypted_epm_bytes, updated_at
		FROM sdn_local_epms
		WHERE schema_name = 'EPM.fbs'
	`
	args := make([]interface{}, 0, 3)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND peer_id = ?`
		args = append(args, cid)
	}
	query += ` ORDER BY updated_at DESC, peer_id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query local EPM records: %w", err)
	}
	defer rows.Close()

	records := make([]*Record, 0, limit)
	for rows.Next() {
		var peerID string
		var encrypted string
		var updatedAt int64
		if err := rows.Scan(&peerID, &encrypted, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan local EPM record: %w", err)
		}
		epmBytes, err := s.decryptLocalEPMPayload(encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt local EPM record: %w", err)
		}
		records = append(records, &Record{
			CID:       peerID,
			PeerID:    peerID,
			Timestamp: time.Unix(updatedAt, 0).UTC(),
			Data:      epmBytes,
			SourceTags: SourceTags{
				ProviderID: "local-node",
				SourceName: "local-epm",
				BatchID:    "local",
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("local EPM rows failed: %w", err)
	}
	return records, nil
}

func (s *FlatSQLStore) countLocalEPMRecordsLocked(filter RawRecordQuery) (int64, error) {
	query := `
		SELECT COUNT(*)
		FROM sdn_local_epms
		WHERE schema_name = 'EPM.fbs'
	`
	args := make([]interface{}, 0, 1)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND peer_id = ?`
		args = append(args, cid)
	}
	var count int64
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count local EPM records: %w", err)
	}
	return count, nil
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
	maxLimit := 1000
	if filter.AllowLargeResultSet {
		maxLimit = 250000
	}
	if filter.Limit > maxLimit {
		filter.Limit = maxLimit
	}

	query := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp,
		       d.stream_path, d.stream_offset, d.record_length, d.signature_hex
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

	if filter.OrderByCID {
		if filter.ProviderID != "" || filter.SourceName != "" || filter.BatchID != "" {
			query += ` ORDER BY tags.cid ASC LIMIT ?`
		} else {
			query += ` ORDER BY d.cid ASC LIMIT ?`
		}
	} else {
		query += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) DESC, d.cid ASC LIMIT ?`
	}
	args = append(args, filter.Limit)
	if filter.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("indexed query failed: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		rec := &Record{}
		var ts int64
		var streamPath string
		var streamOffset, recordLength int64
		var signatureHex sql.NullString
		if err := rows.Scan(&rec.CID, &rec.PeerID, &ts, &streamPath, &streamOffset, &recordLength, &signatureHex); err != nil {
			return nil, fmt.Errorf("failed scanning indexed row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		if err := s.hydrateRecordData(rec, streamPath, streamOffset, recordLength, signatureHex); err != nil {
			return nil, fmt.Errorf("failed reading indexed record data: %w", err)
		}
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
		SELECT cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex
		FROM %s WHERE cid = ?
	`, tableName)

	var record Record
	var timestamp int64
	var streamPath string
	var streamOffset, recordLength int64
	var signatureHex sql.NullString
	err = s.db.QueryRow(querySQL, cid).Scan(
		&record.CID,
		&record.PeerID,
		&timestamp,
		&streamPath,
		&streamOffset,
		&recordLength,
		&signatureHex,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("not found: %s", cid)
		}
		return nil, fmt.Errorf("failed to get record: %w", err)
	}

	record.Timestamp = time.Unix(timestamp, 0)
	if err := s.hydrateRecordData(&record, streamPath, streamOffset, recordLength, signatureHex); err != nil {
		return nil, fmt.Errorf("failed to read record data: %w", err)
	}
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
		err = s.db.QueryRow(fmt.Sprintf(`SELECT SUM(record_length) FROM %s`, tableName)).Scan(&totalBytes)
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
		err = s.db.QueryRow(fmt.Sprintf(`SELECT SUM(record_length) FROM %s WHERE peer_id = ?`, tableName), peerID).Scan(&bytes)
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
	plgTableName, err := sds.SchemaNameToTable("PLG.fbs")
	if err != nil {
		return nil, fmt.Errorf("invalid PLG schema name: %w", err)
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT p.stream_path, p.stream_offset, p.record_length
		FROM sdn_log_index li
		INNER JOIN %s p ON p.cid = li.plg_cid
		WHERE li.publisher_peer_id = ?
		  AND li.schema_type = ?
		  AND li.sequence > ?
		ORDER BY li.sequence ASC
		LIMIT ?
	`, plgTableName), publisherPeerID, schemaType, sinceSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	defer rows.Close()

	var results [][]byte
	for rows.Next() {
		var streamPath string
		var streamOffset, recordLength int64
		if err := rows.Scan(&streamPath, &streamOffset, &recordLength); err != nil {
			log.Warnf("Failed to scan log entry: %v", err)
			continue
		}
		data, err := s.readFlatSQLStreamRecord(streamPath, streamOffset, recordLength)
		if err != nil {
			log.Warnf("Failed to read log entry FlatSQL stream record: %v", err)
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
