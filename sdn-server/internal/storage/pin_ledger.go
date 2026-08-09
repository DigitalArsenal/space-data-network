package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PinLedgerEntry struct {
	CID               string
	SchemaName        string
	ProviderPeerID    string
	ProviderPublicKey string
	ProviderID        string
	SourceName        string
	BatchID           string
	QueryProfile      string
	SnapshotID        string
	Head              string
	HighWaterMark     string
	ByteHash          string
	Role              string
	SegmentStart      int
	SegmentCount      int
	RowCount          int64
	ByteCount         int64
	TTL               time.Duration
	VerificationState string
	VerifiedAt        time.Time
	UpdatedAt         time.Time
}

type PinLedgerQuery struct {
	CID               string
	SchemaName        string
	ProviderPeerID    string
	ProviderPublicKey string
	ProviderID        string
	SourceName        string
	BatchID           string
	QueryProfile      string
	Role              string
	VerificationState string
}

type LocalReplicaStatsQuery struct {
	SchemaName        string
	ProviderPeerID    string
	ProviderPublicKey string
	ProviderID        string
	SourceName        string
	BatchID           string
	QueryProfile      string
}

type LocalReplicaStats struct {
	SchemaName        string
	ProviderPeerID    string
	ProviderPublicKey string
	ProviderID        string
	SourceName        string
	BatchID           string
	QueryProfile      string
	LocalRows         int64
	PinnedRows        int64
	CachedBytes       int64
	PinnedBytes       int64
	SnapshotID        string
	Head              string
	HighWaterMark     string
	LastSyncedAt      time.Time
}

