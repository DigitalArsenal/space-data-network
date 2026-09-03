package storage

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const fullTablePageMaxChunk = 2000

// FullTablePageCursor is the exclusive rowid cursor for each routed table.
// A map is necessary because rowids are local to producer tables. The API
// serializes it as an opaque token for clients; scan callers keep it in memory.
type FullTablePageCursor map[string]int64

// FullTablePageQuery selects one bounded dashboard table page from the durable
// record catalog. The FlatSQL engine relation is intentionally not involved:
// it is a resident hot window, while this lane must reach every stored frame.
type FullTablePageQuery struct {
	SchemaName string
	SourceName string
	Limit      int
	Offset     int
	// BeforeRowID is the legacy single-table cursor. New callers use Cursor so
	// every producer table advances independently.
	BeforeRowID int64
	Cursor      FullTablePageCursor
	// IncludeSource requests the optional batched source-name lookup. A source
	// filter already knows its source name and therefore needs no second query.
	IncludeSource   bool
	KnownSourceName string
	Sort            string
	Descending      bool

	// metadataOnly is used while translating a multi-table OFFSET fallback into
	// bounded cursor chunks. It avoids reading record frames that will be dropped.
	metadataOnly bool
}

// FullTablePageResult carries the next per-table rowid cursor with a page.
type FullTablePageResult struct {
	Records    []*Record
	NextCursor FullTablePageCursor
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

// FullTablePage preserves the original record-slice API for storage callers.
// Cursor-aware callers use FullTablePageWithCursor.
func (s *FlatSQLStore) FullTablePage(query FullTablePageQuery) ([]*Record, error) {
	page, err := s.FullTablePageWithCursor(query)
	if err != nil {
		return nil, err
	}
	return page.Records, nil
}

// FullTablePageWithCursor reads one bounded page while holding the store read
// lock for that page only. The candidates query touches only routed metadata;
// record bytes are hydrated after the per-table candidates have been merged.
func (s *FlatSQLStore) FullTablePageWithCursor(query FullTablePageQuery) (FullTablePageResult, error) {
	if s == nil || s.db == nil {
		return FullTablePageResult{}, fmt.Errorf("store is unavailable")
	}
	query.SchemaName = strings.TrimSpace(query.SchemaName)
	if query.SchemaName == "" {
		return FullTablePageResult{}, fmt.Errorf("schema name is required")
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > fullTablePageMaxChunk {
		query.Limit = fullTablePageMaxChunk
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	// OFFSET remains a compatibility fallback for a client that cannot supply
	// a cursor. It is safe as a single routed-table rowid walk. A multi-table
	// selection is instead advanced in bounded cursor chunks, releasing the
	// read lock after each one; no UNION/GROUP BY relation is paged with OFFSET.
	if query.Offset > 0 && len(query.Cursor) == 0 && query.BeforeRowID == 0 {
		tableCount, err := s.fullTableBackingTableCount(query.SchemaName)
		if err != nil {
			return FullTablePageResult{}, err
		}
		if tableCount > 1 {
			return s.fullTablePageAfterOffset(query)
		}
	}
	return s.fullTablePageChunk(query)
}

func (s *FlatSQLStore) fullTableBackingTableCount(schemaName string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tables, err := s.fullTableReadTablesLocked(schemaName)
	return len(tables), err
}

func (s *FlatSQLStore) fullTablePageAfterOffset(query FullTablePageQuery) (FullTablePageResult, error) {
	remaining := query.Offset
	var cursor FullTablePageCursor
	for remaining > 0 {
		limit := remaining
		if limit > fullTablePageMaxChunk {
			limit = fullTablePageMaxChunk
		}
		discard := query
		discard.Offset = 0
		discard.Limit = limit
		discard.Cursor = cursor
		discard.IncludeSource = false
		discard.KnownSourceName = ""
		discard.metadataOnly = true
		page, err := s.fullTablePageChunk(discard)
		if err != nil {
			return FullTablePageResult{}, err
		}
		if len(page.Records) == 0 {
			return FullTablePageResult{Records: []*Record{}, NextCursor: page.NextCursor}, nil
		}
		remaining -= len(page.Records)
		cursor = page.NextCursor
		if len(page.Records) < limit && remaining > 0 {
			return FullTablePageResult{Records: []*Record{}, NextCursor: cursor}, nil
		}
	}
	query.Offset = 0
	query.Cursor = cursor
	return s.fullTablePageChunk(query)
}

type fullTableCandidate struct {
	table       string
	routedRowID int64
	record      *Record
	signature   sql.NullString
}

func (s *FlatSQLStore) fullTablePageChunk(query FullTablePageQuery) (FullTablePageResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tables, err := s.fullTableReadTablesLocked(query.SchemaName)
	if err != nil {
		return FullTablePageResult{}, fmt.Errorf("invalid schema name: %w", err)
	}
	if len(tables) == 0 {
		return FullTablePageResult{Records: []*Record{}, NextCursor: cloneFullTableCursor(query.Cursor)}, nil
	}

	all := make([]fullTableCandidate, 0, query.Limit*len(tables))
	byTable := make(map[string][]fullTableCandidate, len(tables))
	for _, table := range tables {
		before := int64(math.MaxInt64)
		if query.BeforeRowID > 0 {
			before = query.BeforeRowID
		}
		if cursor, ok := query.Cursor[table]; ok && cursor > 0 {
			before = cursor
		}
		statement, args := fullTableCandidatesSQL(
			table, query.SchemaName, query.SourceName, before, query.Limit, query.Offset,
		)
		rows, err := s.db.Query(statement, args...)
		if err != nil {
			return FullTablePageResult{}, fmt.Errorf("full table candidates query failed: %w", err)
		}
		candidates, err := scanFullTableCandidateRows(rows, table)
		if err != nil {
			return FullTablePageResult{}, err
		}
		byTable[table] = candidates
		all = append(all, candidates...)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].record.RowID != all[j].record.RowID {
			return all[i].record.RowID > all[j].record.RowID
		}
		if all[i].record.CID != all[j].record.CID {
			return all[i].record.CID < all[j].record.CID
		}
		return all[i].table < all[j].table
	})
	selected := make([]fullTableCandidate, 0, query.Limit)
	seen := make(map[string]struct{}, query.Limit)
	for _, candidate := range all {
		if _, duplicate := seen[candidate.record.CID]; duplicate {
			continue
		}
		seen[candidate.record.CID] = struct{}{}
		selected = append(selected, candidate)
		if len(selected) == query.Limit {
			break
		}
	}

	next := cloneFullTableCursor(query.Cursor)
	if len(selected) > 0 {
		boundary := selected[len(selected)-1].record.RowID
		for table, candidates := range byTable {
			for _, candidate := range candidates {
				if candidate.record.RowID < boundary {
					break
				}
				next[table] = candidate.routedRowID
			}
		}
	}

	records := make([]*Record, 0, len(selected))
	for i := range selected {
		candidate := &selected[i]
		if !query.metadataOnly {
			if err := s.hydrateRecordData(
				candidate.record,
				candidate.record.StreamPath,
				candidate.record.StreamOffset,
				candidate.record.RecordLength,
				candidate.signature,
			); err != nil {
				return FullTablePageResult{}, fmt.Errorf("failed reading full table record data: %w", err)
			}
		}
		if query.SourceName != "" {
			candidate.record.SourceTags.SourceName = strings.TrimSpace(query.SourceName)
		} else if query.KnownSourceName != "" {
			candidate.record.SourceTags.SourceName = strings.TrimSpace(query.KnownSourceName)
		}
		records = append(records, candidate.record)
	}
	if query.IncludeSource && strings.TrimSpace(query.SourceName) == "" && strings.TrimSpace(query.KnownSourceName) == "" {
		if err := s.resolveFullTableSourceNamesLocked(query.SchemaName, records); err != nil {
			return FullTablePageResult{}, err
		}
	}
	return FullTablePageResult{Records: records, NextCursor: next}, nil
}

// fullTableCandidatesSQL pages one routed table. records.rowid < ? is present
// even on the first page, so the scan and client-cursor lanes always use an
// INTEGER PRIMARY KEY seek. OFFSET is retained only for the single-table
// compatibility fallback.
func fullTableCandidatesSQL(table, schemaName, sourceName string, beforeRowID int64, limit, offset int) (string, []any) {
	args := make([]any, 0, 7)
	join := ""
	group := ""
	if sourceName = strings.TrimSpace(sourceName); sourceName != "" {
		join = `
			CROSS JOIN sdn_record_source_tags filter_tags INDEXED BY idx_sdn_record_source_tags_unique
			  ON filter_tags.schema_name = ? AND filter_tags.cid = records.cid
			 AND filter_tags.source_name = ?`
		args = append(args, schemaName, sourceName)
		// A CID may have several provenance rows for one source. They are
		// adjacent while records drives the join, so grouping on its rowid keeps
		// one candidate without a per-row EXISTS/subquery.
		group = " GROUP BY records.rowid"
	}
	paging := "LIMIT ?"
	args = append(args, beforeRowID, limit)
	if offset > 0 {
		paging += " OFFSET ?"
		args = append(args, offset)
	}
	args = append(args, schemaName)
	statement := fmt.Sprintf(`
		WITH candidates AS (
			SELECT records.rowid AS routed_rowid, records.cid, records.peer_id,
			       records.timestamp, records.stream_path, records.stream_offset,
			       records.record_length, records.signature_hex
			FROM %s records%s
			WHERE records.rowid < ?%s
			ORDER BY records.rowid DESC
			%s
		)
		SELECT candidates.routed_rowid, idx.rowid, candidates.cid, candidates.peer_id,
		       candidates.timestamp, candidates.stream_path, candidates.stream_offset,
		       candidates.record_length, candidates.signature_hex
		FROM candidates
		CROSS JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = candidates.cid
	`, table, join, group, paging)
	return statement, args
}

func scanFullTableCandidateRows(rows *sql.Rows, table string) ([]fullTableCandidate, error) {
	defer rows.Close()
	candidates := make([]fullTableCandidate, 0)
	for rows.Next() {
		record := &Record{}
		candidate := fullTableCandidate{table: table, record: record}
		var timestamp int64
		if err := rows.Scan(
			&candidate.routedRowID,
			&record.RowID,
			&record.CID,
			&record.PeerID,
			&timestamp,
			&record.StreamPath,
			&record.StreamOffset,
			&record.RecordLength,
			&candidate.signature,
		); err != nil {
			return nil, fmt.Errorf("failed scanning full table candidate: %w", err)
		}
		record.Timestamp = time.Unix(timestamp, 0).UTC()
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("full table candidates rows failed: %w", err)
	}
	return candidates, nil
}

// resolveFullTableSourceNamesLocked resolves a whole returned chunk with one
// query. Rows are newest-first across provenance updates; first assignment for
// a CID therefore wins.
func (s *FlatSQLStore) resolveFullTableSourceNamesLocked(schemaName string, records []*Record) error {
	if len(records) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(records))
	args := make([]any, 0, len(records)+1)
	args = append(args, schemaName)
	seenCID := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, seen := seenCID[record.CID]; seen {
			continue
		}
		seenCID[record.CID] = struct{}{}
		placeholders = append(placeholders, "?")
		args = append(args, record.CID)
	}
	statement := fmt.Sprintf(`
		SELECT cid, source_name
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid IN (%s)
		ORDER BY created_at DESC, rowid DESC
	`, strings.Join(placeholders, ","))
	rows, err := s.db.Query(statement, args...)
	if err != nil {
		return fmt.Errorf("full table source-name query failed: %w", err)
	}
	defer rows.Close()
	names := make(map[string]string, len(records))
	for rows.Next() {
		var cid, sourceName string
		if err := rows.Scan(&cid, &sourceName); err != nil {
			return fmt.Errorf("failed scanning full table source name: %w", err)
		}
		if _, exists := names[cid]; !exists {
			names[cid] = sourceName
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("full table source-name rows failed: %w", err)
	}
	for _, record := range records {
		record.SourceTags.SourceName = names[record.CID]
	}
	return nil
}

func cloneFullTableCursor(cursor FullTablePageCursor) FullTablePageCursor {
	out := make(FullTablePageCursor, len(cursor))
	for table, rowID := range cursor {
		if rowID > 0 {
			out[table] = rowID
		}
	}
	return out
}

func (s *FlatSQLStore) fullTableReadTablesLocked(schemaName string) ([]string, error) {
	legacy, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, 2)
	if exists, err := s.tableExists(legacy); err != nil {
		return nil, err
	} else if exists {
		tables = append(tables, legacy)
	}
	producerTables, err := s.listProducerStandardTables()
	if err != nil {
		return nil, err
	}
	for _, producer := range producerTables {
		if producer.Standard == legacy {
			tables = append(tables, producer.TableName)
		}
	}
	sort.Strings(tables)
	return tables, nil
}
