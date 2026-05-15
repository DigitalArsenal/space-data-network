package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DatasetPublicationQueryProfile = "dataset-publication-offset-v1"

// DefaultShardGroupCARMaxSourceBytes keeps provider-side CAR staging bounded
// for large historical feeds while preserving a small number of importable
// bundles for normal live publications.
const DefaultShardGroupCARMaxSourceBytes int64 = 512 << 20

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
	FeedSequence int64
	PreviousHead string
	FeedHead     string
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

func DatasetShardPublicationCARGroups(publications []DatasetShardPublication, maxSourceBytes int64) [][]DatasetShardPublication {
	ordered := append([]DatasetShardPublication(nil), publications...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].FeedSequence != ordered[j].FeedSequence {
			return ordered[i].FeedSequence < ordered[j].FeedSequence
		}
		return ordered[i].Offset < ordered[j].Offset
	})
	if maxSourceBytes <= 0 {
		if len(ordered) == 0 {
			return nil
		}
		return [][]DatasetShardPublication{ordered}
	}
	groups := make([][]DatasetShardPublication, 0, len(ordered))
	current := make([]DatasetShardPublication, 0)
	var currentBytes int64
	for _, publication := range ordered {
		publicationBytes := publication.ByteCount
		if publicationBytes < 1 {
			publicationBytes = 1
		}
		if len(current) > 0 && currentBytes+publicationBytes > maxSourceBytes {
			groups = append(groups, current)
			current = nil
			currentBytes = 0
		}
		current = append(current, publication)
		currentBytes += publicationBytes
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
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
			feed_sequence INTEGER NOT NULL DEFAULT 0,
			previous_head TEXT NOT NULL DEFAULT '',
			feed_head TEXT NOT NULL DEFAULT '',
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
	for _, column := range []struct {
		name string
		typ  string
	}{
		{"feed_sequence", "INTEGER NOT NULL DEFAULT 0"},
		{"previous_head", "TEXT NOT NULL DEFAULT ''"},
		{"feed_head", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn("sdn_dataset_shard_publications", column.name, column.typ); err != nil {
			return err
		}
	}
	if err := s.createStartupIndex("sdn_dataset_shard_publications", "idx_sdn_dataset_shards_lookup", existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_dataset_shards_lookup
		ON sdn_dataset_shard_publications (
			schema_name, query_profile, provider_id, source_name, batch_id, window_offset
		)
	`); err != nil {
		return err
	}
	return s.createStartupIndex("sdn_dataset_shard_publications", "idx_sdn_dataset_shards_feed", existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_dataset_shards_feed
		ON sdn_dataset_shard_publications (
			schema_name, query_profile, provider_id, source_name, batch_id, feed_sequence
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
	if err := s.populateDatasetShardPublicationFeedMetadataLocked(&pub); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO sdn_dataset_shard_publications (
			schema_name, provider_id, source_name, batch_id, query_profile,
			window_offset, window_limit, record_count, byte_count,
			shard_cid, index_cid, manifest_cid, pnm_cid,
			shard_sha256, index_sha256, query_sha256, result_sha256,
			feed_sequence, previous_head, feed_head, published_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
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
			feed_sequence = excluded.feed_sequence,
			previous_head = excluded.previous_head,
			feed_head = excluded.feed_head,
			published_at = excluded.published_at,
			updated_at = excluded.updated_at
	`, pub.SchemaName, pub.ProviderID, pub.SourceName, pub.BatchID, pub.QueryProfile,
		pub.Offset, pub.Limit, pub.RecordCount, pub.ByteCount,
		pub.ShardCID, pub.IndexCID, pub.ManifestCID, pub.PNMCID,
		pub.ShardSHA256, pub.IndexSHA256, pub.QuerySHA256, pub.ResultSHA256,
		pub.FeedSequence, pub.PreviousHead, pub.FeedHead,
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

func (s *FlatSQLStore) FindDatasetShardPublicationByCID(query DatasetShardPublicationQuery, shardCID string) (DatasetShardPublication, bool, error) {
	query = normalizeDatasetShardPublicationQuery(query)
	shardCID = strings.TrimSpace(shardCID)
	if query.SchemaName == "" {
		return DatasetShardPublication{}, false, errors.New("schema name is required")
	}
	if query.QueryProfile == "" {
		return DatasetShardPublication{}, false, errors.New("query profile is required")
	}
	if shardCID == "" {
		return DatasetShardPublication{}, false, errors.New("shard cid is required")
	}

	where := []string{`schema_name = ?`, `query_profile = ?`, `shard_cid = ?`}
	args := []interface{}{query.SchemaName, query.QueryProfile, shardCID}
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

	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`
		SELECT schema_name, provider_id, source_name, batch_id, query_profile,
		       window_offset, window_limit, record_count, byte_count,
		       shard_cid, index_cid, manifest_cid, pnm_cid,
		       shard_sha256, index_sha256, query_sha256, result_sha256,
		       feed_sequence, previous_head, feed_head, published_at
		FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY feed_sequence ASC, window_offset ASC, published_at DESC
		LIMIT 1
	`, args...)
	pub, err := scanDatasetShardPublication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DatasetShardPublication{}, false, nil
	}
	if err != nil {
		return DatasetShardPublication{}, false, fmt.Errorf("find dataset shard publication by cid: %w", err)
	}
	return pub, true, nil
}

func (s *FlatSQLStore) DatasetPublicationShardPath(pub DatasetShardPublication) (string, error) {
	if s == nil {
		return "", errors.New("FlatSQL store is unavailable")
	}
	pub = normalizeDatasetShardPublication(pub)
	if pub.SchemaName == "" {
		return "", errors.New("schema name is required")
	}
	if len(pub.QuerySHA256) < 16 {
		return "", errors.New("query sha256 is required")
	}
	if len(pub.ShardSHA256) < 16 {
		return "", errors.New("shard sha256 is required")
	}
	fileName := fmt.Sprintf("%s-%s.fbshard", pub.QuerySHA256[:16], pub.ShardSHA256[:16])
	return filepath.Join(s.DatasetPublicationOutputDir(), datasetPublicationPathComponent(pub.SchemaName), "shards", fileName), nil
}

func (s *FlatSQLStore) DatasetPublicationIndexPath(pub DatasetShardPublication) (string, error) {
	if s == nil {
		return "", errors.New("FlatSQL store is unavailable")
	}
	pub = normalizeDatasetShardPublication(pub)
	if pub.SchemaName == "" {
		return "", errors.New("schema name is required")
	}
	if len(pub.QuerySHA256) < 16 {
		return "", errors.New("query sha256 is required")
	}
	if len(pub.IndexSHA256) < 16 {
		return "", errors.New("index sha256 is required")
	}
	fileName := fmt.Sprintf("%s-%s.index.json", pub.QuerySHA256[:16], pub.IndexSHA256[:16])
	return filepath.Join(s.DatasetPublicationOutputDir(), datasetPublicationPathComponent(pub.SchemaName), "indexes", fileName), nil
}

func (s *FlatSQLStore) DatasetPublicationOutputDir() string {
	if s == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(s.basePath), "dataset-publications")
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
		       feed_sequence, previous_head, feed_head, published_at
		FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY feed_sequence ASC, window_offset ASC, published_at DESC
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

func (s *FlatSQLStore) DeleteDatasetShardPublicationsAtOrAfterOffset(query DatasetShardPublicationQuery, offset int) (int64, error) {
	query = normalizeDatasetShardPublicationQuery(query)
	if query.SchemaName == "" {
		return 0, errors.New("schema name is required")
	}
	if query.QueryProfile == "" {
		return 0, errors.New("query profile is required")
	}
	if offset < 0 {
		return 0, errors.New("offset must be non-negative")
	}

	where := []string{`schema_name = ?`, `query_profile = ?`, `window_offset >= ?`}
	args := []interface{}{query.SchemaName, query.QueryProfile, offset}
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

	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`
		DELETE FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return 0, fmt.Errorf("delete stale dataset shard publications: %w", err)
	}
	deleted, _ := result.RowsAffected()
	return deleted, nil
}

// FindLargestDatasetShardPublicationLimit returns the largest published shard
// window for a dataset query. Manifest readers use this to follow the actual
// provider artifact layout instead of the caller's UI page size.
func (s *FlatSQLStore) FindLargestDatasetShardPublicationLimit(query DatasetShardPublicationQuery) (int, bool, error) {
	query = normalizeDatasetShardPublicationQuery(query)
	if query.SchemaName == "" {
		return 0, false, errors.New("schema name is required")
	}
	if query.QueryProfile == "" {
		return 0, false, errors.New("query profile is required")
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

	s.mu.RLock()
	defer s.mu.RUnlock()
	var limit int
	err := s.db.QueryRow(`
		SELECT window_limit
		FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY window_limit DESC, published_at DESC
		LIMIT 1
	`, args...).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find largest dataset shard publication limit: %w", err)
	}
	return limit, true, nil
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
		       feed_sequence, previous_head, feed_head, published_at
		FROM sdn_dataset_shard_publications
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY published_at DESC
		LIMIT 1
	`, args...)
	return scanDatasetShardPublication(row)
}