func (s *FlatSQLStore) initPinLedgerTable() error {
	existed, err := s.tableExists("sdn_pin_ledger")
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_pin_ledger (
			cid TEXT NOT NULL,
			schema_name TEXT NOT NULL,
			provider_peer_id TEXT NOT NULL DEFAULT '',
			provider_public_key TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			batch_id TEXT NOT NULL DEFAULT '',
			query_profile TEXT NOT NULL DEFAULT '',
			snapshot_id TEXT NOT NULL DEFAULT '',
			head TEXT NOT NULL DEFAULT '',
			high_water_mark TEXT NOT NULL DEFAULT '',
			byte_hash TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT '',
			segment_start INTEGER NOT NULL DEFAULT 0,
			segment_count INTEGER NOT NULL DEFAULT 0,
			row_count INTEGER NOT NULL DEFAULT 0,
			byte_count INTEGER NOT NULL DEFAULT 0,
			ttl_seconds INTEGER NOT NULL DEFAULT 0,
			verification_state TEXT NOT NULL DEFAULT '',
			verified_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (
				cid, schema_name, provider_peer_id, provider_public_key,
				provider_id, source_name, batch_id, query_profile, role
			)
		)
	`); err != nil {
		return fmt.Errorf("failed to create pin ledger table: %w", err)
	}
	if err := s.ensureColumn("sdn_pin_ledger", "high_water_mark", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("sdn_pin_ledger", "row_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("sdn_pin_ledger", "segment_start", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("sdn_pin_ledger", "segment_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.createStartupIndex("sdn_pin_ledger", "idx_sdn_pin_ledger_source", existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_pin_ledger_source
		ON sdn_pin_ledger (
			schema_name, query_profile, provider_peer_id, provider_public_key,
			provider_id, source_name, batch_id
		)
	`); err != nil {
		return err
	}
	return s.createStartupIndex("sdn_pin_ledger", "idx_sdn_pin_ledger_verification", existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_pin_ledger_verification
		ON sdn_pin_ledger (verification_state, updated_at DESC)
	`)
}

func (s *FlatSQLStore) UpsertPinLedgerEntry(entry PinLedgerEntry) error {
	if err := s.requireWritable("upsert pin ledger entry"); err != nil {
		return err
	}
	entry = normalizePinLedgerEntry(entry)
	if entry.CID == "" {
		return errors.New("pin ledger CID is required")
	}
	if entry.SchemaName == "" {
		return errors.New("pin ledger schema name is required")
	}
	if entry.ByteCount < 0 {
		return errors.New("pin ledger byte count must be non-negative")
	}
	if entry.RowCount < 0 {
		return errors.New("pin ledger row count must be non-negative")
	}
	if entry.SegmentStart < 0 {
		return errors.New("pin ledger segment start must be non-negative")
	}
	if entry.SegmentCount < 0 {
		return errors.New("pin ledger segment count must be non-negative")
	}
	if entry.VerificationState == "" {
		return errors.New("pin ledger verification state is required")
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyPinLedgerEntryUpsert(entry); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{
		Kind:      auxiliaryEventPinLedgerUpsert,
		PinLedger: &entry,
	}); err != nil {
		return fmt.Errorf("append pin ledger metadata: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) applyPinLedgerEntryUpsert(entry PinLedgerEntry) error {
	_, err := s.auxWrite().Exec(`
		INSERT INTO sdn_pin_ledger (
			cid, schema_name, provider_peer_id, provider_public_key,
			provider_id, source_name, batch_id, query_profile,
			snapshot_id, head, high_water_mark, byte_hash, role, segment_start, segment_count, row_count, byte_count,
			ttl_seconds, verification_state, verified_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(
			cid, schema_name, provider_peer_id, provider_public_key,
			provider_id, source_name, batch_id, query_profile, role
		)
		DO UPDATE SET
			snapshot_id = excluded.snapshot_id,
			head = excluded.head,
			high_water_mark = excluded.high_water_mark,
			byte_hash = excluded.byte_hash,
			segment_start = excluded.segment_start,
			segment_count = excluded.segment_count,
			row_count = excluded.row_count,
			byte_count = excluded.byte_count,
			ttl_seconds = excluded.ttl_seconds,
			verification_state = excluded.verification_state,
			verified_at = excluded.verified_at,
			updated_at = excluded.updated_at
	`, entry.CID, entry.SchemaName, entry.ProviderPeerID, entry.ProviderPublicKey,
		entry.ProviderID, entry.SourceName, entry.BatchID, entry.QueryProfile,
		entry.SnapshotID, entry.Head, entry.HighWaterMark, entry.ByteHash, entry.Role, entry.SegmentStart, entry.SegmentCount, entry.RowCount, entry.ByteCount,
		int64(entry.TTL.Seconds()), entry.VerificationState, unixOrZero(entry.VerifiedAt), entry.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert pin ledger entry: %w", err)
	}
	return nil
}

// pinLedgerWhere builds the shared WHERE clause every pin-ledger read uses.
// It never emits a constant-true term: `1 = 1` is not free on every planner,
// and none of the callers need it.
func pinLedgerWhere(query PinLedgerQuery) (string, []interface{}) {
	where := []string{}
	args := []interface{}{}
	add := func(column, value string) {
		if value == "" {
			return
		}
		where = append(where, column+" = ?")
		args = append(args, value)
	}
	add("cid", query.CID)
	add("schema_name", query.SchemaName)
	add("provider_peer_id", query.ProviderPeerID)
	add("provider_public_key", query.ProviderPublicKey)
	add("provider_id", query.ProviderID)
	add("source_name", query.SourceName)
	add("batch_id", query.BatchID)
	add("query_profile", query.QueryProfile)
	add("role", query.Role)
	add("verification_state", query.VerificationState)
	if len(where) == 0 {
		return "1 = 1", args
	}
	return strings.Join(where, " AND "), args
}

// HasPinLedgerEntry answers the ONE-BIT question "does any matching row exist"
// without materializing a row, ordering a result set, or paying for the columns.
//
// This exists because the boolean was being answered by ListPinLedgerEntries,
// which SELECTs 21 columns and sorts them by (updated_at DESC, cid ASC) — a
// full result-set build and a temp B-tree for a question whose answer is one
// bit. Every engine round trip on a loaded node is measured in seconds
// (sdn-pin-ledger-probe-costs-76-seconds), so the shape of the query matters
// more than the row count suggests.
func (s *FlatSQLStore) HasPinLedgerEntry(query PinLedgerQuery) (bool, error) {
	query = normalizePinLedgerQuery(query)
	clause, args := pinLedgerWhere(query)

	s.mu.RLock()
	defer s.mu.RUnlock()
	var present int
	err := s.db.QueryRow(`
		SELECT 1
		FROM sdn_pin_ledger
		WHERE `+clause+`
		LIMIT 1
	`, args...).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe pin ledger entry: %w", err)
	}
	return true, nil
}

// DistinctPinLedgerSchemas returns the distinct schema_name values of the rows
// matching query.
//
// Callers that would otherwise sweep every SUPPORTED schema looking for
// ledger-derived evidence should ask the ledger which schemas it actually holds
// evidence for: the sweep can only produce output for those. One indexed read
// replaces a per-schema fan-out.
func (s *FlatSQLStore) DistinctPinLedgerSchemas(query PinLedgerQuery) ([]string, error) {
	query = normalizePinLedgerQuery(query)
	clause, args := pinLedgerWhere(query)

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT DISTINCT schema_name
		FROM sdn_pin_ledger
		WHERE `+clause+`
		ORDER BY schema_name ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list pin ledger schemas: %w", err)
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var schemaName string
		if err := rows.Scan(&schemaName); err != nil {
			return nil, fmt.Errorf("scan pin ledger schema: %w", err)
		}
		if schemaName = strings.TrimSpace(schemaName); schemaName != "" {
			schemas = append(schemas, schemaName)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pin ledger schemas rows: %w", err)
	}
	return schemas, nil
}

func (s *FlatSQLStore) ListPinLedgerEntries(query PinLedgerQuery) ([]PinLedgerEntry, error) {
	query = normalizePinLedgerQuery(query)
	clause, args := pinLedgerWhere(query)

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT cid, schema_name, provider_peer_id, provider_public_key,
		       provider_id, source_name, batch_id, query_profile,
		       snapshot_id, head, high_water_mark, byte_hash, role, segment_start, segment_count, row_count, byte_count,
		       ttl_seconds, verification_state, verified_at, updated_at
		FROM sdn_pin_ledger
		WHERE `+clause+`
		ORDER BY updated_at DESC, cid ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list pin ledger entries: %w", err)
	}
	defer rows.Close()

	var entries []PinLedgerEntry
	for rows.Next() {
		entry, err := scanPinLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pin ledger entries rows: %w", err)
	}
	return entries, nil
}

func (s *FlatSQLStore) LocalReplicaStats(query LocalReplicaStatsQuery) ([]LocalReplicaStats, error) {
	query = normalizeLocalReplicaStatsQuery(query)
	statsByKey := map[string]*LocalReplicaStats{}
	sourceStatsByKey := map[string]string{}
	countedShards := map[string]bool{}
	publicationSummaries := map[string]*localReplicaPublicationSummary{}

	ledger, err := s.ListPinLedgerEntries(PinLedgerQuery{
		SchemaName:        query.SchemaName,
		ProviderPeerID:    query.ProviderPeerID,
		ProviderPublicKey: query.ProviderPublicKey,
		ProviderID:        query.ProviderID,
		SourceName:        query.SourceName,
		BatchID:           query.BatchID,
		QueryProfile:      query.QueryProfile,
		Role:              "shard",
		VerificationState: "verified",
	})
	if err != nil {
		return nil, err
	}

	for _, entry := range ledger {
		stat := localReplicaStatForPinEntry(statsByKey, sourceStatsByKey, entry)
		addLocalReplicaPinnedEvidence(stat, countedShards, entry.CID, entry.RowCount, entry.ByteCount)
		entryTime := entry.VerifiedAt
		if entryTime.IsZero() {
			entryTime = entry.UpdatedAt
		}
		updateLocalReplicaStatsHead(stat, entryTime, entry.SnapshotID, entry.Head, entry.HighWaterMark)
	}

	publications, err := s.localReplicaDatasetShardPublications(query)
	if err != nil {
		return nil, err
	}
	for _, pub := range publications {
		stat, sourceKey := localReplicaStatForPublication(statsByKey, sourceStatsByKey, pub)
		summary := publicationSummaries[sourceKey]
		if summary == nil {
			summary = &localReplicaPublicationSummary{}
			publicationSummaries[sourceKey] = summary
		}
		summary.add(pub)
		addLocalReplicaPinnedEvidence(stat, countedShards, pub.ShardCID, int64(pub.RecordCount), pub.ByteCount)
		updateLocalReplicaStatsHead(stat, pub.PublishedAt, pub.FeedHead, pub.FeedHead, "")
	}
	for sourceKey, summary := range publicationSummaries {
		statKey := sourceStatsByKey[sourceKey]
		stat := statsByKey[statKey]
		if stat == nil {
			continue
		}
		if stat.LastSyncedAt.Equal(summary.NewestAt) && stat.Head == summary.NewestHead {
			stat.HighWaterMark = summary.highWaterMark()
		} else if stat.HighWaterMark == "" {
			stat.HighWaterMark = summary.highWaterMark()
		}
	}

	if len(statsByKey) == 0 && query.SchemaName != "" {
		key := localReplicaStatsKey(query.SchemaName, query.ProviderPeerID, query.ProviderPublicKey, query.ProviderID, query.SourceName, query.BatchID, query.QueryProfile)
		statsByKey[key] = &LocalReplicaStats{
			SchemaName:        query.SchemaName,
			ProviderPeerID:    query.ProviderPeerID,
			ProviderPublicKey: query.ProviderPublicKey,
			ProviderID:        query.ProviderID,
			SourceName:        query.SourceName,
			BatchID:           query.BatchID,
			QueryProfile:      query.QueryProfile,
		}
	}

	stats := make([]LocalReplicaStats, 0, len(statsByKey))
	for _, stat := range statsByKey {
		localRows, cachedBytes, err := s.localReplicaRawStats(*stat, query)
		if err != nil {
			return nil, err
		}
		stat.LocalRows = localRows
		stat.CachedBytes = cachedBytes
		stats = append(stats, *stat)
	}
	return stats, nil
}

type localReplicaPublicationSummary struct {
	Count      int
	TotalRows  int64
	TotalBytes int64
	NewestAt   time.Time
	NewestHead string
}

func (summary *localReplicaPublicationSummary) add(pub DatasetShardPublication) {
	if summary == nil {
		return
	}
	summary.Count++
	summary.TotalRows += int64(pub.RecordCount)
	summary.TotalBytes += pub.ByteCount
	if summary.NewestAt.IsZero() || pub.PublishedAt.After(summary.NewestAt) {
		summary.NewestAt = pub.PublishedAt
		summary.NewestHead = pub.FeedHead
	}
}

func (summary localReplicaPublicationSummary) highWaterMark() string {
	return fmt.Sprintf("published-feed-v1:%d:%d:%d:%d", unixOrZero(summary.NewestAt), summary.Count, summary.TotalRows, summary.TotalBytes)
}

func localReplicaStatForPinEntry(statsByKey map[string]*LocalReplicaStats, sourceStatsByKey map[string]string, entry PinLedgerEntry) *LocalReplicaStats {
	key := localReplicaStatsKey(entry.SchemaName, entry.ProviderPeerID, entry.ProviderPublicKey, entry.ProviderID, entry.SourceName, entry.BatchID, entry.QueryProfile)
	stat := statsByKey[key]
	if stat == nil {
		stat = &LocalReplicaStats{
			SchemaName:        entry.SchemaName,
			ProviderPeerID:    entry.ProviderPeerID,
			ProviderPublicKey: entry.ProviderPublicKey,
			ProviderID:        entry.ProviderID,
			SourceName:        entry.SourceName,
			BatchID:           entry.BatchID,
			QueryProfile:      entry.QueryProfile,
		}
		statsByKey[key] = stat
	}
	sourceKey := localReplicaSourceStatsKey(entry.SchemaName, entry.ProviderID, entry.SourceName, entry.BatchID, entry.QueryProfile)
	if sourceStatsByKey[sourceKey] == "" {
		sourceStatsByKey[sourceKey] = key
	}
	return stat
}

func localReplicaStatForPublication(statsByKey map[string]*LocalReplicaStats, sourceStatsByKey map[string]string, pub DatasetShardPublication) (*LocalReplicaStats, string) {
	sourceKey := localReplicaSourceStatsKey(pub.SchemaName, pub.ProviderID, pub.SourceName, pub.BatchID, pub.QueryProfile)
	if key := sourceStatsByKey[sourceKey]; key != "" {
		if stat := statsByKey[key]; stat != nil {
			return stat, sourceKey
		}
	}
	key := localReplicaStatsKey(pub.SchemaName, "", "", pub.ProviderID, pub.SourceName, pub.BatchID, pub.QueryProfile)
	stat := statsByKey[key]
	if stat == nil {
		stat = &LocalReplicaStats{
			SchemaName:   pub.SchemaName,
			ProviderID:   pub.ProviderID,
			SourceName:   pub.SourceName,
			BatchID:      pub.BatchID,
			QueryProfile: pub.QueryProfile,
		}
		statsByKey[key] = stat
	}
	sourceStatsByKey[sourceKey] = key
	return stat, sourceKey
}

func addLocalReplicaPinnedEvidence(stat *LocalReplicaStats, countedShards map[string]bool, shardCID string, rowCount, byteCount int64) {
	if stat == nil {
		return
	}
	evidenceKey := localReplicaShardEvidenceKey(stat.SchemaName, stat.ProviderID, stat.SourceName, stat.BatchID, stat.QueryProfile, shardCID)
	if countedShards[evidenceKey] {
		return
	}
	countedShards[evidenceKey] = true
	stat.PinnedRows += rowCount
	stat.PinnedBytes += byteCount
}

func updateLocalReplicaStatsHead(stat *LocalReplicaStats, syncedAt time.Time, snapshotID, head, highWaterMark string) {
	if stat == nil || syncedAt.IsZero() {
		return
	}
	if stat.LastSyncedAt.IsZero() || syncedAt.After(stat.LastSyncedAt) {
		stat.LastSyncedAt = syncedAt
		stat.SnapshotID = snapshotID
		stat.Head = head
		stat.HighWaterMark = highWaterMark
	}
}

func (s *FlatSQLStore) localReplicaDatasetShardPublications(query LocalReplicaStatsQuery) ([]DatasetShardPublication, error) {
	if query.SchemaName == "" || query.QueryProfile == "" {
		return nil, nil
	}
	if query.ProviderPeerID != "" || query.ProviderPublicKey != "" {
		return nil, nil
	}
	return s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   query.SchemaName,
		ProviderID:   query.ProviderID,
		SourceName:   query.SourceName,
		BatchID:      query.BatchID,
		QueryProfile: query.QueryProfile,
	})
}

type pinLedgerScanner interface {
	Scan(dest ...interface{}) error
}

func scanPinLedgerEntry(scanner pinLedgerScanner) (PinLedgerEntry, error) {
	var entry PinLedgerEntry
	var ttlSeconds int64
	var verifiedAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&entry.CID,
		&entry.SchemaName,
		&entry.ProviderPeerID,
		&entry.ProviderPublicKey,
		&entry.ProviderID,
		&entry.SourceName,
		&entry.BatchID,
		&entry.QueryProfile,
		&entry.SnapshotID,
		&entry.Head,
		&entry.HighWaterMark,
		&entry.ByteHash,
		&entry.Role,
		&entry.SegmentStart,
		&entry.SegmentCount,
		&entry.RowCount,
		&entry.ByteCount,
		&ttlSeconds,
		&entry.VerificationState,
		&verifiedAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PinLedgerEntry{}, err
		}
		return PinLedgerEntry{}, fmt.Errorf("scan pin ledger entry: %w", err)
	}
	entry.TTL = time.Duration(ttlSeconds) * time.Second
	entry.VerifiedAt = timeOrZero(verifiedAt)
	entry.UpdatedAt = timeOrZero(updatedAt)
	return entry, nil
}

func normalizePinLedgerEntry(entry PinLedgerEntry) PinLedgerEntry {
	entry.CID = strings.TrimSpace(entry.CID)
	entry.SchemaName = strings.TrimSpace(entry.SchemaName)
	entry.ProviderPeerID = strings.TrimSpace(entry.ProviderPeerID)
	entry.ProviderPublicKey = strings.TrimSpace(entry.ProviderPublicKey)
	entry.ProviderID = strings.TrimSpace(entry.ProviderID)
	entry.SourceName = strings.TrimSpace(entry.SourceName)
	entry.BatchID = strings.TrimSpace(entry.BatchID)
	entry.QueryProfile = strings.TrimSpace(entry.QueryProfile)
	entry.SnapshotID = strings.TrimSpace(entry.SnapshotID)
	entry.Head = strings.TrimSpace(entry.Head)
	entry.HighWaterMark = strings.TrimSpace(entry.HighWaterMark)
	entry.ByteHash = strings.TrimSpace(entry.ByteHash)
	entry.Role = strings.TrimSpace(entry.Role)
	entry.VerificationState = strings.TrimSpace(entry.VerificationState)
	return entry
}

func normalizePinLedgerQuery(query PinLedgerQuery) PinLedgerQuery {
	query.CID = strings.TrimSpace(query.CID)
	query.SchemaName = strings.TrimSpace(query.SchemaName)
	query.ProviderPeerID = strings.TrimSpace(query.ProviderPeerID)
	query.ProviderPublicKey = strings.TrimSpace(query.ProviderPublicKey)
	query.ProviderID = strings.TrimSpace(query.ProviderID)
	query.SourceName = strings.TrimSpace(query.SourceName)
	query.BatchID = strings.TrimSpace(query.BatchID)
	query.QueryProfile = strings.TrimSpace(query.QueryProfile)
	query.Role = strings.TrimSpace(query.Role)
	query.VerificationState = strings.TrimSpace(query.VerificationState)
	return query
}

func normalizeLocalReplicaStatsQuery(query LocalReplicaStatsQuery) LocalReplicaStatsQuery {
	query.SchemaName = strings.TrimSpace(query.SchemaName)
	query.ProviderPeerID = strings.TrimSpace(query.ProviderPeerID)
	query.ProviderPublicKey = strings.TrimSpace(query.ProviderPublicKey)
	query.ProviderID = strings.TrimSpace(query.ProviderID)
	query.SourceName = strings.TrimSpace(query.SourceName)
	query.BatchID = strings.TrimSpace(query.BatchID)
	query.QueryProfile = strings.TrimSpace(query.QueryProfile)
	return query
}

func localReplicaStatsKey(schemaName, providerPeerID, providerPublicKey, providerID, sourceName, batchID, queryProfile string) string {
	return strings.Join([]string{schemaName, providerPeerID, providerPublicKey, providerID, sourceName, batchID, queryProfile}, "\x00")
}

func localReplicaSourceStatsKey(schemaName, providerID, sourceName, batchID, queryProfile string) string {
	return strings.Join([]string{schemaName, providerID, sourceName, batchID, queryProfile}, "\x00")
}

func localReplicaShardEvidenceKey(schemaName, providerID, sourceName, batchID, queryProfile, shardCID string) string {
	return strings.Join([]string{schemaName, providerID, sourceName, batchID, queryProfile, shardCID}, "\x00")
}

func (s *FlatSQLStore) localReplicaRawStats(stat LocalReplicaStats, query LocalReplicaStatsQuery) (int64, int64, error) {
	filter := RawRecordQuery{
		SchemaName: stat.SchemaName,
		ProviderID: stat.ProviderID,
		SourceName: stat.SourceName,
		BatchID:    stat.BatchID,
	}
	sourceIdentityPresent := filter.ProviderID != "" || filter.SourceName != "" || filter.BatchID != ""
	if query.ProviderPeerID != "" || !sourceIdentityPresent {
		filter.ProducerPeerID = stat.ProviderPeerID
	}
	if query.ProviderPublicKey != "" || !sourceIdentityPresent {
		filter.ProducerPublicKey = stat.ProviderPublicKey
	}
	localRows, err := s.CountRawRecords(filter)
	if err != nil {
		return 0, 0, err
	}
	head, err := s.RawRecordHead(filter)
	if err != nil {
		return 0, 0, err
	}
	return localRows, head.TotalBytes, nil
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Unix()
}

func timeOrZero(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
