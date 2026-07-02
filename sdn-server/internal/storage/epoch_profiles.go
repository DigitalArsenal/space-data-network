package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

)

const (
	EpochProfileDay      = "epoch.day"
	EpochProfileWindow   = "epoch.window"
	EpochProfileAsOf     = "epoch.as_of"
	EpochProfileForward  = "epoch.forward"
	EpochProfileNearest  = "epoch.nearest"
	EpochProfileCoverage = "epoch.coverage"

	EpochMatchExact       = "exact"
	EpochMatchBackfill    = "backfill"
	EpochMatchForwardFill = "forwardfill"
	EpochMatchNearest     = "nearest"
	EpochMatchMissing     = "missing"
)

// EpochProfile describes the SDN temporal query policy for an SDS schema.
type EpochProfile struct {
	SchemaName     string
	EntityKeyField string
	EpochField     string
	EpochUnixField string
	EpochDayField  string
	DisplayFields  []string
}

// EpochRecordQuery selects a temporal profile over the SDN materialized record index.
type EpochRecordQuery struct {
	SchemaName      string
	Profile         string
	Day             string
	At              time.Time
	From            *time.Time
	To              *time.Time
	ProviderID      string
	SourceName      string
	BatchID         string
	NoradCatID      *uint32
	EntityID        string
	Limit           int
	IncludeSource   bool
	MaxDeltaSeconds int64
}

// EpochRecordMatch is one record plus temporal match-quality metadata.
type EpochRecordMatch struct {
	Record         *Record
	EntityKey      string
	RequestedEpoch time.Time
	MatchedEpoch   time.Time
	DeltaSeconds   int64
	MatchType      string
}

// EpochCoverageBucket summarizes indexed temporal coverage by UTC day.
type EpochCoverageBucket struct {
	Day         string
	Count       int64
	OldestEpoch time.Time
	NewestEpoch time.Time
}

// EpochProfileForSchema returns the basic temporal profile for schemas that
// have a stable entity key and indexed epoch.
func EpochProfileForSchema(schemaName string) (EpochProfile, bool) {
	switch normalizeSchemaNameForEpoch(schemaName) {
	case "OMM.fbs":
		return EpochProfile{
			SchemaName:     "OMM.fbs",
			EntityKeyField: "NORAD_CAT_ID",
			EpochField:     "EPOCH",
			EpochUnixField: "epoch_unix",
			EpochDayField:  "epoch_day",
			DisplayFields:  []string{"OBJECT_ID", "OBJECT_NAME"},
		}, true
	case "MPE.fbs":
		return EpochProfile{
			SchemaName:     "MPE.fbs",
			EntityKeyField: "ENTITY_ID",
			EpochField:     "EPOCH",
			EpochUnixField: "epoch_unix",
			EpochDayField:  "epoch_day",
			DisplayFields:  []string{"ENTITY_ID"},
		}, true
	default:
		return EpochProfile{}, false
	}
}

// QueryEpochRecords executes an SDN temporal profile using indexed FlatBuffer
// metadata and hydrates only the matching records.
func (s *FlatSQLStore) QueryEpochRecords(query EpochRecordQuery) ([]EpochRecordMatch, error) {
	query = normalizeEpochRecordQuery(query)
	if _, ok := EpochProfileForSchema(query.SchemaName); !ok {
		return nil, fmt.Errorf("no epoch profile registered for %s", query.SchemaName)
	}
	switch query.Profile {
	case EpochProfileDay:
		if query.Day == "" {
			return nil, errors.New("epoch.day requires day")
		}
		records, err := s.QueryIndexedRecords(indexedRecordQueryForEpoch(query))
		if err != nil {
			return nil, err
		}
		day, _ := time.Parse("2006-01-02", query.Day)
		return epochMatchesFromRecords(query, day, records, EpochMatchExact)
	case EpochProfileWindow:
		if query.From == nil || query.To == nil {
			return nil, errors.New("epoch.window requires from and to")
		}
		records, err := s.queryEpochIndexedRecords(query)
		if err != nil {
			return nil, err
		}
		return epochMatchesFromRecords(query, *query.From, records, EpochMatchExact)
	case EpochProfileAsOf, EpochProfileForward, EpochProfileNearest:
		if query.At.IsZero() {
			return nil, fmt.Errorf("%s requires at", query.Profile)
		}
		return s.queryPointEpochRecords(query)
	case EpochProfileCoverage:
		return nil, errors.New("use QueryEpochCoverage for epoch.coverage")
	default:
		return nil, fmt.Errorf("unsupported epoch profile %q", query.Profile)
	}
}

