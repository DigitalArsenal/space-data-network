package storage

import (
	"fmt"
	"strings"
)

// FullTablePageQuery selects one bounded dashboard table page from the durable
// record catalog. The FlatSQL engine relation is intentionally not involved:
// it is a resident hot window, while this lane must reach every stored frame.
type FullTablePageQuery struct {
	SchemaName string
	SourceName string
	Limit      int
	Offset     int
	// BeforeRowID is an exclusive cursor over the durable global record index.
	// It lets callers walk a large selection in bounded lock windows without
	// OFFSET drift when a newer record is stored between chunks.
	BeforeRowID int64
	Sort        string
	Descending  bool
}

// FullTableColumns returns the compiled SDS projection followed by the engine
// metadata columns. It is derived from the same catalog used to declare the
// engine relation, so discovering columns never waits for engine hydration.
func (s *FlatSQLStore) FullTableColumns(schemaName string) ([]string, error) {
	code := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(schemaName, ".fbs")))
	columns, ok := engineRelationColumns(code)
	if !ok {
		return nil, fmt.Errorf("unknown standard %s", schemaName)
	}
	return append([]string(nil), columns...), nil
}

// FullTablePage reads exactly one page of metadata and record frames while
// holding the store read lock. Callers obtain counts separately, so a request
// never holds the lock across both the count and page operations.
//
// Global ordering is supported for durable metadata columns. Other columns
// are projected from the FlatBuffer and may be sorted by the API within this
// page after this method returns.
func (s *FlatSQLStore) FullTablePage(query FullTablePageQuery) ([]*Record, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store is unavailable")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	schemaName := strings.TrimSpace(query.SchemaName)
	if schemaName == "" {
		return nil, fmt.Errorf("schema name is required")
	}
	readSource, err := s.rawRecordReadSource(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}

	sourceName := strings.TrimSpace(query.SourceName)
	args := make([]interface{}, 0, 8)
	sourceProjection := `COALESCE((
		SELECT tags.source_name
		FROM sdn_record_source_tags tags INDEXED BY idx_sdn_record_source_tags_unique
		WHERE tags.schema_name = ? AND tags.cid = records.cid`
	args = append(args, schemaName)
	if sourceName != "" {
		sourceProjection += ` AND tags.source_name = ?`
		args = append(args, sourceName)
	}
	sourceProjection += ` ORDER BY tags.created_at DESC, tags.rowid DESC LIMIT 1
	), '')`

	statement := fmt.Sprintf(`
		WITH candidates AS (
			SELECT records.rowid, records.cid, records.peer_id, records.timestamp,
			       records.stream_path, records.stream_offset, records.record_length,
			       records.signature_hex, %s AS source_name
			FROM %s records
			WHERE 1 = 1
	`, sourceProjection, readSource)
	if sourceName != "" {
		statement += ` AND EXISTS (
			SELECT 1
			FROM sdn_record_source_tags filter_tags INDEXED BY idx_sdn_record_source_tags_source_cid
			WHERE filter_tags.schema_name = ? AND filter_tags.source_name = ?
			  AND filter_tags.cid = records.cid
		)`
		args = append(args, schemaName, sourceName)
	}
	if query.BeforeRowID > 0 {
		statement += ` AND records.rowid < ?`
		args = append(args, query.BeforeRowID)
	}

	innerOrder, outerOrder := "records.rowid", "candidates.rowid"
	switch strings.ToLower(strings.TrimSpace(query.Sort)) {
	case "_source":
		innerOrder, outerOrder = "source_name", "candidates.source_name"
	case "timestamp":
		innerOrder, outerOrder = "records.timestamp", "candidates.timestamp"
	case "_offset":
		innerOrder, outerOrder = "records.stream_offset", "candidates.stream_offset"
	case "_rowid", "":
		// The default is the global durable record-index rowid.
	}
	direction := "ASC"
	if query.Descending || strings.TrimSpace(query.Sort) == "" {
		direction = "DESC"
	}
	statement += fmt.Sprintf(`
			ORDER BY %s %s, records.rowid DESC, records.cid ASC
			LIMIT ? OFFSET ?
		)
		SELECT candidates.rowid, candidates.cid, candidates.peer_id, candidates.timestamp,
		       candidates.stream_path, candidates.stream_offset, candidates.record_length,
		       candidates.signature_hex,
		       '', candidates.source_name, '', '', '', '', '', NULL
		FROM candidates
		ORDER BY %s %s, candidates.rowid DESC, candidates.cid ASC
	`, innerOrder, direction, outerOrder, direction)
	args = append(args, limit, offset)

	rows, err := s.db.Query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("full table page query failed: %w", err)
	}
	records, err := s.scanRawRecordRows(rows, true)
	if err != nil {
		return nil, err
	}
	return records, nil
}
