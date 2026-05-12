package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const DatasetPublicationQueryProfile = "dataset-publication-offset-v1"

// DatasetShardPublication records one immutable FlatSQL/DPM shard pinned to IPFS.
type DatasetShardPublication struct {
	SchemaName   string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Offset       int
	Limit        int
	RecordCount  int
	ByteCount    int64
	ShardCID     string
	IndexCID     string
	ManifestCID  string
	PNMCID       string
	ShardSHA256  string
	IndexSHA256  string
	QuerySHA256  string
	ResultSHA256 string
	PublishedAt  time.Time
}

// DatasetShardPublicationQuery looks up a published shard for a sync window.
type DatasetShardPublicationQuery struct {
	SchemaName   string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Offset       int
	Limit        int
	RecordCount  int
}

func (s *FlatSQLStore) initDatasetShardPublicationTable() error {
	existed, err := s.tableExists("sdn_dataset_shard_publications")
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_dataset_shard_publications (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			batch_id TEXT NOT NULL DEFAULT '',
			query_profile TEXT NOT NULL DEFAULT '',
			window_offset INTEGER NOT NULL,
			window_limit INTEGER NOT NULL,
			record_count INTEGER NOT NULL,
			byte_count INTEGER NOT NULL,
			shard_cid TEXT NOT NULL,
			index_cid TEXT NOT NULL,
			manifest_cid TEXT NOT NULL DEFAULT '',
			pnm_cid TEXT NOT NULL DEFAULT '',
			shard_sha256 TEXT NOT NULL DEFAULT '',
			index_sha256 TEXT NOT NULL DEFAULT '',
			query_sha256 TEXT NOT NULL DEFAULT '',
			result_sha256 TEXT NOT NULL DEFAULT '',
			published_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (
				schema_name, provider_id, source_name, batch_id,
				query_profile, window_offset, window_limit
			)
		)
	`); err != nil {
		return fmt.Errorf("failed to create dataset shard publication table: %w", err)
	}
	return s.createStartupIndex("sdn_dataset_shard_publications", "idx_sdn_dataset_shards_lookup", existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_dataset_shards_lookup
		ON sdn_dataset_shard_publications (
			schema_name, query_profile, provider_id, source_name, batch_id, window_offset
		)
	`)
}