// QueryEpochCoverage returns per-day temporal coverage for one schema/source.
func (s *FlatSQLStore) QueryEpochCoverage(query EpochRecordQuery) ([]EpochCoverageBucket, error) {
	query = normalizeEpochRecordQuery(query)
	if _, ok := EpochProfileForSchema(query.SchemaName); !ok {
		return nil, fmt.Errorf("no epoch profile registered for %s", query.SchemaName)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	sqlText := `
		SELECT idx.epoch_day, COUNT(*), MIN(idx.epoch_unix), MAX(idx.epoch_unix)
		FROM sdn_record_index idx
	`
	args := []interface{}{}
	if epochSourceFiltered(query) {
		sqlText += `
		INNER JOIN sdn_record_source_tags tags
		  ON tags.schema_name = idx.schema_name AND tags.cid = idx.cid
		`
	}
	sqlText += `
		WHERE idx.schema_name = ?
		  AND idx.epoch_day IS NOT NULL
		  AND idx.epoch_day != ''
		  AND idx.epoch_unix IS NOT NULL
	`
	args = append(args, query.SchemaName)
	sqlText, args = appendEpochFilters(sqlText, args, query, epochSourceFiltered(query))
	sqlText += ` GROUP BY idx.epoch_day ORDER BY idx.epoch_day ASC`

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("epoch coverage query failed: %w", err)
	}
	defer rows.Close()

	var coverage []EpochCoverageBucket
	for rows.Next() {
		var bucket EpochCoverageBucket
		var minEpoch, maxEpoch sql.NullInt64
		if err := rows.Scan(&bucket.Day, &bucket.Count, &minEpoch, &maxEpoch); err != nil {
			return nil, fmt.Errorf("scan epoch coverage: %w", err)
		}
		if minEpoch.Valid {
			bucket.OldestEpoch = time.Unix(minEpoch.Int64, 0).UTC()
		}
		if maxEpoch.Valid {
			bucket.NewestEpoch = time.Unix(maxEpoch.Int64, 0).UTC()
		}
		coverage = append(coverage, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("epoch coverage rows: %w", err)
	}
	return coverage, nil
}

// CountEpochRecords returns the total number of records or entity matches for
// an epoch profile without hydrating FlatBuffer payloads from stream files.
func (s *FlatSQLStore) CountEpochRecords(query EpochRecordQuery) (int64, error) {
	query = normalizeEpochRecordQuery(query)
	if _, ok := EpochProfileForSchema(query.SchemaName); !ok {
		return 0, fmt.Errorf("no epoch profile registered for %s", query.SchemaName)
	}

	switch query.Profile {
	case EpochProfileDay:
		if query.Day == "" {
			return 0, errors.New("epoch.day requires day")
		}
		if _, err := time.Parse("2006-01-02", query.Day); err != nil {
			return 0, fmt.Errorf("invalid day %q (expected YYYY-MM-DD)", query.Day)
		}
		return s.countEpochIndexedRows(query)
	case EpochProfileWindow:
		if query.From == nil || query.To == nil {
			return 0, errors.New("epoch.window requires from and to")
		}
		if !query.From.Before(*query.To) {
			return 0, errors.New("from time must be before to time")
		}
		return s.countEpochIndexedRows(query)
	case EpochProfileAsOf, EpochProfileForward, EpochProfileNearest:
		if query.At.IsZero() {
			return 0, fmt.Errorf("%s requires at", query.Profile)
		}
		return s.countPointEpochEntities(query)
	case EpochProfileCoverage:
		coverage, err := s.QueryEpochCoverage(query)
		if err != nil {
			return 0, err
		}
		return int64(len(coverage)), nil
	default:
		return 0, fmt.Errorf("unsupported epoch profile %q", query.Profile)
	}
}

func (s *FlatSQLStore) countEpochIndexedRows(query EpochRecordQuery) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sourceFiltered := epochSourceFiltered(query)
	sqlText := `
		SELECT COUNT(*)
		FROM sdn_record_index idx
	`
	args := []interface{}{query.SchemaName}
	if sourceFiltered {
		sqlText += epochSourceJoinSQL(true)
	}
	sqlText += `
		WHERE idx.schema_name = ?
		  AND idx.epoch_unix IS NOT NULL
	`
	sqlText, args = appendEpochFilters(sqlText, args, query, sourceFiltered)

	var total int64
	if err := s.db.QueryRow(sqlText, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("epoch indexed count failed: %w", err)
	}
	return total, nil
}