func (s *FlatSQLStore) populateDatasetShardPublicationFeedMetadataLocked(pub *DatasetShardPublication) error {
	if pub == nil {
		return errors.New("dataset shard publication is required")
	}
	existing, err := s.findDatasetShardPublicationLocked(DatasetShardPublicationQuery{
		SchemaName:   pub.SchemaName,
		ProviderID:   pub.ProviderID,
		SourceName:   pub.SourceName,
		BatchID:      pub.BatchID,
		QueryProfile: pub.QueryProfile,
		Offset:       pub.Offset,
		Limit:        pub.Limit,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if pub.FeedSequence <= 0 && err == nil && existing.FeedSequence > 0 {
		pub.FeedSequence = existing.FeedSequence
	}
	if pub.PreviousHead == "" && err == nil && existing.PreviousHead != "" {
		pub.PreviousHead = existing.PreviousHead
	}
	if pub.FeedSequence <= 0 {
		previousSequence, previousHead, err := s.latestDatasetShardPublicationFeedLinkLocked(*pub)
		if err != nil {
			return err
		}
		pub.FeedSequence = previousSequence + 1
		if pub.PreviousHead == "" {
			pub.PreviousHead = previousHead
		}
	}
	pub.FeedHead = DatasetShardPublicationFeedHead(*pub)
	return nil
}

func (s *FlatSQLStore) latestDatasetShardPublicationFeedLinkLocked(pub DatasetShardPublication) (int64, string, error) {
	var sequence int64
	var head string
	err := s.db.QueryRow(`
		SELECT feed_sequence, feed_head
		FROM sdn_dataset_shard_publications
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND query_profile = ?
		  AND NOT (window_offset = ? AND window_limit = ?)
		ORDER BY feed_sequence DESC, window_offset DESC, published_at DESC
		LIMIT 1
	`, pub.SchemaName, pub.ProviderID, pub.SourceName, pub.BatchID, pub.QueryProfile, pub.Offset, pub.Limit).Scan(&sequence, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("find previous dataset shard feed link: %w", err)
	}
	return sequence, head, nil
}

func DatasetShardPublicationFeedHead(pub DatasetShardPublication) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(
		hash,
		"sdn-dataset-feed-entry-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\n",
		pub.SchemaName,
		pub.ProviderID,
		pub.SourceName,
		pub.BatchID,
		pub.QueryProfile,
		pub.FeedSequence,
		pub.PreviousHead,
	)
	_, _ = fmt.Fprintf(
		hash,
		"%d\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\n",
		pub.Offset,
		pub.Limit,
		pub.RecordCount,
		pub.ByteCount,
		pub.ShardCID,
		pub.IndexCID,
		pub.ManifestCID,
		pub.PNMCID,
		pub.ShardSHA256,
		pub.IndexSHA256,
		pub.QuerySHA256,
		pub.ResultSHA256,
		pub.PublishedAt.UTC().Unix(),
	)
	return hex.EncodeToString(hash.Sum(nil))
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
		&pub.FeedSequence,
		&pub.PreviousHead,
		&pub.FeedHead,
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
	pub.PreviousHead = strings.TrimSpace(pub.PreviousHead)
	pub.FeedHead = strings.TrimSpace(pub.FeedHead)
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

func datasetPublicationPathComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".fbs")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	value = replacer.Replace(value)
	if value == "" {
		return "dataset"
	}
	return value
}