func (s *FlatSQLStore) UpsertDatasetShardPublication(pub DatasetShardPublication) error {
	pub = normalizeDatasetShardPublication(pub)
	if pub.SchemaName == "" {
		return errors.New("schema name is required")
	}
	if pub.QueryProfile == "" {
		return errors.New("query profile is required")
	}
	if pub.Offset < 0 {
		return errors.New("offset must be non-negative")
	}
	if pub.Limit <= 0 {
		return errors.New("limit must be positive")
	}
	if pub.RecordCount <= 0 {
		return errors.New("record count must be positive")
	}
	if pub.ByteCount < 0 {
		return errors.New("byte count must be non-negative")
	}
	if pub.ShardCID == "" {
		return errors.New("shard CID is required")
	}
	if pub.IndexCID == "" {
		return errors.New("index CID is required")
	}
	if pub.PublishedAt.IsZero() {
		pub.PublishedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO sdn_dataset_shard_publications (
			schema_name, provider_id, source_name, batch_id, query_profile,
			window_offset, window_limit, record_count, byte_count,
			shard_cid, index_cid, manifest_cid, pnm_cid,
			shard_sha256, index_sha256, query_sha256, result_sha256,
			published_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
		ON CONFLICT(schema_name, provider_id, source_name, batch_id, query_profile, window_offset, window_limit)
		DO UPDATE SET
			record_count = excluded.record_count,
			byte_count = excluded.byte_count,
			shard_cid = excluded.shard_cid,
			index_cid = excluded.index_cid,
			manifest_cid = excluded.manifest_cid,
			pnm_cid = excluded.pnm_cid,
			shard_sha256 = excluded.shard_sha256,
			index_sha256 = excluded.index_sha256,
			query_sha256 = excluded.query_sha256,
			result_sha256 = excluded.result_sha256,
			published_at = excluded.published_at,
			updated_at = excluded.updated_at
	`, pub.SchemaName, pub.ProviderID, pub.SourceName, pub.BatchID, pub.QueryProfile,
		pub.Offset, pub.Limit, pub.RecordCount, pub.ByteCount,
		pub.ShardCID, pub.IndexCID, pub.ManifestCID, pub.PNMCID,
		pub.ShardSHA256, pub.IndexSHA256, pub.QuerySHA256, pub.ResultSHA256,
		pub.PublishedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert dataset shard publication: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) FindDatasetShardPublication(query DatasetShardPublicationQuery) (DatasetShardPublication, bool, error) {
	query = normalizeDatasetShardPublicationQuery(query)
	if query.SchemaName == "" {
		return DatasetShardPublication{}, false, errors.New("schema name is required")
	}
	if query.QueryProfile == "" {
		return DatasetShardPublication{}, false, errors.New("query profile is required")
	}
	if query.Offset < 0 {
		return DatasetShardPublication{}, false, errors.New("offset must be non-negative")
	}
	if query.Limit <= 0 {
		return DatasetShardPublication{}, false, errors.New("limit must be positive")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	pub, err := s.findDatasetShardPublicationLocked(query)
	if errors.Is(err, sql.ErrNoRows) {
		return DatasetShardPublication{}, false, nil
	}
	if err != nil {
		return DatasetShardPublication{}, false, err
	}
	return pub, true, nil
}

func (s *FlatSQLStore) ListDatasetShardPublications(query DatasetShardPublicationQuery) ([]DatasetShardPublication, error) {
	query = normalizeDatasetShardPublicationQuery(query)
	if query.SchemaName == "" {
		return nil, errors.New("schema name is required")
	}
	if query.QueryProfile == "" {
		return nil, errors.New("query profile is required")
	}

	where := []string{`schema_name = ?`, `query_profile = ?`}
	args := []interface{}{query.SchemaName, query.QueryProfile}
	if query.ProviderID != "" {
		where = append(where, `provider_id = ?`)
		args = append(args, query.ProviderID)
	}
	if query.SourceName != "" {
		where = append(where, `source_name = ?`)
		args = append(args, query.SourceName)
	}
	if query.BatchID != "" {
		where = append(where, `batch_id = ?`)
		args = append(args, query.BatchID)
	}
	if query.Limit > 0 {
		where = append(where, `window_limit = ?`)
		args = append(args, query.Limit)
	}
	if query.RecordCount > 0 {
		where = append(where, `record_count = ?`)
		args = append(args, query.RecordCount)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT schema_name, provider_id, source_name, batch_id, query_profile,
		       window_offset, window_limit, record_count, byte_count,
		       shard_cid, index_cid, manifest_cid, pnm_cid,
		       shard_sha256, index_sha256, query_sha256, result_sha256,
		       published_at
		FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY window_offset ASC, published_at DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list dataset shard publications: %w", err)
	}
	defer rows.Close()

	var publications []DatasetShardPublication
	for rows.Next() {
		pub, err := scanDatasetShardPublication(rows)
		if err != nil {
			return nil, err
		}
		publications = append(publications, pub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list dataset shard publications rows: %w", err)
	}
	return publications, nil
}

func (s *FlatSQLStore) findDatasetShardPublicationLocked(query DatasetShardPublicationQuery) (DatasetShardPublication, error) {
	where := []string{`schema_name = ?`, `query_profile = ?`, `window_offset = ?`, `window_limit = ?`}
	args := []interface{}{query.SchemaName, query.QueryProfile, query.Offset, query.Limit}
	if query.ProviderID != "" {
		where = append(where, `provider_id = ?`)
		args = append(args, query.ProviderID)
	}
	if query.SourceName != "" {
		where = append(where, `source_name = ?`)
		args = append(args, query.SourceName)
	}
	if query.BatchID != "" {
		where = append(where, `batch_id = ?`)
		args = append(args, query.BatchID)
	}
	if query.RecordCount > 0 {
		where = append(where, `record_count = ?`)
		args = append(args, query.RecordCount)
	}

	row := s.db.QueryRow(`
		SELECT schema_name, provider_id, source_name, batch_id, query_profile,
		       window_offset, window_limit, record_count, byte_count,
		       shard_cid, index_cid, manifest_cid, pnm_cid,
		       shard_sha256, index_sha256, query_sha256, result_sha256,
		       published_at
		FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY published_at DESC
		LIMIT 1
	`, args...)
	return scanDatasetShardPublication(row)
}

type datasetShardPublicationScanner interface {
	Scan(dest ...interface{}) error
}

func scanDatasetShardPublication(scanner datasetShardPublicationScanner) (DatasetShardPublication, error) {
	var pub DatasetShardPublication
	var publishedAt int64
	if err := scanner.Scan(
		&pub.SchemaName,
		&pub.ProviderID,
		&pub.SourceName,
		&pub.BatchID,
		&pub.QueryProfile,
		&pub.Offset,
		&pub.Limit,
		&pub.RecordCount,
		&pub.ByteCount,
		&pub.ShardCID,
		&pub.IndexCID,
		&pub.ManifestCID,
		&pub.PNMCID,
		&pub.ShardSHA256,
		&pub.IndexSHA256,
		&pub.QuerySHA256,
		&pub.ResultSHA256,
		&publishedAt,
	); err != nil {
		return DatasetShardPublication{}, err
	}
	pub.PublishedAt = time.Unix(publishedAt, 0).UTC()
	return pub, nil
}

func normalizeDatasetShardPublication(pub DatasetShardPublication) DatasetShardPublication {
	pub.SchemaName = strings.TrimSpace(pub.SchemaName)
	pub.ProviderID = strings.TrimSpace(pub.ProviderID)
	pub.SourceName = strings.TrimSpace(pub.SourceName)
	pub.BatchID = strings.TrimSpace(pub.BatchID)
	pub.QueryProfile = strings.TrimSpace(pub.QueryProfile)
	pub.ShardCID = strings.TrimSpace(pub.ShardCID)
	pub.IndexCID = strings.TrimSpace(pub.IndexCID)
	pub.ManifestCID = strings.TrimSpace(pub.ManifestCID)
	pub.PNMCID = strings.TrimSpace(pub.PNMCID)
	pub.ShardSHA256 = strings.TrimSpace(pub.ShardSHA256)
	pub.IndexSHA256 = strings.TrimSpace(pub.IndexSHA256)
	pub.QuerySHA256 = strings.TrimSpace(pub.QuerySHA256)
	pub.ResultSHA256 = strings.TrimSpace(pub.ResultSHA256)
	return pub
}

func normalizeDatasetShardPublicationQuery(query DatasetShardPublicationQuery) DatasetShardPublicationQuery {
	query.SchemaName = strings.TrimSpace(query.SchemaName)
	query.ProviderID = strings.TrimSpace(query.ProviderID)
	query.SourceName = strings.TrimSpace(query.SourceName)
	query.BatchID = strings.TrimSpace(query.BatchID)
	query.QueryProfile = strings.TrimSpace(query.QueryProfile)
	return query
}