func (s *FlatSQLStore) countPointEpochEntities(query EpochRecordQuery) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sourceFiltered := epochSourceFiltered(query)
	entityExpr := epochEntityKeySQL(query.SchemaName)
	targetUnix := query.At.UTC().Unix()
	candidateWhere := `
		WHERE idx.schema_name = ?
		  AND idx.epoch_unix IS NOT NULL
	`
	args := []interface{}{query.SchemaName}
	switch query.Profile {
	case EpochProfileAsOf:
		candidateWhere += ` AND idx.epoch_unix <= ?`
		args = append(args, targetUnix)
	case EpochProfileForward:
		candidateWhere += ` AND idx.epoch_unix >= ?`
		args = append(args, targetUnix)
	case EpochProfileNearest:
	default:
		return 0, fmt.Errorf("unsupported point epoch profile %q", query.Profile)
	}
	candidateWhere, args = appendEpochFilters(candidateWhere, args, query, sourceFiltered)

	rankOrder := "matched_epoch_unix DESC, cid ASC"
	switch query.Profile {
	case EpochProfileForward:
		rankOrder = "matched_epoch_unix ASC, cid ASC"
	case EpochProfileNearest:
		rankOrder = "ABS(matched_epoch_unix - ?) ASC, CASE WHEN matched_epoch_unix <= ? THEN 0 ELSE 1 END ASC, matched_epoch_unix DESC, cid ASC"
		args = append(args, targetUnix, targetUnix)
	}

	maxDeltaFilter := ""
	if query.MaxDeltaSeconds > 0 {
		maxDeltaFilter = ` AND ABS(matched_epoch_unix - ?) <= ?`
		args = append(args, targetUnix, query.MaxDeltaSeconds)
	}

	sqlText := fmt.Sprintf(`
		WITH candidates AS (
			SELECT idx.cid,
			       %s AS entity_key,
			       idx.epoch_unix AS matched_epoch_unix
			FROM sdn_record_index idx
			%s
			%s
		),
		ranked AS (
			SELECT *,
			       ROW_NUMBER() OVER (
			         PARTITION BY entity_key
			         ORDER BY %s
			       ) AS rn
			FROM candidates
		)
		SELECT COUNT(*)
		FROM ranked
		WHERE rn = 1
		  AND entity_key IS NOT NULL
		  AND entity_key != ''
		  %s
	`, entityExpr, epochSourceJoinSQL(sourceFiltered), candidateWhere, rankOrder, maxDeltaFilter)

	var total int64
	if err := s.db.QueryRow(sqlText, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("epoch point count failed: %w", err)
	}
	return total, nil
}

func (s *FlatSQLStore) queryEpochIndexedRecords(query EpochRecordQuery) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := s.recordReadSource(query.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	sourceFiltered := epochSourceFiltered(query)
	sqlText := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp,
		       d.stream_path, d.stream_offset, d.record_length, d.signature_hex
		FROM %s d
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = d.cid
		%s
		WHERE idx.epoch_unix IS NOT NULL
	`, tableName, epochSourceJoinSQL(sourceFiltered))
	args := []interface{}{query.SchemaName}
	sqlText, args = appendEpochFilters(sqlText, args, query, sourceFiltered)
	sqlText += ` ORDER BY idx.epoch_unix ASC, d.cid ASC LIMIT ?`
	args = append(args, epochQueryLimit(query))

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("epoch indexed query failed: %w", err)
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
			return nil, fmt.Errorf("scan epoch indexed row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		if err := s.hydrateRecordData(rec, streamPath, streamOffset, recordLength, signatureHex); err != nil {
			return nil, fmt.Errorf("hydrate epoch indexed record: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("epoch indexed rows: %w", err)
	}
	return records, nil
}

func (s *FlatSQLStore) queryPointEpochRecords(query EpochRecordQuery) ([]EpochRecordMatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := s.recordReadSource(query.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	sourceFiltered := epochSourceFiltered(query)
	entityExpr := epochEntityKeySQL(query.SchemaName)
	targetUnix := query.At.UTC().Unix()
	candidateWhere := `
		WHERE idx.epoch_unix IS NOT NULL
	`
	args := []interface{}{query.SchemaName}
	switch query.Profile {
	case EpochProfileAsOf:
		candidateWhere += ` AND idx.epoch_unix <= ?`
		args = append(args, targetUnix)
	case EpochProfileForward:
		candidateWhere += ` AND idx.epoch_unix >= ?`
		args = append(args, targetUnix)
	case EpochProfileNearest:
	default:
		return nil, fmt.Errorf("unsupported point epoch profile %q", query.Profile)
	}
	candidateWhere, args = appendEpochFilters(candidateWhere, args, query, sourceFiltered)

	rankOrder := "matched_epoch_unix DESC, cid ASC"
	switch query.Profile {
	case EpochProfileForward:
		rankOrder = "matched_epoch_unix ASC, cid ASC"
	case EpochProfileNearest:
		rankOrder = "ABS(matched_epoch_unix - ?) ASC, CASE WHEN matched_epoch_unix <= ? THEN 0 ELSE 1 END ASC, matched_epoch_unix DESC, cid ASC"
		args = append(args, targetUnix, targetUnix)
	}

	sqlText := fmt.Sprintf(`
		WITH candidates AS (
			SELECT d.cid, d.peer_id, d.timestamp,
			       d.stream_path, d.stream_offset, d.record_length, d.signature_hex,
			       %s AS entity_key,
			       idx.epoch_unix AS matched_epoch_unix
			FROM %s d
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = ? AND idx.cid = d.cid
			%s
			%s
		),
		ranked AS (
			SELECT *,
			       ROW_NUMBER() OVER (
			         PARTITION BY entity_key
			         ORDER BY %s
			       ) AS rn
			FROM candidates
		)
		SELECT cid, peer_id, timestamp, stream_path, stream_offset, record_length,
		       signature_hex, entity_key, matched_epoch_unix
		FROM ranked
		WHERE rn = 1
		ORDER BY entity_key ASC
		LIMIT ?
	`, entityExpr, tableName, epochSourceJoinSQL(sourceFiltered), candidateWhere, rankOrder)
	args = append(args, epochQueryLimit(query))

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("epoch point query failed: %w", err)
	}
	defer rows.Close()

	var matches []EpochRecordMatch
	for rows.Next() {
		match, err := s.scanEpochRecordMatch(rows, query)
		if err != nil {
			return nil, err
		}
		if query.MaxDeltaSeconds > 0 && match.DeltaSeconds > query.MaxDeltaSeconds {
			continue
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("epoch point rows: %w", err)
	}
	return matches, nil
}

func (s *FlatSQLStore) scanEpochRecordMatch(scanner interface {
	Scan(dest ...interface{}) error
}, query EpochRecordQuery) (EpochRecordMatch, error) {
	rec := &Record{}
	var ts int64
	var streamPath string
	var streamOffset, recordLength int64
	var signatureHex sql.NullString
	var entityKey string
	var matchedUnix int64
	if err := scanner.Scan(&rec.CID, &rec.PeerID, &ts, &streamPath, &streamOffset, &recordLength, &signatureHex, &entityKey, &matchedUnix); err != nil {
		return EpochRecordMatch{}, fmt.Errorf("scan epoch record: %w", err)
	}
	rec.Timestamp = time.Unix(ts, 0).UTC()
	if err := s.hydrateRecordData(rec, streamPath, streamOffset, recordLength, signatureHex); err != nil {
		return EpochRecordMatch{}, fmt.Errorf("hydrate epoch record: %w", err)
	}
	matched := time.Unix(matchedUnix, 0).UTC()
	return EpochRecordMatch{
		Record:         rec,
		EntityKey:      entityKey,
		RequestedEpoch: query.At.UTC(),
		MatchedEpoch:   matched,
		DeltaSeconds:   absInt64(matchedUnix - query.At.UTC().Unix()),
		MatchType:      epochMatchType(query.Profile, query.At.UTC(), matched),
	}, nil
}

func epochMatchesFromRecords(query EpochRecordQuery, requested time.Time, records []*Record, defaultMatchType string) ([]EpochRecordMatch, error) {
	matches := make([]EpochRecordMatch, 0, len(records))
	for _, record := range records {
		fields, err := extractIndexedFields(query.SchemaName, record.Data)
		if err != nil {
			return nil, err
		}
		if fields.epochUnix == nil {
			continue
		}
		entityKey := fields.entityID
		if fields.noradCatID != nil {
			entityKey = fmt.Sprintf("%d", *fields.noradCatID)
		}
		matched := time.Unix(*fields.epochUnix, 0).UTC()
		matches = append(matches, EpochRecordMatch{
			Record:         record,
			EntityKey:      entityKey,
			RequestedEpoch: requested.UTC(),
			MatchedEpoch:   matched,
			DeltaSeconds:   absInt64(*fields.epochUnix - requested.UTC().Unix()),
			MatchType:      defaultMatchType,
		})
	}
	return matches, nil
}

func indexedRecordQueryForEpoch(query EpochRecordQuery) IndexedRecordQuery {
	return IndexedRecordQuery{
		SchemaName:          query.SchemaName,
		Day:                 query.Day,
		From:                query.From,
		To:                  query.To,
		ProviderID:          query.ProviderID,
		SourceName:          query.SourceName,
		BatchID:             query.BatchID,
		NoradCatID:          query.NoradCatID,
		EntityID:            query.EntityID,
		Limit:               epochQueryLimit(query),
		AllowLargeResultSet: true,
	}
}

func normalizeEpochRecordQuery(query EpochRecordQuery) EpochRecordQuery {
	query.SchemaName = normalizeSchemaNameForEpoch(query.SchemaName)
	query.Profile = strings.TrimSpace(query.Profile)
	if query.Profile == "" {
		query.Profile = EpochProfileDay
	}
	query.Day = strings.TrimSpace(query.Day)
	query.ProviderID = strings.TrimSpace(query.ProviderID)
	query.SourceName = strings.TrimSpace(query.SourceName)
	query.BatchID = strings.TrimSpace(query.BatchID)
	query.EntityID = strings.TrimSpace(query.EntityID)
	return query
}

func normalizeSchemaNameForEpoch(schemaName string) string {
	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" || strings.HasSuffix(schemaName, ".fbs") {
		return schemaName
	}
	return schemaName + ".fbs"
}

func epochQueryLimit(query EpochRecordQuery) int {
	if query.Limit <= 0 {
		return 1000
	}
	if query.Limit > 250000 {
		return 250000
	}
	return query.Limit
}

func epochSourceFiltered(query EpochRecordQuery) bool {
	return query.ProviderID != "" || query.SourceName != "" || query.BatchID != ""
}

func epochSourceJoinSQL(enabled bool) string {
	if !enabled {
		return ""
	}
	return `
			INNER JOIN sdn_record_source_tags tags
			  ON tags.schema_name = idx.schema_name AND tags.cid = idx.cid
	`
}

func appendEpochFilters(sqlText string, args []interface{}, query EpochRecordQuery, sourceFiltered bool) (string, []interface{}) {
	if query.Day != "" {
		sqlText += ` AND idx.epoch_day = ?`
		args = append(args, query.Day)
	}
	if query.From != nil {
		sqlText += ` AND idx.epoch_unix >= ?`
		args = append(args, query.From.UTC().Unix())
	}
	if query.To != nil {
		sqlText += ` AND idx.epoch_unix < ?`
		args = append(args, query.To.UTC().Unix())
	}
	if query.NoradCatID != nil {
		sqlText += ` AND idx.norad_cat_id = ?`
		args = append(args, int64(*query.NoradCatID))
	}
	if query.EntityID != "" {
		sqlText += ` AND idx.entity_id = ?`
		args = append(args, query.EntityID)
	}
	if sourceFiltered {
		if query.ProviderID != "" {
			sqlText += ` AND tags.provider_id = ?`
			args = append(args, query.ProviderID)
		}
		if query.SourceName != "" {
			sqlText += ` AND tags.source_name = ?`
			args = append(args, query.SourceName)
		}
		if query.BatchID != "" {
			sqlText += ` AND tags.batch_id = ?`
			args = append(args, query.BatchID)
		}
	}
	return sqlText, args
}

func epochEntityKeySQL(schemaName string) string {
	switch schemaName {
	case "OMM.fbs":
		return "COALESCE(CASE WHEN idx.norad_cat_id IS NOT NULL THEN CAST(idx.norad_cat_id AS TEXT) END, NULLIF(idx.entity_id, ''), idx.cid)"
	default:
		return "COALESCE(NULLIF(idx.entity_id, ''), CASE WHEN idx.norad_cat_id IS NOT NULL THEN CAST(idx.norad_cat_id AS TEXT) END, idx.cid)"
	}
}

func epochMatchType(profile string, requested, matched time.Time) string {
	requestedUnix := requested.UTC().Unix()
	matchedUnix := matched.UTC().Unix()
	if requestedUnix == matchedUnix {
		return EpochMatchExact
	}
	switch profile {
	case EpochProfileAsOf:
		return EpochMatchBackfill
	case EpochProfileForward:
		return EpochMatchForwardFill
	case EpochProfileNearest:
		return EpochMatchNearest
	default:
		if matchedUnix < requestedUnix {
			return EpochMatchBackfill
		}
		return EpochMatchForwardFill
	}
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
